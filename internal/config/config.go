// Package config loads the gateway's YAML config file and checks it before the
// gateway starts. A bad config should stop the process at startup with a clear
// message, not fail later in the middle of a request.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Provider types the gateway can talk to.
const (
	TypeOpenAI    = "openai"
	TypeAnthropic = "anthropic"
	TypeOllama    = "ollama"
	TypeMock      = "mock"
	TypeBedrock   = "bedrock" // Amazon Bedrock Converse API; AWS credentials, no API key
)

// Config is the whole config file.
type Config struct {
	Server      Server              `yaml:"server"`
	Providers   map[string]Provider `yaml:"providers"`
	Aliases     map[string][]string `yaml:"aliases"` // alias -> ordered "provider/model" targets
	Prices      map[string]Price    `yaml:"prices"`  // "provider/model" -> price
	Keys        []Key               `yaml:"keys"`
	Reliability Reliability         `yaml:"reliability"`
	Limits      Limits              `yaml:"limits"`
	Policies    map[string]Policy   `yaml:"policies"` // limit policies; each key names one
	Cache       Cache               `yaml:"cache"`
	Ledger      Ledger              `yaml:"ledger"`
}

// Ledger configures the Postgres usage ledger: one row per request,
// written in the background. It is off when database_url_env is empty.
type Ledger struct {
	DatabaseURLEnv string `yaml:"database_url_env"` // env var with the Postgres URL (it holds a password)
	// PasswordEnv names an env var whose value replaces the URL's password.
	// Use it for generated passwords with characters that break a URL.
	PasswordEnv   string        `yaml:"password_env"`
	BufferSize    int           `yaml:"buffer_size"`    // records waiting to be written; when full, new ones are dropped
	BatchSize     int           `yaml:"batch_size"`     // rows per write
	FlushInterval time.Duration `yaml:"flush_interval"` // longest wait before a partial batch is written
}

// Cache configures the exact-match response cache. It is off by default,
// because it keeps answer text in memory.
type Cache struct {
	Enabled    bool          `yaml:"enabled"`
	TTL        time.Duration `yaml:"ttl"`         // how long an answer is served from the cache
	MaxEntries int           `yaml:"max_entries"` // the oldest-used answers go first when full
}

// Where Limits.OnStoreError can point.
const (
	FailOpen   = "fail_open"   // the limit store is down: let requests through
	FailClosed = "fail_closed" // the limit store is down: refuse requests with 503
)

// Limits says where rate-limit and budget counters live.
type Limits struct {
	// RedisURLEnv names the env var that holds the Redis URL (it may hold a
	// password). Empty means in-memory counters, which are correct only with
	// one gateway replica.
	RedisURLEnv  string `yaml:"redis_url_env"`
	OnStoreError string `yaml:"on_store_error"` // fail_open (default) or fail_closed
}

// Policy is a set of limits for client keys. 0 means no limit.
type Policy struct {
	RequestsPerMinute int     `yaml:"requests_per_minute"`
	TokensPerMinute   int     `yaml:"tokens_per_minute"`
	DailyUSD          float64 `yaml:"daily_usd"`
	MonthlyUSD        float64 `yaml:"monthly_usd"`
}

// Reliability holds the retry and circuit breaker settings.
type Reliability struct {
	MaxAttempts     int           `yaml:"max_attempts"`     // tries per target, the first try included
	BackoffBase     time.Duration `yaml:"backoff_base"`     // wait before the first retry; doubles on each retry
	BackoffMax      time.Duration `yaml:"backoff_max"`      // longest wait between two tries
	BreakerFailures int           `yaml:"breaker_failures"` // failures in a row that open a provider's breaker
	BreakerCooldown time.Duration `yaml:"breaker_cooldown"` // open time before one probe request is let through
}

// Server holds the HTTP server settings.
type Server struct {
	Addr            string        `yaml:"addr"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"` // how long open requests may finish after SIGTERM
	DrainDelay      time.Duration `yaml:"drain_delay"`      // wait after reporting not-ready, before shutdown
	// MaxConnectionAge asks clients to reconnect once a keep-alive connection is
	// this old (±10%), so new pods get traffic. Default 30s; negative turns it off.
	MaxConnectionAge time.Duration `yaml:"max_connection_age"`
}

