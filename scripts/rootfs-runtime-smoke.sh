#!/usr/bin/env sh
set -eu

artifact=${1:-${LY_ROUTE_ROOTFS_ARTIFACT:-}}
if [ -z "$artifact" ]; then
  echo "usage: $0 /path/to/ly-route-rootfs-*.tar.{zst,gz}" >&2
  exit 2
fi
if [ ! -f "$artifact" ]; then
  echo "rootfs artifact not found: $artifact" >&2
  exit 2
fi

if [ "${LY_ROUTE_ROOTFS_REQUIRED_PACKAGES+x}" != x ]; then
  LY_ROUTE_ROOTFS_REQUIRED_PACKAGES="libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply ly-route-vpp-smart-qos ly-route-vpp-security-guard smartdns ly-route-dns-vpp-proxy xray openssh-server sudo ipset"
fi
required_files_defaulted=false
if [ "${LY_ROUTE_ROOTFS_REQUIRED_FILES+x}" != x ]; then
  required_files_defaulted=true
  LY_ROUTE_ROOTFS_REQUIRED_FILES="/usr/bin/vpp /usr/bin/vppctl /usr/bin/sudo /usr/sbin/ipset /usr/lib/systemd/system/vpp.service /usr/lib/x86_64-linux-gnu/vpp_plugins/abf_plugin.so /usr/lib/ly-route/ly-route-control /usr/lib/ly-route/vpp-apply /usr/lib/ly-route/vpp-apply-default /usr/lib/ly-route/policy-routing-apply-default /usr/lib/ly-route/tune-vpp.sh /usr/lib/ly-route/dns-ipset-sync.py /usr/lib/ly-route/active-dpdk-state.py /usr/sbin/smartdns /usr/bin/xray /usr/sbin/sshd /etc/ssh/sshd_config /etc/vpp/startup.conf /etc/smartdns/smartdns.conf /etc/xray/config.json /etc/ly-route/default-config.json /etc/ly-route/runtime.env /etc/ly-route/vpp-command-map.json /var/lib/ly-route/vpp/operations.json /etc/kea/kea-dhcp4.conf /etc/nginx/conf.d/ly-route-admin.conf /opt/ly-route/admin/index.html /opt/ly-route/admin/app.js /opt/ly-route/admin/shell.js /opt/ly-route/admin/styles.css /opt/ly-route/admin/capabilities.json /etc/systemd/network/10-ethernet-dhcp.network /etc/systemd/system/multi-user.target.wants/vpp.service /etc/systemd/system/multi-user.target.wants/ssh.service /etc/systemd/system/multi-user.target.wants/nginx.service /etc/systemd/system/multi-user.target.wants/ly-route-control-api.service /etc/systemd/system/multi-user.target.wants/kea-dhcp4-server.service /etc/systemd/system/multi-user.target.wants/ly-route-vpp-tune.service /etc/systemd/system/timers.target.wants/ly-route-dns-ipset-sync.timer /etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf /etc/systemd/system/ly-route-control-api.service /etc/systemd/system/ly-route-runtime-check.service /etc/systemd/system/ly-route-vpp-apply.service /etc/systemd/system/ly-route-vpp-tune.service /etc/systemd/system/ly-route-dns-ipset-sync.service /etc/systemd/system/ly-route-dns-ipset-sync.timer /etc/systemd/system/vpp.service.d/10-ly-route-tuning.conf /etc/systemd/system/ly-route-policy-routing.service /etc/systemd/system/ly-route-recovery.service"
fi
: "${LY_ROUTE_ROOTFS_LIVE_REQUIRED:=false}"

