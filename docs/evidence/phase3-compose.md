# Phase 3 evidence: docker compose stack

Raw output of one run on 2026-11-25 01:59 UTC, commit 640c539.
The demo key `sk-gw-local-demo-only` is public on purpose (local stack only).

## 1. make compose-up

```
 Container llm-gateway-redis-1  Started
 Container llm-gateway-ollama-1  Started
 Container llm-gateway-ollama-1  Waiting
 Container llm-gateway-postgres-1  Started
 Container llm-gateway-mock-b-1  Started
 Container llm-gateway-mock-a-1  Started
 Container llm-gateway-postgres-1  Waiting
 Container llm-gateway-redis-1  Waiting
 Container llm-gateway-redis-1  Healthy
 Container llm-gateway-ollama-1  Healthy
 Container llm-gateway-ollama-pull-1  Starting
 Container llm-gateway-ollama-pull-1  Started
 Container llm-gateway-postgres-1  Healthy
 Container llm-gateway-gateway-1  Starting
 Container llm-gateway-gateway-1  Started
```
From `make compose-up` (images already built once) to gateway ready: 22.8 s

```
SERVICE    IMAGE                   STATUS                   PORTS
gateway    llm-gateway:dev         Up 1 second              127.0.0.1:8080->8080/tcp
mock-a     llm-gateway-mock:dev    Up 9 seconds             127.0.0.1:9090->9090/tcp
mock-b     llm-gateway-mock:dev    Up 9 seconds             127.0.0.1:9091->9090/tcp
ollama     ollama/ollama:0.40.17   Up 9 seconds (healthy)   11434/tcp
postgres   postgres:18-alpine      Up 9 seconds (healthy)   5432/tcp
redis      redis:8-alpine          Up 9 seconds (healthy)   6379/tcp
```

## 2. Requests: mock (alias fast) and Ollama (alias local)

```
ollama-pull: ollama-pull exited exit=0
model=fast status=200 provider=mock-a time=0.309089s answer="The quick brown fox jumps over the lazy dog while the mock 
model=local status=200 provider=ollama time=4.224632s answer="Hello, how are you?"
model=local status=200 provider=ollama time=0.118885s answer="Hello, how are you?"
```
The first Ollama call loads the model into memory; the second one is warm.

## 3. Failover inside the stack

```
model=fast status=200 provider=mock-b time=0.289702s answer="The quick brown fox jumps over the lazy dog while the mock 
model=fast status=200 provider=mock-b time=0.335936s answer="The quick brown fox jumps over the lazy dog while the mock 
```
```
after reset:
model=fast status=200 provider=mock-a time=0.241174s answer="The quick brown fox jumps over the lazy dog while the mock 
```

## 4. Usage ledger rows in the stack's Postgres

```
 alias | provider | status | attempts | failovers | latency_ms |  cost_usd   
-------+----------+--------+----------+-----------+------------+-------------
 fast  | mock-a   |    200 |        1 |         0 |        325 | 0.000046000
 local | ollama   |    200 |        1 |         0 |       4542 | 0.000000000
 local | ollama   |    200 |        1 |         0 |        120 | 0.000000000
 fast  | mock-b   |    200 |        3 |         1 |        305 | 0.000046000
 fast  | mock-b   |    200 |        3 |         1 |        358 | 0.000046000
 fast  | mock-a   |    200 |        1 |         0 |        254 | 0.000046000
(6 rows)

```

## 5. Container hardening

```
gateway: user=65532 read_only=true cap_drop=[ALL] security_opt=[no-new-privileges:true]
mock-a: user=65532 read_only=true cap_drop=[ALL] security_opt=[no-new-privileges:true]
shell in the gateway container: OCI runtime exec failed: exec failed: unable to start container process: exec: "sh": executable file not found in $PATH: unknown
listening ports on the host:
  127.0.0.1:8080
  127.0.0.1:9090
  127.0.0.1:9091
```

## 6. Gateway log (startup)

```json
{"time":"2026-11-25T02:00:00.890645751Z","level":"INFO","msg":"gateway listening","version":"dev","addr":":8080","aliases":["fast","local"],"providers":3}
```

## 7. make compose-down

```
 Container llm-gateway-mock-a-1  Removing
 Container llm-gateway-redis-1  Stopped
 Container llm-gateway-redis-1  Removing
 Container llm-gateway-mock-a-1  Removed
 Container llm-gateway-redis-1  Removed
 Container llm-gateway-postgres-1  Stopped
 Container llm-gateway-postgres-1  Removing
 Container llm-gateway-postgres-1  Removed
 Network llm-gateway_default  Removing
 Network llm-gateway_default  Removed
```

End of run.
