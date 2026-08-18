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
jobs=${LY_ROUTE_TEST_JOBS:-4}
case "$jobs" in
  ''|*[!0-9]*|0) echo "LY_ROUTE_TEST_JOBS must be a positive integer" >&2; exit 2 ;;
esac

run_one() {
  entry=$1
  name=${entry%%|*}
  command=${entry#*|}
  log="$evidence_dir/$name.log"
  if (cd "$repo_root" && bash "$command") >"$log" 2>&1; then
    printf 'passed\n' >"$evidence_dir/$name.result"
  else
    status=$?
    printf 'failed:%s\n' "$status" >"$evidence_dir/$name.result"
  fi
}

wait_batch() {
  for pid in "$@"; do
    wait "$pid" || true
  done
}

pids=()
for entry in "${tests[@]}"; do
  name=${entry%%|*}
  rm -f "$evidence_dir/$name.result"
  printf '[RUN] %s\n' "$name"
  run_one "$entry" &
  pids+=("$!")
  if [ "${#pids[@]}" -ge "$jobs" ]; then
    wait_batch "${pids[@]}"
    pids=()
  fi
done
wait_batch "${pids[@]}"

for entry in "${tests[@]}"; do
  name=${entry%%|*}
  result=$(cat "$evidence_dir/$name.result")
  printf '%s\t%s\n' "$name" "$result" >>"$summary"
  if [ "$result" = passed ]; then
    printf '[PASS] %s\n' "$name"
    continue
  fi
  failures=$((failures + 1))
  printf '[FAIL] %s (%s)\n' "$name" "$result"
  tail -n 30 "$evidence_dir/$name.log"
done

printf 'functional matrix complete: %s failures\n' "$failures"
exit "$failures"
