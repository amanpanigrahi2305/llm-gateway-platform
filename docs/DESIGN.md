# Design decisions — LLM Gateway Platform

Each phase adds a short entry here: the decision, the options we had,
why we chose this one, and the trade-off. Plain, short English.

## Prior art — what this project does differently

Other projects solve the same problem. No code is copied from any of them.

| Project | What it is |
|---|---|
| AWS "Guidance for Multi-Provider Generative AI Gateway on AWS" | A reference deployment of LiteLLM on ECS/EKS with RDS, ElastiCache, Secrets Manager, CloudFront/WAF, deployed with Terraform. |
| LiteLLM | A Python proxy and SDK with a very large provider list, virtual keys and budgets. |
| Portkey | An AI gateway (open-source gateway plus a hosted platform) with routing, fallbacks and guardrails. |
| Kong AI Gateway | AI plugins on top of the Kong API gateway. |
| Cloudflare AI Gateway | A managed service at Cloudflare's edge (caching, rate limits, analytics). Not self-hosted. |

What this project does differently:

- **Reliability you can check.** SLOs, burn-rate alerts, chaos tests, a written
  postmortem and automatic canary rollback, with the raw evidence in `docs/evidence/`.
- **Privacy by default.** The gateway never stores prompt or response text.
  Only metadata is logged, and a test fails if prompt text reaches the log.
- **Small Go codebase.** One static binary with four direct dependencies
  (YAML, the Anthropic SDK, Redis and Postgres clients), meant to be read end to end.
- **A $0 path.** A mock provider and Ollama on a laptop, the same Helm chart
  on any Kubernetes cluster, and Terraform for AWS.

The trade-off: far fewer providers and features than LiteLLM or Portkey. This
project goes deep on operations, not wide on integrations.

## Part A — Setup
**Decision:** build inside WSL2 Ubuntu; install dev tools to user space (`~/.local`).
**Options:** native Windows build; `sudo apt` / system-wide installs.
**Why:** Kubernetes, kind, Helm and make work best on Linux; user-space installs
need no admin password, so setup stays reproducible without root.
**Trade-off:** tools live in the user profile, not system-wide; PATH is set in
`~/.bashrc` / `~/.profile`.

## Phase 1 — Gateway core

### Go and the standard library
**Decision:** Go with `net/http` (method-and-path routing is built in since Go 1.22).
Only two direct dependencies: a YAML parser and the official Anthropic SDK.
**Options:** Python (the LiteLLM route); a Go web framework such as gin or echo.
**Why:** one static binary, low memory, small container images later, and
goroutines fit many long streaming connections well.
**Trade-off:** fewer ready-made LLM libraries than Python, so adapters are
written here.

### One internal request shape, one adapter per protocol
**Decision:** every provider implements `Chat` and `ChatStream` on one internal
(OpenAI-shaped) request. OpenAI, Ollama (`/v1`) and the mock speak the same
protocol, so one OpenAI-compatible adapter serves all three. Anthropic uses the
official Go SDK.
**Options:** a hand-written HTTP client for Anthropic too; a generic LLM library.
**Why:** the SDK follows API changes. For example, API version `2026-10-21`
rejects `temperature` and `top_p`, and the SDK sends that version. The shared
OpenAI protocol needs only one small client.
**Trade-off:** the SDK brings indirect dependencies, and translation drops
features not supported yet (tools, images).

### Clients use aliases, never raw model names
**Decision:** a request names an alias (`fast`, `local`); config maps it to an
ordered list of `provider/model` targets.
**Options:** let clients send `provider/model` directly.
**Why:** operators change models without redeploying apps, and the ordered
list is where Phase 2 failover goes.
**Trade-off:** a new model needs a config change before clients can use it.

### Client keys stored as SHA-256 hashes
**Decision:** the config holds only `sha256(key)`. `gateway gen-key` prints a new
key once; `gateway hash-key` reads a key from stdin, not from an argument, so
it stays out of shell history and the process list.
**Options:** bcrypt or argon2; plaintext keys; a database.
**Why:** keys are 256-bit random values. Slow hashes protect weak human
passwords; here they would only add latency to every request.
**Trade-off:** rotating a key means a config change and a restart (until a key
store exists).

