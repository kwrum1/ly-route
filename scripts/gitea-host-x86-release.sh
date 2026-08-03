#!/usr/bin/env sh
set -eu

build_only=${LY_ROUTE_RELEASE_BUILD_ONLY:-0}
fixture=${LY_ROUTE_RELEASE_FIXTURE:-0}
case "$build_only,$fixture" in
  0,0|0,1|1,0|1,1) ;;
  *) echo "LY_ROUTE_RELEASE_BUILD_ONLY and LY_ROUTE_RELEASE_FIXTURE must be 0 or 1" >&2; exit 2 ;;
esac
if [ "$build_only" != 1 ]; then
  : "${GITEA_TOKEN:?GITEA_TOKEN is required to publish x86 physical firmware}"
fi

workspace=${LY_ROUTE_HOST_WORKSPACE:-/opt/gitea-runner-ly-route/host-build-${GITHUB_RUN_ID:-manual}-${GITHUB_RUN_ATTEMPT:-1}}
server=${LY_ROUTE_GITEA_URL:-${GITHUB_SERVER_URL:-http://10.1.18.100:10000}}
repository=${GITHUB_REPOSITORY:-kurumi/ly-route}
checkout_ref=${GITHUB_SHA:-}
repo_url=${server%/}/${repository}.git

run_with_heartbeat() {
  label=$1
  shift
  "$@" &
  child_pid=$!
  (
    while kill -0 "$child_pid" 2>/dev/null; do
      sleep "${LY_ROUTE_CI_HEARTBEAT_SECONDS:-60}"
      if kill -0 "$child_pid" 2>/dev/null; then printf 'still running: %s\n' "$label"; fi
    done
  ) &
  heartbeat_pid=$!
  status=0
  wait "$child_pid" || status=$?
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  return "$status"
}

if [ "$build_only" = 1 ]; then
  source_dir=${LY_ROUTE_HOST_SOURCE_DIR:?LY_ROUTE_HOST_SOURCE_DIR is required for build-only releases}
  source_dir=$(CDPATH= cd -- "$source_dir" && pwd)
  cd "$source_dir"
else
  case "$checkout_ref" in
    ????*) ;;
    *) echo "GITHUB_SHA must be an immutable 40-character commit SHA" >&2; exit 2 ;;
  esac
  if [ "${#checkout_ref}" -ne 40 ] || printf '%s' "$checkout_ref" | grep -Eq '[^0-9a-f]'; then
    echo "GITHUB_SHA must be an immutable lowercase hexadecimal commit SHA" >&2
    exit 2
  fi
  rm -rf "$workspace"
  mkdir -p "$workspace"
  cd "$workspace"
  git init
  git remote add origin "$repo_url"
  auth=$(printf 'x-access-token:%s' "$GITEA_TOKEN" | base64 | tr -d '\n')
  git -c "http.extraHeader=Authorization: Basic $auth" fetch --prune --depth=1 origin "$checkout_ref"
  git checkout --force FETCH_HEAD
  [ "$(git rev-parse HEAD)" = "$checkout_ref" ] || {
    echo "checked out commit does not match GITHUB_SHA" >&2
    exit 1
  }
  source_dir=$workspace
fi

