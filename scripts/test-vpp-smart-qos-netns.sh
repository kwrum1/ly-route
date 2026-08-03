#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null
command -v python3 >/dev/null

plugin=${LY_ROUTE_VPP_SMART_QOS_PLUGIN:-}
if [ -z "$plugin" ]; then
  plugin=$($repo_root/scripts/build-vpp-smart-qos-plugin.sh | tail -1)
fi
[ -f "$plugin" ] || { echo "smart QoS plugin not found: $plugin" >&2; exit 1; }

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="ly-route-smart-qos-$suffix"
client_ns="ly-route-sq-client-$suffix"
server_ns="ly-route-sq-server-$suffix"
cli_socket="/run/vpp/ly-route-smart-qos-$suffix.sock"
result_file="/tmp/ly-route-smart-qos-$suffix.json"
down_result_file="/tmp/ly-route-smart-qos-$suffix-down.json"
workers=${LY_ROUTE_VPP_WORKERS:-0}
case "$workers" in ''|*[!0-9]*) echo "invalid LY_ROUTE_VPP_WORKERS: $workers" >&2; exit 1 ;; esac

cleanup() {
  if [ "${LY_ROUTE_KEEP_TEST_STATE:-0}" = 1 ]; then
    return
  fi
  docker rm -f "$container" >/dev/null 2>&1 || true
  ip netns del "$client_ns" >/dev/null 2>&1 || true
  ip netns del "$server_ns" >/dev/null 2>&1 || true
  rm -f "$result_file" "$down_result_file"
}
trap cleanup EXIT INT TERM
cleanup

if [ "$workers" -gt 0 ]; then
  docker run -d --name "$container" --privileged --network none --shm-size 256m \
    -v "$plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_smart_qos_plugin.so:ro" \
    ly-route/vpp-test:25.10 \
    /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
    cpu "{ workers $workers }" plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
else
  docker run -d --name "$container" --privileged --network none --shm-size 256m \
    -v "$plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_smart_qos_plugin.so:ro" \
    ly-route/vpp-test:25.10 \
    /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
    plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
fi
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$cli_socket" "$@"; }
attempt=0
until vppctl show version >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then docker logs "$container" >&2; exit 1; fi
  sleep 0.25
done
vppctl show plugin | grep -q ly_route_smart_qos_plugin.so

ip netns add "$client_ns"
ip netns add "$server_ns"
ip link add "scl${suffix}" type veth peer name "svl${suffix}"
ip link set "scl${suffix}" netns "$client_ns"
ip link set "svl${suffix}" netns "$vpp_pid"
ip link add "ssv${suffix}" type veth peer name "svs${suffix}"
ip link set "ssv${suffix}" netns "$server_ns"
ip link set "svs${suffix}" netns "$vpp_pid"

ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set "scl${suffix}" up
ip netns exec "$client_ns" ip address add 192.0.2.2/24 dev "scl${suffix}"
ip netns exec "$client_ns" ip address add 192.0.2.3/24 dev "scl${suffix}"
ip netns exec "$client_ns" ip route add default via 192.0.2.1
ip netns exec "$server_ns" ip link set lo up
ip netns exec "$server_ns" ip link set "ssv${suffix}" up
ip netns exec "$server_ns" ip address add 198.51.100.2/24 dev "ssv${suffix}"
ip netns exec "$server_ns" ip route add default via 198.51.100.1
nsenter -t "$vpp_pid" -n ip link set "svl${suffix}" up
nsenter -t "$vpp_pid" -n ip link set "svs${suffix}" up

vppctl create host-interface name "svl${suffix}" >/dev/null
vppctl create host-interface name "svs${suffix}" >/dev/null
lan_interface="host-svl${suffix}"
wan_interface="host-svs${suffix}"
vppctl set interface state "$lan_interface" up
vppctl set interface state "$wan_interface" up
vppctl set interface ip address "$lan_interface" 192.0.2.1/24
vppctl set interface ip address "$wan_interface" 198.51.100.1/24
ip netns exec "$client_ns" ping -c 2 -W 2 198.51.100.2 >/dev/null

