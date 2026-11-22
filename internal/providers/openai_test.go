package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

func intPtr(n int) *int { return &n }

// fakeUpstream returns a test server and a pointer to the last request body it got.
func fakeUpstream(t *testing.T, handler func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *map[string]any) {
	t.Helper()
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		last = map[string]any{"_auth": r.Header.Get("Authorization")}
		if err := json.NewDecoder(r.Body).Decode(&last); err != nil {
			t.Errorf("decode request: %v", err)
		}
		handler(w, last)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestOpenAICompatChat(t *testing.T) {
	srv, last := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	})
	p := NewOpenAICompat("openai", srv.URL+"/v1", "sk-test", time.Minute, true)

	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:     "gpt-test",
		Messages:  []Message{{Role: "user", Content: "Hello"}},
		MaxTokens: intPtr(50),
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Hi there" || resp.FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v, want 7/3", resp.Usage)
	}
	body := *last
	if body["model"] != "gpt-test" || body["max_completion_tokens"] != float64(50) || body["max_tokens"] != nil {
		t.Errorf("OpenAI request body = %v; want model and max_completion_tokens only", body)
	}
}

func TestOllamaUsesMaxTokensAndNoAuth(t *testing.T) {
	srv, last := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	p := NewOpenAICompat("ollama", srv.URL+"/v1", "", time.Minute, false)
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "qwen", Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: intPtr(9)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage != nil {
		t.Errorf("usage = %+v, want nil when upstream sends none", resp.Usage)
	}
	body := *last
	if body["max_tokens"] != float64(9) || body["max_completion_tokens"] != nil || body["_auth"] != "" {
		t.Errorf("Ollama request = %v; want max_tokens and no Authorization header", body)
	}
}

func TestOpenAICompatStream(t *testing.T) {
	srv, last := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
	})
	p := NewOpenAICompat("mock", srv.URL+"/v1", "", time.Minute, false)

	var got []Chunk
	err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(c Chunk) error { got = append(got, c); return nil })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(got) != 4 || got[0].Content != "Hel" || got[1].Content != "lo" || got[2].FinishReason != "stop" {
		t.Fatalf("chunks = %+v", got)
	}
	if u := got[3].Usage; u == nil || u.PromptTokens != 5 || u.CompletionTokens != 2 {
		t.Errorf("usage chunk = %+v", got[3])
	}
	opts, _ := (*last)["stream_options"].(map[string]any)
	if (*last)["stream"] != true || opts["include_usage"] != true {
		t.Errorf("request = %v; want stream with include_usage", *last)
	}
}

func TestOpenAICompatStreamStopsWhenEmitFails(t *testing.T) {
	srv, _ := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\ndata: [DONE]\n\n")
	})
	p := NewOpenAICompat("mock", srv.URL+"/v1", "", time.Minute, false)
	clientGone := errors.New("client gone")
	calls := 0
	err := p.ChatStream(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { calls++; return clientGone })
	if !errors.Is(err, clientGone) || calls != 1 {
		t.Fatalf("err = %v, calls = %d; want the emit error after 1 call", err, calls)
	}
}

func TestOpenAICompatErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"openai style", 429, `{"error":{"message":"slow down","type":"rate_limit"}}`, 429, "slow down"},
		{"ollama style", 404, `{"error":"model not found"}`, 404, "model not found"},
		{"plain text", 503, "upstream down", 503, "upstream down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			p := NewOpenAICompat("x", srv.URL+"/v1", "", time.Minute, false)
			_, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
			var pe *Error
			if !errors.As(err, &pe) || pe.Status != tt.wantStatus || pe.Message != tt.wantMsg {
				t.Fatalf("err = %#v; want status %d and message %q", err, tt.wantStatus, tt.wantMsg)
			}
		})
	}
}

func TestStreamWithoutDoneIsAnError(t *testing.T) {
	srv, _ := fakeUpstream(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	})
	p := NewOpenAICompat("x", srv.URL+"/v1", "", time.Minute, false)
	err := p.ChatStream(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v; want *Error for a stream cut off before [DONE]", err)
	}
}

func TestConnectionRefusedHasStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens here any more
	p := NewOpenAICompat("x", url+"/v1", "", time.Minute, false)
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 0 {
		t.Fatalf("err = %v; want *Error with status 0", err)
	}
}

func TestNewChecksAPIKeyEnv(t *testing.T) {
	cfg := config.Provider{Type: config.TypeOpenAI, BaseURL: "https://example.com/v1", APIKeyEnv: "TEST_KEY", Timeout: time.Minute}
	if _, err := New("openai", cfg, func(string) string { return "" }); err == nil {
		t.Fatal("New succeeded with an empty API key env var")
	}
	p, err := New("openai", cfg, func(string) string { return "sk-x" })
	if err != nil || p.Name() != "openai" {
		t.Fatalf("New = %v, %v", p, err)
	}
}
