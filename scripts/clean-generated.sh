#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
apply=false

if [[ ${1:-} == --apply ]]; then
  apply=true
elif [[ ${1:-} == --help || ${1:-} == -h ]]; then
  printf '%s\n' 'Usage: scripts/clean-generated.sh [--apply]'
  printf '%s\n' 'Without --apply, prints the allowlisted generated paths that would be removed.'
  exit 0
elif (($#)); then
  printf 'unknown argument: %s\n' "$1" >&2
  exit 2
fi

paths=(
  .codex-tmp
  .acceptance
  .codex-build
  dist
  build
  tmp
  runtime-debs
  runtime-downloads
  vpp-master
  govpp-master
  config/build
)

for relative in "${paths[@]}"; do
  candidate="$repo_root/$relative"
  [[ -e $candidate ]] || continue
  resolved=$(realpath -m -- "$candidate")
  case "$resolved" in
    "$repo_root"/*) ;;
    *) printf 'refusing path outside repository: %s\n' "$resolved" >&2; exit 2 ;;
  esac
  if $apply; then
    rm -rf -- "$resolved"
    printf 'removed %s\n' "$relative"
  else
    printf 'would remove %s\n' "$relative"
  fi
done
