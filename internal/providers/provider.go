// Package providers talks to upstream LLM APIs. Each adapter translates one
// internal request/response shape to and from its provider's API, so the rest
// of the gateway does not care which API serves an alias.
package providers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Finish reasons, in OpenAI's words. Every adapter maps to these.
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishContentFilter = "content_filter"
	FinishToolCalls     = "tool_calls"
)

// Message is one chat message. Content is plain text.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the internal request shape (close to OpenAI's).
type ChatRequest struct {
	Model       string // the provider's own model name, set by the router
	Messages    []Message
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Stop        []string
}

// Usage is the token count a provider reports.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// ChatResponse is a complete (non-streamed) answer.
type ChatResponse struct {
	Content      string
	FinishReason string
	Usage        *Usage // nil when the provider did not report usage
}

// Chunk is one piece of a streamed answer: text, the finish reason, usage, or a mix.
type Chunk struct {
	Content      string
	FinishReason string
	Usage        *Usage
}

// Provider is one upstream LLM API.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// ChatStream calls emit for each chunk, in order, and returns when the
	// stream ends. If emit returns an error, the stream stops with that error.
	ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error
}

// Error is a failed upstream call. Status is the upstream HTTP status, or 0
// when no usable HTTP response came back (connection refused, timeout, bad body).
type Error struct {
	Provider string
	Status   int
	Message  string
	Err      error
}

func (e *Error) Error() string {
	msg := "provider " + e.Provider
	if e.Status != 0 {
		msg += fmt.Sprintf(": status %d", e.Status)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap lets errors.Is see the cause, for example context.DeadlineExceeded.
func (e *Error) Unwrap() error { return e.Err }

// Timeouts limit one upstream call. The time-to-first-token limit is applied
// by the router, the same way for every provider.
type Timeouts struct {
	Connect time.Duration // TCP connect and TLS handshake; 0 keeps Go's default (30s)
	Total   time.Duration // the whole call, including a long stream
}

// newHTTPClient returns an HTTP client for upstream calls. Go's default keeps
// only 2 idle connections per host, which forces new TCP and TLS handshakes
// under load; a gateway reuses many. A short connect timeout finds a dead host
// fast, so the router can fail over. The total timeout comes from the context.
func newHTTPClient(connectTimeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 64
	if connectTimeout > 0 {
		dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
		t.DialContext = dialer.DialContext
		t.TLSHandshakeTimeout = connectTimeout
	}
	return &http.Client{Transport: t}
}
