#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage: scripts/source-fingerprint.sh [source-scope ...]

Prints a deterministic SHA-256 over tracked and untracked, non-ignored files
in the requested source scopes. Missing tracked files are included as deletion
markers. The default scope is backend.
USAGE
}

if [[ ${1:-} == --help || ${1:-} == -h ]]; then
  usage
  exit 0
fi

scopes=("$@")
if ((${#scopes[@]} == 0)); then
  scopes=(backend)
fi

manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT
count=0

while IFS= read -r -d '' file; do
  count=$((count + 1))
  if [[ -L $file ]]; then
    digest=$(printf '%s' "$(readlink "$file")" | sha256sum | awk '{print $1}')
    kind=link
  elif [[ -f $file ]]; then
    digest=$(sha256sum "$file" | awk '{print $1}')
    kind=file
  else
    digest=missing
    kind=deleted
  fi
  printf '%s\0%s\0%s\0' "$kind" "$file" "$digest" >>"$manifest"
done < <(git ls-files -z --cached --others --exclude-standard -- "${scopes[@]}" | LC_ALL=C sort -z)

if ((count == 0)); then
  printf 'no source files found in scopes: %s\n' "${scopes[*]}" >&2
  exit 2
fi

sha256sum "$manifest" | awk '{print $1}'
