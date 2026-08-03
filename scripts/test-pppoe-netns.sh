#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmpdir=$(mktemp -d)
helper_dir=$(mktemp -d "$repo_root/backend/.pppoe-e2e.XXXXXX")
backup_dir=$(mktemp -d)
server_options=/etc/ppp/pppoe-server-options
server_pap=/etc/ppp/pap-secrets
server_chap=/etc/ppp/chap-secrets
namespaces=""

cleanup() {
  status=$?
  for namespace in $namespaces; do
    ip netns pids "$namespace" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
    ip netns del "$namespace" 2>/dev/null || true
  done
  for path in "$server_options" "$server_pap" "$server_chap"; do
    base=$(basename "$path")
    if [ -f "$backup_dir/$base" ]; then
      cp -f "$backup_dir/$base" "$path"
    else
      rm -f "$path"
    fi
  done
  if [ "$status" -ne 0 ]; then
    [ ! -f "$tmpdir/pap-cpe.log" ] || tail -80 "$tmpdir/pap-cpe.log" >&2
    [ ! -f "$tmpdir/chap-cpe.log" ] || tail -80 "$tmpdir/chap-cpe.log" >&2
    [ ! -f "$tmpdir/pap-ac.log" ] || { echo "--- pap access concentrator" >&2; tail -120 "$tmpdir/pap-ac.log" >&2; }
    [ ! -f "$tmpdir/chap-ac.log" ] || { echo "--- chap access concentrator" >&2; tail -120 "$tmpdir/chap-ac.log" >&2; }
    [ ! -f "$tmpdir/pap-server-pppd.log" ] || { echo "--- pap server pppd" >&2; tail -120 "$tmpdir/pap-server-pppd.log" >&2; }
    [ ! -f "$tmpdir/lifecycle-root/pppd-client.log" ] || { echo "--- lifecycle pppd" >&2; tail -120 "$tmpdir/lifecycle-root/pppd-client.log" >&2; }
    [ ! -f "$tmpdir/lifecycle-ac.log" ] || { echo "--- lifecycle access concentrator" >&2; tail -80 "$tmpdir/lifecycle-ac.log" >&2; }
  fi
  rm -rf "$tmpdir" "$helper_dir" "$backup_dir"
  exit "$status"
}
trap cleanup EXIT INT TERM

cat > "$helper_dir/main.go" <<'EOF'
package main

import (
  "os"
  "path/filepath"
  service "ly-route/backend/internal/runtime/service"
)

func main() {
  artifacts, err := service.RenderPPPoEConfig([]service.PPPoEPeer{
    {ID: "wan-pap", Interface: "cpe-pap0", Username: "subscriber-pap", Password: "pap-secret"},
    {ID: "wan-chap", Interface: "cpe-chap0", Username: "subscriber-chap", Password: "chap-secret"},
  })
  if err != nil { panic(err) }
  out := os.Args[1]
  for _, artifact := range artifacts {
    if err := os.WriteFile(filepath.Join(out, filepath.Base(artifact.Path)), []byte(artifact.Content), 0600); err != nil { panic(err) }
  }
}
EOF
(cd "$repo_root/backend" && go run "$helper_dir/main.go" "$tmpdir")

for path in "$server_options" "$server_pap" "$server_chap"; do
  base=$(basename "$path")
  [ ! -e "$path" ] || cp -a "$path" "$backup_dir/$base"
done

grep -F '"id": "wan-pap"' "$tmpdir/ly-route-wan-pap.json" >/dev/null
grep -F '"wan_interface": "lyroute-cpe-pap0"' "$tmpdir/ly-route-wan-pap.json" >/dev/null
grep -F '"id": "wan-chap"' "$tmpdir/ly-route-wan-chap.json" >/dev/null
grep -F '"wan_interface": "lyroute-cpe-chap0"' "$tmpdir/ly-route-wan-chap.json" >/dev/null
printf '%s\n' 'subscriber-pap * pap-secret *' > "$tmpdir/pap-secrets"
printf '%s\n' 'subscriber-chap * chap-secret *' > "$tmpdir/chap-secrets"
grep -F 'subscriber-pap' "$tmpdir/pap-secrets" >/dev/null
grep -F 'subscriber-chap' "$tmpdir/chap-secrets" >/dev/null
cat > "$tmpdir/ly-route-wan-pap" <<'EOF'
plugin rp-pppoe.so
nic-cpe-pap0
user subscriber-pap
password pap-secret
noauth
noipdefault
defaultroute
ifname ppp-wan-pap
mtu 1492
mru 1492
EOF
cat > "$tmpdir/ly-route-wan-chap" <<'EOF'
plugin rp-pppoe.so
nic-cpe-chap0
user subscriber-chap
password chap-secret
noauth
noipdefault
defaultroute
ifname ppp-wan-chap
mtu 1492
mru 1492
EOF

