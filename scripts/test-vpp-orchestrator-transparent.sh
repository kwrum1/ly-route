#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -z "${LY_ROUTE_ORCHESTRATOR_PLUGIN:-}" ] && ! find "$repo_root/build/vpp-orchestrator/cmake" -type f -name ly_route_orchestrator_plugin.so -print -quit 2>/dev/null | grep -q .; then
  "$repo_root/scripts/build-vpp-orchestrator-plugin.sh" >/dev/null
fi
plugin=${LY_ROUTE_ORCHESTRATOR_PLUGIN:-$(find "$repo_root/build/vpp-orchestrator/cmake" -type f -name ly_route_orchestrator_plugin.so -print -quit)}
[ -f "$plugin" ]
suffix=$$
container="ly-route-orch-transparent-$suffix"
client="lotc-$suffix"
server="lots-$suffix"
service="loti-$suffix"
socket="/run/vpp/orch-transparent-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/o-transparent-$suffix"
control_pid=""
capture_pid=""

cleanup() {
  set +e
  [ -z "$control_pid" ] || kill "$control_pid" 2>/dev/null
  [ -z "$control_pid" ] || wait "$control_pid" 2>/dev/null
  [ -z "$capture_pid" ] || kill -INT "$capture_pid" 2>/dev/null
  [ -z "$capture_pid" ] || wait "$capture_pid" 2>/dev/null
  ip netns del "$client" 2>/dev/null
  ip netns del "$server" 2>/dev/null
  ip netns del "$service" 2>/dev/null
  docker rm -f "$container" >/dev/null 2>&1
}
trap cleanup EXIT INT TERM

rm -rf "$tmp"
mkdir -p "$tmp"
docker run -d --name "$container" --privileged --network none --shm-size 256m \
  -v "$plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_orchestrator_plugin.so:ro" \
  ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" \
  cpu "{ workers 2 }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do
  vppctl show version >/dev/null 2>&1 && break
  sleep .25
done
vppctl show plugin | grep -q ly_route_orchestrator_plugin.so

ip netns add "$client"
ip netns add "$server"
ip netns add "$service"
for namespace in "$client" "$server" "$service"; do
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.all.disable_ipv6=1
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.default.disable_ipv6=1
done
ip link add lyroute-wan0 type veth peer name client0
ip link add lyroute-wan1 type veth peer name client1
ip link add lyroute-lan0 type veth peer name server0
ip link add lyroute-a-wan type veth peer name inline-wan
ip link add lyroute-a-lan type veth peer name inline-lan
ip link add lyroute-b-wan type veth peer name inline-b-wan
ip link add lyroute-b-lan type veth peer name inline-b-lan
ip link set lyroute-wan0 netns "$vpp_pid"
ip link set lyroute-wan1 netns "$vpp_pid"
ip link set lyroute-lan0 netns "$vpp_pid"
ip link set lyroute-a-wan netns "$vpp_pid"
ip link set lyroute-a-lan netns "$vpp_pid"
ip link set lyroute-b-wan netns "$vpp_pid"
ip link set lyroute-b-lan netns "$vpp_pid"
ip link set client0 netns "$client"
ip link set client1 netns "$client"
ip link set server0 netns "$server"
ip link set inline-wan netns "$service"
ip link set inline-lan netns "$service"
ip link set inline-b-wan netns "$service"
ip link set inline-b-lan netns "$service"

ip -n "$client" addr add 10.0.0.2/24 dev client0
ip -n "$server" addr add 10.0.0.3/24 dev server0
ip -n "$client" link set client0 up
ip -n "$client" link set client1 up
ip -n "$server" link set server0 up
ip -n "$service" link add service-br type bridge
ip -n "$service" link set service-br up
ip -n "$service" link set inline-wan master service-br
ip -n "$service" link set inline-lan master service-br
ip -n "$service" link set inline-wan up
ip -n "$service" link set inline-lan up
ip -n "$service" link add service-br-b type bridge
ip -n "$service" link set service-br-b up
ip -n "$service" link set inline-b-wan master service-br-b
ip -n "$service" link set inline-b-lan master service-br-b
ip -n "$service" link set inline-b-wan up
ip -n "$service" link set inline-b-lan up

