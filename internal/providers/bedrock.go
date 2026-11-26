package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// bedrockStream is the event stream of one ConverseStream call.
type bedrockStream interface {
	Events() <-chan types.ConverseStreamOutput
	Close() error
	Err() error
}

// bedrockAPI is the part of the Bedrock Runtime client the adapter uses.
// Tests replace it with a fake, so no test ever calls AWS.
type bedrockAPI interface {
	Converse(ctx context.Context, in *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(ctx context.Context, in *bedrockruntime.ConverseStreamInput) (bedrockStream, error)
}

// sdkBedrock adapts the SDK client to bedrockAPI.
type sdkBedrock struct{ client *bedrockruntime.Client }

func (s sdkBedrock) Converse(ctx context.Context, in *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
	return s.client.Converse(ctx, in)
}

func (s sdkBedrock) ConverseStream(ctx context.Context, in *bedrockruntime.ConverseStreamInput) (bedrockStream, error) {
	out, err := s.client.ConverseStream(ctx, in)
	if err != nil {
		return nil, err
	}
	return out.GetStream(), nil
}

// Bedrock calls the Amazon Bedrock Converse API, one API for every model on
// Bedrock. Credentials come from the AWS default chain: EKS Pod Identity on
// EKS, environment variables or ~/.aws on a laptop. There is no API key.
type Bedrock struct {
	name    string
	timeout time.Duration
	api     bedrockAPI
}

// NewBedrock returns an adapter for the Bedrock Converse API in region.
// baseURL is optional (a custom endpoint); empty uses the regional endpoint
// (through the VPC endpoint when one exists with private DNS).
func NewBedrock(name, region, baseURL string, t Timeouts) (*Bedrock, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(newHTTPClient(t.Connect)),
		awsconfig.WithRetryMaxAttempts(1), // the gateway's router owns retries and failover
	)
	if err != nil {
		return nil, fmt.Errorf("provider %s: AWS config: %w", name, err)
	}
	client := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		if baseURL != "" {
			o.BaseEndpoint = aws.String(baseURL)
		}
	})
	return &Bedrock{name: name, timeout: t.Total, api: sdkBedrock{client}}, nil
}

// Name returns the provider name from the config.
func (p *Bedrock) Name() string { return p.name }

// Chat sends one Converse request and waits for the whole answer.
func (p *Bedrock) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	in, err := toConverse(req)
	if err != nil {
		return nil, &Error{Provider: p.name, Status: 400, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	out, err := p.api.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: in.model, Messages: in.messages, System: in.system, InferenceConfig: in.inference,
	})
	if err != nil {
		return nil, p.wrapErr(err)
	}
	var text strings.Builder
	if msg, ok := out.Output.(*types.ConverseOutputMemberMessage); ok {
		for _, block := range msg.Value.Content {
			if t, ok := block.(*types.ContentBlockMemberText); ok {
				text.WriteString(t.Value)
			}
		}
	}
	return &ChatResponse{Content: text.String(), FinishReason: mapBedrockStop(out.StopReason), Usage: bedrockUsage(out.Usage)}, nil
}

// ChatStream sends one ConverseStream request. Text deltas are emitted as they
// arrive; reasoning deltas are skipped. The finish reason and usage come last.
func (p *Bedrock) ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error {
	in, err := toConverse(req)
	if err != nil {
		return &Error{Provider: p.name, Status: 400, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stream, err := p.api.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: in.model, Messages: in.messages, System: in.system, InferenceConfig: in.inference,
	})
	if err != nil {
		return p.wrapErr(err)
	}
	defer stream.Close()

	var stop types.StopReason
	var usage *Usage
	for event := range stream.Events() {
		switch e := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if t, ok := e.Value.Delta.(*types.ContentBlockDeltaMemberText); ok && t.Value != "" {
				if err := emit(Chunk{Content: t.Value}); err != nil {
					return err // the client went away
				}
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stop = e.Value.StopReason
		case *types.ConverseStreamOutputMemberMetadata:
			usage = bedrockUsage(e.Value.Usage)
		}
	}
	if err := stream.Err(); err != nil {
		return p.wrapErr(err)
	}
	if stop == "" {
		return &Error{Provider: p.name, Message: "stream ended before messageStop"}
	}
	return emit(Chunk{FinishReason: mapBedrockStop(stop), Usage: usage})
}

