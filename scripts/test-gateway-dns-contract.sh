#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# DNS product contract: ordered first-match evaluation, explicit upstream/WAN
# resolution, proxy-bound resolution, fixed answers, NODATA fail-closed behavior,
# SmartDNS rendering, persistence and the public HTTP resource.
go test \
  ./internal/runtime/dns \
  ./internal/runtime/dnsguard \
  ./internal/runtime/service \
  ./internal/httpapi \
  -count=1

printf 'gateway DNS ordered explicit-resolution, fixed-answer and NODATA contracts passed\n'