for interface in lyroute-wan0 lyroute-wan1 lyroute-lan0 lyroute-a-wan lyroute-a-lan lyroute-b-wan lyroute-b-lan; do
  nsenter -t "$vpp_pid" -n ip link set "$interface" up
  vppctl create host-interface name "$interface" >/dev/null
  vppctl set interface state "host-$interface" up
  vppctl set interface name "host-$interface" "$interface"
done

configure_boundary() {
  vppctl set ly-route orchestrator candidate clear
  vppctl set ly-route orchestrator candidate boundary wan lyroute-wan0 lan lyroute-lan0
  vppctl set ly-route orchestrator candidate group inline-a wan-facing lyroute-a-wan lan-facing lyroute-a-lan
}

start_service_capture() {
  output=$1
  filter=${2:-'icmp and host 10.0.0.2 and host 10.0.0.3'}
  ip netns exec "$service" tcpdump -l -n -i inline-wan \
    "$filter" >"$output" 2>/dev/null &
  capture_pid=$!
  sleep .3
}

stop_service_capture() {
  kill -INT "$capture_pid" 2>/dev/null || true
  wait "$capture_pid" 2>/dev/null || true
  capture_pid=""
}

wait_ipv6_address_ready() {
  namespace=$1
  interface=$2
  address=$3
  for _ in $(seq 1 50); do
    address_state=$(ip -n "$namespace" -6 -o addr show dev "$interface" 2>/dev/null | awk -v address="$address" '$4 == address { print }')
    if [ -n "$address_state" ] && ! printf '%s\n' "$address_state" | grep -Eq 'tentative|dadfailed'; then
      return 0
    fi
    sleep .1
  done
  echo "IPv6 address $address on $namespace/$interface did not become usable" >&2
  ip -n "$namespace" -6 -d addr show dev "$interface" >&2 || true
  return 1
}

configure_boundary
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation direct-1
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/direct.txt"
start_service_capture "$tmp/direct-service.txt"
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/direct-second.txt"
stop_service_capture
! grep -q ' IP ' "$tmp/direct-service.txt"

configure_boundary
vppctl set ly-route orchestrator candidate rule id via-all group-position 10 sequence 10 action via target inline-a family ip4 src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation via-1
start_service_capture "$tmp/via-service.txt"
ip netns exec "$client" ping -c 5 -W 1 10.0.0.3 >"$tmp/via.txt"
stop_service_capture
grep -q ' IP ' "$tmp/via-service.txt"
vppctl show ly-route orchestrator >"$tmp/via-status.txt"
grep -E 'policy via-all .*packets [1-9][0-9]*' "$tmp/via-status.txt"
grep -E 'flow family ip4 src 10\.0\.0\.2 dst 10\.0\.0\.3 proto 1 .*packets [1-9][0-9]* .*groups inline-a' "$tmp/via-status.txt"

vppctl set interface state lyroute-a-wan down
sleep .3
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/bypass.txt"
vppctl show ly-route orchestrator >"$tmp/bypass-status.txt"
grep -E 'group-health inline-a state bypass bypass-packets [1-9][0-9]*' "$tmp/bypass-status.txt"
vppctl set interface state lyroute-a-wan up
sleep .3
start_service_capture "$tmp/recovery-service.txt"
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/recovery.txt"
stop_service_capture
grep -q ' IP ' "$tmp/recovery-service.txt"
vppctl show ly-route orchestrator >"$tmp/recovery-status.txt"
grep -q 'group-health inline-a state up ' "$tmp/recovery-status.txt"

for namespace in "$client" "$server" "$service"; do
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.all.disable_ipv6=0
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.default.disable_ipv6=0
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.all.accept_dad=0
  ip netns exec "$namespace" sysctl -qw net.ipv6.conf.default.accept_dad=0