type converseInput struct {
	model     *string
	messages  []types.Message
	system    []types.SystemContentBlock
	inference *types.InferenceConfiguration
}

// toConverse converts the internal (OpenAI-shaped) request: "system" and
// "developer" messages become system blocks; sampling settings go to the
// inference configuration only when the client set them.
func toConverse(req ChatRequest) (converseInput, error) {
	in := converseInput{model: aws.String(req.Model)}
	for _, m := range req.Messages {
		text := &types.ContentBlockMemberText{Value: m.Content}
		switch m.Role {
		case "system", "developer":
			in.system = append(in.system, &types.SystemContentBlockMemberText{Value: m.Content})
		case "user":
			in.messages = append(in.messages, types.Message{Role: types.ConversationRoleUser, Content: []types.ContentBlock{text}})
		case "assistant":
			in.messages = append(in.messages, types.Message{Role: types.ConversationRoleAssistant, Content: []types.ContentBlock{text}})
		default:
			return converseInput{}, fmt.Errorf("message role %q is not supported for Bedrock", m.Role)
		}
	}
	if len(in.messages) == 0 {
		return converseInput{}, errors.New("at least one user or assistant message is required")
	}
	inf := &types.InferenceConfiguration{StopSequences: req.Stop}
	if req.MaxTokens != nil {
		inf.MaxTokens = aws.Int32(int32(*req.MaxTokens))
	}
	if req.Temperature != nil {
		inf.Temperature = aws.Float32(float32(*req.Temperature))
	}
	if req.TopP != nil {
		inf.TopP = aws.Float32(float32(*req.TopP))
	}
	if inf.MaxTokens != nil || inf.Temperature != nil || inf.TopP != nil || len(inf.StopSequences) > 0 {
		in.inference = inf
	}
	return in, nil
}

// mapBedrockStop maps Bedrock stop reasons to OpenAI finish reasons.
func mapBedrockStop(r types.StopReason) string {
	switch r {
	case types.StopReasonMaxTokens, types.StopReasonModelContextWindowExceeded:
		return FinishLength
	case types.StopReasonToolUse:
		return FinishToolCalls
	case types.StopReasonGuardrailIntervened, types.StopReasonContentFiltered:
		return FinishContentFilter
	default: // end_turn, stop_sequence, malformed output
		return FinishStop
	}
}

// bedrockUsage counts cache reads and writes as prompt tokens, like OpenAI does.
func bedrockUsage(u *types.TokenUsage) *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     int(aws.ToInt32(u.InputTokens) + aws.ToInt32(u.CacheReadInputTokens) + aws.ToInt32(u.CacheWriteInputTokens)),
		CompletionTokens: int(aws.ToInt32(u.OutputTokens)),
	}
}

// Error codes that can arrive inside a stream, without an HTTP status. They get
// the status Bedrock uses for them, so the router classifies them the same way.
var bedrockCodeStatus = map[string]int{
	"ThrottlingException":           429,
	"ServiceQuotaExceededException": 429,
	"ModelNotReadyException":        429,
	"ServiceUnavailableException":   503,
	"InternalServerException":       500,
	"ModelStreamErrorException":     500,
	"ModelTimeoutException":         408,
	"ValidationException":           400,
	"AccessDeniedException":         403,
	"ResourceNotFoundException":     404,
}

func (p *Bedrock) wrapErr(err error) error {
	status := 0
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		status = re.HTTPStatusCode()
	}
	msg := "request failed"
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		msg = apiErr.ErrorCode() + ": " + apiErr.ErrorMessage()
		if status == 0 {
			status = bedrockCodeStatus[apiErr.ErrorCode()]
		}
	}
	return &Error{Provider: p.name, Status: status, Message: msg, Err: err}
}
