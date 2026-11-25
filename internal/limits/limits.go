// Package limits enforces per-key rate limits (requests and tokens per
// minute) and spend budgets (USD per day and per month). Counters live in a
// shared store (Redis), so every gateway replica sees the same numbers.
//
// Windows are fixed and in UTC: a minute, a calendar day, a calendar month.
// A fixed window is simple and cheap (one counter per window). Its weak
// point: a client can send a full minute's limit just before the minute ends
// and again just after, so a short burst can reach twice the limit.
package limits

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

// Reasons a request is refused.
const (
	ReasonRequests  = "requests_per_minute"
	ReasonTokens    = "tokens_per_minute"
	ReasonDaily     = "daily_budget"
	ReasonMonthly   = "monthly_budget"
	ReasonStoreDown = "limit_store_down"
)

// Decision is the answer to "may this request go ahead?".
type Decision struct {
	Allowed    bool
	Reason     string        // which limit was hit, when not allowed
	Message    string        // a sentence for the client
	RetryAfter time.Duration // when the client may try again
}

// Limiter checks and counts usage per client key.
type Limiter struct {
	store     Store
	policies  map[string]config.Policy
	failOpen  bool // what to do when the store is down: allow (true) or refuse (false)
	log       *slog.Logger
	now       func() time.Time
	storeDown atomic.Bool
}

// New returns a Limiter. With failOpen, requests are allowed while the store
// is down; without it, they are refused with 503.
func New(store Store, policies map[string]config.Policy, failOpen bool, log *slog.Logger) *Limiter {
	return &Limiter{store: store, policies: policies, failOpen: failOpen, log: log, now: time.Now}
}

// window holds the counter keys for the current minute, day and month, and
// the time left until each one resets.
type window struct {
	requests, tokens, day, month      string
	untilMinute, untilDay, untilMonth time.Duration
}

func (l *Limiter) window(keyID string) window {
	now := l.now().UTC()
	minute := now.Truncate(time.Minute)
	y, m, d := now.Date()
	nextDay := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	nextMonth := time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
	return window{
		requests:    fmt.Sprintf("gw:rpm:%s:%d", keyID, minute.Unix()),
		tokens:      fmt.Sprintf("gw:tpm:%s:%d", keyID, minute.Unix()),
		day:         fmt.Sprintf("gw:usd:day:%s:%04d-%02d-%02d", keyID, y, m, d),
		month:       fmt.Sprintf("gw:usd:month:%s:%04d-%02d", keyID, y, m),
		untilMinute: minute.Add(time.Minute).Sub(now),
		untilDay:    nextDay.Sub(now),
		untilMonth:  nextMonth.Sub(now),
	}
}

// Admit checks a key's limits before a request, and counts the request.
//
// The request count is added first and then compared (one atomic INCR), so
// two replicas can never both let through the "last" request. Tokens and
// cost are only known after the answer, so Admit compares what was already
// used; Record adds the new usage. One request can therefore overshoot a
// token or USD limit, but the next one is refused.
func (l *Limiter) Admit(ctx context.Context, keyID, policy string) Decision {
	p := l.policies[policy]
	w := l.window(keyID)

	if p.RequestsPerMinute > 0 {
		n, err := l.store.IncrBy(ctx, w.requests, 1, 2*time.Minute)
		if err != nil {
			return l.storeFailed(err)
		}
		l.storeUp()
		if n > int64(p.RequestsPerMinute) {
			return refuse(ReasonRequests, w.untilMinute,
				"rate limit reached: %d requests per minute for this key", p.RequestsPerMinute)
		}
	}
	if p.TokensPerMinute > 0 || p.DailyUSD > 0 || p.MonthlyUSD > 0 {
		used, err := l.store.Get(ctx, w.tokens, w.day, w.month)
		if err != nil {
			return l.storeFailed(err)
		}
		l.storeUp()
		switch {
		case p.TokensPerMinute > 0 && used[0] >= int64(p.TokensPerMinute):
			return refuse(ReasonTokens, w.untilMinute,
				"rate limit reached: %d tokens per minute for this key", p.TokensPerMinute)
		case p.DailyUSD > 0 && used[1] >= nanoUSD(p.DailyUSD):
			return refuse(ReasonDaily, w.untilDay,
				"daily budget of $%.2f for this key is used up; it resets at 00:00 UTC", p.DailyUSD)
		case p.MonthlyUSD > 0 && used[2] >= nanoUSD(p.MonthlyUSD):
			return refuse(ReasonMonthly, w.untilMonth,
				"monthly budget of $%.2f for this key is used up; it resets on the 1st (UTC)", p.MonthlyUSD)
		}
	}
	return Decision{Allowed: true}
}

// Record adds a finished request's tokens and cost to the key's counters.
// Errors are logged, never returned: the answer has already been sent.
func (l *Limiter) Record(ctx context.Context, keyID, policy string, tokens int, costUSD float64) {
	p := l.policies[policy]
	w := l.window(keyID)
	add := func(key string, n int64, ttl time.Duration) {
		if _, err := l.store.IncrBy(ctx, key, n, ttl); err != nil {
			l.storeFailed(err)
		}
	}
	if p.TokensPerMinute > 0 && tokens > 0 {
		add(w.tokens, int64(tokens), 2*time.Minute)
	}
	if cost := nanoUSD(costUSD); cost > 0 {
		if p.DailyUSD > 0 {
			add(w.day, cost, w.untilDay+time.Hour)
		}
		if p.MonthlyUSD > 0 {
			add(w.month, cost, w.untilMonth+time.Hour)
		}
	}
}

// storeFailed applies the fail-open or fail-closed rule and logs only when
// the store goes down, not once per request.
func (l *Limiter) storeFailed(err error) Decision {
	if l.storeDown.CompareAndSwap(false, true) {
		l.log.Error("limit store is down", "fail_open", l.failOpen, "error", err.Error())
	}
	if l.failOpen {
		return Decision{Allowed: true}
	}
	return Decision{Reason: ReasonStoreDown, RetryAfter: 5 * time.Second,
		Message: "the gateway cannot check rate limits right now; try again shortly"}
}

func (l *Limiter) storeUp() {
	if l.storeDown.CompareAndSwap(true, false) {
		l.log.Info("limit store is back")
	}
}

// StoreDown reports whether the last store call failed.
func (l *Limiter) StoreDown() bool { return l.storeDown.Load() }

func refuse(reason string, retryAfter time.Duration, format string, args ...any) Decision {
	return Decision{Reason: reason, RetryAfter: retryAfter, Message: fmt.Sprintf(format, args...)}
}

// nanoUSD turns dollars into whole nano-dollars (1e-9 USD) for integer
// counters. Redis counts integers, and integers do not drift like floats.
func nanoUSD(usd float64) int64 { return int64(math.Round(usd * 1e9)) }
