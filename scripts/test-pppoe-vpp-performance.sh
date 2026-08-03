#!/bin/sh
set -eu

for command in docker ip iperf3 pppd pppoe-server python3; do command -v "$command" >/dev/null; done
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=$(printf '%05d' $(( $$ % 100000 )))
duration=${LY_ROUTE_PERF_DURATION:-10}
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-"$repo_root/.sisyphus/full-acceptance/evidence/g-pppoe-performance"}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-pppoe-perf.XXXXXX")
container="lr-pppoe-perf-$suffix"
client_ns="lr-perf-client-$suffix"
cpe_ns="lr-perf-cpe-$suffix"
ac_ns="lr-perf-ac-$suffix"
server_ns="lr-perf-server-$suffix"
socket="/run/vpp/pppoe-perf-$suffix.sock"
namespaces="$client_ns $cpe_ns $ac_ns $server_ns"

cleanup() {
  status=$?
  set +e
  for namespace in $namespaces; do
    ip netns pids "$namespace" 2>/dev/null | xargs -r kill -9 2>/dev/null
    ip netns del "$namespace" 2>/dev/null
  done
  docker rm -f "$container" >/dev/null 2>&1
  if [ "$status" -ne 0 ]; then
    for log in "$tmp"/*.log; do [ ! -f "$log" ] || { echo "--- $log" >&2; tail -80 "$log" >&2; }; done
  fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT INT TERM

for namespace in $namespaces; do ip netns add "$namespace"; ip -n "$namespace" link set lo up; done

# Real PPPoE discovery and session between isolated CPE and AC namespaces.
ip link add "pa$suffix" type veth peer name "pc$suffix"
ip link set "pa$suffix" netns "$ac_ns"
ip link set "pc$suffix" netns "$cpe_ns"
ip -n "$ac_ns" link set "pa$suffix" up
ip -n "$cpe_ns" link set "pc$suffix" up
cat >"$tmp/server-options" <<'EOF'
noauth
mtu 1492
mru 1492
lcp-echo-interval 10
lcp-echo-failure 3
EOF
cat >"$tmp/cpe-options" <<EOF
plugin rp-pppoe.so
pc$suffix
ifname ppp-wan
noauth
noipdefault
defaultroute
mtu 1492
mru 1492
persist
nodetach
EOF
ip netns exec "$ac_ns" pppoe-server -I "pa$suffix" -L 10.67.0.1 -R 10.67.0.10 -N 1 -C LY-ROUTE-PERF -O "$tmp/server-options" -X "$tmp/pppoe-server.pid" >"$tmp/pppoe-ac.log" 2>&1 &
sleep 1
ip netns exec "$cpe_ns" pppd file "$tmp/cpe-options" >"$tmp/pppoe-cpe.log" 2>&1 &
pppd_pid=$!
attempt=0
until ip -n "$cpe_ns" -4 address show dev ppp-wan 2>/dev/null | grep -q '10.67.0.10'; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 150 ] || { echo 'PPPoE session did not converge' >&2; exit 1; }; sleep 0.1
done
ac_ppp=$(ip -n "$ac_ns" -o link show | awk -F': ' '$2 ~ /^ppp/ {print $2; exit}')
[ -n "$ac_ppp" ] || { echo 'PPPoE server interface was not created' >&2; exit 1; }
ip -n "$cpe_ns" -d address show dev ppp-wan >"$tmp/pppoe-cpe-address.txt"
ip -n "$ac_ns" -d address show dev "$ac_ppp" >"$tmp/pppoe-ac-address.txt"

# Internet endpoint behind the access concentrator.
ip link add "ia$suffix" type veth peer name "is$suffix"
ip link set "ia$suffix" netns "$ac_ns"
ip link set "is$suffix" netns "$server_ns"
ip -n "$ac_ns" link set "ia$suffix" up
ip -n "$ac_ns" address add 203.0.113.1/24 dev "ia$suffix"
ip -n "$server_ns" link set "is$suffix" up
ip -n "$server_ns" address add 203.0.113.2/24 dev "is$suffix"
ip -n "$server_ns" route add default via 203.0.113.1
for namespace in "$ac_ns" "$cpe_ns"; do
  ip netns exec "$namespace" sysctl -qw net.ipv4.ip_forward=1
  ip netns exec "$namespace" sysctl -qw net.ipv4.conf.all.rp_filter=0
done

# VPP sits between the LAN and the live PPPoE CPE.
docker run -d --name "$container" --privileged --network none --shm-size 256m --cpuset-cpus 2-3 \
  ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" \
  cpu "{ main-core 2 corelist-workers 3 }" plugins "{ plugin dpdk_plugin.so { disable } }" >"$tmp/vpp-container-id.txt"
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
attempt=0
until vppctl show version >/dev/null 2>&1; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 100 ] || { docker logs "$container" >&2; exit 1; }; sleep 0.1
done

ip link add "vl$suffix" type veth peer name "cl$suffix"
ip link set "vl$suffix" netns "$vpp_pid"
ip link set "cl$suffix" netns "$client_ns"
ip -n "$client_ns" link set "cl$suffix" mtu 1492 up
ip -n "$client_ns" address add 192.168.88.2/24 dev "cl$suffix"
ip -n "$client_ns" route add default via 192.168.88.1
ip link add "vw$suffix" type veth peer name "cw$suffix"
ip link set "vw$suffix" netns "$vpp_pid"
ip link set "cw$suffix" netns "$cpe_ns"
ip -n "$cpe_ns" link set "cw$suffix" mtu 1492 up
ip -n "$cpe_ns" address add 192.0.2.1/30 dev "cw$suffix"
nsenter -t "$vpp_pid" -n ip link set "vl$suffix" mtu 1492 up
nsenter -t "$vpp_pid" -n ip link set "vw$suffix" mtu 1492 up
vppctl create host-interface name "vl$suffix" >/dev/null
vppctl create host-interface name "vw$suffix" >/dev/null
lan="host-vl$suffix"; wan="host-vw$suffix"
vppctl set interface state "$lan" up
vppctl set interface state "$wan" up
vppctl set interface mtu 1492 "$lan"
vppctl set interface mtu 1492 "$wan"
vppctl set interface ip address "$lan" 192.168.88.1/24
vppctl set interface ip address "$wan" 192.0.2.2/30
vppctl ip route add 0.0.0.0/0 via 192.0.2.1 "$wan"

# Route one public /32 over PPPoE to VPP and use it as the NAT44 address.
ip -n "$ac_ns" route add 10.67.0.11/32 via 10.67.0.10 dev "$ac_ppp"
ip -n "$cpe_ns" route add 10.67.0.11/32 via 192.0.2.2 dev "cw$suffix"
vppctl nat44 plugin enable
vppctl set interface nat44 in "$lan"
vppctl set interface nat44 out "$wan"
vppctl nat44 add address 10.67.0.11

run_iperf() {
  name=$1; shift
  ip netns exec "$server_ns" iperf3 -s -1 >"$tmp/$name-server.log" 2>&1 &
  server_pid=$!; sleep 0.3
  ip netns exec "$client_ns" iperf3 -c 203.0.113.2 -t "$duration" -J "$@" >"$tmp/$name.json"
  wait "$server_pid"
}

ip netns exec "$client_ns" ping -c 3 -W 2 203.0.113.2 >"$tmp/nat-ping.txt"
run_iperf nat-tcp -P 4
run_iperf nat-64b-udp -u -P 4 -l 18 -b 0
vppctl show nat44 sessions >"$tmp/nat-sessions.txt"
vppctl show interface >"$tmp/vpp-interfaces-after-nat.txt"
vppctl show runtime >"$tmp/vpp-runtime-after-nat.txt"

# Remove NAT and benchmark pure routing over the same PPPoE session.
vppctl set interface nat44 in "$lan" del
vppctl set interface nat44 out "$wan" del
vppctl nat44 plugin disable
ip -n "$ac_ns" route add 192.168.88.0/24 via 10.67.0.10 dev "$ac_ppp"
ip -n "$cpe_ns" route add 192.168.88.0/24 via 192.0.2.2 dev "cw$suffix"
ip netns exec "$client_ns" ping -c 3 -W 2 203.0.113.2 >"$tmp/route-ping.txt"
run_iperf route-tcp -P 4
run_iperf route-64b-udp -u -P 4 -l 18 -b 0
vppctl show interface >"$tmp/vpp-interfaces-after-route.txt"
vppctl show runtime >"$tmp/vpp-runtime-after-route.txt"

python3 - "$tmp" "$duration" >"$tmp/summary.json" <<'PY'
import json, pathlib, platform, sys
root = pathlib.Path(sys.argv[1])
duration = int(sys.argv[2])
def load(name): return json.loads((root / name).read_text())
def tcp(name):
    data = load(name)["end"]
    return {"sent_bps": data["sum_sent"]["bits_per_second"], "received_bps": data["sum_received"]["bits_per_second"], "retransmits": data["sum_sent"].get("retransmits", 0)}
def udp(name):
    item = load(name)["end"]["sum"]
    return {"payload_bytes": 18, "ethernet_wire_bytes_with_fcs": 64, "received_bps": item["bits_per_second"], "received_pps": item["packets"] / item["seconds"] if item["seconds"] else 0, "packets": item["packets"], "lost_packets": item["lost_packets"], "lost_percent": item["lost_percent"]}
summary = {"schema_version": 1, "environment": {"architecture": platform.machine(), "virtualized": True, "vpp_workers": 1, "iperf_parallel_streams": 4, "duration_seconds": duration, "pppoe_mtu": 1492}, "nat44_over_pppoe": {"tcp": tcp("nat-tcp.json"), "udp_64b": udp("nat-64b-udp.json")}, "routing_over_pppoe": {"tcp": tcp("route-tcp.json"), "udp_64b": udp("route-64b-udp.json")}}
json.dump(summary, sys.stdout, ensure_ascii=True, indent=2); print()
PY

rm -rf "$evidence_dir"; mkdir -p "$evidence_dir"
cp "$tmp"/*.json "$tmp"/*.txt "$evidence_dir/"
lscpu >"$evidence_dir/lscpu.txt"
iperf3 --version >"$evidence_dir/iperf3-version.txt" 2>&1
vppctl show version >"$evidence_dir/vpp-version.txt"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
cat "$tmp/summary.json"
kill "$pppd_pid" 2>/dev/null || true
trap - EXIT INT TERM
cleanup
