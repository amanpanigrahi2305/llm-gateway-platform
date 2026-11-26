#!/usr/bin/env bash
# Phase 7a evidence: the release workflow's supply-chain steps, run on a laptop.
# Same tools as .github/workflows/release.yml, with two local stand-ins:
#   - a throwaway local registry (127.0.0.1:5001) instead of GHCR
#   - a cosign key pair made for this run instead of keyless signing
#     (keyless needs the GitHub Actions OIDC token)
# Nothing is written to Sigstore's public services: cosign signs with a signing
# config that lists no Fulcio, no transparency log (Rekor) and no timestamp service.
set -u
cd "$(dirname "$0")/../.." || exit 1
REG=localhost:5001
TAG=v0.0.0-local
IMAGE=$REG/llm-gateway
MOCK=$REG/llm-gateway-mock
OUT=${OUT:-/tmp/phase7a}
rm -rf "$OUT"; mkdir -p "$OUT"
KEYS=$(mktemp -d)                      # signing keys live here only, never in the repo
COSIGN_PASSWORD=$(head -c 24 /dev/urandom | base64)
export COSIGN_PASSWORD SYFT_REGISTRY_INSECURE_USE_HTTP=true
trap 'rm -rf "$KEYS"; docker stop llm-gw-registry >/dev/null 2>&1' EXIT

digest() { docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])'; }
sign()   { cosign sign --yes --key "$KEYS/cosign.key" --signing-config "$KEYS/offline.json" --allow-http-registry "$1" 2>&1 | grep -vE '^\s*$' | tail -1; }
verify() { cosign verify --key "${2:-$KEYS/cosign.pub}" --insecure-ignore-tlog=true --allow-http-registry "$1" >/dev/null 2>"$OUT/verify.err"; }

echo "## 1. Throwaway local registry (stand-in for GHCR)"
docker run -d --rm --name llm-gw-registry -p 127.0.0.1:5001:5000 registry:3 >/dev/null
for _ in $(seq 1 30); do curl -sf "http://$REG/v2/" >/dev/null && break; sleep 1; done
echo "registry up: $(curl -s -o /dev/null -w '%{http_code}' "http://$REG/v2/")"

echo
echo "## 2. Multi-arch build and push (linux/amd64 + linux/arm64)"
s=$(date +%s)
docker buildx build -q --platform linux/amd64,linux/arm64 --target gateway \
  --build-arg VERSION=$TAG -t "$IMAGE:$TAG" --push . >/dev/null
docker buildx build -q --platform linux/amd64,linux/arm64 --target mockprovider \
  -t "$MOCK:$TAG" --push . >/dev/null
echo "build + push: $(( $(date +%s) - s )) s"
GW=$(digest "$IMAGE:$TAG"); MK=$(digest "$MOCK:$TAG")
echo "gateway $IMAGE@$GW"
echo "mock    $MOCK@$MK"
docker buildx imagetools inspect "$IMAGE:$TAG" | grep -E 'Platform:' | grep -v unknown

echo
echo "## 3. SBOM with Syft (SPDX JSON)"
syft -q "$IMAGE@$GW" -o spdx-json="$OUT/sbom-gateway.spdx.json"
syft -q "$MOCK@$MK" -o spdx-json="$OUT/sbom-mock.spdx.json"
python3 - "$OUT/sbom-gateway.spdx.json" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
pk = d["packages"]
print(f"gateway SBOM: {len(pk)} packages, spdxVersion {d['spdxVersion']}")
for p in pk:
    n = p.get("name", "")
    if n.startswith(("github.com/", "go.yaml.in", "stdlib", "golang.org")) or n in ("base-files", "tzdata", "netbase"):
        print(f"  {n} {p.get('versionInfo', '')}")
EOF

echo
echo "## 4. Trivy on the SBOM"
trivy sbom --quiet --scanners vuln "$OUT/sbom-gateway.spdx.json" 2>&1 | grep -E 'Total:|│ .*│' | head -6

