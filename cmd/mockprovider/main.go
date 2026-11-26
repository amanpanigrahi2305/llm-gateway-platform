// Command mockprovider is a fake OpenAI-compatible LLM for tests and demos.
// Latency, speed, error rate and an outage switch can be changed at runtime
// (PATCH /admin/settings), so failover, load and chaos tests never spend real
// LLM credits. The admin API has no auth: never expose this service publicly.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	errorRate := flag.Float64("error-rate", 0, "share of requests that fail, 0 to 1 (a start value; PATCH /admin/settings can change it)")
	errorStatus := flag.Int("error-status", 500, "HTTP status of injected errors, a 4xx or 5xx code")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	start := defaultSettings()
	start.ErrorRate, start.ErrorStatus = *errorRate, *errorStatus
	if err := start.validate(); err != nil {
		log.Error("invalid flags", "error", err.Error())
		os.Exit(2)
	}
	srv := &http.Server{Addr: *addr, Handler: newServerWith(start).routes(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("mock provider listening", "addr", *addr, "error_rate", start.ErrorRate, "error_status", start.ErrorStatus)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}
