#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
deb=${LY_ROUTE_XRAY_DEB:-/root/ly-route/runtime-debs/xray_0~fdb9b616_amd64.deb}
backend_port=${LY_ROUTE_XRAY_TEST_BACKEND_PORT:-28080}
backend_address=${LY_ROUTE_XRAY_TEST_BACKEND_ADDRESS:-11.200.0.2}
image=${LY_ROUTE_XRAY_TEST_IMAGE:-ly-route/vpp-test:25.10}
workdir=$(mktemp -d)
container="ly-route-xray-failover-$$"
backend_ns="lyxb$$"
host_veth="lyxh$$"
ns_veth="lyxn$$"
backend_pid=""

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  if [[ -n "$backend_pid" ]]; then kill "$backend_pid" >/dev/null 2>&1 || true; fi
  ip link del "$host_veth" >/dev/null 2>&1 || true
  ip netns del "$backend_ns" >/dev/null 2>&1 || true
  rm -rf "$workdir"
}
trap cleanup EXIT

dpkg-deb -x "$deb" "$workdir/root"
cat >"$workdir/backend.py" <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
import sys

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/payload":
            body = b"proxy-ok\n"
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(204)
            self.end_headers()
    def log_message(self, *_):
        pass

HTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
PY
ip netns add "$backend_ns"
ip link add "$host_veth" type veth peer name "$ns_veth"
ip link set "$ns_veth" netns "$backend_ns"
ip address add 11.200.0.1/30 dev "$host_veth"
ip link set "$host_veth" up
ip -n "$backend_ns" link set lo up
ip -n "$backend_ns" address add "$backend_address/30" dev "$ns_veth"
ip -n "$backend_ns" link set "$ns_veth" up
ip netns exec "$backend_ns" python3 "$workdir/backend.py" "$backend_port" &
backend_pid=$!

uuid=11111111-1111-1111-1111-111111111111
cat >"$workdir/server1.json" <<JSON
{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":21001,"protocol":"vless","settings":{"clients":[{"id":"$uuid"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom"}]}
JSON
cat >"$workdir/server2.json" <<JSON
{"log":{"loglevel":"warning"},"inbounds":[{"listen":"127.0.0.1","port":21002,"protocol":"vless","settings":{"clients":[{"id":"$uuid"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom"}]}
JSON
cat >"$workdir/client.json" <<JSON
{
  "log":{"loglevel":"warning"},
  "api":{"tag":"api","services":["RoutingService"]},
  "inbounds":[
    {"tag":"proxy-in","listen":"127.0.0.1","port":23080,"protocol":"socks","settings":{"udp":false}},
    {"tag":"api-in","listen":"127.0.0.1","port":10085,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}}
  ],
  "outbounds":[
    {"tag":"subscription-main-node-a","protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":21001,"users":[{"id":"$uuid","encryption":"none"}]}]}},
    {"tag":"subscription-main-node-b","protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":21002,"users":[{"id":"$uuid","encryption":"none"}]}]}}
  ],
  "routing":{"rules":[{"type":"field","inboundTag":["api-in"],"outboundTag":"api"},{"type":"field","inboundTag":["proxy-in"],"balancerTag":"subscription-main-fastest"}],"balancers":[{"tag":"subscription-main-fastest","selector":["subscription-main-node-"],"strategy":{"type":"leastPing","settings":{}}}]},
  "observatory":{"subjectSelector":["subscription-main-node-"],"probeURL":"http://$backend_address:$backend_port/generate_204","probeInterval":"1s","enableConcurrency":true}
}
JSON

docker run -d --name "$container" --network host \
  -v "$workdir/root/usr/bin/xray:/usr/local/bin/xray:ro" -v "$workdir:/configs:ro" \
  --entrypoint sh "$image" -c 'sleep infinity' >/dev/null

start_xray() {
  local name=$1
  docker exec "$container" sh -c "xray run -config /configs/$name.json >/tmp/$name.log 2>&1 & echo \$! >/tmp/$name.pid"
}
stop_xray() {
  local name=$1
  docker exec "$container" sh -c "kill \$(cat /tmp/$name.pid)"
}
request_payload() {
  curl --silent --show-error --max-time 5 --noproxy '' --socks5-hostname "127.0.0.1:23080" "http://$backend_address:$backend_port/payload"
}

start_xray server1
start_xray client
sleep 4
first=$(request_payload)
grep -q 'proxy-ok' <<<"$first"
docker exec "$container" xray api bi --server=127.0.0.1:10085 subscription-main-fastest >"$workdir/balancer-first.json"
grep -q 'subscription-main-node-a' "$workdir/balancer-first.json"
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-a go test ./internal/runtime/service -run '^TestXrayBalancerStateIntegration$' -count=1)
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-a go test ./internal/httpapi -run '^TestXrayStatusIntegration$' -count=1)

start_xray server2
sleep 3
stop_xray server1
sleep 3
second=$(request_payload)
grep -q 'proxy-ok' <<<"$second"
docker exec "$container" xray api bi --server=127.0.0.1:10085 subscription-main-fastest >"$workdir/balancer-second.json"
grep -q 'subscription-main-node-b' "$workdir/balancer-second.json"
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-b go test ./internal/runtime/service -run '^TestXrayBalancerStateIntegration$' -count=1)
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-b go test ./internal/httpapi -run '^TestXrayStatusIntegration$' -count=1)

stop_xray server2
sleep 3
if request_payload >"$workdir/fail-closed.out" 2>&1; then
  echo "request unexpectedly succeeded after all proxy nodes failed" >&2
  exit 1
fi
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_UNAVAILABLE=1 go test ./internal/runtime/service -run '^TestXrayBalancerStateIntegration$' -count=1)
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_UNAVAILABLE=1 go test ./internal/httpapi -run '^TestXrayStatusIntegration$' -count=1)

start_xray server1
sleep 3
recovered=$(request_payload)
grep -q 'proxy-ok' <<<"$recovered"
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-a go test ./internal/runtime/service -run '^TestXrayBalancerStateIntegration$' -count=1)
(cd "$repo_root/backend" && LY_ROUTE_XRAY_BALANCER_API_BINARY="$workdir/root/usr/bin/xray" LY_ROUTE_XRAY_EXPECT_SELECTED=subscription-main-node-a go test ./internal/httpapi -run '^TestXrayStatusIntegration$' -count=1)

echo "Xray fastest-node startup, automatic failover, fail-closed status, and recovery container verification passed"
