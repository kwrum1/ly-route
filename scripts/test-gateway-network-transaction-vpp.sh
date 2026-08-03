#!/bin/sh
set -eu

command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="ly-route-gateway-network-$suffix"
client_ns="lyroute-gn-client-$suffix"
wan_ns="lyroute-gn-wan-$suffix"
socket="/run/vpp/gateway-network-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/gateway-network-transaction-$suffix"
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/gateway-network-transaction}
fault_marker="$tmp/inject-route-failure"
completed=false

cleanup() {
	(
		set +e
		[ -z "${server_pid:-}" ] || kill "$server_pid" >/dev/null 2>&1
		ip netns del "$client_ns" >/dev/null 2>&1
		ip netns del "$wan_ns" >/dev/null 2>&1
		docker rm -f "$container" >/dev/null 2>&1
		[ "$completed" != true ] || rm -rf "$tmp"
	)
}
trap cleanup EXIT INT TERM
cleanup
rm -rf "$tmp"
mkdir -p "$tmp"

docker run -d --name "$container" --privileged --network none --shm-size 256m \
	ly-route/vpp-test:25.10 /usr/bin/vpp \
	unix "{ nodaemon cli-listen $socket }" api-segment "{ prefix gatewaynetwork$suffix }" \
	plugins "{ plugin dpdk_plugin.so { disable } plugin linux_cp_plugin.so { enable } plugin linux_nl_plugin.so { enable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 60); do
	vppctl show version >/dev/null 2>&1 && break
	sleep .2
done
vppctl show version >"$tmp/version.txt"

ip netns add "$client_ns"
ip netns add "$wan_ns"
for pair in 'lyroute-lan0 gn-client0 client' 'lyroute-wan0 gn-wan0 wan' 'lyroute-bm0 gn-bm0 host' 'lyroute-bm1 gn-bm1 host' 'lyroute-bm2 gn-bm2 host' 'lyroute-bm3 gn-bm3 host' 'lyroute-bm4 gn-bm4 host' 'lyroute-bm5 gn-bm5 host'; do
	set -- $pair
	vpp_side=$1
	peer=$2
	location=$3
	ip link add "$vpp_side" type veth peer name "$peer"
	ip link set "$vpp_side" netns "$vpp_pid"
	case "$location" in
		client) ip link set "$peer" netns "$client_ns" ;;
		wan) ip link set "$peer" netns "$wan_ns" ;;
		host) ip link set "$peer" up ;;
	esac
	nsenter -t "$vpp_pid" -n ip link set "$vpp_side" up
	vppctl create host-interface name "$vpp_side" >/dev/null
	vppctl set interface state "host-$vpp_side" up
	vppctl set interface name "host-$vpp_side" "$vpp_side"
done

ip -n "$client_ns" link set lo up
ip -n "$client_ns" link set gn-client0 up
ip -n "$client_ns" addr add 10.0.0.2/24 dev gn-client0
ip -n "$client_ns" route add default via 10.0.0.1
ip -n "$wan_ns" link set lo up
ip -n "$wan_ns" link set gn-wan0 up
ip -n "$wan_ns" addr add 10.0.1.2/24 dev gn-wan0
ip -n "$wan_ns" route add default via 10.0.1.1

cat >"$tmp/vppctl" <<EOF
#!/bin/sh
if [ -f '$fault_marker' ]; then
	case "\$*" in
		*'bond add lyroute-fault-bond2'*) echo 'injected bond member apply failure' >&2; exit 42 ;;
	esac
fi
exec docker exec '$container' vppctl -s '$socket' "\$@"
EOF
chmod 0755 "$tmp/vppctl"

# Create the shared-LAN Linux control-plane pair before the production
# transaction assigns the LAN address so netlink observes both apply and
# rollback address transitions.
vppctl lcp create lyroute-lan0 host-if lymgmt0
vppctl lcp lcp-sync on
lcp_ready=false
for _ in $(seq 1 50); do
	if nsenter -t "$vpp_pid" -n ip link show dev lymgmt0 >/dev/null 2>&1; then
		lcp_ready=true
		break
	fi
	sleep .1
done
[ "$lcp_ready" = true ] || { vppctl show lcp >&2; exit 1; }

run_transaction() {
	action=$1
	(cd "$repo_root/backend" && \
		LY_ROUTE_LAN_INTERFACE=lan0 \
		LY_ROUTE_VPPCTL_INTEGRATION_BINARY="$tmp/vppctl" \
		LY_ROUTE_NETWORK_FAULT_MARKER="$fault_marker" \
		LY_ROUTE_NETWORK_TRANSACTION_ACTION="$action" \
		go test ./internal/runtime/apply -run '^TestGatewayNetworkProductionTransactionVPPCTLIntegration$' -count=1 -v) >"$tmp/transaction-$action.txt" 2>&1
}

