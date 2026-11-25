// Package api serves the OpenAI-compatible HTTP API: POST /v1/chat/completions
// (normal and streaming) and GET /v1/models, plus the /healthz and /readyz
// probes. Any OpenAI SDK can use the gateway by changing its base URL.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/auth"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/cache"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/limits"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/usage"
)

// Deps are what the API server needs.
type Deps struct {
	Router  *router.Router
	Keys    *auth.Store
	Prices  usage.PriceTable
	Limiter *limits.Limiter // nil means no rate limits or budgets
	Cache   *cache.Cache    // nil means no response cache
	Ledger  *usage.Ledger   // nil means usage records go only to the log
	Log     *slog.Logger
}

// Server serves the API.
type Server struct {
	router  *router.Router
	keys    *auth.Store
	prices  usage.PriceTable
	limiter *limits.Limiter
	cache   *cache.Cache
	ledger  *usage.Ledger
	log     *slog.Logger
	ready   atomic.Bool
	bg      sync.WaitGroup // work that runs after the response, such as usage counting
}

// New returns an API server. It reports not-ready until SetReady(true).
func New(d Deps) *Server {
	return &Server{router: d.Router, keys: d.Keys, prices: d.Prices, limiter: d.Limiter, cache: d.Cache,
		ledger: d.Ledger, log: d.Log}
}

// Wait blocks until background work started by requests is done. The
// gateway calls it at shutdown, after the HTTP server has stopped.
func (s *Server) Wait() { s.bg.Wait() }

// SetReady sets what /readyz reports. At shutdown the gateway sets it to false,
// so the load balancer stops sending new requests before the process exits.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	return mux
}

// healthz is the liveness probe: the process is up and answers HTTP.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: should this instance get new traffic?
func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// models lists the aliases this key may use, in OpenAI's format.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := []model{}
	for _, alias := range s.router.Aliases() {
		if key.Allows(alias) {
			data = append(data, model{ID: alias, Object: "model", OwnedBy: "llm-gateway"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// authenticate checks the Bearer key and writes a 401 when it is missing or wrong.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Key, bool) {
	raw, ok := auth.BearerToken(r)
	if !ok {
		writeError(w, apiError{http.StatusUnauthorized, "invalid_request_error", "missing_api_key",
			"missing API key: send Authorization: Bearer <key>"})
		return nil, false
	}
	key, ok := s.keys.Lookup(raw)
	if !ok {
		writeError(w, apiError{http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "invalid API key"})
		return nil, false
	}
	return key, true
}

// logRecord writes the usage record to the log and queues it for the ledger.
func (s *Server) logRecord(rec usage.Record) {
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "request", rec.Attrs()...)
	if s.ledger != nil {
		s.ledger.Write(rec) // never blocks
	}
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}