if [ "$required_files_defaulted" = true ]; then
  LY_ROUTE_ROOTFS_REQUIRED_FILES="$LY_ROUTE_ROOTFS_REQUIRED_FILES /usr/lib/x86_64-linux-gnu/vpp_plugins/dpdk_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_security_guard_plugin.so"
  LY_ROUTE_ROOTFS_REQUIRED_FILES="$LY_ROUTE_ROOTFS_REQUIRED_FILES /usr/lib/x86_64-linux-gnu/vpp_plugins/linux_cp_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/linux_nl_plugin.so"
  LY_ROUTE_ROOTFS_REQUIRED_FILES="$LY_ROUTE_ROOTFS_REQUIRED_FILES /usr/lib/ly-route/ly-route-dns-vpp-proxy /usr/lib/ly-route/ly-route-dns-vpp-proxy-v6 /usr/lib/ly-route/dns-vpp-v6-namespace-apply /usr/lib/ly-route/dns-vpp-session-apply /etc/ly-route/vcl.conf /etc/ly-route/vcl-v6.conf /etc/systemd/system/ly-route-dns-vpp-v6-namespace.service /etc/systemd/system/ly-route-dns-vpp-session.service /lib/systemd/system/ly-route-dns-vpp-proxy.service /lib/systemd/system/ly-route-dns-vpp-proxy-v6.service /etc/systemd/system/multi-user.target.wants/ly-route-dns-vpp-v6-namespace.service /etc/systemd/system/multi-user.target.wants/ly-route-dns-vpp-session.service /etc/systemd/system/multi-user.target.wants/ly-route-dns-vpp-proxy.service /etc/systemd/system/multi-user.target.wants/ly-route-dns-vpp-proxy-v6.service"
fi

case "$artifact" in
  *.tar.zst) compressor=unzstd ;;
  *.tar.gz|*.tgz) compressor='gzip -d' ;;
  *) echo "unsupported rootfs compression: $artifact" >&2; exit 2 ;;
esac

if [ -f "$artifact.sha256" ]; then
  artifact_dir=$(dirname "$artifact")
  artifact_name=$(basename "$artifact")
  (cd "$artifact_dir" && sha256sum -c "$artifact_name.sha256")
fi

if tar --use-compress-program="$compressor" -tf "$artifact" | grep -Eiq 'af_packet|no-zero-copy|native-driver-auto|generic[-_]?xdp|generic[-_]?skb'; then
  echo "rootfs artifact contains a forbidden dataplane path" >&2
  exit 1
fi

tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

