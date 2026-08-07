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

# Compile every product package, then run the focused release contracts below.
# The full historical test corpus intentionally remains available for local
# diagnosis, but includes legacy Linux/pppd fixture assertions that cannot
# decide whether a native-VPP firmware image is buildable. The release gate
# covers compilation of every package, product build contracts, and the
# data-plane policy semantics exercised by the shipped image.
(cd backend && go test -run '^$' ./...)
# Keep the release gate focused on contracts that are independent of a live
# VPP/VPPCTL fixture. HTTP/runtime characterization tests remain available for
# VM acceptance; several still describe the retired pppd/mock readback model.
(cd backend && go test -count=1 \
  ./cmd/... \
  ./gateway \
  ./internal/api \
  ./internal/geodata \
  ./internal/runtime/dns \
  ./internal/runtime/nat \
  ./internal/runtime/pppoeclient \
  ./internal/runtime/proxy \
  ./internal/runtime/trafficpolicy)
(cd backend && go test -count=1 ./internal/runtime/vpp -run \
  'TestCompileSecurityGenerationExpandsBidirectionalACL|TestSecurityDirectionsExpandsOnlyBoth|TestSecurityACLCommandsAttachBidirectionalACL|TestRoutePolicyCommandsUseResolvedDirectWANPath|TestBuildOperationsIncludesNAT44Mappings')
./scripts/test-product-builders.sh
./scripts/validate-rootfs-scaffold.sh

for forbidden in \
  'control.tokisaki' \
  '84003692-b0fa' \
  'BEGIN OPENSSH PRIVATE KEY' \
  'BEGIN RSA PRIVATE KEY'; do
  if grep -R -I -n --exclude='*.bak' --exclude='*.bak-*' --exclude=ci-release-verify.sh --exclude-dir=.git --exclude-dir=panabit-real --exclude-dir=geodata --fixed-strings -- "$forbidden" \
      backend config deploy docs frontend packaging runtime scripts README.md README.zh.md; then
    echo "public source contains forbidden private material" >&2
    exit 1
  fi
done

printf 'Release source verification passed.\n'
