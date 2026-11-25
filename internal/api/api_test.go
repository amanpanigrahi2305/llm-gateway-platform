package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/auth"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/cache"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/limits"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/usage"
)

const (
	devKey     = "sk-gw-dev"
	limitedKey = "sk-gw-limited"
	tightKey   = "sk-gw-tight"  // 2 requests per minute
	budgetKey  = "sk-gw-budget" // $0.00003 per day
)

// fakeProvider returns canned answers and records the request it got.
type fakeProvider struct {
	resp   *providers.ChatResponse
	chunks []providers.Chunk
	err    error // returned before any chunk
	midErr error // returned after the chunks
	got    providers.ChatRequest
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, req providers.ChatRequest, emit func(providers.Chunk) error) error {
	f.got = req
	if f.err != nil {
		return f.err
	}
	for _, c := range f.chunks {
		if err := emit(c); err != nil {
			return err
		}
	}
	return f.midErr
}

type testAPI struct {
	srv  *Server
	h    http.Handler
	logs *bytes.Buffer
}

func newTestAPI(t *testing.T, fp *fakeProvider) testAPI {
	t.Helper()
	return newTestAPIWithStore(t, fp, limits.NewMemoryStore(), true)
}

func newTestAPIWithStore(t *testing.T, fp *fakeProvider, store limits.Store, failOpen bool) testAPI {
	t.Helper()
	rt, err := router.New(
		map[string][]string{"fast": {"fake/model-x"}, "other": {"fake/model-y"}},
		map[string]providers.Provider{"fake": fp},
		router.Options{}, // one try, no retries: retries are tested in the router package
	)
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewStore([]config.Key{
		{ID: "dev", Hash: auth.Hash(devKey), Owner: "aman", Team: "platform", Aliases: []string{"*"}, Policy: "default"},
		{ID: "limited", Hash: auth.Hash(limitedKey), Aliases: []string{"other"}, Policy: "default"},
		{ID: "tight", Hash: auth.Hash(tightKey), Aliases: []string{"*"}, Policy: "tight"},
		{ID: "budget", Hash: auth.Hash(budgetKey), Aliases: []string{"*"}, Policy: "budget"},
	})
	policies := map[string]config.Policy{
		"default": {},
		"tight":   {RequestsPerMinute: 2},
		"budget":  {DailyUSD: 0.00003},
	}
	prices := usage.PriceTable{
		"fake/model-x": {InputPerMillion: 1, OutputPerMillion: 2},
		"fake/model-y": {InputPerMillion: 0, OutputPerMillion: 0},
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	srv := New(Deps{Router: rt, Keys: keys, Prices: prices, Limiter: limits.New(store, policies, failOpen, log),
		Cache: cache.New(time.Minute, 100), Log: log})
	srv.SetReady(true)
	return testAPI{srv: srv, h: srv.Handler(), logs: &logs}
}

func (a testAPI) call(method, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rr := httptest.NewRecorder()
	a.h.ServeHTTP(rr, req)
	return rr
}

// lastRecord returns the last usage record written to the log.
func (a testAPI) lastRecord(t *testing.T) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(a.logs.String()), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	return m
}

func sseData(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, d)
		}
	}
	return out
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	return m
}

// get walks a decoded JSON value: get(m, "choices", 0, "delta", "content").
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			s, _ := v.([]any)
			if k >= len(s) {
				return nil
			}
			v = s[k]
		}
	}
	return v
}

