// Command gateway runs the LLM gateway, plus two helper commands for client keys.
//
//	gateway serve -config config.yaml   run the HTTP server (the default command)
//	gateway gen-key                     print a new client key and its hash
//	gateway hash-key < keyfile          print the hash of an existing key
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/api"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/auth"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/usage"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "gen-key":
		return genKey(stdout)
	case "hash-key":
		return hashKey(stdin, stdout)
	}
	return fmt.Errorf("unknown command %q (use serve, gen-key or hash-key)", cmd)
}

// genKey prints a new key and its hash. The key is shown only this once.
func genKey(out io.Writer) error {
	key, err := auth.Generate()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "key:  %s\nhash: %s\n\n", key, auth.Hash(key))
	fmt.Fprintln(out, "Give the key to the client; it is shown only once.")
	fmt.Fprintln(out, "Put the hash in the config under keys[].hash.")
	return nil
}

// hashKey reads a key from stdin, so the key does not end up in shell history
// or in the process list, and prints its hash.
func hashKey(in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, 4096))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return errors.New("no key on stdin; usage: gateway hash-key < keyfile")
	}
	fmt.Fprintln(out, auth.Hash(key))
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", envOr("GATEWAY_CONFIG", "config.yaml"), "path to the config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	provs := make(map[string]providers.Provider, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		p, err := providers.New(name, pc, os.Getenv)
		if err != nil {
			return err
		}
		provs[name] = p
	}
	rel := cfg.Reliability
	rt, err := router.New(cfg.Aliases, provs, router.Options{
		MaxAttempts: rel.MaxAttempts,
		BackoffBase: rel.BackoffBase,
		BackoffMax:  rel.BackoffMax,
		Breaker:     router.BreakerConfig{Failures: rel.BreakerFailures, Cooldown: rel.BreakerCooldown},
		Log:         log,
	})
	if err != nil {
		return err
	}
	apiServer := api.New(rt, auth.NewStore(cfg.Keys), usage.PriceTable(cfg.Prices), log)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second, // slow-header (Slowloris) protection
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout on purpose: it would cut off long streaming answers.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 1)
	go func() { listenErr <- srv.ListenAndServe() }()
	apiServer.SetReady(true)
	log.Info("gateway listening", "addr", cfg.Server.Addr, "aliases", rt.Aliases(), "providers", len(provs))

	select {
	case err := <-listenErr:
		return err // for example, the port is already in use
	case <-ctx.Done():
	}
	stop() // a second Ctrl-C now stops the process at once
	return shutdown(srv, apiServer, cfg.Server, log)
}

// shutdown stops the server gently: report not-ready so load balancers stop
// sending traffic, wait drain_delay, then let open requests (including streams)
// finish for up to shutdown_timeout before closing what is left.
func shutdown(srv *http.Server, apiServer *api.Server, sc config.Server, log *slog.Logger) error {
	log.Info("shutdown started", "drain_delay", sc.DrainDelay.String(), "shutdown_timeout", sc.ShutdownTimeout.String())
	apiServer.SetReady(false)
	time.Sleep(sc.DrainDelay)

	ctx, cancel := context.WithTimeout(context.Background(), sc.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("shutdown timeout reached; closing open connections", "error", err.Error())
		return srv.Close()
	}
	log.Info("shutdown complete")
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
