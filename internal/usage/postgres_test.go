package usage

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgres runs against a real Postgres when GATEWAY_TEST_DATABASE_URL is
// set. It writes two rows with a unique request ID prefix and deletes them.
func TestPostgres(t *testing.T) {
	url := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set GATEWAY_TEST_DATABASE_URL to run against a real Postgres")
	}
	ctx := context.Background()
	p, err := NewPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	prefix := "test-" + time.Now().Format("20060102150405.000000000") + "-"
	defer func() { _, _ = p.pool.Exec(ctx, "DELETE FROM usage_records WHERE request_id LIKE $1", prefix+"%") }()

	now := time.Now().UTC().Truncate(time.Microsecond)
	recs := []Record{
		{Time: now, RequestID: prefix + "1", KeyID: "dev", Alias: "fast", Provider: "mock", Model: "mock-1",
			Status: 200, LatencyMS: 12, PromptTokens: 6, CompletionTokens: 20, CostUSD: 0.000046, Attempts: 1},
		{Time: now, RequestID: prefix + "2", KeyID: "dev", Alias: "fast", Status: 502, ErrorType: "upstream_error",
			Attempts: 4, Failovers: 1},
	}
	if err := p.WriteBatch(ctx, recs[:1]); err != nil { // the first batch also creates the schema
		t.Fatalf("WriteBatch: %v", err)
	}
	if err := p.WriteBatch(ctx, recs[1:]); err != nil {
		t.Fatalf("second WriteBatch: %v", err)
	}

	var n, failovers int
	var cost float64
	var ts time.Time
	err = p.pool.QueryRow(ctx,
		"SELECT count(*), sum(cost_usd)::float8, sum(failovers), min(ts) FROM usage_records WHERE request_id LIKE $1",
		prefix+"%").Scan(&n, &cost, &failovers, &ts)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || cost != 0.000046 || failovers != 1 || !ts.Equal(now) {
		t.Errorf("rows %d, cost %v, failovers %d, ts %v; want 2, 0.000046, 1, %v", n, cost, failovers, ts, now)
	}
}
