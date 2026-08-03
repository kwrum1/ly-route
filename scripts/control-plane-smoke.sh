#!/usr/bin/env sh
set -eu

: "${LY_ROUTE_SMOKE_URL:=http://127.0.0.1:8080}"
: "${LY_ROUTE_SMOKE_USERNAME:=admin}"
: "${LY_ROUTE_SMOKE_PASSWORD:=secret}"

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-smoke.XXXXXX")
trap 'rm -rf "$tmpdir"' EXIT INT TERM
cookie="$tmpdir/cookies.txt"

api() {
  method="$1"
  path="$2"
  body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -b "$cookie" -c "$cookie" -X "$method" -H 'Content-Type: application/json' -d "$body" "$LY_ROUTE_SMOKE_URL$path"
  else
    curl -fsS -b "$cookie" -c "$cookie" -X "$method" "$LY_ROUTE_SMOKE_URL$path"
  fi
}

curl -fsS -c "$cookie" -H 'Content-Type: application/json' -d "{\"username\":\"$LY_ROUTE_SMOKE_USERNAME\",\"password\":\"$LY_ROUTE_SMOKE_PASSWORD\"}" "$LY_ROUTE_SMOKE_URL/api/v1/auth/login" > "$tmpdir/login.json"

api POST /api/v1/auth/users '{"username":"smoke-operator","role":"readonly","password":"RouteSmoke1"}' > "$tmpdir/user-create.json"
api PATCH /api/v1/auth/users/smoke-operator '{"username":"smoke-operator","role":"admin","password":"RouteSmoke2"}' > "$tmpdir/user-patch.json"

api POST /api/v1/dhcp/servers '{"id":"smoke-management","interface_id":"eth0","subnet":"192.168.88.0/24","pools":["192.168.88.100-192.168.88.199"],"routers":["192.168.88.1"],"name_servers":["192.168.88.1"],"enabled":true}' > "$tmpdir/dhcp-management.json"
api POST /api/v1/dhcp/servers '{"id":"smoke-vpp-lan","interface_id":"lyroute-lan0","subnet":"192.168.30.0/24","pools":["192.168.30.100-192.168.30.199"],"routers":["192.168.30.1"],"name_servers":["192.168.30.1"],"enabled":true}' > "$tmpdir/dhcp-vpp.json"
api POST /api/v1/dns/policies '{"id":"smoke-split","name":"smoke split dns","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"smoke-direct","source_prefixes":["192.168.30.0/24"],"domains":["direct.example"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}},{"id":"smoke-block","source_prefixes":["192.168.30.0/24"],"domains":["blocked.example"],"outcome":{"kind":"reject"}}]}}' > "$tmpdir/dns.json"

api GET /api/v1/runtime/preview > "$tmpdir/runtime-preview.json"
jq -e '([.plan.service_artifacts[] | select(.service=="kea")] | length) == 1' "$tmpdir/runtime-preview.json" >/dev/null
jq -e '.plan.dhcp_servers[] | select(.id=="smoke-management" and .interface_id=="eth0")' "$tmpdir/runtime-preview.json" >/dev/null
jq -e '.plan.dhcp_servers[] | select(.id=="smoke-vpp-lan" and .interface_id=="lyroute-lan0")' "$tmpdir/runtime-preview.json" >/dev/null
jq -e '.plan.service_artifacts[] | select(.service=="smartdns" and (.path | contains("smoke-split")))' "$tmpdir/runtime-preview.json" >/dev/null
jq -e '.plan.dns_policies[] | select(.id=="smoke-split" and .enabled==true)' "$tmpdir/runtime-preview.json" >/dev/null

api DELETE /api/v1/auth/users/smoke-operator > "$tmpdir/user-delete.json"

printf 'control-plane smoke passed: %s\n' "$LY_ROUTE_SMOKE_URL"
