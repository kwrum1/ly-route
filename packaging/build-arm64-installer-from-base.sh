#!/usr/bin/env bash
# Build the ARM64 Gateway one-click installer from a published ARM64 installer
# bundle and a release ARM64 upgrade package. This script only assembles binary
# artifacts; it does not compile product source code.
#
# Usage:
#   packaging/build-arm64-installer-from-base.sh \
#     --version 1.1.4 \
#     --package ly-route-upgrade-gateway-bookworm-arm64.tar.zst \
#     --base-installer ly-route-gateway-arm64-installer.tar.zst \
#     --out dist \
#     [--commit GIT_SHA]
#
# The base installer provides packages/, install.sh, README.md and the current
# overlay. The upgrade package provides the target control plane, VPP plugins,
# UI and version.env. The result is a new installer tar with updated manifest
# and checksums.
set -euo pipefail

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
}

version=
package=
base_installer=
out_dir=dist
commit=${GITHUB_SHA:-unknown}
work=
keep_work=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version=${2:-}; shift 2 ;;
    --package) package=${2:-}; shift 2 ;;
    --base-installer) base_installer=${2:-}; shift 2 ;;
    --out) out_dir=${2:-}; shift 2 ;;
    --commit) commit=${2:-}; shift 2 ;;
    --work) work=${2:-}; shift 2 ;;
    --keep-work) keep_work=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$version" ] || { echo "--version is required" >&2; exit 2; }
case "$version" in *[!0-9A-Za-z.+~-]*) echo "invalid version: $version" >&2; exit 2 ;; esac
[ -f "$package" ] || { echo "--package must point to an existing upgrade package: $package" >&2; exit 2; }
[ -f "$base_installer" ] || { echo "--base-installer must point to an existing installer: $base_installer" >&2; exit 2; }

for command in tar zstd sha256sum stat sed python3; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
case "$out_dir" in /*) artifact_dir=$out_dir ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
mkdir -p "$artifact_dir"

if [ -z "$work" ]; then
  work_root=${LY_ROUTE_ISO_WORK_ROOT:-${TMPDIR:-/tmp}}
  mkdir -p "$work_root"
  work=$(mktemp -d "$work_root/ly-route-arm64.XXXXXX")
fi
work=$(CDPATH= cd -- "$work" && pwd)
cleanup() {
  if [ "$keep_work" = "1" ]; then
    echo "work directory kept: $work"
  else
    rm -rf "$work"
  fi
}
trap cleanup EXIT INT TERM

bundle="$work/bundle"
mkdir -p "$bundle"
echo "== extract base installer =="
tar --zstd -xf "$base_installer" -C "$bundle"
[ -d "$bundle/overlay" ] || { echo "base installer has no overlay/ directory" >&2; exit 1; }
[ -f "$bundle/overlay.tar.zst" ] || { echo "base installer has no overlay.tar.zst" >&2; exit 1; }
[ -f "$bundle/install.sh" ] || { echo "base installer has no install.sh" >&2; exit 1; }
[ -d "$bundle/packages" ] || { echo "base installer has no packages/ directory" >&2; exit 1; }

echo "== overlay ARM64 upgrade package =="
tar --zstd -xf "$package" -C "$bundle/overlay" \
  --exclude='./manifest.json' --exclude='./checksums.sha256' --overwrite

rm -f "$bundle/overlay"/usr/lib/*/vpp_plugins/ly_route_orchestrator_plugin.so
find "$bundle/overlay" -type d -name __pycache__ -prune -exec rm -rf {} + 2>/dev/null || true
rm -rf "$bundle/overlay"/run/* "$bundle/overlay"/tmp/* 2>/dev/null || true

printf 'LY_ROUTE_VERSION=%s\nLY_ROUTE_ARTIFACT=arm64-installer\n' "$version" \
  > "$bundle/overlay/etc/ly-route/version.env"
cat > "$bundle/overlay/etc/ly-route/product-manifest.json" <<EOF
{
  "schema_version": 1,
  "artifact_type": "installer",
  "product": "gateway",
  "version": "$version",
  "commit": "$commit",
  "arch": "arm64",
  "suite": "bookworm"
}
EOF
sed -i "s|^APP_VERSION=.*|APP_VERSION=\"$version\"|" "$bundle/install.sh"

echo "== repack overlay =="
epoch=${SOURCE_DATE_EPOCH:-$(date +%s)}
tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
  -C "$bundle/overlay" -cf - . | zstd -19 -q -f -o "$bundle/overlay.tar.zst"
overlay_sha=$(sha256sum "$bundle/overlay.tar.zst" | awk '{print $1}')
echo "overlay sha256=$overlay_sha"

echo "== update manifest and checksums =="
python3 - "$bundle/manifest.json" "$version" "$commit" "$overlay_sha" <<'PY'
import json
import sys

path, version, commit, overlay_sha = sys.argv[1:5]
with open(path, encoding="utf-8") as source:
    manifest = json.load(source)
manifest["version"] = version
manifest["commit"] = commit
manifest.setdefault("components", {}).setdefault("overlay", {})["sha256"] = overlay_sha
with open(path, "w", encoding="utf-8") as target:
    json.dump(manifest, target, ensure_ascii=False, indent=2)
    target.write("\n")
PY

(
  cd "$bundle"
  {
    sha256sum install.sh manifest.json overlay.tar.zst
    sha256sum packages/*.deb
  } > checksums.sha256
  sha256sum -c checksums.sha256 >/dev/null
)

echo "== verify overlay =="
rm -rf "$work/verify" && mkdir -p "$work/verify"
tar --zstd -xf "$bundle/overlay.tar.zst" -C "$work/verify"
grep -qx "LY_ROUTE_VERSION=$version" "$work/verify/etc/ly-route/version.env"
grep -qx "LY_ROUTE_ARTIFACT=arm64-installer" "$work/verify/etc/ly-route/version.env"
plugins=$(find "$work/verify"/usr/lib/*/vpp_plugins -name 'ly_route_*.so' 2>/dev/null | wc -l)
[ "$plugins" -ge 5 ] || { echo "ARM overlay has only $plugins ly_route_* plugins" >&2; exit 1; }
if find "$work/verify" -name 'ly_route_orchestrator_plugin.so' | grep -q .; then
  echo "orchestrator plugin must not be present in the ARM installer" >&2
  exit 1
fi

echo "== build installer =="
out="$artifact_dir/ly-route-gateway-arm64-installer.tar.zst"
rm -f "$out" "$out.sha256"
tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
  -C "$bundle" -cf - . | zstd -19 -q -f -o "$out"
ls -lh "$out"
sha256sum "$out" | tee "$out.sha256"
echo "installer ready: $out"
