#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
disk_builder="$repo_root/scripts/build-disk-image.sh"
host_release="$repo_root/scripts/gitea-host-x86-release.sh"
upgrade_builder="$repo_root/scripts/build-upgrade-package.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-x86-release-test.XXXXXX")
builder_tmp="$tmp/builder-tmp"
release_dir="$tmp/release"
mkdir -p "$builder_tmp"

cleanup() {
  status=$?
  set +e
  if command -v losetup >/dev/null 2>&1 && losetup -a | grep -F "$builder_tmp" >/dev/null 2>&1; then
    echo "cleanup failure: loop device still references $builder_tmp" >&2
    status=1
  fi
  if mount | grep -F "$builder_tmp" >/dev/null 2>&1; then
    echo "cleanup failure: mount still references $builder_tmp" >&2
    status=1
  fi
  rm -rf "$tmp"
  printf 'cleanup receipt: no retained fixture mounts, loops, or temp directory\n'
  exit "$status"
}
trap cleanup EXIT INT TERM

fail() {
  echo "$*" >&2
  exit 1
}

expect_rejection() {
  label=$1
  expected=$2
  shift 2
  if "$@" >"$tmp/rejection.out" 2>"$tmp/rejection.err"; then
    fail "$label unexpectedly succeeded"
  fi
  grep -F -- "$expected" "$tmp/rejection.err" >/dev/null || {
    cat "$tmp/rejection.err" >&2
    fail "$label did not emit: $expected"
  }
}

for command_name in node tar sha256sum zstd; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required test command missing: $command_name"
done

cat >"$tmp/control" <<'EOF'
#!/bin/sh
exit 0
EOF
cat >"$tmp/vpp-apply" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$tmp/control" "$tmp/vpp-apply"

expect_rejection "ambiguous disk image" "--product is required (gateway or orchestrator)" \
  "$disk_builder" --rootfs "$tmp/control" --out "$tmp/rejected-ambiguous"

env \
  TMPDIR="$builder_tmp" \
  SOURCE_DATE_EPOCH=0 \
  GITHUB_SHA=fixture-commit \
  LY_ROUTE_HOST_SOURCE_DIR="$repo_root" \
  LY_ROUTE_RELEASE_OUTPUT_DIR="$release_dir" \
  LY_ROUTE_RELEASE_BUILD_ONLY=1 \
  LY_ROUTE_RELEASE_FIXTURE=1 \
  LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1 \
  LY_ROUTE_BUILD_TAR_ONLY=1 \
  LY_ROUTE_DISK_IMAGE_FIXTURE=1 \
  LY_ROUTE_CONTROL_BINARY="$tmp/control" \
  LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
  "$host_release" >/dev/null

