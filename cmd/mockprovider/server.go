package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Settings control how the mock behaves. Change them at runtime with
// PATCH /admin/settings, for example {"outage": true}.
type Settings struct {
	LatencyMS      int     `json:"latency_ms"`      // delay before any response (network or queue time)
	TTFTMS         int     `json:"ttft_ms"`         // extra delay before the first token
	TokensPerSec   float64 `json:"tokens_per_sec"`  // generation speed; 0 means no delay
	ResponseTokens int     `json:"response_tokens"` // tokens per answer (max_tokens can lower it)
	ErrorRate      float64 `json:"error_rate"`      // share of requests that fail, 0 to 1
	ErrorStatus    int     `json:"error_status"`    // HTTP status for injected errors, e.g. 500 or 429
	Outage         bool    `json:"outage"`          // when true, every request fails with 503
	OmitUsage      bool    `json:"omit_usage"`      // never report token usage
}

func defaultSettings() Settings {
	return Settings{TTFTMS: 50, TokensPerSec: 100, ResponseTokens: 20, ErrorStatus: 500}
}

func (s Settings) validate() error {
	switch {
	case s.LatencyMS < 0 || s.TTFTMS < 0 || s.TokensPerSec < 0:
		return errors.New("latency_ms, ttft_ms and tokens_per_sec must not be negative")
	case s.ResponseTokens < 1 || s.ResponseTokens > 100_000:
		return errors.New("response_tokens must be between 1 and 100000")
	case s.ErrorRate < 0 || s.ErrorRate > 1:
		return errors.New("error_rate must be between 0 and 1")
	case s.ErrorStatus < 400 || s.ErrorStatus > 599:
		return errors.New("error_status must be a 4xx or 5xx code")
	}
	return nil
}

type server struct {
	mu       sync.RWMutex
	settings Settings
	random   func() float64 // replaced in tests
}

func newServer() *server {
	return &server{settings: defaultSettings(), random: rand.Float64}
}

func (s *server) current() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /admin/settings", s.getSettings)
	mux.HandleFunc("PATCH /admin/settings", s.patchSettings)
	mux.HandleFunc("POST /admin/reset", s.reset)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
	Stream        bool `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	MaxTokens           *int `json:"max_tokens"`
	MaxCompletionTokens *int `json:"max_completion_tokens"`
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	cfg := s.current()
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if !wait(r.Context(), ms(cfg.LatencyMS)) {
		return
	}
	if cfg.Outage {
		writeError(w, http.StatusServiceUnavailable, "mock outage switch is on")
		return
	}
	if cfg.ErrorRate > 0 && s.random() < cfg.ErrorRate {
		if cfg.ErrorStatus == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		writeError(w, cfg.ErrorStatus, "mock injected error")
		return
	}

	n, finish := cfg.ResponseTokens, "stop"
	limit := req.MaxCompletionTokens
	if limit == nil {
		limit = req.MaxTokens
	}
	if limit != nil && *limit < n {
		n, finish = max(*limit, 0), "length"
	}
	tokens := makeTokens(n)

	promptChars := 0
	for _, m := range req.Messages {
		promptChars += utf8.RuneCountInString(m.Content)
	}
	usage := &usageJSON{PromptTokens: (promptChars + 3) / 4, CompletionTokens: n}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if cfg.OmitUsage {
		usage = nil
	}

	id := "mock-" + randomHex(8)
	if req.Stream {
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		s.stream(w, r, cfg, id, req.Model, tokens, finish, usage, includeUsage)
		return
	}

	// A non-streaming answer takes as long as generating every token would.
	if !wait(r.Context(), ms(cfg.TTFTMS)+time.Duration(n)*tokenInterval(cfg.TokensPerSec)) {
		return
	}
	writeJSON(w, http.StatusOK, completion{
		ID: id, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []completionChoice{{Message: message{Role: "assistant", Content: strings.Join(tokens, "")}, FinishReason: finish}},
		Usage:   usage,
	})
}

func (s *server) stream(w http.ResponseWriter, r *http.Request, cfg Settings, id, model string,
	tokens []string, finish string, usage *usageJSON, includeUsage bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	// Like real providers, send headers at once and the first token later.
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	created := time.Now().Unix()
	send := func(c streamChunk) bool {
		c.ID, c.Object, c.Created, c.Model = id, "chat.completion.chunk", created, model
		b, _ := json.Marshal(c)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	one := func(d delta, finishReason *string) streamChunk {
		return streamChunk{Choices: []streamChoice{{Delta: d, FinishReason: finishReason}}}
	}

	if !wait(r.Context(), ms(cfg.TTFTMS)) || !send(one(delta{Role: "assistant"}, nil)) {
		return
	}
	interval := tokenInterval(cfg.TokensPerSec)
	for i, tok := range tokens {
		if i > 0 && !wait(r.Context(), interval) {
			return
		}
		if !send(one(delta{Content: tok}, nil)) {
			return
		}
	}
	if !send(one(delta{}, &finish)) {
		return
	}
	if includeUsage && usage != nil && !send(streamChunk{Choices: []streamChoice{}, Usage: usage}) {
		return
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
}

func (s *server) models(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   []map[string]any{{"id": "mock-1", "object": "model", "created": 0, "owned_by": "mock"}},
	})
}

func (s *server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.current())
}

// patchSettings changes only the fields in the body, for example {"outage":true}.
func (s *server) patchSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields() // catch typos such as {"outtage": true}
	if err := dec.Decode(&next); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
		return
	}
	if err := next.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.settings = next
	writeJSON(w, http.StatusOK, next)
}

func (s *server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.settings = defaultSettings()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.current())
}

// --- response shapes (OpenAI Chat Completions) ---

type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionChoice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usageJSON         `json:"usage,omitempty"`
}

type delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type streamChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usageJSON     `json:"usage,omitempty"`
}

// --- helpers ---

var words = strings.Fields("The quick brown fox jumps over the lazy dog while the mock model streams tokens at a steady pace")

// makeTokens returns n fake tokens; each word counts as one token.
func makeTokens(n int) []string {
	out := make([]string, n)
	for i := range out {
		w := words[i%len(words)]
		if i > 0 {
			w = " " + w
		}
		out[i] = w
	}
	return out
}

// wait pauses for d. It returns false early if the client goes away.
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func tokenInterval(tokensPerSec float64) time.Duration {
	if tokensPerSec <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / tokensPerSec)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "mock_error"}})
}