// Provider is one upstream LLM API.
type Provider struct {
	Type           string        `yaml:"type"`
	BaseURL        string        `yaml:"base_url"`
	APIKeyEnv      string        `yaml:"api_key_env"`     // name of the env var that holds the key, never the key
	ConnectTimeout time.Duration `yaml:"connect_timeout"` // TCP connect and TLS handshake
	TTFTTimeout    time.Duration `yaml:"ttft_timeout"`    // streaming: longest wait for the first token
	Timeout        time.Duration `yaml:"timeout"`         // total time for one upstream call
	Region         string        `yaml:"region"`          // AWS region (bedrock only)
}

// Price is the cost in USD per 1 million tokens.
type Price struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// Key is one client API key. Only its SHA-256 hash is stored.
type Key struct {
	ID      string   `yaml:"id"`
	Hash    string   `yaml:"hash"`
	Owner   string   `yaml:"owner"`
	Team    string   `yaml:"team"`
	Aliases []string `yaml:"aliases"` // aliases this key may use; "*" means all
	Policy  string   `yaml:"policy"`  // name of a limit policy in policies
}

var defaultBaseURL = map[string]string{
	TypeOpenAI:    "https://api.openai.com/v1",
	TypeAnthropic: "https://api.anthropic.com",
	TypeOllama:    "http://localhost:11434/v1",
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Load reads and checks the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes and checks config YAML. Unknown keys are errors, so a typo
// such as "aliasses" is caught instead of silently ignored.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("parse config: file is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.setDefaults()
	if problems := c.validate(); len(problems) > 0 {
		return nil, fmt.Errorf("invalid config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return &c, nil
}

// SplitTarget splits "provider/model" at the first "/". Model names may
// contain more slashes or colons, for example "ollama/qwen2.5:0.5b".
func SplitTarget(target string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(target, "/")
	return provider, model, ok && provider != "" && model != ""
}

func (c *Config) setDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 30 * time.Second
	}
	if c.Server.MaxConnectionAge == 0 {
		c.Server.MaxConnectionAge = 30 * time.Second
	}
	for name, p := range c.Providers {
		if p.BaseURL == "" {
			p.BaseURL = defaultBaseURL[p.Type]
		}
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		if p.ConnectTimeout == 0 {
			p.ConnectTimeout = 5 * time.Second
		}
		if p.TTFTTimeout == 0 {
			p.TTFTTimeout = 30 * time.Second
		}
		if p.Timeout == 0 {
			p.Timeout = 5 * time.Minute
		}
		c.Providers[name] = p
	}
	for i := range c.Keys {
		if c.Keys[i].Policy == "" {
			c.Keys[i].Policy = "default"
		}
	}
	r := &c.Reliability
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 2
	}
	if r.BackoffBase == 0 {
		r.BackoffBase = 100 * time.Millisecond
	}
	if r.BackoffMax == 0 {
		r.BackoffMax = 2 * time.Second
	}
	if r.BreakerFailures == 0 {
		r.BreakerFailures = 5
	}
	if r.BreakerCooldown == 0 {
		r.BreakerCooldown = 30 * time.Second
	}
	if c.Limits.OnStoreError == "" {
		c.Limits.OnStoreError = FailOpen
	}
	if c.Cache.TTL == 0 {
		c.Cache.TTL = 5 * time.Minute
	}
	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 1000
	}
	if c.Ledger.BufferSize == 0 {
		c.Ledger.BufferSize = 10_000
	}
	if c.Ledger.BatchSize == 0 {
		c.Ledger.BatchSize = 500
	}
	if c.Ledger.FlushInterval == 0 {
		c.Ledger.FlushInterval = time.Second
	}
}

