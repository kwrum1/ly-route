#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

run_tuning() {
  scenario=$1
  cpus=$2
  memory_mb=$3
  root="$tmp/$scenario"
  LY_ROUTE_ROOT="$root" \
    LY_ROUTE_PLATFORM_ARCH=x86_64 \
    LY_ROUTE_VPP_CPU_COUNT="$cpus" \
    LY_ROUTE_VPP_MEMORY_MB="$memory_mb" \
    LY_ROUTE_VPP_MAX_WORKERS=8 \
    "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh"
}

run_tuning two-core 2 4096
grep -q '^LY_ROUTE_VPP_WORKERS=1$' "$tmp/two-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^CPUAffinity=0-1$' "$tmp/two-core/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"

run_tuning three-core 3 4096
grep -q '^LY_ROUTE_VPP_WORKERS=2$' "$tmp/three-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^CPUAffinity=0-2$' "$tmp/three-core/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"

run_tuning low-memory 8 1024
grep -q '^LY_ROUTE_VPP_WORKERS=1$' "$tmp/low-memory/var/lib/ly-route/vpp-tuning.env"
grep -q '^CPUAffinity=1-2$' "$tmp/low-memory/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"

run_tuning eight-core 8 16384
grep -q '^LY_ROUTE_LINUX_CONTROL_CPUS=0$' "$tmp/eight-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^LY_ROUTE_VPP_MAIN_CORE=1$' "$tmp/eight-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^LY_ROUTE_VPP_WORKERS=6$' "$tmp/eight-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^CPUAffinity=1-7$' "$tmp/eight-core/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"
grep -q '^  corelist-workers 2-7$' "$tmp/eight-core/etc/vpp/startup.conf"
grep -q '^  runtime-dir /run/vpp$' "$tmp/eight-core/etc/vpp/startup.conf"
grep -q '^  use-app-socket-api$' "$tmp/eight-core/etc/vpp/startup.conf"

run_tuning sixteen-core 16 32768
grep -q '^LY_ROUTE_VPP_WORKERS=8$' "$tmp/sixteen-core/var/lib/ly-route/vpp-tuning.env"
grep -q '^CPUAffinity=1-9$' "$tmp/sixteen-core/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"
grep -q '^  corelist-workers 2-9$' "$tmp/sixteen-core/etc/vpp/startup.conf"

for lcp_plugin in \
  '  plugin linux_cp_plugin.so { enable }' \
  '  plugin linux_nl_plugin.so { enable }'; do
  if ! grep -Fq "$lcp_plugin" "$tmp/eight-core/etc/vpp/startup.conf"; then
    echo "generated VPP tuning config is missing Linux control-plane plugin: $lcp_plugin" >&2
    exit 1
  fi
done

for key in \
  'net.ipv4.ip_forward = 1' \
  'net.ipv6.conf.all.forwarding = 1' \
  'net.ipv4.conf.all.rp_filter = 0' \
  'net.ipv4.conf.all.send_redirects = 0' \
  'net.core.netdev_budget = 600'; do
  grep -q "^$key$" "$tmp/eight-core/etc/sysctl.d/90-ly-route-vpp.conf"
done
grep -q '^OOMScoreAdjust=-900$' "$tmp/eight-core/etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf"
if grep -q '^api-trace' "$tmp/eight-core/etc/vpp/startup.conf"; then
  echo "production VPP configuration enabled API tracing" >&2
  exit 1
fi

if LY_ROUTE_ROOT="$tmp/invalid" LY_ROUTE_VPP_CPU_COUNT=all LY_ROUTE_VPP_MEMORY_MB=4096 \
  "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh" >/dev/null 2>&1; then
  echo "invalid CPU override was accepted" >&2
  exit 1
fi

printf 'VPP tuning CPU allocation scenarios passed\n'
