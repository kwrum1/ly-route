#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"

# Kea, PPPoE and Xray cross API, encrypted persistence, renderers and process
# controllers. Their complete owning packages prove typed validation, product
# ownership and that credentials never enter responses, receipts or audit text.
go test \
  ./internal/product \
  ./internal/persistence \
  ./internal/httpapi \
  ./internal/runtime/service \
  ./internal/runtime/proxy \
  -count=1

printf 'gateway Kea, PPPoE and Xray typed contracts, ownership and secret redaction passed\n'
