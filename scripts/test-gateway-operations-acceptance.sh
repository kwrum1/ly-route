#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-"$repo_root/.sisyphus/full-acceptance/evidence/g-operations"}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-operations.XXXXXX")

cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

runtime_tests='TestConfigExportCharacterizationIncludesGatewayDesiredResources|TestSnapshotCharacterizationStoresExportedGatewayPayloadAndHash|TestConfigSnapshotRestoreReplacesDesiredState|TestObjectGroupImportExportAndReferenceDeleteGuard|TestProductionGatewayHTTPCommitsOnlyAfterEightVPPCTLReadbacksAndRestarts|TestRuntimeApplyUsesConfiguredServiceRuntimeAndStatusExposesLastApply|TestRuntimeApplySerializesConcurrentTransactions'
fault_tests='TestAuthenticatedConfigApplyRunsPipelineAndAuditsRollback|TestGatewayRuntimeApplyFailureAuditsRollbackReceipt|TestGatewayConfigApplyTransactionFailureNeverReportsCommittedOrRunning|TestProductConfigImportRejectsCrossProductDuringDryRunWithoutWrites|TestProductConfigImportRejectsForgedOrchestratorGatewayResourceWithoutWrites|TestProductSnapshotRestoreRejectsTamperedPayloadHashWithoutWrites|TestFirmwareUpdateStageRejectsInvalidPackageChecksum|TestProductFirmwareUpdateStageRejectsCrossProductBeforeConfigBackup|TestRestartEvidenceRejectsAfterPayloadDifferentFromPersistedDesiredResource|TestRestartEvidenceRejectsSupplementalPayloadHashDifferentFromPersistedDesiredPlan'

(
  cd "$repo_root/backend"
  go test ./internal/httpapi -count=1 -v -run "^($runtime_tests)$"
) >"$tmp/runtime.log" 2>&1
(
  cd "$repo_root/backend"
  go test ./internal/httpapi -count=1 -v -run "^($fault_tests)$"
) >"$tmp/fault.log" 2>&1

grep -q '^PASS$' "$tmp/runtime.log"
grep -q '^PASS$' "$tmp/fault.log"
rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
cp "$tmp/runtime.log" "$evidence_dir/"
cp "$tmp/fault.log" "$evidence_dir/"
git -C "$repo_root" rev-parse HEAD >"$evidence_dir/commit.txt"
printf 'gateway operations runtime and failure acceptance passed: %s\n' "$evidence_dir"
