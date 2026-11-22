// Package router maps a model alias (what the client asks for) to an ordered
// list of provider/model targets (what actually serves it). Apps only know
// aliases, so operators can change models without redeploying apps.
package router

import (
	"context"
	"errors"
	"fmt"
	"slices"

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

// Router resolves aliases and sends requests to their targets.
type Router struct {
	aliases map[string][]Target
}

// New builds a Router from the config aliases and the built providers.
func New(aliases map[string][]string, provs map[string]providers.Provider) (*Router, error) {
	r := &Router{aliases: make(map[string][]Target, len(aliases))}
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

// Chat sends the request to the alias's first target and returns the answer
// and the target that served it. Phase 2 adds retries and failover down the list.
func (r *Router) Chat(ctx context.Context, alias string, req providers.ChatRequest) (*providers.ChatResponse, Target, error) {
	targets, err := r.Resolve(alias)
	if err != nil {
		return nil, Target{}, err
	}
	t := targets[0]
	req.Model = t.Model
	resp, err := t.Provider.Chat(ctx, req)
	return resp, t, err
}

// ChatStream is Chat for streaming. emit receives the serving target with each
// chunk, so the caller can set response headers before the first byte.
func (r *Router) ChatStream(ctx context.Context, alias string, req providers.ChatRequest,
	emit func(Target, providers.Chunk) error) (Target, error) {
	targets, err := r.Resolve(alias)
	if err != nil {
		return Target{}, err
	}
	t := targets[0]
	req.Model = t.Model
	err = t.Provider.ChatStream(ctx, req, func(c providers.Chunk) error { return emit(t, c) })
	return t, err
}
