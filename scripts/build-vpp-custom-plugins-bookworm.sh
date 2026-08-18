#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# The first chroot package transaction has no CA bundle yet. Keep this
# bootstrap on the USTC mirror over HTTP; package signatures still authenticate
# the Debian archives.
mirror=${LY_ROUTE_MIRROR:-http://mirrors.ustc.edu.cn/debian}
security_mirror=${LY_ROUTE_SECURITY_MIRROR:-http://mirrors.ustc.edu.cn/debian-security}
arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-amd64}
debs=${LY_ROUTE_VPP_DEBS_DIR:-$repo_root/runtime-debs-bookworm-dma}
out=${LY_ROUTE_VPP_PLUGIN_OUTPUT_DIR:-$repo_root/runtime-plugins-bookworm}
work=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-plugins.XXXXXX")
rootfs=$work/rootfs

cleanup() {
  for mountpoint in "$rootfs/sys" "$rootfs/proc" "$rootfs/dev/pts" "$rootfs/dev"; do
    mountpoint -q "$mountpoint" && umount -l "$mountpoint" || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

case "$arch" in
  amd64) debian_arch=amd64 ;;
  arm64) debian_arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

for package in vpp-dev libvppinfra-dev; do
  compgen -G "$debs/${package}_*_${debian_arch}.deb" >/dev/null || {
    echo "missing $package package in $debs" >&2
    exit 1
  }
done

mkdir -p "$rootfs" "$out"
mmdebstrap --architectures="$debian_arch" --variant=minbase --components=main \
  --aptopt="Acquire::Retries \"3\";" \
  --aptopt="Acquire::http::Timeout \"30\";" \
  bookworm "$rootfs" "$mirror"

cat > "$rootfs/etc/apt/sources.list" <<EOF
deb $mirror bookworm main
deb $mirror bookworm-updates main
deb $security_mirror bookworm-security main
EOF

mount --bind /dev "$rootfs/dev"
mount --bind /dev/pts "$rootfs/dev/pts"
mount -t proc proc "$rootfs/proc"
mount -t sysfs sysfs "$rootfs/sys"

mkdir -p "$rootfs/src/ly-route" "$rootfs/packages" "$rootfs/out"
cp -a "$repo_root/runtime" "$repo_root/scripts" "$rootfs/src/ly-route/"
cp "$debs"/vpp-dev_*_"$debian_arch".deb "$debs"/libvppinfra-dev_*_"$debian_arch".deb "$rootfs/packages/"

chroot "$rootfs" /bin/bash -eu -c '
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends build-essential cmake pkg-config
  export LY_ROUTE_VPP_DEV_DEBS_DIR=/packages
  export LY_ROUTE_VPP_SECURITY_GUARD_BUILD_DIR=/build/security-guard
  export LY_ROUTE_VPP_SMART_QOS_BUILD_DIR=/build/smart-qos
  export LY_ROUTE_VPP_PPPOE_CLIENT_BUILD_DIR=/build/pppoe-client
  export LY_ROUTE_VPP_DNS_INTERCEPT_BUILD_DIR=/build/dns-intercept
  export LY_ROUTE_VPP_PRE_NAT_ROUTE_BUILD_DIR=/build/pre-nat-route
  if ! /src/ly-route/scripts/build-vpp-security-guard-plugin.sh > /tmp/security-plugin-build.log 2>&1; then
    cat /tmp/security-plugin-build.log
    exit 1
  fi
  if ! /src/ly-route/scripts/build-vpp-smart-qos-plugin.sh > /tmp/qos-plugin-build.log 2>&1; then
    cat /tmp/qos-plugin-build.log
    exit 1
  fi
  security_plugin=$(tail -n 1 /tmp/security-plugin-build.log)
  qos_plugin=$(tail -n 1 /tmp/qos-plugin-build.log)
  if ! /src/ly-route/scripts/build-vpp-pppoe-client-plugin.sh > /tmp/pppoe-plugin-build.log 2>&1; then
    cat /tmp/pppoe-plugin-build.log
    exit 1
  fi
  pppoe_plugin=$(tail -n 1 /tmp/pppoe-plugin-build.log)
  if ! /src/ly-route/scripts/build-vpp-dns-intercept-plugin.sh > /tmp/dns-intercept-plugin-build.log 2>&1; then
    cat /tmp/dns-intercept-plugin-build.log
    exit 1
  fi
  dns_intercept_plugin=$(tail -n 1 /tmp/dns-intercept-plugin-build.log)
  if ! sh /src/ly-route/scripts/build-vpp-pre-nat-route-plugin.sh > /tmp/pre-nat-route-plugin-build.log 2>&1; then
    cat /tmp/pre-nat-route-plugin-build.log
    exit 1
  fi
  pre_nat_route_plugin=$(tail -n 1 /tmp/pre-nat-route-plugin-build.log)
  install -m 0644 "$security_plugin" /out/ly_route_security_guard_plugin.so
  install -m 0644 "$qos_plugin" /out/ly_route_smart_qos_plugin.so
  install -m 0644 "$pppoe_plugin" /out/ly_route_pppoe_client_plugin.so
  install -m 0644 "$dns_intercept_plugin" /out/ly_route_dns_intercept_plugin.so
  install -m 0644 "$pre_nat_route_plugin" /out/ly_route_pre_nat_route_plugin.so
  ldd /out/ly_route_security_guard_plugin.so
  ldd /out/ly_route_smart_qos_plugin.so
  ldd /out/ly_route_pppoe_client_plugin.so
  ldd /out/ly_route_dns_intercept_plugin.so
  ldd /out/ly_route_pre_nat_route_plugin.so
'

install -m 0644 "$rootfs/out/ly_route_security_guard_plugin.so" "$out/ly_route_security_guard_plugin.so"
install -m 0644 "$rootfs/out/ly_route_smart_qos_plugin.so" "$out/ly_route_smart_qos_plugin.so"
install -m 0644 "$rootfs/out/ly_route_pppoe_client_plugin.so" "$out/ly_route_pppoe_client_plugin.so"
install -m 0644 "$rootfs/out/ly_route_dns_intercept_plugin.so" "$out/ly_route_dns_intercept_plugin.so"
install -m 0644 "$rootfs/out/ly_route_pre_nat_route_plugin.so" "$out/ly_route_pre_nat_route_plugin.so"
for plugin in "$out"/*.so; do
  if readelf --version-info "$plugin" | grep -q 'GLIBC_2\.3[89]\|GLIBC_2\.4[0-9]'; then
    echo "plugin requires a post-Bookworm glibc: $plugin" >&2
    exit 1
  fi
done
sha256sum "$out"/*.so
