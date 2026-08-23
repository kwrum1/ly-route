#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage:
  scripts/hotfix-deploy.sh --manifest dist/hotfix/ly-route-control/HASH/ly-route-control.manifest \
    --host root@gateway --remote /usr/lib/ly-route/ly-route-control \
    --service ly-route-control-api

The artifact path is derived from the manifest. Deployment is rejected when
the current source fingerprint or artifact SHA-256 differs from that manifest.
USAGE
}

manifest=
host=
remote_file=
service=
while (($#)); do
  case "$1" in
    --manifest) manifest=${2:?missing value for --manifest}; shift 2 ;;
    --host) host=${2:?missing value for --host}; shift 2 ;;
    --remote) remote_file=${2:?missing value for --remote}; shift 2 ;;
    --service) service=${2:?missing value for --service}; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

for value in manifest host remote_file service; do
  if [[ -z ${!value} ]]; then
    printf '%s is required\n' "$value" >&2
    usage >&2
    exit 2
  fi
done

[[ -f $manifest ]] || { printf 'manifest not found: %s\n' "$manifest" >&2; exit 2; }

manifest_value() {
  local key=$1
  awk -F= -v key="$key" '
    $1 == key { sub(/^[^=]*=/, ""); print; found=1; exit }
    END { if (!found) exit 1 }
  ' "$manifest"
}

format=$(manifest_value format)
artifact_file=$(manifest_value artifact_file)
expected_sha=$(manifest_value artifact_sha256)
expected_fingerprint=$(manifest_value source_fingerprint)
scope_csv=$(manifest_value source_scopes)

[[ $format == ly-route-hotfix-v1 ]] || { printf 'unsupported manifest format: %s\n' "$format" >&2; exit 2; }
[[ $artifact_file == "$(basename -- "$artifact_file")" ]] || { printf '%s\n' 'manifest artifact_file must be a basename' >&2; exit 2; }
local_file="$(dirname -- "$manifest")/$artifact_file"
[[ -f $local_file ]] || { printf 'sealed artifact not found: %s\n' "$local_file" >&2; exit 2; }

IFS=, read -r -a scopes <<<"$scope_csv"
current_fingerprint=$("$repo_root/scripts/source-fingerprint.sh" "${scopes[@]}")
if [[ $current_fingerprint != "$expected_fingerprint" ]]; then
  printf '%s\n' 'refusing stale hotfix: source changed after artifact build' >&2
  printf 'manifest=%s current=%s\n' "$expected_fingerprint" "$current_fingerprint" >&2
  exit 3
fi

local_sha=$(sha256sum "$local_file" | awk '{print $1}')
if [[ $local_sha != "$expected_sha" ]]; then
  printf '%s\n' 'refusing hotfix: artifact SHA-256 does not match manifest' >&2
  exit 3
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
remote_tmp="/tmp/ly-route-hotfix-${stamp}-$$"
remote_manifest_tmp="${remote_tmp}.manifest"
remote_backup="${remote_file}.pre-hotfix"
remote_manifest_dir=/var/lib/ly-route/hotfix-manifests

scp_command=(scp)
ssh_command=(ssh)
askpass_file=
if [[ -n ${LY_HOTFIX_PASSWORD:-} ]]; then
  command -v setsid >/dev/null 2>&1 || { printf '%s\n' 'LY_HOTFIX_PASSWORD requires setsid' >&2; exit 2; }
  askpass_file=$(mktemp)
  trap 'rm -f "$askpass_file"' EXIT
  cat >"$askpass_file" <<'EOF'
#!/bin/sh
printf '%s\n' "$LY_HOTFIX_PASSWORD"
EOF
  chmod 0700 "$askpass_file"
  export SSH_ASKPASS="$askpass_file" SSH_ASKPASS_REQUIRE=force DISPLAY=:0
  scp_command=(setsid -w scp)
  ssh_command=(setsid -w ssh)
elif [[ -n ${SSHPASS:-} ]]; then
  command -v sshpass >/dev/null 2>&1 || { printf '%s\n' 'SSHPASS is set but sshpass is unavailable' >&2; exit 2; }
  scp_command=(sshpass -e scp)
  ssh_command=(sshpass -e ssh)
fi

printf 'Deploying sealed artifact %s -> %s:%s\n' "$local_file" "$host" "$remote_file"
"${scp_command[@]}" "$local_file" "$host:$remote_tmp"
"${scp_command[@]}" "$manifest" "$host:$remote_manifest_tmp"
"${ssh_command[@]}" "$host" "set -eu
test -f '$remote_file' && cp -a '$remote_file' '$remote_backup' || true
install -m 0755 '$remote_tmp' '$remote_file'
remote_sha=\$(sha256sum '$remote_file' | awk '{print \$1}')
test \"\$remote_sha\" = '$local_sha'
mkdir -p '$remote_manifest_dir'
install -m 0644 '$remote_manifest_tmp' '$remote_manifest_dir/$service.manifest'
systemctl restart '$service'
systemctl is-active --quiet '$service'
rm -f '$remote_tmp' '$remote_manifest_tmp'
printf 'hotfix_sha256=%s\\n' \"\$remote_sha\"
printf 'source_fingerprint=%s\\n' '$expected_fingerprint'
printf 'backup=%s\\n' '$remote_backup'"

printf 'Hotfix deployed and %s is active. Re-run the same scenario now.\n' "$service"
