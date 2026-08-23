#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
rootfs_builder="$repo_root/scripts/build-rootfs.sh"
upgrade_builder="$repo_root/scripts/build-upgrade-package.sh"
matrix="$repo_root/packaging/fixtures/product-builders/matrix.json"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-product-builders.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

fail() {
  echo "$*" >&2
  exit 1
}

assert_no_output_files() {
  output_dir=$1
  if [ -d "$output_dir" ] && find "$output_dir" -type f -print -quit | grep -q .; then
    fail "rejected build created output files in $output_dir"
  fi
}

expect_rejection() {
  label=$1
  expected=$2
  output_dir=$3
  shift 3
  rm -rf "$output_dir"
  if "$@" >"$tmp/rejection.out" 2>"$tmp/rejection.err"; then
    fail "$label unexpectedly succeeded"
  fi
  grep -F -- "$expected" "$tmp/rejection.err" >/dev/null || {
    cat "$tmp/rejection.err" >&2
    fail "$label did not emit: $expected"
  }
  assert_no_output_files "$output_dir"
}

validate_artifact_manifest() {
  artifact_type=$1
  product=$2
  arch=$3
  manifest=$4
  profile="$repo_root/packaging/build-profiles/$product.json"
  source_fingerprint=$("$repo_root/scripts/source-fingerprint.sh" backend frontend packaging runtime scripts)
  node - "$matrix" "$profile" "$artifact_type" "$product" "$arch" "$manifest" "$source_fingerprint" <<'NODE'
const { readFileSync } = require("node:fs");
const [matrixPath, profilePath, artifactType, product, arch, manifestPath, sourceFingerprint] = process.argv.slice(2);
const matrix = JSON.parse(readFileSync(matrixPath, "utf8"));
const profile = JSON.parse(readFileSync(profilePath, "utf8"));
const actual = JSON.parse(readFileSync(manifestPath, "utf8"));
const combination = matrix[artifactType].find((item) => item.product === product && item.arch === arch);
if (!combination) throw new Error(`missing ${artifactType} fixture for ${product}/${arch}`);
const expected = {
  schema_version: 1,
  artifact_type: artifactType,
  artifact_name: combination.artifact_name,
  product,
  suite: matrix.suite,
  arch,
  source_commit: matrix.source_commit,
  source_fingerprint: sourceFingerprint,
  control_profile: "/etc/ly-route/product-manifest.json",
  frontend_bundle: "/opt/ly-route/admin",
  frontend_product: profile.frontend_product,
  database_path: profile.database_path,
  config_path: profile.config_path,
  services: profile.services,
  systemd_units: profile.systemd_units,
  packages: profile.required_packages,
};
if (JSON.stringify(actual) !== JSON.stringify(expected)) {
  throw new Error(`artifact manifest mismatch\nexpected=${JSON.stringify(expected)}\nactual=${JSON.stringify(actual)}`);
}
NODE
}

validate_product_payload() {
  artifact_type=$1
  product=$2
  arch=$3
  artifact=$4
  extract_dir=$5
  rm -rf "$extract_dir"
  mkdir -p "$extract_dir"
  tar -xf "$artifact" -C "$extract_dir"
  validate_artifact_manifest "$artifact_type" "$product" "$arch" \
    "$extract_dir/usr/share/ly-route/artifact-manifest.json"
  cmp "$repo_root/packaging/product-profiles/$product.json" \
    "$extract_dir/etc/ly-route/product-manifest.json"
  cmp "$extract_dir/etc/ly-route/product-manifest.json" \
    "$extract_dir/opt/ly-route/admin/capabilities.json"
  grep -F "window.LY_ROUTE_PRODUCT_ENTRYPOINT = \"$product\";" \
    "$extract_dir/opt/ly-route/admin/app.js" >/dev/null
  grep -F "LY_ROUTE_PRODUCT_PROFILE=/etc/ly-route/product-manifest.json" \
    "$extract_dir/etc/systemd/system/ly-route-control-api.service" >/dev/null
  grep -F "LY_ROUTE_DB_PATH=/var/lib/ly-route/$product/ly-route.db" \
    "$extract_dir/etc/systemd/system/ly-route-control-api.service" >/dev/null
  grep -F "LY_ROUTE_CONFIG_PATH=/var/lib/ly-route/$product/config.json" \
    "$extract_dir/etc/systemd/system/ly-route-control-api.service" >/dev/null

  if [ "$artifact_type" = upgrade ]; then
    return 0
  fi

  if [ "$product" = gateway ]; then
    # Runtime command ordering is not a product contract: DNS VPP helpers may
    # be inserted between these services. Verify each required Gateway command.
    for command_name in smartdns kea-dhcp4 xray ipset; do
      grep -Eq "^LY_ROUTE_REQUIRED_COMMANDS=.*(^|,)${command_name}(,|$)" \
        "$extract_dir/etc/ly-route/runtime.env" || fail "Gateway runtime env misses command: $command_name"
    done
    test -f "$extract_dir/etc/kea/kea-dhcp4.conf"
    test -x "$extract_dir/usr/lib/ly-route/ly-route-pppoe-client"
    test -f "$extract_dir/etc/systemd/system/ly-route-pppoe.target"
    test -f "$extract_dir/etc/systemd/system/ly-route-pppoe@.service"
    test -f "$extract_dir/etc/systemd/system/ly-route-policy-routing.service"
  else
    for forbidden in smartdns kea-dhcp4 xray pppd; do
      if grep -R -F -- "$forbidden" \
        "$extract_dir/etc/ly-route/runtime.env" \
        "$extract_dir/usr/share/ly-route/artifact-manifest.json" >/dev/null 2>&1; then
        fail "Orchestrator payload contains forbidden service/package: $forbidden"
      fi
    done
    test ! -e "$extract_dir/etc/kea"
    test ! -e "$extract_dir/usr/lib/ly-route/ly-route-pppoe-client"
    test ! -e "$extract_dir/etc/systemd/system/ly-route-pppoe.target"
    test ! -e "$extract_dir/etc/systemd/system/ly-route-pppoe@.service"
    test ! -e "$extract_dir/etc/systemd/system/ly-route-policy-routing.service"
    test ! -e "$extract_dir/usr/lib/ly-route/policy-routing-apply-default"
  fi
}

