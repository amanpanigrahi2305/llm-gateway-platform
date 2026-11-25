package router

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
)

// fakeProvider answers with "from <name>". errs scripts the result of each
// call in order (nil means success); calls after the script succeed.
type fakeProvider struct {
	name       string
	errs       []error
	midStream  error // streaming: fail after the first chunk
	mu         sync.Mutex
	calls      int
	lastModel  string
	beforeCall func() // runs at the start of each call, if set
}

func (f *fakeProvider) next(req providers.ChatRequest) error {
	if f.beforeCall != nil {
		f.beforeCall()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastModel = req.Model
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	return nil
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if err := f.next(req); err != nil {
		return nil, err
	}
	return &providers.ChatResponse{Content: "from " + f.name}, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, req providers.ChatRequest, emit func(providers.Chunk) error) error {
	if err := f.next(req); err != nil {
		return err
	}
	if err := emit(providers.Chunk{Content: "from " + f.name}); err != nil {
		return err
	}
	return f.midStream
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func upstream(status int) error { return &providers.Error{Provider: "test", Status: status} }

var opts = Options{
	MaxAttempts: 2, BackoffBase: 100 * time.Millisecond, BackoffMax: time.Second,
	Breaker: BreakerConfig{Failures: 5, Cooldown: 30 * time.Second},
}

// newTestRouter builds a router with alias "fast" = primary, then backup.
// Sleeps are recorded instead of waited.
func newTestRouter(t *testing.T, primary, backup *fakeProvider) (*Router, *[]time.Duration) {
	t.Helper()
	return newRouterWith(t, opts, primary, backup)
}

func newRouterWith(t *testing.T, o Options, primary, backup *fakeProvider) (*Router, *[]time.Duration) {
	t.Helper()
	r, err := New(
		map[string][]string{
			"fast":  {"primary/model-a", "backup/model-b"},
			"local": {"backup/qwen2.5:0.5b"},
		},
		map[string]providers.Provider{"primary": primary, "backup": backup},
		o,
	)
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	r.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return ctx.Err()
	}
	r.randN = func(n int64) int64 { return n - 1 } // always the longest wait
	return r, &slept
}

func TestResolveKeepsOrder(t *testing.T) {
	r, _ := newTestRouter(t, &fakeProvider{name: "primary"}, &fakeProvider{name: "backup"})
	targets, err := r.Resolve("fast")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].String() != "primary/model-a" || targets[1].String() != "backup/model-b" {
		t.Fatalf("targets = %v", targets)
	}
	if _, err := r.Resolve("slow"); !errors.Is(err, ErrUnknownAlias) {
		t.Fatalf("err = %v, want ErrUnknownAlias", err)
	}
}

func TestChatUsesFirstTargetWhenHealthy(t *testing.T) {
	primary, backup := &fakeProvider{name: "primary"}, &fakeProvider{name: "backup"}
	r, _ := newTestRouter(t, primary, backup)
	resp, out, err := r.Chat(context.Background(), "fast", providers.ChatRequest{Model: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "from primary" || out.Target.String() != "primary/model-a" || out.Attempts != 1 || out.Failovers != 0 {
		t.Errorf("resp %q, outcome %+v", resp.Content, out)
	}
	if primary.lastModel != "model-a" || backup.callCount() != 0 {
		t.Errorf("primary got model %q, backup calls %d", primary.lastModel, backup.callCount())
	}
}

func TestRetryThenSucceed(t *testing.T) {
	primary := &fakeProvider{name: "primary", errs: []error{upstream(503)}}
	r, slept := newTestRouter(t, primary, &fakeProvider{name: "backup"})
	resp, out, err := r.Chat(context.Background(), "fast", providers.ChatRequest{})
	if err != nil || resp.Content != "from primary" {
		t.Fatalf("resp %v, err %v", resp, err)
	}
	if out.Attempts != 2 || out.Failovers != 0 || !slices.Equal(*slept, []time.Duration{100 * time.Millisecond}) {
		t.Errorf("outcome %+v, slept %v", out, *slept)
	}
}

func TestFailoverAfterRetries(t *testing.T) {
	primary := &fakeProvider{name: "primary", errs: []error{upstream(500), upstream(500)}}
	backup := &fakeProvider{name: "backup"}
	r, _ := newTestRouter(t, primary, backup)
	resp, out, err := r.Chat(context.Background(), "fast", providers.ChatRequest{})
	if err != nil || resp.Content != "from backup" {
		t.Fatalf("resp %v, err %v", resp, err)
	}
	if out.Target.String() != "backup/model-b" || out.Attempts != 3 || out.Failovers != 1 || primary.callCount() != 2 {
		t.Errorf("outcome %+v, primary calls %d", out, primary.callCount())
	}
	if backup.lastModel != "model-b" {
		t.Errorf("backup got model %q, want its own model name", backup.lastModel)
	}
}

func TestErrorClasses(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantErr      bool
		primaryCalls int
		backupCalls  int
	}{
		{"429 is retried, then fails over", upstream(429), false, 2, 1},
		{"408 is retried", upstream(408), false, 2, 1},
		{"connection error is retried", &providers.Error{Provider: "test", Message: "request failed", Err: errors.New("connection refused")}, false, 2, 1},
		{"401 fails over without retry", upstream(401), false, 1, 1},
		{"404 fails over without retry", upstream(404), false, 1, 1},
		{"400 is the client's fault: no retry, no failover", upstream(400), true, 1, 0},
		{"422 is the client's fault", upstream(422), true, 1, 0},
		{"non-upstream error stops", errors.New("cannot encode request"), true, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := &fakeProvider{name: "primary", errs: []error{tt.err, tt.err}}
			backup := &fakeProvider{name: "backup"}
			r, _ := newTestRouter(t, primary, backup)
			_, _, err := r.Chat(context.Background(), "fast", providers.ChatRequest{})
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error: %v", err, tt.wantErr)
			}
			if primary.callCount() != tt.primaryCalls || backup.callCount() != tt.backupCalls {
				t.Errorf("calls: primary %d, backup %d; want %d, %d",
					primary.callCount(), backup.callCount(), tt.primaryCalls, tt.backupCalls)
			}
		})
	}
}

