#!/bin/sh
set -eu

: "${LY_ROUTE_RECOVERY_REPORT:=/var/lib/ly-route/recovery-last.json}"
: "${LY_ROUTE_API_URL:=http://127.0.0.1:8080}"
: "${LY_ROUTE_RUNTIME_CHECK:=/usr/lib/ly-route/runtime-check.sh}"
: "${LY_ROUTE_DNS_SYNC_TOKEN_FILE:=/etc/ly-route/dns-sync.token}"
: "${LY_ROUTE_PPPOE_CONFIG_DIR:=/etc/ly-route/pppoe}"
: "${LY_ROUTE_PPPOE_WAIT_SECONDS:=45}"
: "${LY_ROUTE_RECOVERY_ATTEMPTS:=12}"
: "${LY_ROUTE_RECOVERY_RETRY_SECONDS:=5}"
# A complete runtime transaction can restart VPP-side DNS adapters, Xray,
# SmartDNS, and Kea.  It routinely exceeds 30 seconds on a cold PPPoE link;
# timing out earlier queues overlapping transactions with stale PPPoE paths.
: "${LY_ROUTE_RECOVERY_APPLY_TIMEOUT:=90}"

mkdir -p "$(dirname "$LY_ROUTE_RECOVERY_REPORT")"
timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
runtime_status=unknown
readiness_status=unknown
pppoe_restore_status=not_configured
pppoe_restore_count=0

restore_pppoe_sessions() {
  [ -d "$LY_ROUTE_PPPOE_CONFIG_DIR" ] || return 0
  set -- "$LY_ROUTE_PPPOE_CONFIG_DIR"/*.json
  [ -f "$1" ] || return 0

  pppoe_records=$(python3 - "$@" <<'PY'
import json
import os
import re
import sys

safe_name = re.compile(r"^[A-Za-z0-9_.@-]+$")
for path in sorted(sys.argv[1:]):
    name = os.path.basename(path).removesuffix(".json")
    if not safe_name.fullmatch(name):
        raise SystemExit(f"unsafe PPPoE config name: {name}")
    with open(path, "r", encoding="utf-8") as source:
        config = json.load(source)
    status_file = str(config.get("status_file", "")).strip()
    if not status_file.startswith("/run/ly-route/pppoe/"):
        raise SystemExit(f"unsafe PPPoE status path: {status_file}")
    print(f"{name}|{status_file}")
PY
  ) || {
    pppoe_restore_status=invalid_config
    return 0
  }
  [ -n "$pppoe_records" ] || return 0

  pppoe_restore_status=starting
  for record in $pppoe_records; do
    config_name=${record%%|*}
    pppoe_restore_count=$((pppoe_restore_count + 1))
    if ! systemctl start --no-block "ly-route-pppoe@$config_name.service"; then
      pppoe_restore_status=start_failed
    fi
  done
  [ "$pppoe_restore_status" = start_failed ] && return 0

  deadline=$(($(date +%s) + LY_ROUTE_PPPOE_WAIT_SECONDS))
  while [ "$(date +%s)" -le "$deadline" ]; do
    all_active=true
    for record in $pppoe_records; do
      config_name=${record%%|*}
      if ! systemctl is-active --quiet "ly-route-pppoe@$config_name.service"; then
        all_active=false
        break
      fi
    done
    if [ "$all_active" = true ] && PPPOE_RECORDS="$pppoe_records" python3 - <<'PY'
import json
import os

for record in os.environ["PPPOE_RECORDS"].split():
    _, path = record.split("|", 1)
    try:
        with open(path, "r", encoding="utf-8") as source:
            status = json.load(source)
    except (OSError, UnicodeError, json.JSONDecodeError):
        raise SystemExit(1)
    if status.get("state") != "connected" or not status.get("interface"):
        raise SystemExit(1)
PY
    then
      pppoe_restore_status=running
      return 0
    fi
    sleep 1
  done
  pppoe_restore_status=timeout
}

restore_pppoe_sessions

if "$LY_ROUTE_RUNTIME_CHECK" >/tmp/ly-route-runtime-check.out 2>/tmp/ly-route-runtime-check.err; then
  readiness_status=ready
else
  readiness_status=degraded
fi

apply_status=not_attempted
apply_attempts=0
if command -v curl >/dev/null 2>&1; then
  if curl -fsS "$LY_ROUTE_API_URL/api/v1/runtime/status" >/tmp/ly-route-runtime-status.json 2>/tmp/ly-route-runtime-status.err; then
    runtime_status=$(python3 - /tmp/ly-route-runtime-status.json <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as source:
    print(str(json.load(source).get("status", "unknown")))
PY
    ) || runtime_status=invalid
  else
    runtime_status=unreachable
  fi

  # A running control plane does not prove that the VPP paths still point at
  # the current PPPoE session.  Native PPPoE reconnects may create a new
  # session interface while the persisted runtime status remains "running".
  # Always reconcile after restoring sessions; the transaction is idempotent
  # and its readback removes stale paths before installing the new ones.
  if [ "$readiness_status" != ready ]; then
    apply_status=blocked_readiness
  elif [ -r "$LY_ROUTE_DNS_SYNC_TOKEN_FILE" ]; then
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
          apply_result=$(python3 - /tmp/ly-route-runtime-apply.json <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as source:
    print(str(json.load(source).get("status", "unknown")))
PY
          ) || apply_result=invalid
          if [ "$apply_result" = committed ] || [ "$apply_result" = already_applied ]; then
            apply_status=committed
            break
          fi
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
  "pppoe_restore_status": "$pppoe_restore_status",
  "pppoe_restore_count": $pppoe_restore_count,
  "runtime_status": "$runtime_status",
  "runtime_apply_status": "$apply_status",
  "runtime_apply_attempts": $apply_attempts,
  "actions": ["pppoe-session-restore", "runtime-readiness-check", "runtime-status-query", "runtime-reconcile"],
  "scope": "single-device"
}
EOF
