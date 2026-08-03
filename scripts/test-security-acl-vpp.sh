#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=$$
container="ly-route-vpp-acl-$suffix"
client="lrac-$suffix"
server="lras-$suffix"
socket="/run/vpp/acl-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-security-acl-$suffix"
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/o-vpp-security-acl}
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
	ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" api-segment "{ prefix acl$suffix }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do vppctl show version >/dev/null 2>&1 && break; sleep .25; done
vppctl show version >"$tmp/version.txt"

ip netns add "$client"
ip netns add "$server"
ip link add lyroute-lan0 type veth peer name acl-client0
ip link add lyroute-wan0 type veth peer name acl-server0
ip link set lyroute-lan0 netns "$vpp_pid"
ip link set lyroute-wan0 netns "$vpp_pid"
ip link set acl-client0 netns "$client"
ip link set acl-server0 netns "$server"
ip -n "$client" addr add 10.0.0.2/24 dev acl-client0
ip -n "$client" link set acl-client0 up
ip -n "$client" route add default via 10.0.0.1
ip -n "$server" addr add 10.0.1.2/24 dev acl-server0
ip -n "$server" link set acl-server0 up
ip -n "$server" route add default via 10.0.1.1
for interface in lyroute-lan0 lyroute-wan0; do
	nsenter -t "$vpp_pid" -n ip link set "$interface" up
	vppctl create host-interface name "$interface" >/dev/null
	vppctl set interface state "host-$interface" up
	vppctl set interface name "host-$interface" "$interface"
done
vppctl set interface ip address lyroute-lan0 10.0.0.1/24
vppctl set interface ip address lyroute-wan0 10.0.1.1/24
ip netns exec "$client" ping -c 2 -W 1 10.0.1.2 >"$tmp/direct-before.txt"
cat >"$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec $container vppctl -s $socket "\$@"
EOF
chmod 0755 "$tmp/vppctl"

run_lifecycle() {
	(cd "$repo_root/backend" && LY_ROUTE_LAN_INTERFACE=lan0 LY_ROUTE_VPPCTL_INTEGRATION_BINARY="$tmp/vppctl" LY_ROUTE_SECURITY_ACL_ACTION="$1" \
		go test ./internal/runtime/vpp -run TestVPPCTLSecurityACLLifecycleIntegration -count=1)
}
run_lifecycle apply
vppctl show acl-plugin acl >"$tmp/acl-after-apply.txt"
vppctl show acl-plugin interface >"$tmp/interface-after-apply.txt"
grep -q 'ly-route-integration_deny' "$tmp/acl-after-apply.txt"
grep -q 'input acl' "$tmp/interface-after-apply.txt"
set +e
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/denied.txt"
denied_rc=$?
set -e
[ "$denied_rc" -ne 0 ] || { echo "security ACL did not deny matching packet flow" >&2; exit 1; }

run_lifecycle delete
vppctl show acl-plugin acl >"$tmp/acl-after-delete.txt"
vppctl show acl-plugin interface >"$tmp/interface-after-delete.txt"
! grep -q 'ly-route-integration_deny' "$tmp/acl-after-delete.txt"
! grep -q 'input acl.*0' "$tmp/interface-after-delete.txt"
ip netns exec "$client" ping -c 5 -W 1 10.0.1.2 >"$tmp/direct-after-delete.txt"

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
for evidence in "$tmp"/*.txt; do
	sed -i 's/\r$//; s/[[:space:]]*$//' "$evidence"
	sed -i -e ':trim' -e '/^[[:space:]]*$/{ $d; N; b trim; }' "$evidence"
done
cp "$tmp"/*.txt "$evidence_dir/"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
completed=true
printf 'security ACL real VPP deny/delete verification passed: %s\n' "$evidence_dir"
trap - EXIT INT TERM
cleanup
