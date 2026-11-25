package usage

import (
	"context"
	_ "embed"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// schemaLockID is a fixed number for a Postgres advisory lock. It makes
// several replicas that start at the same time create the table one by one.
const schemaLockID = 7_142_024

var ledgerColumns = []string{
	"ts", "request_id", "key_id", "owner", "team", "alias", "provider", "model", "stream",
	"status", "error_type", "latency_ms", "ttft_ms", "prompt_tokens", "completion_tokens",
	"estimated", "cost_usd", "saved_usd", "cache_hit", "attempts", "failovers",
}

// Postgres stores usage records in the usage_records table.
type Postgres struct {
	pool  *pgxpool.Pool
	ready atomic.Bool // the schema exists
}

// NewPostgres prepares a connection pool. It does not connect yet, so the
// gateway can start while the database is down.
func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

// Ping checks the connection.
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Close closes the pool.
func (p *Postgres) Close() { p.pool.Close() }

// Migrate creates the table and indexes if they are missing.
func (p *Postgres) Migrate(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLockID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, schemaSQL); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WriteBatch inserts records with COPY, the fastest way to load many rows.
// The first call creates the schema, so a database that was down at
// startup is set up as soon as it is back.
func (p *Postgres) WriteBatch(ctx context.Context, recs []Record) error {
	if !p.ready.Load() {
		if err := p.Migrate(ctx); err != nil {
			return err
		}
		p.ready.Store(true)
	}
	rows := make([][]any, len(recs))
	for i, r := range recs {
		rows[i] = []any{
			r.Time, r.RequestID, r.KeyID, r.Owner, r.Team, r.Alias, r.Provider, r.Model, r.Stream,
			r.Status, r.ErrorType, r.LatencyMS, r.TTFTMS, r.PromptTokens, r.CompletionTokens,
			r.Estimated, r.CostUSD, r.SavedUSD, r.CacheHit, r.Attempts, r.Failovers,
		}
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"usage_records"}, ledgerColumns, pgx.CopyFromRows(rows))
	return err
}