func TestAllTargetsFailReturnsLastError(t *testing.T) {
	primary := &fakeProvider{name: "primary", errs: []error{upstream(500), upstream(500)}}
	backup := &fakeProvider{name: "backup", errs: []error{upstream(502), upstream(503)}}
	r, _ := newTestRouter(t, primary, backup)
	_, out, err := r.Chat(context.Background(), "fast", providers.ChatRequest{})
	var pe *providers.Error
	if !errors.As(err, &pe) || pe.Status != 503 {
		t.Fatalf("err = %v, want the last error (503)", err)
	}
	if out.Attempts != 4 || out.Failovers != 1 {
		t.Errorf("outcome %+v", out)
	}
}

func TestStreamFailsOverBeforeFirstChunk(t *testing.T) {
	primary := &fakeProvider{name: "primary", errs: []error{upstream(503), upstream(503)}}
	r, _ := newTestRouter(t, primary, &fakeProvider{name: "backup"})
	var seen []string
	out, err := r.ChatStream(context.Background(), "fast", providers.ChatRequest{}, func(tg Target, c providers.Chunk) error {
		seen = append(seen, tg.String()+": "+c.Content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"backup/model-b: from backup"}) || out.Failovers != 1 {
		t.Errorf("emitted %v, outcome %+v", seen, out)
	}
}

func TestStreamNotRetriedAfterFirstChunk(t *testing.T) {
	primary := &fakeProvider{name: "primary", midStream: upstream(502)}
	backup := &fakeProvider{name: "backup"}
	r, _ := newTestRouter(t, primary, backup)
	var chunks int
	out, err := r.ChatStream(context.Background(), "fast", providers.ChatRequest{}, func(Target, providers.Chunk) error {
		chunks++
		return nil
	})
	if err == nil {
		t.Fatal("want the mid-stream error")
	}
	if chunks != 1 || out.Attempts != 1 || backup.callCount() != 0 {
		t.Errorf("chunks %d, outcome %+v, backup calls %d; want no retry after the first chunk", chunks, out, backup.callCount())
	}
}

func TestClientGoneStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := &fakeProvider{name: "primary", errs: []error{upstream(503), upstream(503)}, beforeCall: cancel}
	backup := &fakeProvider{name: "backup"}
	r, _ := newTestRouter(t, primary, backup)
	_, out, err := r.Chat(ctx, "fast", providers.ChatRequest{})
	if err == nil || out.Attempts != 1 || backup.callCount() != 0 {
		t.Errorf("err %v, outcome %+v, backup calls %d; want stop after the client left", err, out, backup.callCount())
	}
}