func TestChatCompletion(t *testing.T) {
	fp := &fakeProvider{resp: &providers.ChatResponse{Content: "Hello!", FinishReason: "stop",
		Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 5}}}
	a := newTestAPI(t, fp)

	rr := a.call("POST", "/v1/chat/completions", devKey,
		`{"model":"fast","messages":[{"role":"user","content":"my secret prompt"}],"temperature":0.5,"max_tokens":20}`)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	body := decodeJSON(t, rr.Body.String())
	if get(body, "object") != "chat.completion" || get(body, "model") != "fast" ||
		get(body, "choices", 0, "message", "content") != "Hello!" || get(body, "usage", "total_tokens") != float64(15) {
		t.Errorf("body = %s", rr.Body)
	}
	if rr.Header().Get("X-Gateway-Provider") != "fake" || rr.Header().Get("X-Gateway-Model") != "model-x" ||
		!strings.HasPrefix(rr.Header().Get("X-Request-Id"), "chatcmpl-") {
		t.Errorf("headers = %v", rr.Header())
	}
	if fp.got.Model != "model-x" || *fp.got.MaxTokens != 20 || *fp.got.Temperature != 0.5 {
		t.Errorf("provider got %+v", fp.got)
	}

	rec := a.lastRecord(t)
	if rec["status"] != float64(200) || rec["key_id"] != "dev" || rec["team"] != "platform" ||
		rec["alias"] != "fast" || rec["estimated"] != false || rec["attempts"] != float64(1) || rec["failovers"] != float64(0) {
		t.Errorf("record = %v", rec)
	}
	// 10 input tokens at $1/1M + 5 output tokens at $2/1M = $0.00002
	if cost := rec["cost_usd"].(float64); math.Abs(cost-0.00002) > 1e-12 {
		t.Errorf("cost_usd = %v, want 0.00002", cost)
	}
	if strings.Contains(a.logs.String(), "secret prompt") || strings.Contains(a.logs.String(), "Hello!") {
		t.Error("prompt or answer text was written to the log")
	}
}

func TestStreamingChunks(t *testing.T) {
	fp := &fakeProvider{chunks: []providers.Chunk{
		{Content: "Hel"}, {Content: "lo"}, {FinishReason: "stop"},
		{Usage: &providers.Usage{PromptTokens: 3, CompletionTokens: 2}},
	}}
	a := newTestAPI(t, fp)
	rr := a.call("POST", "/v1/chat/completions", devKey,
		`{"model":"fast","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`)

	if rr.Code != 200 || rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	lines := sseData(t, rr.Body.String())
	// role chunk, "Hel", "lo", finish chunk, usage chunk, [DONE]
	if len(lines) != 6 || lines[5] != "[DONE]" {
		t.Fatalf("got %d events: %v", len(lines), lines)
	}
	first, finish, usageChunk := decodeJSON(t, lines[0]), decodeJSON(t, lines[3]), decodeJSON(t, lines[4])
	if get(first, "object") != "chat.completion.chunk" || get(first, "choices", 0, "delta", "role") != "assistant" {
		t.Errorf("first chunk = %s", lines[0])
	}
	if get(decodeJSON(t, lines[1]), "choices", 0, "delta", "content") != "Hel" {
		t.Errorf("content chunk = %s", lines[1])
	}
	if get(finish, "choices", 0, "finish_reason") != "stop" {
		t.Errorf("finish chunk = %s", lines[3])
	}
	if c, _ := get(usageChunk, "choices").([]any); len(c) != 0 || get(usageChunk, "usage", "total_tokens") != float64(5) {
		t.Errorf("usage chunk = %s", lines[4])
	}
	rec := a.lastRecord(t)
	if rec["status"] != float64(200) || rec["stream"] != true || rec["estimated"] != false || rec["completion_tokens"] != float64(2) {
		t.Errorf("record = %v", rec)
	}
}

func TestStreamWithoutUsageIsEstimated(t *testing.T) {
	fp := &fakeProvider{chunks: []providers.Chunk{{Content: "abcdefgh"}, {FinishReason: "stop"}}}
	a := newTestAPI(t, fp)
	rr := a.call("POST", "/v1/chat/completions", devKey,
		`{"model":"fast","messages":[{"role":"user","content":"hello world!"}],"stream":true,"stream_options":{"include_usage":true}}`)
	lines := sseData(t, rr.Body.String())
	usageChunk := decodeJSON(t, lines[len(lines)-2])
	// 12 prompt characters -> 3 tokens; 8 answer characters -> 2 tokens
	if get(usageChunk, "usage", "prompt_tokens") != float64(3) || get(usageChunk, "usage", "completion_tokens") != float64(2) {
		t.Errorf("usage chunk = %s", lines[len(lines)-2])
	}
	if rec := a.lastRecord(t); rec["estimated"] != true {
		t.Errorf("record = %v; want estimated=true", rec)
	}
}

