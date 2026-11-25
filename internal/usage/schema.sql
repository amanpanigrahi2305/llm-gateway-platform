-- The usage ledger: one row per request. Metadata only, never prompt or
-- answer text. The gateway runs this at startup; every statement is safe to
-- run again.
CREATE TABLE IF NOT EXISTS usage_records (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts                timestamptz   NOT NULL,  -- when the request started
    request_id        text          NOT NULL,
    key_id            text          NOT NULL,
    owner             text          NOT NULL,
    team              text          NOT NULL,
    alias             text          NOT NULL,  -- what the client asked for
    provider          text          NOT NULL,  -- what served it
    model             text          NOT NULL,
    stream            boolean       NOT NULL,
    status            integer       NOT NULL,  -- HTTP status; 499 = client went away
    error_type        text          NOT NULL,
    latency_ms        integer       NOT NULL,
    ttft_ms           integer       NOT NULL,
    prompt_tokens     integer       NOT NULL,
    completion_tokens integer       NOT NULL,
    estimated         boolean       NOT NULL,  -- tokens estimated, not reported
    cost_usd          numeric(16,9) NOT NULL,
    saved_usd         numeric(16,9) NOT NULL,  -- cache hit: cost avoided
    cache_hit         boolean       NOT NULL,
    attempts          integer       NOT NULL,
    failovers         integer       NOT NULL
);

-- "What did key X spend this month?" and time-range reports.
CREATE INDEX IF NOT EXISTS usage_records_key_ts ON usage_records (key_id, ts);
CREATE INDEX IF NOT EXISTS usage_records_ts ON usage_records (ts);
