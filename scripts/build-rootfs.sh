#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/build-rootfs.sh --product gateway|orchestrator [--arch amd64|arm64] [--suite bookworm] [--out dist/rootfs] [--manifest PATH] [--frontend-bundle DIRECTORY]

Builds a product-specific Debian-based Ly Route rootfs.

Environment:
  LY_ROUTE_MIRROR                  Debian mirror URL.
  LY_ROUTE_EXTRA_PACKAGES          Comma-separated extra packages.
  LY_ROUTE_EXTRA_DEBS_DIR          Directory containing local .deb packages.
  LY_ROUTE_CONTROL_BINARY          Prebuilt product-specific control binary.
  LY_ROUTE_CONTROL_PRODUCT         Product ID for LY_ROUTE_CONTROL_BINARY.
  LY_ROUTE_PPPOE_CLIENT_BINARY     Prebuilt native PPPoE client for gateway builds.
  LY_ROUTE_VPP_APPLY_BINARY        Prebuilt vpp-apply binary for fixture builds.
  LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1 Create an overlay-only fixture without mmdebstrap.
  LY_ROUTE_BUILD_TAR_ONLY=1        Leave the deterministic artifact uncompressed.
  SOURCE_DATE_EPOCH                Reproducible archive timestamp.
EOF
}

arch=amd64
suite=bookworm
out_dir=dist/rootfs
product=
manifest=
frontend_bundle=
mirror=${LY_ROUTE_MIRROR:-http://deb.debian.org/debian}
extra_packages=${LY_ROUTE_EXTRA_PACKAGES:-}
extra_debs_dir=${LY_ROUTE_EXTRA_DEBS_DIR:-}
control_binary=${LY_ROUTE_CONTROL_BINARY:-}
pppoe_client_binary=${LY_ROUTE_PPPOE_CLIENT_BINARY:-}
vpp_apply_binary=${LY_ROUTE_VPP_APPLY_BINARY:-}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --arch|--suite|--out|--product|--manifest|--frontend-bundle)
      option=$1
      [ "$#" -ge 2 ] && [ -n "$2" ] || { echo "$option requires a value" >&2; exit 2; }
      case "$option" in
        --arch) arch=$2 ;;
        --suite) suite=$2 ;;
        --out) out_dir=$2 ;;
        --product) product=$2 ;;
        --manifest) manifest=$2 ;;
        --frontend-bundle) frontend_bundle=$2 ;;
      esac
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$product" ] || { echo "--product is required (gateway or orchestrator)" >&2; exit 2; }
case "$product" in
  gateway|orchestrator) ;;
  *) echo "Unsupported product: $product (expected gateway or orchestrator)" >&2; exit 2 ;;
esac
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 2 ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
geodata_dir=${LY_ROUTE_GEODATA_DIR:-$repo_root/packaging/geodata}
. "$repo_root/packaging/runtime-boundaries/gateway.sh"
. "$repo_root/scripts/lib/product-build-profile.sh"
load_product_build_profile "$repo_root" "$product" "$manifest"
export LY_ROUTE_SOURCE_FINGERPRINT=$(product_source_fingerprint "$repo_root")
validate_prebuilt_control "$control_binary" "$product"
if [ -n "$pppoe_client_binary" ]; then
  [ "$product" = gateway ] || product_build_fail "LY_ROUTE_PPPOE_CLIENT_BINARY is only valid for gateway builds"
  [ -x "$pppoe_client_binary" ] || product_build_fail "native PPPoE client is not executable: $pppoe_client_binary"
fi
validate_prebuilt_vpp_apply "$vpp_apply_binary"

product_build_require_file "$repo_root/scripts/build-controller-shell.sh"
product_build_require_file "$repo_root/packaging/nginx/ly-route-admin.conf"
if [ -n "$frontend_bundle" ]; then
  validate_frontend_bundle "$frontend_bundle" "$product"
else
  for source in index.html styles.css shell.js app.js; do
    product_build_require_file "$repo_root/frontend/$product/$source"
  done
fi

case "$product,$extra_packages" in
  orchestrator,*smartdns*|orchestrator,*kea-dhcp4-server*|orchestrator,*isc-dhcp-client*|orchestrator,*xray*|orchestrator,*ppp*)
    product_build_fail "Orchestrator extra packages contain a forbidden Gateway package"
    ;;
