#!/bin/sh
set -eu

command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null
command -v python3 >/dev/null

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="ly-route-vpp-nat-$suffix"
server_ns="ly-route-nat-server-$suffix"
client_ns="ly-route-nat-client-$suffix"
wan_ns="ly-route-nat-wan-$suffix"
bridge="nb${suffix}"
cli_socket="/run/vpp/ly-route-nat-$suffix.sock"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  ip netns del "$server_ns" >/dev/null 2>&1 || true
  ip netns del "$client_ns" >/dev/null 2>&1 || true
  ip netns del "$wan_ns" >/dev/null 2>&1 || true
  ip link del "$bridge" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

docker run -d --name "$container" --privileged --network none --shm-size 256m \
  ly-route/vpp-test:25.10 \
  /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
  plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$cli_socket" "$@"; }

vpp_ready=false
attempt=0
while [ "$attempt" -lt 100 ]; do
  if vppctl show version >/dev/null 2>&1; then
    vpp_ready=true
    break
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true)" != true ]; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.1
done
if [ "$vpp_ready" != true ]; then
  echo "VPP CLI socket did not become ready: $cli_socket" >&2
  docker logs "$container" >&2 || true
  exit 1
fi

ip netns add "$server_ns"
ip netns add "$client_ns"
ip netns add "$wan_ns"
ip link add "$bridge" type bridge
ip link set "$bridge" up

ip link add "nvs${suffix}" type veth peer name "nbs${suffix}"
ip link set "nvs${suffix}" netns "$server_ns"
ip link set "nbs${suffix}" master "$bridge"
ip link set "nbs${suffix}" up
ip link add "nvc${suffix}" type veth peer name "nbc${suffix}"
ip link set "nvc${suffix}" netns "$client_ns"
ip link set "nbc${suffix}" master "$bridge"
ip link set "nbc${suffix}" up
ip link add "nvl${suffix}" type veth peer name "nbl${suffix}"
ip link set "nvl${suffix}" netns "$vpp_pid"
ip link set "nbl${suffix}" master "$bridge"
ip link set "nbl${suffix}" up
ip link add "nvw${suffix}" type veth peer name "nww${suffix}"
ip link set "nvw${suffix}" netns "$vpp_pid"
ip link set "nww${suffix}" netns "$wan_ns"

ip netns exec "$server_ns" ip link set lo up
ip netns exec "$server_ns" ip link set "nvs${suffix}" up
ip netns exec "$server_ns" ip address add 192.168.88.20/24 dev "nvs${suffix}"
ip netns exec "$server_ns" ip route add default via 192.168.88.1
ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set "nvc${suffix}" up
ip netns exec "$client_ns" ip address add 192.168.88.30/24 dev "nvc${suffix}"
ip netns exec "$client_ns" ip route add default via 192.168.88.1
ip netns exec "$wan_ns" ip link set lo up
ip netns exec "$wan_ns" ip link set "nww${suffix}" up
ip netns exec "$wan_ns" ip address add 203.0.113.1/24 dev "nww${suffix}"

nsenter -t "$vpp_pid" -n ip link set "nvl${suffix}" up
nsenter -t "$vpp_pid" -n ip link set "nvw${suffix}" up
vppctl create host-interface name "nvl${suffix}" >/dev/null
vppctl create host-interface name "nvw${suffix}" >/dev/null
lan_interface="host-nvl${suffix}"
wan_interface="host-nvw${suffix}"
vppctl set interface state "$lan_interface" up
vppctl set interface state "$wan_interface" up
vppctl set interface ip address "$lan_interface" 192.168.88.1/24
vppctl set interface ip address "$wan_interface" 203.0.113.2/24
vppctl nat44 plugin enable
vppctl set interface nat44 in "$lan_interface"
vppctl set interface nat44 out "$wan_interface" output-feature
vppctl nat44 add address 203.0.113.3
vppctl nat44 add static mapping tcp local 192.168.88.20 8080 external 203.0.113.3 8080
vppctl nat44 add static mapping local 192.168.88.20 external 203.0.113.4

ip netns exec "$server_ns" python3 - <<'PY' >/tmp/ly-route-nat-server.log 2>&1 &
import socket

listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("192.168.88.20", 8080))
listener.listen(8)
for _ in range(3):
    connection, _ = listener.accept()
    connection.sendall(b"ly-route-nat-ok")
    connection.close()
PY
server_pid=$!
sleep 1

tcp_probe() {
  namespace=$1
  address=$2
  port=$3
  ip netns exec "$namespace" python3 - "$address" "$port" <<'PY'
import socket
import sys

connection = socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=3)
payload = connection.recv(64)
connection.close()
if payload != b"ly-route-nat-ok":
    raise SystemExit("unexpected NAT payload: %r" % payload)
PY
}

tcp_probe "$wan_ns" 203.0.113.3 8080
tcp_probe "$wan_ns" 203.0.113.4 8080
capture_file="/tmp/ly-route-nat-hairpin-$suffix.pcap.txt"
timeout 8 tcpdump -lni "$bridge" tcp >"$capture_file" 2>&1 &
capture_pid=$!
sleep 1
if ! tcp_probe "$client_ns" 203.0.113.3 8080; then
  echo "NAT44 hairpin probe failed" >&2
  wait "$capture_pid" 2>/dev/null || true
  cat "$capture_file" >&2
  ip netns exec "$server_ns" ip route show
  ip netns exec "$server_ns" ip neighbor show
  ip netns exec "$client_ns" ip neighbor show
  vppctl show nat44 sessions
  vppctl show errors
  exit 1
fi
kill "$capture_pid" 2>/dev/null || true
wait "$capture_pid" 2>/dev/null || true
rm -f "$capture_file"
wait "$server_pid"

readback=$(vppctl show nat44 static mappings)
printf '%s\n' "$readback" | grep -F '192.168.88.20' >/dev/null
printf '%s\n' "$readback" | grep -F '203.0.113.3' >/dev/null
printf '%s\n' "$readback" | grep -F '203.0.113.4' >/dev/null
vppctl show nat44 sessions | grep -E '192\.168\.88\.(20|30)|203\.0\.113\.(3|4)' >/dev/null

echo "VPP NAT44 port mapping, one-to-one mapping, hairpin, session and readback verification passed"
