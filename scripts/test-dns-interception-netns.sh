#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
suffix=$$
router="lyrdnsr$suffix"
client="lyrdnsc$suffix"
tmp=$(mktemp -d)
server_pid=
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" >/dev/null 2>&1 || true
  ip netns del "$client" >/dev/null 2>&1 || true
  ip netns del "$router" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

command -v ip >/dev/null
command -v nft >/dev/null
command -v python3 >/dev/null
ip netns add "$router"
ip netns add "$client"
ip link add "ldc$suffix" type veth peer name "ldr$suffix"
ip link set "ldc$suffix" netns "$client"
ip link set "ldr$suffix" netns "$router"
ip -n "$client" addr add 10.99.0.2/24 dev "ldc$suffix"
ip -n "$router" addr add 10.99.0.1/24 dev "ldr$suffix"
ip -n "$client" link set lo up
ip -n "$router" link set lo up
ip -n "$client" link set "ldc$suffix" up
ip -n "$router" link set "ldr$suffix" up
ip -n "$client" route add default via 10.99.0.1
ip netns exec "$router" sysctl -qw net.ipv4.ip_forward=1

helper_dir="$repo_root/backend/.dns-render-$suffix"
mkdir -p "$helper_dir"
cat > "$helper_dir/main.go" <<'EOF'
package main
import (
  "os"
  "ly-route/backend/internal/runtime/service"
)
func main() {
  artifacts, err := service.RenderGatewayNftablesCapture(struct{ }{}, service.DNSInterceptionPlan{})
  _ = artifacts; _ = err
  _ = os.Stdout
}
EOF
# The helper must live inside backend so Go's internal-package rule is enforced.
cat > "$helper_dir/main.go" <<EOF
package main
import (
  "fmt"
  "ly-route/backend/internal/runtime/proxy"
  "ly-route/backend/internal/runtime/service"
)
func main() {
  artifacts, err := service.RenderGatewayNftablesCapture(proxy.NftablesCapturePlan{}, service.DNSInterceptionPlan{LANInterfaces: []string{"ldr$suffix"}, ListenPort: 53})
  if err != nil { panic(err) }
  fmt.Print(artifacts[0].Content)
}
EOF
(cd "$repo_root/backend" && go run "./.dns-render-$suffix") > "$tmp/nftables.conf"
rm -rf "$helper_dir"
ip netns exec "$router" nft -f "$tmp/nftables.conf"

ip netns exec "$router" python3 -c '
import socket,threading
udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); udp.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); udp.bind(("0.0.0.0",53))
tcp=socket.socket(socket.AF_INET,socket.SOCK_STREAM); tcp.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); tcp.bind(("0.0.0.0",53)); tcp.listen()
def u():
  while True:
    data,addr=udp.recvfrom(2048); udp.sendto(b"udp:"+data,addr)
def t():
  while True:
    conn,_=tcp.accept(); data=conn.recv(2048); conn.sendall(b"tcp:"+data); conn.close()
threading.Thread(target=u,daemon=True).start(); threading.Thread(target=t,daemon=True).start(); threading.Event().wait()
' > "$tmp/dns-server.log" 2>&1 &
server_pid=$!
sleep 0.2
ip netns exec "$client" python3 - <<'PY'
import socket
target=("198.51.100.53",53)
udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); udp.settimeout(2); udp.sendto(b"proof",target)
assert udp.recvfrom(64)[0] == b"udp:proof"
tcp=socket.create_connection(target,2); tcp.sendall(b"proof"); assert tcp.recv(64) == b"tcp:proof"; tcp.close()
PY
ip netns exec "$router" nft list chain inet ly_route_dns_capture dns_prerouting > "$tmp/nft-readback.txt"
grep -q 'udp dport 53 counter packets [1-9]' "$tmp/nft-readback.txt"
grep -q 'tcp dport 53 counter packets [1-9]' "$tmp/nft-readback.txt"
printf '%s\n' 'Legacy nftables DNS TCP/UDP interception namespace verification passed (not production dataplane evidence)'
