#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# Keep the fail-closed boundary independently visible in release evidence. The
# HTTP test also proves rejected requests leave zero policy writes and produce
# one failure audit record per attempted mutation.
go test ./internal/runtime/dns \
  -run 'TestCompilePolicyRejectsImplicitOrAmbiguousDirectResolver|TestDecideUnavailableSelectedResolverStopsWithoutLowerRuleFallback|TestCompilePolicyDefaultsUnspecifiedMissToExplicitReject|TestDNSPolicyMissRejectDoesNotLeakBridgeOrConnectionLimit' \
  -count=1

go test ./internal/httpapi \
  -run 'TestDNSPolicyRejectsImplicitFallbackAndUnsupportedClaimsWithZeroWrites|TestRuntimePlanFailsClosedWhenStoredDNSPoliciesAreDisabled|TestDNSPolicyRejectsUnavailableDomainSetBeforeSave' \
  -count=1

printf 'gateway DNS implicit upstream, fallback, generic route/NAT and DoH/DPI claims are rejected, audited and leave zero writes\n'
