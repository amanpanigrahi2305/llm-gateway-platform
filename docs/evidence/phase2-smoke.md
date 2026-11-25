# Phase 2 evidence: reliability and cost control

Raw output of one end-to-end run on 2026-11-25 01:44 UTC. Two mock providers
(mock-a on :9090, mock-b on :9091) behind alias `fast` = [mock-a, mock-b].
Only the mock provider was used: no LLM credits. Keys are not shown.

## 0. Setup

```
commit:    4299ad4
go:        go1.27.4
redis:     v=8.12.0
postgres:  postgres (PostgreSQL) 18.7
k6:        k6 v2.4.0 (commit/dd9395906d, go1.26.11, linux/amd64)
cpu:       AMD Ryzen AI 9 HX PRO 375 w/ Radeon 890M (24 threads, WSL2)
settings:  max_attempts=2 backoff_base=100ms breaker_failures=5 breaker_cooldown=10s ttft_timeout=1s
```

## 1. Healthy: mock-a serves

```
status=200 provider=mock-a cache=- retry_after=- time=0.254s
```
```json
{"status":200,"provider":"mock-a","attempts":1,"failovers":0,"latency_ms":269,"ttft_ms":0,"error_type":""}
```

## 2. mock-a outage: failover to mock-b, then the breaker opens

PATCH mock-a {"outage":true}, then 8 requests one after another.
```
req 1: status=200 provider=mock-b cache=- retry_after=- time=0.303s
req 2: status=200 provider=mock-b cache=- retry_after=- time=0.247s
req 3: status=200 provider=mock-b cache=- retry_after=- time=0.271s
req 4: status=200 provider=mock-b cache=- retry_after=- time=0.247s
req 5: status=200 provider=mock-b cache=- retry_after=- time=0.245s
req 6: status=200 provider=mock-b cache=- retry_after=- time=0.237s
req 7: status=200 provider=mock-b cache=- retry_after=- time=0.250s
req 8: status=200 provider=mock-b cache=- retry_after=- time=0.237s
```
Usage records (attempts = upstream calls; failovers = targets passed over):
```json
{"status":200,"provider":"mock-b","attempts":3,"failovers":1,"latency_ms":325,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":3,"failovers":1,"latency_ms":263,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":2,"failovers":1,"latency_ms":290,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":1,"failovers":1,"latency_ms":264,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":1,"failovers":1,"latency_ms":263,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":1,"failovers":1,"latency_ms":254,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":1,"failovers":1,"latency_ms":267,"ttft_ms":0,"error_type":""}
{"status":200,"provider":"mock-b","attempts":1,"failovers":1,"latency_ms":254,"ttft_ms":0,"error_type":""}
```
Breaker log:
```json
{"t":"19:44:59.376","provider":"mock-a","from":"closed","to":"open"}
```

## 3. mock-a recovers: half-open probe closes the breaker

Outage off. Wait 10.5 s (cool-down), then 2 requests.
```
req 1: status=200 provider=mock-a cache=- retry_after=- time=0.237s
req 2: status=200 provider=mock-a cache=- retry_after=- time=0.240s
```
```json
{"t":"19:45:09.573","provider":"mock-a","from":"open","to":"half_open"}
{"t":"19:45:09.826","provider":"mock-a","from":"half_open","to":"closed"}
```

## 4. Streaming: failover before the first chunk

```
status=200 provider=mock-b content-type=text/event-stream
data: {"id":"chatcmpl-60f07fa639b9507d8b6810ad","object":"chat.completion.chunk","created":1795571110,"model":"fast","choices":[{"index":0,"delta":{"r

data: {"id":"chatcmpl-60f07fa639b9507d8b6810ad","object":"chat.completion.chunk","created":1795571110,"model":"fast","choices":[{"index":0,"delta":{"c
...
data: [DONE]

```
```json
{"status":200,"provider":"mock-b","attempts":3,"failovers":1,"latency_ms":354,"ttft_ms":139,"error_type":""}
```

## 5. Slow provider: time-to-first-token timeout (1 s) fails over

PATCH mock-a {"ttft_ms":3000}: mock-a sends headers at once but its first token after 3 s.
```
status=200 time=2.139410s provider=mock-b
```
```json
{"status":200,"provider":"mock-b","attempts":3,"failovers":1,"latency_ms":2293,"ttft_ms":2078,"error_type":""}
```

## 6. Which errors are retried

mock-a answers 400 (client error): passed through at once, no retry, no failover.
```
status=400 provider=mock-a cache=- retry_after=- time=0.004s
error.code=upstream_rejected_request
```
mock-a answers 429 (rate limited): retried once, then mock-b serves.
```
status=200 provider=mock-b cache=- retry_after=- time=0.342s
```
```json
{"status":400,"provider":"mock-a","attempts":1,"failovers":0,"latency_ms":2,"ttft_ms":0,"error_type":"upstream_4xx"}
{"status":200,"provider":"mock-b","attempts":3,"failovers":1,"latency_ms":364,"ttft_ms":0,"error_type":""}
```

