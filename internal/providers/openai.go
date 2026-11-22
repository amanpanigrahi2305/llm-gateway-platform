package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat speaks the OpenAI Chat Completions API. OpenAI, Ollama (on its
// /v1 path) and the mock provider all use this protocol, so one adapter serves
// all three; only the base URL and the API key differ.
type OpenAICompat struct {
	name    string
	baseURL string
	apiKey  string // empty for Ollama and the mock
	timeout time.Duration
	// OpenAI's own API wants max_completion_tokens (newer models reject
	// max_tokens); Ollama and the mock read max_tokens.
	useMaxCompletionTokens bool
	client                 *http.Client
}

// NewOpenAICompat returns an adapter for an OpenAI-compatible API.
func NewOpenAICompat(name, baseURL, apiKey string, timeout time.Duration, useMaxCompletionTokens bool) *OpenAICompat {
	return &OpenAICompat{
		name:                   name,
		baseURL:                strings.TrimRight(baseURL, "/"),
		apiKey:                 apiKey,
		timeout:                timeout,
		useMaxCompletionTokens: useMaxCompletionTokens,
		client:                 newHTTPClient(),
	}
}

// Name returns the provider name from the config.
func (p *OpenAICompat) Name() string { return p.name }

type oaRequest struct {
	Model               string        `json:"model"`
	Messages            []Message     `json:"messages"`
	Temperature         *float64      `json:"temperature,omitempty"`
	TopP                *float64      `json:"top_p,omitempty"`
	MaxTokens           *int          `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int          `json:"max_completion_tokens,omitempty"`
	Stop                []string      `json:"stop,omitempty"`
	Stream              bool          `json:"stream,omitempty"`
	StreamOptions       *oaStreamOpts `json:"stream_options,omitempty"`
}

type oaStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u *oaUsage) toUsage() *Usage {
	if u == nil {
		return nil
	}
	return &Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens}
}

func (p *OpenAICompat) newRequest(req ChatRequest, stream bool) oaRequest {
	body := oaRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      stream,
	}
	if p.useMaxCompletionTokens {
		body.MaxCompletionTokens = req.MaxTokens
	} else {
		body.MaxTokens = req.MaxTokens
	}
	if stream {
		// Ask for token usage in the last chunk; without it we must estimate.
		body.StreamOptions = &oaStreamOpts{IncludeUsage: true}
	}
	return body
}

// Chat sends one request and waits for the whole answer.
func (p *OpenAICompat) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resp, err := p.post(ctx, p.newRequest(req, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *oaUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &Error{Provider: p.name, Message: "invalid response body", Err: err}
	}
	if len(out.Choices) == 0 {
		return nil, &Error{Provider: p.name, Message: "response has no choices"}
	}
	c := out.Choices[0]
	return &ChatResponse{Content: c.Message.Content, FinishReason: c.FinishReason, Usage: out.Usage.toUsage()}, nil
}

// ChatStream sends one streaming request and emits each chunk as it arrives.
func (p *OpenAICompat) ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resp, err := p.post(ctx, p.newRequest(req, true))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var emitErr error
	sawDone := false
	err = readSSE(resp.Body, func(_, data string) error {
		if data == "[DONE]" {
			sawDone = true
			return errStopSSE
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *oaUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return &Error{Provider: p.name, Message: "invalid stream chunk", Err: err}
		}
		if c.Error != nil {
			return &Error{Provider: p.name, Message: c.Error.Message}
		}
		var out Chunk
		if len(c.Choices) > 0 {
			out.Content = c.Choices[0].Delta.Content
			if fr := c.Choices[0].FinishReason; fr != nil {
				out.FinishReason = *fr
			}
		}
		out.Usage = c.Usage.toUsage()
		if out.Content == "" && out.FinishReason == "" && out.Usage == nil {
			return nil // e.g. the first chunk, which only carries the role
		}
		if err := emit(out); err != nil {
			emitErr = err
			return err
		}
		return nil
	})
	switch {
	case emitErr != nil:
		return emitErr // the client went away; pass the error through unchanged
	case err != nil:
		var pe *Error
		if errors.As(err, &pe) {
			return pe
		}
		return &Error{Provider: p.name, Message: "stream read failed", Err: err}
	case !sawDone:
		return &Error{Provider: p.name, Message: "stream ended before [DONE]"}
	}
	return nil
}

func (p *OpenAICompat) post(ctx context.Context, body oaRequest) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &Error{Provider: p.name, Message: "request failed", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, &Error{Provider: p.name, Status: resp.StatusCode, Message: errorMessage(resp.Body)}
	}
	return resp, nil
}

// errorMessage pulls the message out of an error body. OpenAI sends
// {"error":{"message":"..."}}; Ollama sometimes sends {"error":"..."}.
func errorMessage(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && len(body.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if json.Unmarshal(body.Error, &s) == nil && s != "" {
			return s
		}
	}
	return strings.TrimSpace(string(raw))
}
