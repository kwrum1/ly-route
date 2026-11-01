#!/usr/bin/env bash
# Build the x86_64 Gateway installer ISO from a published base ISO and a release
# upgrade package. This script only assembles binary artifacts; it does not
# compile product source code.
#
# Usage:
#   packaging/build-gateway-iso-from-base.sh \
#     --version 1.1.4 \
#     --package ly-route-upgrade-gateway-bookworm-amd64.tar.zst \
#     --base-iso ly-route-gateway-gui-installer-1.1.3.iso \
#     --out dist
#
# Optional:
#   --work DIR        Reuse/keep a work directory (default: mktemp)
#   --keep-work       Do not delete the work directory
#
# Environment:
#   LY_ROUTE_ISO_WORK_ROOT  Parent directory for mktemp (default: ${TMPDIR:-/tmp})
set -euo pipefail

usage() {
  sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
}

version=
package=
base_iso=
out_dir=dist
work=
keep_work=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version=${2:-}; shift 2 ;;
    --package) package=${2:-}; shift 2 ;;
    --base-iso) base_iso=${2:-}; shift 2 ;;
    --out) out_dir=${2:-}; shift 2 ;;
    --work) work=${2:-}; shift 2 ;;
    --keep-work) keep_work=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$version" ] || { echo "--version is required" >&2; exit 2; }
case "$version" in *[!0-9A-Za-z.+~-]*) echo "invalid version: $version" >&2; exit 2 ;; esac
[ -f "$package" ] || { echo "--package must point to an existing upgrade package: $package" >&2; exit 2; }
[ -f "$base_iso" ] || { echo "--base-iso must point to an existing ISO: $base_iso" >&2; exit 2; }

for command in xorriso zstd tar sha256sum stat awk sed; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
case "$out_dir" in /*) artifact_dir=$out_dir ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
mkdir -p "$artifact_dir"

if [ -z "$work" ]; then
  work_root=${LY_ROUTE_ISO_WORK_ROOT:-${TMPDIR:-/tmp}}
  mkdir -p "$work_root"
  work=$(mktemp -d "$work_root/ly-route-iso.XXXXXX")
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

echo "== extract base payload =="
xorriso -osirrox on -indev "$base_iso" \
  -extract /installer/payload/rootfs.tar.zst "$work/base-rootfs.tar.zst" >/dev/null
[ -s "$work/base-rootfs.tar.zst" ] || { echo "base ISO has no installer payload" >&2; exit 1; }

mkdir -p "$work/rootfs"
tar --zstd -xpf "$work/base-rootfs.tar.zst" -C "$work/rootfs" \
  --no-same-owner --exclude='./dev/*'

echo "== overlay release package =="
tar --zstd -xpf "$package" -C "$work/rootfs" \
  --no-same-owner --exclude='./dev/*' \
  --exclude='./manifest.json' --exclude='./checksums.sha256' --overwrite

# Remove artifacts that are not part of the Gateway product and leftovers that
# must never be baked into an image.
rm -f "$work/rootfs/etc/systemd/system/ly-route-policy-routing.service.d/10-dns-vpp-lifecycle.conf"
rmdir "$work/rootfs/etc/systemd/system/ly-route-policy-routing.service.d" 2>/dev/null || true
rm -f "$work/rootfs"/usr/lib/*/vpp_plugins/ly_route_orchestrator_plugin.so
find "$work/rootfs"/usr/lib/*/vpp_plugins -name '*.so' -exec chmod 0644 {} + 2>/dev/null || true
find "$work/rootfs" -type d -name __pycache__ -prune -exec rm -rf {} + 2>/dev/null || true
rm -rf "$work/rootfs"/run/* "$work/rootfs"/tmp/* 2>/dev/null || true

printf 'LY_ROUTE_VERSION=%s\n' "$version" > "$work/rootfs/etc/ly-route/version.env"

# Fresh installs default to DPDK UIO: VFIO is unstable on Jasper Lake/igc and
# unavailable without an isolated IOMMU group.
env_file="$work/rootfs/etc/ly-route/runtime.env"
touch "$env_file"
if grep -q '^LY_ROUTE_DPDK_TRANSPORT=' "$env_file"; then
  sed -i 's|^LY_ROUTE_DPDK_TRANSPORT=.*|LY_ROUTE_DPDK_TRANSPORT=uio|' "$env_file"
else
  printf 'LY_ROUTE_DPDK_TRANSPORT=uio\n' >> "$env_file"
fi
grep -q '^LY_ROUTE_VPP_ALLOW_ACTIVE_NATIVE_PROBE=' "$env_file" || \
  printf 'LY_ROUTE_VPP_ALLOW_ACTIVE_NATIVE_PROBE=false\n' >> "$env_file"

echo "== repack payload =="
tar -C "$work/rootfs" -cf - . | zstd -T0 -10 -q -o "$work/rootfs.tar.zst"
payload_size=$(stat -c %s "$work/rootfs.tar.zst")
payload_sha=$(sha256sum "$work/rootfs.tar.zst" | awk '{print $1}')
printf '%s  rootfs.tar.zst\n' "$payload_sha" > "$work/rootfs.tar.zst.sha256"
printf '%s\n' "$payload_size" > "$work/rootfs.tar.zst.size"
echo "payload: size=$payload_size sha256=$payload_sha"

echo "== build ISO =="
iso_name=ly-route-gateway-gui-installer.iso
iso_path="$artifact_dir/$iso_name"
rm -f "$iso_path"
xorriso -indev "$base_iso" -outdev "$iso_path" \
  -map "$work/rootfs.tar.zst" /installer/payload/rootfs.tar.zst \
  -map "$work/rootfs.tar.zst.sha256" /installer/payload/rootfs.tar.zst.sha256 \
  -map "$work/rootfs.tar.zst.size" /installer/payload/rootfs.tar.zst.size \
  -boot_image any replay -commit >/dev/null

ls -lh "$iso_path"
sha256sum "$iso_path" | tee "$iso_path.sha256"
echo "ISO ready: $iso_path"
