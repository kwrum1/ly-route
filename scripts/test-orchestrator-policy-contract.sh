#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"
go test ./internal/orchestrator ./internal/orchestratorapi -count=1
go test ./internal/httpapi -run 'TestOrchestrator(ObjectGroupsAreIPOnly|RejectsGatewayOnlyRoutesWithTypedCapabilityError|RegistersSharedRuntimeTelemetryAndConfigRoutes)$' -count=1

printf 'orchestrator typed IP-only policy and product-boundary contracts passed\n'