esac

validate_extra_debs() {
  if [ -z "$extra_debs_dir" ]; then
    [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" = 1 ] || \
      product_build_fail "LY_ROUTE_EXTRA_DEBS_DIR is required for complete product rootfs builds"
    return 0
  fi
  [ -d "$extra_debs_dir" ] || product_build_fail "LY_ROUTE_EXTRA_DEBS_DIR does not exist: $extra_debs_dir"
  runtime_stamp="$extra_debs_dir/.ly-route-source-fingerprint"
  [ -f "$runtime_stamp" ] || product_build_fail "Runtime package directory has no source fingerprint: $extra_debs_dir"
  expected_runtime_fingerprint=$(product_source_fingerprint "$repo_root")
  actual_runtime_fingerprint=$(cat "$runtime_stamp")
  [ "$actual_runtime_fingerprint" = "$expected_runtime_fingerprint" ] ||
    product_build_fail "Runtime package directory is stale: expected $expected_runtime_fingerprint, got $actual_runtime_fingerprint"
  [ ! -e "$extra_debs_dir/.ly-route-build-in-progress" ] ||
    product_build_fail "Runtime package directory contains an incomplete build marker: $extra_debs_dir"
  set -- "$extra_debs_dir"/*.deb
  [ -f "$1" ] || product_build_fail "LY_ROUTE_EXTRA_DEBS_DIR contains no .deb packages: $extra_debs_dir"
  command -v dpkg-deb >/dev/null 2>&1 || product_build_fail "dpkg-deb is required to inspect LY_ROUTE_EXTRA_DEBS_DIR"
  for package_path in "$extra_debs_dir"/*.deb; do
    package_name=$(dpkg-deb -f "$package_path" Package 2>/dev/null) || product_build_fail "Invalid deb package: $package_path"
    package_arch=$(dpkg-deb -f "$package_path" Architecture 2>/dev/null) || product_build_fail "Invalid deb package: $package_path"
    case "$package_arch" in
      "$arch"|all) ;;
      *) product_build_fail "Package architecture mismatch: $package_name is $package_arch, expected $arch" ;;
    esac
    if [ "$product" = orchestrator ]; then
      case "$package_name" in
        smartdns|kea-dhcp4-server|isc-dhcp-client|xray|ppp)
          product_build_fail "Orchestrator package set contains forbidden package: $package_name"
          ;;
      esac
    fi
  done
  if [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" != 1 ]; then
  required_runtime_packages='libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply'
  [ "$product" != gateway ] || required_runtime_packages="$LY_ROUTE_GATEWAY_CUSTOM_RUNTIME_PACKAGES"
	  [ "$product" != orchestrator ] || required_runtime_packages="$required_runtime_packages ly-route-vpp-orchestrator"
    for required in $required_runtime_packages; do
      found=false
      for package_path in "$extra_debs_dir"/*.deb; do
        [ "$(dpkg-deb -f "$package_path" Package 2>/dev/null)" != "$required" ] || found=true
      done
      [ "$found" = true ] || product_build_fail "LY_ROUTE_EXTRA_DEBS_DIR is missing required runtime package: $required"
    done
  fi
}
validate_extra_debs

overlay="$repo_root/packaging/rootfs-overlay"
source_commit=$(product_source_commit)
source_epoch=$(product_source_date_epoch)
artifact_base="ly-route-rootfs-${product}-${suite}-${arch}"
if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" = 1 ]; then
  artifact_name="$artifact_base.tar"
elif command -v zstd >/dev/null 2>&1; then
  artifact_name="$artifact_base.tar.zst"
else
  artifact_name="$artifact_base.tar.gz"
fi
case "$out_dir" in
  /*) artifact_dir=$out_dir ;;
  *) artifact_dir="$repo_root/$out_dir" ;;
esac
artifact="$artifact_dir/$artifact_name"
tar_artifact="$artifact_dir/$artifact_base.tar"
work_parent=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-rootfs.XXXXXX")
rootfs="$work_parent/rootfs-$arch"

cleanup_rootfs() {
  target=$1
  for mount_path in "$target/proc" "$target/sys" "$target/dev/pts" "$target/dev"; do
    if mountpoint -q "$mount_path" 2>/dev/null; then
      umount "$mount_path" 2>/dev/null || umount -l "$mount_path" 2>/dev/null || true
    fi
  done
  rm -rf "$target"
}
cleanup_workdir() {
  cleanup_rootfs "$rootfs"
  rm -rf "$work_parent"
}
trap cleanup_workdir EXIT INT TERM

copy_payload() {
  target=$1
  mkdir -p "$target/opt/ly-route/admin" "$target/etc/nginx/conf.d"
  copy_product_frontend "$target/opt/ly-route/admin" "$frontend_bundle" "$product"
  cp "$repo_root/packaging/nginx/ly-route-admin.conf" "$target/etc/nginx/conf.d/ly-route-admin.conf"
  rm -f "$target/etc/nginx/sites-enabled/default"
  if [ "$product" = gateway ]; then
    if [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" != 1 ]; then
      for geodata_file in geoip.dat geosite.dat china-list.txt manifest.json; do
        product_build_require_file "$geodata_dir/$geodata_file"
      done
    fi
    if [ -d "$geodata_dir" ]; then
      mkdir -p "$target/usr/share/ly-route/geodata"
      for geodata_file in geoip.dat geosite.dat china-list.txt manifest.json; do
        [ -f "$geodata_dir/$geodata_file" ] && cp "$geodata_dir/$geodata_file" "$target/usr/share/ly-route/geodata/$geodata_file"
      done
    fi
  fi
}

install_extra_debs() {
  target=$1
  [ -n "$extra_debs_dir" ] || return 0
  if [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" = 1 ]; then
    for package_path in "$extra_debs_dir"/*.deb; do
      dpkg-deb -x "$package_path" "$target"
    done
    if [ -f "$target/etc/smartdns/smartdns.conf" ]; then
      sed -i 's/^bind .*/bind 127.0.0.1:1053 -no-speed-check/; s/^bind-tcp .*/bind-tcp 127.0.0.1:1053 -no-speed-check/; /^server /d' "$target/etc/smartdns/smartdns.conf"
      sed -i 's#^conf-file .*#conf-file /etc/smartdns/conf.d/ly-route-active.conf#' "$target/etc/smartdns/smartdns.conf"
    fi
    vcl_library=$(find "$target/usr/lib" -type f -name 'libvcl_ldpreload.so.*' -print -quit)
    if [ -n "$vcl_library" ]; then
      ln -sf "${vcl_library#"$target"}" "$target/usr/lib/ly-route/libvcl_ldpreload.so"
    fi
    return 0
  fi
  mkdir -p "$target/tmp/ly-route-extra-debs"
  cp "$extra_debs_dir"/*.deb "$target/tmp/ly-route-extra-debs/"
  env -u TMPDIR chroot "$target" /bin/sh -c 'DEBIAN_FRONTEND=noninteractive dpkg --force-confold -i /tmp/ly-route-extra-debs/*.deb || true; DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold -f install -y; DEBIAN_FRONTEND=noninteractive dpkg --force-confold --configure -a'
  rm -rf "$target/tmp/ly-route-extra-debs"
  if [ -f "$target/lib/systemd/system/smartdns.service" ]; then
    sed -i 's#ExecStart=/usr/sbin/smartdns -f -c /etc/smartdns/smartdns.conf#ExecStart=/usr/sbin/smartdns -f -p - -c /etc/smartdns/smartdns.conf#' "$target/lib/systemd/system/smartdns.service"
  fi
  vcl_library=$(find "$target/usr/lib" -type f -name 'libvcl_ldpreload.so.*' -print -quit)
  if [ -n "$vcl_library" ]; then
    ln -sf "${vcl_library#"$target"}" "$target/usr/lib/ly-route/libvcl_ldpreload.so"
  fi
  if [ -f "$target/etc/smartdns/smartdns.conf" ]; then
    sed -i 's/^bind .*/bind 127.0.0.1:1053 -no-speed-check/; s/^bind-tcp .*/bind-tcp 127.0.0.1:1053 -no-speed-check/; /^server /d' "$target/etc/smartdns/smartdns.conf"
    sed -i 's#^conf-file .*#conf-file /etc/smartdns/conf.d/ly-route-active.conf#' "$target/etc/smartdns/smartdns.conf"
  fi
}

build_control_binary() {
  target=$1
  mkdir -p "$target/usr/lib/ly-route"
  if [ -n "$control_binary" ]; then
    cp "$control_binary" "$target/usr/lib/ly-route/ly-route-control"
  else
    case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; esac
    command -v go >/dev/null 2>&1 || product_build_fail "go is required to build ly-route-control for rootfs packaging"
    (cd "$repo_root/backend" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$target/usr/lib/ly-route/ly-route-control" "./cmd/$product-control")
  fi
  chmod 0755 "$target/usr/lib/ly-route/ly-route-control"
}

build_pppoe_client_binary() {
  target=$1
  [ "$product" = gateway ] || return 0
  mkdir -p "$target/usr/lib/ly-route"
  case "$arch" in
    amd64) goarch=amd64 ;;
    arm64) goarch=arm64 ;;
    *) product_build_fail "native PPPoE client is not supported for rootfs architecture: $arch" ;;
  esac
  command -v go >/dev/null 2>&1 || product_build_fail "go is required to build the native PPPoE client"
  if [ -n "${pppoe_client_binary:-}" ]; then
    cp "$pppoe_client_binary" "$target/usr/lib/ly-route/ly-route-pppoe-client"
  else
    (cd "$repo_root/backend" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
      -o "$target/usr/lib/ly-route/ly-route-pppoe-client" ./cmd/ly-route-pppoe-client)
  fi
  chmod 0755 "$target/usr/lib/ly-route/ly-route-pppoe-client"
}

