// Package usage turns token counts into an estimated USD cost and defines the
// per-request usage record. The record holds metadata only, never prompt or
// response text: privacy by default.
package usage

import (
	"log/slog"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

// PriceTable maps "provider/model" to its price per 1M tokens.
type PriceTable map[string]config.Price

// Cost returns the estimated USD cost of one request. ok is false when the
// target has no price. This is an estimate; the provider's bill is the truth.
func (p PriceTable) Cost(target string, promptTokens, completionTokens int) (usd float64, ok bool) {
	price, ok := p[target]
	if !ok {
		return 0, false
	}
	usd = (float64(promptTokens)*price.InputPerMillion + float64(completionTokens)*price.OutputPerMillion) / 1_000_000
	return usd, true
}

// EstimateTokens guesses a token count from a character count, at about four
// characters per token (a common rule of thumb for English). It is used only
// when a provider does not report usage, and the record is marked Estimated.
func EstimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// Record is the metadata of one request. Phase 1 writes it to the JSON log.
// Phase 2 also stores it in Postgres.
type Record struct {
	RequestID        string
	KeyID            string
	Owner            string
	Team             string
	Alias            string // what the client asked for
	Provider         string // what served it
	Model            string
	Stream           bool
	Status           int    // HTTP status, or 499 when the client went away
	ErrorType        string // short machine-readable reason; never upstream text
	LatencyMS        int64
	TTFTMS           int64 // streaming only: time to first token
	PromptTokens     int
	CompletionTokens int
	Estimated        bool // true when tokens were estimated, not reported
	CostUSD          float64
	Attempts         int // upstream calls, retries included
	Failovers        int // targets passed over (0 = the first target answered)
	CacheHit         bool
	SavedUSD         float64 // cache hit: what the upstream call would have cost
}

// Attrs returns the record as flat log fields.
func (r Record) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("request_id", r.RequestID),
		slog.String("key_id", r.KeyID),
		slog.String("owner", r.Owner),
		slog.String("team", r.Team),
		slog.String("alias", r.Alias),
		slog.String("provider", r.Provider),
		slog.String("model", r.Model),
		slog.Bool("stream", r.Stream),
		slog.Int("status", r.Status),
		slog.String("error_type", r.ErrorType),
		slog.Int64("latency_ms", r.LatencyMS),
		slog.Int64("ttft_ms", r.TTFTMS),
		slog.Int("prompt_tokens", r.PromptTokens),
		slog.Int("completion_tokens", r.CompletionTokens),
		slog.Bool("estimated", r.Estimated),
		slog.Float64("cost_usd", r.CostUSD),
		slog.Int("attempts", r.Attempts),
		slog.Int("failovers", r.Failovers),
		slog.Bool("cache_hit", r.CacheHit),
		slog.Float64("saved_usd", r.SavedUSD),
	}
}