### Privacy by default
**Decision:** each request logs one usage record: key, team, alias, provider,
model, tokens, cost, latency, status. No prompt or answer text, and no upstream
error text (only a short `error_type`).
**Why:** prompts can hold personal or secret data. Not storing them removes the
risk.
**Trade-off:** a bad prompt is harder to debug from the gateway side.

### Cost is an estimate from a price table
**Decision:** cost = tokens × USD per 1M tokens from the config. Every alias
target must have a price (0 is allowed), so a paid model is never reported as
$0 by mistake. If a provider reports no usage, tokens are estimated at about
4 characters per token and the record is marked `estimated`.
**Trade-off:** the provider's bill is the source of truth. Anthropic cached
input is counted at the normal input price (a small overestimate), and tiered
prices are not modeled.

### Streaming
**Decision:** headers are sent only when the first chunk arrives. An upstream
failure before that returns a normal JSON error with the right status. After
that, the `200` is already sent, so the error goes inside the stream (like
OpenAI does) and the stream ends without `[DONE]`. Upstream usage is requested
with `stream_options.include_usage`.
**Why:** clients get correct status codes whenever it is still possible.
**Trade-off:** time to first byte includes the provider's time to first token.
(Phase 2 explains why a stream is never retried after it starts.)

### Anthropic translation
**Decision:** system and developer messages become the top-level `system`
field; `temperature` and `top_p` are dropped; `max_tokens` defaults to 16,000
(64,000 when streaming); `refusal` maps to `content_filter`; SDK retries are off.
**Why:** API version `2026-10-21` rejects sampling parameters. Current models
always think, and thinking tokens count toward `max_tokens`, so small defaults
would cut answers short. Retries belong to the gateway (Phase 2), not to each SDK.
**Not done:** Anthropic's server-side refusal fallback. The gateway owns
routing, and cost accounting must know which model answered.

