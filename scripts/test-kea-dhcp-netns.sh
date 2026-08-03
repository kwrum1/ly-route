#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
kea_debs=${LY_ROUTE_KEA_DEBS_DIR:-/root/ly-route/runtime-debs/kea-test}

command -v ip >/dev/null
command -v dhclient >/dev/null
command -v python3 >/dev/null
[ -d "$kea_debs" ]

tmpdir=$(mktemp -d)
server_ns=ly-route-kea-server-$$
client_ns=ly-route-kea-client-$$
helper_dir=$(mktemp -d "$repo_root/backend/.kea-e2e.XXXXXX")
client_lease=/var/lib/dhcp/dhclient-ly-route-e2e-$$.leases
client_pid=/run/dhclient-ly-route-e2e-$$.pid
kea_pid=
cleanup() {
	status=$?
	if [ -n "$kea_pid" ]; then
		kill "$kea_pid" 2>/dev/null || true
		wait "$kea_pid" 2>/dev/null || true
	fi
	if [ "$status" -ne 0 ]; then
		[ ! -f "$tmpdir/kea.log" ] || { echo "--- kea.log" >&2; tail -80 "$tmpdir/kea.log" >&2; }
		[ ! -f "$tmpdir/dhclient.log" ] || { echo "--- dhclient.log" >&2; tail -80 "$tmpdir/dhclient.log" >&2; }
	fi
  ip netns del "$server_ns" 2>/dev/null || true
  ip netns del "$client_ns" 2>/dev/null || true
  rm -f "$client_lease" "$client_pid"
  rm -rf "$tmpdir" "$helper_dir"
	exit "$status"
}
trap cleanup EXIT INT TERM

cat > "$helper_dir/main.go" <<'EOF'
package main

import (
  "fmt"
  service "ly-route/backend/internal/runtime/service"
)

func main() {
  artifacts, err := service.RenderKeaDHCP4(service.KeaDHCP4Plan{
    ID: "lan-e2e", InterfaceID: "lan0", Subnet: "192.0.2.0/24",
    Pools: []string{"192.0.2.100-192.0.2.101"}, Routers: []string{"192.0.2.1"}, NameServers: []string{"192.0.2.1"}, LeaseTime: 120,
    Reservations: []service.KeaReservation{{HWAddress: "02:00:00:00:00:02", IPAddress: "192.0.2.50", Hostname: "reserved-client"}},
  })
  if err != nil { panic(err) }
  fmt.Print(artifacts[0].Content)
}
EOF
(cd "$repo_root/backend" && go run "$helper_dir/main.go") > "$tmpdir/kea-dhcp4.conf"
python3 - "$tmpdir/kea-dhcp4.conf" "$tmpdir/kea-leases4.csv" <<'EOF'
import json, sys
path, leases = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    config = json.load(source)
config["Dhcp4"]["lease-database"] = {"type": "memfile", "persist": True, "name": leases}
with open(path, "w", encoding="utf-8") as target:
    json.dump(config, target)
EOF

mkdir -p "$tmpdir/rootfs"
for deb in "$kea_debs"/*.deb; do dpkg-deb -x "$deb" "$tmpdir/rootfs"; done
ip netns add "$server_ns"
ip netns add "$client_ns"
ip link add lan0 type veth peer name client0
ip link set lan0 netns "$server_ns"
ip link set client0 netns "$client_ns"
ip netns exec "$server_ns" ip link set lo up
ip netns exec "$server_ns" ip link set lan0 up
ip netns exec "$server_ns" ip address add 192.0.2.1/24 dev lan0
ip netns exec "$client_ns" ip link set lo up
ip netns exec "$client_ns" ip link set client0 address 02:00:00:00:00:02
ip netns exec "$client_ns" ip link set client0 up

mkdir -p "$tmpdir/leases"
mkdir -p "$tmpdir/run"
chmod 0755 "$tmpdir"
touch "$client_lease"
chmod 0666 "$client_lease"
ip netns exec "$server_ns" env KEA_LOCKFILE_DIR="$tmpdir/run" KEA_PIDFILE_DIR="$tmpdir/run" LD_LIBRARY_PATH="$tmpdir/rootfs/usr/lib/x86_64-linux-gnu" "$tmpdir/rootfs/usr/sbin/kea-dhcp4" -c "$tmpdir/kea-dhcp4.conf" > "$tmpdir/kea.log" 2>&1 &
kea_pid=$!
sleep 1
ip netns exec "$client_ns" dhclient -4 -1 -v -pf "$client_pid" -lf "$client_lease" client0 > "$tmpdir/dhclient.log" 2>&1
ip netns exec "$client_ns" ip -4 address show dev client0 | grep -F '192.0.2.50/' >/dev/null
ip netns exec "$client_ns" ip route show default | grep -F 'via 192.0.2.1' >/dev/null
grep -F 'option domain-name-servers 192.0.2.1;' "$client_lease" >/dev/null
grep -F '192.0.2.50' "$tmpdir/kea.log" >/dev/null
(
  cd "$repo_root/backend"
  LY_ROUTE_KEA_LEASE_INTEGRATION_FILE="$tmpdir/kea-leases4.csv" go test ./internal/runtime/service -run '^TestKeaMemfileLeaseCollectorIntegration$' -count=1
  LY_ROUTE_KEA_LEASE_INTEGRATION_FILE="$tmpdir/kea-leases4.csv" go test ./internal/httpapi -run '^TestKeaLeaseHTTPIntegration$' -count=1
)
kill "$kea_pid" 2>/dev/null || true
wait "$kea_pid" 2>/dev/null || true

# Kea must recover the persisted memfile lease after a daemon restart.
ip netns exec "$server_ns" env KEA_LOCKFILE_DIR="$tmpdir/run" KEA_PIDFILE_DIR="$tmpdir/run" LD_LIBRARY_PATH="$tmpdir/rootfs/usr/lib/x86_64-linux-gnu" "$tmpdir/rootfs/usr/sbin/kea-dhcp4" -c "$tmpdir/kea-dhcp4.conf" > "$tmpdir/kea-restart.log" 2>&1 &
kea_pid=$!
sleep 1
(
  cd "$repo_root/backend"
  LY_ROUTE_KEA_LEASE_INTEGRATION_FILE="$tmpdir/kea-leases4.csv" go test ./internal/httpapi -run '^TestKeaLeaseHTTPIntegration$' -count=1
)
kill "$kea_pid" 2>/dev/null || true
wait "$kea_pid" 2>/dev/null || true
kea_pid=

# Dependency loss must be explicit and must never serve a cached/stale lease.
mv "$tmpdir/kea-leases4.csv" "$tmpdir/kea-leases4.csv.offline"
(
  cd "$repo_root/backend"
  LY_ROUTE_KEA_LEASE_INTEGRATION_FILE="$tmpdir/kea-leases4.csv" LY_ROUTE_KEA_LEASE_EXPECT_UNAVAILABLE=1 go test ./internal/httpapi -run '^TestKeaLeaseHTTPIntegration$' -count=1
)
mv "$tmpdir/kea-leases4.csv.offline" "$tmpdir/kea-leases4.csv"

echo "Kea DHCP lease, API readback, restart continuity, and dependency-loss verification passed"