run_transaction apply

linux_ready=false
for _ in $(seq 1 50); do
	if nsenter -t "$vpp_pid" -n ip -4 address show dev lymgmt0 2>/dev/null | grep -F '10.0.0.1/24' >/dev/null; then
		linux_ready=true
		break
	fi
	sleep .1
done
[ "$linux_ready" = true ] || { vppctl show lcp >&2; nsenter -t "$vpp_pid" -n ip address show dev lymgmt0 >&2; exit 1; }
nsenter -t "$vpp_pid" -n python3 -m http.server 8443 --bind 10.0.0.1 >"$tmp/management-http.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 50); do
	ip netns exec "$client_ns" curl -fsS --connect-timeout 1 http://10.0.0.1:8443/ >/dev/null 2>&1 && break
	sleep .1
done
ip netns exec "$client_ns" curl -fsS --connect-timeout 2 http://10.0.0.1:8443/ >"$tmp/management-before-link-fault.html"

run_transaction fault
ip netns exec "$client_ns" curl -fsS --connect-timeout 2 http://10.0.0.1:8443/ >"$tmp/management-after-transaction-rollback.html"

ip netns exec "$client_ns" ping -c 5 -W 1 10.0.1.2 >"$tmp/lan-to-wan.txt"
ip netns exec "$wan_ns" ping -c 5 -W 1 10.0.0.2 >"$tmp/wan-to-lan.txt"
vppctl show interface >"$tmp/interfaces-before-link-fault.txt"
vppctl show interface address >"$tmp/addresses-after-rollback.txt"
vppctl show bond details >"$tmp/bond-after-rollback.txt"
grep -F '10.0.0.1/24' "$tmp/addresses-after-rollback.txt" >/dev/null
grep -F '10.0.1.1/24' "$tmp/addresses-after-rollback.txt" >/dev/null
! grep -F '10.0.1.254/24' "$tmp/addresses-after-rollback.txt" >/dev/null
grep -F 'lyroute-bm0' "$tmp/bond-after-rollback.txt" >/dev/null
grep -F 'lyroute-bm1' "$tmp/bond-after-rollback.txt" >/dev/null
! grep -F 'lyroute-fault-bond1' "$tmp/bond-after-rollback.txt" >/dev/null
! grep -F 'lyroute-fault-bond2' "$tmp/bond-after-rollback.txt" >/dev/null

vppctl set interface state lyroute-wan0 down
set +e
ip netns exec "$client_ns" ping -c 2 -W 1 10.0.1.2 >"$tmp/wan-link-down.txt" 2>&1
down_rc=$?
set -e
[ "$down_rc" -ne 0 ] || { echo 'WAN link fault did not interrupt WAN packet flow' >&2; exit 1; }
ip netns exec "$client_ns" curl -fsS --connect-timeout 2 http://10.0.0.1:8443/ >"$tmp/management-during-wan-fault.html"
run_transaction reconcile
recovered=false
for _ in $(seq 1 20); do
	if ip netns exec "$client_ns" ping -c 1 -W 1 10.0.1.2 >/dev/null 2>&1; then
		recovered=true
		break
	fi
	sleep .2
done
[ "$recovered" = true ] || { vppctl show interface >"$tmp/interfaces-recovery-timeout.txt"; exit 1; }
ip netns exec "$client_ns" ping -c 3 -W 1 10.0.1.2 >"$tmp/wan-link-recovered.txt"
vppctl show interface >"$tmp/interfaces-after-recovery.txt"

# Package-wide transaction and typed lifecycle contracts complement the live
# VPP scenario with every Gateway resource class (WAN groups, routes, ACL/QoS,
# NAT44 and port maps), including rollback-readback rejection paths.
(cd "$repo_root/backend" && go test ./internal/runtime/apply ./internal/runtime/vpp -count=1) >"$tmp/network-production-contracts.txt" 2>&1

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
for evidence in "$tmp"/*.txt "$tmp"/*.html; do
	[ -f "$evidence" ] || continue
	sed -i 's/\r$//; s/[[:space:]]*$//' "$evidence"
	cp "$evidence" "$evidence_dir/"
done
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
completed=true
printf 'Gateway production transaction, real VPP interface/bond readback, bidirectional packet, rollback and management-survival verification passed: %s\n' "$evidence_dir"
trap - EXIT INT TERM
cleanup
