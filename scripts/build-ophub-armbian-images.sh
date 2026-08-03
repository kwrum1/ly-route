#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-ophub-armbian-images.sh --rootfs ROOTFS [options]

Build ophub/amlogic-s9xxx-armbian images with Ly Route rootfs content injected as
an Armbian common-files layer. This is the production ARM path for the full
ophub platform matrix: Amlogic, Rockchip, and Allwinner.

Options:
  --rootfs FILE       ly-route-rootfs-*-arm64.tar.zst
  --out DIR          Output directory, default dist/ophub-armbian
  --work DIR         Working directory, default dist/ophub-work
  --boards VALUE     ophub rebuild -b value, default ${LY_ROUTE_OPHUB_BOARDS:-all}
  --size-mb VALUE    ophub rebuild -s value, default ${LY_ROUTE_OPHUB_SIZE_MB:-4096}
  --base-url URL     Base Armbian .img/.img.gz URL. If omitted, latest ophub trunk arm64 image is used.
  --ophub-ref REF    ophub git ref, default ${LY_ROUTE_OPHUB_REF:-main}
EOF
}

rootfs=""
out_dir="dist/ophub-armbian"
work_dir="dist/ophub-work"
boards="${LY_ROUTE_OPHUB_BOARDS:-all}"
size_mb="${LY_ROUTE_OPHUB_SIZE_MB:-4096}"
base_url="${LY_ROUTE_OPHUB_BASE_IMAGE_URL:-}"
ophub_ref="${LY_ROUTE_OPHUB_REF:-main}"
cache_dir="${LY_ROUTE_OPHUB_CACHE_DIR:-}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --rootfs) rootfs="${2:-}"; shift 2 ;;
    --out) out_dir="${2:-}"; shift 2 ;;
    --work) work_dir="${2:-}"; shift 2 ;;
    --boards) boards="${2:-}"; shift 2 ;;
    --size-mb) size_mb="${2:-}"; shift 2 ;;
    --base-url) base_url="${2:-}"; shift 2 ;;
    --ophub-ref) ophub_ref="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$rootfs" ] || [ ! -f "$rootfs" ]; then
  echo "--rootfs is required and must point to an existing arm64 tarball" >&2
  exit 2
fi

for cmd in curl tar zstd gzip python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command missing: $cmd" >&2
    exit 1
  fi
done

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$out_dir" in /*) artifact_dir="$out_dir" ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
case "$work_dir" in /*) work_parent="$work_dir" ;; *) work_parent="$repo_root/$work_dir" ;; esac

rm -rf "$work_parent" "$artifact_dir"
mkdir -p "$work_parent" "$artifact_dir"

ophub_dir="$work_parent/amlogic-s9xxx-armbian"

download_github_tree() {
  repo="$1"
  ref="$2"
  source_dir="$3"
  target_dir="$4"
  local_archive="${5:-}"
  archive="$work_parent/${repo##*/}-${ref//\//_}.tar.gz"
  extract_dir="$work_parent/${repo##*/}-${ref//\//_}-extract"
  rm -rf "$extract_dir" "$target_dir"
  mkdir -p "$extract_dir" "$target_dir"
  if [ -n "$local_archive" ] && [ -f "$local_archive" ]; then
    cp "$local_archive" "$archive"
  else
    curl -fL --retry 8 --retry-delay 15 --connect-timeout 30 \
      "https://codeload.github.com/${repo}/tar.gz/${ref}" \
      -o "$archive"
  fi
  tar -xzf "$archive" -C "$extract_dir" --strip-components=1
  cp -a "$extract_dir/$source_dir/." "$target_dir/"
}

ophub_archive=""
if [ -n "$cache_dir" ]; then
  ophub_archive="$cache_dir/amlogic-s9xxx-armbian-main.tar.gz"
fi
download_github_tree ophub/amlogic-s9xxx-armbian "$ophub_ref" . "$ophub_dir" "$ophub_archive"

download_ophub_tree() {
  repo="$1"
  source_dir="$2"
  target_dir="$3"
  local_archive=""
  if [ -n "$cache_dir" ]; then
    local_archive="$cache_dir/${repo##*/}-main.tar.gz"
  fi
  download_github_tree "$repo" main "$source_dir" "$target_dir" "$local_archive"
}

download_ophub_tree ophub/u-boot u-boot "$ophub_dir/build-armbian/u-boot"
download_ophub_tree ophub/firmware firmware "$ophub_dir/build-armbian/armbian-files/common-files/usr/lib/firmware"

prefetch_kernel() {
  key="$1"
  version="$2"
  if [ -z "$cache_dir" ]; then
    return 0
  fi
  archive="$cache_dir/kernel_${key}-${version}.tar.gz"
  if [ ! -f "$archive" ]; then
    archive="$cache_dir/kernel_${key}.tar.gz"
  fi
  if [ ! -f "$archive" ]; then
    return 0
  fi
  target="$ophub_dir/build-armbian/kernel/$key"
  mkdir -p "$target"
  tar -mxzf "$archive" -C "$target"
}

