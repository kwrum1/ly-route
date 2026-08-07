#!/bin/sh
set -eu

: "${LY_ROUTE_RECOVERY_REPORT:=/var/lib/ly-route/recovery-last.json}"
: "${LY_ROUTE_API_URL:=http://127.0.0.1:8080}"
: "${LY_ROUTE_RUNTIME_CHECK:=/usr/lib/ly-route/runtime-check.sh}"
: "${LY_ROUTE_DNS_SYNC_TOKEN_FILE:=/etc/ly-route/dns-sync.token}"
: "${LY_ROUTE_RECOVERY_ATTEMPTS:=12}"
: "${LY_ROUTE_RECOVERY_RETRY_SECONDS:=5}"
: "${LY_ROUTE_RECOVERY_APPLY_TIMEOUT:=30}"

mkdir -p "$(dirname "$LY_ROUTE_RECOVERY_REPORT")"
timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
runtime_status=unknown
readiness_status=unknown

if "$LY_ROUTE_RUNTIME_CHECK" >/tmp/ly-route-runtime-check.out 2>/tmp/ly-route-runtime-check.err; then
  readiness_status=ready
else
  readiness_status=degraded
fi

apply_status=not_attempted
apply_attempts=0
if command -v curl >/dev/null 2>&1; then
  if curl -fsS "$LY_ROUTE_API_URL/api/v1/runtime/status" >/tmp/ly-route-runtime-status.json 2>/tmp/ly-route-runtime-status.err; then
    runtime_status=queried
  else
    runtime_status=unreachable
  fi

  if [ -r "$LY_ROUTE_DNS_SYNC_TOKEN_FILE" ]; then
    sync_token=$(tr -d '\r\n' < "$LY_ROUTE_DNS_SYNC_TOKEN_FILE")
    if [ -n "$sync_token" ]; then
      attempt=1
      while [ "$attempt" -le "$LY_ROUTE_RECOVERY_ATTEMPTS" ]; do
        apply_attempts=$attempt
        if curl -fsS --max-time "$LY_ROUTE_RECOVERY_APPLY_TIMEOUT" \
          -X POST "$LY_ROUTE_API_URL/api/v1/runtime/apply" \
          -H "X-LY-Route-DNS-Sync-Token: $sync_token" \
          -H 'Content-Type: application/json' \
          -d '{}' > /tmp/ly-route-runtime-apply.json \
          2> /tmp/ly-route-runtime-apply.err; then
          if grep -Eq '"status"[[:space:]]*:[[:space:]]*"(committed|already_applied)"' /tmp/ly-route-runtime-apply.json; then
            apply_status=committed
            break
          fi
          apply_status=accepted
          break
        fi
        apply_status=retrying
        if [ "$attempt" -lt "$LY_ROUTE_RECOVERY_ATTEMPTS" ]; then
          sleep "$LY_ROUTE_RECOVERY_RETRY_SECONDS"
        fi
        attempt=$((attempt + 1))
      done
      if [ "$apply_status" = retrying ]; then
        apply_status=failed
      fi
    else
      apply_status=token_empty
    fi
  else
    apply_status=token_missing
  fi
fi

cat > "$LY_ROUTE_RECOVERY_REPORT" <<EOF
{
  "timestamp": "$timestamp",
  "readiness_status": "$readiness_status",
  "runtime_status": "$runtime_status",
  "runtime_apply_status": "$apply_status",
  "runtime_apply_attempts": $apply_attempts,
  "actions": ["runtime-readiness-check", "runtime-status-query", "runtime-reconcile"],
  "scope": "single-device"
}
EOF
