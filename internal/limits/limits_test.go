package limits

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

// 10:30:45 UTC: 15 s left in the minute, 13h29m15s left in the day.
var testNow = time.Date(2026, 11, 24, 10, 30, 45, 0, time.UTC)

type testLimiter struct {
	*Limiter
	now  *time.Time
	logs *bytes.Buffer
}

func newTestLimiter(store Store, p config.Policy, failOpen bool) testLimiter {
	now := testNow
	var logs bytes.Buffer
	l := New(store, map[string]config.Policy{"p": p}, failOpen, slog.New(slog.NewJSONHandler(&logs, nil)))
	l.now = func() time.Time { return now }
	if m, ok := store.(*MemoryStore); ok {
		m.now = l.now
	}
	return testLimiter{Limiter: l, now: &now, logs: &logs}
}

func (t testLimiter) advance(d time.Duration) { *t.now = t.now.Add(d) }

func TestRequestsPerMinute(t *testing.T) {
	l := newTestLimiter(NewMemoryStore(), config.Policy{RequestsPerMinute: 3}, true)
	ctx := context.Background()
	for i := range 3 {
		if d := l.Admit(ctx, "dev", "p"); !d.Allowed {
			t.Fatalf("request %d refused: %+v", i+1, d)
		}
	}
	d := l.Admit(ctx, "dev", "p")
	if d.Allowed || d.Reason != ReasonRequests || d.RetryAfter != 15*time.Second {
		t.Fatalf("4th request: %+v; want refused, requests_per_minute, retry after 15s", d)
	}
	if !l.Admit(ctx, "other-key", "p").Allowed {
		t.Fatal("another key was refused; limits are per key")
	}
	l.advance(15 * time.Second) // a new minute
	if d := l.Admit(ctx, "dev", "p"); !d.Allowed {
		t.Fatalf("refused in the next minute: %+v", d)
	}
}

func TestTokensPerMinute(t *testing.T) {
	l := newTestLimiter(NewMemoryStore(), config.Policy{TokensPerMinute: 100}, true)
	ctx := context.Background()
	if !l.Admit(ctx, "dev", "p").Allowed {
		t.Fatal("first request refused")
	}
	l.Record(ctx, "dev", "p", 120, 0) // one request may overshoot...
	d := l.Admit(ctx, "dev", "p")     // ...but the next one is refused
	if d.Allowed || d.Reason != ReasonTokens || d.RetryAfter != 15*time.Second {
		t.Fatalf("after 120 tokens: %+v; want refused, tokens_per_minute", d)
	}
}

func TestDailyBudget(t *testing.T) {
	l := newTestLimiter(NewMemoryStore(), config.Policy{DailyUSD: 0.0001}, true)
	ctx := context.Background()
	l.Record(ctx, "dev", "p", 0, 0.00006)
	if !l.Admit(ctx, "dev", "p").Allowed {
		t.Fatal("refused under budget")
	}
	l.Record(ctx, "dev", "p", 0, 0.00006) // $0.00012 spent of $0.0001
	d := l.Admit(ctx, "dev", "p")
	want := 13*time.Hour + 29*time.Minute + 15*time.Second
	if d.Allowed || d.Reason != ReasonDaily || d.RetryAfter != want ||
		!strings.Contains(d.Message, "$0.0001 ") || !strings.Contains(d.Message, "00:00 UTC") {
		t.Fatalf("over budget: %+v; want refused, daily_budget, retry after %v", d, want)
	}
	l.advance(want) // midnight UTC: a new day
	if d := l.Admit(ctx, "dev", "p"); !d.Allowed {
		t.Fatalf("refused on the next day: %+v", d)
	}
}

func TestMonthlyBudget(t *testing.T) {
	l := newTestLimiter(NewMemoryStore(), config.Policy{DailyUSD: 10, MonthlyUSD: 1}, true)
	ctx := context.Background()
	l.Record(ctx, "dev", "p", 0, 1.5)
	d := l.Admit(ctx, "dev", "p")
	untilDec1 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Sub(testNow)
	if d.Allowed || d.Reason != ReasonMonthly || d.RetryAfter != untilDec1 {
		t.Fatalf("over monthly budget: %+v; want monthly_budget, retry after %v", d, untilDec1)
	}
}

// failingStore fails every call, like Redis when it is down.
type failingStore struct{ calls int }

func (f *failingStore) IncrBy(context.Context, string, int64, time.Duration) (int64, error) {
	f.calls++
	return 0, errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")
}

