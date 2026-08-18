#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-apply-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

mkdir -p "$tmp/bin" "$tmp/out"
cat >"$tmp/bin/vppctl" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$LY_ROUTE_TEST_VPPCTL_LOG"
case "$*" in
  'show version') printf '%s\n' 'vpp v25.10' ;;
  'show tap') ;;
  'show acl-plugin acl') printf '%s\n' 'acl-index 2 count 1 tag {ly-route-route_10}' ;;
  'show abf policy 16469') printf '%s\n' 'abf:[0]: policy:16469 acl:2' ' path-list:[1] locks:1 flags:shared' '  path:[2] pl-index:1 ip4 weight=1 pref=0 deag:' '   fib-index:3' ;;
  'set acl-plugin acl index 2 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535 tag ly-route-route_10') ;;
  'abf policy add id 16469 acl 2 via ip4-lookup-in-table 42') echo 'duplicate ABF add reached VPP' >&2; exit 99 ;;
  *) ;;
esac
EOF
chmod 0755 "$tmp/bin/vppctl"

cat >"$tmp/operations.json" <<'EOF'
{"operations":[{"Name":"vpp.route-policy","Resource":"route-10","Payload":{},"VPPCtlCommands":["set acl-plugin acl index 28031 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535 tag ly-route-route_10","abf policy add id 16469 acl 28031 via ip4-lookup-in-table 42"]}]}
EOF

LY_ROUTE_RUNTIME_DEBS_DIR="$tmp/out" "$repo/scripts/build-runtime-debs.sh" vpp-apply
deb=$(find "$tmp/out" -maxdepth 1 -name 'ly-route-vpp-apply_*_*.deb' -print -quit)
test -n "$deb"
dpkg-deb -x "$deb" "$tmp/root"

: >"$tmp/vppctl.log"
PATH="$tmp/bin:$PATH" \
LY_ROUTE_TEST_VPPCTL_LOG="$tmp/vppctl.log" \
LY_ROUTE_VPP_COMMAND_MAP="$tmp/missing-command-map.json" \
LY_ROUTE_VPP_RECEIPT="$tmp/receipt.json" \
"$tmp/root/usr/lib/ly-route/vpp-apply" "$tmp/operations.json"

if grep -q '^abf policy add ' "$tmp/vppctl.log"; then
  echo 'persisted replay appended an existing ABF path' >&2
  exit 1
fi
grep -q '"status": "already-applied"' "$tmp/receipt.json"
echo 'vpp-apply ABF replay is idempotent'
