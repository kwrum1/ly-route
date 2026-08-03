#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
image=${LY_ROUTE_VPP_TEST_IMAGE:-ly-route/vpp-test:25.10}
suffix=$(( $$ % 10000 ))
container="lyroute-native-$suffix"
host_if="nxh$suffix"
peer_if=testxdp0
tmp=$(mktemp -d)

cleanup() {
  ip link del "$host_if" >/dev/null 2>&1 || true
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

docker run -d --rm --name "$container" --privileged --network none "$image" \
  /usr/bin/vpp unix '{ nodaemon cli-listen /run/vpp/cli.sock }' >/dev/null
pid=$(docker inspect -f '{{.State.Pid}}' "$container")
ip link add "$host_if" type veth peer name "$peer_if"
ip link set "$peer_if" netns "$pid"
ip link set "$host_if" up
nsenter -t "$pid" -n ip link set "$peer_if" up

ready=false
for _ in $(seq 1 50); do
  if docker exec "$container" vppctl -s /run/vpp/cli.sock show version >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
[ "$ready" = true ]

cat > "$tmp/vppctl" <<EOF
#!/bin/sh
exec docker exec "$container" vppctl -s /run/vpp/cli.sock "\$@"
EOF
chmod 0755 "$tmp/vppctl"

"$tmp/vppctl" help create interface af_xdp | grep -q 'zero-copy'
cd "$repo_root/backend"
LY_ROUTE_NATIVE_VPPCTL="$tmp/vppctl" go test ./internal/runtime/vpp -run '^TestAFXDPZeroCopyAttachmentFailClosedVPPIntegration$' -count=1

printf 'stock VPP AF_XDP zero-copy unsupported-veth path locks semantically and leaves no stale interface\n'