done
ip netns exec "$client" sysctl -qw net.ipv6.conf.client0.disable_ipv6=0
ip netns exec "$client" sysctl -qw net.ipv6.conf.client0.accept_dad=0
ip netns exec "$server" sysctl -qw net.ipv6.conf.server0.disable_ipv6=0
ip netns exec "$server" sysctl -qw net.ipv6.conf.server0.accept_dad=0
ip -n "$client" -6 addr add 2001:db8:1::2/64 dev client0
ip -n "$server" -6 addr add 2001:db8:1::3/64 dev server0
wait_ipv6_address_ready "$client" client0 2001:db8:1::2/64
wait_ipv6_address_ready "$server" server0 2001:db8:1::3/64
configure_boundary
vppctl set ly-route orchestrator candidate rule id via-ipv6 group-position 10 sequence 10 action via target inline-a family ip6 src ::/0 dst ::/0 proto 58 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation via-ipv6-1
start_service_capture "$tmp/via-ipv6-service.txt" 'icmp6 and host 2001:db8:1::2 and host 2001:db8:1::3'
ip netns exec "$client" ping6 -c 4 -W 1 2001:db8:1::3 >"$tmp/via-ipv6.txt"
stop_service_capture
grep -q '2001:db8:1::2' "$tmp/via-ipv6-service.txt"
vppctl show ly-route orchestrator >"$tmp/via-ipv6-status.txt"
grep -E 'policy via-ipv6 .*packets [1-9][0-9]*' "$tmp/via-ipv6-status.txt"
grep -E 'flow family ip6 src 2001:db8:1::2 dst 2001:db8:1::3 proto 58 .*groups inline-a' "$tmp/via-ipv6-status.txt"

ip -n "$client" link add link client0 name client0.100 type vlan id 100
ip -n "$server" link add link server0 name server0.100 type vlan id 100
ip -n "$client" addr add 10.100.0.2/24 dev client0.100
ip -n "$server" addr add 10.100.0.3/24 dev server0.100
ip -n "$client" link set client0.100 up
ip -n "$server" link set server0.100 up
configure_boundary
vppctl set ly-route orchestrator candidate rule id via-vlan group-position 10 sequence 10 action via target inline-a family ip4 src 10.100.0.0/24 dst 10.100.0.0/24 proto 1 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation via-vlan-1
start_service_capture "$tmp/via-vlan-service.txt" 'vlan 100 and icmp'
ip netns exec "$client" ping -c 4 -W 1 10.100.0.3 >"$tmp/via-vlan.txt"
stop_service_capture
grep -q '10.100.0.2' "$tmp/via-vlan-service.txt"
vppctl show ly-route orchestrator >"$tmp/via-vlan-status.txt"
grep -E 'policy via-vlan .*packets [1-9][0-9]*' "$tmp/via-vlan-status.txt"
grep -E 'flow family ip4 src 10.100.0.2 dst 10.100.0.3 proto 1 .*groups inline-a' "$tmp/via-vlan-status.txt"

vppctl set ly-route orchestrator candidate clear
vppctl set ly-route orchestrator candidate boundary wan lyroute-wan0 lan lyroute-lan0
vppctl set ly-route orchestrator candidate group inline-a wan-facing lyroute-a-wan lan-facing lyroute-a-lan
vppctl set ly-route orchestrator candidate group inline-b wan-facing lyroute-b-wan lan-facing lyroute-b-lan
vppctl set ly-route orchestrator candidate rule id first-hop group-position 10 sequence 10 action via target inline-a family ip4 src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate rule id second-hop group-position 20 sequence 10 action via target inline-b family ip4 src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation via-two-groups-1
start_service_capture "$tmp/via-two-a-service.txt"
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/via-two-a.txt"
stop_service_capture
grep -q ' IP ' "$tmp/via-two-a-service.txt"
ip netns exec "$service" tcpdump -l -n -i inline-b-wan 'icmp and host 10.0.0.2 and host 10.0.0.3' >"$tmp/via-two-b-service.txt" 2>/dev/null &
capture_pid=$!
sleep .3
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/via-two-b.txt"
stop_service_capture
grep -q ' IP ' "$tmp/via-two-b-service.txt"
vppctl show ly-route orchestrator >"$tmp/via-two-status.txt"
grep -E 'policy first-hop .*packets [1-9][0-9]*' "$tmp/via-two-status.txt"
grep -E 'policy second-hop .*packets [1-9][0-9]*' "$tmp/via-two-status.txt"
grep -E 'flow family ip4 src 10.0.0.2 dst 10.0.0.3 proto 1 .*groups inline-a,inline-b' "$tmp/via-two-status.txt"

configure_boundary
vppctl set ly-route orchestrator candidate rule id drop-all group-position 10 sequence 10 action drop target none family ip4 src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535
vppctl set ly-route orchestrator candidate default direct
vppctl set ly-route orchestrator commit generation drop-1
set +e
ip netns exec "$client" ping -c 2 -W 1 10.0.0.3 >"$tmp/drop.txt" 2>&1
drop_rc=$?
set -e
[ "$drop_rc" -ne 0 ]
vppctl show ly-route orchestrator >"$tmp/drop-status.txt"
grep -E 'policy drop-all .*packets [1-9][0-9]*' "$tmp/drop-status.txt"

