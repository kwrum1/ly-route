#!/bin/sh
set -eu

: "${LY_ROUTE_VPP_TUNING:=auto}"
: "${LY_ROUTE_VPP_MAX_WORKERS:=8}"
: "${LY_ROUTE_VPP_MAX_HUGEPAGES_MB:=4096}"
: "${LY_ROUTE_ROOT:=/}"
: "${LY_ROUTE_PLATFORM_ARCH:=auto}"
: "${LY_ROUTE_VPP_CPU_COUNT:=}"
: "${LY_ROUTE_VPP_MEMORY_MB:=}"

root_path() {
  case "$LY_ROUTE_ROOT" in
    /) printf '%s\n' "$1" ;;
    *) printf '%s%s\n' "$LY_ROUTE_ROOT" "$1" ;;
  esac
}

state_dir=$(root_path /var/lib/ly-route)
mkdir -p "$state_dir" "$(root_path /etc/ly-route)" "$(root_path /etc/vpp)" "$(root_path /etc/sysctl.d)" "$(root_path /etc/systemd/system/vpp.service.d)" "$(root_path /dev/hugepages)"

if [ "$LY_ROUTE_VPP_TUNING" = "off" ]; then
  printf 'LY_ROUTE_VPP_TUNING=off\n' > "$state_dir/vpp-tuning.env"
  exit 0
fi

cpu_count=$LY_ROUTE_VPP_CPU_COUNT
if [ -z "$cpu_count" ]; then
  cpu_count=$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 1)
fi
case "$cpu_count" in *[!0-9]*|''|0) echo "invalid VPP CPU count: $cpu_count" >&2; exit 1 ;; esac
mem_mb=$LY_ROUTE_VPP_MEMORY_MB
if [ -z "$mem_mb" ]; then
  mem_kb=$(awk '/^MemTotal:/ { print $2; exit }' /proc/meminfo 2>/dev/null || echo 1048576)
  case "$mem_kb" in *[!0-9]*|'') mem_kb=1048576 ;; esac
  mem_mb=$((mem_kb / 1024))
fi
case "$mem_mb" in *[!0-9]*|''|0) echo "invalid VPP memory size: $mem_mb" >&2; exit 1 ;; esac
machine_arch="$LY_ROUTE_PLATFORM_ARCH"
if [ "$machine_arch" = auto ] || [ -z "$machine_arch" ]; then
  machine_arch=$(uname -m 2>/dev/null || echo unknown)
fi
case "$machine_arch" in
  x86_64|amd64) platform_profile=x86 ;;
  aarch64|arm64|armv7l|armv8l) platform_profile=arm ;;
  *) platform_profile=generic ;;
esac

control_cpus=shared
if [ "$cpu_count" -le 1 ]; then
  workers=0
elif [ "$cpu_count" -ge 4 ]; then
  # Keep CPU0 available for the Linux management plane and device interrupts.
  workers=$((cpu_count - 2))
  control_cpus=0
else
  workers=$((cpu_count - 1))
fi
if [ "$workers" -gt "$LY_ROUTE_VPP_MAX_WORKERS" ]; then
  workers=$LY_ROUTE_VPP_MAX_WORKERS
fi

case "$platform_profile" in
  arm)
    if [ "$workers" -gt 4 ]; then workers=4; fi
    max_huge_mb=$((mem_mb / 5))
    if [ "$max_huge_mb" -lt 128 ]; then max_huge_mb=128; fi
    buffers_floor=4096
    swappiness=10
    dirty_ratio=10
    dirty_background_ratio=3
    netdev_backlog=65536
    somaxconn=8192
    ;;
  x86)
    max_huge_mb=$((mem_mb / 3))
    if [ "$max_huge_mb" -lt 512 ]; then max_huge_mb=512; fi
    buffers_floor=8192
    swappiness=1
    dirty_ratio=20
    dirty_background_ratio=5
    netdev_backlog=250000
    somaxconn=65535
    ;;
  *)
    max_huge_mb=$((mem_mb / 4))
    if [ "$max_huge_mb" -lt 128 ]; then max_huge_mb=128; fi
    buffers_floor=4096
    swappiness=5
    dirty_ratio=15
    dirty_background_ratio=5
    netdev_backlog=131072
    somaxconn=16384
    ;;
