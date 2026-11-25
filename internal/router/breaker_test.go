package router

import (
	"slices"
	"testing"
	"time"
)

// fakeClock is a clock that moves only when the test says so.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBreaker() (*Breaker, *fakeClock, *[]string) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	var changes []string
	b := NewBreaker(BreakerConfig{Failures: 3, Cooldown: 10 * time.Second}, func(from, to State) {
		changes = append(changes, from.String()+"->"+to.String())
	})
	b.now = clock.Now
	return b, clock, &changes
}

func TestBreakerOpensAfterFailuresInARow(t *testing.T) {
	b, _, changes := newTestBreaker()
	b.Failure()
	b.Failure()
	b.Success() // resets the count
	b.Failure()
	b.Failure()
	if b.State() != Closed || !b.Allow() {
		t.Fatal("breaker opened before 3 failures in a row")
	}
	b.Failure()
	if b.State() != Open || b.Allow() {
		t.Fatalf("state %v after 3 failures in a row; want open and refusing", b.State())
	}
	if !slices.Equal(*changes, []string{"closed->open"}) {
		t.Errorf("changes = %v", *changes)
	}
}

func TestBreakerHalfOpenAllowsOneProbe(t *testing.T) {
	b, clock, _ := newTestBreaker()
	for range 3 {
		b.Failure()
	}
	clock.Advance(9 * time.Second)
	if b.Allow() {
		t.Fatal("allowed a request before the cool-down ended")
	}
	clock.Advance(time.Second)
	if !b.Allow() {
		t.Fatal("refused the probe after the cool-down")
	}
	if b.State() != HalfOpen || b.Allow() {
		t.Fatalf("state %v; want half-open with only one probe allowed", b.State())
	}
}

func TestBreakerProbeSuccessCloses(t *testing.T) {
	b, clock, changes := newTestBreaker()
	for range 3 {
		b.Failure()
	}
	clock.Advance(10 * time.Second)
	b.Allow()
	b.Success()
	if b.State() != Closed || !b.Allow() || !b.Allow() {
		t.Fatalf("state %v after a good probe; want closed", b.State())
	}
	want := []string{"closed->open", "open->half_open", "half_open->closed"}
	if !slices.Equal(*changes, want) {
		t.Errorf("changes = %v, want %v", *changes, want)
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	b, clock, _ := newTestBreaker()
	for range 3 {
		b.Failure()
	}
	clock.Advance(10 * time.Second)
	b.Allow()
	b.Failure()
	if b.State() != Open || b.Allow() {
		t.Fatalf("state %v after a failed probe; want open", b.State())
	}
	clock.Advance(10 * time.Second) // a new, full cool-down
	if !b.Allow() {
		t.Fatal("refused the next probe after the second cool-down")
	}
}

func TestBreakerAbortFreesProbe(t *testing.T) {
	b, clock, _ := newTestBreaker()
	for range 3 {
		b.Failure()
	}
	clock.Advance(10 * time.Second)
	b.Allow()
	b.Abort() // for example, the client went away during the probe
	if b.State() != HalfOpen || !b.Allow() {
		t.Fatal("abort did not free the probe slot")
	}
}
