#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

control_env="$tmp/control-api.env"
cat > "$control_env" <<'EOF'
LY_ROUTE_MANAGEMENT_INTERFACE=old-management
LY_ROUTE_LAN_INTERFACE=legacy-auto-data
LY_ROUTE_LAN_CIDR=192.168.1.1/24
LY_ROUTE_OWNER_EXPLICIT_SETTING=preserved
EOF

"$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/migrate-control-env.sh" "$control_env" eth0 192.168.88.254/24 192.168.88.1
"$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/migrate-control-env.sh" "$control_env" eth0 192.168.88.254/24 192.168.88.1

grep -q '^LY_ROUTE_MANAGEMENT_INTERFACE=eth0$' "$control_env"
grep -q '^LY_ROUTE_LAN_CIDR=192.168.88.254/24$' "$control_env"
grep -q '^LY_ROUTE_MANAGEMENT_GATEWAY=192.168.88.1$' "$control_env"
grep -q '^LY_ROUTE_OWNER_EXPLICIT_SETTING=preserved$' "$control_env"
test "$(grep -c '^LY_ROUTE_MANAGEMENT_INTERFACE=' "$control_env")" -eq 1
test "$(grep -c '^LY_ROUTE_LAN_CIDR=' "$control_env")" -eq 1
test "$(grep -c '^LY_ROUTE_MANAGEMENT_GATEWAY=' "$control_env")" -eq 1
if grep -q '^LY_ROUTE_LAN_INTERFACE=' "$control_env"; then
  echo "legacy automatic data assignment survived migration" >&2
  exit 1
fi

printf 'Firstboot environment migration scenarios passed\n'