prefetch_kernel stable 6.18.35
prefetch_kernel rk35xx 6.1.141
prefetch_kernel rk3588 6.1.141

if [ -z "$base_url" ]; then
  base_url=$(python3 - <<'PY'
import json, urllib.request
import os
request = urllib.request.Request('https://api.github.com/repos/ophub/amlogic-s9xxx-armbian/releases/latest')
token = os.environ.get('GITHUB_TOKEN') or os.environ.get('GH_TOKEN')
if token:
    request.add_header('Authorization', f'Bearer {token}')
request.add_header('Accept', 'application/vnd.github+json')
with urllib.request.urlopen(request, timeout=60) as response:
    release = json.load(response)
assets = release.get('assets', [])
matches = [asset for asset in assets if asset.get('name', '').startswith('Armbian_') and '-trunk_' in asset.get('name', '') and '_arm64_' in asset.get('name', '') and asset.get('name', '').endswith('.img.gz')]
if not matches:
    raise SystemExit('no latest ophub trunk arm64 .img.gz asset found')
matches.sort(key=lambda asset: asset.get('size', 0))
print(matches[0]['browser_download_url'])
PY
)
fi

mkdir -p "$ophub_dir/build/output/images"
base_file="${LY_ROUTE_OPHUB_BASE_IMAGE_FILE:-}"
if [ -z "$base_file" ] && [ -n "$cache_dir" ]; then
  base_file=$(find "$cache_dir" -maxdepth 1 -type f -name 'Armbian_*-trunk_*_arm64_*.img.gz' | sort | tail -n 1 || true)
fi
if [ -n "$base_file" ] && [ -f "$base_file" ]; then
  base_name=${base_file##*/}
else
  base_name=${base_url##*/}
fi
case "$base_name" in
  *.img.gz)
    if [ -n "${base_file:-}" ] && [ -f "$base_file" ]; then
      cp "$base_file" "$ophub_dir/build/output/images/$base_name"
    else
      curl -fL --retry 5 --retry-delay 10 "$base_url" -o "$ophub_dir/build/output/images/$base_name"
    fi
    gzip -dc "$ophub_dir/build/output/images/$base_name" > "$ophub_dir/build/output/images/${base_name%.gz}"
    ;;
  *.img)
    if [ -n "${base_file:-}" ] && [ -f "$base_file" ]; then
      cp "$base_file" "$ophub_dir/build/output/images/$base_name"
    else
      curl -fL --retry 5 --retry-delay 10 "$base_url" -o "$ophub_dir/build/output/images/$base_name"
    fi
    ;;
  *)
    echo "base image URL must end with .img or .img.gz: $base_url" >&2
    exit 2
    ;;
esac

common_files="$ophub_dir/build-armbian/armbian-files/common-files"
mkdir -p "$common_files"
tar --numeric-owner --use-compress-program=unzstd -xf "$rootfs" -C "$common_files"
rm -rf "$common_files/dev" "$common_files/proc" "$common_files/sys" "$common_files/run" "$common_files/tmp"
mkdir -p "$common_files/dev" "$common_files/proc" "$common_files/sys" "$common_files/run" "$common_files/tmp"
chmod 1777 "$common_files/tmp"

(
  cd "$ophub_dir"
  (
    while true; do
      printf 'ophub rebuild still running at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      df -h . "$artifact_dir"
      sleep 60
    done
  ) &
  heartbeat_pid=$!
  trap 'kill "$heartbeat_pid" 2>/dev/null || true' EXIT
  set +e
  sudo ./rebuild -b "$boards" -s "$size_mb" -n ly-route
  rebuild_status=$?
  set -e
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  trap - EXIT
  exit "$rebuild_status"
)

find "$ophub_dir/build/output/images" -maxdepth 1 -type f \( -name 'Armbian_*.img' -o -name 'Armbian_*.img.gz' -o -name 'Armbian_*.sha' -o -name 'Armbian_*.sha256' \) ! -name '*-trunk_*' -print0 |
  while IFS= read -r -d '' image_file; do
    mv -f "$image_file" "$artifact_dir/"
  done

find "$artifact_dir" -maxdepth 1 -type f -name 'Armbian_*.img' -print0 |
  while IFS= read -r -d '' image_file; do
    gzip -f -k "$image_file"
    sha256sum "$image_file.gz" > "$image_file.gz.sha256"
    rm -f "$image_file"
  done

find "$artifact_dir" -maxdepth 1 -type f -name 'Armbian_*.img.gz' -print0 |
  while IFS= read -r -d '' image_file; do
    sha256sum "$image_file" > "$image_file.sha256"
  done

cat > "$artifact_dir/ophub-armbian-build.manifest" <<EOF
ophub_ref=$ophub_ref
ophub_boards=$boards
ophub_size_mb=$size_mb
ophub_base_url=$base_url
rootfs=$(basename "$rootfs")
EOF

printf 'Built ophub Armbian images in %s\n' "$artifact_dir"
