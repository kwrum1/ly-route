#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
builder="$repo_root/scripts/build-controller-shell.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-controller-shell-test.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

assert_single_file() {
  directory=$1
  name=$2
  count=$(find "$directory" -type f -name "$name" | wc -l | tr -d ' ')
  if [ "$count" -ne 1 ]; then
    echo "expected exactly one $name in $directory, found $count" >&2
    exit 1
  fi
}

assert_manifest_product() {
  bundle=$1
  product=$2
  node - "$bundle/capabilities.json" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [manifestPath, product] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
if (manifest.product !== product) {
  throw new Error(`capability manifest product ${manifest.product} does not match ${product}`);
}
NODE
}

assert_absent() {
  bundle=$1
  forbidden=$2
  if grep -R -F -- "$forbidden" "$bundle" >/dev/null 2>&1; then
    echo "forbidden Orchestrator artifact content: $forbidden" >&2
    exit 1
  fi
}

# Given the selected product profiles.
gateway="$tmp/gateway"
orchestrator="$tmp/orchestrator"

# When each static bundle is built.
"$builder" --product gateway --out "$gateway"
"$builder" --product orchestrator --out "$orchestrator"

# Then each emitted artifact has exactly one product entrypoint and capability manifest.
for product in gateway orchestrator; do
  bundle="$tmp/$product"
  assert_single_file "$bundle" app.js
  assert_single_file "$bundle" capabilities.json
  assert_manifest_product "$bundle" "$product"
  grep -F "window.LY_ROUTE_PRODUCT_ENTRYPOINT = \"$product\";" "$bundle/app.js" >/dev/null
  [ ! -e "$bundle/mock-api.js" ]
done

grep -F 'network/wangroup_manager' "$gateway/app.js" >/dev/null
grep -F '/api/v1/gateway/nat/port-maps' "$gateway/app.js" >/dev/null
grep -F '/api/v1/objects/ip-groups' "$orchestrator/app.js" >/dev/null
grep -F '"object_groups"' "$orchestrator/capabilities.json" >/dev/null

for forbidden in \
  'WAN群组' '路由/NAT' '端口映射' 'DNS管控' 'DHCP服务' 'Top域名' \
  '域名管理' '代理出口' 'OAF' 'DPI' 'NAT' \
  '/api/v1/mode' '/api/v1/gateway/' '/api/v1/proxy/' '/api/v1/dns/' \
  '/api/v1/dhcp/' '/api/v1/objects/groups' '/api/v1/firmware/' '/api/v1/telemetry/top-domains'; do
  assert_absent "$orchestrator" "$forbidden"
done

# Given a mismatched or malformed capability manifest.
if "$builder" --product gateway --manifest "$repo_root/packaging/product-profiles/orchestrator.json" --out "$tmp/mismatch" >"$tmp/mismatch.out" 2>"$tmp/mismatch.err"; then
  echo "gateway build accepted an Orchestrator capability manifest" >&2
  exit 1
fi
grep -F 'manifest product mismatch' "$tmp/mismatch.err" >/dev/null
printf '{' > "$tmp/malformed.json"
if "$builder" --product gateway --manifest "$tmp/malformed.json" --out "$tmp/malformed" >"$tmp/malformed.out" 2>"$tmp/malformed.err"; then
  echo "gateway build accepted malformed capability manifest" >&2
  exit 1
fi
grep -F 'invalid capability manifest JSON' "$tmp/malformed.err" >/dev/null

printf 'Controller-shell product isolation tests passed\n'
