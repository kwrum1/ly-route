#!/usr/bin/env bash
set -euo pipefail

source_dir=${LY_ROUTE_VPP_SRC:?LY_ROUTE_VPP_SRC is required}
output_dir=${LY_ROUTE_RUNTIME_DEBS_DIR:?LY_ROUTE_RUNTIME_DEBS_DIR is required}
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mirror=${LY_ROUTE_MIRROR:-https://deb.debian.org/debian}
pip_index=${LY_ROUTE_PIP_INDEX_URL:-https://pypi.org/simple}
arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-arm64}
suite=${LY_ROUTE_VPP_BUILD_SUITE:-bookworm}

case "$arch" in
  amd64|arm64) ;;
  *) echo "bookworm VPP builder supports amd64 and arm64, got: $arch" >&2; exit 2 ;;
esac
[ -f "$source_dir/Makefile" ] || { echo "VPP source tree not found: $source_dir" >&2; exit 1; }
command -v mmdebstrap >/dev/null 2>&1 || { echo "mmdebstrap is required" >&2; exit 1; }
command -v chroot >/dev/null 2>&1 || { echo "chroot is required" >&2; exit 1; }

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-bookworm.XXXXXX")
rootfs="$work_dir/rootfs"
mounted=0
cleanup() {
  if [ "$mounted" -eq 1 ]; then
    umount "$rootfs/sys" "$rootfs/proc" "$rootfs/dev/pts" "$rootfs/dev" 2>/dev/null || true
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

include='apt,ca-certificates,gpgv,build-essential,bash,git,sudo,curl,python3,python3-minimal,cmake,ninja-build,meson'
include="$include,dpkg-dev,debhelper,devscripts,pkg-config,libssl-dev,libelf-dev,libpcap-dev,libnuma-dev"
include="$include,libnl-3-dev,libnl-route-3-dev,zlib1g-dev,libunwind-dev,libsystemd-dev,libcap-dev"
include="$include,libmnl-dev,liblz4-dev,liblzma-dev,libzstd-dev,libibverbs-dev,chrpath,python3-all,dh-python,flex,bison,gawk,perl,tar,rsync"
mmdebstrap --mode=root --architectures="$arch" --variant=minbase --components=main \
  --include="$include" \
  "$suite" "$rootfs" "$mirror"

# Make the host download cache visible to VPP's external dependency recipes.
# Those recipes look specifically in /root/Downloads before reaching the
# network, so copy the cache into the isolated Bookworm rootfs.
download_cache=${LY_ROUTE_DOWNLOAD_CACHE:-/root/Downloads}
if [ -d "$download_cache" ]; then
  mkdir -p "$rootfs/root/Downloads"
  cp -a "$download_cache/." "$rootfs/root/Downloads/"
fi

mkdir -p "$rootfs/src/vpp" "$output_dir"
mount --bind /dev "$rootfs/dev"
mount --bind /dev/pts "$rootfs/dev/pts"
mount -t proc proc "$rootfs/proc"
mount -t sysfs sysfs "$rootfs/sys"
mounted=1
cp -a "$source_dir/." "$rootfs/src/vpp/"
source_version=$(git -C "$source_dir" describe --long --match 'v*' HEAD 2>/dev/null || true)
if [ -z "$source_version" ]; then
  source_commit=$(git -C "$source_dir" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
  source_version="v25.10-0-g$source_commit"
fi
printf '%s\n' "$source_version" > "$rootfs/src/vpp/src/scripts/.version"

# Apply repository-owned VPP fixes to the copied build tree. The source cache
# stays untouched so a failed build cannot leave a partially patched checkout.
for patch_path in "$repo_root"/packaging/vpp-patches/*.patch; do
  [ -f "$patch_path" ] || continue
  if git -C "$rootfs/src/vpp" apply --check "$patch_path" >/dev/null 2>&1; then
    git -C "$rootfs/src/vpp" apply --whitespace=nowarn "$patch_path"
  elif git -C "$rootfs/src/vpp" apply --reverse --check "$patch_path" >/dev/null 2>&1; then
    : # The source cache already contains this patch.
  else
    echo "VPP source patch cannot be applied: $patch_path" >&2
    exit 1
  fi
done

rm -rf "$rootfs/src/vpp/build-root"/build-* \
  "$rootfs/src/vpp/build-root"/install-* \
  "$rootfs/src/vpp/build-root"/.deps.ok
rm -f "$rootfs/src/vpp/build-root"/*.deb \
  "$rootfs/src/vpp/build-root"/*.buildinfo \
  "$rootfs/src/vpp/build-root"/*.changes
make_args=${LY_ROUTE_VPP_MAKE_ARGS:-DPDK_MLX_IBV_LINK=dlopen}
LY_ROUTE_VPP_MAKE_ARGS="$make_args" PIP_INDEX_URL="$pip_index" PIP_DEFAULT_TIMEOUT=120 PIP_RETRIES=10 \
  PIP_DISABLE_PIP_VERSION_CHECK=1 chroot "$rootfs" /bin/bash -lc \
  'export DEBIAN_FRONTEND=noninteractive; git config --global --add safe.directory /src/vpp && cd /src/vpp && make UNATTENDED=y install-dep && make UNATTENDED=y SOURCE_PATH=/src/vpp PLATFORM=vpp vpp_arch=native ${LY_ROUTE_VPP_MAKE_ARGS:-DPDK_MLX_IBV_LINK=dlopen} pkg-deb'

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
for package in "$output_dir"/*.deb; do
  [ -f "$package" ] || continue
  package_arch=$(dpkg-deb -f "$package" Architecture)
  [ "$package_arch" = "$arch" ] || {
    echo "VPP package architecture mismatch: $package ($package_arch, expected $arch)" >&2
    exit 1
  }
  package_deps=$(dpkg-deb -f "$package" Pre-Depends Depends Recommends Suggests 2>/dev/null || true)
  case "$package_deps" in
    *'libc6 (>= 2.38)'*|*'libc6 (>= 2.39)'*|*'libc6 (>= 2.40)'*|*'libc6 (>= 2.41)'*|*'libc6 (>= 2.42)'*|*libssl3t64*)
      echo "VPP package is not Bookworm-compatible: $package" >&2
      printf '%s\n' "$package_deps" >&2
      exit 1
      ;;
  esac
done
printf 'Bookworm-compatible VPP packages written to %s\n' "$output_dir"
