#!/bin/sh
set -eu
umask 077

: "${LY_ROUTE_VPPCTL:=vppctl}"
: "${LY_ROUTE_VPP_SESSION_READY_ATTEMPTS:=30}"
: "${LY_ROUTE_VPP_SESSION_READY_INTERVAL:=1}"
: "${LY_ROUTE_VPP_SESSION_LOCK:=/run/ly-route-vpp-session-enable.lock}"

case "$LY_ROUTE_VPP_SESSION_READY_ATTEMPTS:$LY_ROUTE_VPP_SESSION_READY_INTERVAL" in
  *[!0123456789:]*|0:*|*:0)
    echo "VPP session readiness retry settings must be positive integers" >&2
    exit 1
    ;;
esac

session_ready() {
  output=$("$LY_ROUTE_VPPCTL" "show session" 2>&1) || return 1
  case "$output" in
    *"session layer is not enabled"*) return 1 ;;
    *) return 0 ;;
  esac
}

# VPP can report an empty successful `show session` result when the layer is
# enabled but has no sessions. Serialize the check-and-toggle sequence because
# this helper can be reached both from VPP ExecStartPost and its readiness unit.
mkdir -p "$(dirname "$LY_ROUTE_VPP_SESSION_LOCK")"
exec 9>"$LY_ROUTE_VPP_SESSION_LOCK"
flock -w "$LY_ROUTE_VPP_SESSION_READY_ATTEMPTS" 9 || {
  echo "timed out waiting for VPP session readiness lock" >&2
  exit 1
}

attempt=1
while :; do
  if session_ready; then
    exit 0
  fi
  # In VPP 25.10 this CLI behaves as a toggle: invoking it while the session
  # layer is active disables the layer and disconnects every VCL application.
  # Only invoke it after readback proves the layer is disabled.
  "$LY_ROUTE_VPPCTL" "session enable rt-backend rule-table" >/dev/null 2>&1 || true
  if session_ready; then
    exit 0
  fi
  if [ "$attempt" -ge "$LY_ROUTE_VPP_SESSION_READY_ATTEMPTS" ]; then
    echo "VPP session layer did not become ready" >&2
    exit 1
  fi
  sleep "$LY_ROUTE_VPP_SESSION_READY_INTERVAL"
  attempt=$((attempt + 1))
done
