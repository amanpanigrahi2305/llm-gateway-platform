# Design decisions — LLM Gateway Platform

Each phase adds a short entry here: the decision, the options we had,
why we chose this one, and the trade-off. Plain, short English.

## Prior art — what this project does differently
_To write in Phase 1. Compare with AWS "Guidance for Multi-Provider Generative AI
Gateway on AWS", LiteLLM, Portkey, Kong AI Gateway, Cloudflare AI Gateway.
Our angle: reliability you can verify (SLOs, chaos tests, postmortem, canary
rollback), written in Go, privacy by default, and a $0 laptop path._

## Part A — Setup
**Decision:** build inside WSL2 Ubuntu; install dev tools to user space (`~/.local`).
**Options:** native Windows build; `sudo apt` / system-wide installs.
**Why:** Kubernetes, kind, Helm and make work best on Linux; user-space installs
need no admin password, so setup stays reproducible without root.
**Trade-off:** tools live in the user profile, not system-wide; PATH is set in
`~/.bashrc` / `~/.profile`.

## Phase 1 — Gateway core
_TBD_

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
