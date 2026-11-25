// Package router maps a model alias (what the client asks for) to an ordered
// list of provider/model targets (what actually serves it). It also keeps a
// request alive through provider failures: it retries temporary errors with
// backoff, and it fails over to the next target in the list.
package router

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
)

// ErrUnknownAlias means the client asked for a model alias that is not configured.
var ErrUnknownAlias = errors.New("unknown model alias")

// Target is one provider/model pair behind an alias.
type Target struct {
	Provider providers.Provider
	Model    string
}

// String returns "provider/model", the key used in the price table.
func (t Target) String() string { return t.Provider.Name() + "/" + t.Model }

// Options control retries.
type Options struct {
	MaxAttempts int           // tries per target, the first try included
	BackoffBase time.Duration // wait before the first retry; doubles on each retry
	BackoffMax  time.Duration // longest wait between two tries
}

// Outcome tells the caller what the router did, for the usage record.
type Outcome struct {
	Target    Target // the target that answered, or the last one tried
	Attempts  int    // upstream calls made, retries included
	Failovers int    // targets passed over before the last one tried
}

// Router resolves aliases and sends requests to their targets.
type Router struct {
	aliases map[string][]Target
	opts    Options
	sleep   func(context.Context, time.Duration) error // replaced in tests
	randN   func(n int64) int64                        // random number in [0, n); replaced in tests
}

// New builds a Router from the config aliases and the built providers.
func New(aliases map[string][]string, provs map[string]providers.Provider, opts Options) (*Router, error) {
	opts.MaxAttempts = max(opts.MaxAttempts, 1)
	r := &Router{
		aliases: make(map[string][]Target, len(aliases)),
		opts:    opts,
		sleep:   sleepContext,
		randN:   rand.Int64N,
	}
	for alias, targets := range aliases {
		for _, t := range targets {
			name, model, ok := config.SplitTarget(t)
			p, found := provs[name]
			if !ok || !found {
				return nil, fmt.Errorf("alias %s: unknown target %q", alias, t)
			}
			r.aliases[alias] = append(r.aliases[alias], Target{Provider: p, Model: model})
		}
	}
	return r, nil
}

// Aliases returns all alias names, sorted.
func (r *Router) Aliases() []string {
	names := make([]string, 0, len(r.aliases))
	for a := range r.aliases {
		names = append(names, a)
	}
	slices.Sort(names)
	return names
}

// Resolve returns the ordered targets for an alias.
func (r *Router) Resolve(alias string) ([]Target, error) {
	targets, ok := r.aliases[alias]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlias, alias)
	}
	return targets, nil
}

// Chat sends a normal request, with retries and failover.
func (r *Router) Chat(ctx context.Context, alias string, req providers.ChatRequest) (*providers.ChatResponse, Outcome, error) {
	var resp *providers.ChatResponse
	out, err := r.run(ctx, alias, func(ctx context.Context, t Target) (bool, error) {
		req.Model = t.Model
		var err error
		resp, err = t.Provider.Chat(ctx, req)
		return false, err
	})
	return resp, out, err
}

// ChatStream sends a streaming request. emit receives the serving target with
// each chunk, so the caller can set response headers before the first byte.
//
// Retries and failover happen only before the first chunk. After that, the
// client already has part of an answer, and a new answer from another try
// would not continue it: the client would get two answers glued together.
func (r *Router) ChatStream(ctx context.Context, alias string, req providers.ChatRequest,
	emit func(Target, providers.Chunk) error) (Outcome, error) {
	return r.run(ctx, alias, func(ctx context.Context, t Target) (bool, error) {
		req.Model = t.Model
		started := false
		err := t.Provider.ChatStream(ctx, req, func(c providers.Chunk) error {
			started = true
			return emit(t, c)
		})
		return started, err
	})
}

// attemptFunc makes one upstream call to t. started is true when part of the
// answer already went to the client; such a call is never repeated.
type attemptFunc func(ctx context.Context, t Target) (started bool, err error)

// run tries the alias's targets in order. Each target gets up to MaxAttempts
// tries for temporary errors, then the next target takes over.
func (r *Router) run(ctx context.Context, alias string, attempt attemptFunc) (Outcome, error) {
	targets, err := r.Resolve(alias)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	var lastErr error
	for i, t := range targets {
		out.Target, out.Failovers = t, i
	tries:
		for try := 1; try <= r.opts.MaxAttempts; try++ {
			if try > 1 {
				if err := r.sleep(ctx, r.backoff(try-1)); err != nil {
					return out, err // the client left during the wait
				}
			}
			out.Attempts++
			started, err := attempt(ctx, t)
			if err == nil {
				return out, nil
			}
			lastErr = err
			if started || ctx.Err() != nil {
				return out, err // part of the answer was sent, or the client left
			}
			switch classify(err) {
			case stop:
				return out, err
			case skip:
				break tries // no retry: go to the next target
			}
		}
	}
	return out, lastErr
}

// action is what the router does after a failed attempt.
type action int

const (
	retry action = iota // a temporary failure: try again, then fail over
	skip                // this target cannot serve the request: fail over at once
	stop                // the request itself is wrong: return the error to the client
)

// classify decides what to do after an error.
//   - 408, 429, 5xx, timeouts and connection errors are temporary: retry.
//   - 401, 403 and 404 mean the gateway's key or model name is wrong for this
//     provider. Another try will fail the same way, but another provider may work.
//   - Other 4xx (400, 413, 422, ...) mean the request is wrong. Every provider
//     would reject it, so the client gets the error at once.
func classify(err error) action {
	var pe *providers.Error
	if !errors.As(err, &pe) {
		return stop // not an upstream failure, for example a request that cannot be encoded
	}
	switch s := pe.Status; {
	case s == 0: // no HTTP response: connection refused, reset, timeout
		return retry
	case s == 408 || s == 429 || s >= 500:
		return retry
	case s == 401 || s == 403 || s == 404:
		return skip
	}
	return stop
}

// backoff returns the wait before retry n (1, 2, ...): a random time between 0
// and BackoffBase*2^(n-1), capped at BackoffMax ("full jitter"). The random
// part spreads out retries from many requests, so they do not all hit a
// recovering provider at the same moment.
func (r *Router) backoff(n int) time.Duration {
	ceiling := r.opts.BackoffMax
	if d := r.opts.BackoffBase << (n - 1); d > 0 && d < ceiling {
		ceiling = d
	}
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(r.randN(int64(ceiling) + 1))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