## 7. Every provider down: 502, then 503 once both breakers are open

```
req 1: status=502 provider=mock-b cache=- retry_after=- time=0.038s error.code=upstream_error
req 2: status=502 provider=mock-b cache=- retry_after=- time=0.122s error.code=upstream_error
req 3: status=502 provider=mock-b cache=- retry_after=- time=0.077s error.code=upstream_error
req 4: status=503 provider=- cache=- retry_after=- time=0.002s error.code=providers_unavailable
```
```json
{"status":502,"provider":"mock-b","attempts":4,"failovers":1,"latency_ms":39,"ttft_ms":0,"error_type":"upstream_error"}
{"status":502,"provider":"mock-b","attempts":4,"failovers":1,"latency_ms":129,"ttft_ms":0,"error_type":"upstream_error"}
{"status":502,"provider":"mock-b","attempts":2,"failovers":1,"latency_ms":80,"ttft_ms":0,"error_type":"upstream_error"}
{"status":503,"provider":"","attempts":0,"failovers":0,"latency_ms":0,"ttft_ms":0,"error_type":"circuit_open"}
```
```json
{"t":"19:45:14.386","provider":"mock-a","from":"closed","to":"open"}
{"t":"19:45:14.415","provider":"mock-b","from":"closed","to":"open"}
```
Both outages off; after the cool-down one request per alias closes both breakers.
```
status=200 provider=mock-a cache=- retry_after=- time=0.236s
status=200 provider=mock-b cache=- retry_after=- time=0.236s
```
```json
{"t":"19:45:25.121","provider":"mock-a","from":"open","to":"half_open"}
{"t":"19:45:25.373","provider":"mock-a","from":"half_open","to":"closed"}
{"t":"19:45:25.410","provider":"mock-b","from":"open","to":"half_open"}
{"t":"19:45:25.662","provider":"mock-b","from":"half_open","to":"closed"}
```

## 8. Rate limit: key with 5 requests per minute

```
req 1: status=200 provider=mock-a cache=- retry_after=- time=0.258s
req 2: status=200 provider=mock-a cache=- retry_after=- time=0.241s
req 3: status=200 provider=mock-a cache=- retry_after=- time=0.241s
req 4: status=200 provider=mock-a cache=- retry_after=- time=0.241s
req 5: status=200 provider=mock-a cache=- retry_after=- time=0.242s
req 6: status=429 provider=- cache=- retry_after=33 time=0.005s
```
429 body:
```json
{"error":{"code":"rate_limit_exceeded","message":"rate limit reached: 5 requests per minute for this key","type":"rate_limit_error"}}
```

## 9. Budget: key with $0.0001 per day

Each mock answer costs $0.000046 (6 input tokens at $1/1M + 20 output tokens at $2/1M).
Requests 1-3 are checked against $0, $0.000046 and $0.000092 spent: all under $0.0001.
```
req 1: status=200 provider=mock-a cache=- retry_after=- time=0.256s
req 2: status=200 provider=mock-a cache=- retry_after=- time=0.238s
req 3: status=200 provider=mock-a cache=- retry_after=- time=0.240s
req 4: status=429 provider=- cache=- retry_after=80071 time=0.003s
```
429 body:
```json
{"error":{"code":"budget_exceeded","message":"daily budget of $0.0001 for this key is used up; it resets at 00:00 UTC","type":"rate_limit_error"}}
```
Redis counter (integer nano-dollars) for the key today:
```
gw:usd:day:budget:2026-11-25 = 138000
```
```json
{"status":200,"cost_usd":0.000046,"error_type":""}
{"status":200,"cost_usd":0.000046,"error_type":""}
{"status":200,"cost_usd":0.000046,"error_type":""}
{"status":429,"cost_usd":0,"error_type":"daily_budget"}
```

## 10. Cache: temperature 0, same request twice

```
status=200 provider=mock-a cache=miss retry_after=- time=0.238s
status=200 provider=mock-a cache=hit retry_after=- time=0.002s
temperature 0.7 (never cached):
status=200 provider=mock-a cache=- retry_after=- time=0.236s
```
```json
{"latency_ms":254,"cache_hit":false,"cost_usd":0.000046,"saved_usd":0}
{"latency_ms":0,"cache_hit":true,"cost_usd":0,"saved_usd":0.000046}
{"latency_ms":252,"cache_hit":false,"cost_usd":0.000046,"saved_usd":0}
```

## 11. Failover under load (k6, 20 virtual users, 20 s)