configure_product() {
  target=$1
  mkdir -p "$target/etc/ly-route" "$target/usr/share/ly-route" "$target/var/lib/ly-route/$product"
  cp "$PRODUCT_MANIFEST" "$target/etc/ly-route/product-manifest.json"
  cp "$PRODUCT_DEFAULT_CONFIG" "$target/etc/ly-route/default-config.json"
  cat >"$target/etc/ly-route/runtime.env" <<EOF
LY_ROUTE_ENABLE_SERVICE_RUNTIME=true
LY_ROUTE_PRODUCT=$product
LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=false
LY_ROUTE_REQUIRED_COMMANDS=$PRODUCT_REQUIRED_COMMANDS
LY_ROUTE_REQUIRED_UNITS=$PRODUCT_REQUIRED_UNITS
LY_ROUTE_RUNTIME_READINESS=/var/lib/ly-route/runtime-readiness.json
LY_ROUTE_VPP_APPLY_COMMAND=/usr/lib/ly-route/vpp-apply
LY_ROUTE_VPP_COMMAND_MAP=/etc/ly-route/vpp-command-map.json
LY_ROUTE_VPP_RECEIPT=/var/lib/ly-route/vpp-apply-receipt.json
LY_ROUTE_VPP_CAPABILITY_PROOF=/var/lib/ly-route/vpp-native-capabilities.json
LY_ROUTE_VPP_PROBE_COMMAND=/usr/lib/ly-route/runtime-check.sh
LY_ROUTE_VPP_DATA_INTERFACES=
LY_ROUTE_VPP_PROOF_TTL_SECONDS=300
LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE=false
EOF
  service="$target/etc/systemd/system/ly-route-control-api.service"
  awk -v profile='Environment=LY_ROUTE_PRODUCT_PROFILE=/etc/ly-route/product-manifest.json' \
      -v database="Environment=LY_ROUTE_DB_PATH=$PRODUCT_DATABASE_PATH" \
      -v config="Environment=LY_ROUTE_CONFIG_PATH=$PRODUCT_CONFIG_PATH" '
    /^Environment=LY_ROUTE_CONFIG_PATH=/ { print profile; print database; print config; next }
    { print }
  ' "$service" >"$service.tmp"
  mv "$service.tmp" "$service"
  if [ "$product" = gateway ]; then
    # PPPoE is implemented by Ly Route's native VPP client. The legacy pppd
    # unit must never enter a production gateway rootfs.
    rm -f "$target/etc/systemd/system/pppd@.service"
  elif [ "$product" = orchestrator ]; then
    rm -rf "$target/etc/kea" "$target/etc/smartdns" "$target/etc/xray" "$target/etc/ppp"
    rm -f "$target/etc/systemd/system/pppd@.service" \
      "$target/etc/systemd/system/ly-route-pppoe.target" \
      "$target/etc/systemd/system/ly-route-pppoe@.service" \
      "$target/etc/systemd/system/ly-route-policy-routing.service"
    rm -rf "$target/etc/systemd/system/kea-dhcp4-server.service.d"
    sed -i '/kea-dhcp4-server\.service/d; s/ with DHCP enabled//' "$target/usr/lib/ly-route/firstboot.sh"
  fi
}

