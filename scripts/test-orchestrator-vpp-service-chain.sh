#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=$$
container="ly-route-vpp-chain-$suffix"
client="lrc-$suffix"
server="lrs-$suffix"
service="lri-$suffix"
socket="/run/vpp/chain-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-service-chain-$suffix"
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-service-chain}
completed=false
control_pid=""

cleanup() {
	set +e
	[ -z "$control_pid" ] || kill "$control_pid" 2>/dev/null
	[ -z "$control_pid" ] || wait "$control_pid" 2>/dev/null
	ip netns del "$client" 2>/dev/null
	ip netns del "$server" 2>/dev/null
	ip netns del "$service" 2>/dev/null
	docker rm -f "$container" >/dev/null 2>&1
	rm -f "$tmp/vppctl"
	[ "$completed" != true ] || rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

rm -rf "$tmp"
mkdir -p "$tmp"
docker run -d --name "$container" --privileged --network none --shm-size 256m \
	ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" api-segment "{ prefix chain$suffix }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do
	vppctl show version >/dev/null 2>&1 && break
	sleep .25
done
vppctl show version >"$tmp/version.txt"

ip netns add "$client"
ip netns add "$server"
ip netns add "$service"
ip link add lyroute-wan0 type veth peer name client0
ip link add lyroute-lan0 type veth peer name server0
ip link add lyroute-a-wan type veth peer name inline-wan
ip link add lyroute-a-lan type veth peer name inline-lan
ip link set lyroute-wan0 netns "$vpp_pid"
ip link set lyroute-lan0 netns "$vpp_pid"
ip link set lyroute-a-wan netns "$vpp_pid"
ip link set lyroute-a-lan netns "$vpp_pid"
ip link set client0 netns "$client"
ip link set server0 netns "$server"
ip link set inline-wan netns "$service"
ip link set inline-lan netns "$service"

ip -n "$client" addr add 10.0.0.2/24 dev client0
ip -n "$client" link set client0 up
ip -n "$client" route add default via 10.0.0.1
ip -n "$server" addr add 10.0.1.2/24 dev server0
ip -n "$server" link set server0 up
ip -n "$server" route add default via 10.0.1.1
ip -n "$service" addr add 198.18.1.2/24 dev inline-wan
ip -n "$service" addr add 198.18.2.2/24 dev inline-lan
ip -n "$service" link set inline-wan up
ip -n "$service" link set inline-lan up
ip netns exec "$service" sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$service" sysctl -qw net.ipv4.conf.all.rp_filter=0
ip netns exec "$service" sysctl -qw net.ipv4.conf.inline-wan.rp_filter=0
ip netns exec "$service" sysctl -qw net.ipv4.conf.inline-lan.rp_filter=0
ip -n "$service" route add 10.0.0.0/24 via 198.18.1.1
ip -n "$service" route add 10.0.1.0/24 via 198.18.2.1

for interface in lyroute-wan0 lyroute-lan0 lyroute-a-wan lyroute-a-lan; do
	nsenter -t "$vpp_pid" -n ip link set "$interface" up
	vppctl create host-interface name "$interface" >/dev/null
	vppctl set interface state "host-$interface" up
	vppctl set interface name "host-$interface" "$interface"
done
vppctl set interface ip address lyroute-wan0 10.0.0.1/24
vppctl set interface ip address lyroute-lan0 10.0.1.1/24
vppctl set interface ip address lyroute-a-wan 198.18.1.1/24
vppctl set interface ip address lyroute-a-lan 198.18.2.1/24

ip netns exec "$client" ping -c 2 -W 1 10.0.1.2 >"$tmp/direct-before.txt"
cat >"$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec $container vppctl -s $socket "\$@"
EOF
chmod 0755 "$tmp/vppctl"
run_lifecycle() {
	(cd "$repo_root/backend" && LY_ROUTE_VPPCTL_INTEGRATION_BINARY="$tmp/vppctl" LY_ROUTE_SERVICE_CHAIN_ACTION="$1" \
		go test ./internal/runtime/vpp -run TestVPPCTLServiceChainLifecycleIntegration -count=1)
}
run_lifecycle apply
before=$(ip -n "$service" -s link show inline-wan | awk '/RX:/{getline; print $1; exit}')
ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/through-service.txt"
after=$(ip -n "$service" -s link show inline-wan | awk '/RX:/{getline; print $1; exit}')
[ "$after" -gt "$before" ] || { echo "service node did not receive chained traffic" >&2; exit 1; }

ip -n "$service" link set inline-wan down
ip -n "$service" link set inline-lan down
run_lifecycle bypass
ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/bypass.txt"
vppctl show abf policy >"$tmp/abf-after-bypass.txt"
vppctl show acl-plugin acl >"$tmp/acl-after-bypass.txt"
! grep -q 'integration-chain' "$tmp/acl-after-bypass.txt"
! grep -q 'abf:' "$tmp/abf-after-bypass.txt"

ip -n "$service" link set inline-wan up
ip -n "$service" link set inline-lan up
vppctl set interface state lyroute-a-wan up
vppctl set interface state lyroute-a-lan up
ip netns exec "$service" sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$service" sysctl -qw net.ipv4.conf.all.rp_filter=0
ip netns exec "$service" sysctl -qw net.ipv4.conf.inline-wan.rp_filter=0
ip netns exec "$service" sysctl -qw net.ipv4.conf.inline-lan.rp_filter=0
ip -n "$service" route replace 10.0.0.0/24 via 198.18.1.1
ip -n "$service" route replace 10.0.1.0/24 via 198.18.2.1
ip netns exec "$service" ip neigh flush all >/dev/null 2>&1 || true
for _ in 1 2 3; do
	ip netns exec "$service" ping -c 1 -W 1 198.18.1.1 >/dev/null
	ip netns exec "$service" ping -c 1 -W 1 198.18.2.1 >/dev/null
done
run_lifecycle apply
vppctl show interface lyroute-a-wan >"$tmp/vpp-recovered-wan-interface.txt"
vppctl show interface lyroute-a-lan >"$tmp/vpp-recovered-lan-interface.txt"
before=$(ip -n "$service" -s link show inline-lan | awk '/RX:/{getline; print $1; exit}')
vppctl clear trace
vppctl trace add af-packet-input 32
set +e
ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/recovered.txt"
recovered_rc=$?
set -e
vppctl show abf policy >"$tmp/abf-recovered.txt"
vppctl show acl-plugin acl >"$tmp/acl-recovered.txt"
vppctl show abf attach lyroute-wan0 >"$tmp/abf-recovered-wan-attach.txt"
vppctl show abf attach lyroute-lan0 >"$tmp/abf-recovered-lan-attach.txt"
vppctl show interface lyroute-a-wan >"$tmp/vpp-recovered-wan-interface.txt"
vppctl show interface lyroute-a-lan >"$tmp/vpp-recovered-lan-interface.txt"
vppctl show ip fib >"$tmp/vpp-recovered-fib.txt"
vppctl show ip neighbors >"$tmp/vpp-recovered-neighbors.txt"
vppctl show trace >"$tmp/vpp-recovered-trace.txt"
ip -n "$service" -s link show >"$tmp/service-recovered-links.txt"
ip netns exec "$service" ip route show >"$tmp/service-recovered-routes.txt"
ip netns exec "$service" ip neigh show >"$tmp/service-recovered-neighbors.txt"
ip netns exec "$service" sysctl net.ipv4.ip_forward net.ipv4.conf.all.rp_filter >"$tmp/service-recovered-sysctl.txt"
[ "$recovered_rc" -eq 0 ] || { echo "recovered packet flow failed; see $tmp" >&2; exit 1; }
after=$(ip -n "$service" -s link show inline-lan | awk '/RX:/{getline; print $1; exit}')
[ "$after" -gt "$before" ] || { echo "recovered service node did not receive chained traffic" >&2; exit 1; }
grep -q 'ly-route-integration_chain' "$tmp/acl-recovered.txt"

# Apply a real VPP policer on the same WAN ingress while the service chain is
# active. This proves traffic control precedes chain traversal without adding
# Gateway routing, NAT, or DNS semantics to the Orchestrator.
vppctl policer add name lyroute-orchestrator-limit type 1r2c cir 100 cb 12500 rate kbps conform-action transmit exceed-action drop violate-action drop
vppctl policer input name lyroute-orchestrator-limit lyroute-wan0
service_before_rate=$(ip -n "$service" -s link show inline-wan | awk '/RX:/{getline; print $1; exit}')
set +e
ip netns exec "$client" ping -f -c 500 -s 1000 -W 1 10.0.1.2 >"$tmp/orchestrator-policer-ping.txt" 2>&1
rate_ping_rc=$?
set -e
[ "$rate_ping_rc" -eq 0 ] || { echo "policed ICMP flow did not reach LAN through the service chain" >&2; exit 1; }
rate_received=$(sed -n 's/.*packets transmitted, \([0-9][0-9]*\) received.*/\1/p' "$tmp/orchestrator-policer-ping.txt" | tail -1)
[ -n "$rate_received" ] || { echo "could not parse policed ICMP receive count" >&2; exit 1; }
[ "$rate_received" -gt 0 ] && [ "$rate_received" -lt 500 ] || { echo "orchestrator VPP policer result is invalid: received=$rate_received" >&2; exit 1; }
service_after_rate=$(ip -n "$service" -s link show inline-wan | awk '/RX:/{getline; print $1; exit}')
[ "$service_after_rate" -gt "$service_before_rate" ] || { echo "policed traffic did not traverse the active service chain" >&2; exit 1; }
vppctl show policer name lyroute-orchestrator-limit >"$tmp/orchestrator-policer.txt"
grep -E '(exceed|violate) [1-9][0-9]* packets' "$tmp/orchestrator-policer.txt" >/dev/null
grep -E 'conform [1-9][0-9]* packets' "$tmp/orchestrator-policer.txt" >/dev/null
printf 'received=%s sent=500 service-rx-before=%s service-rx-after=%s\n' "$rate_received" "$service_before_rate" "$service_after_rate" >"$tmp/orchestrator-policer-packet.txt"

# Exercise the production Orchestrator binary against the same live VPP
# instance. The packets above become the source of API counters; no fixture or
# Gateway DHCP/NAT/DNS collector participates.
(cd "$repo_root/backend" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$tmp/ly-route-control" ./cmd/orchestrator-control)
port=$((19000 + suffix % 1000))
cat >"$tmp/topology.json" <<'EOF'
{"schema_version":1,"management_interface":"mgmt0","management_shared":false,"interfaces":[{"name":"wan","role":"wan","port":"wan0"},{"name":"lan","role":"lan","port":"lan0"}],"orchestration_groups":[{"name":"inline-a","ports":[{"interface":"a-wan","direction":"wan_facing"},{"interface":"a-lan","direction":"lan_facing"}]}]}
EOF
LY_ROUTE_API_HOST=127.0.0.1 LY_ROUTE_API_PORT="$port" \
	LY_ROUTE_DB_PATH="$tmp/orchestrator.db" \
	LY_ROUTE_ADMIN_USERNAME=admin LY_ROUTE_ADMIN_PASSWORD=telemetry-secret \
	LY_ROUTE_FORCE_PASSWORD_CHANGE=false LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY=false \
	LY_ROUTE_VPPCTL="$tmp/vppctl" LY_ROUTE_ORCHESTRATOR_RECONCILE_INTERVAL=1h \
	"$tmp/ly-route-control" >"$tmp/control.log" 2>&1 &
control_pid=$!
attempts=0
until curl --fail --silent "http://127.0.0.1:$port/api/v1/health" >"$tmp/telemetry-health.json"; do
	attempts=$((attempts + 1))
	if [ "$attempts" -ge 50 ]; then cat "$tmp/control.log" >&2; exit 1; fi
	sleep .1
done
curl --fail --silent --show-error -c "$tmp/cookies" -H 'Content-Type: application/json' \
	-d '{"username":"admin","password":"telemetry-secret"}' \
	"http://127.0.0.1:$port/api/v1/auth/login" >"$tmp/telemetry-login.json"
curl --fail --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT \
	--data-binary @"$tmp/topology.json" \
	"http://127.0.0.1:$port/api/v1/orchestrator/topology" >"$tmp/telemetry-topology.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/dashboard" >"$tmp/telemetry-dashboard-baseline.json"
sleep 1
ip netns exec "$client" ping -c 8 -W 1 10.0.1.2 >"$tmp/telemetry-packet-flow.txt"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/dashboard" >"$tmp/telemetry-dashboard.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/runtime/status" >"$tmp/telemetry-runtime-status.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/online-users" >"$tmp/telemetry-online-users.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/top-sessions" >"$tmp/telemetry-top-connections.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/policy-hits" >"$tmp/telemetry-policy-hits.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
	"http://127.0.0.1:$port/api/v1/telemetry/traffic-trend?window=24h&points=288" >"$tmp/telemetry-trend.json"
python3 - "$tmp/telemetry-dashboard.json" "$tmp/telemetry-online-users.json" "$tmp/telemetry-top-connections.json" "$tmp/telemetry-policy-hits.json" "$tmp/telemetry-trend.json" "$tmp/telemetry-runtime-status.json" <<'PY'
import json,sys
dashboard,users,connections,hits,trend,runtime=(json.load(open(path)) for path in sys.argv[1:])
data=dashboard['data']
assert data['device_mode']=='orchestrator'
assert not any(cap['name']=='runtime_status' and cap['available'] is False for cap in dashboard['capabilities'])
group=next(item for item in data['orchestration_groups'] if item['name']=='inline-a')
assert group['state']=='available'
assert group['additive'] is False
assert group['wan_to_lan']['bytes']>0 and group['lan_to_wan']['bytes']>0
assert group['wan_to_lan']['rate_state']=='available'
assert group['lan_to_wan']['rate_state']=='available'
assert any(item['ip'] in ('10.0.0.2','10.0.1.2') for item in users['data']['items'])
assert users['data']['state']=='available' and users['data']['degraded'] is False
assert connections['data']['state']=='unavailable' and connections['data']['degraded'] is True
assert 'VPP native flow observation is not configured' in connections['data']['degraded_reason']
assert hits['data']==[]
assert any(cap['name']=='orchestrator_policy_telemetry' and cap['available'] is False for cap in hits['capabilities'])
series=trend['series']['logical_egresses']
inline=next(item for item in series if item['id']=='inline-a')
assert inline['kind']=='orchestration_group' and len(inline['samples'])>=2
assert any(sample.get('download_bps',0)>0 or sample.get('upload_bps',0)>0 for sample in inline['samples'])
runtime_text=json.dumps(runtime,sort_keys=True).lower()
for forbidden in ('proxy_egress','smartdns','kea','xray','pppoe','pppd','nftables','linux_routing'):
    assert forbidden not in runtime_text, forbidden
PY
sha256sum "$tmp/ly-route-control" >"$tmp/telemetry-binary.sha256.txt"
kill "$control_pid"
wait "$control_pid" || true
control_pid=""

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
for evidence in "$tmp"/*.txt; do
	sed -i 's/\r$//; s/[[:space:]]*$//' "$evidence"
	sed -i -e ':trim' -e '/^[[:space:]]*$/{ $d; N; b trim; }' "$evidence"
done
cp "$tmp"/*.txt "$evidence_dir/"
cp "$tmp"/telemetry-*.json "$evidence_dir/"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
completed=true
printf 'orchestrator real VPP service-chain/bypass/recovery verification passed: %s\n' "$evidence_dir"
trap - EXIT INT TERM
cleanup