func (f *failingStore) Get(context.Context, ...string) ([]int64, error) {
	f.calls++
	return nil, errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")
}

func TestStoreDownFailOpen(t *testing.T) {
	l := newTestLimiter(&failingStore{}, config.Policy{RequestsPerMinute: 1}, true)
	for range 3 {
		if d := l.Admit(context.Background(), "dev", "p"); !d.Allowed {
			t.Fatalf("fail-open refused a request: %+v", d)
		}
	}
	if !l.StoreDown() || strings.Count(l.logs.String(), "limit store is down") != 1 {
		t.Errorf("StoreDown %v, logs %q; want one log line, not one per request", l.StoreDown(), l.logs)
	}
}

func TestStoreDownFailClosed(t *testing.T) {
	l := newTestLimiter(&failingStore{}, config.Policy{DailyUSD: 5}, false)
	d := l.Admit(context.Background(), "dev", "p")
	if d.Allowed || d.Reason != ReasonStoreDown || d.RetryAfter != 5*time.Second {
		t.Fatalf("fail-closed: %+v; want refused with limit_store_down", d)
	}
}

func TestStoreRecovery(t *testing.T) {
	l := newTestLimiter(&failingStore{}, config.Policy{RequestsPerMinute: 5}, true)
	l.Admit(context.Background(), "dev", "p")
	l.store = NewMemoryStore() // Redis is back
	l.Admit(context.Background(), "dev", "p")
	if l.StoreDown() || !strings.Contains(l.logs.String(), "limit store is back") {
		t.Errorf("StoreDown %v, logs %q; want recovery logged", l.StoreDown(), l.logs)
	}
}

func TestNoLimitsSkipsTheStore(t *testing.T) {
	store := &failingStore{}
	l := newTestLimiter(store, config.Policy{}, false)
	l.Record(context.Background(), "dev", "p", 500, 1.0)
	if d := l.Admit(context.Background(), "dev", "p"); !d.Allowed || store.calls != 0 {
		t.Fatalf("decision %+v, store calls %d; a policy without limits needs no store", d, store.calls)
	}
}

func TestMemoryStoreExpiry(t *testing.T) {
	m := NewMemoryStore()
	now := testNow
	m.now = func() time.Time { return now }
	ctx := context.Background()
	if n, _ := m.IncrBy(ctx, "k", 2, time.Minute); n != 2 {
		t.Fatalf("first IncrBy = %d", n)
	}
	if n, _ := m.IncrBy(ctx, "k", 3, time.Minute); n != 5 {
		t.Fatalf("second IncrBy = %d; the TTL must not reset the count", n)
	}
	now = now.Add(time.Minute)
	if vals, _ := m.Get(ctx, "k", "missing"); vals[0] != 0 || vals[1] != 0 {
		t.Fatalf("after expiry Get = %v, want [0 0]", vals)
	}
}

// TestRedisStore runs against a real Redis when GATEWAY_TEST_REDIS_URL is set,
// for example redis://localhost:6379/15 (database 15, so dev data is untouched).
func TestRedisStore(t *testing.T) {
	url := os.Getenv("GATEWAY_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set GATEWAY_TEST_REDIS_URL to run against a real Redis")
	}
	s, err := NewRedisStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	key := "gw:test:" + time.Now().Format("150405.000000000")
	defer s.client.Del(ctx, key)

	if n, err := s.IncrBy(ctx, key, 2, time.Minute); err != nil || n != 2 {
		t.Fatalf("IncrBy = %d, %v", n, err)
	}
	if n, err := s.IncrBy(ctx, key, 3, time.Hour); err != nil || n != 5 {
		t.Fatalf("IncrBy = %d, %v", n, err)
	}
	if ttl := s.client.TTL(ctx, key).Val(); ttl <= 0 || ttl > time.Minute {
		t.Errorf("TTL = %v; want the first expiry (1m) kept", ttl)
	}
	vals, err := s.Get(ctx, key, key+":missing")
	if err != nil || vals[0] != 5 || vals[1] != 0 {
		t.Fatalf("Get = %v, %v; want [5 0]", vals, err)
	}

	l := New(s, map[string]config.Policy{"p": {RequestsPerMinute: 2}}, false, slog.New(slog.DiscardHandler))
	id := "test-" + key
	var allowed []bool
	for range 3 {
		allowed = append(allowed, l.Admit(ctx, id, "p").Allowed)
	}
	if !slices.Equal(allowed, []bool{true, true, false}) {
		t.Errorf("allowed = %v; with Redis, the 3rd request in a minute must be refused", allowed)
	}
}
