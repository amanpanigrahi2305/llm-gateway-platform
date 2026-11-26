#!/usr/bin/env bash
# Install CI tools at fixed versions into $DEST (default ~/.local/bin).
# Every download is checked against a SHA-256 written in this file, so a
# replaced release file or a moved tag cannot change what CI runs.
#
#   scripts/ci/install-tools.sh                 # all tools
#   scripts/ci/install-tools.sh trivy kind      # only these
#
# To upgrade a tool: change its URL and copy the SHA-256 from the release's
# official checksum file.
set -euo pipefail
DEST=${DEST:-$HOME/.local/bin}
mkdir -p "$DEST"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# install_one NAME URL SHA256 [FILE-IN-TAR]
install_one() {
  local name=$1 url=$2 sha=$3 member=${4:-}
  local file="$work/${url##*/}"
  curl -fsSL --retry 3 -o "$file" "$url"
  echo "$sha  $file" | sha256sum -c --quiet -
  if [ -n "$member" ] && [[ $file == *.zip ]]; then
    python3 -c 'import sys, zipfile; zipfile.ZipFile(sys.argv[1]).extract(sys.argv[2], sys.argv[3])' "$file" "$member" "$work"
    install -m 0755 "$work/$member" "$DEST/$name"
  elif [ -n "$member" ]; then
    tar -xzf "$file" -C "$work" "$member"
    install -m 0755 "$work/$member" "$DEST/$name"
  else
    install -m 0755 "$file" "$DEST/$name"
  fi
  echo "installed $name ($url)"
}

tools=("$@")
[ ${#tools[@]} -eq 0 ] && tools=(trivy gitleaks actionlint kind helm)
for t in "${tools[@]}"; do
  case $t in
    trivy) install_one trivy \
      https://github.com/aquasecurity/trivy/releases/download/v0.76.2/trivy_0.76.2_Linux-64bit.tar.gz \
      bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf trivy ;;
    gitleaks) install_one gitleaks \
      https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz \
      551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb gitleaks ;;
    actionlint) install_one actionlint \
      https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_amd64.tar.gz \
      8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8 actionlint ;;
    kind) install_one kind \
      https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-amd64 \
      aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d ;;
    helm) install_one helm \
      https://get.helm.sh/helm-v4.3.2-linux-amd64.tar.gz \
      d1c0278d46beab79582d7e50f5b3dc206944ba8e577cde618513fd07daa53983 linux-amd64/helm ;;
    terraform) install_one terraform \
      https://releases.hashicorp.com/terraform/1.17.2/terraform_1.17.2_linux_amd64.zip \
      e6f16b2f0b137ee5729caa8c4e79a3bae4ee261acd198a1fcfa8584a6296605b terraform ;;
    tflint) install_one tflint \
      https://github.com/terraform-linters/tflint/releases/download/v0.66.0/tflint_linux_amd64.zip \
      22be11cadc25cd02401e94c0ccc91aa63467debb8485c2a755d98f4c0a9d1e2e tflint ;;
    *) echo "unknown tool: $t" >&2; exit 1 ;;
  esac
done
