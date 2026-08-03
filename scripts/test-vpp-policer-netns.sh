#!/bin/sh
set -eu

command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null
command -v python3 >/dev/null

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="ly-route-vpp-policer-$suffix"
client_ns="ly-route-qos-client-$suffix"
server_ns="ly-route-qos-server-$suffix"
cli_socket="/run/vpp/ly-route-qos-$suffix.sock"
count_file="/tmp/ly-route-qos-count-$suffix"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  ip netns del "$client_ns" >/dev/null 2>&1 || true
  ip netns del "$server_ns" >/dev/null 2>&1 || true
  rm -f "$count_file"
}
trap cleanup EXIT INT TERM
cleanup

docker run -d --name "$container" --privileged --network none --shm-size 256m \
  ly-route/vpp-test:25.10 \
  /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
  plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$cli_socket" "$@"; }
attempt=0
until vppctl show version >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 20 ]; then
    docker logs "$container" >&2
    exit 1
  fi
  sleep 0.25
done

ip netns add "$client_ns"
ip netns add "$server_ns"
ip link add "qcl${suffix}" type veth peer name "qvl${suffix}"
ip link set "qcl${suffix}" netns "$client_ns"
ip link set "qvl${suffix}" netns "$vpp_pid"
ip link add "qsv${suffix}" type veth peer name "qvs${suffix}"
ip link set "qsv${suffix}" netns "$server_ns"
ip link set "qvs${suffix}" netns "$vpp_pid"

ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set "qcl${suffix}" up
ip netns exec "$client_ns" ip address add 192.0.2.2/24 dev "qcl${suffix}"
ip netns exec "$client_ns" ip route add default via 192.0.2.1
ip netns exec "$server_ns" ip link set lo up
ip netns exec "$server_ns" ip link set "qsv${suffix}" up
ip netns exec "$server_ns" ip address add 198.51.100.2/24 dev "qsv${suffix}"
ip netns exec "$server_ns" ip route add default via 198.51.100.1

nsenter -t "$vpp_pid" -n ip link set "qvl${suffix}" up
nsenter -t "$vpp_pid" -n ip link set "qvs${suffix}" up
vppctl create host-interface name "qvl${suffix}" >/dev/null
vppctl create host-interface name "qvs${suffix}" >/dev/null
lan_interface="host-qvl${suffix}"
wan_interface="host-qvs${suffix}"
vppctl set interface state "$lan_interface" up
vppctl set interface state "$wan_interface" up
vppctl set interface ip address "$lan_interface" 192.0.2.1/24
vppctl set interface ip address "$wan_interface" 198.51.100.1/24
ip netns exec "$client_ns" ping -c 3 -W 2 198.51.100.2 >/dev/null

vppctl policer add name lyroute-user-limit type 1r2c cir 100 cb 12500 rate kbps conform-action transmit exceed-action drop violate-action drop
vppctl policer input name lyroute-user-limit "$lan_interface"

ip netns exec "$server_ns" python3 - "$count_file" <<'PY' &
import socket
import sys
import time

receiver = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
receiver.bind(("198.51.100.2", 9000))
receiver.settimeout(0.5)
deadline = time.monotonic() + 6
count = 0
while time.monotonic() < deadline:
    try:
        receiver.recvfrom(2048)
        count += 1
    except TimeoutError:
        if count:
            break
with open(sys.argv[1], "w", encoding="ascii") as output:
    output.write(str(count))
PY
receiver_pid=$!
sleep 1
ip netns exec "$client_ns" python3 - <<'PY'
import socket

sender = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
payload = b"q" * 1000
for _ in range(5000):
    sender.sendto(payload, ("198.51.100.2", 9000))
sender.close()
PY
wait "$receiver_pid"

received=$(cat "$count_file")
if [ "$received" -le 0 ] || [ "$received" -ge 5000 ]; then
  echo "VPP policer packet result is invalid: received=$received sent=5000" >&2
  exit 1
fi
policer=$(vppctl show policer name lyroute-user-limit)
printf '%s\n' "$policer" | grep -E '(exceed|violate) [1-9][0-9]* packets' >/dev/null
printf '%s\n' "$policer" | grep -E 'conform [1-9][0-9]* packets' >/dev/null

echo "VPP user-rate policer forwarding, conform and exceed-drop verification passed (received=$received sent=5000)"