export LY_ROUTE_MIRROR=${LY_ROUTE_MIRROR:-http://mirror.nju.edu.cn/debian}
export LY_ROUTE_SECURITY_MIRROR=${LY_ROUTE_SECURITY_MIRROR:-http://mirror.nju.edu.cn/debian-security}
export LY_ROUTE_GITEA_URL=${LY_ROUTE_GITEA_URL:-http://10.1.18.100:10000}
export GITHUB_SERVER_URL=${GITHUB_SERVER_URL:-http://10.1.18.100:10000}
export GITHUB_REPOSITORY=${GITHUB_REPOSITORY:-kurumi/ly-route}
export GITHUB_SHA=$checkout_ref
export GOPROXY=${GOPROXY:-https://goproxy.cn,direct}
export GOSUMDB=${GOSUMDB:-sum.golang.org}
export GOTOOLCHAIN=auto
export GOMODCACHE=${GOMODCACHE:-/root/go/pkg/mod}
export LY_ROUTE_RUNTIME_SRC_DIR=${LY_ROUTE_RUNTIME_SRC_DIR:-/opt/gitea-runner-ly-route/cache/runtime-src}
export LY_ROUTE_FDIO_CACHE_DIR=${LY_ROUTE_FDIO_CACHE_DIR:-/opt/gitea-runner-ly-route/cache/fdio}
export LY_ROUTE_SOURCE_OFFLINE=${LY_ROUTE_SOURCE_OFFLINE:-1}
export TMPDIR=${TMPDIR:-$workspace/tmp}
mkdir -p "$TMPDIR"

release_root=${LY_ROUTE_RELEASE_OUTPUT_DIR:-$source_dir/dist}
case "$release_root" in /*) ;; *) release_root="$source_dir/$release_root" ;; esac
rootfs_dir=$release_root/rootfs
upgrade_dir=$release_root/upgrade
firmware_dir=$release_root/physical-firmware
mkdir -p "$rootfs_dir" "$upgrade_dir" "$firmware_dir"

sh -n scripts/build-rootfs.sh
bash -n scripts/build-disk-image.sh
sh -n scripts/build-upgrade-package.sh
sh -n scripts/publish-gitea-release.sh

if [ "$fixture" != 1 ]; then
  ./scripts/build-runtime-debs.sh smartdns
  ./scripts/build-runtime-debs.sh xray
  ./scripts/build-runtime-debs.sh vpp-apply
  ./scripts/build-runtime-debs.sh vpp-fdio
  export LY_ROUTE_EXTRA_DEBS_DIR="$source_dir/runtime-debs"
fi

select_artifact() {
  artifact_prefix=$1
  selected=
  count=0
  for candidate in "$artifact_prefix.tar.zst" "$artifact_prefix.tar.gz" "$artifact_prefix.tar"; do
    if [ -f "$candidate" ]; then selected=$candidate; count=$((count + 1)); fi
  done
  [ "$count" -eq 1 ] || {
    echo "Expected exactly one artifact for $artifact_prefix, found $count" >&2
    return 1
  }
  printf '%s\n' "$selected"
}

validate_upgrade_archive() {
  package=$1
  package_name=$(basename "$package")
  (cd "$upgrade_dir" && sha256sum -c "$package_name.sha256" >/dev/null)
  case "$package" in
    *.tar.zst) zstd -t "$package" >/dev/null ;;
    *.tar.gz) gzip -t "$package" ;;
    *.tar) tar -tf "$package" >/dev/null ;;
    *) echo "Unsupported upgrade archive: $package" >&2; return 1 ;;
  esac
}

write_upgrade_sidecar() {
  package=$1
  product=$2
  sidecar="$upgrade_dir/ly-route-upgrade-$product-bookworm-amd64.manifest.json"
  case "$package" in
    *.tar.zst) zstd -dc "$package" | tar -xOf - ./manifest.json >"$sidecar" ;;
    *.tar.gz) gzip -dc "$package" | tar -xOf - ./manifest.json >"$sidecar" ;;
    *.tar) tar -xOf "$package" ./manifest.json >"$sidecar" ;;
    *) echo "Unsupported upgrade archive: $package" >&2; return 1 ;;
  esac
  node - "$sidecar" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [path, product] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(path, "utf8"));
if (manifest.package_type !== "ly-route-upgrade" || manifest.product !== product ||
    manifest.suite !== "bookworm" || manifest.arch !== "amd64") {
  console.error(`Upgrade sidecar manifest mismatch for ${product}`);
  process.exit(1);
}
NODE
  (cd "$upgrade_dir" && sha256sum "$(basename "$sidecar")" >"$(basename "$sidecar").sha256")
}

for product in gateway orchestrator; do
  printf 'building x86 %s release artifacts\n' "$product"
  LY_ROUTE_CONTROL_PRODUCT=$product ./scripts/build-rootfs.sh \
    --product "$product" --arch amd64 --out "$rootfs_dir"
  rootfs=$(select_artifact "$rootfs_dir/ly-route-rootfs-$product-bookworm-amd64")

  LY_ROUTE_CONTROL_PRODUCT=$product ./scripts/build-upgrade-package.sh \
    --product "$product" --arch amd64 \
    --base-manifest "$source_dir/packaging/product-profiles/$product.json" \
    --out "$upgrade_dir"
  upgrade=$(select_artifact "$upgrade_dir/ly-route-upgrade-$product-bookworm-amd64")
  validate_upgrade_archive "$upgrade"
  write_upgrade_sidecar "$upgrade" "$product"

  run_with_heartbeat "build x86 $product physical disk image" \
    ./scripts/build-disk-image.sh --product "$product" --rootfs "$rootfs" \
      --out "$firmware_dir" --size 4G

  image="$firmware_dir/ly-route-$product-bookworm-amd64-4g.img.zst"
  [ -f "$image" ] || { echo "Missing $product x86 image: $image" >&2; exit 1; }
  (cd "$firmware_dir" && sha256sum -c "$(basename "$image").sha256" >/dev/null)
  zstd -t "$image" >/dev/null
  test -f "$image.manifest.json"
  (cd "$firmware_dir" && sha256sum -c "$(basename "$image").manifest.json.sha256" >/dev/null)
  node - "$image.manifest.json" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [path, product] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(path, "utf8"));
if (manifest.artifact_type !== "disk-image" || manifest.product !== product ||
    manifest.arch !== "amd64" || manifest.frontend_product !== product) {
  console.error(`Disk image manifest mismatch for ${product}`);
  process.exit(1);
}
NODE
done

if [ "$build_only" = 1 ]; then
  printf 'Built dual x86 release fixtures in %s\n' "$release_root"
  exit 0
fi

tag=ly-route-gitea-dual-x86-${LY_ROUTE_RELEASE_TIMESTAMP:-$(date -u +%y.%m.%d-%H.%M.%S)}
./scripts/publish-gitea-release.sh \
  --tag "$tag" \
  --title "$tag" \
  --path "$firmware_dir/ly-route-gateway-bookworm-amd64-4g.img.zst" \
  --path "$firmware_dir/ly-route-gateway-bookworm-amd64-4g.img.zst.sha256" \
  --path "$firmware_dir/ly-route-gateway-bookworm-amd64-4g.img.zst.manifest.json" \
  --path "$firmware_dir/ly-route-gateway-bookworm-amd64-4g.img.zst.manifest.json.sha256" \
  --path "$firmware_dir/ly-route-orchestrator-bookworm-amd64-4g.img.zst" \
  --path "$firmware_dir/ly-route-orchestrator-bookworm-amd64-4g.img.zst.sha256" \
  --path "$firmware_dir/ly-route-orchestrator-bookworm-amd64-4g.img.zst.manifest.json" \
  --path "$firmware_dir/ly-route-orchestrator-bookworm-amd64-4g.img.zst.manifest.json.sha256" \
  --path "$upgrade_dir/ly-route-upgrade-gateway-bookworm-amd64.tar.zst" \
  --path "$upgrade_dir/ly-route-upgrade-gateway-bookworm-amd64.tar.zst.sha256" \
  --path "$upgrade_dir/ly-route-upgrade-gateway-bookworm-amd64.manifest.json" \
  --path "$upgrade_dir/ly-route-upgrade-gateway-bookworm-amd64.manifest.json.sha256" \
  --path "$upgrade_dir/ly-route-upgrade-orchestrator-bookworm-amd64.tar.zst" \
  --path "$upgrade_dir/ly-route-upgrade-orchestrator-bookworm-amd64.tar.zst.sha256" \
  --path "$upgrade_dir/ly-route-upgrade-orchestrator-bookworm-amd64.manifest.json" \
  --path "$upgrade_dir/ly-route-upgrade-orchestrator-bookworm-amd64.manifest.json.sha256"
