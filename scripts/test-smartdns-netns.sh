#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
smartdns_deb=${LY_ROUTE_SMARTDNS_DEB:-/root/ly-route/runtime-debs/smartdns_0~48.1_amd64.deb}

command -v ip >/dev/null
command -v ipset >/dev/null
command -v python3 >/dev/null
[ -r "$smartdns_deb" ]

(cd "$repo_root/backend" && go test ./internal/httpapi -run '^TestDNS(ResolveDefaultsToNODATANoDefaultUpstream|ResolveUsesStoredPolicyOrderSuffixMatchAndFailsClosed|RuleUpdateVerifiesChecksumSwitchesPolicyAndRetainsRollback)$' -count=1)

tmpdir=$(mktemp -d)
namespace=ly-route-smartdns-e2e-$$
helper_dir=$(mktemp -d "$repo_root/backend/.smartdns-e2e.XXXXXX")
cleanup() {
  if [ -r "${tmpdir:-}/smartdns.pid" ]; then
    kill "$(cat "$tmpdir/smartdns.pid")" 2>/dev/null || true
  fi
  if [ -r "${tmpdir:-}/fallback.pid" ]; then
    kill "$(cat "$tmpdir/fallback.pid")" 2>/dev/null || true
  fi
  ip netns del "$namespace" 2>/dev/null || true
  rm -rf "$tmpdir" "$helper_dir"
}
trap cleanup EXIT INT TERM

cat > "$helper_dir/main.go" <<'EOF'
package main

import (
  "fmt"
  "time"
  "ly-route/backend/internal/runtime/dns"
  service "ly-route/backend/internal/runtime/service"
  "ly-route/backend/internal/runtime/trafficpolicy"
)

