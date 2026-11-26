#!/usr/bin/env bash
# Phase 3 evidence: the HPA scales the gateway on CPU under load, then back down.
# Needs: make kind-up && make deploy. Uses only the mock providers (no LLM cost).
# mock-a's delay is cut to 5 ms during the test so each virtual user sends more
# requests; it is reset at the end.
set -u
cd "$(dirname "$0")/../.." || exit 1
KEY=sk-gw-local-demo-only
GW=http://127.0.0.1:8088
MOCK_A=http://127.0.0.1:9190
OUT=${OUT:-/tmp/phase3}
VUS=${VUS:-100}
LOAD=${LOAD:-180s}
mkdir -p "$OUT"
SEL=app.kubernetes.io/name=llm-gateway

now()   { awk '{print $1}' /proc/uptime; }
since() { awk -v a="$1" -v b="$(now)" 'BEGIN{printf "%.0f", b-a}'; }
hpa()   { kubectl get hpa llm-gateway -o jsonpath='{.status.currentMetrics[0].resource.current.averageUtilization} {.status.currentReplicas} {.status.desiredReplicas}'; }
ready() { kubectl get deploy llm-gateway -o jsonpath='{.status.readyReplicas}'; }
poll()  { local t0=$1; while :; do read -r cpu cur des <<<"$(hpa)"; echo "t=$(since "$t0") cpu=${cpu:-?}% replicas=${cur:-?} desired=${des:-?} ready=$(ready)"; sleep 5; done; }

echo "## E. HPA under load ($VUS VUs for $LOAD, mock-a delay 5 ms)"
curl -s -X PATCH -d '{"latency_ms":5,"ttft_ms":0,"tokens_per_sec":0}' "$MOCK_A/admin/settings" > /dev/null
echo "before: $(kubectl top pods -l "$SEL" --no-headers | awk '{printf "%s %s %s; ", $1, $2, $3}')"
t0=$(now)
poll "$t0" > "$OUT/hpa-timeline.txt" & PL=$!
k6 run -q --summary-export "$OUT/hpa.json" -e GATEWAY_URL=$GW -e GATEWAY_KEY=$KEY \
  -e VUS="$VUS" -e DURATION="$LOAD" loadtest/steady.js > "$OUT/hpa-k6.txt" 2>&1
tl=$(since "$t0")
echo "load ended at t=${tl}s"
kubectl top pods -l "$SEL" --no-headers > "$OUT/hpa-top-end.txt" 2>&1
# Watch the scale-down: 120 s stabilization, then 1 pod per 30 s.
for i in $(seq 1 72); do
  [ "$(kubectl get hpa llm-gateway -o jsonpath='{.status.currentReplicas}')" = 2 ] && break
  sleep 5
done
sleep 5; kill $PL 2>/dev/null
curl -s -X POST "$MOCK_A/admin/reset" > /dev/null

python3 - "$OUT/hpa.json" <<'EOF'
import json, sys
m = json.load(open(sys.argv[1]))["metrics"]
d = m["http_req_duration"]; f = m["http_req_failed"]
print(f"k6: requests={m['http_reqs']['count']} failed={f.get('passes', 0)} rate={m['http_reqs']['rate']:.0f}/s "
      f"p50={d['med']:.2f}ms p95={d['p(95)']:.2f}ms max={d['max']:.2f}ms")
print("outcomes: " + " ".join(f"{k[len('outcome{result:'):-1]}={v['count']}"
      for k, v in sorted(m.items()) if k.startswith("outcome{") and v.get("count", 0) > 0))
EOF
echo "timeline (every 5 s; t in seconds since load start):"
cat "$OUT/hpa-timeline.txt"
echo "at load end: $(awk '{printf "%s %s %s; ", $1, $2, $3}' "$OUT/hpa-top-end.txt")"
echo "HPA events:"
kubectl get events --field-selector involvedObject.kind=HorizontalPodAutoscaler \
  --sort-by=.lastTimestamp -o custom-columns=REASON:.reason,MESSAGE:.message --no-headers | tail -12
