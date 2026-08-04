#!/usr/bin/env bash
set -euo pipefail

source_dir=${LY_ROUTE_VPP_SRC:?LY_ROUTE_VPP_SRC is required}
output_dir=${LY_ROUTE_RUNTIME_DEBS_DIR:?LY_ROUTE_RUNTIME_DEBS_DIR is required}
mirror=${LY_ROUTE_MIRROR:-https://deb.debian.org/debian}
arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-arm64}
suite=${LY_ROUTE_VPP_BUILD_SUITE:-bookworm}

case "$arch" in
  arm64) ;;
  *) echo "bookworm VPP builder currently supports arm64, got: $arch" >&2; exit 2 ;;
esac
[ -f "$source_dir/Makefile" ] || { echo "VPP source tree not found: $source_dir" >&2; exit 1; }
command -v mmdebstrap >/dev/null 2>&1 || { echo "mmdebstrap is required" >&2; exit 1; }
command -v chroot >/dev/null 2>&1 || { echo "chroot is required" >&2; exit 1; }

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-bookworm.XXXXXX")
rootfs="$work_dir/rootfs"
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT INT TERM

include='apt,ca-certificates,build-essential,bash,git,sudo,curl,python3,python3-minimal,cmake,ninja-build,meson'
include="$include,dpkg-dev,debhelper,devscripts,pkg-config,libssl-dev,libelf-dev,libpcap-dev,libnuma-dev"
include="$include,libnl-3-dev,libnl-route-3-dev,zlib1g-dev,libunwind-dev,libsystemd-dev,libcap-dev"
include="$include,libmnl-dev,liblz4-dev,liblzma-dev,libzstd-dev,flex,bison,gawk,perl,tar,rsync"
mmdebstrap --mode=root --architectures="$arch" --variant=minbase --components=main \
  --include="$include" \
  "$suite" "$rootfs" "$mirror"

mkdir -p "$rootfs/src/vpp" "$output_dir"
cp -a "$source_dir/." "$rootfs/src/vpp/"
chroot "$rootfs" /bin/bash -lc \
  'git config --global --add safe.directory /src/vpp && cd /src/vpp && make UNATTENDED=y install-dep && make UNATTENDED=y pkg-deb'

found=0
while IFS= read -r package; do
  cp "$package" "$output_dir/"
  found=1
done < <(find "$rootfs/src/vpp" -type f -name '*.deb' \
  \( -path '*/build-root/*.deb' -o -path '*/build-root/packages/*.deb' -o -path '*/build-root/install-vpp-native/*.deb' \))

if [ "$found" -ne 1 ]; then
  echo "Bookworm VPP build completed but no .deb packages were found" >&2
  exit 1
fi
printf 'Bookworm-compatible VPP packages written to %s\n' "$output_dir"
