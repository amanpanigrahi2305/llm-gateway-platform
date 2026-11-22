package usage

import (
	"math"
	"testing"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

func TestCost(t *testing.T) {
	prices := PriceTable{
		"anthropic/claude-opus-5-5": {InputPerMillion: 4, OutputPerMillion: 20},
		"ollama/qwen2.5:0.5b":       {InputPerMillion: 0, OutputPerMillion: 0},
	}
	tests := []struct {
		name               string
		target             string
		prompt, completion int
		want               float64
		wantOK             bool
	}{
		// 1,000 input tokens at $4/1M = $0.004; 500 output tokens at $20/1M = $0.01
		{"paid model", "anthropic/claude-opus-5-5", 1000, 500, 0.014, true},
		{"one million tokens each way", "anthropic/claude-opus-5-5", 1_000_000, 1_000_000, 24, true},
		{"free local model", "ollama/qwen2.5:0.5b", 5000, 5000, 0, true},
		{"zero tokens", "anthropic/claude-opus-5-5", 0, 0, 0, true},
		{"unknown target", "nope/model", 10, 10, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := prices.Cost(tt.target, tt.prompt, tt.completion)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("cost = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct{ chars, want int }{
		{0, 0}, {-5, 0}, {1, 1}, {4, 1}, {5, 2}, {400, 100},
	}
	for _, tt := range tests {
		if got := EstimateTokens(tt.chars); got != tt.want {
			t.Errorf("EstimateTokens(%d) = %d, want %d", tt.chars, got, tt.want)
		}
	}
}

func TestPriceTableUsesConfigPrices(t *testing.T) {
	cfg := map[string]config.Price{"mock/mock-1": {InputPerMillion: 1, OutputPerMillion: 2}}
	got, ok := PriceTable(cfg).Cost("mock/mock-1", 1000, 1000)
	if !ok || math.Abs(got-0.003) > 1e-12 {
		t.Fatalf("cost = %v, %v; want 0.003, true", got, ok)
	}
}
