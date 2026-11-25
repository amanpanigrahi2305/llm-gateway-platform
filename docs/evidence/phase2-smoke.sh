#!/usr/bin/env bash
# Phase 2 end-to-end run: failover, circuit breaker, timeouts, rate limits, budgets,
# cache, Redis-down rules and the Postgres ledger, with the mock provider only.
# It prints Markdown; docs/evidence/phase2-smoke.md is its output.
#
# Needs: make dev-up (Redis + Postgres), jq, k6, and in .env: GATEWAY_DEV_KEY,
# GATEWAY_REDIS_URL, GATEWAY_DATABASE_URL. Uses ports 8080, 9090, 9091.
# It empties the local dev Redis (db 0) and the usage_records table first.
#
#   bash docs/evidence/phase2-smoke.sh > docs/evidence/phase2-smoke.md
set -u
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$HOME/go/bin:$PATH"
cd "$(dirname "$0")/../.." || exit 1
set -a; . ./.env; set +a
TMP=$(mktemp -d)
GW=http://localhost:8080
PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done; docker start llm-gw-redis >/dev/null 2>&1; rm -rf "$TMP"; }
trap cleanup EXIT

code() { echo '```'"${1:-}"; }
h() { echo; echo "## $*"; echo; }

hdr() { grep -i "^$1:" "$TMP/hdr" | tail -1 | cut -d' ' -f2- | tr -d '\r'; }
# chat KEY BODY -> one line: status, serving provider, cache, Retry-After, time
chat() {
  curl -s -o "$TMP/body" -D "$TMP/hdr" -w '%{http_code} %{time_total}' \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -d "$2" "$GW/v1/chat/completions" > "$TMP/meta"
  read -r st secs < "$TMP/meta"
  printf 'status=%s provider=%s cache=%s retry_after=%s time=%.3fs\n' \
    "$st" "$(hdr X-Gateway-Provider || true)" "$(hdr X-Gateway-Cache || true)" "$(hdr Retry-After || true)" "$secs" \
    | sed 's/=\([ ]\)/=-\1/g; s/=$/=-/'
}
errcode() { jq -r '.error.code // empty' "$TMP/body"; }
errmsg() { jq -r '.error.message // empty' "$TMP/body"; }
mock() { curl -s -X PATCH -d "$2" "http://localhost:$1/admin/settings" > /dev/null; }
mockreset() { curl -s -X POST "http://localhost:$1/admin/reset" > /dev/null; }
mark() { wc -l < "$TMP/gw.log"; }
since() { tail -n +"$(( $1 + 1 ))" "$TMP/gw.log"; }
recs() { since "$1" | grep '"msg":"request"' | jq -c '{status,provider,attempts,failovers,latency_ms,ttft_ms,error_type}'; }
brk() { since "$1" | grep 'circuit breaker' | jq -c '{t: .time[11:23], provider, from, to}'; }

start_gateway() { # $1 = config file
  ./bin/gateway serve -config "$1" >> "$TMP/gw.log" 2>&1 &
  GWPID=$!; PIDS+=("$GWPID")
  for _ in $(seq 1 50); do curl -sf "$GW/readyz" >/dev/null && return; sleep 0.1; done
  echo "gateway did not become ready"; tail -5 "$TMP/gw.log"; exit 1
}

P='"messages":[{"role":"user","content":"phase2-secret-prompt hi"}]'
FAST='{"model":"fast",'"$P"'}'

# --- setup -----------------------------------------------------------------
make build >/dev/null
DEV="$GATEWAY_DEV_KEY"
read -r TIGHT TIGHT_HASH < <(./bin/gateway gen-key | awk '/^key:/{k=$2} /^hash:/{print k, $2}')
read -r BUDGET BUDGET_HASH < <(./bin/gateway gen-key | awk '/^key:/{k=$2} /^hash:/{print k, $2}')
read -r OPS OPS_HASH < <(./bin/gateway gen-key | awk '/^key:/{k=$2} /^hash:/{print k, $2}')
DEV_HASH=$(printf '%s' "$DEV" | ./bin/gateway hash-key)

