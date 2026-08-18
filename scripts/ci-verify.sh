#!/usr/bin/env bash
set -euo pipefail

# Compatibility entry point. Daily development must stay fast; artifact and
# full acceptance checks belong to ci-release-verify.sh and the live batch.
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if [[ ${1:-} == --release ]]; then
  shift
  exec "$repo_root/scripts/ci-release-verify.sh" "$@"
fi

exec bash "$repo_root/scripts/dev-hotfix-check.sh" "$@"
