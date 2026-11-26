package config

import (
	"strings"
	"testing"
	"time"
)

var validYAML = `
server:
  addr: ":9999"
providers:
  mock:
    type: mock
    base_url: http://localhost:9090/v1/
  ollama:
    type: ollama
aliases:
  fast: ["mock/mock-1", "ollama/qwen2.5:0.5b"]
prices:
  "mock/mock-1": {input_per_million: 1, output_per_million: 2}
  "ollama/qwen2.5:0.5b": {input_per_million: 0, output_per_million: 0}
keys:
  - id: dev
    hash: "` + strings.Repeat("a", 64) + `"
    aliases: ["*"]
policies:
  default: {requests_per_minute: 60, daily_usd: 5}
`

func TestParseValidConfigFillsDefaults(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Server.Addr != ":9999" {
		t.Errorf("addr = %q, want :9999", c.Server.Addr)
	}
	if c.Server.ShutdownTimeout != 30*time.Second {
		t.Errorf("shutdown_timeout = %v, want 30s default", c.Server.ShutdownTimeout)
	}
	if c.Server.MaxConnectionAge != 30*time.Second {
		t.Errorf("max_connection_age = %v, want 30s default", c.Server.MaxConnectionAge)
	}
	if got := c.Providers["mock"].BaseURL; got != "http://localhost:9090/v1" {
		t.Errorf("mock base_url = %q, want trailing slash removed", got)
	}
	if got := c.Providers["ollama"].BaseURL; got != "http://localhost:11434/v1" {
		t.Errorf("ollama base_url = %q, want default", got)
	}
	if p := c.Providers["mock"]; p.Timeout != 5*time.Minute || p.ConnectTimeout != 5*time.Second || p.TTFTTimeout != 30*time.Second {
		t.Errorf("timeouts = total %v, connect %v, ttft %v; want defaults 5m, 5s, 30s", p.Timeout, p.ConnectTimeout, p.TTFTTimeout)
	}
	if got := c.Keys[0].Policy; got != "default" {
		t.Errorf("policy = %q, want default", got)
	}
	if c.Limits.OnStoreError != FailOpen || c.Policies["default"].RequestsPerMinute != 60 {
		t.Errorf("limits = %+v, policies = %+v", c.Limits, c.Policies)
	}
	if c.Cache.Enabled || c.Cache.TTL != 5*time.Minute || c.Cache.MaxEntries != 1000 {
		t.Errorf("cache = %+v; want off by default, 5m, 1000", c.Cache)
	}
	if r := c.Reliability; r.MaxAttempts != 2 || r.BackoffBase != 100*time.Millisecond || r.BackoffMax != 2*time.Second ||
		r.BreakerFailures != 5 || r.BreakerCooldown != 30*time.Second {
		t.Errorf("reliability = %+v, want defaults 2, 100ms, 2s, 5, 30s", r)
	}
}

func TestParseBedrockNeedsNoBaseURL(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(validYAML, "type: ollama", "type: bedrock\n    region: eu-west-1", 1)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p := c.Providers["ollama"]; p.Type != TypeBedrock || p.Region != "eu-west-1" || p.BaseURL != "" {
		t.Errorf("provider = %+v; want bedrock, eu-west-1, no base_url", p)
	}
}

func TestParseRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"empty file", "", "file is empty"},
		{"typo in key", validYAML + "aliasses: {}\n", "field aliasses not found"},
		{"unknown provider type", strings.Replace(validYAML, "type: ollama", "type: olama", 1), `unknown type "olama"`},
		{"openai needs key env", strings.Replace(validYAML, "type: ollama", "type: openai", 1), "api_key_env is required"},
		{"bedrock needs region", strings.Replace(validYAML, "type: ollama", "type: bedrock", 1), "region is required for type bedrock"},
		{"bedrock takes no API key", strings.Replace(validYAML, "type: ollama", "type: bedrock\n    region: us-east-1\n    api_key_env: X", 1), "not api_key_env"},
		{"target without slash", strings.Replace(validYAML, `"mock/mock-1", `, `"mock-1", `, 1), "must look like provider/model"},
		{"unknown provider in alias", strings.Replace(validYAML, `["mock/mock-1", `, `["nope/mock-1", `, 1), `unknown provider "nope"`},
		{"missing price", strings.Replace(validYAML, `  "mock/mock-1": {input_per_million: 1, output_per_million: 2}`+"\n", "", 1), `no price for "mock/mock-1"`},
		{"bad key hash", strings.Replace(validYAML, strings.Repeat("a", 64), "not-a-hash", 1), "hash must be 64 lowercase hex"},
		{"unknown alias on key", strings.Replace(validYAML, `aliases: ["*"]`, `aliases: ["slow"]`, 1), `unknown alias "slow"`},
		{"ttft longer than total", strings.Replace(validYAML, "type: ollama", "type: ollama\n    ttft_timeout: 2m\n    timeout: 1m", 1), "ttft_timeout must not be longer than timeout"},
		{"too many attempts", validYAML + "reliability: {max_attempts: 50}\n", "max_attempts must be between 1 and 10"},
		{"unknown policy on key", strings.Replace(validYAML, `aliases: ["*"]`, `aliases: ["*"]`+"\n    policy: gold", 1), `unknown policy "gold"`},
		{"bad store rule", validYAML + "limits: {on_store_error: maybe}\n", "on_store_error must be fail_open or fail_closed"},
		{"negative budget", strings.Replace(validYAML, "daily_usd: 5", "daily_usd: -1", 1), "limits must not be negative"},
		{"backoff base above max", validYAML + "reliability: {backoff_base: 5s, backoff_max: 1s}\n", "backoff_base must not be negative or larger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("Parse succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSplitTarget(t *testing.T) {
	tests := []struct {
		in, provider, model string
		ok                  bool
	}{
		{"ollama/qwen2.5:0.5b", "ollama", "qwen2.5:0.5b", true},
		{"router/meta/llama-3", "router", "meta/llama-3", true},
		{"no-slash", "", "", false},
		{"/model", "", "", false},
		{"provider/", "", "", false},
	}
	for _, tt := range tests {
		p, m, ok := SplitTarget(tt.in)
		if ok != tt.ok || (ok && (p != tt.provider || m != tt.model)) {
			t.Errorf("SplitTarget(%q) = %q, %q, %v; want %q, %q, %v", tt.in, p, m, ok, tt.provider, tt.model, tt.ok)
		}
	}
}