write_config() { # $1 = on_store_error
cat > "$TMP/config.yaml" <<EOF
server: {addr: ":8080", shutdown_timeout: 30s, drain_delay: 1s}
providers:
  mock-a: {type: mock, base_url: "http://localhost:9090/v1", ttft_timeout: 1s, timeout: 30s}
  mock-b: {type: mock, base_url: "http://localhost:9091/v1", ttft_timeout: 1s, timeout: 30s}
aliases:
  fast: ["mock-a/mock-1", "mock-b/mock-1"]
  only-b: ["mock-b/mock-1"]
prices:
  "mock-a/mock-1": {input_per_million: 1, output_per_million: 2}
  "mock-b/mock-1": {input_per_million: 1, output_per_million: 2}
reliability: {max_attempts: 2, backoff_base: 100ms, backoff_max: 1s, breaker_failures: 5, breaker_cooldown: 10s}
limits: {redis_url_env: GATEWAY_REDIS_URL, on_store_error: $1}
policies:
  default: {}
  tight: {requests_per_minute: 5}
  budget: {daily_usd: 0.0001}
  roomy: {requests_per_minute: 1000, daily_usd: 10}
cache: {enabled: true, ttl: 5m, max_entries: 1000}
ledger: {database_url_env: GATEWAY_DATABASE_URL, flush_interval: 500ms}
keys:
  - {id: dev, owner: aman, team: platform, hash: "$DEV_HASH", aliases: ["*"], policy: default}
  - {id: tight, owner: aman, team: demo, hash: "$TIGHT_HASH", aliases: ["*"], policy: tight}
  - {id: budget, owner: aman, team: demo, hash: "$BUDGET_HASH", aliases: ["*"], policy: budget}
  - {id: ops, owner: aman, team: demo, hash: "$OPS_HASH", aliases: ["*"], policy: roomy}
EOF
}
write_config fail_open

docker exec llm-gw-redis redis-cli FLUSHDB >/dev/null       # db 0: fresh limit counters for this run
docker exec llm-gw-postgres psql -qU gateway -d gateway -c 'TRUNCATE usage_records' >/dev/null 2>&1

./bin/mockprovider -addr :9090 > "$TMP/mock-a.log" 2>&1 & PIDS+=($!)
./bin/mockprovider -addr :9091 > "$TMP/mock-b.log" 2>&1 & PIDS+=($!)
sleep 0.5
: > "$TMP/gw.log"
start_gateway "$TMP/config.yaml"

echo "# Phase 2 evidence: reliability and cost control"
echo
echo "Raw output of one end-to-end run on $(date -u '+%Y-%m-%d %H:%M UTC'). Two mock providers"
echo "(mock-a on :9090, mock-b on :9091) behind alias \`fast\` = [mock-a, mock-b]."
echo "Only the mock provider was used: no LLM credits. Keys are not shown."
h "0. Setup"
code
echo "commit:    $(git rev-parse --short HEAD)"
echo "go:        $(go version | cut -d' ' -f3)"
echo "redis:     $(docker exec llm-gw-redis redis-server --version | cut -d' ' -f3)"
echo "postgres:  $(docker exec llm-gw-postgres postgres --version)"
echo "k6:        $(k6 version | head -1)"
echo "cpu:       $(lscpu | sed -n 's/^Model name: *//p') ($(nproc) threads, WSL2)"
echo "settings:  max_attempts=2 backoff_base=100ms breaker_failures=5 breaker_cooldown=10s ttft_timeout=1s"
code

h "1. Healthy: mock-a serves"
m=$(mark)
code; chat "$DEV" "$FAST"; code
code json; recs "$m"; code

h "2. mock-a outage: failover to mock-b, then the breaker opens"
echo "PATCH mock-a {\"outage\":true}, then 8 requests one after another."
mock 9090 '{"outage":true}'
m=$(mark)
code; for i in $(seq 1 8); do printf 'req %d: ' "$i"; chat "$DEV" "$FAST"; done; code
echo "Usage records (attempts = upstream calls; failovers = targets passed over):"
code json; recs "$m"; code
echo "Breaker log:"
code json; brk "$m"; code

h "3. mock-a recovers: half-open probe closes the breaker"
mock 9090 '{"outage":false}'
echo "Outage off. Wait 10.5 s (cool-down), then 2 requests."
sleep 10.5
m=$(mark)
code; for i in 1 2; do printf 'req %d: ' "$i"; chat "$DEV" "$FAST"; done; code
code json; brk "$m"; code

h "4. Streaming: failover before the first chunk"
mock 9090 '{"outage":true}'
m=$(mark)
code
curl -sN -D "$TMP/hdr" -o "$TMP/stream" -H "Authorization: Bearer $DEV" -H 'Content-Type: application/json' \
  -d '{"model":"fast","stream":true,'"$P"'}' "$GW/v1/chat/completions"
