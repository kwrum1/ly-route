#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage: scripts/build-hotfix-go.sh --package ./cmd/gateway-control --name ly-route-control \
  [--source-scope backend] [--goos linux] [--goarch amd64]

Compile-checks and rebuilds one Go command, then seals it with the current
source fingerprint. The output is never written to a generic reusable name.
USAGE
}

package=
name=
goos=linux
goarch=amd64
scopes=()
while (($#)); do
  case "$1" in
    --package) package=${2:?missing value for --package}; shift 2 ;;
    --name) name=${2:?missing value for --name}; shift 2 ;;
    --source-scope) scopes+=("${2:?missing value for --source-scope}"); shift 2 ;;
    --goos) goos=${2:?missing value for --goos}; shift 2 ;;
    --goarch) goarch=${2:?missing value for --goarch}; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n $package && -n $name ]] || { usage >&2; exit 2; }
if ((${#scopes[@]} == 0)); then
  scopes=(backend)
fi
command -v go >/dev/null 2>&1 || { printf '%s\n' 'go is required' >&2; exit 2; }

printf '==> compile check %s\n' "$package"
(cd "$repo_root/backend" && go test -run '^$' "$package")

mkdir -p "$repo_root/dist"
build_dir=$(mktemp -d "$repo_root/dist/.hotfix-build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT

printf '==> build %s/%s %s\n' "$goos" "$goarch" "$package"
(cd "$repo_root/backend" && \
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
  go build -trimpath -buildvcs=false -ldflags '-s -w' -o "$build_dir/$name" "$package")

seal_args=(--artifact "$build_dir/$name" --name "$name")
for scope in "${scopes[@]}"; do
  seal_args+=(--source-scope "$scope")
done
"$repo_root/scripts/seal-hotfix-artifact.sh" "${seal_args[@]}"
