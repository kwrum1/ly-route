#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# Gateway owns the broadest product surface. Run every package that defines its
# profile, storage schema, HTTP contract, compiler, transaction and enforcing
# adapter so a newly declared resource cannot exist in only one layer.
go test \
  ./internal/product \
  ./internal/persistence \
  ./internal/api \
  ./internal/httpapi \
  ./internal/runtime/... \
  -count=1

cd "$repo_root"
bash scripts/test-controller-shell-profile-isolation.sh

printf 'gateway API, persistence, profile, compiler, transaction and bundle contracts passed\n'
