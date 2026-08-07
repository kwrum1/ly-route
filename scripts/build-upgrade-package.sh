#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/build-upgrade-package.sh --product gateway|orchestrator [--arch amd64|arm64] [--suite bookworm] [--out dist/upgrade] [--manifest PATH] [--frontend-bundle DIRECTORY] [--base-manifest PATH]

Builds a product-specific online upgrade package.

Environment:
  LY_ROUTE_CONTROL_BINARY      Prebuilt product-specific control binary.
  LY_ROUTE_CONTROL_PRODUCT     Product ID for LY_ROUTE_CONTROL_BINARY.
  LY_ROUTE_VPP_APPLY_BINARY    Prebuilt vpp-apply binary for fixture builds.
  LY_ROUTE_EXTRA_DEBS_DIR      Runtime deb directory (defaults to runtime-debs).
  LY_ROUTE_BUILD_TAR_ONLY=1    Leave the deterministic fixture uncompressed.
  SOURCE_DATE_EPOCH            Reproducible archive and manifest timestamp.
EOF
}

arch=amd64
suite=bookworm
out_dir=dist/upgrade
product=
manifest=
frontend_bundle=
base_manifest=
control_binary=${LY_ROUTE_CONTROL_BINARY:-}
vpp_apply_binary=${LY_ROUTE_VPP_APPLY_BINARY:-}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --arch|--suite|--out|--product|--manifest|--frontend-bundle|--base-manifest)
      option=$1
      [ "$#" -ge 2 ] && [ -n "$2" ] || { echo "$option requires a value" >&2; exit 2; }
      case "$option" in
        --arch) arch=$2 ;;
        --suite) suite=$2 ;;
        --out) out_dir=$2 ;;
        --product) product=$2 ;;
        --manifest) manifest=$2 ;;
        --frontend-bundle) frontend_bundle=$2 ;;
        --base-manifest) base_manifest=$2 ;;
      esac
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$product" ] || { echo "--product is required (gateway or orchestrator)" >&2; exit 2; }
case "$product" in
  gateway|orchestrator) ;;
  *) echo "Unsupported product: $product (expected gateway or orchestrator)" >&2; exit 2 ;;
esac
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 2 ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
geodata_dir=${LY_ROUTE_GEODATA_DIR:-$repo_root/packaging/geodata}
. "$repo_root/scripts/lib/product-build-profile.sh"
load_product_build_profile "$repo_root" "$product" "$manifest"
validate_prebuilt_control "$control_binary" "$product"
validate_prebuilt_vpp_apply "$vpp_apply_binary"
validate_upgrade_base_manifest "$base_manifest" "$product"
product_build_require_file "$repo_root/scripts/build-controller-shell.sh"
product_build_require_file "$repo_root/packaging/nginx/ly-route-admin.conf"
product_build_require_file "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service"
if [ -n "$frontend_bundle" ]; then
  validate_frontend_bundle "$frontend_bundle" "$product"
else
  for source in index.html styles.css shell.js app.js; do
    product_build_require_file "$repo_root/frontend/$product/$source"
  done
fi

selected_vpp_deb=
if [ -z "$vpp_apply_binary" ]; then
  runtime_debs_dir=${LY_ROUTE_EXTRA_DEBS_DIR:-$repo_root/runtime-debs}
  [ -d "$runtime_debs_dir" ] || product_build_fail "Runtime deb directory does not exist: $runtime_debs_dir"
  command -v dpkg-deb >/dev/null 2>&1 || product_build_fail "dpkg-deb is required to inspect runtime packages"
  for package_path in "$runtime_debs_dir"/ly-route-vpp-apply_*.deb; do
    [ -f "$package_path" ] || continue
    package_arch=$(dpkg-deb -f "$package_path" Architecture 2>/dev/null) || product_build_fail "Invalid deb package: $package_path"
    [ "$package_arch" != "$arch" ] || selected_vpp_deb=$package_path
  done
  [ -n "$selected_vpp_deb" ] || product_build_fail "Missing ly-route-vpp-apply package for architecture: $arch"
fi
if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" != 1 ]; then
  command -v zstd >/dev/null 2>&1 || product_build_fail "zstd is required to build upgrade packages"
fi

source_commit=$(product_source_commit)
source_epoch=$(product_source_date_epoch)
created_at=$(date -u -d "@$source_epoch" +%Y-%m-%dT%H:%M:%SZ)
artifact_base="ly-route-upgrade-${product}-${suite}-${arch}"
if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" = 1 ]; then
  artifact_name="$artifact_base.tar"
