#!/usr/bin/env bash
# GitOps on the local kind cluster (after make kind-up):
#   1. Argo CD and Argo Rollouts, pinned versions
#   2. an in-cluster git server (Gitea) with this repo pushed to it, and a
#      push webhook so Argo CD syncs right after each push
#   3. the gateway's Argo CD Application (namespace llm-gateway)
# The plain Helm release from `make deploy` is removed first (same NodePorts).
#   scripts/gitops/up.sh [repo-dir]    (default: this repo's HEAD)
set -euo pipefail
ARGOCD_VERSION=v3.6.3
ROLLOUTS_VERSION=v1.10.2
ROOT=$(git rev-parse --show-toplevel)
REPO_DIR=${1:-$ROOT}
cd "$ROOT"

wait_for() { # namespace kind/name...
  local ns=$1; shift
  for r in "$@"; do kubectl -n "$ns" rollout status "$r" --timeout=300s >/dev/null; done
}

echo "== Argo CD $ARGOCD_VERSION and Argo Rollouts $ROLLOUTS_VERSION"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml" >/dev/null
kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -n argo-rollouts --server-side --force-conflicts \
  -f "https://github.com/argoproj/argo-rollouts/releases/download/$ROLLOUTS_VERSION/install.yaml" >/dev/null

echo "== git server"
kubectl apply -f deploy/gitops/gitea.yaml >/dev/null
wait_for argocd deploy/argocd-server deploy/argocd-repo-server deploy/argocd-redis statefulset/argocd-application-controller
wait_for argo-rollouts deploy/argo-rollouts
wait_for gitops deploy/gitea

if ! kubectl -n gitops get secret gitea-admin >/dev/null 2>&1; then
  pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
  kubectl -n gitops create secret generic gitea-admin --from-literal=password="$pw" >/dev/null
  kubectl -n gitops exec deploy/gitea -- gitea admin user create --admin --username gitops \
    --password "$pw" --email gitops@example.invalid --must-change-password=false >/dev/null
  echo "git server admin user created (password in Secret gitops/gitea-admin)"
fi
scripts/gitops/push.sh "$REPO_DIR"

echo "== webhook: git server -> Argo CD"
pw=$(kubectl -n gitops get secret gitea-admin -o jsonpath='{.data.password}' | base64 -d)
kubectl -n gitops port-forward svc/gitea 3301:3000 >/dev/null 2>&1 & pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:3301/api/healthz >/dev/null && break; sleep 0.5; done
api=http://127.0.0.1:3301/api/v1/repos/gitops/llm-gateway-platform/hooks
if [ "$(curl -s -u "gitops:$pw" "$api" | grep -c argocd-server)" = 0 ]; then
  curl -sf -u "gitops:$pw" -H 'Content-Type: application/json' -d '{
    "type": "gogs", "active": true, "events": ["push"],
    "config": {"url": "https://argocd-server.argocd.svc.cluster.local/api/webhook", "content_type": "json"}
  }' "$api" >/dev/null && echo "webhook created"
fi

echo "== images and Application"
tag=$(grep -m1 -E '^  tag:' deploy/gitops/kind-release.yaml | awk '{print $2}')
make -s kind-load TAG="$tag" > /tmp/kind-load.log 2>&1 || { tail -20 /tmp/kind-load.log; exit 1; }
echo "images $tag built and loaded into kind"
if helm status llm-gateway -n default >/dev/null 2>&1; then
  helm uninstall llm-gateway -n default --wait >/dev/null && echo "removed the plain Helm release from make deploy"
fi
kubectl apply -f deploy/gitops/application.yaml >/dev/null
for _ in $(seq 1 120); do
  s=$(kubectl -n argocd get application llm-gateway -o jsonpath='{.status.sync.status} {.status.health.status}' 2>/dev/null || true)
  [ "$s" = "Synced Healthy" ] && break
  sleep 5
done
echo "Application llm-gateway: $s"
