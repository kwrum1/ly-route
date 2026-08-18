#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
baseline="$repo_root/config/build/compiler-toolchain.env"

if [ ! -f "$baseline" ]; then
  echo "missing compiler baseline: $baseline" >&2
  exit 2
fi
# shellcheck disable=SC1090
. "$baseline"

fail() {
  echo "compiler environment check failed: $*" >&2
  exit 1
}

command -v go >/dev/null 2>&1 || fail "go is not installed"
command -v gcc >/dev/null 2>&1 || fail "gcc is not installed"
command -v g++ >/dev/null 2>&1 || fail "g++ is not installed"
command -v git >/dev/null 2>&1 || fail "git is not installed"

actual_go_version=$(go version | awk '{print $3}')
[ -n "${LY_ROUTE_EXPECT_GO_VERSION:-}" ] && LY_ROUTE_GO_VERSION="$LY_ROUTE_EXPECT_GO_VERSION"
expected_host_arch=${LY_ROUTE_EXPECT_HOST_ARCH:-x86_64}
expected_goarch=${LY_ROUTE_EXPECT_GOARCH:-$LY_ROUTE_GOARCH}
expected_deb_arch=${LY_ROUTE_EXPECT_DEB_ARCH:-amd64}
[ "$actual_go_version" = "$LY_ROUTE_GO_VERSION" ] || fail "Go $actual_go_version found; require $LY_ROUTE_GO_VERSION"
[ "$(go env GOOS)" = "$LY_ROUTE_GOOS" ] || fail "GOOS must be $LY_ROUTE_GOOS"
[ "$(go env GOARCH)" = "$expected_goarch" ] || fail "GOARCH must be $expected_goarch"
[ "$(go env GOTOOLCHAIN)" = "$LY_ROUTE_GOTOOLCHAIN" ] || fail "GOTOOLCHAIN must be $LY_ROUTE_GOTOOLCHAIN"
[ "$(go env CGO_ENABLED)" = "$LY_ROUTE_CGO_ENABLED" ] || fail "CGO_ENABLED must be $LY_ROUTE_CGO_ENABLED"
[ "$(go env GOPROXY)" = "$LY_ROUTE_GOPROXY" ] || fail "GOPROXY must be $LY_ROUTE_GOPROXY"
[ "$(go env GOSUMDB)" = "$LY_ROUTE_GOSUMDB" ] || fail "GOSUMDB must be $LY_ROUTE_GOSUMDB"
[ "$(uname -s)" = Linux ] || fail "compiler host must be Linux"
[ "$(uname -m)" = "$expected_host_arch" ] || fail "compiler host must be $expected_host_arch"

if command -v dpkg >/dev/null 2>&1; then
  [ "$(dpkg --print-architecture)" = "$expected_deb_arch" ] || fail "compiler package architecture must be $expected_deb_arch"
fi

[ -f "$repo_root/backend/go.mod" ] || fail "complete backend tree is missing"
[ -f "$repo_root/backend/cmd/gateway-control/main.go" ] || fail "gateway-control source is missing"
[ -f "$repo_root/backend/internal/runtime/vpp/adapter.go" ] || fail "VPP runtime source is missing"

if [ "$(git -C "$repo_root" config --get core.autocrlf || true)" = true ]; then
  fail "core.autocrlf=true is forbidden on the Linux compiler"
fi

crlf_files=$(git -C "$repo_root" grep -I -l "$(printf '\r')" -- . || true)
[ -z "$crlf_files" ] || fail "tracked text files contain CRLF; run scripts/normalize-source-line-endings.sh"

echo "compiler environment OK: $actual_go_version $(go env GOOS)/$(go env GOARCH) host=$(uname -m) target=$LY_ROUTE_DEBIAN_TARGET line-ending=$LY_ROUTE_LINE_ENDING"
