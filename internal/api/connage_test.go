package api

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// connAgeServer starts a test server with a fake clock and counts the TCP
// connections it accepts.
func connAgeServer(t *testing.T, maxAge time.Duration) (*Server, *httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	s := New(Deps{Log: slog.New(slog.DiscardHandler), MaxConnAge: maxAge})
	s.SetReady(true)
	var clock atomic.Int64 // fake time in nanoseconds
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	s.now = func() time.Time { return time.Unix(0, clock.Load()) }
	s.jitter = func() float64 { return 0.5 } // the age limit is exactly maxAge

	var conns atomic.Int64
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.ConnContext = s.ConnContext
	ts.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return s, ts, &clock, &conns
}

// getHealthz sends GET /healthz and reports whether the server asked to close the connection.
func getHealthz(t *testing.T, ts *httptest.Server) (closeAsked bool) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.Close
}

func TestOldConnectionsAreClosed(t *testing.T) {
	_, ts, clock, conns := connAgeServer(t, time.Minute)

	for range 2 {
		if getHealthz(t, ts) {
			t.Fatal("a young connection was asked to close")
		}
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("connections = %d, want 1 (keep-alive reused)", n)
	}

	clock.Add(int64(61 * time.Second))
	if !getHealthz(t, ts) {
		t.Fatal("a connection older than max_connection_age was not asked to close")
	}
	// The client must open a new connection, and the new one starts young.
	if getHealthz(t, ts) {
		t.Fatal("the new connection was asked to close")
	}
	if n := conns.Load(); n != 2 {
		t.Fatalf("connections = %d, want 2 (old one closed, new one opened)", n)
	}
}

func TestEveryConnectionIsClosedWhileNotReady(t *testing.T) {
	s, ts, _, _ := connAgeServer(t, time.Minute)
	s.SetReady(false) // shutdown drain
	if !getHealthz(t, ts) {
		t.Fatal("a response during the drain did not ask the client to close")
	}
}

func TestConnectionAgeOff(t *testing.T) {
	_, ts, clock, conns := connAgeServer(t, 0)
	getHealthz(t, ts)
	clock.Add(int64(24 * time.Hour))
	if getHealthz(t, ts) {
		t.Fatal("max_connection_age is off, but the connection was asked to close")
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("connections = %d, want 1", n)
	}
}

func TestConnectionAgeJitter(t *testing.T) {
	s := New(Deps{MaxConnAge: 100 * time.Second})
	base := time.Unix(0, 0)
	s.now = func() time.Time { return base }
	for _, tc := range []struct {
		jitter float64
		want   time.Duration
	}{{0, 90 * time.Second}, {0.5, 100 * time.Second}, {0.999999, 110 * time.Second}} {
		s.jitter = func() float64 { return tc.jitter }
		ctx := s.ConnContext(t.Context(), nil)
		got := ctx.Value(connDeadlineKey{}).(time.Time).Sub(base)
		if got.Round(time.Second) != tc.want {
			t.Errorf("jitter %v: age limit %v, want %v", tc.jitter, got, tc.want)
		}
	}
}