esac

if [ "$mem_mb" -lt 2048 ]; then
  huge_mb=128
  buffers_per_numa=4096
  if [ "$workers" -gt 1 ]; then workers=1; fi
elif [ "$mem_mb" -lt 4096 ]; then
  huge_mb=512
  buffers_per_numa=8192
  if [ "$workers" -gt 1 ]; then workers=1; fi
elif [ "$mem_mb" -lt 8192 ]; then
  huge_mb=1024
  buffers_per_numa=16384
  if [ "$workers" -gt 2 ]; then workers=2; fi
else
  huge_mb=$((mem_mb / 4))
  buffers_per_numa=32768
fi
if [ "$huge_mb" -gt "$max_huge_mb" ]; then
  huge_mb=$max_huge_mb
fi
if [ "$huge_mb" -gt "$LY_ROUTE_VPP_MAX_HUGEPAGES_MB" ]; then
  huge_mb=$LY_ROUTE_VPP_MAX_HUGEPAGES_MB
fi
if [ "$huge_mb" -lt 128 ]; then
  huge_mb=128
fi
if [ "$buffers_per_numa" -lt "$buffers_floor" ]; then
  buffers_per_numa=$buffers_floor
fi
hugepages=$((huge_mb / 2))
if [ "$hugepages" -lt 64 ]; then
  hugepages=64
fi

main_core=0
worker_start=1
if [ "$control_cpus" = 0 ]; then
  main_core=1
  worker_start=2
fi
worker_end=$((worker_start + workers - 1))
affinity="$main_core"
if [ "$workers" -gt 0 ]; then
  affinity="$main_core-$worker_end"
fi

cat > "$(root_path /etc/sysctl.d/90-ly-route-vpp.conf)" <<EOF
vm.nr_hugepages = $hugepages
vm.max_map_count = 262144
vm.swappiness = $swappiness
vm.zone_reclaim_mode = 0
vm.dirty_ratio = $dirty_ratio
vm.dirty_background_ratio = $dirty_background_ratio
kernel.numa_balancing = 0
fs.file-max = 2097152
net.core.rmem_max = 268435456
net.core.wmem_max = 268435456
net.core.netdev_max_backlog = $netdev_backlog
net.core.netdev_budget = 600
net.core.netdev_budget_usecs = 8000
net.core.somaxconn = $somaxconn
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
net.ipv4.conf.all.rp_filter = 0
net.ipv4.conf.default.rp_filter = 0
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.secure_redirects = 0
net.ipv4.conf.default.secure_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.accept_source_route = 0
net.ipv4.conf.default.accept_source_route = 0
net.ipv6.conf.all.accept_redirects = 0
net.ipv6.conf.default.accept_redirects = 0
net.ipv6.conf.all.accept_source_route = 0
net.ipv6.conf.default.accept_source_route = 0
net.ipv4.tcp_syncookies = 1
EOF
if [ "$LY_ROUTE_ROOT" = / ]; then
  sysctl -p /etc/sysctl.d/90-ly-route-vpp.conf >/dev/null 2>&1 || true
  printf '%s\n' "$hugepages" > /proc/sys/vm/nr_hugepages 2>/dev/null || true
  if ! mountpoint -q /dev/hugepages; then
    mount -t hugetlbfs nodev /dev/hugepages 2>/dev/null || true
  fi
fi

cat > "$(root_path /etc/systemd/system/vpp.service.d/10-ly-route-affinity.conf)" <<EOF
[Service]
CPUAffinity=$affinity
LimitMEMLOCK=infinity
LimitNOFILE=1048576
TasksMax=infinity
OOMScoreAdjust=-900
Nice=-10
EOF

