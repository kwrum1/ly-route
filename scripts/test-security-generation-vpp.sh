#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
security_guard_plugin=${LY_ROUTE_VPP_SECURITY_GUARD_PLUGIN:-}
if [ -z "$security_guard_plugin" ]; then
	security_guard_plugin=$($repo_root/scripts/build-vpp-security-guard-plugin.sh | tail -1)
fi
[ -f "$security_guard_plugin" ] || { echo "security guard plugin not found: $security_guard_plugin" >&2; exit 1; }
suffix=$$
container="ly-route-vpp-security-generation-$suffix"
client="lrsgc-$suffix"
server="lrsgs-$suffix"
socket="/run/vpp/security-generation-$suffix.sock"
tmp="$repo_root/.sisyphus/full-acceptance/evidence/g-security-generation-$suffix"
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/g-security-generation}
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
	-v "$security_guard_plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_security_guard_plugin.so:ro" \
	ly-route/vpp-test:25.10 /usr/bin/vpp unix "{ nodaemon cli-listen $socket }" api-segment "{ prefix securitygeneration$suffix }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$socket" "$@"; }
for _ in $(seq 1 40); do vppctl show version >/dev/null 2>&1 && break; sleep .25; done
vppctl show version >"$tmp/version.txt"
vppctl show plugin >"$tmp/plugins.txt"
grep -q ly_route_security_guard_plugin.so "$tmp/plugins.txt"

ip netns add "$client"
ip netns add "$server"
ip link add lyroute-lan0 type veth peer name sg-client0
ip link add lyroute-wan0 type veth peer name sg-server0
ip link set lyroute-lan0 netns "$vpp_pid"
ip link set lyroute-wan0 netns "$vpp_pid"
ip link set sg-client0 netns "$client"
ip link set sg-server0 netns "$server"
ip -n "$client" link set sg-client0 address 02:00:00:00:00:02
ip -n "$client" addr add 10.0.0.2/24 dev sg-client0
ip -n "$client" -6 addr add 2001:db8:0::9/64 dev sg-client0 nodad
ip -n "$client" link set sg-client0 up
ip -n "$client" route add default via 10.0.0.1
ip -n "$server" addr add 10.0.1.2/24 dev sg-server0
ip -n "$server" -6 addr add 2001:db8:1::2/64 dev sg-server0 nodad
ip -n "$server" link set sg-server0 up
ip -n "$server" route add default via 10.0.1.1
for interface in lyroute-lan0 lyroute-wan0; do
	nsenter -t "$vpp_pid" -n ip link set "$interface" up
	vppctl create host-interface name "$interface" >/dev/null
	vppctl set interface state "host-$interface" up
	vppctl set interface name "host-$interface" "$interface"
done
vppctl set interface ip address lyroute-lan0 10.0.0.1/24
vppctl set interface ip address lyroute-wan0 10.0.1.1/24
vppctl set interface ip address lyroute-lan0 2001:db8:0::1/64
vppctl set interface ip address lyroute-wan0 2001:db8:1::1/64
ip netns exec "$client" ping -c 2 -W 1 10.0.1.2 >"$tmp/direct-before.txt"
ip -n "$client" -6 route replace default via 2001:db8:0::1 dev sg-client0
ip -n "$server" -6 route replace default via 2001:db8:1::1 dev sg-server0
ip netns exec "$client" ping -6 -c 2 -W 1 2001:db8:1::2 >"$tmp/ipv6-direct-before.txt"

cat >"$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec $container vppctl -s $socket "\$@"
EOF
chmod 0755 "$tmp/vppctl"