validate_upgrade_manifest() {
  product=$1
  arch=$2
  root=$3
  profile="$repo_root/packaging/build-profiles/$product.json"
  node - "$root/manifest.json" "$profile" "$product" "$arch" <<'NODE'
const { readFileSync } = require("node:fs");
const [manifestPath, profilePath, product, arch] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
const profile = JSON.parse(readFileSync(profilePath, "utf8"));
if (manifest.package_type !== "ly-route-upgrade" || manifest.product !== product ||
    manifest.suite !== "bookworm" || manifest.arch !== arch ||
    manifest.commit !== "fixture-commit" || manifest.created_at !== "1970-01-01T00:00:00Z" ||
    JSON.stringify(manifest.services) !== JSON.stringify(profile.services)) {
  throw new Error(`invalid upgrade manifest for ${product}/${arch}`);
}
const expectedChecksums = [
  "etc/ly-route/product-manifest.json",
  "etc/nginx/conf.d/ly-route-admin.conf",
  "etc/systemd/system/ly-route-control-api.service",
  "opt/ly-route/admin/app.js",
  "opt/ly-route/admin/capabilities.json",
  "opt/ly-route/admin/index.html",
  "opt/ly-route/admin/shell.js",
  "opt/ly-route/admin/styles.css",
  "usr/lib/ly-route/ly-route-control",
  "usr/lib/ly-route/vpp-apply",
  "usr/share/ly-route/artifact-manifest.json",
];
if (JSON.stringify(Object.keys(manifest.checksums).sort()) !== JSON.stringify(expectedChecksums)) {
  throw new Error(`upgrade checksum allowlist mismatch for ${product}/${arch}`);
}
NODE
  (cd "$root" && sha256sum -c checksums.sha256 >/dev/null)
}

for product in gateway orchestrator; do
  cat >"$tmp/control-$product" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod 0755 "$tmp/control-$product"
  "$repo_root/scripts/build-controller-shell.sh" --product "$product" --out "$tmp/bundle-$product" >/dev/null
done
cat >"$tmp/vpp-apply" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$tmp/vpp-apply"

common_rootfs_env="SOURCE_DATE_EPOCH=0 GITHUB_SHA=fixture-commit LY_ROUTE_BUILD_TAR_ONLY=1 LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1"
common_upgrade_env="SOURCE_DATE_EPOCH=0 GITHUB_SHA=fixture-commit LY_ROUTE_BUILD_TAR_ONLY=1"

expect_rejection "omitted rootfs product" "--product is required (gateway or orchestrator)" "$tmp/reject-rootfs" \
  env SOURCE_DATE_EPOCH=0 LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1 LY_ROUTE_CONTROL_BINARY="$tmp/control-gateway" \
  "$rootfs_builder" --arch amd64 --out "$tmp/reject-rootfs"
expect_rejection "omitted upgrade product" "--product is required (gateway or orchestrator)" "$tmp/reject-upgrade" \
  "$upgrade_builder" --arch amd64 --out "$tmp/reject-upgrade"
expect_rejection "unknown rootfs product" "Unsupported product: invalid (expected gateway or orchestrator)" "$tmp/reject-rootfs" \
  "$rootfs_builder" --product invalid --out "$tmp/reject-rootfs"
expect_rejection "unknown upgrade product" "Unsupported product: invalid (expected gateway or orchestrator)" "$tmp/reject-upgrade" \
  "$upgrade_builder" --product invalid --out "$tmp/reject-upgrade"
