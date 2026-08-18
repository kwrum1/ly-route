#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage: scripts/dev-hotfix-check.sh [go-package ...]

Runs only the daily source gate. It does not build a rootfs, ISO, VPP deb, or
release artifact. With no package argument the backend is compile-checked as
a whole; pass one or more Go packages to keep a local change targeted.
USAGE
}

if [[ ${1:-} == --help || ${1:-} == -h ]]; then
  usage
  exit 0
fi

printf '%s\n' '==> source whitespace check'
# Unified-diff patch files contain a leading context marker by design; checking
# their embedded source indentation as ordinary working-tree whitespace gives
# false positives. The patched C/VPP sources themselves are checked normally.
git diff --check -- . ':(exclude)packaging/vpp-patches/*.patch'

printf '%s\n' '==> shell syntax check'
while IFS= read -r -d '' file; do
  # bash parses the POSIX shell subset too, while handling the Bash scripts
  # used by the acceptance helpers without a second shell-specific branch.
  bash -n "$file"
done < <(find scripts packaging/rootfs-overlay -type f -name '*.sh' -print0)

if ! command -v go >/dev/null 2>&1; then
  printf '%s\n' 'go is required for the daily source gate' >&2
  exit 2
fi

packages=("$@")
if ((${#packages[@]} == 0)); then
  packages=(./...)
fi

printf '==> Go compile check: %s\n' "${packages[*]}"
(cd "$repo_root/backend" && go test -run '^$' "${packages[@]}")

printf '%s\n' 'Daily source gate passed. Runtime, ISO, hardware, and performance checks are separate stages.'
