#!/bin/sh
set -eu

: "${LY_ROUTE_RECOVERY_REPORT:=/var/lib/ly-route/recovery-last.json}"
: "${LY_ROUTE_API_URL:=http://127.0.0.1:8080}"

mkdir -p "$(dirname "$LY_ROUTE_RECOVERY_REPORT")"
timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
runtime_status=unknown
readiness_status=unknown

if /usr/lib/ly-route/runtime-check.sh >/tmp/ly-route-runtime-check.out 2>/tmp/ly-route-runtime-check.err; then
  readiness_status=ready
else
  readiness_status=degraded
fi

if command -v curl >/dev/null 2>&1; then
  if curl -fsS "$LY_ROUTE_API_URL/api/v1/runtime/status" >/tmp/ly-route-runtime-status.json 2>/tmp/ly-route-runtime-status.err; then
    runtime_status=queried
  else
    runtime_status=unreachable
  fi
fi

cat > "$LY_ROUTE_RECOVERY_REPORT" <<EOF
{
  "timestamp": "$timestamp",
  "readiness_status": "$readiness_status",
  "runtime_status": "$runtime_status",
  "actions": ["runtime-readiness-check", "runtime-status-query"],
  "scope": "single-device"
}
EOF