// breakerRouter has one try per target and breakers that open after 2 failures.
func breakerRouter(t *testing.T, primary, backup *fakeProvider) (*Router, *fakeClock) {
	t.Helper()
	o := opts
	o.MaxAttempts = 1
	o.Breaker = BreakerConfig{Failures: 2, Cooldown: time.Minute}
	r, _ := newRouterWith(t, o, primary, backup)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	for _, b := range r.breakers {
		b.now = clock.Now
	}
	return r, clock
}

func TestOpenBreakerSkipsProvider(t *testing.T) {
	down := []error{upstream(503), upstream(503), upstream(503)}
	primary, backup := &fakeProvider{name: "primary", errs: down}, &fakeProvider{name: "backup"}
	r, clock := breakerRouter(t, primary, backup)
	ctx := context.Background()

	for range 2 { // two failures open primary's breaker; backup answers both times
		if resp, _, err := r.Chat(ctx, "fast", providers.ChatRequest{}); err != nil || resp.Content != "from backup" {
			t.Fatalf("resp %v, err %v", resp, err)
		}
	}
	if r.BreakerStates()["primary"] != Open {
		t.Fatalf("primary breaker = %v, want open", r.BreakerStates()["primary"])
	}
	_, out, err := r.Chat(ctx, "fast", providers.ChatRequest{})
	if err != nil || primary.callCount() != 2 || out.Attempts != 1 || out.Failovers != 1 {
		t.Fatalf("err %v, primary calls %d, outcome %+v; want primary skipped", err, primary.callCount(), out)
	}

	// After the cool-down one probe goes to primary. It fails (third scripted
	// error), so the breaker opens again and backup answers.
	clock.Advance(time.Minute)
	if _, _, err := r.Chat(ctx, "fast", providers.ChatRequest{}); err != nil || primary.callCount() != 3 {
		t.Fatalf("err %v, primary calls %d; want one probe", err, primary.callCount())
	}
	if r.BreakerStates()["primary"] != Open {
		t.Fatalf("primary breaker = %v after a failed probe, want open", r.BreakerStates()["primary"])
	}

	// The next probe succeeds (the script has no more errors): the breaker closes.
	clock.Advance(time.Minute)
	resp, _, err := r.Chat(ctx, "fast", providers.ChatRequest{})
	if err != nil || resp.Content != "from primary" || r.BreakerStates()["primary"] != Closed {
		t.Fatalf("resp %v, err %v, state %v; want primary back and closed", resp, err, r.BreakerStates()["primary"])
	}
}

func TestAllBreakersOpen(t *testing.T) {
	down := []error{upstream(500), upstream(500)}
	primary, backup := &fakeProvider{name: "primary", errs: down}, &fakeProvider{name: "backup", errs: down}
	r, _ := breakerRouter(t, primary, backup)
	for range 2 {
		_, _, _ = r.Chat(context.Background(), "fast", providers.ChatRequest{})
	}
	_, out, err := r.Chat(context.Background(), "fast", providers.ChatRequest{})
	if !errors.Is(err, ErrCircuitOpen) || out.Attempts != 0 {
		t.Fatalf("err %v, outcome %+v; want ErrCircuitOpen and no attempts", err, out)
	}
}

func TestClientErrorsDoNotOpenBreaker(t *testing.T) {
	bad := []error{upstream(400), upstream(400), upstream(400)}
	r, _ := breakerRouter(t, &fakeProvider{name: "primary", errs: bad}, &fakeProvider{name: "backup"})
	for range 3 {
		_, _, _ = r.Chat(context.Background(), "fast", providers.ChatRequest{})
	}
	if r.BreakerStates()["primary"] != Closed {
		t.Fatal("400 errors opened the breaker; they are the client's fault")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	r, _ := newTestRouter(t, &fakeProvider{name: "primary"}, &fakeProvider{name: "backup"})
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second}
	for n, w := range want {
		if got := r.backoff(n + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v (longest wait)", n+1, got, w)
		}
	}
	r.randN = func(int64) int64 { return 0 }
	if got := r.backoff(3); got != 0 {
		t.Errorf("backoff with jitter 0 = %v, want 0", got)
	}
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(map[string][]string{"fast": {"ghost/model"}}, map[string]providers.Provider{}, opts)
	if err == nil {
		t.Fatal("New accepted a target with an unknown provider")
	}
}

func TestAliasesSorted(t *testing.T) {
	r, _ := newTestRouter(t, &fakeProvider{name: "primary"}, &fakeProvider{name: "backup"})
	if got := r.Aliases(); !slices.Equal(got, []string{"fast", "local"}) {
		t.Fatalf("Aliases() = %v", got)
	}
}
