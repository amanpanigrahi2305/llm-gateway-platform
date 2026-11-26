#!/usr/bin/env bash
# Smoke test for the gateway on kind (after make kind-up deploy). Uses only the
# mock providers. Exits non-zero on the first failed check.
set -euo pipefail
GW=${GW:-http://127.0.0.1:8088}          # gateway NodePort
MOCK_A=${MOCK_A:-http://127.0.0.1:9190}  # mock-a admin API
KEY=sk-gw-local-demo-only                # public demo key (deploy/kind/values.yaml)
BODY='{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":5}'

fail() { echo "FAIL: $*" >&2; exit 1; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
chat() { curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@" "$GW/v1/chat/completions"; }
trap 'curl -s -X POST "$MOCK_A/admin/reset" >/dev/null || true' EXIT

[ "$(code "$GW/healthz")" = 200 ] || fail "healthz"
[ "$(code "$GW/readyz")" = 200 ] || fail "readyz"
echo "ok   probes"

out=$(chat -w '\n%{http_code}' -d "$BODY")
[ "$(tail -1 <<<"$out")" = 200 ] || fail "chat: $out"
grep -q '"content"' <<<"$out" || fail "chat: no content in $out"
echo "ok   chat completion"

chat -N -d "${BODY%\}},\"stream\":true}" | grep -q '^data: \[DONE\]' || fail "stream did not end with [DONE]"
echo "ok   streaming"

[ "$(code -X POST -H 'Content-Type: application/json' -d "$BODY" "$GW/v1/chat/completions")" = 401 ] || fail "no key was not 401"
echo "ok   missing key -> 401"

curl -s -X PATCH -d '{"outage":true}' "$MOCK_A/admin/settings" >/dev/null
prov=$(chat -o /dev/null -w '%header{x-gateway-provider}' -d "$BODY")
[ "$prov" = mock-b ] || fail "with mock-a down, served by '$prov', want mock-b"
echo "ok   failover to mock-b"

echo "kind smoke test: all checks passed"
