#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=$$
container="ly-route-vpp-obond-$suffix"
client="lrob-c-$suffix"
server="lrob-s-$suffix"
socket="/run/vpp/obond-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-bond-policy-$suffix"
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-bond-policy}
completed=false

cleanup() {
	set +e
	ip netns del "$client" 2>/dev/null
	ip netns del "$server" 2>/dev/null
	docker rm -f "$container" >/dev/null 2>&1
	rm -f "$tmp/vppctl"
	[ "$completed" != true ] || rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

rm -rf "$tmp"
mkdir -p "$tmp"
docker run -d --name "$container" --privileged --network none --shm-size 256m \
	ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" api-segment "{ prefix obond$suffix }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do vppctl show version >/dev/null 2>&1 && break; sleep .25; done
vppctl show version >"$tmp/version.txt"

ip netns add "$client"
ip netns add "$server"
ip link add lyroute-wan0 type veth peer name client0
ip link add lyroute-wan1 type veth peer name client1
ip link add lyroute-lan0 type veth peer name server0
ip link set lyroute-wan0 netns "$vpp_pid"
ip link set lyroute-wan1 netns "$vpp_pid"
ip link set lyroute-lan0 netns "$vpp_pid"
ip link set client0 netns "$client"
ip link set client1 netns "$client"
ip link set server0 netns "$server"

ip -n "$client" link add bond0 type bond mode active-backup miimon 100
ip -n "$client" link set client0 master bond0
ip -n "$client" link set client1 master bond0
ip -n "$client" link set client0 up
ip -n "$client" link set client1 up
ip -n "$client" addr add 10.0.0.2/24 dev bond0
ip -n "$client" link set bond0 up
ip -n "$client" route add default via 10.0.0.1
ip -n "$server" addr add 10.0.1.2/24 dev server0
ip -n "$server" link set server0 up
ip -n "$server" route add default via 10.0.1.1

for interface in lyroute-wan0 lyroute-wan1 lyroute-lan0; do
	nsenter -t "$vpp_pid" -n ip link set "$interface" up
	vppctl create host-interface name "$interface" >/dev/null
	vppctl set interface state "host-$interface" up
	vppctl set interface name "host-$interface" "$interface"
done
vppctl create bond mode active-backup id 77
vppctl set interface name BondEthernet77 lyroute-bond0
vppctl bond add lyroute-bond0 lyroute-wan0
vppctl bond add lyroute-bond0 lyroute-wan1
vppctl set interface state lyroute-bond0 up
vppctl set interface ip address lyroute-bond0 10.0.0.1/24
vppctl set interface ip address lyroute-lan0 10.0.1.1/24
vppctl show bond details >"$tmp/bond-details.txt"
grep -q 'BondEthernet77' "$tmp/bond-details.txt"
grep -q 'lyroute-wan0' "$tmp/bond-details.txt"
grep -q 'lyroute-wan1' "$tmp/bond-details.txt"

ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/direct-before.txt"
cat >"$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec $container vppctl -s $socket "\$@"
EOF
chmod 0755 "$tmp/vppctl"

run_acl_lifecycle() {
	(cd "$repo_root/backend" && LY_ROUTE_LAN_INTERFACE=bond0 LY_ROUTE_VPPCTL_INTEGRATION_BINARY="$tmp/vppctl" LY_ROUTE_SECURITY_ACL_ACTION="$1" \
		go test ./internal/runtime/vpp -run TestVPPCTLSecurityACLLifecycleIntegration -count=1)
}
run_acl_lifecycle apply
vppctl show acl-plugin acl >"$tmp/acl-after-apply.txt"
vppctl show acl-plugin interface >"$tmp/acl-interface-after-apply.txt"
grep -q 'ly-route-integration_deny' "$tmp/acl-after-apply.txt"
set +e
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/denied.txt"
denied_rc=$?
set -e
[ "$denied_rc" -ne 0 ] || { echo 'bond ingress security policy did not drop matching traffic' >&2; exit 1; }

run_acl_lifecycle delete
ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/direct-after-delete.txt"

vppctl policer add name lyroute-orchestrator-bond-limit type 1r2c cir 1000 cb 125000 rate kbps conform-action transmit exceed-action drop violate-action drop
# The return direction exits through the logical bond, so this exercises a
# bond-owned traffic-control attachment without depending on member selection.
vppctl policer output name lyroute-orchestrator-bond-limit lyroute-bond0
set +e
ip netns exec "$client" ping -i 0.005 -c 500 -s 1000 -W 1 10.0.1.2 >"$tmp/policer-ping.txt" 2>&1
policer_rc=$?
set -e
[ "$policer_rc" -eq 0 ] || { echo 'policed bond traffic did not reach physical LAN' >&2; exit 1; }
received=$(sed -n 's/.*packets transmitted, \([0-9][0-9]*\) received.*/\1/p' "$tmp/policer-ping.txt" | tail -1)
[ -n "$received" ] && [ "$received" -gt 0 ] && [ "$received" -lt 500 ] || { echo "invalid bond policer packet count: $received" >&2; exit 1; }
vppctl show policer name lyroute-orchestrator-bond-limit >"$tmp/policer.txt"
grep -E '(exceed|violate) [1-9][0-9]* packets' "$tmp/policer.txt" >/dev/null
grep -E 'conform [1-9][0-9]* packets' "$tmp/policer.txt" >/dev/null
vppctl show interface lyroute-bond0 >"$tmp/bond-interface-counters.txt"
vppctl show interface lyroute-lan0 >"$tmp/lan-interface-counters.txt"
grep -E 'rx packets +[1-9][0-9]*' "$tmp/bond-interface-counters.txt" >/dev/null
grep -E 'tx packets +[1-9][0-9]*' "$tmp/lan-interface-counters.txt" >/dev/null

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
for evidence in "$tmp"/*.txt; do
	sed -i 's/\r$//; s/[[:space:]]*$//' "$evidence"
done
cp "$tmp"/*.txt "$evidence_dir/"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
completed=true
printf 'orchestrator VPP bond direct/drop/security/traffic-control packet verification passed: %s\n' "$evidence_dir"
trap - EXIT INT TERM
cleanup
