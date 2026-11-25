#!/usr/bin/env bash
# Phase 3 evidence: the gateway on a local 3-node kind cluster.
# Needs: make kind-up && make deploy. Uses only the mock providers (no LLM cost).
# Writes raw output to $OUT (default /tmp/phase3).
set -u
cd "$(dirname "$0")/../.." || exit 1
KEY=sk-gw-local-demo-only          # public demo key from deploy/kind/values.yaml
GW=http://127.0.0.1:8088           # NodePort 30080 (deploy/kind/cluster.yaml)
MOCK_A=http://127.0.0.1:9190       # mock-a admin API
OUT=${OUT:-/tmp/phase3}
mkdir -p "$OUT"
SEL=app.kubernetes.io/name=llm-gateway

now()   { awk '{print $1}' /proc/uptime; }   # monotonic seconds; the WSL wall clock can jump
since() { awk -v a="$1" -v b="$(now)" 'BEGIN{printf "%.1f", b-a}'; }
chat()  { curl -s -o /dev/null -w '%{http_code} %{time_total}s provider=%header{x-gateway-provider}\n' \
            -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
            -d '{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' "$GW/v1/chat/completions"; }
ledger_rows() { kubectl exec deploy/llm-gateway-postgres -- psql -U gateway -d gateway -tAc 'select count(*) from usage_records' 2>/dev/null || echo 0; }
ready_pods()  { kubectl get deploy llm-gateway -o jsonpath='{.status.readyReplicas}'; }

# One line per second: ready pods and ready endpoints behind the Service.
timeline() {
  local t0=$1
  while :; do
    local e
    e=$(kubectl get endpointslices -l kubernetes.io/service-name=llm-gateway \
          -o jsonpath='{range .items[*].endpoints[*]}{.conditions.ready}{"\n"}{end}' | grep -c true)
    echo "t=$(since "$t0") ready_pods=$(ready_pods) ready_endpoints=$e"
    sleep 1
  done
}

# Wait until a new pod replaces $1 and the deployment is back to 2 ready pods.
wait_replaced() {
  local old=$1 t0=$2
  while :; do
    local names; names=$(kubectl get pods -l "$SEL" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].ready}{"\n"}{end}')
    local fresh; fresh=$(echo "$names" | grep -v "^$old " | grep -c ' true$')
    [ "$fresh" -ge 2 ] && { echo "replacement ready after $(since "$t0")s"; return; }
    sleep 0.5
  done
}

summary() {  # k6 --summary-export file
  python3 - "$1" <<'EOF'
import json, sys
m = json.load(open(sys.argv[1]))["metrics"]
d = m["http_req_duration"]; f = m["http_req_failed"]
total = m["http_reqs"]["count"]
print(f"requests={total} failed={f.get('passes', 0)} rate={m['http_reqs']['rate']:.0f}/s "
      f"p50={d['med']:.2f}ms p95={d['p(95)']:.2f}ms max={d['max']:.2f}ms")
print("outcomes: " + " ".join(f"{k[len('outcome{result:'):-1]}={v['count']}"
      for k, v in sorted(m.items()) if k.startswith("outcome{") and v.get("count", 0) > 0))
EOF
}

echo "## A. Cluster"
kind version
kubectl get nodes -o custom-columns=NAME:.metadata.name,STATUS:.status.conditions[-1].type,VERSION:.status.nodeInfo.kubeletVersion
kubectl get pods -o wide | awk '{print $1, $2, $3, $7}'
echo "security context (user, non-root, read-only root):"
kubectl get pods -o jsonpath='{range .items[*]}{.metadata.name}{" uid="}{.spec.securityContext.runAsUser}{" nonRoot="}{.spec.securityContext.runAsNonRoot}{" readOnlyRoot="}{.spec.containers[0].securityContext.readOnlyRootFilesystem}{"\n"}{end}'
kubectl get hpa,pdb

echo
echo "## B. Smoke through the NodePort"
echo "healthz $(curl -s -o /dev/null -w '%{http_code}' $GW/healthz)  readyz $(curl -s -o /dev/null -w '%{http_code}' $GW/readyz)"
for i in 1 2 3; do chat; done
printf 'stream: '
curl -sN -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"fast","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":5}' \
  "$GW/v1/chat/completions" > "$OUT/stream.txt"
echo "$(grep -c '^data: {' "$OUT/stream.txt") chunks, last line: $(grep '^data:' "$OUT/stream.txt" | tail -1)"
echo "failover: mock-a outage on"
curl -s -X PATCH -d '{"outage":true}' "$MOCK_A/admin/settings" > /dev/null
for i in 1 2 3; do chat; done
curl -s -X POST "$MOCK_A/admin/reset" > /dev/null
echo "mock-a reset"; chat
sleep 2
echo "ledger rows in the cluster Postgres: $(ledger_rows)"

echo
echo "## C. Delete both original gateway pods, one at a time, under load (20 VUs, 60s)"
rows0=$(ledger_rows)
mapfile -t orig < <(kubectl get pods -l "$SEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
echo "original pods: ${orig[*]}"
t0=$(now)
timeline "$t0" > "$OUT/podkill-timeline.txt" & TL=$!
k6 run -q --summary-export "$OUT/podkill.json" -e GATEWAY_URL=$GW -e GATEWAY_KEY=$KEY \
  -e VUS=20 -e DURATION=60s loadtest/steady.js > "$OUT/podkill-k6.txt" 2>&1 & K6=$!
for idx in 0 1; do
  sleep $(( idx == 0 ? 15 : 10 ))
  p=${orig[$idx]}
  kubectl logs -f "$p" > "$OUT/podkill-$idx.log" 2>&1 &
  td=$(now)
  echo "t=$(since "$t0") delete $p"
  kubectl delete pod "$p" --wait=false > /dev/null
  wait_replaced "$p" "$td"
done
wait $K6; kill $TL 2>/dev/null
summary "$OUT/podkill.json"
for idx in 0 1; do
  echo "pod $idx shutdown log:"
  grep -oE '"msg":"(shutdown started|shutdown complete|usage ledger closed)"[^}]*' "$OUT/podkill-$idx.log" | cut -c1-160
done
sleep 3
rows1=$(ledger_rows)
echo "ledger rows added during the test: $((rows1 - rows0))"
echo "lowest ready endpoints seen: $(awk '{split($3,a,"="); print a[2]}' "$OUT/podkill-timeline.txt" | sort -n | head -1)"

echo
echo "## D. Rolling restart under load (20 VUs, 60s)"
rows0=$(ledger_rows)
t0=$(now)
timeline "$t0" > "$OUT/rollout-timeline.txt" & TL=$!
k6 run -q --summary-export "$OUT/rollout.json" -e GATEWAY_URL=$GW -e GATEWAY_KEY=$KEY \
  -e VUS=20 -e DURATION=60s loadtest/steady.js > "$OUT/rollout-k6.txt" 2>&1 & K6=$!
sleep 10
tr=$(now)
kubectl rollout restart deploy/llm-gateway > /dev/null
kubectl rollout status deploy/llm-gateway --timeout=180s > /dev/null
echo "rollout finished after $(since "$tr")s"
wait $K6; kill $TL 2>/dev/null
summary "$OUT/rollout.json"
sleep 3
rows1=$(ledger_rows)
echo "ledger rows added during the test: $((rows1 - rows0))"
echo "lowest ready endpoints seen: $(awk '{split($3,a,"="); print a[2]}' "$OUT/rollout-timeline.txt" | sort -n | head -1)"
kubectl get pods -l "$SEL" -o wide | awk '{print $1, $2, $3, $7}'
