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
- **Small Go codebase.** One static binary with two direct dependencies,
  meant to be read end to end.
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
_TBD_

## Phase 3 — Containers + Kubernetes
_TBD_

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
