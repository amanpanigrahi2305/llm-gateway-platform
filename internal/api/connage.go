package api

import (
	"context"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
)

// Keep-alive connections and load balancing on Kubernetes.
//
// kube-proxy (like most layer-4 load balancers) picks a pod once per TCP
// connection, not per request. A client with a keep-alive pool keeps sending to
// the pods it first connected to, so pods that the HPA adds get no traffic from
// it. To spread the load again, the server asks clients to reconnect: once a
// connection is older than its age limit, the next response carries
// "Connection: close". Go's server closes the connection after that response,
// and the client's next request opens a new one, which may land on any pod.
//
// Each connection's limit is max_connection_age ±10%, so a pool that opened all
// its connections at once does not reconnect all at once. While the server is
// not ready (for example during the shutdown drain), every response carries
// "Connection: close", so clients move off a pod that is about to stop.

type connDeadlineKey struct{}

// ConnContext is meant for http.Server.ConnContext. It stores, per connection,
// the moment after which the server asks the client to reconnect.
func (s *Server) ConnContext(ctx context.Context, _ net.Conn) context.Context {
	if s.maxConnAge <= 0 {
		return ctx
	}
	age := time.Duration(float64(s.maxConnAge) * (0.9 + 0.2*s.jitter()))
	return context.WithValue(ctx, connDeadlineKey{}, s.now().Add(age))
}

// closeOldConns adds "Connection: close" to responses on connections past their
// age limit, and to every response while the server is not ready.
func (s *Server) closeOldConns(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() || s.connExpired(r.Context()) {
			w.Header().Set("Connection", "close")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) connExpired(ctx context.Context) bool {
	deadline, ok := ctx.Value(connDeadlineKey{}).(time.Time)
	return ok && !s.now().Before(deadline)
}

// defaultJitter returns a random number in [0, 1).
func defaultJitter() float64 { return rand.Float64() }