expect_rejection "wrong rootfs frontend bundle" "Frontend bundle product mismatch: expected gateway" "$tmp/reject-rootfs" \
  env $common_rootfs_env LY_ROUTE_CONTROL_BINARY="$tmp/control-gateway" LY_ROUTE_CONTROL_PRODUCT=gateway \
  "$rootfs_builder" --product gateway --frontend-bundle "$tmp/bundle-orchestrator" --out "$tmp/reject-rootfs"
expect_rejection "wrong upgrade frontend bundle" "Frontend bundle product mismatch: expected gateway" "$tmp/reject-upgrade" \
  env $common_upgrade_env LY_ROUTE_CONTROL_BINARY="$tmp/control-gateway" LY_ROUTE_CONTROL_PRODUCT=gateway LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
  "$upgrade_builder" --product gateway --frontend-bundle "$tmp/bundle-orchestrator" --out "$tmp/reject-upgrade"

cp "$repo_root/packaging/product-profiles/gateway.json" "$tmp/tampered-profile.json"
node - "$tmp/tampered-profile.json" <<'NODE'
const fs = require("node:fs");
const path = process.argv[2];
const profile = JSON.parse(fs.readFileSync(path, "utf8"));
profile.services = profile.services.filter((service) => service !== "smartdns");
fs.writeFileSync(path, `${JSON.stringify(profile, null, 2)}\n`);
NODE
expect_rejection "tampered rootfs profile" "Product manifest mismatch: expected gateway canonical profile" "$tmp/reject-rootfs" \
  env $common_rootfs_env LY_ROUTE_CONTROL_BINARY="$tmp/control-gateway" LY_ROUTE_CONTROL_PRODUCT=gateway \
  "$rootfs_builder" --product gateway --manifest "$tmp/tampered-profile.json" --out "$tmp/reject-rootfs"
expect_rejection "cross-product upgrade" "Upgrade base product mismatch: expected gateway, got orchestrator" "$tmp/reject-upgrade" \
  env $common_upgrade_env LY_ROUTE_CONTROL_BINARY="$tmp/control-gateway" LY_ROUTE_CONTROL_PRODUCT=gateway LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
  "$upgrade_builder" --product gateway --base-manifest "$repo_root/packaging/product-profiles/orchestrator.json" --out "$tmp/reject-upgrade"

for product in gateway orchestrator; do
  for arch in amd64 arm64; do
    rootfs_name="ly-route-rootfs-$product-bookworm-$arch.tar"
    rootfs_a="$tmp/rootfs-a-$product-$arch"
    rootfs_b="$tmp/rootfs-b-$product-$arch"
    env $common_rootfs_env LY_ROUTE_CONTROL_BINARY="$tmp/control-$product" LY_ROUTE_CONTROL_PRODUCT="$product" LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
      "$rootfs_builder" --product "$product" --arch "$arch" --frontend-bundle "$tmp/bundle-$product" --out "$rootfs_a" >/dev/null
    env $common_rootfs_env LY_ROUTE_CONTROL_BINARY="$tmp/control-$product" LY_ROUTE_CONTROL_PRODUCT="$product" LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
      "$rootfs_builder" --product "$product" --arch "$arch" --frontend-bundle "$tmp/bundle-$product" --out "$rootfs_b" >/dev/null
    cmp "$rootfs_a/$rootfs_name" "$rootfs_b/$rootfs_name"
    cmp "$rootfs_a/$rootfs_name.sha256" "$rootfs_b/$rootfs_name.sha256"
    validate_product_payload rootfs "$product" "$arch" "$rootfs_a/$rootfs_name" "$tmp/extract-rootfs"

    upgrade_name="ly-route-upgrade-$product-bookworm-$arch.tar"
    upgrade_a="$tmp/upgrade-a-$product-$arch"
    upgrade_b="$tmp/upgrade-b-$product-$arch"
    env $common_upgrade_env LY_ROUTE_CONTROL_BINARY="$tmp/control-$product" LY_ROUTE_CONTROL_PRODUCT="$product" LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
      "$upgrade_builder" --product "$product" --arch "$arch" --frontend-bundle "$tmp/bundle-$product" --base-manifest "$repo_root/packaging/product-profiles/$product.json" --out "$upgrade_a" >/dev/null
    env $common_upgrade_env LY_ROUTE_CONTROL_BINARY="$tmp/control-$product" LY_ROUTE_CONTROL_PRODUCT="$product" LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
      "$upgrade_builder" --product "$product" --arch "$arch" --frontend-bundle "$tmp/bundle-$product" --base-manifest "$repo_root/packaging/product-profiles/$product.json" --out "$upgrade_b" >/dev/null
    cmp "$upgrade_a/$upgrade_name" "$upgrade_b/$upgrade_name"
    cmp "$upgrade_a/$upgrade_name.sha256" "$upgrade_b/$upgrade_name.sha256"
    validate_product_payload upgrade "$product" "$arch" "$upgrade_a/$upgrade_name" "$tmp/extract-upgrade"
    validate_upgrade_manifest "$product" "$arch" "$tmp/extract-upgrade"
  done
done

printf 'Product-aware rootfs and upgrade builder tests passed\n'
