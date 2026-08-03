#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
plugin=${LY_ROUTE_VPP_SECURITY_GUARD_PLUGIN:-}
if [ -z "$plugin" ]; then
  plugin=$($repo_root/scripts/build-vpp-security-guard-plugin.sh | tail -1)
fi
[ -f "$plugin" ] || { echo "security guard plugin not found: $plugin" >&2; exit 1; }

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="ly-route-security-guard-$suffix"
client="ly-route-sg-client-$suffix"
server="ly-route-sg-server-$suffix"
socket="/run/vpp/security-guard-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/g-security-guard"

cleanup() {
  set +e
  ip netns del "$client" >/dev/null 2>&1 || true
  ip netns del "$server" >/dev/null 2>&1 || true
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup
rm -rf "$tmp"
mkdir -p "$tmp"

docker run -d --name "$container" --privileged --network none --shm-size 256m \
  -v "$plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_security_guard_plugin.so:ro" \
  ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" \
  plugins "{ plugin dpdk_plugin.so { disable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do vppctl show version >/dev/null 2>&1 && break; sleep .25; done
vppctl show plugin >"$tmp/plugins.txt"
grep -q ly_route_security_guard_plugin.so "$tmp/plugins.txt"

ip netns add "$client"
ip netns add "$server"
ip link add sg-lan0 type veth peer name sg-client0
ip link add sg-wan0 type veth peer name sg-server0
ip link set sg-lan0 netns "$vpp_pid"
ip link set sg-wan0 netns "$vpp_pid"
ip link set sg-client0 netns "$client"
ip link set sg-server0 netns "$server"
ip -n "$client" link set sg-client0 up
ip -n "$client" addr add 10.0.0.2/24 dev sg-client0
ip -n "$client" -6 addr add 2001:db8:0::2/64 dev sg-client0 nodad
ip -n "$client" route add default via 10.0.0.1
ip -n "$client" -6 route add default via 2001:db8:0::1 dev sg-client0
ip -n "$server" link set sg-server0 up
ip -n "$server" addr add 10.0.1.2/24 dev sg-server0
ip -n "$server" -6 addr add 2001:db8:1::2/64 dev sg-server0 nodad
ip -n "$server" route add default via 10.0.1.1
ip -n "$server" -6 route add default via 2001:db8:1::1 dev sg-server0
for interface in sg-lan0 sg-wan0; do
  nsenter -t "$vpp_pid" -n ip link set "$interface" up
  vppctl create host-interface name "$interface" >/dev/null
  vppctl set interface state "host-$interface" up
  vppctl set interface name "host-$interface" "$interface"
done
vppctl set interface ip address sg-lan0 10.0.0.1/24
vppctl set interface ip address sg-wan0 10.0.1.1/24
vppctl set interface ip address sg-lan0 2001:db8:0::1/64
vppctl set interface ip address sg-wan0 2001:db8:1::1/64
ip netns exec "$client" ping -c 2 -W 1 10.0.1.2 >"$tmp/direct.txt"
ip netns exec "$client" ping -6 -c 2 -W 1 2001:db8:1::2 >"$tmp/direct-ipv6.txt"

set_output=$(vppctl set ly-route security-guard rule syn-test interface sg-lan0 family ip4 \
  attack-type syn_flood threshold-pps 5 burst-packets 2 mode enforce \
  source 10.0.0.0/24 2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
vppctl show ly-route security-guard >"$tmp/status-before.txt"
grep -q 'rule syn-test enabled 1 family 4' "$tmp/status-before.txt" || { cat "$tmp/status-before.txt" >&2; exit 1; }

ip netns exec "$server" python3 - <<'PY' >/dev/null 2>&1 &
import socket
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("10.0.1.2", 8443))
s.listen(64)
while True:
    c, _ = s.accept()
    c.close()
PY
server_pid=$!
sleep .5
ip netns exec "$client" python3 - <<'PY'
import socket
for _ in range(80):
    s = socket.socket()
    s.settimeout(.2)
    try:
        s.connect(("10.0.1.2", 8443))
    except OSError:
        pass
    s.close()
PY
vppctl show ly-route security-guard >"$tmp/status-after-syn.txt"
grep -q 'rule syn-test enabled 1' "$tmp/status-after-syn.txt" || { cat "$tmp/status-after-syn.txt" >&2; exit 1; }
grep -Eq 'matched [1-9][0-9]*' "$tmp/status-after-syn.txt" || { cat "$tmp/status-after-syn.txt" >&2; exit 1; }
grep -Eq 'drops [1-9][0-9]*' "$tmp/status-after-syn.txt" || { cat "$tmp/status-after-syn.txt" >&2; exit 1; }

set_output=$(vppctl set ly-route security-guard rule syn-alert interface sg-lan0 family ip4 \
  attack-type syn_flood threshold-pps 5 burst-packets 2 mode alert \
  source 10.0.0.0/24 2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
vppctl show ly-route security-guard >"$tmp/status-alert.txt"
grep -q 'rule syn-alert enabled 1' "$tmp/status-alert.txt" || { cat "$tmp/status-alert.txt" >&2; exit 1; }

set_output=$(vppctl set ly-route security-guard rule syn-test interface sg-lan0 family ip4 \
  attack-type syn_flood threshold-pps 5 burst-packets 2 mode enforce disable
  2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
vppctl show ly-route security-guard >"$tmp/status-disabled.txt"
grep -q 'rule syn-test enabled 0' "$tmp/status-disabled.txt" || { cat "$tmp/status-disabled.txt" >&2; exit 1; }

ip netns exec "$client" python3 - <<'PY'
import socket
for _ in range(80):
    s = socket.socket()
    s.settimeout(.2)
    try:
        s.connect(("10.0.1.2", 8443))
    except OSError:
        pass
    s.close()
PY
vppctl show ly-route security-guard >"$tmp/status-after-alert.txt"
grep -Eq 'rule syn-alert enabled 1 family 4 .* matched [1-9][0-9]* .* alerts [1-9][0-9]*' "$tmp/status-after-alert.txt" || { cat "$tmp/status-after-alert.txt" >&2; exit 1; }

delete_output=$(vppctl delete ly-route security-guard rule syn-alert 2>&1)
case "$delete_output" in *unknown*|*invalid*|*failed*) echo "$delete_output" >&2; exit 1 ;; esac
vppctl show ly-route security-guard >"$tmp/status-after-delete.txt"
if grep -q 'rule syn-alert ' "$tmp/status-after-delete.txt"; then
  cat "$tmp/status-after-delete.txt" >&2
  exit 1
fi

set_output=$(vppctl set ly-route security-guard rule udp-test interface sg-lan0 family ip4 \
  attack-type udp_flood threshold-pps 5 burst-packets 2 mode enforce source 10.0.0.0/24 2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
ip netns exec "$server" python3 - <<'PY' >/dev/null 2>&1 &
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("10.0.1.2", 5353))
while True:
    s.recvfrom(65535)
PY
udp_server_pid=$!
sleep .2
ip netns exec "$client" python3 - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
for _ in range(80):
    s.sendto(b"guard", ("10.0.1.2", 5353))
PY
vppctl show ly-route security-guard >"$tmp/status-after-udp.txt"
grep -Eq 'rule udp-test enabled 1 family 4 .* matched [1-9][0-9]* .* drops [1-9][0-9]*' "$tmp/status-after-udp.txt" || { cat "$tmp/status-after-udp.txt" >&2; exit 1; }
kill "$udp_server_pid" >/dev/null 2>&1 || true

set_output=$(vppctl set ly-route security-guard rule icmp4-test interface sg-lan0 family ip4 \
  attack-type icmp_flood threshold-pps 5 burst-packets 2 mode enforce source 10.0.0.0/24 2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
set +e
ip netns exec "$client" ping -c 80 -i .01 -W 1 10.0.1.2 >"$tmp/icmp4-flood.txt"
set -e
vppctl show ly-route security-guard >"$tmp/status-after-icmp4.txt"
grep -Eq 'rule icmp4-test enabled 1 family 4 .* matched [1-9][0-9]* .* drops [1-9][0-9]*' "$tmp/status-after-icmp4.txt" || { cat "$tmp/status-after-icmp4.txt" >&2; exit 1; }

set_output=$(vppctl set ly-route security-guard rule icmp6-test interface sg-lan0 family ip6 \
  attack-type icmp_flood threshold-pps 5 burst-packets 2 mode enforce source 2001:db8:0::/64 2>&1)
case "$set_output" in *unknown*|*invalid*|*failed*) echo "$set_output" >&2; exit 1 ;; esac
set +e
ip netns exec "$client" ping -6 -c 80 -i .01 -W 1 2001:db8:1::2 >"$tmp/icmp6-flood.txt"
set -e
vppctl show ly-route security-guard >"$tmp/status-after-icmp6.txt"
grep -Eq 'rule icmp6-test enabled 1 family 6 .* matched [1-9][0-9]* .* drops [1-9][0-9]*' "$tmp/status-after-icmp6.txt" || { cat "$tmp/status-after-icmp6.txt" >&2; exit 1; }

kill "$server_pid" >/dev/null 2>&1 || true
cp "$tmp"/*.txt "${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$tmp}/" 2>/dev/null || true
printf 'VPP security guard SYN/UDP/ICMP/IPv6 alert/disable packet verification passed: %s\n' "$tmp"
