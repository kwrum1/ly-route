#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/publish-gitea-release.sh --tag TAG --title TITLE --path GLOB [--path GLOB ...]

Create or reuse a Gitea release and upload matched files as release assets.

Environment:
  LY_ROUTE_GITEA_URL     Gitea server URL, preferred when set.
  GITHUB_SERVER_URL      Gitea server URL fallback.
  GITHUB_REPOSITORY      Repository path, for example kurumi/ly-route.
  GITEA_TOKEN/GITHUB_TOKEN  Token with release write permission.
EOF
}

tag=""
title=""
paths_file="${TMPDIR:-/tmp}/ly-route-release-paths.$$"
trap 'rm -f "$paths_file"' EXIT INT TERM
: > "$paths_file"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --title) title="${2:-}"; shift 2 ;;
    --path) printf '%s\n' "${2:-}" >> "$paths_file"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$tag" ] || [ -z "$title" ] || [ ! -s "$paths_file" ]; then
  usage >&2
  exit 2
fi
case "$tag" in
  */*|*..*|*\"*) echo "Unsafe release tag: $tag" >&2; exit 2 ;;
esac
case "$title" in
  *\"*) echo "Unsafe release title: $title" >&2; exit 2 ;;
esac

server="${LY_ROUTE_GITEA_URL:-${GITHUB_SERVER_URL:-http://10.1.18.100:10000}}"
case "$server" in
  https://github.com|http://github.com) server="http://10.1.18.100:10000" ;;
esac
repository="${GITHUB_REPOSITORY:-kurumi/ly-route}"
token="${GITEA_TOKEN:-${GITHUB_TOKEN:-}}"
if [ -z "$token" ]; then
  echo "GITEA_TOKEN or GITHUB_TOKEN is required to publish releases" >&2
  exit 1
fi
api="${server%/}/api/v1/repos/$repository"
auth_header="Authorization: token $token"

json_id() {
  python3 -c 'import json,sys; data=json.load(sys.stdin); print(data.get("id", ""))'
}

release_json=$(curl -fsS -H "$auth_header" "$api/releases/tags/$tag" 2>/dev/null || true)
release_id=""
if [ -n "$release_json" ]; then
  release_id=$(printf '%s' "$release_json" | json_id)
fi
if [ -z "$release_id" ]; then
  release_json=$(curl -fsS -X POST -H "$auth_header" -H 'Content-Type: application/json' \
    -d "{\"tag_name\":\"$tag\",\"target_commitish\":\"${GITHUB_SHA:-main}\",\"name\":\"$title\",\"body\":\"Ly Route firmware artifacts\",\"draft\":false,\"prerelease\":false}" \
    "$api/releases")
  release_id=$(printf '%s' "$release_json" | json_id)
fi
if [ -z "$release_id" ]; then
  echo "Unable to determine Gitea release id for $tag" >&2
  exit 1
fi

found=0
while IFS= read -r pattern; do
  [ -n "$pattern" ] || continue
  set -- $pattern
  for file do
    if [ -f "$file" ]; then
      name=$(basename "$file")
      curl -fsS -X POST -H "$auth_header" \
        -F "attachment=@$file" \
        "$api/releases/$release_id/assets?name=$name" >/dev/null
      printf 'uploaded release asset %s to %s\n' "$name" "$tag"
      found=1
    fi
  done
done < "$paths_file"

if [ "$found" -ne 1 ]; then
  echo "No release asset files matched for $tag" >&2
  exit 1
fi
