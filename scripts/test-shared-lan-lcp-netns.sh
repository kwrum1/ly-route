#!/bin/sh
set -eu

command -v docker >/dev/null
command -v ip >/dev/null
command -v nsenter >/dev/null

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=${LY_ROUTE_TEST_SUFFIX:-$$}
container="lyroute-shared-lan-$suffix"
client_ns="lyroute-sl-client-$suffix"
wan_ns="lyroute-sl-wan-$suffix"
cli_socket="/run/vpp/cli.sock"
lan_host="slc${suffix}p"
lan_vpp="slc${suffix}v"
wan_host="slw${suffix}p"
wan_vpp="slw${suffix}v"

cleanup() {
  [ -z "${server_pid:-}" ] || kill "$server_pid" >/dev/null 2>&1 || true
  docker rm -f "$container" >/dev/null 2>&1 || true
  ip netns del "$client_ns" >/dev/null 2>&1 || true
  ip netns del "$wan_ns" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

test_binary=$(mktemp "${TMPDIR:-/tmp}/lyroute-lcp-vpp.XXXXXX")
rm -f "$test_binary"
(cd "$repo_root/backend" && go test -c -o "$test_binary" ./internal/runtime/vpp)

docker run -d --name "$container" --privileged --network none --shm-size 256m \
  ly-route/vpp-test:25.10 \
  /usr/bin/vpp unix "{ nodaemon cli-listen $cli_socket }" \
  plugins "{ plugin dpdk_plugin.so { disable } plugin linux_cp_plugin.so { enable } plugin linux_nl_plugin.so { enable } }" >/dev/null
vpp_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
vppctl() { docker exec "$container" vppctl -s "$cli_socket" "$@"; }
docker cp "$test_binary" "$container:/tmp/vpp-runtime.test" >/dev/null
rm -f "$test_binary"

ready=false
for _ in $(seq 1 60); do
  if vppctl show lcp >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
if [ "$ready" != true ]; then
  docker logs "$container" >&2 || true
  exit 1
fi

ip netns add "$client_ns"
ip netns add "$wan_ns"
ip link add "$lan_host" type veth peer name "$lan_vpp"
ip link set "$lan_host" netns "$client_ns"
ip link set "$lan_vpp" netns "$vpp_pid"
ip link add "$wan_host" type veth peer name "$wan_vpp"
ip link set "$wan_host" netns "$wan_ns"
ip link set "$wan_vpp" netns "$vpp_pid"

ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set "$lan_host" up
ip netns exec "$client_ns" ip addr add 10.10.10.2/24 dev "$lan_host"
ip netns exec "$client_ns" ip route add default via 10.10.10.254
ip netns exec "$wan_ns" ip link set lo up
ip netns exec "$wan_ns" ip link set "$wan_host" up
ip netns exec "$wan_ns" ip addr add 198.51.100.1/24 dev "$wan_host"
ip netns exec "$wan_ns" ip route add 10.10.10.0/24 via 198.51.100.254

nsenter -t "$vpp_pid" -n ip link set "$lan_vpp" up
nsenter -t "$vpp_pid" -n ip link set "$wan_vpp" up
vppctl create host-interface name "$lan_vpp" >/dev/null
vppctl create host-interface name "$wan_vpp" >/dev/null
vppctl set interface state "host-$lan_vpp" up
vppctl set interface state "host-$wan_vpp" up
docker exec "$container" sh -c "LY_ROUTE_VPPCTL_INTEGRATION_BINARY=vppctl LY_ROUTE_LCP_VPP_INTERFACE=host-$lan_vpp /tmp/vpp-runtime.test -test.run '^TestManagementLCPVPPCTLIntegration$' -test.v"
vppctl set interface ip address "host-$lan_vpp" 10.10.10.254/24
vppctl set interface ip address "host-$wan_vpp" 198.51.100.254/24

linux_ready=false
for _ in $(seq 1 50); do
  if nsenter -t "$vpp_pid" -n ip -4 address show dev lymgmt0 | grep -F '10.10.10.254/24' >/dev/null; then
    linux_ready=true
    break
  fi
  sleep 0.1
done
if [ "$linux_ready" != true ]; then
  vppctl show lcp >&2 || true
  nsenter -t "$vpp_pid" -n ip address show >&2 || true
  exit 1
fi

nsenter -t "$vpp_pid" -n python3 -m http.server 8443 --bind 10.10.10.254 >/tmp/lyroute-shared-lan-http.log 2>&1 &
server_pid=$!
http_ready=false
for _ in $(seq 1 50); do
  if ip netns exec "$client_ns" curl -fsS --connect-timeout 1 http://10.10.10.254:8443/ >/dev/null 2>&1; then
    http_ready=true
    break
  fi
  sleep 0.1
done
if [ "$http_ready" != true ]; then
  vppctl show lcp >&2 || true
  vppctl show interface address >&2 || true
  docker logs "$container" >&2 || true
  exit 1
fi

ip netns exec "$client_ns" ping -c 3 -W 2 198.51.100.1 >/dev/null
vppctl show lcp | grep -F "host-$lan_vpp" >/dev/null
nsenter -t "$vpp_pid" -n ip route show | grep -F '10.10.10.0/24' >/dev/null

docker exec "$container" sh -c "LY_ROUTE_VPPCTL_INTEGRATION_BINARY=vppctl LY_ROUTE_LCP_VPP_INTERFACE=host-$lan_vpp LY_ROUTE_LCP_ENABLED=false /tmp/vpp-runtime.test -test.run '^TestManagementLCPVPPCTLIntegration$' -test.v"
if nsenter -t "$vpp_pid" -n ip link show lymgmt0 >/dev/null 2>&1; then
  echo 'exclusive management cleanup left lymgmt0 present' >&2
  exit 1
fi

printf '%s\n' 'VPP shared-LAN LCP idempotency, management access, concurrent forwarding, and exclusive cleanup verification passed'
