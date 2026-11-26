package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer starts the mock with no delays, so tests run fast.
func newTestServer(t *testing.T, change func(*Settings)) *httptest.Server {
	t.Helper()
	s := newServer()
	s.settings.TTFTMS, s.settings.TokensPerSec, s.settings.ResponseTokens = 0, 0, 5
	if change != nil {
		change(&s.settings)
	}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const helloRequest = `{"model":"mock-1","messages":[{"role":"user","content":"hello world"}]}`

func TestChatNonStream(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest)
	var out completion
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || out.Choices[0].Message.Content != "The quick brown fox jumps" || out.Choices[0].FinishReason != "stop" {
		t.Fatalf("status %d, body %+v", resp.StatusCode, out)
	}
	// "hello world" is 11 characters, so about 3 prompt tokens.
	if out.Usage == nil || out.Usage.PromptTokens != 3 || out.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

func TestMaxTokensCapsTheAnswer(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := do(t, "POST", srv.URL+"/v1/chat/completions", `{"model":"m","messages":[],"max_tokens":2}`)
	var out completion
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Choices[0].FinishReason != "length" || out.Usage.CompletionTokens != 2 {
		t.Fatalf("got %+v; want finish length and 2 tokens", out)
	}
}

// streamLines returns the data payloads of an SSE response.
func streamLines(t *testing.T, resp *http.Response) []string {
	t.Helper()
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			lines = append(lines, data)
		}
	}
	return lines
}

func TestStreamFormat(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := do(t, "POST", srv.URL+"/v1/chat/completions",
		`{"model":"mock-1","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	lines := streamLines(t, resp)
	// role chunk + 5 tokens + finish chunk + usage chunk + [DONE]
	if len(lines) != 9 || lines[8] != "[DONE]" {
		t.Fatalf("got %d lines: %v", len(lines), lines)
	}
	var first, finish, usage streamChunk
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[6]), &finish)
	_ = json.Unmarshal([]byte(lines[7]), &usage)
	if first.Choices[0].Delta.Role != "assistant" {
		t.Errorf("first chunk = %s; want the assistant role", lines[0])
	}
	if finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != "stop" {
		t.Errorf("finish chunk = %s", lines[6])
	}
	if usage.Usage == nil || usage.Usage.CompletionTokens != 5 || len(usage.Choices) != 0 {
		t.Errorf("usage chunk = %s", lines[7])
	}
}

func TestOmitUsage(t *testing.T) {
	srv := newTestServer(t, func(s *Settings) { s.OmitUsage = true })
	resp := do(t, "POST", srv.URL+"/v1/chat/completions",
		`{"model":"m","messages":[],"stream":true,"stream_options":{"include_usage":true}}`)
	for _, l := range streamLines(t, resp) {
		if strings.Contains(l, `"usage"`) {
			t.Fatalf("usage sent although omit_usage is on: %s", l)
		}
	}
}

func TestOutageSwitchAndReset(t *testing.T) {
	srv := newTestServer(t, nil)
	if resp := do(t, "PATCH", srv.URL+"/admin/settings", `{"outage":true}`); resp.StatusCode != 200 {
		t.Fatalf("PATCH status %d", resp.StatusCode)
	}
	if resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest); resp.StatusCode != 503 {
		t.Fatalf("status during outage = %d, want 503", resp.StatusCode)
	}
	do(t, "POST", srv.URL+"/admin/reset", "")
	if resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest); resp.StatusCode != 200 {
		t.Fatalf("status after reset = %d, want 200", resp.StatusCode)
	}
}

func TestResetReturnsToStartSettings(t *testing.T) {
	start := defaultSettings()
	start.TTFTMS, start.TokensPerSec, start.ErrorRate = 0, 0, 1 // a mock started with -error-rate 1
	srv := httptest.NewServer(newServerWith(start).routes())
	t.Cleanup(srv.Close)

	do(t, "PATCH", srv.URL+"/admin/settings", `{"error_rate":0}`)
	if resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest); resp.StatusCode != 200 {
		t.Fatalf("status with error_rate 0 = %d, want 200", resp.StatusCode)
	}
	do(t, "POST", srv.URL+"/admin/reset", "")
	if resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest); resp.StatusCode != 500 {
		t.Fatalf("status after reset = %d, want 500 (back to the start error rate)", resp.StatusCode)
	}
}

func TestInjected429HasRetryAfter(t *testing.T) {
	srv := newTestServer(t, func(s *Settings) { s.ErrorRate, s.ErrorStatus = 1, 429 })
	resp := do(t, "POST", srv.URL+"/v1/chat/completions", helloRequest)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("status %d, Retry-After %q; want 429 and 1", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestPatchRejectsBadSettings(t *testing.T) {
	srv := newTestServer(t, nil)
	for _, body := range []string{`{"error_rate":2}`, `{"outtage":true}`, `{"error_status":200}`, `not json`} {
		if resp := do(t, "PATCH", srv.URL+"/admin/settings", body); resp.StatusCode != 400 {
			t.Errorf("PATCH %s: status %d, want 400", body, resp.StatusCode)
		}
	}
}
