# Phase 7a evidence: release supply chain, tested on the laptop

Raw output of `docs/evidence/phase7a-supply-chain.sh` (2026-11-26 03:20 UTC,
commit 39d29d1 plus the uncommitted release workflow and this script).

It runs the same steps as `.github/workflows/release.yml` with two local
stand-ins, by choice: a throwaway registry on 127.0.0.1:5001 instead of GHCR,
and a cosign key pair made for the run instead of keyless signing (keyless
needs the GitHub Actions OIDC token). cosign used a signing config with no
Fulcio, no transparency log and no timestamp service, so nothing was sent to
Sigstore's public services. The real keyless run happens on the first version
tag. "build + push: 9 s" used the build cache; the first run took 28 s.

What it shows:
- both images are multi-arch (amd64 + arm64) and pushed by digest
- Syft SBOM (SPDX 2.3) lists 37 packages, including every Go module; Trivy
  finds 0 vulnerabilities in it
- signatures and the SBOM attestation verify against the public key
- verification fails for the wrong key, for an unsigned image, and when the
  release tag is moved to another image (signatures belong to a digest)
- the Helm chart is pushed as an OCI artifact and signed the same way

```
## 1. Throwaway local registry (stand-in for GHCR)
registry up: 200

## 2. Multi-arch build and push (linux/amd64 + linux/arm64)
build + push: 9 s
gateway localhost:5001/llm-gateway@sha256:6e9600e2ec152e5f9e81dae107d383a5ebce2b486b99d8b10deafe911a595ccb
mock    localhost:5001/llm-gateway-mock@sha256:7fa82f876f44d79326a717fc21c1871783018c6e6127c6d365acc4c4f14b6869
  Platform:    linux/amd64
  Platform:    linux/arm64

## 3. SBOM with Syft (SPDX JSON)
gateway SBOM: 37 packages, spdxVersion SPDX-2.3
  base-files 13.8+deb13u8
  github.com/amanpanigrahi2305/llm-gateway-platform UNKNOWN
  github.com/anthropics/anthropic-sdk-go/v2 v2.13.0
  github.com/bahlo/generic-list-go v0.2.0
  github.com/buger/jsonparser v1.1.2
  github.com/cespare/xxhash/v2 v2.3.0
  github.com/go-logr/logr v1.4.2
  github.com/go-logr/stdr v1.2.2
  github.com/invopop/jsonschema v0.14.0
  github.com/jackc/pgpassfile v1.0.0
  github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761
  github.com/jackc/pgx/v5 v5.13.0
  github.com/jackc/puddle/v2 v2.2.3
  github.com/pb33f/ordered-map/v2 v2.3.1
  github.com/redis/go-redis/v9 v9.24.0
  github.com/standard-webhooks/standard-webhooks/libraries v0.0.1
  github.com/tidwall/gjson v1.18.0
  github.com/tidwall/match v1.1.1
  github.com/tidwall/pretty v1.2.1
  github.com/tidwall/sjson v1.2.5
  go.yaml.in/yaml/v3 v3.0.6
  go.yaml.in/yaml/v4 v4.0.0-rc.2
  golang.org/x/sync v0.22.0
  golang.org/x/sys v0.50.0
  golang.org/x/text v0.41.0
  netbase 6.5
  stdlib go1.27.4
  tzdata 2026f-0+deb13u1

## 4. Trivy on the SBOM
│ Target │   Type   │ Vulnerabilities │
│        │ gobinary │        0        │

## 5. Sign and attach the SBOM as a signed attestation
key pair made in a temp dir (removed at exit)
signing config (no external services): {"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json", "rekorTlogConfig":{}, "tsaConfig":{}}
sign gateway: Pushing signature to: localhost:5001/llm-gateway
sign mock:    Pushing signature to: localhost:5001/llm-gateway-mock
attest gateway SBOM: ok
transparency-log lines in attest output: 0

## 6. Verify
verify gateway signature: OK
verify mock signature:    OK
verify SBOM attestation: OK (predicateType https://spdx.dev/Document, 37 packages, subject 6e9600e2ec15...)

## 7. Checks that must FAIL
wrong key: rejected (Error: no matching attestations: failed to verify signature: could not verify envelope: ac)
unsigned image: rejected (no signatures found)
moved tag: rejected (tag now -> sha256:a5b54d89ce81..., which has no signature)

## 8. Helm chart as an OCI artifact, signed
chart oci://localhost:5001/charts/llm-gateway:0.0.0-local digest sha256:eda6a5ee6dae...
sign chart: Pushing signature to: localhost:5001/charts/llm-gateway
verify chart signature: OK
appVersion: v0.0.0-local
name: llm-gateway
version: 0.0.0-local

## 9. Registry contents
{"repositories":["charts/llm-gateway","llm-gateway","llm-gateway-mock"]}

llm-gateway tags: sha256-6e9600e2ec152e5f9e81dae107d383a5ebce2b486b99d8b10deafe911a595ccb tampered v0.0.0-local
llm-gateway-mock tags: sha256-7fa82f876f44d79326a717fc21c1871783018c6e6127c6d365acc4c4f14b6869 v0.0.0-local
charts/llm-gateway tags: 0.0.0-local sha256-eda6a5ee6dae76e81c07faf55502097e70938fd6ea09ac781f0abab900c9ad46
```
