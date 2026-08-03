#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# These complete packages own strict payload validation, authorization/audit,
# management reachability protection, product-resource allowlists, secret
# redaction and zero-write transaction rejection. Package-wide execution keeps
# the gate effective when new Gateway resources or mutation routes are added.
go test \
  ./internal/product \
  ./internal/persistence \
  ./internal/httpapi \
  ./internal/runtime/... \
  -count=1

printf 'gateway invalid input, privilege, management and cross-product mutations are rejected, audited and leave zero forbidden writes\n'