enable_units() {
  target=$1
  wants="$target/etc/systemd/system/multi-user.target.wants"
  timer_wants="$target/etc/systemd/system/timers.target.wants"
  mkdir -p "$wants" "$timer_wants" "$target/etc/systemd/system/network-online.target.wants"
  ln -sf /lib/systemd/system/systemd-networkd.service "$wants/systemd-networkd.service"
  ln -sf /lib/systemd/system/systemd-resolved.service "$wants/systemd-resolved.service"
  ln -sf /etc/systemd/system/ly-route-runtime-check.service "$wants/ly-route-runtime-check.service"
  ln -sf /etc/systemd/system/ly-route-vpp-apply.service "$wants/ly-route-vpp-apply.service"
  ln -sf /etc/systemd/system/ly-route-vpp-session-enable.service "$wants/ly-route-vpp-session-enable.service"
  ln -sf /etc/systemd/system/ly-route-control-api.service "$wants/ly-route-control-api.service"
  ln -sf /etc/systemd/system/ly-route-recovery.service "$wants/ly-route-recovery.service"
  ln -sf /etc/systemd/system/ly-route-vpp-tune.service "$wants/ly-route-vpp-tune.service"
  ln -sf /etc/systemd/system/ly-route-vfio-preflight.service "$wants/ly-route-vfio-preflight.service"
  if [ -f "$target/usr/lib/systemd/system/vpp.service" ]; then
    ln -sf /usr/lib/systemd/system/vpp.service "$wants/vpp.service"
  elif [ -f "$target/lib/systemd/system/vpp.service" ]; then
    ln -sf /lib/systemd/system/vpp.service "$wants/vpp.service"
  fi
  [ ! -f "$target/lib/systemd/system/ssh.service" ] || ln -sf /lib/systemd/system/ssh.service "$wants/ssh.service"
  ln -sf /lib/systemd/system/nginx.service "$wants/nginx.service"
  ln -sf /etc/systemd/system/ly-route-firstboot.service "$wants/ly-route-firstboot.service"
  if [ "$product" = gateway ]; then
    ln -sf /etc/systemd/system/ly-route-pppoe.target "$wants/ly-route-pppoe.target"
    ln -sf /lib/systemd/system/kea-dhcp4-server.service "$wants/kea-dhcp4-server.service"
    [ ! -f "$target/lib/systemd/system/smartdns.service" ] || ln -sf /lib/systemd/system/smartdns.service "$wants/smartdns.service"
    [ ! -f "$target/lib/systemd/system/ly-route-dns-vpp-proxy.service" ] || ln -sf /lib/systemd/system/ly-route-dns-vpp-proxy.service "$wants/ly-route-dns-vpp-proxy.service"
    [ ! -f "$target/lib/systemd/system/ly-route-dns-vpp-proxy-v6.service" ] || ln -sf /lib/systemd/system/ly-route-dns-vpp-proxy-v6.service "$wants/ly-route-dns-vpp-proxy-v6.service"
    ln -sf /etc/systemd/system/ly-route-dns-vpp-v6-namespace.service "$wants/ly-route-dns-vpp-v6-namespace.service"
    ln -sf /etc/systemd/system/ly-route-dns-vpp-session.service "$wants/ly-route-dns-vpp-session.service"
    [ ! -f "$target/lib/systemd/system/xray.service" ] || ln -sf /lib/systemd/system/xray.service "$wants/xray.service"
    ln -sf /etc/systemd/system/ly-route-dns-ipset-sync.timer "$timer_wants/ly-route-dns-ipset-sync.timer"
  fi
}

