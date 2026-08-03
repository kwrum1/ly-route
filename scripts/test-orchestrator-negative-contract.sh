#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# These packages own topology, policy, authorization, zero-write conflict, and
# product-store validation. Running the whole packages prevents a new invalid
# path from bypassing a narrowly named regression list.
go test ./internal/orchestrator ./internal/orchestratorapi -count=1

# The shared HTTP layer must reject Gateway capabilities, domain objects,
# forged imports, and cross-product snapshots before persistence changes.
go test ./internal/httpapi -run 'Test(OrchestratorRejectsGatewayOnlyRoutesWithTypedCapabilityError|OrchestratorObjectGroupsAreIPOnly|ProductConfigImportRejectsCrossProductDuringDryRunWithoutWrites|ProductConfigImportRejectsForgedOrchestratorGatewayResourceWithoutWrites|ProductSnapshotContainsProductAndRejectsCrossProductRestoreWithoutWrites)$' -count=1

printf 'orchestrator invalid ownership, references, loops, privileges and cross-product resources are rejected without writes\n'
