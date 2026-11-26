#!/usr/bin/env bash
# Push a git repo's HEAD to main on the in-cluster git server (Gitea).
# The server is a local mirror for tests, so this force-pushes.
#   scripts/gitops/push.sh [repo-dir]      (default: this repo)
set -euo pipefail
REPO_DIR=${1:-$(git rev-parse --show-toplevel)}
PORT=${GITEA_PORT:-3300}

pw=$(kubectl -n gitops get secret gitea-admin -o jsonpath='{.data.password}' | base64 -d)
kubectl -n gitops port-forward svc/gitea "$PORT:3000" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -sf "http://127.0.0.1:$PORT/api/healthz" >/dev/null && break; sleep 0.5; done

# The password goes in a header, never in the URL or the output.
auth=$(printf 'gitops:%s' "$pw" | base64 -w0)
git -C "$REPO_DIR" -c http.extraHeader="Authorization: Basic $auth" \
  push --force --quiet "http://127.0.0.1:$PORT/gitops/llm-gateway-platform.git" HEAD:main
echo "pushed $(git -C "$REPO_DIR" rev-parse --short HEAD) to gitops/llm-gateway-platform main"