func TestStreamErrorBeforeFirstChunkIsJSON(t *testing.T) {
	fp := &fakeProvider{err: &providers.Error{Provider: "fake", Status: 503, Message: "down"}}
	a := newTestAPI(t, fp)
	rr := a.call("POST", "/v1/chat/completions", devKey, `{"model":"fast","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rr.Code != 502 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q; want a 502 JSON error", rr.Code, rr.Header().Get("Content-Type"))
	}
	if code := get(decodeJSON(t, rr.Body.String()), "error", "code"); code != "upstream_error" {
		t.Errorf("error code = %v", code)
	}
	if rec := a.lastRecord(t); rec["status"] != float64(502) || rec["error_type"] != "upstream_error" || rec["provider"] != "fake" {
		t.Errorf("record = %v", rec)
	}
}

func TestStreamErrorAfterStartIsInBand(t *testing.T) {
	fp := &fakeProvider{
		chunks: []providers.Chunk{{Content: "partial"}},
		midErr: &providers.Error{Provider: "fake", Message: "connection reset"},
	}
	a := newTestAPI(t, fp)
	rr := a.call("POST", "/v1/chat/completions", devKey, `{"model":"fast","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	lines := sseData(t, rr.Body.String())
	if rr.Code != 200 || len(lines) != 3 || slices.Contains(lines, "[DONE]") {
		t.Fatalf("status %d, events %v; want role, partial, error and no [DONE]", rr.Code, lines)
	}
	if get(decodeJSON(t, lines[2]), "error", "code") != "upstream_error" {
		t.Errorf("last event = %s; want an error object", lines[2])
	}
	if rec := a.lastRecord(t); rec["status"] != float64(502) || rec["error_type"] != "upstream_error_midstream" {
		t.Errorf("record = %v", rec)
	}
}

func TestUpstreamErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{"rate limited", &providers.Error{Provider: "fake", Status: 429}, 429, "upstream_rate_limited"},
		{"bad request passes through", &providers.Error{Provider: "fake", Status: 400, Message: "bad param"}, 400, "upstream_rejected_request"},
		{"gateway's provider key is wrong", &providers.Error{Provider: "fake", Status: 401}, 502, "upstream_auth_failed"},
		{"provider 500", &providers.Error{Provider: "fake", Status: 500}, 502, "upstream_error"},
		{"model missing upstream", &providers.Error{Provider: "fake", Status: 404, Message: "model not found"}, 502, "upstream_error"},
		{"timeout", &providers.Error{Provider: "fake", Message: "request failed", Err: context.DeadlineExceeded}, 504, "upstream_timeout"},
		{"every breaker open", router.ErrCircuitOpen, 503, "providers_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAPI(t, &fakeProvider{err: tt.err})
			rr := a.call("POST", "/v1/chat/completions", devKey, `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
			if rr.Code != tt.status || get(decodeJSON(t, rr.Body.String()), "error", "code") != tt.wantCode {
				t.Fatalf("status %d body %s; want %d %s", rr.Code, rr.Body, tt.status, tt.wantCode)
			}
		})
	}
}

func TestAuthAndModelChecks(t *testing.T) {
	fp := &fakeProvider{resp: &providers.ChatResponse{Content: "ok"}}
	a := newTestAPI(t, fp)
	body := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	}
	tests := []struct {
		name, key, model string
		status           int
		code             string
	}{
		{"no key", "", "fast", 401, "missing_api_key"},
		{"wrong key", "sk-gw-wrong", "fast", 401, "invalid_api_key"},
		{"unknown model", devKey, "nope", 404, "model_not_found"},
		{"model not allowed for key", limitedKey, "fast", 403, "model_not_allowed"},
		{"allowed model", limitedKey, "other", 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := a.call("POST", "/v1/chat/completions", tt.key, body(tt.model))
			if rr.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
			if tt.code != "" && get(decodeJSON(t, rr.Body.String()), "error", "code") != tt.code {
				t.Errorf("body = %s, want code %s", rr.Body, tt.code)
			}
		})
	}
}

func TestRequestParsing(t *testing.T) {
	fp := &fakeProvider{resp: &providers.ChatResponse{Content: "ok"}}
	a := newTestAPI(t, fp)

	bad := []string{
		`not json`,
		`{"model":"fast","messages":[]}`,
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"fast","messages":[{"content":"no role"}]}`,
		`{"model":"fast","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		`{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":0}`,
	}
	for _, b := range bad {
		if rr := a.call("POST", "/v1/chat/completions", devKey, b); rr.Code != 400 {
			t.Errorf("body %s: status %d, want 400", b, rr.Code)
		}
	}

	rr := a.call("POST", "/v1/chat/completions", devKey,
		`{"model":"fast","messages":[{"role":"user","content":[{"type":"text","text":"Hello "},{"type":"text","text":"there"}]}],"stop":"END"}`)
	if rr.Code != 200 {
		t.Fatalf("text parts: status %d: %s", rr.Code, rr.Body)
	}
	if fp.got.Messages[0].Content != "Hello there" || !slices.Equal(fp.got.Stop, []string{"END"}) {
		t.Errorf("provider got %+v", fp.got)
	}
}

func TestModelsFilteredByKey(t *testing.T) {
	a := newTestAPI(t, &fakeProvider{})
	ids := func(key string) []string {
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(a.call("GET", "/v1/models", key, "").Body.Bytes(), &out)
		var names []string
		for _, d := range out.Data {
			names = append(names, d.ID)
		}
		return names
	}
	if got := ids(devKey); !slices.Equal(got, []string{"fast", "other"}) {
		t.Errorf("dev key sees %v", got)
	}
	if got := ids(limitedKey); !slices.Equal(got, []string{"other"}) {
		t.Errorf("limited key sees %v", got)
	}
	if rr := a.call("GET", "/v1/models", "", ""); rr.Code != 401 {
		t.Errorf("no key: status %d, want 401", rr.Code)
	}
}

const hiBody = `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`

func TestRateLimitReturns429(t *testing.T) {
	a := newTestAPI(t, &fakeProvider{resp: &providers.ChatResponse{Content: "ok"}})
	for i := range 2 {
		if rr := a.call("POST", "/v1/chat/completions", tightKey, hiBody); rr.Code != 200 {
			t.Fatalf("request %d: status %d", i+1, rr.Code)
		}
	}
	rr := a.call("POST", "/v1/chat/completions", tightKey, hiBody)
	if rr.Code != 429 || get(decodeJSON(t, rr.Body.String()), "error", "code") != "rate_limit_exceeded" {
		t.Fatalf("3rd request: status %d, body %s; want 429 rate_limit_exceeded", rr.Code, rr.Body)
	}
	if ra := rr.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q, want seconds until the next minute", ra)
	}
	if rec := a.lastRecord(t); rec["status"] != float64(429) || rec["error_type"] != "requests_per_minute" {
		t.Errorf("record = %v", rec)
	}
	if rr := a.call("POST", "/v1/chat/completions", devKey, hiBody); rr.Code != 200 {
		t.Errorf("another key got status %d; limits are per key", rr.Code)
	}
}

func TestBudgetBlocksKey(t *testing.T) {
	// Each answer costs $0.00002 (10 input tokens at $1/1M + 5 output at $2/1M).
	fp := &fakeProvider{resp: &providers.ChatResponse{Content: "ok", Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 5}}}
	a := newTestAPI(t, fp)
	for i := range 2 { // $0 then $0.00002 spent: both under the $0.00003 budget
		if rr := a.call("POST", "/v1/chat/completions", budgetKey, hiBody); rr.Code != 200 {
			t.Fatalf("request %d: status %d", i+1, rr.Code)
		}
		a.srv.Wait() // usage is counted in the background
	}
	rr := a.call("POST", "/v1/chat/completions", budgetKey, hiBody) // $0.00004 spent
	body := decodeJSON(t, rr.Body.String())
	if rr.Code != 429 || get(body, "error", "code") != "budget_exceeded" || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, body %s; want 429 budget_exceeded with Retry-After", rr.Code, rr.Body)
	}
	if msg, _ := get(body, "error", "message").(string); !strings.Contains(msg, "daily budget") {
		t.Errorf("message = %q; want a clear reason", msg)
	}
}

