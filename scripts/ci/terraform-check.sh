#!/usr/bin/env bash
# Offline Terraform checks: fmt, init (no backend), validate, terraform test with
# mocked providers, tflint. No AWS credentials are used and no AWS API is
# called; AWS credential variables are removed for safety.
#   scripts/ci/terraform-check.sh            (from the repo root)
set -euo pipefail
cd "$(dirname "$0")/../../deploy/terraform"
unset AWS_PROFILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
export AWS_EC2_METADATA_DISABLED=true TF_IN_AUTOMATION=1
export TF_PLUGIN_CACHE_DIR="${TF_PLUGIN_CACHE_DIR:-$HOME/.terraform.d/plugin-cache}"
mkdir -p "$TF_PLUGIN_CACHE_DIR"

echo "== terraform fmt -check"
terraform fmt -check -recursive .

for d in modules/* bootstrap examples/*; do
  echo "== $d"
  terraform -chdir="$d" init -backend=false -input=false >/dev/null
  terraform -chdir="$d" validate -no-color
  if [ -d "$d/tests" ]; then
    terraform -chdir="$d" test -no-color
  fi
done

echo "== tflint"
tflint --init --config "$PWD/.tflint.hcl" >/dev/null
tflint --recursive --config "$PWD/.tflint.hcl" --format compact
echo "terraform checks passed"
