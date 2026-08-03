#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

export LY_ROUTE_VPP_WORKERS=${LY_ROUTE_VPP_WORKERS:-2}
bash "$repo_root/scripts/test-vpp-smart-qos-netns.sh"

cd "$repo_root/backend"
go test -v ./internal/runtime/vpp -run 'TestSupplementalQoSOwnsSmartQoSApplyAndRollbackDisable|TestValidateSmartQoSDisabledReadbackRejectsStillEnabledInterface'
