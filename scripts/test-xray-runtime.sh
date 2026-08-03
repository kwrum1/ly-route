#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
deb=${LY_ROUTE_XRAY_DEB:-/root/ly-route/runtime-debs/xray_0~fdb9b616_amd64.deb}
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

if [[ ! -f "$deb" ]]; then
  echo "Xray package not found: $deb" >&2
  exit 1
fi

dpkg-deb -x "$deb" "$workdir/root"
xray_binary="$workdir/root/usr/bin/xray"
if [[ ! -x "$xray_binary" ]]; then
  echo "Xray binary missing from package: $xray_binary" >&2
  exit 1
fi

cd "$repo_root/backend"
LY_ROUTE_XRAY_BINARY="$xray_binary" go test ./internal/runtime/proxy -run '^TestXrayRuntimeConfigsAcceptedByBinary$' -count=1 -v
"$xray_binary" version | head -n 1
echo "Xray protocol schemas and leastPing failover configuration verification passed"
