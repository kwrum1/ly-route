#!/bin/sh
set -eu

command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
vpp_container="ly-route-vpp-wan-$suffix"
client_ns="ly-route-wan-client-$suffix"
up0_ns="ly-route-wan-up0-$suffix"
up1_ns="ly-route-wan-up1-$suffix"
cli_socket=/run/vpp/ly-route-wan-$suffix.sock

cleanup() {
  docker rm -f "$vpp_container" >/dev/null 2>&1 || true
  ip netns del "$client_ns" >/dev/null 2>&1 || true
  ip netns del "$up0_ns" >/dev/null 2>&1 || true
  ip netns del "$up1_ns" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

docker run -d --name "$vpp_container" --privileged --network none --shm-size 256m \
  ly-route/vpp-test:25.10 \
  /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
  plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$vpp_container")
vppctl() { docker exec "$vpp_container" vppctl -s "$cli_socket" "$@"; }

vpp_ready=false
for _ in $(seq 1 60); do
  if vppctl show version >/dev/null 2>&1; then
    vpp_ready=true
    break
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$vpp_container")" != true ]; then
    break
  fi
  sleep 0.1
done
if [ "$vpp_ready" != true ]; then
  echo "VPP WAN-group fixture did not become ready" >&2
  docker logs "$vpp_container" >&2 || true
  exit 1
fi

ip netns add "$client_ns"
ip netns add "$up0_ns"
ip netns add "$up1_ns"
ip link add "wc${suffix}p" type veth peer name "wc${suffix}v"
ip link set "wc${suffix}p" netns "$client_ns"
ip link set "wc${suffix}v" netns "$vpp_pid"
ip link add "wu0${suffix}p" type veth peer name "wu0${suffix}v"
ip link set "wu0${suffix}p" netns "$up0_ns"
ip link set "wu0${suffix}v" netns "$vpp_pid"
ip link add "wu1${suffix}p" type veth peer name "wu1${suffix}v"
ip link set "wu1${suffix}p" netns "$up1_ns"
ip link set "wu1${suffix}v" netns "$vpp_pid"

ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set "wc${suffix}p" up
ip netns exec "$client_ns" ip addr add 192.0.2.2/24 dev "wc${suffix}p"
ip netns exec "$client_ns" ip route add 10.0.0.1/32 via 192.0.2.1

for ns in "$up0_ns" "$up1_ns"; do
  ip netns exec "$ns" ip link set lo up
done
ip netns exec "$up0_ns" ip link set "wu0${suffix}p" up
ip netns exec "$up0_ns" ip addr add 198.51.100.1/30 dev "wu0${suffix}p"
ip netns exec "$up0_ns" ip addr add 10.0.0.1/32 dev lo
ip netns exec "$up0_ns" ip route add 192.0.2.0/24 via 198.51.100.2
ip netns exec "$up1_ns" ip link set "wu1${suffix}p" up
ip netns exec "$up1_ns" ip addr add 203.0.113.1/30 dev "wu1${suffix}p"
ip netns exec "$up1_ns" ip addr add 10.0.0.1/32 dev lo
ip netns exec "$up1_ns" ip route add 192.0.2.0/24 via 203.0.113.2

nsenter -t "$vpp_pid" -n ip link set "wc${suffix}v" up
nsenter -t "$vpp_pid" -n ip link set "wu0${suffix}v" up
nsenter -t "$vpp_pid" -n ip link set "wu1${suffix}v" up
vppctl create host-interface name "wc${suffix}v" >/dev/null
vppctl create host-interface name "wu0${suffix}v" >/dev/null
vppctl create host-interface name "wu1${suffix}v" >/dev/null
vppctl set interface state "host-wc${suffix}v" up
vppctl set interface state "host-wu0${suffix}v" up
vppctl set interface state "host-wu1${suffix}v" up
vppctl set interface ip address "host-wc${suffix}v" 192.0.2.1/24
vppctl set interface ip address "host-wu0${suffix}v" 198.51.100.2/30
vppctl set interface ip address "host-wu1${suffix}v" 203.0.113.2/30

vppctl ip table add 901
vppctl set ip flow-hash table 901 src dst sport dport proto
vppctl ip route add table 901 10.0.0.1/32 via 198.51.100.1 host-wu0${suffix}v weight 1 preference 0
vppctl ip route add table 901 10.0.0.1/32 via 203.0.113.1 host-wu1${suffix}v weight 1 preference 1
vppctl ip route add 10.0.0.1/32 via ip4-lookup-in-table 901
vppctl show ip fib table 901 | grep -E 'via (198\.51\.100\.1|203\.0\.113\.1)' >/dev/null

primary_before=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
backup_before=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
ip netns exec "$client_ns" ping -c 3 -W 2 10.0.0.1 >/dev/null
primary_after=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
if [ "$primary_after" -le "$primary_before" ]; then
  echo "WAN primary-backup did not use the primary path while healthy" >&2
  exit 1
