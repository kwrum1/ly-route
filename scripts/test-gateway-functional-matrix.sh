#!/usr/bin/env bash
set -uo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
evidence_dir=${LY_ROUTE_FUNCTIONAL_MATRIX_EVIDENCE:-"$repo_root/.sisyphus/full-acceptance/evidence/gateway-functional-matrix"}
mkdir -p "$evidence_dir"

tests=(
  "dns-contract|scripts/test-gateway-dns-contract.sh"
  "dns-negative|scripts/test-gateway-dns-negative-contract.sh"
  "gateway-negative|scripts/test-gateway-negative-contract.sh"
  "security-contract|scripts/test-gateway-security-contract.sh"
  "operations|scripts/test-gateway-operations-acceptance.sh"
  "kea-dhcp|scripts/test-kea-dhcp-netns.sh"
  "nat44|scripts/test-nat44-vpp-netns.sh"
  "smartdns|scripts/test-smartdns-netns.sh"
  "wan-group|scripts/test-wan-group-vpp-netns.sh"
  "xray-failover|scripts/test-xray-fastest-failover-container.sh"
  "dns-transparent|scripts/test-vpp-dns-transparent-abf.sh"
)

failures=0
summary="$evidence_dir/summary.tsv"
printf 'test\tresult\n' >"$summary"
for entry in "${tests[@]}"; do
  name=${entry%%|*}
  command=${entry#*|}
  log="$evidence_dir/$name.log"
  printf '[RUN] %s\n' "$name"
  if (cd "$repo_root" && bash "$command") >"$log" 2>&1; then
    printf '[PASS] %s\n' "$name"
    printf '%s\tpassed\n' "$name" >>"$summary"
  else
    status=$?
    failures=$((failures + 1))
    printf '[FAIL] %s (exit=%s)\n' "$name" "$status"
    tail -n 30 "$log"
    printf '%s\tfailed:%s\n' "$name" "$status" >>"$summary"
  fi
done

printf 'functional matrix complete: %s failures\n' "$failures"
exit "$failures"