cat > "$(root_path /etc/ly-route/platform-tuning.env)" <<EOF
LY_ROUTE_PLATFORM_ARCH=$machine_arch
LY_ROUTE_PLATFORM_PROFILE=$platform_profile
LY_ROUTE_SWAPPINESS=$swappiness
LY_ROUTE_DIRTY_RATIO=$dirty_ratio
LY_ROUTE_DIRTY_BACKGROUND_RATIO=$dirty_background_ratio
LY_ROUTE_NETDEV_BACKLOG=$netdev_backlog
LY_ROUTE_SOMAXCONN=$somaxconn
EOF

{
  cat <<'EOF'
unix {
  nodaemon
  log /var/log/vpp/vpp.log
  cli-listen /run/vpp/cli.sock
  runtime-dir /run/vpp
  gid vpp
}

api-segment {
  gid vpp
}

socksvr {
  default
}

session {
  enable rt-backend rule-table
  use-app-socket-api
}

EOF
  printf 'cpu {\n'
  printf '  main-core %s\n' "$main_core"
  if [ "$workers" -gt 0 ]; then
    printf '  corelist-workers %s-%s\n' "$worker_start" "$worker_end"
  else
    printf '  workers 0\n'
  fi
  printf '}\n\n'
  printf 'buffers {\n  buffers-per-numa %s\n}\n\n' "$buffers_per_numa"
  cat <<'EOF'
plugins {
  # DPDK is enabled by the ownership preflight only when it is the selected
  # fallback.  Loading it by default makes VPP probe every PCI device.
  plugin dpdk_plugin.so { disable }
  plugin linux_cp_plugin.so { enable }
  plugin linux_nl_plugin.so { enable }
}
EOF
} > "$(root_path /etc/vpp/startup.conf)"

cat > "$state_dir/vpp-tuning.env" <<EOF
LY_ROUTE_VPP_TUNING=auto
LY_ROUTE_PLATFORM_ARCH=$machine_arch
LY_ROUTE_PLATFORM_PROFILE=$platform_profile
LY_ROUTE_VPP_CPU_COUNT=$cpu_count
LY_ROUTE_LINUX_CONTROL_CPUS=$control_cpus
LY_ROUTE_VPP_MAIN_CORE=$main_core
LY_ROUTE_VPP_WORKERS=$workers
LY_ROUTE_VPP_CPU_AFFINITY=$affinity
LY_ROUTE_VPP_HUGEPAGES=$hugepages
LY_ROUTE_VPP_HUGEPAGES_MB=$huge_mb
LY_ROUTE_VPP_BUFFERS_PER_NUMA=$buffers_per_numa
EOF

if [ "$LY_ROUTE_ROOT" = / ]; then
  if [ "$platform_profile" = arm ]; then
    for governor in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
      [ -w "$governor" ] && printf '%s\n' schedutil > "$governor" 2>/dev/null || true
    done
  elif [ "$platform_profile" = x86 ]; then
    for governor in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
      [ -w "$governor" ] && printf '%s\n' performance > "$governor" 2>/dev/null || true
    done
  fi
  # VPP/DPDK/AF_XDP owns dataplane queue placement. Linux RPS/XPS would add a
  # second cross-CPU handoff and can reorder flows before they reach VPP.
  for queue in /sys/class/net/*/queues/rx-*/rps_cpus /sys/class/net/*/queues/tx-*/xps_cpus; do
    [ -w "$queue" ] && printf '%s\n' 0 > "$queue" 2>/dev/null || true
  done
  for queue in /sys/class/net/*/queues/rx-*/rps_flow_cnt; do
    [ -w "$queue" ] && printf '%s\n' 0 > "$queue" 2>/dev/null || true
  done
  systemctl daemon-reload 2>/dev/null || true
fi
