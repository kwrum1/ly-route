#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/checkout-worktree.sh

Prepare a Gitea Actions worktree without depending on actions/checkout.

Environment:
  GITHUB_SERVER_URL      Gitea server URL, for example http://10.1.18.100:10000.
  GITHUB_REPOSITORY      Repository path, for example kurumi/ly-route.
  GITHUB_SHA             Commit SHA to check out.
  GITHUB_REF_NAME        Branch name fallback when GITHUB_SHA is unavailable.
  GITEA_TOKEN/GITHUB_TOKEN  Optional token for private HTTP fetches.
  LY_ROUTE_REPO_URL      Optional explicit repository URL override.
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

repo_url="${LY_ROUTE_REPO_URL:-}"
if [ -z "$repo_url" ]; then
  server="${GITHUB_SERVER_URL:-http://10.1.18.100:10000}"
  repository="${GITHUB_REPOSITORY:-kurumi/ly-route}"
  repo_url="${server%/}/${repository}.git"
fi

token="${GITEA_TOKEN:-${GITHUB_TOKEN:-}}"
auth_header=""
if [ -n "$token" ]; then
  auth=$(printf 'x-access-token:%s' "$token" | base64 | tr -d '\n')
  auth_header="Authorization: Basic $auth"
fi

checkout_ref="${GITHUB_SHA:-}"
if [ -z "$checkout_ref" ]; then
  checkout_ref="${GITHUB_REF_NAME:-main}"
fi

if [ -d .git ]; then
  git remote set-url origin "$repo_url" 2>/dev/null || git remote add origin "$repo_url"
else
  git init
  git remote add origin "$repo_url"
fi

if [ -n "$auth_header" ]; then
  git -c "http.extraHeader=$auth_header" fetch --prune --depth=1 origin "$checkout_ref" || git -c "http.extraHeader=$auth_header" fetch --prune --depth=1 origin "${GITHUB_REF_NAME:-main}"
else
  git fetch --prune --depth=1 origin "$checkout_ref" || git fetch --prune --depth=1 origin "${GITHUB_REF_NAME:-main}"
fi
git checkout --force FETCH_HEAD
git clean -ffd

printf 'checked out %s from %s\n' "$(git rev-parse --short HEAD)" "$repo_url"