run_session() {
  mode=$1
  client_id=$2
  server_interface=$3
  client_interface=$4
  peer_id=$5
  expected_address=$6
  auth_phrase=$7
  ac_ns="ly-route-pppoe-${mode}-ac-$$"
  cpe_ns="ly-route-pppoe-${mode}-cpe-$$"
  namespaces="$namespaces $ac_ns $cpe_ns"
  ip netns add "$ac_ns"
  ip netns add "$cpe_ns"
  ip link add "$server_interface" type veth peer name "$client_interface"
  ip link set "$server_interface" netns "$ac_ns"
  ip link set "$client_interface" netns "$cpe_ns"
  ip netns exec "$ac_ns" ip link set lo up
  ip netns exec "$ac_ns" ip link set "$server_interface" up
  ip netns exec "$cpe_ns" ip link set lo up
  ip netns exec "$cpe_ns" ip link set "$client_interface" up

  {
    printf '%s\n' "require-$mode"
    printf '%s\n' "debug"
    printf 'logfile %s\n' "$tmpdir/$mode-server-pppd.log"
  } > "$server_options"
  case "$mode" in
    pap) cp "$tmpdir/pap-secrets" "$server_pap"; cp "$tmpdir/pap-secrets" "$server_chap" ;;
    chap) cp "$tmpdir/chap-secrets" "$server_pap"; cp "$tmpdir/chap-secrets" "$server_chap" ;;
  esac

  ip netns exec "$ac_ns" pppoe-server -q /usr/sbin/pppd -I "$server_interface" -O "$server_options" -L 10.67.0.1 -R "$expected_address" -N 1 -C LY-ROUTE-TEST -X "$tmpdir/$mode-server.pid" >"$tmpdir/$mode-ac.log" 2>&1 &
  sleep 1
  ip netns exec "$ac_ns" ps -ef >>"$tmpdir/$mode-ac.log" 2>&1 || true
  ip netns exec "$cpe_ns" pppd file "$tmpdir/ly-route-$peer_id" nodetach debug logfd 2 >"$tmpdir/$mode-cpe.log" 2>&1 &
  client_pid=$!
  attempt=0
  while [ "$attempt" -lt 15 ]; do
    if ip netns exec "$cpe_ns" ip -4 address show dev "ppp-$peer_id" 2>/dev/null | grep -F "$expected_address" >/dev/null; then
      break
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  ip netns exec "$cpe_ns" ip -4 address show dev "ppp-$peer_id" | grep -F "$expected_address" >/dev/null
  ip netns exec "$cpe_ns" ip -6 address show dev "ppp-$peer_id" | grep -F 'inet6 fe80:' >/dev/null
  grep -F "$auth_phrase" "$tmpdir/$mode-cpe.log" >/dev/null
  timeout 12 ip netns exec "$cpe_ns" ping -c 3 -W 2 10.67.0.1 >/dev/null
  ip netns exec "$cpe_ns" ip -s link show dev "ppp-$peer_id" | grep -E 'RX:|TX:' >/dev/null
  kill "$client_pid" 2>/dev/null || true
  # Reap the foreground shell job before it can emit a misleading cleanup notice.
  wait "$client_pid" 2>/dev/null || true
  ip netns pids "$ac_ns" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  ip netns pids "$cpe_ns" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  ip netns del "$ac_ns"
  ip netns del "$cpe_ns"
  namespaces=""
}

run_session pap pap ac-pap0 cpe-pap0 wan-pap 10.67.0.10 'PAP authentication succeeded'
run_session chap chap ac-chap0 cpe-chap0 wan-chap 10.67.0.10 'CHAP authentication succeeded'

