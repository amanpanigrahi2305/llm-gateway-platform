#!/usr/bin/env bash
# Phase 7a evidence: GitOps releases with Argo CD and Argo Rollouts on kind.
# Needs: make kind-up && make gitops-up. Uses only the mock providers.
#   B. good release (new image tag): canary 10% -> check -> 50% -> check -> 100%
#   C. bad release (new tag + config that sends traffic to a flaky upstream):
#      the canary check fails and the rollout aborts by itself
#   D. fix forward: git revert of the bad commit
#   E. drift: changes made with kubectl are undone by Argo CD
# Every release is a commit pushed to the in-cluster git server. During B, C
# and D, k6 sends a fixed 100 requests/s through the gateway NodePort.
set -u
cd "$(dirname "$0")/../.." || exit 1
NS=llm-gateway
KEY=sk-gw-local-demo-only
OUT=${OUT:-/tmp/phase7a-gitops}
rm -rf "$OUT"; mkdir -p "$OUT"
PORT=3302
K6=""

now()   { awk '{print $1}' /proc/uptime; }        # monotonic; the WSL wall clock jumps
since() { awk -v a="$1" -v b="$(now)" 'BEGIN{printf "%.1f", b-a}'; }
app()   { kubectl -n argocd get application llm-gateway -o jsonpath='{.status.sync.revision} {.status.sync.status} {.status.health.status}'; }
ro()    { kubectl -n $NS get rollout llm-gateway -o jsonpath='{.status.phase} step={.status.currentStepIndex} abort={.status.abort}'; }
pods()  { kubectl -n $NS get pods -l app.kubernetes.io/name=llm-gateway \
            -o jsonpath='{range .items[?(@.status.containerStatuses[0].ready==true)]}{.spec.containers[0].image}{"\n"}{end}' \
          | sed 's#.*:##' | sort | uniq -c | awk '{printf "%s=%s ", $2, $1}'; }

# 20 requests straight to each running gateway pod (not through the Service).
percheck() {
  local ips; ips=$(kubectl -n $NS get pods -l app.kubernetes.io/name=llm-gateway --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}={.status.podIP}={.spec.containers[0].image}{" "}{end}')
  kubectl -n $NS run "percheck-$RANDOM" --image=curlimages/curl:8.23.0 --restart=Never --rm -i --quiet \
    --env="IPS=$ips" --env="KEY=$KEY" --command -- sh -c '
  all=0; badpods=0
  for e in $IPS; do
    IFS="=" read -r name ip image <<EOF
$e
EOF
    ok=0
    for i in $(seq 1 20); do
      c=$(curl -s -o /dev/null -m 5 -w "%{http_code}" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
        -d "{\"model\":\"fast\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":5}" http://$ip:8080/v1/chat/completions)
      [ "$c" = 200 ] && ok=$((ok+1))
    done
    all=$((all+1)); [ "$ok" = 20 ] || { badpods=$((badpods+1)); echo "  $name ${image##*:}: $ok/20 ok"; }
  done
  echo "  per-pod check: $((all-badpods)) of $all pods answered 20/20"' 2>/dev/null
}

# Work clone of the in-cluster repo; pushes go through a port-forward.
pw=$(kubectl -n gitops get secret gitea-admin -o jsonpath='{.data.password}' | base64 -d)
AUTH="Authorization: Basic $(printf 'gitops:%s' "$pw" | base64 -w0)"
kubectl -n gitops port-forward svc/gitea $PORT:3000 >/dev/null 2>&1 & PF=$!
trap 'kill $PF 2>/dev/null; [ -n "$K6" ] && kill $K6 2>/dev/null' EXIT
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:$PORT/api/healthz >/dev/null && break; sleep 0.5; done
WORK=$OUT/work
git -c http.extraHeader="$AUTH" clone -q http://127.0.0.1:$PORT/gitops/llm-gateway-platform.git "$WORK"
git -C "$WORK" config user.name release-bot; git -C "$WORK" config user.email release-bot@example.invalid
push() { git -C "$WORK" -c http.extraHeader="$AUTH" push -q origin HEAD:main; git -C "$WORK" rev-parse --short HEAD; }

# Print a line whenever Argo CD, the Rollout or the ready pods change; stop
# when the check command succeeds or after a timeout.
watch_until() { # t0 timeout check-command
  local t0=$1 limit=$2 check=$3 last="" line
  while :; do
    line="argocd=$(app | awk '{print substr($1,1,7), $2, $3}') | rollout=$(ro) | ready pods: $(pods)"
    [ "$line" != "$last" ] && echo "t=$(since "$t0")s $line" && last=$line
    eval "$check" && return 0
    awk -v e="$(since "$t0")" -v l="$limit" 'BEGIN{exit !(e>l)}' && { echo "TIMEOUT after ${limit}s"; return 1; }
    sleep 1
  done
}
k6_start() { k6 run -q --summary-export "$OUT/$1.json" -e GATEWAY_URL=http://127.0.0.1:8088 -e GATEWAY_KEY=$KEY \
               -e RATE=100 -e DURATION=600s loadtest/steady.js > "$OUT/$1-k6.txt" 2>&1 & K6=$!; sleep 3; }
