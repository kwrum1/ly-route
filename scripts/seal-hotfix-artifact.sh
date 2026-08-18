#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage: scripts/seal-hotfix-artifact.sh --artifact FILE --name NAME \
  --source-scope PATH [--source-scope PATH ...]

Copies one freshly built file into a source-addressed directory and writes its
provenance manifest. Older local artifacts with the same name are removed only
after the new artifact has been sealed successfully.
USAGE
}

artifact=
name=
scopes=()
while (($#)); do
  case "$1" in
    --artifact) artifact=${2:?missing value for --artifact}; shift 2 ;;
    --name) name=${2:?missing value for --name}; shift 2 ;;
    --source-scope) scopes+=("${2:?missing value for --source-scope}"); shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n $artifact && -n $name ]] || { usage >&2; exit 2; }
((${#scopes[@]} > 0)) || { printf '%s\n' 'at least one --source-scope is required' >&2; exit 2; }
[[ -f $artifact ]] || { printf 'artifact not found: %s\n' "$artifact" >&2; exit 2; }
[[ $name =~ ^[A-Za-z0-9._-]+$ ]] || { printf 'invalid artifact name: %s\n' "$name" >&2; exit 2; }

fingerprint=$("$repo_root/scripts/source-fingerprint.sh" "${scopes[@]}")
commit=$(git rev-parse HEAD)
scope_csv=$(IFS=,; printf '%s' "${scopes[*]}")
artifact_root="$repo_root/dist/hotfix/$name"
target_dir="$artifact_root/$fingerprint"
mkdir -p "$repo_root/dist"
stage_dir=$(mktemp -d "$repo_root/dist/.hotfix-seal.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT

install -m 0755 "$artifact" "$stage_dir/$name"
artifact_sha=$(sha256sum "$stage_dir/$name" | awk '{print $1}')
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

cat >"$stage_dir/$name.manifest" <<EOF
format=ly-route-hotfix-v1
artifact_name=$name
artifact_file=$name
artifact_sha256=$artifact_sha
source_commit=$commit
source_fingerprint=$fingerprint
source_scopes=$scope_csv
built_at=$built_at
builder=$(hostname)
EOF

mkdir -p "$artifact_root"
rm -rf "$target_dir"
mv "$stage_dir" "$target_dir"
trap - EXIT

find "$artifact_root" -mindepth 1 -maxdepth 1 -type d ! -name "$fingerprint" -exec rm -rf -- {} +

printf 'artifact_path=%s\n' "$target_dir/$name"
printf 'manifest_path=%s\n' "$target_dir/$name.manifest"
printf 'source_fingerprint=%s\n' "$fingerprint"
printf 'artifact_sha256=%s\n' "$artifact_sha"