# Production closure: save only topology and the whole policy through the
# product API. No per-flow apply endpoint or hidden L3 next hop is supplied.
(cd "$repo_root/backend" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$tmp/ly-route-control" ./cmd/orchestrator-control)
cat >"$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec $container vppctl -s $socket "\$@"
EOF
chmod 0755 "$tmp/vppctl"
observed=$(date -u -d '-1 minute' +%Y-%m-%dT%H:%M:%SZ)
valid=$(date -u -d '+4 minutes' +%Y-%m-%dT%H:%M:%SZ)
cat >"$tmp/capabilities.json" <<EOF
{"management_interface":"mgmt0","proofs":[
{"linux_interface":"wan0","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"$observed","valid_until":"$valid"}},
{"linux_interface":"wan1","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"$observed","valid_until":"$valid"}},
{"linux_interface":"lan0","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"$observed","valid_until":"$valid"}},
{"linux_interface":"a-wan","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"$observed","valid_until":"$valid"}},
{"linux_interface":"a-lan","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"$observed","valid_until":"$valid"}}
]}
EOF
chmod 0600 "$tmp/capabilities.json"
cat >"$tmp/topology.json" <<'EOF'
{"schema_version":1,"management_interface":"mgmt0","interfaces":[{"name":"wan","role":"wan","bond":{"name":"bond-wan","members":["wan0","wan1"]}},{"name":"lan","role":"lan","port":"lan0"}],"orchestration_groups":[{"name":"inline-a","ports":[{"interface":"a-wan","direction":"wan_facing"},{"interface":"a-lan","direction":"lan_facing"}]}]}
EOF
cat >"$tmp/policy.json" <<'EOF'
{"schema_version":1,"ip_objects":[],"policy_groups":[{"id":"security","position":10,"rules":[{"id":"via-all","sequence":10,"match":{"sources":["any"],"destinations":["any"],"protocol":"any"},"action":{"kind":"via","group":"inline-a"}}]}],"default":{"kind":"direct"}}
EOF
# The product API must own the VPP bond lifecycle. Only the Linux peer is
# prepared here; no VPP bond command is issued by the acceptance fixture.
vppctl set ly-route orchestrator disable
ip -n "$client" addr flush dev client0
ip -n "$client" link set client0 down
# AF_PACKET test interfaces do not propagate peer carrier changes into VPP's
# admin/link state, so inject the matching VPP member fault explicitly. Native
# NIC and DPDK drivers report this state directly on target hardware.
vppctl set interface state lyroute-wan0 down
ip -n "$client" link set client1 down
ip -n "$client" link add bond0 type bond mode active-backup miimon 100
ip -n "$client" link set client0 master bond0
ip -n "$client" link set client1 master bond0
ip -n "$client" addr add 10.0.0.2/24 dev bond0
ip -n "$client" link set client0 up
ip -n "$client" link set client1 up
ip -n "$client" link set bond0 up
port=$((20000 + suffix % 1000))
LY_ROUTE_API_HOST=127.0.0.1 LY_ROUTE_API_PORT="$port" \
  LY_ROUTE_DB_PATH="$tmp/orchestrator.db" \
  LY_ROUTE_ADMIN_USERNAME=admin LY_ROUTE_ADMIN_PASSWORD=transparent-secret \
  LY_ROUTE_FORCE_PASSWORD_CHANGE=false LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY=false \
  LY_ROUTE_VPPCTL="$tmp/vppctl" LY_ROUTE_VPP_CAPABILITY_PROOF="$tmp/capabilities.json" \
  LY_ROUTE_ORCHESTRATOR_TRANSACTION_JOURNAL="$tmp/transparent-transaction.json" \
  LY_ROUTE_MANAGEMENT_INTERFACE=mgmt0 LY_ROUTE_ORCHESTRATOR_RECONCILE_INTERVAL=1h \
  "$tmp/ly-route-control" >"$tmp/control.log" 2>&1 &
control_pid=$!
attempts=0
until curl --fail --silent "http://127.0.0.1:$port/api/v1/health" >"$tmp/api-health.json"; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 50 ]; then cat "$tmp/control.log" >&2; exit 1; fi
  sleep .1
done
curl --fail --silent --show-error -c "$tmp/cookies" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"transparent-secret"}' \
  "http://127.0.0.1:$port/api/v1/auth/login" >"$tmp/api-login.json"
topology_status=$(curl --silent --show-error -o "$tmp/api-topology.json" -w '%{http_code}' -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT \
  --data-binary @"$tmp/topology.json" "http://127.0.0.1:$port/api/v1/orchestrator/topology")
[ "$topology_status" = 200 ] || { cat "$tmp/api-topology.json" >&2; exit 1; }
[ ! -e "$tmp/transparent-transaction.json" ]
vppctl show bond details >"$tmp/api-bond-readback.txt"
grep -q 'lyroute-wan0' "$tmp/api-bond-readback.txt"
grep -q 'lyroute-wan1' "$tmp/api-bond-readback.txt"
start_service_capture "$tmp/api-direct-service.txt"
if ! ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/api-direct.txt"; then
  vppctl show ly-route orchestrator >"$tmp/api-direct-failure-orchestrator.txt" || true
  vppctl show interface >"$tmp/api-direct-failure-interfaces.txt" || true
  vppctl show bond details >"$tmp/api-direct-failure-bond.txt" || true
  ip -n "$client" -d link show >"$tmp/api-direct-failure-client-links.txt" || true
  ip -n "$client" neigh show >"$tmp/api-direct-failure-client-neighbors.txt" || true
  exit 1
fi
stop_service_capture
! grep -q ' IP ' "$tmp/api-direct-service.txt"
# Both peers use active-backup. Dropping the active member must preserve the
# transparent direct path through the remaining member without API changes.
ip -n "$client" link set client0 down
sleep .4
set +e
ip netns exec "$client" ping -c 4 -W 1 10.0.0.3 >"$tmp/api-bond-member-failover.txt"
bond_failover_rc=$?
set -e
vppctl show bond details >"$tmp/api-bond-member-failover-readback.txt"
vppctl show interface >"$tmp/api-bond-member-failover-interfaces.txt"
ip -n "$client" -d link show >"$tmp/api-bond-member-failover-client-links.txt"
[ "$bond_failover_rc" -eq 0 ]
ip -n "$client" link set client0 up
vppctl set interface state lyroute-wan0 up
sleep .4
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/api-bond-member-recovery.txt"
policy_status=$(curl --silent --show-error -o "$tmp/api-policy.json" -w '%{http_code}' -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT \
  --data-binary @"$tmp/policy.json" "http://127.0.0.1:$port/api/v1/orchestrator/policy")
[ "$policy_status" = 200 ] || { cat "$tmp/api-policy.json" >&2; exit 1; }
[ ! -e "$tmp/transparent-transaction.json" ]
start_service_capture "$tmp/api-via-service.txt"
ip netns exec "$client" ping -c 5 -W 1 10.0.0.3 >"$tmp/api-via.txt"
stop_service_capture
grep -q ' IP ' "$tmp/api-via-service.txt"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/policy-hits" >"$tmp/api-policy-hits.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/top-sessions" >"$tmp/api-top-connections.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/dashboard" >"$tmp/api-dashboard.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/online-users" >"$tmp/api-online-users.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/traffic-trend?window=24h&points=288" >"$tmp/api-traffic-trend.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/runtime/status" >"$tmp/api-runtime-status.json"
vppctl show ly-route orchestrator >"$tmp/api-vpp-readback.txt"
python3 - "$tmp/api-policy-hits.json" "$tmp/api-top-connections.json" "$tmp/api-dashboard.json" "$tmp/api-online-users.json" "$tmp/api-traffic-trend.json" "$tmp/api-runtime-status.json" <<'PY'
import json,sys
hits,connections,dashboard,users,trend,runtime=(json.load(open(path)) for path in sys.argv[1:])
assert any(item['policy_id']=='via-all' and item['hits']>0 for item in hits['data']), hits
assert any(cap['name']=='orchestrator_policy_telemetry' and cap['available'] is True for cap in hits['capabilities']), hits
items=connections['data']['items']
assert connections['data']['state']=='available' and connections['data']['degraded'] is False, connections
assert any(item['source_ip']=='10.0.0.2' and item['destination_ip']=='10.0.0.3' and item['protocol']=='icmp' and item['bytes']>0 and item['groups']==['inline-a'] for item in items), connections
data=dashboard['data']
assert data['device_mode']=='orchestrator' and data['active_path']=='vpp' and data['degraded'] is False, dashboard
assert data['sessions']>0 and data['policy_hits']>0, dashboard
assert any(group['name']=='inline-a' and group['state']=='available' and group['bypass'] is False for group in data['orchestration_groups']), dashboard
assert users['data']['state']=='available' and users['data']['degraded'] is False and isinstance(users['data']['items'],list), users
assert trend['state']=='available' and trend['degraded'] is False, trend
assert any(series['id']=='inline-a' for series in trend['series']['logical_egresses']), trend
assert runtime['status'] in ('running','degraded') and isinstance(runtime['components'],list), runtime
PY
# Runtime status must report real drift instead of retaining a stale green
# receipt, and a whole-policy reapply must restore a fresh generation.
vppctl set ly-route orchestrator disable
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/runtime/status" >"$tmp/api-runtime-drift.json"
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/telemetry/dashboard" >"$tmp/api-dashboard-drift.json"
python3 - "$tmp/api-runtime-drift.json" "$tmp/api-dashboard-drift.json" <<'PY'
import json,sys
runtime,dashboard=(json.load(open(path)) for path in sys.argv[1:])
assert runtime['status']=='degraded', runtime
assert any(item['name']=='vpp' and item['state']=='degraded' and item['available'] is False for item in runtime['components']), runtime
assert dashboard['data']['runtime_state']=='degraded' and dashboard['data']['degraded'] is True, dashboard
PY
reconcile_status=$(curl --silent --show-error -o "$tmp/api-policy-reconcile.json" -w '%{http_code}' -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT \
  --data-binary @"$tmp/policy.json" "http://127.0.0.1:$port/api/v1/orchestrator/policy")
[ "$reconcile_status" = 200 ] || { cat "$tmp/api-policy-reconcile.json" >&2; exit 1; }
curl --fail --silent --show-error -b "$tmp/cookies" "http://127.0.0.1:$port/api/v1/runtime/status" >"$tmp/api-runtime-recovered.json"
python3 - "$tmp/api-runtime-recovered.json" <<'PY'
import json,sys
runtime=json.load(open(sys.argv[1]))
assert runtime['status']=='running', runtime
assert all(item['state']=='running' and item['fresh'] is True for item in runtime['components']), runtime
PY
kill "$control_pid"
wait "$control_pid" || true
control_pid=""

# A persisted configuration must restore the dataplane after VPP/control-plane
# restart. Locking the plugin first proves this is an actual replay.
vppctl set ly-route orchestrator disable
vppctl show ly-route orchestrator >"$tmp/api-before-restart.txt"
grep -q 'state locked' "$tmp/api-before-restart.txt"
# Simulate a crash after the database commit but before journal cleanup. Startup
# must consume the durable bond intent, reconcile it with persisted topology and
# remove the journal only after live semantic readback succeeds.
cat >"$tmp/transparent-transaction.json" <<'EOF'
{"version":1,"transaction_id":"simulated-crash-after-database-commit","operation":"apply","generation":"simulated","desired_bonds":[{"name":"lyroute-bond-wan","mode":"active-backup","members":["lyroute-wan0","lyroute-wan1"]}],"started_at":"2026-08-01T00:00:00Z"}
EOF
chmod 0600 "$tmp/transparent-transaction.json"
cp "$tmp/transparent-transaction.json" "$tmp/api-journal-before-restart.json"
LY_ROUTE_API_HOST=127.0.0.1 LY_ROUTE_API_PORT="$port" \
  LY_ROUTE_DB_PATH="$tmp/orchestrator.db" \
  LY_ROUTE_ADMIN_USERNAME=admin LY_ROUTE_ADMIN_PASSWORD=transparent-secret \
  LY_ROUTE_FORCE_PASSWORD_CHANGE=false LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY=false \
  LY_ROUTE_VPPCTL="$tmp/vppctl" LY_ROUTE_VPP_CAPABILITY_PROOF="$tmp/capabilities.json" \
  LY_ROUTE_ORCHESTRATOR_TRANSACTION_JOURNAL="$tmp/transparent-transaction.json" \
  LY_ROUTE_MANAGEMENT_INTERFACE=mgmt0 LY_ROUTE_ORCHESTRATOR_RECONCILE_INTERVAL=1h \
  "$tmp/ly-route-control" >"$tmp/control-restart.log" 2>&1 &
control_pid=$!
attempts=0
until curl --fail --silent "http://127.0.0.1:$port/api/v1/health" >"$tmp/api-restart-health.json"; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 50 ]; then cat "$tmp/control-restart.log" >&2; exit 1; fi
  sleep .1
done
[ ! -e "$tmp/transparent-transaction.json" ]
printf 'journal cleared after persisted topology reconciliation\n' >"$tmp/api-journal-cleared.txt"
vppctl show ly-route orchestrator >"$tmp/api-after-restart.txt"
grep -q 'state running' "$tmp/api-after-restart.txt"
grep -q 'policy via-all ' "$tmp/api-after-restart.txt"
vppctl show bond details >"$tmp/api-bond-after-restart.txt"
grep -q 'lyroute-wan0' "$tmp/api-bond-after-restart.txt"
grep -q 'lyroute-wan1' "$tmp/api-bond-after-restart.txt"
curl --fail --silent --show-error -c "$tmp/cookies-restart" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"transparent-secret"}' \
  "http://127.0.0.1:$port/api/v1/auth/login" >"$tmp/api-restart-login.json"
start_service_capture "$tmp/api-restart-via-service.txt"
ip netns exec "$client" ping -c 3 -W 1 10.0.0.3 >"$tmp/api-restart-via.txt"
stop_service_capture
grep -q ' IP ' "$tmp/api-restart-via-service.txt"

delete_policy_status=$(curl --silent --show-error -o "$tmp/api-delete-policy.json" -w '%{http_code}' -b "$tmp/cookies-restart" -X DELETE \
  "http://127.0.0.1:$port/api/v1/orchestrator/policy")
[ "$delete_policy_status" = 204 ] || { cat "$tmp/api-delete-policy.json" >&2; exit 1; }
delete_topology_status=$(curl --silent --show-error -o "$tmp/api-delete-topology.json" -w '%{http_code}' -b "$tmp/cookies-restart" -X DELETE \
  "http://127.0.0.1:$port/api/v1/orchestrator/topology")
[ "$delete_topology_status" = 204 ] || { cat "$tmp/api-delete-topology.json" >&2; exit 1; }
vppctl show ly-route orchestrator >"$tmp/api-after-delete.txt"
grep -q 'state locked' "$tmp/api-after-delete.txt"
vppctl show bond details >"$tmp/api-bond-after-delete.txt"
! grep -q 'lyroute-wan0' "$tmp/api-bond-after-delete.txt"
set +e
ip netns exec "$client" ping -c 2 -W 1 10.0.0.3 >"$tmp/api-after-delete-ping.txt" 2>&1
after_delete_rc=$?
set -e
[ "$after_delete_rc" -ne 0 ]
kill "$control_pid"
wait "$control_pid" || true
control_pid=""

canonical="$repo_root/.sisyphus/full-acceptance/evidence/o-transparent"
rm -rf "$canonical"
mkdir -p "$canonical"
for evidence in \
  direct.txt via.txt via-service.txt bypass.txt bypass-status.txt recovery.txt recovery-status.txt \
  via-ipv6.txt via-ipv6-service.txt via-vlan.txt via-vlan-service.txt via-two-a-service.txt via-two-b-service.txt via-two-status.txt drop.txt drop-status.txt \
  api-topology.json api-policy.json api-bond-readback.txt api-bond-member-failover.txt api-bond-member-failover-readback.txt \
  api-bond-member-recovery.txt api-policy-hits.json api-top-connections.json api-dashboard.json api-online-users.json \
  api-traffic-trend.json api-runtime-status.json api-runtime-drift.json api-dashboard-drift.json api-policy-reconcile.json api-runtime-recovered.json api-vpp-readback.txt api-after-restart.txt api-bond-after-restart.txt \
  api-restart-via.txt api-restart-via-service.txt api-journal-before-restart.json api-journal-cleared.txt api-after-delete.txt api-bond-after-delete.txt; do
  cp "$tmp/$evidence" "$canonical/$evidence"
done
sha256sum "$tmp/ly-route-control" >"$canonical/control-binary.sha256"
git -C "$repo_root" rev-parse HEAD >"$canonical/commit.txt"
printf 'transparent orchestrator VPP plugin verification passed: %s\n' "$tmp"
