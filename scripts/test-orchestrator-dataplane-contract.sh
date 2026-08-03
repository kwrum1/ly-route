#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# The VPP package owns common-tier selection, freshness, measured-candidate
# ordering, DPDK prerequisites, fail-closed behavior, and production bond CLI.
go test ./internal/runtime/vpp -count=1

# Orchestrator topology must preserve independent LAN/WAN bond membership and
# reject reuse by orchestration groups or management ownership violations.
go test ./internal/orchestrator -run 'TestParseTopology_(acceptsIndependentBondEndpoints|rejects_invalid_interface_ownership)$' -count=1

printf 'orchestrator device-wide native-first common-tier and bond-member dataplane contracts passed\n'
