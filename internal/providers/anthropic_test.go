package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

func ptr[T any](v T) *T { return &v }

// toJSON returns the request exactly as the SDK would send it.
func toJSON(t *testing.T, params anthropic.MessageNewParams) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestToAnthropicParamsConvertsSystemAndDropsSampling(t *testing.T) {
	params, err := toAnthropicParams(ChatRequest{
		Model: "claude-opus-5-5",
		Messages: []Message{
			{Role: "system", Content: "Be brief."},
			{Role: "developer", Content: "Answer in English."},
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "Bye"},
		},
		Temperature: ptr(0.2),
		TopP:        ptr(0.9),
		Stop:        []string{"END"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	body := toJSON(t, params)

	system, _ := body["system"].([]any)
	if len(system) != 1 || system[0].(map[string]any)["text"] != "Be brief.\n\nAnswer in English." {
		t.Errorf("system = %v; want both system messages joined", body["system"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v; want 3 (system messages removed)", msgs)
	}
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Errorf("roles = %v", roles)
	}
	for _, field := range []string{"temperature", "top_p", "top_k"} {
		if _, sent := body[field]; sent {
			t.Errorf("%s was sent; API version 2026-10-21 rejects it", field)
		}
	}
	if body["max_tokens"] != float64(anthropicDefaultMaxTokens) {
		t.Errorf("max_tokens = %v; want default %d", body["max_tokens"], anthropicDefaultMaxTokens)
	}
	if stops, _ := body["stop_sequences"].([]any); len(stops) != 1 || stops[0] != "END" {
		t.Errorf("stop_sequences = %v", body["stop_sequences"])
	}
}

func TestToAnthropicParamsMaxTokens(t *testing.T) {
	user := []Message{{Role: "user", Content: "x"}}
	stream, _ := toAnthropicParams(ChatRequest{Model: "m", Messages: user}, true)
	if stream.MaxTokens != anthropicDefaultStreamMaxTokens {
		t.Errorf("stream default = %d, want %d", stream.MaxTokens, anthropicDefaultStreamMaxTokens)
	}
	set, _ := toAnthropicParams(ChatRequest{Model: "m", Messages: user, MaxTokens: ptr(300)}, true)
	if set.MaxTokens != 300 {
		t.Errorf("client max_tokens = %d, want 300", set.MaxTokens)
	}
}

func TestToAnthropicParamsRejectsBadInput(t *testing.T) {
	if _, err := toAnthropicParams(ChatRequest{Messages: []Message{{Role: "system", Content: "only system"}}}, false); err == nil {
		t.Error("want error when there is no user or assistant message")
	}
	if _, err := toAnthropicParams(ChatRequest{Messages: []Message{{Role: "tool", Content: "x"}}}, false); err == nil {
		t.Error("want error for an unsupported role")
	}
}

func TestMapStopReason(t *testing.T) {
	tests := map[string]string{
		"end_turn":                      FinishStop,
		"stop_sequence":                 FinishStop,
		"pause_turn":                    FinishStop,
		"max_tokens":                    FinishLength,
		"model_context_window_exceeded": FinishLength,
		"tool_use":                      FinishToolCalls,
		"refusal":                       FinishContentFilter,
	}
	for in, want := range tests {
		if got := mapStopReason(anthropic.StopReason(in)); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromAnthropicMessageSkipsThinking(t *testing.T) {
	var msg anthropic.Message
	err := json.Unmarshal([]byte(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
		"content":[
			{"type":"thinking","thinking":"","signature":"sig"},
			{"type":"text","text":"Paris."}
		],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":0}
	}`), &msg)
	if err != nil {
		t.Fatal(err)
	}
	resp := fromAnthropicMessage(&msg)
	if resp.Content != "Paris." || resp.FinishReason != FinishStop {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.PromptTokens != 15 || resp.Usage.CompletionTokens != 20 {
		t.Errorf("usage = %+v; want prompt 15 (10 + 5 cached), completion 20", resp.Usage)
	}
}

const anthropicStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

// fakeAnthropic is a test server standing in for api.anthropic.com.
func fakeAnthropic(t *testing.T, status int, body string, gotRequest *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		if gotRequest != nil {
			_ = json.NewDecoder(r.Body).Decode(gotRequest)
		}
		if strings.Contains(body, "event:") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnthropicChatStream(t *testing.T) {
	var sent map[string]any
	srv := fakeAnthropic(t, 200, anthropicStream, &sent)
	p := NewAnthropic("anthropic", srv.URL, "test-key", Timeouts{Total: time.Minute})

	var chunks []Chunk
	err := p.ChatStream(context.Background(), ChatRequest{
		Model:       "claude-opus-5-5",
		Messages:    []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}},
		Temperature: ptr(0.0),
	}, func(c Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(chunks) != 3 || chunks[0].Content != "Hello" || chunks[1].Content != " world" {
		t.Fatalf("chunks = %+v; want two text chunks and a final chunk", chunks)
	}
	last := chunks[2]
	if last.FinishReason != FinishStop || last.Usage == nil || last.Usage.PromptTokens != 12 || last.Usage.CompletionTokens != 42 {
		t.Errorf("final chunk = %+v; want stop with usage 12/42", last)
	}
	if _, ok := sent["temperature"]; ok {
		t.Error("temperature reached the Anthropic API")
	}
	if sent["stream"] != true || sent["max_tokens"] != float64(anthropicDefaultStreamMaxTokens) {
		t.Errorf("request = %v", sent)
	}
}

func TestAnthropicErrorKeepsStatus(t *testing.T) {
	srv := fakeAnthropic(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, nil)
	p := NewAnthropic("anthropic", srv.URL, "test-key", Timeouts{Total: time.Minute})
	_, err := p.Chat(context.Background(), ChatRequest{Model: "claude-opus-5-5", Messages: []Message{{Role: "user", Content: "hi"}}})
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 529 {
		t.Fatalf("err = %v; want *Error with status 529 (and no SDK retries)", err)
	}
}