k6_stop()  { kill -INT "$K6" 2>/dev/null; wait "$K6" 2>/dev/null; K6=""
  python3 - "$OUT/$1.json" <<'EOF'
import json, sys
m = json.load(open(sys.argv[1]))["metrics"]
d = m["http_req_duration"]; n = m["http_reqs"]["count"]; f = m["http_req_failed"].get("passes", 0)
print(f"k6 (100 requests/s): requests={n} failed={f} ({100*f/n:.2f}%) p95={d['p(95)']:.2f}ms; outcomes: "
      + " ".join(f"{k[len('outcome{result:'):-1]}={v['count']}" for k, v in sorted(m.items())
                 if k.startswith("outcome{") and v.get("count", 0) > 0))
EOF
}
analysis_logs() {
  for j in $(kubectl -n $NS get jobs -o name --sort-by=.metadata.creationTimestamp | tail -"$1"); do
    echo "  $(kubectl -n $NS logs "$j" 2>/dev/null | tail -1)"
  done
}

echo "## A. Starting point"
kubectl -n argocd get deploy argocd-server -o jsonpath='Argo CD: {.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n argo-rollouts get deploy argo-rollouts -o jsonpath='Argo Rollouts: {.spec.template.spec.containers[0].image}{"\n"}'
echo "argocd: $(app) | rollout: $(ro) | ready pods: $(pods)"
echo "steps: $(kubectl -n $NS get rollout llm-gateway -o jsonpath='{.spec.strategy.canary.steps}')"

echo
echo "## B. Good release: image gitops-1 -> gitops-2"
make -s kind-load TAG=gitops-2 >/dev/null 2>&1 && echo "images gitops-2 built and loaded into kind"
sed -i 's/^  tag: gitops-1$/  tag: gitops-2/' "$WORK/deploy/gitops/kind-release.yaml"
git -C "$WORK" commit -qam "release: gateway gitops-2"
k6_start good
t0=$(now); echo "pushed $(push)"
watch_until "$t0" 300 '[ "$(ro | cut -d" " -f1)" = Healthy ] && pods | grep -q "^gitops-2=10 $"'
echo "promoted to 100% after $(since "$t0") s"
k6_stop good
echo "canary checks:"; analysis_logs 2

echo
echo "## C. Bad release: image gitops-3 and alias fast -> mock-flaky (fails 50%)"
make -s kind-load TAG=gitops-3 >/dev/null 2>&1 && echo "images gitops-3 built and loaded into kind"
sed -i 's/^  tag: gitops-2$/  tag: gitops-3/' "$WORK/deploy/gitops/kind-release.yaml"
cat >> "$WORK/deploy/gitops/kind-release.yaml" <<'EOF'
  aliases:
    fast: ["mock-flaky/mock-1"]   # the bug: only the flaky upstream, no failover
EOF
git -C "$WORK" commit -qam "release: gateway gitops-3 (bad config)"
k6_start bad
t0=$(now); echo "pushed $(push)"
watch_until "$t0" 300 '[ "$(ro | cut -d" " -f3)" = abort=true ] && ! pods | grep -q gitops-3'
echo "aborted and canary removed after $(since "$t0") s"
sleep 10; k6_stop bad
echo "rollout message: $(kubectl -n $NS get rollout llm-gateway -o jsonpath='{.status.message}')"
echo "canary check:"; analysis_logs 1
echo "after the abort, every pod (all stable gitops-2):"; percheck

echo
echo "## D. Fix forward: git revert of the bad commit"
git -C "$WORK" revert --no-edit HEAD >/dev/null
k6_start fix
t0=$(now); echo "pushed $(push) ($(git -C "$WORK" log -1 --format=%s))"
watch_until "$t0" 300 '[ "$(ro | cut -d" " -f1)" = Healthy ] && app | grep -q "Synced Healthy"'
echo "healthy again after $(since "$t0") s"
sleep 10; k6_stop fix
echo "after the revert:"; percheck

echo
echo "## E. Drift: changes made with kubectl are undone"
t0=$(now); kubectl -n $NS delete networkpolicy llm-gateway >/dev/null
until kubectl -n $NS get networkpolicy llm-gateway >/dev/null 2>&1; do sleep 0.5; done
echo "deleted the NetworkPolicy: back after $(since "$t0") s"
t0=$(now); kubectl -n $NS patch pdb llm-gateway --type merge -p '{"spec":{"maxUnavailable":5}}' >/dev/null
until [ "$(kubectl -n $NS get pdb llm-gateway -o jsonpath='{.spec.maxUnavailable}')" = 1 ]; do sleep 0.5; done
echo "loosened the PodDisruptionBudget to 5: back to 1 after $(since "$t0") s"
t0=$(now); kubectl -n $NS patch rollout llm-gateway --type merge -p '{"spec":{"replicas":3}}' >/dev/null
until [ "$(kubectl -n $NS get rollout llm-gateway -o jsonpath='{.spec.replicas}')" = 10 ]; do sleep 0.5; done
echo "scaled the Rollout to 3: back to 10 after $(since "$t0") s"
cm=$(kubectl -n $NS get configmap -l app.kubernetes.io/component=config --sort-by=.metadata.creationTimestamp -o name | tail -1)
printf 'hand edit of %s: ' "$cm"
kubectl -n $NS patch "$cm" --type merge -p '{"data":{"config.yaml":"hand-edited: true\n"}}' 2>&1 | grep -oE 'field is immutable|patched' | head -1
sleep 5
echo "final: argocd=$(app) | rollout=$(ro) | ready pods: $(pods)"
echo "config versions kept: $(kubectl -n $NS get configmap -l app.kubernetes.io/component=config -o name | tr '\n' ' ')"
echo
echo "git history on the in-cluster server:"
git -C "$WORK" log --format='  %h %an: %s' -4