echo
echo "## 5. Sign and attach the SBOM as a signed attestation"
(cd "$KEYS" && cosign generate-key-pair >/dev/null 2>&1) && echo "key pair made in a temp dir (removed at exit)"
cosign signing-config create --out "$KEYS/offline.json" 2>/dev/null
echo "signing config (no external services): $(cat "$KEYS/offline.json")"
echo "sign gateway: $(sign "$IMAGE@$GW")"
echo "sign mock:    $(sign "$MOCK@$MK")"
cosign attest --yes --key "$KEYS/cosign.key" --signing-config "$KEYS/offline.json" --allow-http-registry \
  --type spdxjson --predicate "$OUT/sbom-gateway.spdx.json" "$IMAGE@$GW" >/dev/null 2>"$OUT/attest.err" \
  && echo "attest gateway SBOM: ok" || { echo "attest failed:"; tail -3 "$OUT/attest.err"; }
grep -ciE 'tlog entry created|rekor' "$OUT/attest.err" | sed 's/^/transparency-log lines in attest output: /'

echo
echo "## 6. Verify"
verify "$IMAGE@$GW" && echo "verify gateway signature: OK" || { echo "verify gateway: FAILED"; tail -2 "$OUT/verify.err"; }
verify "$MOCK@$MK"  && echo "verify mock signature:    OK" || { echo "verify mock: FAILED"; tail -2 "$OUT/verify.err"; }
cosign verify-attestation --key "$KEYS/cosign.pub" --insecure-ignore-tlog=true --allow-http-registry \
  --type spdxjson "$IMAGE@$GW" 2>/dev/null > "$OUT/attestation.json" && python3 - "$OUT/attestation.json" <<'EOF'
import base64, json, sys
env = json.loads(open(sys.argv[1]).readline())
stmt = json.loads(base64.b64decode(env["payload"]))
print(f"verify SBOM attestation: OK (predicateType {stmt['predicateType']}, "
      f"{len(stmt['predicate']['packages'])} packages, subject {stmt['subject'][0]['digest']['sha256'][:12]}...)")
EOF

echo
echo "## 7. Checks that must FAIL"
(cd "$KEYS" && mkdir other && cd other && cosign generate-key-pair >/dev/null 2>&1)
verify "$IMAGE@$GW" "$KEYS/other/cosign.pub" && echo "wrong key: ACCEPTED (bad)" || echo "wrong key: rejected ($(grep -m1 -E '^Error' "$OUT/verify.err" | cut -c1-90))"
docker buildx build -q --platform linux/amd64 --target gateway --build-arg VERSION=tampered \
  -t "$IMAGE:tampered" --push . >/dev/null
TD=$(digest "$IMAGE:tampered")
verify "$IMAGE@$TD" && echo "unsigned image: ACCEPTED (bad)" || echo "unsigned image: rejected ($(grep -oE 'no signatures found|no matching signatures' "$OUT/verify.err" | head -1))"
# Someone moves the release tag to another image: the tag now points to an unsigned digest.
docker buildx imagetools create -t "$IMAGE:$TAG" "$IMAGE@$TD" >/dev/null 2>&1
verify "$IMAGE:$TAG" && echo "moved tag: ACCEPTED (bad)" || echo "moved tag: rejected (tag now -> ${TD:0:19}..., which has no signature)"

echo
echo "## 8. Helm chart as an OCI artifact, signed"
helm package deploy/helm/llm-gateway --version 0.0.0-local --app-version $TAG -d "$OUT" >/dev/null
helm push "$OUT/llm-gateway-0.0.0-local.tgz" "oci://$REG/charts" --plain-http > "$OUT/helm-push.log" 2>&1
CD=$(grep -oE 'sha256:[0-9a-f]{64}' "$OUT/helm-push.log" | head -1)
echo "chart oci://$REG/charts/llm-gateway:0.0.0-local digest ${CD:0:19}..."
echo "sign chart: $(sign "$REG/charts/llm-gateway@$CD")"
verify "$REG/charts/llm-gateway@$CD" && echo "verify chart signature: OK" || { echo "verify chart: FAILED"; tail -2 "$OUT/verify.err"; }
helm show chart "oci://$REG/charts/llm-gateway" --version 0.0.0-local --plain-http 2>/dev/null | grep -E '^(name|version|appVersion):'

echo
echo "## 9. Registry contents"
curl -s "http://$REG/v2/_catalog"; echo
for r in llm-gateway llm-gateway-mock charts/llm-gateway; do
  echo "$r tags: $(curl -s "http://$REG/v2/$r/tags/list" | python3 -c 'import json,sys; print(" ".join(sorted(json.load(sys.stdin).get("tags") or [])))')"
done
