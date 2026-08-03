#!/usr/bin/env sh
set -eu

if [ -x /usr/local/go/bin/go ]; then
  export PATH=/usr/local/go/bin:$PATH
fi

# Runs the currently executable, real packet/protocol acceptance subset and
# records only test summaries. The full-acceptance registry remains the source
# of truth for feature-level release status.
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-"$repo_root/.sisyphus/full-acceptance/evidence/latest-container-run"}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo 'container acceptance requires root for namespaces, VPP, and Docker' >&2
    exit 1
  fi
}

run_case() {
  name=$1
  script=$2
  log="$evidence_dir/$name.log"
  printf '==> %s\n' "$name"
  case "$script" in
    *.mjs) runner=node ;;
    *) runner=bash ;;
  esac
  if timeout "${LY_ROUTE_ACCEPTANCE_CASE_TIMEOUT:-240}" "$runner" "$repo_root/$script" >"$log" 2>&1; then
    printf 'passed %s\n' "$name" | tee -a "$evidence_dir/summary.txt"
  else
    status=$?
    printf 'failed %s (exit %s); see %s\n' "$name" "$status" "$log" >&2
    printf 'failed %s (exit %s)\n' "$name" "$status" >>"$evidence_dir/summary.txt"
    exit "$status"
  fi
}

require_root
command -v timeout >/dev/null 2>&1
command -v bash >/dev/null 2>&1
mkdir -p "$evidence_dir"
: >"$evidence_dir/summary.txt"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
date -u +%Y-%m-%dT%H:%M:%SZ >"$evidence_dir/executed-at.txt"

run_case vpp-vcl-session-rule scripts/test-vpp-vcl-session-rule.sh
run_case vpp-native-attachment-fail-closed scripts/test-vpp-native-attachment-fail-closed.sh
run_case vpp-dns-source-routing scripts/test-dns-vpp-proxy-source-routing.sh
run_case vpp-dns-adapter-packet-flow scripts/test-vpp-smartdns-packet-flow.sh
run_case vpp-dns-transparent-arbitrary-target scripts/test-vpp-dns-transparent-abf.sh
run_case shared-lan-management-lcp scripts/test-shared-lan-lcp-netns.sh
run_case gateway-network-transaction scripts/test-gateway-network-transaction-vpp.sh
run_case smartdns-ttl scripts/test-smartdns-netns.sh
run_case kea-dhcp scripts/test-kea-dhcp-netns.sh
run_case pppoe scripts/test-pppoe-netns.sh
run_case nat44 scripts/test-nat44-vpp-netns.sh
run_case security-acl scripts/test-security-acl-vpp.sh
run_case security-generation scripts/test-security-generation-vpp.sh
run_case dns-priority-vpp-route-policy scripts/test-route-policy-vpp.sh
run_case wan-groups scripts/test-wan-group-vpp-netns.sh
run_case user-rate-limit scripts/test-vpp-policer-netns.sh
run_case smart-qos scripts/test-vpp-smart-qos-multiworker.sh
run_case xray-config-binary scripts/test-xray-runtime.sh
run_case proxy-fastest-failover scripts/test-xray-fastest-failover-container.sh
run_case gateway-product-contract scripts/test-gateway-product-contract.sh
run_case gateway-negative-contract scripts/test-gateway-negative-contract.sh
run_case gateway-services-contract scripts/test-gateway-services-contract.sh
run_case gateway-security-contract scripts/test-gateway-security-contract.sh
run_case gateway-dns-contract scripts/test-gateway-dns-contract.sh
run_case gateway-dns-negative-contract scripts/test-gateway-dns-negative-contract.sh
run_case orchestrator-product-contract scripts/test-orchestrator-product-contract.sh
run_case orchestrator-policy-contract scripts/test-orchestrator-policy-contract.sh
run_case orchestrator-negative-contract scripts/test-orchestrator-negative-contract.sh
run_case orchestrator-dataplane-contract scripts/test-orchestrator-dataplane-contract.sh
run_case orchestrator-transparent scripts/test-vpp-orchestrator-transparent.sh
run_case orchestrator-bond-policy scripts/test-orchestrator-vpp-bond-policy.sh
run_case gateway-browser-ui scripts/test-controller-shell-gateway-ui.mjs
run_case orchestrator-browser-ui scripts/test-controller-shell-orchestrator-ui.mjs

printf 'container acceptance subset passed\n' | tee -a "$evidence_dir/summary.txt"