verify_gateway_vpp_plugins() {
  target=$1
  [ "$product" = gateway ] || return 0
  [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" != 1 ] || return 0
  case "$arch" in
    amd64) multiarch=x86_64-linux-gnu ;;
    arm64) multiarch=aarch64-linux-gnu ;;
    *) product_build_fail "unsupported gateway VPP plugin architecture: $arch" ;;
  esac
  for package in $LY_ROUTE_GATEWAY_CUSTOM_RUNTIME_PACKAGES; do
    grep -Eq "^Package: $package$" "$target/var/lib/dpkg/status" ||
      product_build_fail "gateway rootfs is missing installed VPP plugin package: $package"
  done
  for plugin in $LY_ROUTE_GATEWAY_VPP_PLUGINS; do
    [ -s "$target/usr/lib/$multiarch/vpp_plugins/$plugin" ] ||
      product_build_fail "gateway rootfs is missing VPP plugin: $plugin"
  done
  for runtime_file in $LY_ROUTE_GATEWAY_RUNTIME_FILES_COMMON; do
    [ -e "$target$runtime_file" ] ||
      product_build_fail "gateway rootfs is missing required runtime file: $runtime_file"
  done
  for unit in $LY_ROUTE_GATEWAY_RUNTIME_UNITS; do
    [ -e "$target/lib/systemd/system/$unit" ] || [ -e "$target/usr/lib/systemd/system/$unit" ] ||
      product_build_fail "gateway rootfs is missing required runtime unit: $unit"
  done
}