vppctl set ly-route smart-qos interface "$wan_interface" rate 2000
vppctl show ly-route smart-qos | grep -q "interface $wan_interface enabled"

ip netns exec "$server_ns" python3 - "$result_file" <<'PY' &
import json
import socket
import sys
import time

sockets = []
for port in range(9000, 9005):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("198.51.100.2", port))
    sock.setblocking(False)
    sockets.append(sock)
counts = [0] * len(sockets)
total_bytes = 0
first = None
last = None
deadline = time.monotonic() + 7
while time.monotonic() < deadline:
    received = False
    for index, sock in enumerate(sockets):
        try:
            payload = sock.recv(2048)
        except BlockingIOError:
            continue
        now = time.monotonic()
        first = now if first is None else first
        last = now
        counts[index] += 1
        total_bytes += len(payload)
        received = True
    if not received:
        time.sleep(0.001)
duration = 0 if first is None or last is None else last - first
with open(sys.argv[1], "w", encoding="ascii") as output:
    json.dump({"counts": counts, "bytes": total_bytes, "duration": duration}, output)
PY
receiver_pid=$!
sleep 0.5

ip netns exec "$client_ns" python3 - <<'PY' &
import socket
import time

sockets = [socket.socket(socket.AF_INET, socket.SOCK_DGRAM) for _ in range(5)]
for sock in sockets[:4]:
    sock.bind(("192.0.2.2", 0))
sockets[4].bind(("192.0.2.3", 0))
payload = b"q" * 1000
deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    for index, sock in enumerate(sockets):
        sock.sendto(payload, ("198.51.100.2", 9000 + index))
PY
sender_pid=$!
sleep 0.5
ping_output=$(ip netns exec "$client_ns" ping -c 20 -i 0.1 -W 2 198.51.100.2)
wait "$sender_pid"
wait "$receiver_pid"

python3 - "$result_file" "$ping_output" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="ascii") as source:
    result = json.load(source)
if result["duration"] < 2:
    raise SystemExit(f"shaping duration too short: {result}")
rate_mbps = result["bytes"] * 8 / result["duration"] / 1_000_000
if not 1.2 <= rate_mbps <= 2.8:
    raise SystemExit(f"shaped rate outside tolerance: {rate_mbps:.3f} Mbps {result}")
host_one = sum(result["counts"][:4])
host_two = result["counts"][4]
low, high = sorted((host_one, host_two))
if low == 0 or high / low > 1.5:
    raise SystemExit(f"per-host fairness outside tolerance: {result['counts']}")
flow_low, flow_high = min(result["counts"][:4]), max(result["counts"][:4])
if flow_low == 0 or flow_high / flow_low > 1.5:
    raise SystemExit(f"per-flow fairness outside tolerance: {result['counts']}")
match = re.search(r"= [^/]+/[^/]+/([^/]+)/", sys.argv[2])
if not match or float(match.group(1)) > 80:
    raise SystemExit(f"loaded ping max is missing or too high: {sys.argv[2]}")
print(f"rate={rate_mbps:.3f}Mbps flows={result['counts']} hosts={[host_one, host_two]} loaded_ping_max={match.group(1)}ms")
PY

vppctl set ly-route smart-qos interface "$wan_interface" disable
vppctl set ly-route smart-qos interface "$lan_interface" rate 2000 host-isolation destination

ip netns exec "$client_ns" python3 - "$down_result_file" <<'PY' &
import json
import socket
import sys
import time

sockets = []
for port in range(9100, 9105):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", port))
    sock.setblocking(False)
    sockets.append(sock)
counts = [0] * len(sockets)
total_bytes = 0
first = None
last = None
deadline = time.monotonic() + 7
while time.monotonic() < deadline:
    received = False
    for index, sock in enumerate(sockets):
        try:
            payload = sock.recv(2048)
        except BlockingIOError:
            continue
        now = time.monotonic()
        first = now if first is None else first
        last = now
        counts[index] += 1
        total_bytes += len(payload)
        received = True
    if not received:
        time.sleep(0.001)
duration = 0 if first is None or last is None else last - first
with open(sys.argv[1], "w", encoding="ascii") as output:
    json.dump({"counts": counts, "bytes": total_bytes, "duration": duration}, output)