echo "status=$(head -1 "$TMP/hdr" | cut -d' ' -f2) provider=$(hdr X-Gateway-Provider) content-type=$(hdr Content-Type)"
head -3 "$TMP/stream" | cut -c1-150
echo "..."
tail -2 "$TMP/stream"
code
code json; recs "$m"; code
mock 9090 '{"outage":false}'

h "5. Slow provider: time-to-first-token timeout (1 s) fails over"
echo "PATCH mock-a {\"ttft_ms\":3000}: mock-a sends headers at once but its first token after 3 s."
mock 9090 '{"ttft_ms":3000}'
m=$(mark)
code
curl -sN -o /dev/null -D "$TMP/hdr" -w 'status=%{http_code} time=%{time_total}s ' -H "Authorization: Bearer $DEV" \
  -H 'Content-Type: application/json' -d '{"model":"fast","stream":true,'"$P"'}' "$GW/v1/chat/completions"
echo "provider=$(hdr X-Gateway-Provider)"
code
code json; recs "$m"; code
mockreset 9090
chat "$DEV" "$FAST" >/dev/null   # a success on mock-a resets its failure count

h "6. Which errors are retried"
echo "mock-a answers 400 (client error): passed through at once, no retry, no failover."
mock 9090 '{"error_rate":1,"error_status":400}'
m=$(mark)
code; chat "$DEV" "$FAST"; echo "error.code=$(errcode)"; code
echo "mock-a answers 429 (rate limited): retried once, then mock-b serves."
mock 9090 '{"error_rate":1,"error_status":429}'
code; chat "$DEV" "$FAST"; code
code json; recs "$m"; code
mockreset 9090
chat "$DEV" "$FAST" >/dev/null

h "7. Every provider down: 502, then 503 once both breakers are open"
mock 9090 '{"outage":true}'; mock 9091 '{"outage":true}'
m=$(mark)
code; for i in 1 2 3 4; do printf 'req %d: ' "$i"; chat "$DEV" "$FAST" | tr -d '\n'; echo " error.code=$(errcode)"; done; code
code json; recs "$m"; code
code json; brk "$m"; code
mockreset 9090; mockreset 9091
echo "Both outages off; after the cool-down one request per alias closes both breakers."
sleep 10.5
m=$(mark)
code; chat "$DEV" "$FAST"; chat "$DEV" '{"model":"only-b",'"$P"'}'; code
code json; brk "$m"; code

h "8. Rate limit: key with 5 requests per minute"
m=$(mark)
code; for i in $(seq 1 6); do printf 'req %d: ' "$i"; chat "$TIGHT" "$FAST"; done; code
echo "429 body:"
code json; jq -c . "$TMP/body"; code

h "9. Budget: key with \$0.0001 per day"
echo "Each mock answer costs \$0.000046 (6 input tokens at \$1/1M + 20 output tokens at \$2/1M)."
echo "Requests 1-3 are checked against \$0, \$0.000046 and \$0.000092 spent: all under \$0.0001."
m=$(mark)
code; for i in 1 2 3 4; do printf 'req %d: ' "$i"; chat "$BUDGET" "$FAST"; sleep 0.3; done; code
echo "429 body:"
code json; jq -c . "$TMP/body"; code
echo "Redis counter (integer nano-dollars) for the key today:"
code; docker exec llm-gw-redis redis-cli --scan --pattern 'gw:usd:day:budget:*' | while read -r k; do
  echo "$k = $(docker exec llm-gw-redis redis-cli GET "$k")"; done; code
code json; since "$m" | grep '"msg":"request"' | jq -c '{status,cost_usd,error_type}'; code

h "10. Cache: temperature 0, same request twice"
T0='{"model":"fast","temperature":0,'"$P"'}'
m=$(mark)
code; chat "$DEV" "$T0"; chat "$DEV" "$T0"; echo "temperature 0.7 (never cached):"; chat "$DEV" '{"model":"fast","temperature":0.7,'"$P"'}'; code
code json; since "$m" | grep '"msg":"request"' | jq -c '{latency_ms,cache_hit,cost_usd,saved_usd}'; code

