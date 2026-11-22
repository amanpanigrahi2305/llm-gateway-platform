package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Default max_tokens when the client sends none. Anthropic requires the field.
// Current Claude models always think, and thinking tokens count toward
// max_tokens, so a small default would cut answers short. It is a ceiling,
// not a charge: you pay only for tokens actually generated.
const (
	anthropicDefaultMaxTokens       = 16000
	anthropicDefaultStreamMaxTokens = 64000
)

// Anthropic calls the Claude Messages API through the official Go SDK.
type Anthropic struct {
	name    string
	timeout time.Duration
	client  anthropic.Client
}

// NewAnthropic returns an adapter for the Anthropic Messages API.
func NewAnthropic(name, baseURL, apiKey string, timeout time.Duration) *Anthropic {
	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0), // the gateway owns retries and failover (Phase 2)
		option.WithHTTPClient(newHTTPClient()),
	)
	return &Anthropic{name: name, timeout: timeout, client: client}
}

// Name returns the provider name from the config.
func (p *Anthropic) Name() string { return p.name }

// Chat sends one request and waits for the whole answer.
func (p *Anthropic) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	params, err := toAnthropicParams(req, false)
	if err != nil {
		return nil, &Error{Provider: p.name, Status: 400, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, p.wrapErr(err)
	}
	return fromAnthropicMessage(msg), nil
}

// ChatStream sends one streaming request. Text deltas are emitted as they
// arrive; thinking blocks are skipped. The finish reason and usage come last.
func (p *Anthropic) ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error {
	params, err := toAnthropicParams(req, true)
	if err != nil {
		return &Error{Provider: p.name, Status: 400, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg anthropic.Message // collects stop reason and usage across events
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return &Error{Provider: p.name, Message: "invalid stream event", Err: err}
		}
		delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent)
		if !ok {
			continue
		}
		if text, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok && text.Text != "" {
			if err := emit(Chunk{Content: text.Text}); err != nil {
				return err // the client went away
			}
		}
	}
	if err := stream.Err(); err != nil {
		return p.wrapErr(err)
	}
	if msg.StopReason == "" {
		return &Error{Provider: p.name, Message: "stream ended before message_stop"}
	}
	return emit(Chunk{FinishReason: mapStopReason(msg.StopReason), Usage: anthropicUsage(msg.Usage)})
}

// toAnthropicParams converts the internal (OpenAI-shaped) request:
//   - "system" and "developer" messages become the top-level system prompt;
//   - temperature and top_p are dropped, because API version 2026-10-21 rejects
//     them on every model (the current models accept only the defaults anyway);
//   - max_tokens gets a default, because Anthropic requires it.
func toAnthropicParams(req ChatRequest, stream bool) (anthropic.MessageNewParams, error) {
	var system []string
	var msgs []anthropic.MessageParam
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			system = append(system, m.Content)
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			return anthropic.MessageNewParams{}, fmt.Errorf("message role %q is not supported for Anthropic", m.Role)
		}
	}
	if len(msgs) == 0 {
		return anthropic.MessageNewParams{}, errors.New("at least one user or assistant message is required")
	}

	maxTokens := anthropicDefaultMaxTokens
	if stream {
		maxTokens = anthropicDefaultStreamMaxTokens
	}
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	params := anthropic.MessageNewParams{
		Model:         req.Model,
		MaxTokens:     int64(maxTokens),
		Messages:      msgs,
		StopSequences: req.Stop,
	}
	if len(system) > 0 {
		params.System = []anthropic.TextBlockParam{{Text: strings.Join(system, "\n\n")}}
	}
	return params, nil
}

// fromAnthropicMessage keeps only the text blocks; thinking blocks are not
// part of the OpenAI response shape.
func fromAnthropicMessage(msg *anthropic.Message) *ChatResponse {
	var text strings.Builder
	for _, block := range msg.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return &ChatResponse{
		Content:      text.String(),
		FinishReason: mapStopReason(msg.StopReason),
		Usage:        anthropicUsage(msg.Usage),
	}
}

// mapStopReason maps Anthropic stop reasons to OpenAI finish reasons.
func mapStopReason(r anthropic.StopReason) string {
	switch string(r) {
	case "max_tokens", "model_context_window_exceeded":
		return FinishLength
	case "tool_use":
		return FinishToolCalls
	case "refusal":
		return FinishContentFilter
	default: // end_turn, stop_sequence, pause_turn
		return FinishStop
	}
}

// anthropicUsage counts cached input tokens as prompt tokens, like OpenAI does.
// Cache reads are cheaper than normal input, so cost is slightly overestimated.
func anthropicUsage(u anthropic.Usage) *Usage {
	return &Usage{
		PromptTokens:     int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens),
		CompletionTokens: int(u.OutputTokens),
	}
}

func (p *Anthropic) wrapErr(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return &Error{Provider: p.name, Status: apiErr.StatusCode, Message: apiErr.Error(), Err: err}
	}
	return &Error{Provider: p.name, Message: "request failed", Err: err}
}