run_lifecycle() {
  ac_ns="ly-route-pppoe-live-ac-$$"
  cpe_ns="ly-route-pppoe-live-cpe-$$"
  namespaces="$namespaces $ac_ns $cpe_ns"
  ip netns add "$ac_ns"
  ip netns add "$cpe_ns"
  ip link add ac-live0 type veth peer name cpe-live0
  ip link set ac-live0 netns "$ac_ns"
  ip link set cpe-live0 netns "$cpe_ns"
  ip netns exec "$ac_ns" ip link set lo up
  ip netns exec "$ac_ns" ip link set ac-live0 up
  ip netns exec "$cpe_ns" ip link set lo up
  ip netns exec "$cpe_ns" ip link set cpe-live0 up

  {
    printf '%s\n' 'require-chap'
  } > "$server_options"
  lifecycle_root="$tmpdir/lifecycle-root"
  mkdir -p "$lifecycle_root"
  mkdir -p "$lifecycle_root/etc/ppp"
  printf '%s\n' 'subscriber-live * live-secret *' > "$lifecycle_root/etc/ppp/pap-secrets"
  printf '%s\n' 'subscriber-live * live-secret *' > "$lifecycle_root/etc/ppp/chap-secrets"
  mkdir -p "$lifecycle_root/etc/ppp/peers"
  cat > "$lifecycle_root/etc/ppp/peers/ly-route-pppoe@ly-route-wan-live" <<'EOF'
plugin rp-pppoe.so
nic-cpe-live0
user subscriber-live
password live-secret
noauth
noipdefault
defaultroute
ifname ppp-wan-live
mtu 1492
mru 1492
EOF
  ip netns exec "$ac_ns" pppoe-server -I ac-live0 -L 10.67.0.1 -R 10.67.0.10 -N 4 -C LY-ROUTE-LIFECYCLE -X "$tmpdir/lifecycle-server.pid" >"$tmpdir/lifecycle-ac.log" 2>&1 &
  sleep 1
  (
    cd "$repo_root/backend"
    ip netns exec "$cpe_ns" env LY_ROUTE_PPPOE_LIFECYCLE_ROOT="$lifecycle_root" LY_ROUTE_PPPOE_CLIENT_INTERFACE=cpe-live0 go test ./internal/httpapi -run '^TestPPPoELifecycleHTTPIntegration$' -count=1 -timeout=60s
  )
  grep -F 'CHAP authentication succeeded' "$lifecycle_root/pppd-client.log" >/dev/null

  # Dependency loss must fail explicitly without stale interface or VPP route handoff.
  ip netns pids "$ac_ns" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  sleep 1
  (
    cd "$repo_root/backend"
    ip netns exec "$cpe_ns" env LY_ROUTE_PPPOE_LIFECYCLE_ROOT="$lifecycle_root" LY_ROUTE_PPPOE_CLIENT_INTERFACE=cpe-live0 LY_ROUTE_PPPOE_EXPECT_UNAVAILABLE=1 go test ./internal/httpapi -run '^TestPPPoELifecycleHTTPIntegration$' -count=1 -timeout=60s
  )

  # Restart the access concentrator and prove API recovery with a fresh session.
  ip netns exec "$ac_ns" pppoe-server -I ac-live0 -L 10.67.0.1 -R 10.67.0.10 -N 4 -C LY-ROUTE-LIFECYCLE -X "$tmpdir/lifecycle-server-recovery.pid" >>"$tmpdir/lifecycle-ac.log" 2>&1 &
  sleep 1
  (
    cd "$repo_root/backend"
    ip netns exec "$cpe_ns" env LY_ROUTE_PPPOE_LIFECYCLE_ROOT="$lifecycle_root" LY_ROUTE_PPPOE_CLIENT_INTERFACE=cpe-live0 go test ./internal/httpapi -run '^TestPPPoELifecycleHTTPIntegration$' -count=1 -timeout=60s
  )
  ip netns pids "$ac_ns" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  ip netns pids "$cpe_ns" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  ip netns del "$ac_ns"
  ip netns del "$cpe_ns"
  namespaces=""
}

run_lifecycle

echo "PPPoE PAP/CHAP, IPCP, IPv6CP, packet-flow, API lifecycle, dependency-loss, and recovery verification passed"