PY
down_receiver_pid=$!
sleep 0.5

ip netns exec "$server_ns" python3 - <<'PY' &
import socket
import time

sockets = [socket.socket(socket.AF_INET, socket.SOCK_DGRAM) for _ in range(5)]
payload = b"d" * 1000
destinations = [("192.0.2.2", 9100 + index) for index in range(4)] + [("192.0.2.3", 9104)]
deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    for sock, destination in zip(sockets, destinations):
        sock.sendto(payload, destination)
PY
down_sender_pid=$!
sleep 0.5
down_ping_output=$(ip netns exec "$client_ns" ping -c 20 -i 0.1 -W 2 198.51.100.2)
wait "$down_sender_pid"
wait "$down_receiver_pid"

python3 - "$down_result_file" "$down_ping_output" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="ascii") as source:
    result = json.load(source)
if result["duration"] < 2:
    raise SystemExit(f"downstream shaping duration too short: {result}")
rate_mbps = result["bytes"] * 8 / result["duration"] / 1_000_000
if not 1.2 <= rate_mbps <= 2.8:
    raise SystemExit(f"downstream shaped rate outside tolerance: {rate_mbps:.3f} Mbps {result}")
host_one = sum(result["counts"][:4])
host_two = result["counts"][4]
low, high = sorted((host_one, host_two))
if low == 0 or high / low > 1.5:
    raise SystemExit(f"downstream per-host fairness outside tolerance: {result['counts']}")
flow_low, flow_high = min(result["counts"][:4]), max(result["counts"][:4])
if flow_low == 0 or flow_high / flow_low > 1.5:
    raise SystemExit(f"downstream per-flow fairness outside tolerance: {result['counts']}")
match = re.search(r"= [^/]+/[^/]+/([^/]+)/", sys.argv[2])
if not match or float(match.group(1)) > 80:
    raise SystemExit(f"downstream loaded ping max is missing or too high: {sys.argv[2]}")
print(f"down_rate={rate_mbps:.3f}Mbps flows={result['counts']} hosts={[host_one, host_two]} loaded_ping_max={match.group(1)}ms")
PY

status=$(vppctl show ly-route smart-qos)
printf '%s\n' "$status" | grep -q 'algorithm fq-codel'
printf '%s\n' "$status" | grep -q "interface $lan_interface enabled rate-kbps 2000 host-isolation destination"
if [ "$workers" -gt 0 ]; then
  printf '%s\n' "$status" | grep -q 'scheduler-thread 1'
else
  printf '%s\n' "$status" | grep -q 'scheduler-thread 0'
fi
printf '%s\n' "$status" | grep -Eq 'transmitted [1-9][0-9]*'
printf '%s\n' "$status" | grep -Eq '(aqm-drops|overflow-drops) [1-9][0-9]*'
invalid_rate_output=$(vppctl set ly-route smart-qos interface "$lan_interface" rate 0 2>&1 || true)
printf '%s\n' "$invalid_rate_output" | grep -Eq 'rate is required|smart QoS configuration failed'
status_after_invalid=$(vppctl show ly-route smart-qos)
printf '%s\n' "$status_after_invalid" | grep -q "interface $lan_interface enabled rate-kbps 2000 host-isolation destination"
vppctl set ly-route smart-qos interface "$lan_interface" disable
status_after_disable=$(vppctl show ly-route smart-qos)
if printf '%s\n' "$status_after_disable" | grep -q "interface $lan_interface enabled"; then
  echo 'Smart QoS interface remained enabled after rollback disable' >&2
  exit 1
fi
vppctl set ly-route smart-qos interface "$lan_interface" rate 2000 host-isolation destination
vppctl show ly-route smart-qos | grep -q "interface $lan_interface enabled rate-kbps 2000 host-isolation destination"
printf 'Smart QoS fault checks passed: invalid rate preserved prior state; rollback disable and restore read back exactly\n'
printf 'VPP smart QoS packet flow passed with %s workers: %s\n' "$workers" "$(printf '%s' "$status" | tr '\n' ';')"