h "11. Failover under load (k6, 20 virtual users, 20 s)"
echo "mock-a outage on at t=5 s, off at t=15 s. Alias fast. max_tokens=5."
chat "$DEV" '{"model":"only-b",'"$P"'}' >/dev/null
m=$(mark)
t_k6=$(date +%s.%N)
k6 run --quiet --summary-trend-stats 'avg,med,p(95),p(99),max' -e GATEWAY_KEY="$DEV" loadtest/failover.js > "$TMP/k6.txt" 2>&1 &
K6=$!
sleep 5; mock 9090 '{"outage":true}'
sleep 10; mock 9090 '{"outage":false}'
wait "$K6"; k6rc=$?
code; sed 's/\x1b\[[0-9;]*m//g' "$TMP/k6.txt" | grep -v '^$' | grep -vi 'gateway_key'; code
echo "k6 exit code: $k6rc"
echo
echo "Gateway records during the run:"
code
since "$m" | grep '"msg":"request"' | jq -r '[.status, .provider] | @tsv' | sort | uniq -c | sed 's/^ */count=/'
code
echo "Breaker log during the run:"
code json; brk "$m"; code
echo "Note: WSL2 steps its wall clock back every ~30 s when it syncs with Windows, so log"
echo "timestamps can show less than the 10 s cool-down. The breaker, latency_ms, curl and k6"
echo "use a monotonic clock and are not affected. Clock steps logged during the run:"
code; journalctl --since "@${t_k6%.*}" --no-pager 2>/dev/null | grep 'Time jumped' | head -5; code

h "12. Redis down with on_store_error: fail_open"
echo "Key with policy roomy (1000 requests/min, \$10/day), so its limits need Redis."
m=$(mark)
code
echo "redis up:   $(chat "$OPS" "$FAST")"
docker stop llm-gw-redis >/dev/null
for i in 1 2 3; do echo "redis down: $(chat "$OPS" "$FAST")"; done
docker start llm-gw-redis >/dev/null
for _ in $(seq 1 30); do docker exec llm-gw-redis redis-cli ping >/dev/null 2>&1 && break; sleep 0.2; done
for i in 1 2; do echo "redis back: $(chat "$OPS" "$FAST")"; done
code
echo "Gateway log (limit store):"
code json; since "$m" | grep 'limit store' | jq -c '{t: .time[11:23], level, msg, fail_open, error}'; code

h "13. Usage ledger (Postgres) and graceful shutdown"
logged=$(grep -c '"msg":"request"' "$TMP/gw.log")
kill -TERM "$GWPID"; wait "$GWPID" 2>/dev/null
code json; grep -E 'usage ledger closed|shutdown complete' "$TMP/gw.log" | jq -c '{msg, written, dropped, failed}'; code
rows=$(docker exec llm-gw-postgres psql -tAU gateway -d gateway -c 'SELECT count(*) FROM usage_records')
echo "Request records in the gateway log: $logged. Rows in usage_records: $rows."
echo
code
docker exec llm-gw-postgres psql -U gateway -d gateway -c \
  "SELECT provider, status, count(*), sum(attempts) AS attempts, sum(failovers) AS failovers, sum(cost_usd) AS cost_usd, sum(saved_usd) AS saved_usd FROM usage_records GROUP BY 1,2 ORDER BY 3 DESC"
code
echo "Privacy check: the prompt text and the mock's answer text in the log and in a dump of the table."
code
echo "gateway log:     'phase2-secret-prompt' $(grep -c 'phase2-secret-prompt' "$TMP/gw.log") times; 'quick brown fox' $(grep -c 'quick brown fox' "$TMP/gw.log") times"
dump=$(docker exec llm-gw-postgres pg_dump -U gateway -d gateway -t usage_records --data-only)
echo "usage_records:   'phase2-secret-prompt' $(grep -c 'phase2-secret-prompt' <<<"$dump") times; 'quick brown fox' $(grep -c 'quick brown fox' <<<"$dump") times"
code

h "14. Redis down with on_store_error: fail_closed"
write_config fail_closed
start_gateway "$TMP/config.yaml"
m=$(mark)
docker stop llm-gw-redis >/dev/null
code; chat "$OPS" "$FAST" | tr -d '\n'; echo " error.code=$(errcode)"; echo "message: $(errmsg)"; code
docker start llm-gw-redis >/dev/null
for _ in $(seq 1 30); do docker exec llm-gw-redis redis-cli ping >/dev/null 2>&1 && break; sleep 0.2; done
code; echo "redis back: $(chat "$OPS" "$FAST")"; code
kill -TERM "$GWPID"; wait "$GWPID" 2>/dev/null
echo
echo "End of run."
