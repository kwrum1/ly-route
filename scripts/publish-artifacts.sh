#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/publish-artifacts.sh --name NAME --path GLOB [--path GLOB ...]

Publish CI artifacts to a local filesystem directory without depending on
actions/upload-artifact.

Environment:
  LY_ROUTE_ARTIFACTS_DIR  Destination root. Defaults to dist/gitea-artifacts.
  GITHUB_RUN_ID           Included in the artifact path when available.
  GITHUB_SHA              Included in manifest metadata when available.
EOF
}

name=""
paths_file="${TMPDIR:-/tmp}/ly-route-artifact-paths.$$"
trap 'rm -f "$paths_file"' EXIT INT TERM
: > "$paths_file"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --name) name="${2:-}"; shift 2 ;;
    --path) printf '%s\n' "${2:-}" >> "$paths_file"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$name" ] || [ ! -s "$paths_file" ]; then
  usage >&2
  exit 2
fi
case "$name" in
  */*|*..*) echo "Artifact name must not contain slashes or '..': $name" >&2; exit 2 ;;
esac

artifact_root="${LY_ROUTE_ARTIFACTS_DIR:-dist/gitea-artifacts}"
case "$artifact_root" in
  /|*..*) echo "Refusing unsafe artifact root: $artifact_root" >&2; exit 2 ;;
esac
run_id="${GITHUB_RUN_ID:-manual}"
case "$run_id" in
  */*|*..*) echo "Run id must not contain slashes or '..': $run_id" >&2; exit 2 ;;
esac
dest="$artifact_root/$run_id/$name"
tmp_dest="$dest.tmp.$$"
case "$dest" in
  /|/*/..|*/../*) echo "Refusing unsafe artifact destination: $dest" >&2; exit 2 ;;
esac

rm -rf "$tmp_dest"
mkdir -p "$tmp_dest"

found=0
while IFS= read -r pattern; do
  [ -n "$pattern" ] || continue
  set -- $pattern
  for file do
    if [ -f "$file" ]; then
      mkdir -p "$tmp_dest/$(dirname "$file")"
      cp -p "$file" "$tmp_dest/$file"
      found=1
    fi
  done
done < "$paths_file"

if [ "$found" -ne 1 ]; then
  echo "No artifact files matched for $name" >&2
  rm -rf "$tmp_dest"
  exit 1
fi

{
  printf 'name=%s\n' "$name"
  printf 'run_id=%s\n' "$run_id"
  printf 'sha=%s\n' "${GITHUB_SHA:-unknown}"
  printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  (cd "$tmp_dest" && find . -type f | sort | sed 's#^./##')
} > "$tmp_dest/manifest.txt"

rm -rf "$dest"
mkdir -p "$(dirname "$dest")"
mv "$tmp_dest" "$dest"
printf 'published artifact %s to %s\n' "$name" "$dest"