func TestCacheServesRepeatedTemperatureZeroRequest(t *testing.T) {
	fp := &fakeProvider{resp: &providers.ChatResponse{Content: "first answer", FinishReason: "stop",
		Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 5}}}
	a := newTestAPI(t, fp)
	body := func(temp string) string {
		return `{"model":"fast","messages":[{"role":"user","content":"hi"}],"temperature":` + temp + `}`
	}
	if rr := a.call("POST", "/v1/chat/completions", devKey, body("0")); rr.Header().Get("X-Gateway-Cache") != "miss" {
		t.Fatalf("first call: X-Gateway-Cache = %q, want miss", rr.Header().Get("X-Gateway-Cache"))
	}
	fp.resp = &providers.ChatResponse{Content: "second answer"} // the provider would now answer differently

	rr := a.call("POST", "/v1/chat/completions", devKey, body("0"))
	got := decodeJSON(t, rr.Body.String())
	if rr.Header().Get("X-Gateway-Cache") != "hit" || get(got, "choices", 0, "message", "content") != "first answer" ||
		get(got, "usage", "total_tokens") != float64(15) || rr.Header().Get("X-Gateway-Provider") != "fake" {
		t.Fatalf("second call: headers %v, body %s; want the cached answer", rr.Header(), rr.Body)
	}
	rec := a.lastRecord(t)
	if rec["cache_hit"] != true || rec["cost_usd"] != float64(0) || rec["prompt_tokens"] != float64(0) ||
		math.Abs(rec["saved_usd"].(float64)-0.00002) > 1e-12 {
		t.Errorf("record = %v; want cache_hit, no cost, saved_usd 0.00002", rec)
	}

	other := a.call("POST", "/v1/chat/completions", limitedKey, `{"model":"other","messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	if other.Header().Get("X-Gateway-Cache") != "miss" {
		t.Error("another key or alias must not share cache entries")
	}
	if rr := a.call("POST", "/v1/chat/completions", devKey, body("0.5")); rr.Header().Get("X-Gateway-Cache") != "" {
		t.Error("temperature 0.5 must not use the cache")
	}
	stream := a.call("POST", "/v1/chat/completions", devKey,
		`{"model":"fast","messages":[{"role":"user","content":"hi"}],"temperature":0,"stream":true}`)
	if stream.Header().Get("X-Gateway-Cache") != "" {
		t.Error("streaming requests must not use the cache")
	}
}

// downStore fails every call, like Redis when it is down.
type downStore struct{}

func (downStore) IncrBy(context.Context, string, int64, time.Duration) (int64, error) {
	return 0, errors.New("connection refused")
}

func (downStore) Get(context.Context, ...string) ([]int64, error) {
	return nil, errors.New("connection refused")
}

func TestLimitStoreDown(t *testing.T) {
	ok := &fakeProvider{resp: &providers.ChatResponse{Content: "ok"}}
	open := newTestAPIWithStore(t, ok, downStore{}, true)
	if rr := open.call("POST", "/v1/chat/completions", tightKey, hiBody); rr.Code != 200 {
		t.Errorf("fail-open: status %d, want 200", rr.Code)
	}
	closed := newTestAPIWithStore(t, ok, downStore{}, false)
	rr := closed.call("POST", "/v1/chat/completions", tightKey, hiBody)
	if rr.Code != 503 || get(decodeJSON(t, rr.Body.String()), "error", "code") != "limit_store_unavailable" {
		t.Errorf("fail-closed: status %d, body %s; want 503 limit_store_unavailable", rr.Code, rr.Body)
	}
}

func TestHealthAndReadiness(t *testing.T) {
	a := newTestAPI(t, &fakeProvider{})
	if rr := a.call("GET", "/readyz", "", ""); rr.Code != 200 {
		t.Fatalf("readyz = %d, want 200", rr.Code)
	}
	a.srv.SetReady(false)
	if rr := a.call("GET", "/readyz", "", ""); rr.Code != 503 {
		t.Fatalf("readyz after SetReady(false) = %d, want 503", rr.Code)
	}
	if rr := a.call("GET", "/healthz", "", ""); rr.Code != 200 {
		t.Fatalf("healthz = %d, want 200 even when not ready", rr.Code)
	}
}
