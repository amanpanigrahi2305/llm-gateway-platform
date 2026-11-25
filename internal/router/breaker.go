package router

import (
	"sync"
	"time"
)

// State is a circuit breaker state.
type State int

const (
	Closed   State = iota // normal: requests go to the provider
	Open                  // failing: requests skip the provider until the cool-down ends
	HalfOpen              // testing: one probe request goes to the provider
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	}
	return "half_open"
}

// BreakerConfig sets when a breaker opens and how long it stays open.
type BreakerConfig struct {
	Failures int           // failures in a row that open the breaker
	Cooldown time.Duration // time in the open state before one probe is let through
}

// Breaker is the circuit breaker for one provider. When a provider keeps
// failing, the breaker opens: requests then fail over at once instead of
// waiting for errors or timeouts, and the provider gets time to recover.
//
//	closed --N failures in a row--> open --cool-down ends--> half-open --probe ok--> closed
//	                                 ^                           |
//	                                 +-------probe failed--------+
type Breaker struct {
	cfg      BreakerConfig
	now      func() time.Time     // replaced in tests
	onChange func(from, to State) // called on every state change, under the lock

	mu       sync.Mutex
	state    State
	failures int       // failures in a row while closed
	openedAt time.Time // when the breaker last opened
	probing  bool      // half-open: the one probe is in flight
}

// NewBreaker returns a closed breaker. onChange may be nil.
func NewBreaker(cfg BreakerConfig, onChange func(from, to State)) *Breaker {
	cfg.Failures = max(cfg.Failures, 1)
	return &Breaker{cfg: cfg, now: time.Now, onChange: onChange}
}

// State returns the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow reports whether a request may go to the provider now. When the
// cool-down has ended, the first caller becomes the half-open probe; other
// callers are refused until the probe ends.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Closed:
		return true
	case Open:
		if b.now().Sub(b.openedAt) < b.cfg.Cooldown {
			return false
		}
		b.setState(HalfOpen)
		b.probing = true
		return true
	default: // HalfOpen
		if b.probing {
			return false
		}
		b.probing = true
		return true
	}
}

// Success records a call that got a real answer from the provider. A client
// error such as 400 also counts: the provider is up and answering.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Closed:
		b.failures = 0
	case HalfOpen:
		b.probing = false
		b.failures = 0
		b.setState(Closed)
	case Open:
		// A call that started before the breaker opened. Wait for the probe.
	}
}

// Failure records a provider-side failure: 5xx, 429, timeout, connection error.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Closed:
		b.failures++
		if b.failures >= b.cfg.Failures {
			b.trip()
		}
	case HalfOpen:
		b.probing = false
		b.trip()
	case Open:
		// A call that started before the breaker opened. Nothing changes.
	}
}

// Abort records a call that ended without showing the provider's health, for
// example because the client went away. It frees the half-open probe slot.
func (b *Breaker) Abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == HalfOpen {
		b.probing = false
	}
}

func (b *Breaker) trip() {
	b.failures = 0
	b.openedAt = b.now()
	b.setState(Open)
}

func (b *Breaker) setState(s State) {
	from := b.state
	b.state = s
	if b.onChange != nil && from != s {
		b.onChange(from, s)
	}
}