func main() {
  now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
  pbr := []trafficpolicy.RoutePolicy{{
    ID: "ordinary-pbr", Priority: 10, Action: "route", Egress: "wan-secondary",
    Match: trafficpolicy.Match{Sources: []string{"any"}, Destinations: []string{"any"}, Protocols: []string{"any"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}},
  }}
  overrides := []trafficpolicy.DNSOverrideIntent{{Source: "192.0.2.0/24", ResolvedIP: "203.0.113.53", Egress: "wan-primary", ExpiresAt: now.Add(60 * time.Second).Format(time.RFC3339)}}
  decision := trafficpolicy.DecideRoute(pbr, trafficpolicy.Flow{SourceIP: "192.0.2.10", DestIP: "203.0.113.53", Protocol: "tcp", DestPort: "443"}, overrides, now)
  if !decision.Matched || decision.Egress != "wan-primary" || decision.Reason != "dns_intent_override" { panic(fmt.Sprintf("DNS fixed-line decision lost precedence: %#v", decision)) }
  expired := trafficpolicy.DecideRoute(pbr, trafficpolicy.Flow{SourceIP: "192.0.2.10", DestIP: "203.0.113.53", Protocol: "tcp", DestPort: "443"}, overrides, now.Add(61*time.Second))
  if !expired.Matched || expired.Egress != "wan-secondary" || expired.RuleID != "ordinary-pbr" { panic(fmt.Sprintf("expired DNS override did not return to PBR: %#v", expired)) }
  fmt.Println("# DNS fixed-line precedence and TTL expiry decision verified")

  policy := dns.NewPolicy(dns.Reject(), []dns.Rule{
    {ID: "fixed-wan", Domains: []string{"updates.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, WANEgressID: "wan-primary"}},
    {ID: "unavailable", Domains: []string{"failure.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, UpstreamID: "dns-unavailable"}},
  })
  compiled, err := dns.CompilePolicy(policy, nil)
  if err != nil { panic(err) }
  artifacts, err := service.RenderSmartDNSBundle([]service.SmartDNSPlan{{
    ID: "fixed-wan", Render: compiled.RenderSmartDNS(),
    Upstreams: []service.SmartDNSUpstream{
      {ID: "dns-wan-primary", Servers: []string{"127.0.0.1:1053"}, Interface: "lo", WANEgressID: "wan-primary"},
      {ID: "dns-unavailable", Servers: []string{"127.0.0.1:1054"}, Interface: "lo"},
      {ID: "dns-fallback", Servers: []string{"127.0.0.1:1055"}, Interface: "lo"},
    },
    Cache: service.SmartDNSCache{Size: 32768, TTLMin: 60, TTLMax: 600, Prefetch: true},
  }})
  if err != nil { panic(err) }
  fmt.Print(artifacts[0].Content)
}
EOF

(cd "$repo_root/backend" && go run "$helper_dir/main.go") > "$tmpdir/ly-route-active.conf"
grep -F 'nameserver /-.updates.example/dns-wan-primary' "$tmpdir/ly-route-active.conf" >/dev/null
grep -F 'nameserver /-.failure.example/dns-unavailable' "$tmpdir/ly-route-active.conf" >/dev/null
grep -F 'address #' "$tmpdir/ly-route-active.conf" >/dev/null
grep -F ' -interface lo' "$tmpdir/ly-route-active.conf" >/dev/null
grep -F 'rr-ttl-min 60' "$tmpdir/ly-route-active.conf" >/dev/null

dpkg-deb -x "$smartdns_deb" "$tmpdir/rootfs"
cat > "$tmpdir/dns-upstream.py" <<'EOF'
import socket, struct

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("127.0.0.1", 1053))
while True:
    query, peer = sock.recvfrom(4096)
    question_end = 12
    while query[question_end] != 0:
        question_end += query[question_end] + 1
    question_end += 5
    header = query[:2] + b"\x81\x80" + b"\x00\x01\x00\x01\x00\x00\x00\x00"
    answer = b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04" + socket.inet_aton("203.0.113.53")
    sock.sendto(header + query[12:question_end] + answer, peer)
EOF
cat > "$tmpdir/dns-client.py" <<'EOF'
import socket, struct

def encode(name):
    return b"".join(bytes([len(part)]) + part.encode() for part in name.split(".")) + b"\0"

query = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00" + encode("updates.example") + b"\x00\x01\x00\x01"
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.settimeout(5)
sock.sendto(query, ("127.0.0.1", 5533))
answer, _ = sock.recvfrom(4096)
if answer[:2] != b"\x12\x34" or answer[6:8] != b"\x00\x01" or socket.inet_aton("203.0.113.53") not in answer:
    raise SystemExit("unexpected DNS response: " + answer.hex())
EOF
cat > "$tmpdir/dns-fallback.py" <<'EOF'
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("127.0.0.1", 1055))
while True:
    query, peer = sock.recvfrom(4096)
    labels = []
    offset = 12
    while offset < len(query) and query[offset]:
        size = query[offset]
        offset += 1
        labels.append(query[offset:offset + size].decode("ascii", "replace"))
        offset += size
    if ".".join(labels) in {"failure.example", "unmatched.example"}:
        print("LEAK", peer, ".".join(labels), flush=True)
    sock.sendto(query[:2] + b"\x81\x82" + query[4:], peer)
EOF
cat > "$tmpdir/dns-fault-client.py" <<'EOF'
import socket

def encode(name):
    return b"".join(bytes([len(part)]) + part.encode() for part in name.split(".")) + b"\0"

def query(name, ident, allowed_rcodes):
    packet = ident + b"\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00" + encode(name) + b"\x00\x01\x00\x01"
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(8)
    sock.sendto(packet, ("127.0.0.1", 5533))
    answer, _ = sock.recvfrom(4096)
    rcode = answer[3] & 0x0f
    answers = int.from_bytes(answer[6:8], "big")
    if answer[:2] != ident or rcode not in allowed_rcodes or answers != 0:
        raise SystemExit(f"unexpected fail-closed response for {name}: {answer.hex()}")

query("unmatched.example", b"\x22\x01", {0, 3})
query("failure.example", b"\x22\x02", {0, 2})
EOF

ip netns add "$namespace"
ip netns exec "$namespace" ip link set lo up
ip netns exec "$namespace" ipset create lyroute_dns_fixed-wan hash:ip timeout 600 family inet
ip netns exec "$namespace" python3 "$tmpdir/dns-upstream.py" > "$tmpdir/upstream.log" 2>&1 &
upstream_pid=$!
ip netns exec "$namespace" python3 "$tmpdir/dns-fallback.py" > "$tmpdir/fallback.log" 2>&1 &
fallback_pid=$!
printf '%s\n' "$fallback_pid" > "$tmpdir/fallback.pid"
(
  printf '%s\n' 'bind :5533'
  cat "$tmpdir/ly-route-active.conf"
) > "$tmpdir/smartdns.conf"
ip netns exec "$namespace" "$tmpdir/rootfs/usr/sbin/smartdns" -f -x -p "$tmpdir/smartdns.pid" -c "$tmpdir/smartdns.conf" > "$tmpdir/smartdns.log" 2>&1 &
smartdns_pid=$!
sleep 2
if ! ip netns exec "$namespace" python3 "$tmpdir/dns-client.py"; then
  nl -ba "$tmpdir/smartdns.conf" >&2 || true
  cat "$tmpdir/smartdns.log" >&2 || true
  cat "$tmpdir/upstream.log" >&2 || true
  exit 1
fi
ip netns exec "$namespace" ipset test lyroute_dns_fixed-wan 203.0.113.53 2>&1 | grep -q 'is in set lyroute_dns_fixed-wan'
if ! ip netns exec "$namespace" python3 "$tmpdir/dns-fault-client.py"; then
  cat "$tmpdir/smartdns.log" >&2 || true
  cat "$tmpdir/fallback.log" >&2 || true
  exit 1
fi
if [ -s "$tmpdir/fallback.log" ]; then
  echo 'DNS policy leaked to an unselected fallback upstream' >&2
  cat "$tmpdir/fallback.log" >&2
  exit 1
fi
kill "$smartdns_pid" "$upstream_pid" "$fallback_pid" 2>/dev/null || true
wait "$smartdns_pid" 2>/dev/null || true
wait "$upstream_pid" 2>/dev/null || true
wait "$fallback_pid" 2>/dev/null || true

echo "SmartDNS WAN-pinned upstream, TTL, NODATA, and unavailable-upstream fail-closed verification passed"
