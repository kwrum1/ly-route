#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root/backend"
go test ./internal/product ./internal/persistence ./internal/orchestrator ./internal/orchestratorapi ./internal/httpapi -count=1
cd "$repo_root"
bash scripts/test-controller-shell-profile-isolation.sh

printf 'orchestrator API, persistence, profile, compiler and bundle isolation contracts passed\n'