// validate returns every problem it finds, sorted, so one run shows them all.
func (c *Config) validate() []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Server.ShutdownTimeout < 0 || c.Server.DrainDelay < 0 {
		add("server: shutdown_timeout and drain_delay must not be negative")
	}
	if r := c.Reliability; r.MaxAttempts < 1 || r.MaxAttempts > 10 {
		add("reliability: max_attempts must be between 1 and 10")
	}
	if r := c.Reliability; r.BackoffBase < 0 || r.BackoffMax < r.BackoffBase {
		add("reliability: backoff_base must not be negative or larger than backoff_max")
	}
	if r := c.Reliability; r.BreakerFailures < 1 || r.BreakerCooldown < 0 {
		add("reliability: breaker_failures must be at least 1 and breaker_cooldown not negative")
	}
	if s := c.Limits.OnStoreError; s != FailOpen && s != FailClosed {
		add("limits: on_store_error must be %s or %s, not %q", FailOpen, FailClosed, s)
	}
	if c.Cache.TTL < 0 || c.Cache.MaxEntries < 0 {
		add("cache: ttl and max_entries must not be negative")
	}
	if l := c.Ledger; l.BufferSize < 0 || l.BatchSize < 0 || l.FlushInterval < 0 {
		add("ledger: buffer_size, batch_size and flush_interval must not be negative")
	}
	for name, p := range c.Policies {
		if p.RequestsPerMinute < 0 || p.TokensPerMinute < 0 || p.DailyUSD < 0 || p.MonthlyUSD < 0 {
			add("policies.%s: limits must not be negative (0 means no limit)", name)
		}
	}

	if len(c.Providers) == 0 {
		add("providers: at least one provider is required")
	}
	for name, p := range c.Providers {
		known := true
		switch p.Type {
		case TypeOpenAI, TypeAnthropic:
			if p.APIKeyEnv == "" {
				add("providers.%s: api_key_env is required for type %q", name, p.Type)
			}
		case TypeOllama, TypeMock:
		case TypeBedrock:
			if p.Region == "" {
				add("providers.%s: region is required for type bedrock", name)
			}
			if p.APIKeyEnv != "" {
				add("providers.%s: bedrock uses AWS credentials (for example EKS Pod Identity), not api_key_env", name)
			}
		default:
			known = false
			add("providers.%s: unknown type %q (use openai, anthropic, bedrock, ollama or mock)", name, p.Type)
		}
		// Bedrock's endpoint comes from the region; base_url only overrides it.
		if known && p.BaseURL == "" && p.Type != TypeBedrock {
			add("providers.%s: base_url is required", name)
		}
		if strings.Contains(name, "/") {
			add("providers.%s: name must not contain \"/\"", name)
		}
		if p.ConnectTimeout < 0 || p.TTFTTimeout < 0 || p.Timeout < 0 {
			add("providers.%s: timeouts must not be negative", name)
		}
		if p.TTFTTimeout > p.Timeout {
			add("providers.%s: ttft_timeout must not be longer than timeout", name)
		}
	}

	if len(c.Aliases) == 0 {
		add("aliases: at least one alias is required")
	}
	for alias, targets := range c.Aliases {
		if len(targets) == 0 {
			add("aliases.%s: needs at least one provider/model target", alias)
		}
		for _, t := range targets {
			prov, _, ok := SplitTarget(t)
			if !ok {
				add("aliases.%s: target %q must look like provider/model", alias, t)
				continue
			}
			if _, found := c.Providers[prov]; !found {
				add("aliases.%s: target %q uses unknown provider %q", alias, t, prov)
			}
			if _, priced := c.Prices[t]; !priced {
				add("prices: no price for %q (used by alias %s); use 0 for free local models", t, alias)
			}
		}
	}
	for t, p := range c.Prices {
		if p.InputPerMillion < 0 || p.OutputPerMillion < 0 {
			add("prices.%s: prices must not be negative", t)
		}
	}

	if len(c.Keys) == 0 {
		add("keys: at least one client key is required")
	}
	ids, hashes := map[string]bool{}, map[string]bool{}
	for i, k := range c.Keys {
		where := fmt.Sprintf("keys[%d]", i)
		if k.ID != "" {
			where += " (" + k.ID + ")"
		}
		switch {
		case k.ID == "":
			add("%s: id is required", where)
		case ids[k.ID]:
			add("%s: duplicate id", where)
		}
		ids[k.ID] = true
		switch {
		case !hashPattern.MatchString(k.Hash):
			add("%s: hash must be 64 lowercase hex characters; run 'gateway gen-key'", where)
		case hashes[k.Hash]:
			add("%s: same hash as another key", where)
		}
		hashes[k.Hash] = true
		if len(k.Aliases) == 0 {
			add("%s: aliases is required (use [\"*\"] for all)", where)
		}
		for _, a := range k.Aliases {
			if _, ok := c.Aliases[a]; !ok && a != "*" {
				add("%s: unknown alias %q", where, a)
			}
		}
		if _, ok := c.Policies[k.Policy]; !ok {
			add("%s: unknown policy %q; define it under policies (all limits 0 means no limit)", where, k.Policy)
		}
	}

	slices.Sort(problems)
	return problems
}