fi
backup_before=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
ip netns exec "$up0_ns" ip link set "wu0${suffix}p" down
vppctl set interface state "host-wu0${suffix}v" down
sleep 1
if ! ip netns exec "$client_ns" ping -c 3 -W 2 10.0.0.1 >/dev/null; then
  vppctl show interface
  vppctl show ip fib table 901
  exit 1
fi
backup_after=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
if [ "$backup_after" -le "$backup_before" ]; then
  echo "WAN primary-backup did not move packets to the backup path" >&2
  exit 1
fi

ip netns exec "$up0_ns" ip link set "wu0${suffix}p" up
ip netns exec "$up0_ns" ip route replace 192.0.2.0/24 via 198.51.100.2
vppctl set interface state "host-wu0${suffix}v" up
primary_before=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
rejoined=false
for _ in 1 2 3 4 5; do
  if ip netns exec "$client_ns" ping -c 1 -W 1 10.0.0.1 >/dev/null; then
    rejoined=true
    break
  fi
  sleep 1
done
if [ "$rejoined" != true ]; then
  echo "WAN primary-backup did not restore packet flow inside the recovery window" >&2
  ip netns exec "$up0_ns" ip address show dev "wu0${suffix}p"
  ip netns exec "$up0_ns" ip route show
  ip netns exec "$up0_ns" ip neighbor show
  ip netns exec "$up0_ns" ping -c 1 -W 1 198.51.100.2 || true
  vppctl show interface
  vppctl show ip fib table 901
  vppctl show ip neighbors
  exit 1
fi
primary_after=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
if [ "$primary_after" -le "$primary_before" ]; then
  echo "WAN primary-backup did not automatically rejoin the recovered primary path" >&2
  exit 1
fi

send_udp_flows() {
  first_port=$1
  flow_count=$2
  ip netns exec "$client_ns" python3 - "$first_port" "$flow_count" <<'PY'
import socket
import sys

first_port = int(sys.argv[1])
flow_count = int(sys.argv[2])
for source_port in range(first_port, first_port + flow_count):
    sender = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sender.bind(("192.0.2.2", source_port))
    sender.sendto(b"ly-route-wan-flow", ("10.0.0.1", 9000))
    sender.close()
PY
}

vppctl ip route del table 901 10.0.0.1/32 via 198.51.100.1 "host-wu0${suffix}v" weight 1 preference 0
vppctl ip route del table 901 10.0.0.1/32 via 203.0.113.1 "host-wu1${suffix}v" weight 1 preference 1
vppctl ip route add table 901 10.0.0.1/32 via 198.51.100.1 "host-wu0${suffix}v" weight 3 preference 0
vppctl ip route add table 901 10.0.0.1/32 via 203.0.113.1 "host-wu1${suffix}v" weight 1 preference 0
primary_before=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
backup_before=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
send_udp_flows 20000 256
sleep 1
primary_after=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
backup_after=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
weighted_primary=$((primary_after - primary_before))
weighted_backup=$((backup_after - backup_before))
if [ "$weighted_primary" -le "$weighted_backup" ] || [ "$weighted_backup" -le 0 ]; then
  echo "WAN weighted mode distribution is invalid: primary=$weighted_primary backup=$weighted_backup" >&2
  exit 1
fi

vppctl ip route del table 901 10.0.0.1/32 via 198.51.100.1 "host-wu0${suffix}v" weight 3 preference 0
vppctl ip route del table 901 10.0.0.1/32 via 203.0.113.1 "host-wu1${suffix}v" weight 1 preference 0
vppctl ip route add table 901 10.0.0.1/32 via 198.51.100.1 "host-wu0${suffix}v" weight 1 preference 0
vppctl ip route add table 901 10.0.0.1/32 via 203.0.113.1 "host-wu1${suffix}v" weight 1 preference 0
primary_before=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
backup_before=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
send_udp_flows 21000 256
sleep 1
primary_after=$(ip netns exec "$up0_ns" cat "/sys/class/net/wu0${suffix}p/statistics/rx_packets")
backup_after=$(ip netns exec "$up1_ns" cat "/sys/class/net/wu1${suffix}p/statistics/rx_packets")
five_tuple_primary=$((primary_after - primary_before))
five_tuple_backup=$((backup_after - backup_before))
if [ "$five_tuple_primary" -le 0 ] || [ "$five_tuple_backup" -le 0 ]; then
  echo "WAN five-tuple mode did not use both paths: primary=$five_tuple_primary backup=$five_tuple_backup" >&2
  exit 1
fi

echo "VPP WAN primary-backup, weighted, five-tuple packet flow, failover, and automatic rejoin verification passed"