validate_image() {
  product=$1
  image_base="ly-route-$product-bookworm-amd64-4g.img"
  rootfs_base="ly-route-rootfs-$product-bookworm-amd64.tar"
  upgrade_base="ly-route-upgrade-$product-bookworm-amd64.tar"
  image="$release_dir/physical-firmware/$image_base"
  compressed="$image.zst"
  image_manifest="$compressed.manifest.json"
  upgrade="$release_dir/upgrade/$upgrade_base"
  upgrade_manifest="$release_dir/upgrade/ly-route-upgrade-$product-bookworm-amd64.manifest.json"

  for artifact in "$image" "$compressed" "$image.sha256" "$compressed.sha256" \
    "$image_manifest" "$image_manifest.sha256" "$upgrade" "$upgrade.sha256" \
    "$upgrade_manifest" "$upgrade_manifest.sha256"; do
    test -f "$artifact" || fail "missing product-qualified artifact: $artifact"
  done
  (cd "$release_dir/physical-firmware" && \
    sha256sum -c "$image_base.sha256" >/dev/null && \
    sha256sum -c "$image_base.zst.sha256" >/dev/null && \
    sha256sum -c "$image_base.zst.manifest.json.sha256" >/dev/null)
  (cd "$release_dir/upgrade" && \
    sha256sum -c "$upgrade_base.sha256" >/dev/null && \
    sha256sum -c "ly-route-upgrade-$product-bookworm-amd64.manifest.json.sha256" >/dev/null)
  zstd -t "$compressed" >/dev/null

  extract="$tmp/extract-$product"
  mkdir -p "$extract"
  tar -xf "$image" -C "$extract"
  cmp "$repo_root/packaging/product-profiles/$product.json" "$extract/etc/ly-route/product-manifest.json"
  cmp "$extract/etc/ly-route/product-manifest.json" "$extract/opt/ly-route/admin/capabilities.json"
  grep -F "window.LY_ROUTE_PRODUCT_ENTRYPOINT = \"$product\";" "$extract/opt/ly-route/admin/app.js" >/dev/null
  grep -F "LY_ROUTE_DB_PATH=/var/lib/ly-route/$product/ly-route.db" \
    "$extract/etc/systemd/system/ly-route-control-api.service" >/dev/null
  test -L "$extract/etc/systemd/system/multi-user.target.wants/ly-route-firstboot.service"

  node - "$repo_root/packaging/build-profiles/$product.json" \
    "$extract/usr/share/ly-route/artifact-manifest.json" "$image_manifest" \
    "$upgrade_manifest" "$product" "$image_base" "$rootfs_base" <<'NODE'
const { readFileSync } = require("node:fs");
const [profilePath, rootfsManifestPath, imageManifestPath, upgradeManifestPath,
  product, imageName, rootfsName] = process.argv.slice(2);
const read = (path) => JSON.parse(readFileSync(path, "utf8"));
const profile = read(profilePath);
const rootfs = read(rootfsManifestPath);
const image = read(imageManifestPath);
const upgrade = read(upgradeManifestPath);
const same = (left, right) => JSON.stringify(left) === JSON.stringify(right);
if (rootfs.product !== product || rootfs.artifact_name !== rootfsName ||
    rootfs.frontend_product !== profile.frontend_product ||
    rootfs.database_path !== profile.database_path ||
    !same(rootfs.services, profile.services) || !same(rootfs.systemd_units, profile.systemd_units)) {
  throw new Error(`rootfs allowlist mismatch for ${product}`);
}
if (image.schema_version !== 1 || image.artifact_type !== "disk-image" ||
    image.artifact_name !== `${imageName}.zst` || image.product !== product ||
    image.rootfs_artifact !== rootfsName || image.frontend_product !== profile.frontend_product ||
    image.database_path !== profile.database_path || !same(image.services, profile.services) ||
    !same(image.systemd_units, profile.systemd_units) ||
    !/^[0-9a-f]{64}$/.test(image.checksums.image_sha256) ||
    !/^[0-9a-f]{64}$/.test(image.checksums.compressed_sha256)) {
  throw new Error(`disk image manifest mismatch for ${product}`);
}
if (upgrade.product !== product || upgrade.arch !== "amd64" ||
    !same(upgrade.services, profile.services)) {
  throw new Error(`upgrade sidecar manifest mismatch for ${product}`);
}
NODE

  if [ "$product" = gateway ]; then
    test -f "$extract/etc/kea/kea-dhcp4.conf"
    test -f "$extract/etc/systemd/system/ly-route-policy-routing.service"
    grep -F 'with DHCP enabled' "$extract/usr/lib/ly-route/firstboot.sh" >/dev/null
  else
    test ! -e "$extract/etc/kea"
    test ! -e "$extract/etc/systemd/system/ly-route-policy-routing.service"
    if grep -F 'with DHCP enabled' "$extract/usr/lib/ly-route/firstboot.sh" >/dev/null; then
      fail "orchestrator first boot advertises Gateway DHCP"
    fi
  fi
}

validate_image gateway
validate_image orchestrator

if find "$release_dir" -type f \( -name 'ly-route-bookworm-*' -o -name 'ly-route-rootfs-bookworm-*' -o -name 'ly-route-upgrade-bookworm-*' \) -print -quit | grep -q .; then
  fail "release contains an ambiguous artifact name"
fi

gateway_rootfs="$release_dir/rootfs/ly-route-rootfs-gateway-bookworm-amd64.tar"
orchestrator_rootfs="$release_dir/rootfs/ly-route-rootfs-orchestrator-bookworm-amd64.tar"
expect_rejection "unsafe suite" "Unsupported suite: ../escape (expected bookworm)" \
  env TMPDIR="$builder_tmp" LY_ROUTE_DISK_IMAGE_FIXTURE=1 SOURCE_DATE_EPOCH=0 \
  "$disk_builder" --product gateway --suite ../escape --rootfs "$gateway_rootfs" --out "$tmp/rejected-suite"
