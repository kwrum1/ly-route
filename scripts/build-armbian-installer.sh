#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-armbian-installer.sh --product gateway --rootfs FILE --runtime-debs DIR [--out dist/armbian]

Builds a self-contained one-click installer for an existing Armbian Bookworm
arm64 system. The installer preserves the board kernel, DTB, bootloader and /boot.
EOF
}

product=
rootfs=
runtime_debs=
out_dir=dist/armbian
while [ "$#" -gt 0 ]; do
  case "$1" in
    --product) product=${2:-}; shift 2 ;;
    --rootfs) rootfs=${2:-}; shift 2 ;;
    --runtime-debs) runtime_debs=${2:-}; shift 2 ;;
    --out) out_dir=${2:-}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ "$product" = gateway ] || { echo "--product must be gateway" >&2; exit 2; }
[ -f "$rootfs" ] || { echo "--rootfs must point to an existing archive" >&2; exit 2; }
[ -f "$rootfs.sha256" ] || { echo "rootfs checksum is missing: $rootfs.sha256" >&2; exit 1; }
[ -d "$runtime_debs" ] || { echo "--runtime-debs must point to a package directory" >&2; exit 2; }
(cd "$(dirname "$rootfs")" && sha256sum -c "$(basename "$rootfs").sha256" >/dev/null)

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$out_dir" in /*) artifact_dir=$out_dir ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
mkdir -p "$artifact_dir"
work=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-armbian.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM
extracted="$work/rootfs"
bundle="$work/bundle"
overlay="$bundle/overlay"
mkdir -p "$extracted" "$overlay" "$bundle/packages"

case "$rootfs" in
  *.tar.zst) tar --use-compress-program=unzstd -xf "$rootfs" -C "$extracted" ;;
  *.tar.gz) tar -xzf "$rootfs" -C "$extracted" ;;
  *.tar) tar -xf "$rootfs" -C "$extracted" ;;
  *) echo "unsupported rootfs format: $rootfs" >&2; exit 2 ;;
esac

node - "$extracted/usr/share/ly-route/artifact-manifest.json" <<'NODE'
const fs = require("node:fs");
const manifest = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
if (manifest.product !== "gateway" || manifest.arch !== "arm64" || manifest.artifact_type !== "rootfs") {
  throw new Error("rootfs must be a gateway/arm64 rootfs artifact");
}
NODE

copy_path() {
  source=$1
  [ -e "$extracted/$source" ] || return 0
  mkdir -p "$overlay/$(dirname "$source")"
  cp -a "$extracted/$source" "$overlay/$source"
}
for path in \
  etc/ly-route etc/vpp etc/kea \
  etc/nginx/conf.d/ly-route-admin.conf \
  etc/systemd/network/10-ethernet-dhcp.network \
  opt/ly-route usr/lib/ly-route usr/share/ly-route; do
  copy_path "$path"
done
mkdir -p "$overlay/etc/systemd/system"
for path in "$extracted"/etc/systemd/system/ly-route* "$extracted"/etc/systemd/system/kea-dhcp4-server.service.d; do
  [ -e "$path" ] || continue
  cp -a "$path" "$overlay/etc/systemd/system/"
done

required_packages='libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply ly-route-vpp-smart-qos ly-route-vpp-security-guard ly-route-vpp-pppoe-client ly-route-dns-vpp-proxy smartdns xray'
for package in $required_packages; do
  found=
  for deb in "$runtime_debs/${package}_"*"_arm64.deb"; do
    [ -f "$deb" ] || continue
    [ "$(dpkg-deb -f "$deb" Package)" = "$package" ] || continue
    cp "$deb" "$bundle/packages/"
    found=true
    break
  done
  [ "$found" = true ] || { echo "missing arm64 runtime package: $package" >&2; exit 1; }
done

source_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$repo_root" log -1 --format=%ct 2>/dev/null || date +%s)}
tar --sort=name --mtime="@$source_epoch" --owner=0 --group=0 --numeric-owner \
  -C "$overlay" -cf "$bundle/overlay.tar" .
zstd -19 -f "$bundle/overlay.tar" -o "$bundle/overlay.tar.zst" >/dev/null
rm "$bundle/overlay.tar"

cat > "$bundle/install.sh" <<'INSTALL'
#!/bin/bash
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "Run this installer as root." >&2; exit 1; }
[ "$(dpkg --print-architecture)" = arm64 ] || { echo "This package requires arm64." >&2; exit 1; }
[ -f /etc/armbian-release ] || { echo "This package requires an installed Armbian system." >&2; exit 1; }
. /etc/os-release
[ "${VERSION_CODENAME:-}" = bookworm ] || { echo "Only Armbian Bookworm is supported by this release." >&2; exit 1; }
[ ! -e /etc/ly-route/product-manifest.json ] || {
  echo "Ly Route is already installed. Apply the arm64 upgrade package from the web console." >&2
  exit 1
}

bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
(cd "$bundle_dir" && sha256sum -c checksums.sha256)
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates nginx-light openssh-server sudo openssl zstd \
  iproute2 iputils-ping kea-dhcp4-server isc-dhcp-client python3 libunwind8 \
  libnl-3-200 libnl-route-3-200 libpcap0.8 libnuma1 libelf1 zlib1g
apt-get install -y "$bundle_dir"/packages/*.deb
zstd -dc "$bundle_dir/overlay.tar.zst" | tar -xpf - -C /
systemctl daemon-reload
systemctl enable systemd-networkd.service systemd-resolved.service nginx.service \
  ly-route-firstboot.service ly-route-runtime-check.service ly-route-vpp-tune.service \
  ly-route-vpp-apply.service ly-route-control-api.service ly-route-recovery.service \
  ly-route-pppoe.target
systemctl enable vpp.service smartdns.service kea-dhcp4-server.service xray.service
/usr/lib/ly-route/firstboot.sh
systemctl restart ly-route-runtime-check.service
systemctl restart ly-route-control-api.service nginx.service
echo "Ly Route installation completed. Open https://192.168.88.1/ and sign in with admin/password."
INSTALL
chmod 0755 "$bundle/install.sh"

(
  cd "$bundle"
  sha256sum overlay.tar.zst packages/*.deb > checksums.sha256
)
cat > "$bundle/manifest.json" <<EOF
{
  "schema_version": 1,
  "artifact_type": "armbian-one-click-installer",
  "product": "gateway",
  "suite": "bookworm",
  "arch": "arm64",
  "preserves_board_boot_stack": true
}
EOF

artifact="$artifact_dir/ly-route-gateway-armbian-bookworm-arm64-installer.tar.zst"
tar --sort=name --mtime="@$source_epoch" --owner=0 --group=0 --numeric-owner \
  -C "$bundle" -cf "$work/installer.tar" .
zstd -19 -f "$work/installer.tar" -o "$artifact" >/dev/null
sha256sum "$artifact" > "$artifact.sha256"
printf 'Built %s\n' "$artifact"
