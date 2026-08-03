#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

check_shell() {
  file=$1
  first=$(head -1 "$file")
  case "$first" in
    *bash*) bash -n "$file" ;;
    '#!'*) sh -n "$file" ;;
  esac
}

while IFS= read -r file; do check_shell "$file"; done < <(find scripts packaging/rootfs-overlay -type f -name '*.sh' -print)

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-release-verify.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
./scripts/build-controller-shell.sh --product gateway --out "$tmp/gateway-ui"
test -s "$tmp/gateway-ui/app.js"
test -s "$tmp/gateway-ui/capabilities.json"

(cd backend && go test ./...)
./scripts/test-product-builders.sh
./scripts/validate-rootfs-scaffold.sh

for forbidden in \
  'control.tokisaki' \
  '84003692-b0fa' \
  'BEGIN OPENSSH PRIVATE KEY' \
  'BEGIN RSA PRIVATE KEY'; do
  if grep -R -I -n --exclude='*.bak' --exclude='*.bak-*' --exclude=ci-release-verify.sh --exclude-dir=.git --exclude-dir=panabit-real --fixed-strings -- "$forbidden" \
      backend config deploy docs frontend packaging runtime scripts README.md README.zh.md; then
    echo "public source contains forbidden private material" >&2
    exit 1
  fi
done

printf 'Release source verification passed.\n'