expect_rejection "wrong-product image" "Rootfs product mismatch: expected orchestrator, got gateway" \
  env TMPDIR="$builder_tmp" LY_ROUTE_DISK_IMAGE_FIXTURE=1 SOURCE_DATE_EPOCH=0 \
  "$disk_builder" --product orchestrator --rootfs "$gateway_rootfs" --out "$tmp/rejected-product"

tampered_tree="$tmp/tampered-tree"
mkdir -p "$tampered_tree"
tar -xf "$gateway_rootfs" -C "$tampered_tree"
node - "$tampered_tree/usr/share/ly-route/artifact-manifest.json" <<'NODE'
const fs = require("node:fs");
const path = process.argv[2];
const manifest = JSON.parse(fs.readFileSync(path, "utf8"));
manifest.services = manifest.services.filter((service) => service !== "smartdns");
fs.writeFileSync(path, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
tampered_rootfs="$tmp/ly-route-rootfs-gateway-bookworm-amd64.tar"
(cd "$tampered_tree" && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cpf "$tampered_rootfs" .)
(cd "$tmp" && sha256sum "$(basename "$tampered_rootfs")" >"$(basename "$tampered_rootfs").sha256")
expect_rejection "tampered rootfs manifest" "Rootfs artifact manifest does not match canonical gateway build profile" \
  env TMPDIR="$builder_tmp" LY_ROUTE_DISK_IMAGE_FIXTURE=1 SOURCE_DATE_EPOCH=0 \
  "$disk_builder" --product gateway --rootfs "$tampered_rootfs" --out "$tmp/rejected-tamper"

unsafe_tree="$tmp/unsafe-tree"
mkdir -p "$unsafe_tree"
ln -s /tmp "$unsafe_tree/etc"
unsafe_rootfs="$tmp/ly-route-rootfs-gateway-bookworm-amd64-unsafe.tar"
tar -cf "$unsafe_rootfs" -C "$unsafe_tree" .
(cd "$tmp" && sha256sum "$(basename "$unsafe_rootfs")" >"$(basename "$unsafe_rootfs").sha256")
expect_rejection "unsafe rootfs directory" "Rootfs archive contains an unsafe directory: etc" \
  env TMPDIR="$builder_tmp" LY_ROUTE_DISK_IMAGE_FIXTURE=1 SOURCE_DATE_EPOCH=0 \
  "$disk_builder" --product gateway --rootfs "$unsafe_rootfs" --out "$tmp/rejected-member"

expect_rejection "cross-product upgrade" "Upgrade base product mismatch: expected gateway, got orchestrator" \
  env TMPDIR="$builder_tmp" SOURCE_DATE_EPOCH=0 GITHUB_SHA=fixture-commit \
    LY_ROUTE_BUILD_TAR_ONLY=1 LY_ROUTE_CONTROL_BINARY="$tmp/control" LY_ROUTE_CONTROL_PRODUCT=gateway \
    LY_ROUTE_VPP_APPLY_BINARY="$tmp/vpp-apply" \
  "$upgrade_builder" --product gateway --arch amd64 \
    --base-manifest "$repo_root/packaging/product-profiles/orchestrator.json" \
    --out "$tmp/rejected-upgrade"

deterministic_dir="$tmp/deterministic"
env TMPDIR="$builder_tmp" LY_ROUTE_DISK_IMAGE_FIXTURE=1 SOURCE_DATE_EPOCH=0 \
  "$disk_builder" --product gateway --rootfs "$gateway_rootfs" --out "$deterministic_dir" >/dev/null
cmp "$release_dir/physical-firmware/ly-route-gateway-bookworm-amd64-4g.img" \
  "$deterministic_dir/ly-route-gateway-bookworm-amd64-4g.img"
cmp "$release_dir/physical-firmware/ly-route-gateway-bookworm-amd64-4g.img.zst" \
  "$deterministic_dir/ly-route-gateway-bookworm-amd64-4g.img.zst"

if command -v losetup >/dev/null 2>&1 && losetup -a | grep -F "$builder_tmp" >/dev/null 2>&1; then
  fail "fixture retained a loop device"
fi
if mount | grep -F "$builder_tmp" >/dev/null 2>&1; then
  fail "fixture retained a mount"
fi
if find "$builder_tmp" -mindepth 1 -maxdepth 1 -type d -name 'ly-route-disk-image.*' -print -quit | grep -q .; then
  fail "fixture retained a disk-image work directory"
fi

printf 'Dual x86 product release fixture tests passed\n'
