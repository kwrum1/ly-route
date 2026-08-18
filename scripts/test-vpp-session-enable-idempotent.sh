#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
script="$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/enable-vpp-session.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

cat >"$tmp/vppctl" <<'EOF'
#!/bin/sh
set -eu
state=${TEST_VPP_STATE:?}
calls=${TEST_VPP_CALLS:?}
case "$1" in
  "show session")
    if [ "$(cat "$state")" = enabled ]; then
      # VPP may return a successful empty response when no sessions exist.
      :
    else
      echo "show session: session layer is not enabled"
    fi
    ;;
  "session enable rt-backend rule-table")
    echo enable >>"$calls"
    echo enabled >"$state"
    ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$tmp/vppctl"

run_case() {
  initial=$1
  expected=$2
  state="$tmp/state-$initial"
  calls="$tmp/calls-$initial"
  printf '%s\n' "$initial" >"$state"
  : >"$calls"
  TEST_VPP_STATE="$state" TEST_VPP_CALLS="$calls" \
    LY_ROUTE_VPP_SESSION_LOCK="$tmp/lock-$initial" \
    LY_ROUTE_VPPCTL="$tmp/vppctl" LY_ROUTE_VPP_SESSION_READY_INTERVAL=1 \
    LY_ROUTE_VPP_SESSION_READY_ATTEMPTS=2 "$script"
  actual=$(wc -l <"$calls" | tr -d ' ')
  [ "$actual" = "$expected" ] || {
    echo "$initial: got $actual enable calls, want $expected" >&2
    exit 1
  }
}

run_case enabled 0
run_case disabled 1
echo "VPP session enable idempotency passed"