mkdir -p "$artifact_dir" "$work_parent"
cleanup_rootfs "$rootfs"
if [ "${LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY:-0}" = 1 ]; then
  mkdir -p "$rootfs"
elif command -v mmdebstrap >/dev/null 2>&1; then
  include='apt,systemd-sysv,dbus,nginx-light,openssh-server,sudo,openssl,ca-certificates,curl,gpgv,iproute2,iputils-ping,netbase,python3,python3-minimal,libunwind8,libnl-3-200,libnl-route-3-200,libpcap0.8,libnuma1,libelf1,zlib1g'
  if [ "$product" = gateway ]; then
    include="$include,kea-dhcp4-server,isc-dhcp-client,ipset"
  fi
  [ -z "$extra_packages" ] || include="$include,$extra_packages"
  mmdebstrap --architectures="$arch" --variant=minbase --components=main --include="$include" "$suite" "$rootfs" "$mirror"
else
  product_build_fail "mmdebstrap is required for a complete rootfs. Install it or set LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1 for scaffold validation."
fi

cp -a "$overlay/." "$rootfs/"
find "$rootfs/etc/systemd/system" -type f \
  \( -name '*.service' -o -name '*.socket' -o -name '*.timer' -o -name '*.target' -o -name '*.conf' \) \
  -exec chmod 0644 {} +
copy_payload "$rootfs"
build_control_binary "$rootfs"
build_pppoe_client_binary "$rootfs"
install_extra_debs "$rootfs"
# Runtime packages can replace the shared Ly Route library directory while
# being configured. Reassert the native PPPoE client after package install so
# the gateway image always contains the client required by its systemd unit.
if [ "$product" = gateway ] && [ ! -x "$rootfs/usr/lib/ly-route/ly-route-pppoe-client" ]; then
  build_pppoe_client_binary "$rootfs"
fi
if [ -n "$vpp_apply_binary" ]; then
  mkdir -p "$rootfs/usr/lib/ly-route"
  cp "$vpp_apply_binary" "$rootfs/usr/lib/ly-route/vpp-apply"
  chmod 0755 "$rootfs/usr/lib/ly-route/vpp-apply"
fi
configure_product "$rootfs"
enable_units "$rootfs"
verify_gateway_vpp_plugins "$rootfs"

mkdir -p "$rootfs/usr/lib/ly-route" "$rootfs/var/lib/ly-route/vpp"
if [ "$product" = gateway ]; then
  mkdir -p "$rootfs/var/lib/ly-route/policy-routing"
  cat >"$rootfs/usr/lib/ly-route/policy-routing-apply-default" <<'EOF'
#!/bin/sh
set -eu
: "${LY_ROUTE_POLICY_ROUTING_SCRIPT:=/var/lib/ly-route/policy-routing/apply.sh}"
if [ -x "$LY_ROUTE_POLICY_ROUTING_SCRIPT" ]; then
  # The rendered script owns its bounded VPP readiness check. Retrying it here
  # duplicated every TAP/route operation and could leave a canceled HTTP apply
  # holding a stale generation for several minutes. systemd retries this unit
  # after a failed boot-time handoff.
  exec "$LY_ROUTE_POLICY_ROUTING_SCRIPT"