mock-a outage on at t=5 s, off at t=15 s. Alias fast. max_tokens=5.
```
  █ THRESHOLDS 
    http_req_failed
    ✓ 'rate<0.01' rate=0.00%
    served{provider:mock-a}
    ✓ 'count>=0' count=1861
    served{provider:mock-b}
    ✓ 'count>=0' count=1935
  █ TOTAL RESULTS 
    checks_total.......: 3796    210.941805/s
    checks_succeeded...: 100.00% 3796 out of 3796
    checks_failed......: 0.00%   0 out of 3796
    ✓ status is 200
    CUSTOM
    served.........................: 3796   210.941805/s
      { provider:mock-a }..........: 1861   103.414831/s
      { provider:mock-b }..........: 1935   107.526974/s
    HTTP
    http_req_duration..............: avg=103.93ms med=104.15ms p(95)=109.24ms p(99)=114.29ms max=199.77ms
      { expected_response:true }...: avg=103.93ms med=104.15ms p(95)=109.24ms p(99)=114.29ms max=199.77ms
    http_req_failed................: 0.00%  0 out of 3796
    http_reqs......................: 3796   210.941805/s
    EXECUTION
    iteration_duration.............: avg=105.47ms med=104.45ms p(95)=109.68ms p(99)=117.5ms  max=200.02ms
    iterations.....................: 3796   210.941805/s
    vus............................: 20     min=20        max=20
    vus_max........................: 20     min=20        max=20
    NETWORK
    data_received..................: 1.9 MB 106 kB/s
    data_sent......................: 1.1 MB 63 kB/s
```
k6 exit code: 0

Gateway records during the run:
```
count=1861 200	mock-a
count=1935 200	mock-b
```
Breaker log during the run:
```json
{"t":"19:45:36.046","provider":"mock-a","from":"closed","to":"open"}
{"t":"19:45:43.976","provider":"mock-a","from":"open","to":"half_open"}
{"t":"19:45:44.085","provider":"mock-a","from":"half_open","to":"closed"}
```
Note: WSL2 steps its wall clock back every ~30 s when it syncs with Windows, so log
timestamps can show less than the 10 s cool-down. The breaker, latency_ms, curl and k6
use a monotonic clock and are not affected. Clock steps logged during the run:
```
Nov 24 19:45:34 AmanAP systemd-journald[42]: Time jumped backwards, rotating.
```

## 12. Redis down with on_store_error: fail_open

Key with policy roomy (1000 requests/min, $10/day), so its limits need Redis.
```
redis up:   status=200 provider=mock-a cache=- retry_after=- time=0.250s
redis down: status=200 provider=mock-a cache=- retry_after=- time=0.241s
redis down: status=200 provider=mock-a cache=- retry_after=- time=0.239s
redis down: status=200 provider=mock-a cache=- retry_after=- time=0.239s
redis back: status=200 provider=mock-a cache=- retry_after=- time=0.246s
redis back: status=200 provider=mock-a cache=- retry_after=- time=0.238s
```
Gateway log (limit store):
```json
{"t":"19:45:50.457","level":"ERROR","msg":"limit store is down","fail_open":true,"error":"dial tcp 127.0.0.1:6380: connect: connection refused"}
{"t":"19:45:52.424","level":"INFO","msg":"limit store is back","fail_open":null,"error":null}
```

## 13. Usage ledger (Postgres) and graceful shutdown

```json
{"msg":"shutdown complete","written":null,"dropped":null,"failed":null}
{"msg":"usage ledger closed","written":3839,"dropped":0,"failed":0}
```
Request records in the gateway log: 3839. Rows in usage_records: 3839.

```
 provider | status | count | attempts | failovers |  cost_usd   |  saved_usd  
----------+--------+-------+----------+-----------+-------------+-------------
 mock-b   |    200 |  1948 |     1973 |      1946 | 0.025753000 | 0.000000000
 mock-a   |    200 |  1884 |     1883 |         0 | 0.025205000 | 0.000046000
 mock-b   |    502 |     3 |       10 |         3 | 0.000000000 | 0.000000000
          |    429 |     2 |        0 |         0 | 0.000000000 | 0.000000000
          |    503 |     1 |        0 |         0 | 0.000000000 | 0.000000000
 mock-a   |    400 |     1 |        1 |         0 | 0.000000000 | 0.000000000
(6 rows)

```
Privacy check: the prompt text and the mock's answer text in the log and in a dump of the table.
```
gateway log:     'phase2-secret-prompt' 0 times; 'quick brown fox' 0 times
usage_records:   'phase2-secret-prompt' 0 times; 'quick brown fox' 0 times
```

## 14. Redis down with on_store_error: fail_closed

```
status=503 provider=- cache=- retry_after=5 time=0.004s error.code=limit_store_unavailable
message: the gateway cannot check rate limits right now; try again shortly
```
```
redis back: status=200 provider=mock-a cache=- retry_after=- time=0.245s
```

End of run.