(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show acl-plugin acl >"$tmp/acl-after-apply.txt"
vppctl show acl-plugin interface >"$tmp/acl-interface-after-apply.txt"
vppctl show acl-plugin macip acl >"$tmp/macip-after-apply.txt"
vppctl show acl-plugin macip interface >"$tmp/macip-interface-after-apply.txt"
grep -q 'ly-route-security-gen-' "$tmp/acl-after-apply.txt"
grep -q 'ly-route-security-macip-' "$tmp/macip-after-apply.txt"
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/allowed-bound-client.txt"

ip -n "$client" link set sg-client0 down
ip -n "$client" link set sg-client0 address 02:00:00:00:00:03
ip -n "$client" link set sg-client0 up
ip -n "$client" route replace 10.0.0.0/24 dev sg-client0 src 10.0.0.2
ip -n "$client" route replace default via 10.0.0.1 dev sg-client0
ip -n "$client" route get 10.0.1.2 >"$tmp/spoof-client-route.txt"
set +e
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/spoofed-mac-denied.txt"
spoof_rc=$?
set -e
[ "$spoof_rc" -ne 0 ] || { echo 'spoofed MAC was not denied' >&2; exit 1; }

ip -n "$client" link set sg-client0 down
ip -n "$client" link set sg-client0 address 02:00:00:00:00:02
ip -n "$client" -4 addr flush dev sg-client0
ip -n "$client" addr add 10.0.0.9/24 dev sg-client0
ip -n "$client" link set sg-client0 up
ip -n "$client" route replace 10.0.0.0/24 dev sg-client0 src 10.0.0.9
ip -n "$client" route replace default via 10.0.0.1 dev sg-client0
ip -n "$client" route get 10.0.1.2 >"$tmp/threat-client-route.txt"
grep -q 'via 10.0.0.1' "$tmp/threat-client-route.txt"
set +e
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/threat-ip-denied.txt"
threat_rc=$?
set -e
[ "$threat_rc" -ne 0 ] || { echo 'threat-list IP was not denied' >&2; exit 1; }

(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_ACTION=move go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show acl-plugin acl >"$tmp/acl-after-delete.txt"
vppctl show acl-plugin macip acl >"$tmp/macip-after-delete.txt"
if grep -q 'ly-route-security-gen-' "$tmp/acl-after-delete.txt"; then echo 'security ACL tag remained after delete' >&2; exit 1; fi
if grep -q 'ly-route-security-macip-' "$tmp/macip-after-delete.txt"; then echo 'security MACIP tag remained after delete' >&2; exit 1; fi
ip netns exec "$client" ping -c 3 -W 1 10.0.1.2 >"$tmp/direct-after-delete.txt"
ip -n "$client" -6 addr replace 2001:db8:0::9/64 dev sg-client0 nodad
ip -n "$client" -6 route replace default via 2001:db8:0::1 dev sg-client0
ip netns exec "$client" ping -6 -c 3 -W 1 2001:db8:1::2 >"$tmp/ipv6-direct-before-threat.txt"

(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_PROFILE=ipv6 go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show acl-plugin acl >"$tmp/ipv6-acl-after-apply.txt"
grep -q 'ipv6 deny src 2001:db8::9/128 dst ::/0' "$tmp/ipv6-acl-after-apply.txt"
set +e
ip netns exec "$client" ping -6 -c 3 -W 1 2001:db8:1::2 >"$tmp/ipv6-threat-denied.txt"
ipv6_threat_rc=$?
set -e
[ "$ipv6_threat_rc" -ne 0 ] || { echo 'IPv6 threat-list address was not denied' >&2; exit 1; }
(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_PROFILE=ipv6 LY_ROUTE_SECURITY_GENERATION_ACTION=delete go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
ip netns exec "$client" ping -6 -c 3 -W 1 2001:db8:1::2 >"$tmp/ipv6-direct-after-delete.txt"

ip -n "$client" -4 addr flush dev sg-client0
ip -n "$client" addr add 10.0.0.2/24 dev sg-client0
ip -n "$client" route replace 10.0.0.0/24 dev sg-client0 src 10.0.0.2
ip -n "$client" route replace default via 10.0.0.1 dev sg-client0
(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_PROFILE=attack go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show ly-route security-guard >"$tmp/security-guard-after-apply.txt"
grep -q 'rule ly-route-security-attack-integration_syn-ip4 enabled 1 family 4' "$tmp/security-guard-after-apply.txt" || { cat "$tmp/security-guard-after-apply.txt" >&2; exit 1; }
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
attack_server_pid=$!
sleep .5
ip netns exec "$client" python3 - <<'PY'
import socket
for _ in range(80):
    s = socket.socket()
    s.settimeout(.02)
    try:
        s.connect(("10.0.1.2", 8443))
    except OSError:
        pass
    s.close()
PY
vppctl show ly-route security-guard >"$tmp/security-guard-after-flood.txt"
grep -Eq 'rule ly-route-security-attack-integration_syn-ip4 enabled 1 family 4 .* matched [1-9][0-9]* .* drops [1-9][0-9]*' "$tmp/security-guard-after-flood.txt" || { cat "$tmp/security-guard-after-flood.txt" >&2; exit 1; }
kill "$attack_server_pid" >/dev/null 2>&1 || true
(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_PROFILE=attack LY_ROUTE_SECURITY_GENERATION_ACTION=fault go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show ly-route security-guard >"$tmp/security-guard-after-failed-update.txt"
grep -Eq 'rule ly-route-security-attack-integration_syn-ip4 enabled 1 family 4 interface lyroute-lan0 threshold-pps 5 burst-packets 2' "$tmp/security-guard-after-failed-update.txt" || { cat "$tmp/security-guard-after-failed-update.txt" >&2; exit 1; }
if grep -q 'invalid-update\|missing0' "$tmp/security-guard-after-failed-update.txt"; then echo 'failed security update left an invalid rule' >&2; exit 1; fi
(cd "$repo_root/backend" && LY_ROUTE_RUNTIME_APPLY_FAILURE_EVIDENCE="$tmp/runtime-api-failure.json" go test ./internal/httpapi -run TestGatewayRuntimeApplyFailureAuditsRollbackReceipt -count=1)
test -s "$tmp/runtime-api-failure.json"
(cd "$repo_root/backend" && LY_ROUTE_SECURITY_GENERATION_VPPCTL="$tmp/vppctl" LY_ROUTE_SECURITY_GENERATION_PROFILE=attack LY_ROUTE_SECURITY_GENERATION_ACTION=delete go test ./internal/runtime/vpp -run TestSecurityGenerationVPPCTLIntegration -count=1)
vppctl show ly-route security-guard >"$tmp/security-guard-after-delete.txt"
if grep -q 'ly-route-security-attack-' "$tmp/security-guard-after-delete.txt"; then echo 'security guard rule remained after delete' >&2; exit 1; fi

rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
for evidence in "$tmp"/*.txt; do
	sed -i 's/\r$//; s/[[:space:]]*$//' "$evidence"
	cp "$evidence" "$evidence_dir/"
done
cp "$tmp/runtime-api-failure.json" "$evidence_dir/"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
completed=true
printf 'security generation real VPP MACIP spoof and threat-list packet verification passed: %s\n' "$evidence_dir"
trap - EXIT INT TERM
cleanup