fi
: "${LY_ROUTE_POLICY_ROUTING_RECEIPT:=/var/lib/ly-route/policy-routing-receipt.json}"
mkdir -p "$(dirname "$LY_ROUTE_POLICY_ROUTING_RECEIPT")"
printf '{"status":"no-policy-rendered","reason":"control plane has not rendered Linux policy routing operations yet"}\n' >"$LY_ROUTE_POLICY_ROUTING_RECEIPT"
EOF
  chmod 0755 "$rootfs/usr/lib/ly-route/policy-routing-apply-default"
fi
cat >"$rootfs/usr/lib/ly-route/vpp-apply-default" <<'EOF'
#!/bin/sh
set -eu
: "${LY_ROUTE_VPP_APPLY_COMMAND:=/usr/lib/ly-route/vpp-apply}"
: "${LY_ROUTE_VPP_OPERATIONS:=/var/lib/ly-route/vpp/operations.json}"
if [ ! -s "$LY_ROUTE_VPP_OPERATIONS" ]; then
  mkdir -p "$(dirname "$LY_ROUTE_VPP_OPERATIONS")"
  printf '{"operations":[]}\n' >"$LY_ROUTE_VPP_OPERATIONS"
fi
exec "$LY_ROUTE_VPP_APPLY_COMMAND" "$LY_ROUTE_VPP_OPERATIONS"
EOF
printf '{"operations":[]}\n' >"$rootfs/var/lib/ly-route/vpp/operations.json"
chmod 0755 "$rootfs/usr/lib/ly-route/firstboot.sh" "$rootfs/usr/lib/ly-route/tune-vpp.sh" \
  "$rootfs/usr/lib/ly-route/runtime-check.sh" "$rootfs/usr/lib/ly-route/recover-runtime.sh" \
  "$rootfs/usr/lib/ly-route/ly-route-control" "$rootfs/usr/lib/ly-route/vpp-apply-default" \
  "$rootfs/usr/lib/ly-route/dns-ipset-sync.py" "$rootfs/usr/lib/ly-route/active-dpdk-state.py" \
  "$rootfs/usr/lib/ly-route/prepare-vfio.sh"
if [ "$product" = gateway ]; then
  chmod 0755 "$rootfs/usr/lib/ly-route/ly-route-pppoe-client"
fi

printf 'ly-route\n' >"$rootfs/etc/hostname"
mkdir -p "$rootfs/etc/systemd/resolved.conf.d"
cat >"$rootfs/etc/systemd/resolved.conf.d/ly-route.conf" <<'EOF'
[Resolve]
DNSStubListener=yes
EOF

for text_root in "$rootfs/etc" "$rootfs/usr/lib/systemd" "$rootfs/usr/lib/ly-route"; do
  [ ! -d "$text_root" ] || find "$text_root" -type f -exec sh -c '
    for file do
      if LC_ALL=C grep -Iq . "$file"; then
        sed -i "s/\r$//" "$file"
      fi
    done
  ' sh {} +
done

write_product_artifact_manifest "$rootfs/usr/share/ly-route/artifact-manifest.json" rootfs \
  "$artifact_name" "$product" "$suite" "$arch" "$source_commit"
rm -f "$artifact_dir/$artifact_base.tar" "$artifact_dir/$artifact_base.tar.zst" \
  "$artifact_dir/$artifact_base.tar.gz" "$artifact_dir/$artifact_base.tar.sha256" \
  "$artifact_dir/$artifact_base.tar.zst.sha256" "$artifact_dir/$artifact_base.tar.gz.sha256"
create_deterministic_tar "$rootfs" "$tar_artifact" "$source_epoch"
if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" = 1 ]; then
  artifact=$tar_artifact
elif command -v zstd >/dev/null 2>&1; then
  zstd -19 -f "$tar_artifact" -o "$artifact"
  rm -f "$tar_artifact"
else
  gzip -n -f "$tar_artifact"
  artifact="$tar_artifact.gz"
fi
write_artifact_checksum "$artifact"
printf 'Built %s\n' "$artifact"