extract_required_file() {
  file=$1
  archive_path=".${file}"
  if tar --use-compress-program="$compressor" -xf "$artifact" -C "$tmp" "$archive_path" 2>/dev/null; then
    return 0
  fi
  # Debian bookworm uses usrmerge, so /lib is represented as /usr/lib in tar archives.
  case "$file" in
    /lib/*)
      tar --use-compress-program="$compressor" -xf "$artifact" -C "$tmp" "./usr${file}"
      ;;
    *)
      echo "missing rootfs archive entry: $file" >&2
      return 1
      ;;
  esac
}

if [ -n "$LY_ROUTE_ROOTFS_REQUIRED_PACKAGES" ]; then
  tar --use-compress-program="$compressor" -xf "$artifact" -C "$tmp" ./var/lib/dpkg/status
fi
for file in $LY_ROUTE_ROOTFS_REQUIRED_FILES; do
  extract_required_file "$file"
  check_path="$tmp$file"
  if [ ! -e "$check_path" ] && [ ! -L "$check_path" ]; then
    case "$file" in
      /lib/*) check_path="$tmp/usr${file}" ;;
    esac
  fi
  if [ ! -e "$check_path" ] && [ ! -L "$check_path" ]; then
    echo "missing rootfs file: $file" >&2
    exit 1
  fi
done

for package in $LY_ROUTE_ROOTFS_REQUIRED_PACKAGES; do
  if ! grep -Eq "^Package: ${package}$" "$tmp/var/lib/dpkg/status"; then
    echo "missing installed package in rootfs: $package" >&2
    exit 1
  fi
done

if printf '%s' " $LY_ROUTE_ROOTFS_REQUIRED_FILES " | grep -q ' /usr/lib/ly-route/vpp-apply '; then
  test -x "$tmp/usr/lib/ly-route/vpp-apply"
fi
if [ -f "$tmp/etc/ly-route/runtime.env" ]; then
  grep -q 'LY_ROUTE_ENABLE_SERVICE_RUNTIME=true' "$tmp/etc/ly-route/runtime.env"
fi
if [ -f "$tmp/etc/ly-route/vpp-command-map.json" ]; then
  grep -q '"operations"' "$tmp/etc/ly-route/vpp-command-map.json"
fi
if grep -Eiq 'dpdk|af_packet|no-zero-copy|native-driver-auto|generic[-_]?xdp|generic[-_]?skb' \
  "$tmp/etc/ly-route/runtime.env" \
  "$tmp/etc/ly-route/default-config.json" \
  "$tmp/etc/ly-route/vpp-command-map.json"; then
  echo "rootfs runtime configuration contains a forbidden dataplane path" >&2
  exit 1
fi
if [ -f "$tmp/etc/systemd/system/ly-route-control-api.service" ]; then
  grep -q '/usr/lib/ly-route/ly-route-control' "$tmp/etc/systemd/system/ly-route-control-api.service"
  grep -q 'After=network.target ly-route-firstboot.service ly-route-runtime-check.service' "$tmp/etc/systemd/system/ly-route-control-api.service"
fi
if [ -f "$tmp/etc/systemd/system/ly-route-policy-routing.service" ]; then
  grep -q '/usr/lib/ly-route/policy-routing-apply-default' "$tmp/usr/lib/ly-route/policy-routing-apply-default"
fi
if [ -f "$tmp/etc/systemd/system/ly-route-vpp-apply.service" ]; then
  grep -q '/usr/lib/ly-route/vpp-apply-default' "$tmp/etc/systemd/system/ly-route-vpp-apply.service"
fi
if [ -f "$tmp/etc/nginx/conf.d/ly-route-admin.conf" ]; then
  grep -q 'root /opt/ly-route/admin' "$tmp/etc/nginx/conf.d/ly-route-admin.conf"
  grep -q 'listen 443 ssl default_server' "$tmp/etc/nginx/conf.d/ly-route-admin.conf"
  grep -q 'ssl_certificate /etc/ly-route/tls/admin.crt' "$tmp/etc/nginx/conf.d/ly-route-admin.conf"
  grep -q 'proxy_pass http://127.0.0.1:8080/api/v1/' "$tmp/etc/nginx/conf.d/ly-route-admin.conf"
fi
if [ -f "$tmp/opt/ly-route/admin/index.html" ]; then
  grep -q 'Ly Route' "$tmp/opt/ly-route/admin/index.html"
  if grep -q 'mock-api.js' "$tmp/opt/ly-route/admin/index.html"; then
    echo "production admin UI must not load mock-api.js" >&2
    exit 1
  fi
fi
if [ -f "$tmp/usr/lib/ly-route/firstboot.sh" ]; then
  grep -q 'openssl req -x509' "$tmp/usr/lib/ly-route/firstboot.sh"
  grep -q '/etc/ly-route/tls' "$tmp/usr/lib/ly-route/firstboot.sh"
fi
if [ -f "$tmp/etc/ly-route/default-config.json" ]; then
  grep -q '"active_path": "dataplane_locked"' "$tmp/etc/ly-route/default-config.json"
  grep -q '"interfaces": \[\]' "$tmp/etc/ly-route/default-config.json"
  if grep -q '"gateway_role": "wan"' "$tmp/etc/ly-route/default-config.json"; then
    echo "rootfs default config must not preconfigure WAN" >&2
    exit 1
  fi
fi
if [ -f "$tmp/etc/kea/kea-dhcp4.conf" ]; then
  grep -q '"interfaces": \["eth0"\]' "$tmp/etc/kea/kea-dhcp4.conf"
  grep -q '192.168.88.100 - 192.168.88.199' "$tmp/etc/kea/kea-dhcp4.conf"
fi
if [ -f "$tmp/etc/systemd/network/10-ethernet-dhcp.network" ]; then
  grep -q 'DHCP=no' "$tmp/etc/systemd/network/10-ethernet-dhcp.network"
fi
if [ -f "$tmp/etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf" ]; then
  grep -q 'After=ly-route-firstboot.service' "$tmp/etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf"
fi

if command -v systemd-nspawn >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
  printf 'systemd-nspawn and curl available; live boot smoke can be run by the hardware/VM validation plan.\n'
elif [ "$LY_ROUTE_ROOTFS_LIVE_REQUIRED" = "true" ]; then
  echo "systemd-nspawn and curl are required for live rootfs boot smoke" >&2
  exit 1
else
  printf 'systemd-nspawn unavailable; completed static rootfs runtime smoke only.\n'
fi

printf 'rootfs runtime smoke passed: %s\n' "$artifact"