### Status codes
**Decision:** an upstream 400/413/422 (the request's own fault) is passed
through. Upstream 401/403 (the gateway's own provider key is wrong) and 404
(the alias points at a missing model) become 502. Timeouts become 504. A client
that disconnects is logged as 499.
**Why:** the client should only see a 4xx when the client can fix it.

### Health checks and graceful shutdown
**Decision:** `/healthz` (liveness) answers while the process runs. `/readyz`
(readiness) turns to 503 on SIGTERM. Then the gateway waits `drain_delay`, and
`http.Server.Shutdown` lets open requests and streams finish for up to
`shutdown_timeout`. There is no `WriteTimeout`, because it would cut long streams.
**Why:** Kubernetes stops sending traffic to a pod that is not ready. The pod
then finishes what it has before it exits.
**Measured:** a 3-second stream started before SIGTERM finished with all 15
chunks and `[DONE]`; `/readyz` returned 503 during the drain; the process exited
2.6 s after SIGTERM (`docs/evidence/phase1-smoke.md`, section 11).

## Phase 2 — Reliability and cost control

Evidence for every number below: `docs/evidence/phase2-smoke.md`.

### Retries and failover
**Decision:** the router tries the alias's targets in order. Each error is put
in one of three groups:
- **Temporary** (408, 429, 5xx, timeout, connection error): retry the same
  target, then fail over to the next one. Default: 2 tries per target.
- **Broken target** (401, 403, 404): the gateway's key or model name is wrong
  for this provider. A retry would fail the same way, so fail over at once.
- **Client error** (400, 413, 422 and other 4xx): the request itself is wrong.
  Every provider would reject it, so the client gets the error at once.

The wait between tries is random between 0 and 100 ms × 2^(n-1), capped at 2 s
("full jitter"). Without the random part, all requests that failed together
would retry together and hit a recovering provider at the same moment.
**Options:** retry only, with no failover; fail over only, with no retry; a
retry library.
**Why:** a short blip is often gone on the next try; a real outage needs another
provider. The rules are 60 lines of code.
**Trade-off:** a failed try adds latency. Measured (`latency_ms`): the first
failover requests took 263–325 ms; once the breaker was open, 254–267 ms, the
same as a healthy request (269 ms).

### Never retry a stream after its first chunk
**Decision:** a streaming request may be retried or failed over only before the
first chunk reaches the client. After that, an error ends the stream (an error
event and no `[DONE]`).
**Why:** the client has already shown part of an answer. A retry produces a new,
different answer from the start. The gateway cannot join the two, and sending
both would give the user broken text. The client knows what it has shown, so
only the client can decide to retry.
**Trade-off:** a provider that fails in the middle of a stream is not hidden from
the client.

### Circuit breaker per provider
**Decision:** each provider has a breaker. It opens after 5 failures in a row
(temporary errors only). While open, requests skip the provider and fail over
at once. After a 30 s cool-down it lets **one** probe request through
(half-open): success closes it, failure opens it again for a new cool-down.
When every target's breaker is open, the client gets 503 `providers_unavailable`
at once.
**Options:** failure rate over a time window; no breaker (retries only).
**Why:** without a breaker, every request first waits for the dead provider to
fail (or time out) before it fails over. "N failures in a row" is easy to
explain and to test. One probe means a sick provider gets one request, not a
flood, while it recovers.
**What counts:** 5xx, 429, timeouts and connection errors are failures. A 400 is
the client's fault and counts as a success: the provider answered. A client that
disconnects counts as neither.
**Trade-off:** a provider that fails every other request never trips a
"N in a row" breaker; a rate-based breaker would catch that.
**Measured:** with mock-a down, the breaker opened during the 3rd request. From
then on, each request needed 1 attempt instead of 3. After the outage ended, the
probe closed it again. With both mocks down: 502 three times, then 503 in 2 ms.

### Three timeouts per provider
**Decision:** connect (5 s, TCP + TLS), time to first token for streams (30 s)
and total (5 min). The first-token timer is in the router, not in each adapter,
so it works the same for every provider. When it fires, the try counts as a
timeout: it is retried or failed over, and if nothing works the client gets 504.
**Why:** a provider that accepts the connection and then sends nothing is the
worst case. Without a first-token limit, the client waits the full 5 minutes.
**Trade-off:** models that think before they answer send their first text token
late. Set `ttft_timeout` above their thinking time, or real answers get cut off.
**Measured:** with mock-a's first token at 3 s and `ttft_timeout: 1s`, the stream
was served by mock-b after 2.14 s (two 1 s tries, then mock-b).

### Rate limits live in Redis
**Decision:** per-key requests/minute and tokens/minute counters are kept in
Redis, shared by all replicas.
**Why not in memory:** with N replicas behind a load balancer, each replica
would count only its own share of the traffic. A key with a limit of 60/min
could then send 60 × N per minute. Redis gives every replica the same count.
**How:** fixed windows (one counter per key per UTC minute). The request counter
is `INCR` then compare, inside `MULTI/EXEC` with `EXPIRE … NX`, so two replicas
can never both let through the "last" request. Tokens are only known after the
answer, so they are added afterwards, and the check uses what is already used.
**Trade-off:** a fixed window lets a client send its full limit at the end of one
minute and again at the start of the next (up to 2× in a short burst). A sliding
window or token bucket is smoother but needs more Redis work per request. One
request can overshoot the token limit; the next one is refused.
**Measured:** a key with 5 requests/minute got 5 × 200, then 429 with
`Retry-After: 33` (seconds to the next minute).

### Budgets
**Decision:** daily and monthly USD per key, also Redis counters (UTC day, UTC
month). Money is counted in whole nano-dollars (1e-9 USD), because Redis counts
integers and integers do not drift like floats. When a budget is used up, the
client gets 429 `budget_exceeded` with a clear message and `Retry-After` until
the reset.
**Trade-off:** like tokens, cost is known only after the answer, so the last
request can go over the budget; the next one is refused. Redis is the counter,
the Postgres ledger is the record: if Redis loses its data, budgets restart from
zero (rebuilding them from the ledger is not done).
**Measured:** a key with $0.0001/day: 3 requests at $0.000046 each, then 429
`budget_exceeded`.

### When Redis is down: fail open by default
**Decision:** `limits.on_store_error` is `fail_open` (default) or `fail_closed`.
Fail open lets requests through without limits; fail closed refuses them with
503 `limit_store_unavailable`. Redis calls have 200 ms timeouts and no retries, so
a dead Redis is noticed in milliseconds. The outage is logged once when it
starts and once when it ends, not once per request.
**Why fail open:** the gateway exists to keep AI features running. A Redis outage
should not turn into an outage of every app behind the gateway. The risk is
bounded: spend can go over budget only for as long as Redis is down, and Phase 5
adds an alert (`LimitStoreDown`).
**When to choose fail closed:** when a hard spend cap matters more than uptime,
for example a free tier open to the public.
**Measured:** with Redis stopped, fail open gave 200 in 239–241 ms (250 ms with
Redis up), so no added latency; the 200 ms Redis timeout is the upper bound.
Fail closed gave 503 in 4 ms. Both recovered on their own when Redis came back.

### Response cache (simple, off by default)
**Decision:** exact-match cache in each replica's memory (LRU with a TTL). It is
**off by default**, because it keeps answer text, and this project stores no
prompt or answer text by default. Safety rules: only non-streaming requests with
`temperature: 0`; the key is a SHA-256 of the client key ID, alias and request,
so one key never gets another key's answer and prompts are not stored.
**Why temperature 0 only:** with a higher temperature the client wants a new
answer each time. Serving a stored one would silently change what they asked for.
**Options:** a shared cache in Redis (more hits across replicas, but answer text
would sit in Redis).
**Trade-off:** each replica has its own cache, so more replicas mean fewer hits.
Unlike rate limits, that is fine: a miss only costs money, it is never wrong.
**Measured:** a repeated temperature-0 request: 238 ms (miss), then 2 ms (hit),
with `saved_usd` = $0.000046 in the record.

### Usage ledger in Postgres, written in the background
**Decision:** every usage record also goes to the `usage_records` table
(metadata only). A request never waits for the database: it puts the record in
a buffered channel (10,000 records) and one worker writes batches with `COPY`
(every 500 rows or every second). When the buffer is full, records are dropped
and counted, never blocked on. Shutdown writes what is left.
**Options:** insert inside the request (simple, but a slow database slows every
request); a message queue such as Kafka (durable, but one more system to run).
**Why:** serving requests matters more than the ledger, and the JSON log still has
every record. The table is created on the first write under a Postgres advisory
lock, so replicas that start together do not race, and the gateway starts even
while Postgres is down.
**Trade-off:** records in the buffer are lost if the process is killed hard
(`SIGKILL`). A graceful stop writes them.
**Measured:** 3,839 records in the log and 3,839 rows in the table, 0 dropped,
0 failed. The prompt text was found 0 times in the log and in a dump of the table.

### Failover under load
**Measured** (k6, 20 virtual users, 20 s, mock-a outage from second 5 to second
15): 3,796 requests, **0 failed**, p95 109 ms, max 200 ms. mock-a served 1,861
and mock-b 1,935. Laptop: AMD Ryzen AI 9 HX PRO 375, WSL2.

## Phase 3 — Containers + Kubernetes

Evidence: `docs/evidence/phase3-compose.md` (laptop stack) and
`docs/evidence/phase3-kind.md` (kind cluster).

### Small distroless images
**Decision:** a two-stage Dockerfile. Stage 1 compiles static Go binaries
(`CGO_ENABLED=0`); stage 2 copies them into `distroless/static-debian13:nonroot`.
The final image has no shell, no package manager and runs as user 65532. The
build stage runs on the build machine's own CPU and cross-compiles, so an arm64
image builds fast on an amd64 laptop (no emulation).
**Options:** Alpine (has a shell, so easy to debug), `scratch` (smaller, but no
CA certificates, time zones or non-root user unless added by hand), a full
Debian or Ubuntu image.
**Why:** fewer files means fewer CVEs for Trivy to report (Phase 7) and nothing
for an attacker to use if they get in: no shell to open.
**Trade-off:** `kubectl exec ... sh` does not work. Debug with
`kubectl debug --image=busybox --target=gateway` (an ephemeral container that
shares the pod's process view) instead.
**Measured:** gateway 7.41 MB compressed (amd64), 6.87 MB (arm64); mock 3.79 MB.
Both architectures built in 86 s.

### A laptop stack with Docker Compose
**Decision:** `make compose-up` starts the gateway, Redis, Postgres, Ollama and
two mocks. Every port is bound to 127.0.0.1, and every app container has a
read-only root, no Linux capabilities and `no-new-privileges`.
**Why:** a new developer gets the full system with one command and no cloud
account. Ollama gives a real local model at no cost.
**Measured:** ready in 22.8 s; a mock request and an Ollama request (4.2 s cold,
0.12 s warm) both worked, and failover worked inside the stack.

### One Helm chart; test services behind a flag
**Decision:** one chart deploys the gateway anywhere. Redis, Postgres and the two
mocks are in the same chart under `dev.*`, off by default; the kind values turn
them on. In production, Redis and Postgres come from outside (managed services
in Phase 6) through an existing Secret.
**Options:** Bitnami sub-charts for Redis and Postgres; a separate "dev" chart.
**Why:** the test services are about 60 lines each, run as non-root with a
read-only root, and keep no data (Postgres uses an `emptyDir`). A sub-chart
would add hundreds of settings we do not need.
**Trade-off:** the dev Postgres loses its data when its pod moves. That is fine
for tests and the reason it is never enabled outside a local cluster.

### Probes: liveness never checks Redis or Postgres
**Decision:** three probes.
- **Startup** (`/healthz`, every 1 s, up to 30 s): the other probes wait for it.
- **Liveness** (`/healthz`): is the process alive? It never checks Redis,
  Postgres or providers.
- **Readiness** (`/readyz`, every 2 s): should this pod get traffic? It turns
  false when shutdown starts.
**Why:** if liveness checked Redis, a Redis outage would make Kubernetes restart
**every** gateway pod at once and turn a small problem (limits fail open) into a
full outage. A restart only helps a stuck process, so that is all liveness asks.

### Graceful shutdown on Kubernetes
**Decision:** on SIGTERM the gateway turns `/readyz` to 503, keeps serving for
`drain_delay` (5 s), then stops taking new connections and lets open requests
and streams finish (up to 30 s). Last, it writes what is left in the ledger
buffer. `terminationGracePeriodSeconds` is 45 s, more than 5 + 30.
**Why the 5 s wait:** when a pod is deleted, Kubernetes sends SIGTERM and removes
the pod from the Service at the same time. kube-proxy on every node needs a
moment to update its rules. A pod that stopped at once would refuse requests that
were already routed to it.
**Options:** a `preStop` sleep hook (the image has no `sleep` binary; Kubernetes
1.30+ has a built-in sleep action that would also work).
**Trade-off:** each pod takes at least 5 s to stop, so rollouts are slower.
**Measured:** both original pods deleted one after the other under steady load
(20 virtual users, 60 s): 11,400 requests, **0 failed**, p95 107.80 ms. Each
replacement was ready in about 3 s. Both stopping pods wrote their ledger buffer
(0 dropped), and the table gained exactly 11,400 rows.

### Rollouts never drop below the wanted number of pods
**Decision:** `maxUnavailable: 0, maxSurge: 1`: a new pod must be ready before an
old one is stopped. A PodDisruptionBudget (`maxUnavailable: 1`) stops a node
drain from taking down both pods at once.
**Trade-off:** a rollout needs room for one extra pod.
**Measured:** `kubectl rollout restart` under the same load: 11,380 requests,
**0 failed**; ready endpoints never fell below 2.

### Spread pods across nodes: a rule, not a wish
**Decision:** a topology spread on `kubernetes.io/hostname` with `maxSkew: 1`,
`whenUnsatisfiable: DoNotSchedule`, `matchLabelKeys: [pod-template-hash]` and
`nodeTaintsPolicy: Honor`.
**What went wrong first:** with `ScheduleAnyway`, a rollout put **both** gateway
pods on one worker. `ScheduleAnyway` only adds to a node's score, and the
scheduler liked the less busy node better for both pods. One node failure would
then have taken every replica, which the PodDisruptionBudget cannot prevent
(it only limits voluntary evictions).
**Why the other two settings:** the spread counts pods that match the label
selector, and old and new pods of a rollout share those labels. Without
`pod-template-hash`, the new pods are placed to balance the *old* ones and can
still end up together. Without `nodeTaintsPolicy: Honor`, the tainted control
plane counts as an empty node, and `DoNotSchedule` would refuse a second pod on
a worker.
**Trade-off:** if only one node can take pods, `DoNotSchedule` still works (one
domain has no skew); but with many replicas and too few nodes, pods can stay
`Pending` instead of crowding one node.
**Measured:** one pod per worker after a deploy and after each of 3 rolling
restarts.

### Keep-alive connections and kube-proxy: ask clients to reconnect
**Decision:** `server.max_connection_age` (default 30 s, ±10% per connection).
After that age, the next response carries `Connection: close`; Go's server closes
the connection after that response and the client opens a new one. During the
shutdown drain, every response carries it.
**What went wrong first:** in the first HPA test the HPA did its job (2 → 4 → 6
pods), but the new pods got almost no traffic. At the end of the load the 2
original pods used 998m and 995m CPU (their 1-core limit) and the 4 new pods
2–3m. kube-proxy picks a pod once per TCP connection; k6's 100 keep-alive
connections were opened at the start and never moved. Real clients with
connection pools (OpenAI SDKs, other services) behave the same way.
**Options:** a layer-7 proxy that balances each request (an Ingress controller or
a service mesh such as Linkerd); clients that open a new connection per request
(slow, and we do not control clients); `max_connection_age` in the server.
**Why:** it is about 30 lines, works with any client and any load balancer, and is
the same idea as gRPC's `MaxConnectionAge` and Envoy's `max_connection_duration`.
The jitter stops a pool that connected all at once from reconnecting all at once.
**Trade-off:** one extra TCP handshake (and TLS handshake, if TLS ends at the pod)
per connection every 30 s, which is nothing next to an LLM call. Load moves to new
pods only as connections age, so it takes up to ~33 s to rebalance.

**Measured:** the same HPA test (100 virtual users, 180 s) before and after:
1,853 → 3,814 requests/s, p95 97.25 → 61.22 ms, and at the end of the load all 6
pods were busy (614–932m CPU) instead of 2.

### HPA on CPU: scale up fast, scale down slowly
**Decision:** `autoscaling/v2` on CPU, target 70% of the 250m request, 2 to 6
pods. Scale up: no waiting window, up to +100% every 15 s. Scale down: a 120 s
window, then at most 1 pod every 30 s.
**Options:** scale on requests per second or on requests in flight (needs the
Phase 4 metrics plus KEDA or the Prometheus Adapter); a fixed replica count.
**Why CPU first:** it needs only metrics-server, and the gateway's CPU does follow
its request rate (3,814 requests/s used about 5 cores). Slow scale-down stops the
pod count from going up and down with short dips.
**Trade-off:** an LLM gateway mostly *waits* for providers. With real models
(seconds per answer) many requests can be in flight while CPU stays low, so CPU
under-reports load. Requests in flight is the better signal; switch to it once
Phase 4 exports it.
**Measured:** 2 → 4 pods at the first CPU reading under load (t=32 s), 4 → 6 at
t=45 s; 688,351 requests, **0 failed**, p95 61.22 ms. Back to 2 pods 255 s after
the load ended (CPU readings lag 30–45 s behind the load, then the 120 s window,
then 1 pod per 30 s).

### Pod security
**Decision:** every pod runs as non-root with a read-only root filesystem, all
Linux capabilities dropped, no privilege escalation and the `RuntimeDefault`
seccomp profile. The service account token is not mounted (the gateway never
calls the Kubernetes API).
**Why:** these settings pass the Kubernetes "restricted" Pod Security Standard,
and a stolen token cannot be used if it is not there.
**Measured:** on kind, the gateway and mocks run as uid 65532, Redis as 999 and
Postgres as 70, all with a read-only root.

### kind for local Kubernetes
**Decision:** a 3-node kind cluster (1 control plane, 2 workers) created by
`make kind-up`, with metrics-server for the HPA. `make deploy` builds the images,
loads them into the nodes and installs the chart, with a new image tag each time,
so only the gateway pods roll and the test Postgres keeps its data.
**Options:** minikube (one node by default), k3d (k3s, not upstream Kubernetes),
Docker Desktop's built-in Kubernetes (one node).
**Why:** kind runs upstream Kubernetes with several nodes in Docker, and it also
runs in GitHub Actions (Phase 7). Two workers make pod spread and drains real.
The node image is pinned by digest, so every run gets the same Kubernetes build.
**Trade-off:** traffic from the laptop reaches the NodePort through Docker's port
forwarder, so latency numbers include that hop and are not cloud numbers.

## Phase 4 — Observability
_TBD_

## Phase 5 — SRE
_TBD_

## Phase 6 — Terraform / AWS
_TBD_

## Phase 7 — CI / GitOps / release
_TBD_

## Phase 9 — README and docs
_TBD_
