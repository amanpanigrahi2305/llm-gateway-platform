package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// fakeBedrock records the request and returns scripted answers. No AWS calls.
type fakeBedrock struct {
	converseIn *bedrockruntime.ConverseInput
	streamIn   *bedrockruntime.ConverseStreamInput
	out        *bedrockruntime.ConverseOutput
	events     []types.ConverseStreamOutput
	streamErr  error // returned by the stream after its events
	err        error // returned by the call itself
	calls      int
}

func (f *fakeBedrock) Converse(_ context.Context, in *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
	f.calls++
	f.converseIn = in
	return f.out, f.err
}

func (f *fakeBedrock) ConverseStream(_ context.Context, in *bedrockruntime.ConverseStreamInput) (bedrockStream, error) {
	f.calls++
	f.streamIn = in
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan types.ConverseStreamOutput, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return &fakeStream{ch: ch, err: f.streamErr}, nil
}

type fakeStream struct {
	ch  chan types.ConverseStreamOutput
	err error
}

func (s *fakeStream) Events() <-chan types.ConverseStreamOutput { return s.ch }
func (s *fakeStream) Close() error                              { return nil }
func (s *fakeStream) Err() error                                { return s.err }

func newTestBedrock(f *fakeBedrock) *Bedrock {
	return &Bedrock{name: "bedrock", timeout: 0x7fffffff, api: f}
}

func text(s string) types.ContentBlock { return &types.ContentBlockMemberText{Value: s} }

func TestBedrockChatConvertsRequestAndResponse(t *testing.T) {
	f := &fakeBedrock{out: &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: []types.ContentBlock{text("Hello "), &types.ContentBlockMemberReasoningContent{}, text("world")},
		}},
		StopReason: types.StopReasonMaxTokens,
		Usage: &types.TokenUsage{InputTokens: aws.Int32(10), CacheReadInputTokens: aws.Int32(5),
			CacheWriteInputTokens: aws.Int32(2), OutputTokens: aws.Int32(4), TotalTokens: aws.Int32(21)},
	}}
	temp, maxTokens := 0.2, 50
	resp, err := newTestBedrock(f).Chat(t.Context(), ChatRequest{
		Model: "amazon.nova-micro-v1:0",
		Messages: []Message{{Role: "system", Content: "Be short."}, {Role: "developer", Content: "No emoji."},
			{Role: "user", Content: "Hi"}, {Role: "assistant", Content: "Hello"}, {Role: "user", Content: "Again"}},
		Temperature: &temp, MaxTokens: &maxTokens, Stop: []string{"END"},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := f.converseIn
	if aws.ToString(in.ModelId) != "amazon.nova-micro-v1:0" || len(in.System) != 2 || len(in.Messages) != 3 {
		t.Fatalf("model %q, %d system blocks, %d messages", aws.ToString(in.ModelId), len(in.System), len(in.Messages))
	}
	if in.Messages[1].Role != types.ConversationRoleAssistant {
		t.Errorf("second message role = %s", in.Messages[1].Role)
	}
	ic := in.InferenceConfig
	if ic == nil || aws.ToInt32(ic.MaxTokens) != 50 || aws.ToFloat32(ic.Temperature) != 0.2 || ic.TopP != nil || ic.StopSequences[0] != "END" {
		t.Errorf("inference config = %+v", ic)
	}
	if resp.Content != "Hello world" || resp.FinishReason != FinishLength {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 17 || resp.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v; want prompt 17 (10 + cache 5 + 2), completion 4", resp.Usage)
	}
}

func TestBedrockSendsNoInferenceConfigWhenUnset(t *testing.T) {
	f := &fakeBedrock{out: &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{}, StopReason: types.StopReasonEndTurn}}
	if _, err := newTestBedrock(f).Chat(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "Hi"}}}); err != nil {
		t.Fatal(err)
	}
	if f.converseIn.InferenceConfig != nil {
		t.Errorf("inference config = %+v; want nil so the model's defaults apply", f.converseIn.InferenceConfig)
	}
}

func TestBedrockRejectsUnknownRoleWithoutCalling(t *testing.T) {
	f := &fakeBedrock{}
	_, err := newTestBedrock(f).Chat(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: "tool", Content: "x"}}})
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 400 || f.calls != 0 {
		t.Fatalf("err = %v, calls = %d; want a 400 and no call", err, f.calls)
	}
}

func TestBedrockStream(t *testing.T) {
	f := &fakeBedrock{events: []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberMessageStart{},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{Delta: &types.ContentBlockDeltaMemberText{Value: "Hel"}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{Delta: &types.ContentBlockDeltaMemberReasoningContent{}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{Delta: &types.ContentBlockDeltaMemberText{Value: "lo"}}},
		&types.ConverseStreamOutputMemberContentBlockStop{},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{InputTokens: aws.Int32(3), OutputTokens: aws.Int32(2), TotalTokens: aws.Int32(5)}}},
	}}
	var chunks []Chunk
	err := newTestBedrock(f).ChatStream(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "Hi"}}},
		func(c Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Content != "Hel" || chunks[1].Content != "lo" {
		t.Fatalf("chunks = %+v; want two text chunks and a final one", chunks)
	}
	last := chunks[2]
	if last.FinishReason != FinishStop || last.Usage == nil || last.Usage.PromptTokens != 3 || last.Usage.CompletionTokens != 2 {
		t.Errorf("final chunk = %+v", last)
	}
}

func TestBedrockStreamWithoutStopIsAnError(t *testing.T) {
	f := &fakeBedrock{events: []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{Delta: &types.ContentBlockDeltaMemberText{Value: "cut"}}},
	}}
	err := newTestBedrock(f).ChatStream(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "Hi"}}},
		func(Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "messageStop") {
		t.Fatalf("err = %v; want 'stream ended before messageStop'", err)
	}
}

func TestBedrockErrorStatus(t *testing.T) {
	httpErr := func(code int) error {
		return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
			Err:      &smithy.GenericAPIError{Code: "ServiceUnavailableException", Message: "busy"},
		}}
	}
	tests := []struct {
		name   string
		call   error // error of the call
		stream error // error inside the stream
		want   int
	}{
		{"HTTP status wins", httpErr(503), nil, 503},
		{"throttling without a status", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}, nil, 429},
		{"error in the middle of a stream", nil, &smithy.GenericAPIError{Code: "ModelStreamErrorException"}, 500},
		{"network error", errors.New("connection reset"), nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBedrock{err: tc.call, streamErr: tc.stream}
			err := newTestBedrock(f).ChatStream(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "Hi"}}},
				func(Chunk) error { return nil })
			var pe *Error
			if !errors.As(err, &pe) || pe.Status != tc.want {
				t.Fatalf("err = %v; want status %d", err, tc.want)
			}
		})
	}
}

// Creating the adapter reads local AWS settings only; it calls no AWS API.
func TestNewBedrockCallsNoAPI(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	p, err := NewBedrock("bedrock", "us-east-1", "", Timeouts{})
	if err != nil || p.Name() != "bedrock" {
		t.Fatalf("NewBedrock: %v", err)
	}
}
