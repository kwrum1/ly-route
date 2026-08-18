#!/usr/bin/env bash
set -Eeuo pipefail

# Fast functional acceptance for an already-running gateway.
# Each lane owns a temporary Linux namespace and AF_PACKET interface. This
# validates real client packets without changing production interface roles.

(( EUID == 0 )) || { echo "must run as root" >&2; exit 2; }
command -v ip >/dev/null
command -v vppctl >/dev/null
command -v ping >/dev/null

suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
pppoe_if=${LY_ROUTE_PPPOE_IF:-pppoe_session0}
base_octet=${LY_ROUTE_TEST_BASE_OCTET:-77}
target=${LY_ROUTE_TEST_TARGET:-8.8.8.8}
lan_prefix=${LY_ROUTE_TEST_LAN_PREFIX:-192.168}

lan_if="lyf${suffix}"
client_if="lyc${suffix}"
vpp_if="host-${lan_if}"
ns="lyfast${suffix}"

cleanup() {
  vppctl set interface nat44 ei in "$vpp_if" out "$pppoe_if" del >/dev/null 2>&1 || true
  vppctl delete host-interface name "$lan_if" >/dev/null 2>&1 || true
  ip netns del "$ns" >/dev/null 2>&1 || true
  ip link del "$lan_if" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

ip netns add "$ns"
ip link add "$lan_if" type veth peer name "$client_if"
ip link set "$client_if" netns "$ns"
ip link set "$lan_if" up
ip netns exec "$ns" ip link set lo up
ip netns exec "$ns" ip link set "$client_if" up

vppctl create host-interface name "$lan_if" cksum-gso-disable >/dev/null
vppctl set interface state "$vpp_if" up
vppctl set interface ip address "$vpp_if" "$lan_prefix.$base_octet.1/24"
vppctl set interface nat44 ei in "$vpp_if" out "$pppoe_if"

ip netns exec "$ns" ip address add "$lan_prefix.$base_octet.2/24" dev "$client_if"
ip netns exec "$ns" ip route add default via "$lan_prefix.$base_octet.1"

ip netns exec "$ns" ping -c 2 -W 3 "$lan_prefix.$base_octet.1" >/dev/null
ip netns exec "$ns" ping -c 2 -W 5 "$target" >/dev/null

sessions=$(vppctl show nat44 ei session 2>/dev/null || vppctl show nat44 sessions 2>/dev/null || true)
printf '%s\n' "$sessions" | grep -F "$lan_prefix.$base_octet.2" >/dev/null
printf 'PASS lane=%s client=%s target=%s pppoe=%s\n' "$base_octet" "$lan_prefix.$base_octet.2" "$target" "$pppoe_if"
