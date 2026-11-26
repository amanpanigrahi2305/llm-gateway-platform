// Command gateway runs the LLM gateway, plus helper commands.
//
//	gateway serve -config config.yaml   run the HTTP server (the default command)
//	gateway gen-key                     print a new client key and its hash
//	gateway hash-key < keyfile          print the hash of an existing key
//	gateway version                     print the build version
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
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/cache"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/limits"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/usage"
)

// version is set at build time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

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
	case "version":
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	return fmt.Errorf("unknown command %q (use serve, gen-key, hash-key or version)", cmd)
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
	ttft := make(map[string]time.Duration, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		p, err := providers.New(name, pc, os.Getenv)
		if err != nil {
			return err
		}
		provs[name] = p
		ttft[name] = pc.TTFTTimeout
	}
	rel := cfg.Reliability
	rt, err := router.New(cfg.Aliases, provs, router.Options{
		MaxAttempts: rel.MaxAttempts,
		BackoffBase: rel.BackoffBase,
		BackoffMax:  rel.BackoffMax,
		Breaker:     router.BreakerConfig{Failures: rel.BreakerFailures, Cooldown: rel.BreakerCooldown},
		Log:         log,

		FirstTokenTimeout: ttft,
	})
	if err != nil {
		return err
	}
	limiter, closeStore, err := newLimiter(cfg, log)
	if err != nil {
		return err
	}
	defer closeStore()
	ledger, closeLedger, err := newLedger(cfg, log)
	if err != nil {
		return err
	}
	defer closeLedger() // runs after shutdown(), so the last records are written

	var responseCache *cache.Cache
	if cfg.Cache.Enabled {
		responseCache = cache.New(cfg.Cache.TTL, cfg.Cache.MaxEntries)
		log.Info("response cache is on: temperature-0 answers are kept in memory", "ttl", cfg.Cache.TTL.String())
	}

	apiServer := api.New(api.Deps{
		Router:  rt,
		Keys:    auth.NewStore(cfg.Keys),
		Prices:  usage.PriceTable(cfg.Prices),
		Limiter: limiter,
		Cache:   responseCache,
		Ledger:  ledger,
		Log:     log,

		MaxConnAge: cfg.Server.MaxConnectionAge,
	})

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           apiServer.Handler(),
		ConnContext:       apiServer.ConnContext, // per-connection age limit (max_connection_age)
		ReadHeaderTimeout: 10 * time.Second,      // slow-header (Slowloris) protection
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout on purpose: it would cut off long streaming answers.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 1)
	go func() { listenErr <- srv.ListenAndServe() }()
	apiServer.SetReady(true)
	log.Info("gateway listening", "version", version, "addr", cfg.Server.Addr, "aliases", rt.Aliases(), "providers", len(provs),
		"max_connection_age", cfg.Server.MaxConnectionAge.String())

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
	var err error
	if err = srv.Shutdown(ctx); err != nil {
		log.Warn("shutdown timeout reached; closing open connections", "error", err.Error())
		err = srv.Close()
	}
	apiServer.Wait() // finish background work, such as usage counting
	log.Info("shutdown complete")
	return err
}

// newLimiter builds the rate limiter. Counters live in Redis when
// limits.redis_url_env is set, otherwise in memory (correct for one replica only).
func newLimiter(cfg *config.Config, log *slog.Logger) (*limits.Limiter, func(), error) {
	failOpen := cfg.Limits.OnStoreError == config.FailOpen
	env := cfg.Limits.RedisURLEnv
	if env == "" {
		log.Warn("rate limits and budgets use in-memory counters; this is correct only with one gateway replica")
		return limits.New(limits.NewMemoryStore(), cfg.Policies, failOpen, log), func() {}, nil
	}
	url := os.Getenv(env)
	if url == "" {
		return nil, nil, fmt.Errorf("limits: environment variable %s is empty", env)
	}
	store, err := limits.NewRedisStore(url)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		// Start anyway. A gateway that exits whenever Redis blips would
		// crash-loop on Kubernetes; the on_store_error rule covers the gap.
		log.Warn("redis is not reachable at startup", "on_store_error", cfg.Limits.OnStoreError, "error", err.Error())
	}
	return limits.New(store, cfg.Policies, failOpen, log), func() { _ = store.Close() }, nil
}

// newLedger starts the Postgres usage ledger when ledger.database_url_env is
// set. The returned function writes what is left and closes the pool.
func newLedger(cfg *config.Config, log *slog.Logger) (*usage.Ledger, func(), error) {
	env := cfg.Ledger.DatabaseURLEnv
	if env == "" {
		log.Info("usage ledger is off; usage records go to the log only")
		return nil, func() {}, nil
	}
	url := os.Getenv(env)
	if url == "" {
		return nil, nil, fmt.Errorf("ledger: environment variable %s is empty", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	password := ""
	if pe := cfg.Ledger.PasswordEnv; pe != "" {
		if password = os.Getenv(pe); password == "" {
			return nil, nil, fmt.Errorf("ledger: environment variable %s is empty", pe)
		}
	}
	db, err := usage.NewPostgres(ctx, url, password)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		// Start anyway: the ledger is not on the request path.
		log.Warn("postgres is not reachable at startup; ledger records fail until it is back", "error", err.Error())
	}
	lc := cfg.Ledger
	l := usage.NewLedger(db, usage.LedgerOptions{BufferSize: lc.BufferSize, BatchSize: lc.BatchSize,
		FlushInterval: lc.FlushInterval}, log)
	return l, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := l.Close(ctx); err != nil {
			log.Warn("usage ledger did not finish writing in time", "error", err.Error())
		}
		s := l.Stats()
		log.Info("usage ledger closed", "written", s.Written, "dropped", s.Dropped, "failed", s.Failed)
		db.Close()
	}, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