else
  artifact_name="$artifact_base.tar.zst"
fi
case "$out_dir" in
  /*) artifact_dir=$out_dir ;;
  *) artifact_dir="$repo_root/$out_dir" ;;
esac
artifact="$artifact_dir/$artifact_name"
tar_artifact="$artifact_dir/$artifact_base.tar"
work_parent=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-upgrade.XXXXXX")
package_root="$work_parent/package"
trap 'rm -rf "$work_parent"' EXIT INT TERM

mkdir -p "$artifact_dir" "$package_root/usr/lib/ly-route" "$package_root/usr/share/ly-route" \
  "$package_root/opt/ly-route/admin" "$package_root/etc/ly-route" \
  "$package_root/etc/nginx/conf.d" "$package_root/etc/systemd/system"

if [ -n "$control_binary" ]; then
  cp "$control_binary" "$package_root/usr/lib/ly-route/ly-route-control"
else
  case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; esac
  command -v go >/dev/null 2>&1 || product_build_fail "go is required to build ly-route-control for upgrade packaging"
  (cd "$repo_root/backend" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$package_root/usr/lib/ly-route/ly-route-control" "./cmd/$product-control")
fi
chmod 0755 "$package_root/usr/lib/ly-route/ly-route-control"
if [ -n "$vpp_apply_binary" ]; then
  cp "$vpp_apply_binary" "$package_root/usr/lib/ly-route/vpp-apply"
else
  dpkg-deb -x "$selected_vpp_deb" "$package_root"
fi
product_build_require_file "$package_root/usr/lib/ly-route/vpp-apply"
chmod 0755 "$package_root/usr/lib/ly-route/vpp-apply"

copy_product_frontend "$package_root/opt/ly-route/admin" "$frontend_bundle" "$product"
cp "$PRODUCT_MANIFEST" "$package_root/etc/ly-route/product-manifest.json"
cp "$repo_root/packaging/nginx/ly-route-admin.conf" "$package_root/etc/nginx/conf.d/ly-route-admin.conf"
if [ "$product" = gateway ]; then
  if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" != 1 ]; then
    for geodata_file in geoip.dat geosite.dat china-list.txt manifest.json; do
      product_build_require_file "$geodata_dir/$geodata_file"
    done
  fi
  if [ -d "$geodata_dir" ]; then
    mkdir -p "$package_root/usr/share/ly-route/geodata"
    for geodata_file in geoip.dat geosite.dat china-list.txt manifest.json; do
      [ -f "$geodata_dir/$geodata_file" ] && cp "$geodata_dir/$geodata_file" "$package_root/usr/share/ly-route/geodata/$geodata_file"
    done
  fi
fi
service="$package_root/etc/systemd/system/ly-route-control-api.service"
cp "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service" "$service"
awk -v profile='Environment=LY_ROUTE_PRODUCT_PROFILE=/etc/ly-route/product-manifest.json' \
    -v database="Environment=LY_ROUTE_DB_PATH=$PRODUCT_DATABASE_PATH" \
    -v config="Environment=LY_ROUTE_CONFIG_PATH=$PRODUCT_CONFIG_PATH" '
  /^Environment=LY_ROUTE_CONFIG_PATH=/ { print profile; print database; print config; next }
  { print }
' "$service" >"$service.tmp"
mv "$service.tmp" "$service"

write_product_artifact_manifest "$package_root/usr/share/ly-route/artifact-manifest.json" upgrade \
  "$artifact_name" "$product" "$suite" "$arch" "$source_commit"

control_hash=$(sha256sum "$package_root/usr/lib/ly-route/ly-route-control" | cut -d' ' -f1)
vpp_apply_hash=$(sha256sum "$package_root/usr/lib/ly-route/vpp-apply" | cut -d' ' -f1)
app_hash=$(sha256sum "$package_root/opt/ly-route/admin/app.js" | cut -d' ' -f1)
capabilities_hash=$(sha256sum "$package_root/opt/ly-route/admin/capabilities.json" | cut -d' ' -f1)
index_hash=$(sha256sum "$package_root/opt/ly-route/admin/index.html" | cut -d' ' -f1)
shell_hash=$(sha256sum "$package_root/opt/ly-route/admin/shell.js" | cut -d' ' -f1)
styles_hash=$(sha256sum "$package_root/opt/ly-route/admin/styles.css" | cut -d' ' -f1)
nginx_hash=$(sha256sum "$package_root/etc/nginx/conf.d/ly-route-admin.conf" | cut -d' ' -f1)
service_hash=$(sha256sum "$service" | cut -d' ' -f1)
profile_hash=$(sha256sum "$package_root/etc/ly-route/product-manifest.json" | cut -d' ' -f1)
artifact_manifest_hash=$(sha256sum "$package_root/usr/share/ly-route/artifact-manifest.json" | cut -d' ' -f1)
cat >"$package_root/checksums.sha256" <<EOF
$control_hash  usr/lib/ly-route/ly-route-control
$vpp_apply_hash  usr/lib/ly-route/vpp-apply
$app_hash  opt/ly-route/admin/app.js
$capabilities_hash  opt/ly-route/admin/capabilities.json
$index_hash  opt/ly-route/admin/index.html
$shell_hash  opt/ly-route/admin/shell.js
$styles_hash  opt/ly-route/admin/styles.css
$nginx_hash  etc/nginx/conf.d/ly-route-admin.conf
$service_hash  etc/systemd/system/ly-route-control-api.service
$profile_hash  etc/ly-route/product-manifest.json
$artifact_manifest_hash  usr/share/ly-route/artifact-manifest.json
EOF
if [ -d "$package_root/usr/share/ly-route/geodata" ]; then
  for geodata_file in geoip.dat geosite.dat china-list.txt manifest.json; do
    if [ -f "$package_root/usr/share/ly-route/geodata/$geodata_file" ]; then
      geodata_hash=$(sha256sum "$package_root/usr/share/ly-route/geodata/$geodata_file" | cut -d' ' -f1)
      printf '%s  %s\n' "$geodata_hash" "usr/share/ly-route/geodata/$geodata_file" >>"$package_root/checksums.sha256"
    fi
  done
fi

node - "$package_root/manifest.json" "$PRODUCT_BUILD_PROFILE" "$product" "$suite" "$arch" \
  "$source_commit" "$created_at" "$control_hash" "$vpp_apply_hash" "$app_hash" \
  "$capabilities_hash" "$index_hash" "$shell_hash" "$styles_hash" "$nginx_hash" \
  "$service_hash" "$profile_hash" "$artifact_manifest_hash" <<'NODE'
const { readFileSync, writeFileSync } = require("node:fs");
const [output, profilePath, product, suite, arch, commit, createdAt,
  controlHash, vppHash, appHash, capabilitiesHash, indexHash, shellHash, stylesHash,
  nginxHash, serviceHash, profileHash, artifactManifestHash] = process.argv.slice(2);
const profile = JSON.parse(readFileSync(profilePath, "utf8"));
const manifest = {
  package_type: "ly-route-upgrade",
  product,
  suite,
  arch,
  commit,
  created_at: createdAt,
  install_root: "/usr/lib/ly-route",
  services: profile.services,
  checksums: {
    "usr/lib/ly-route/ly-route-control": controlHash,
    "usr/lib/ly-route/vpp-apply": vppHash,
    "opt/ly-route/admin/app.js": appHash,
    "opt/ly-route/admin/capabilities.json": capabilitiesHash,
    "opt/ly-route/admin/index.html": indexHash,
    "opt/ly-route/admin/shell.js": shellHash,
    "opt/ly-route/admin/styles.css": stylesHash,
    "etc/nginx/conf.d/ly-route-admin.conf": nginxHash,
    "etc/systemd/system/ly-route-control-api.service": serviceHash,
    "etc/ly-route/product-manifest.json": profileHash,
    "usr/share/ly-route/artifact-manifest.json": artifactManifestHash,
  },
};
writeFileSync(output, `${JSON.stringify(manifest, null, 2)}\n`);
NODE

rm -f "$artifact_dir/$artifact_base.tar" "$artifact_dir/$artifact_base.tar.zst" \
  "$artifact_dir/$artifact_base.tar.sha256" "$artifact_dir/$artifact_base.tar.zst.sha256"
create_deterministic_tar "$package_root" "$tar_artifact" "$source_epoch"
if [ "${LY_ROUTE_BUILD_TAR_ONLY:-0}" = 1 ]; then
  artifact=$tar_artifact
else
  zstd -19 -f "$tar_artifact" -o "$artifact"
  rm -f "$tar_artifact"
fi
write_artifact_checksum "$artifact"
printf 'Built %s\n' "$artifact"
