#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-disk-image.sh --product gateway|orchestrator --rootfs FILE [--out dist/disk-image] [--size 4G] [--suite bookworm]

Builds a product-specific amd64 GPT disk image with BIOS and UEFI boot support.
The rootfs checksum and embedded Task 22 artifact manifest must match --product.

Environment:
  LY_ROUTE_MIRROR              Debian mirror URL for boot packages.
  LY_ROUTE_SECURITY_MIRROR     Debian security mirror URL for boot packages.
  LY_ROUTE_DISK_IMAGE_FIXTURE=1  Emit a deterministic tar-backed image fixture.
  SOURCE_DATE_EPOCH            Reproducible fixture archive timestamp.
  http_proxy/https_proxy may be set for apt in the chroot.
EOF
}

product=""
rootfs=""
out_dir="dist/disk-image"
size="4G"
suite="bookworm"
fixture=${LY_ROUTE_DISK_IMAGE_FIXTURE:-0}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --product|--rootfs|--out|--size|--suite)
      option=$1
      [ "$#" -ge 2 ] && [ -n "$2" ] || { echo "$option requires a value" >&2; exit 2; }
      case "$option" in
        --product) product=$2 ;;
        --rootfs) rootfs=$2 ;;
        --out) out_dir=$2 ;;
        --size) size=$2 ;;
        --suite) suite=$2 ;;
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
case "$suite" in
  bookworm) ;;
  *) echo "Unsupported suite: $suite (expected bookworm)" >&2; exit 2 ;;
esac
[ -n "$rootfs" ] && [ -f "$rootfs" ] || {
  echo "--rootfs is required and must point to an existing product-specific tarball" >&2
  exit 2
}
case "$fixture" in 0|1) ;; *) echo "LY_ROUTE_DISK_IMAGE_FIXTURE must be 0 or 1" >&2; exit 2 ;; esac

size_number=${size%[GgMm]}
size_suffix=${size#"$size_number"}
case "$size_number" in ''|*[!0-9]*) echo "Unsupported image size: $size" >&2; exit 2 ;; esac
case "$size_suffix" in G|g|M|m) ;; *) echo "Unsupported image size: $size" >&2; exit 2 ;; esac
size_label=$(printf '%s%s' "$size_number" "$size_suffix" | tr '[:upper:]' '[:lower:]')

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$repo_root/scripts/lib/product-build-profile.sh"
load_product_build_profile "$repo_root" "$product" ""

rootfs_dir=$(CDPATH= cd -- "$(dirname -- "$rootfs")" && pwd)
rootfs_name=$(basename "$rootfs")
rootfs="$rootfs_dir/$rootfs_name"
product_build_require_file "$rootfs.sha256"
if ! (cd "$rootfs_dir" && sha256sum -c "$rootfs_name.sha256" >/dev/null); then
  product_build_fail "Rootfs checksum validation failed: $rootfs_name"
fi
rootfs_hash=$(sha256sum "$rootfs" | cut -d' ' -f1)

rootfs_tar_list() {
  case "$rootfs_name" in
    *.tar.zst) tar --use-compress-program=unzstd -tf "$rootfs" ;;
    *.tar.gz) tar -tzf "$rootfs" ;;
    *.tar) tar -tf "$rootfs" ;;
    *) product_build_fail "Unsupported rootfs archive format: $rootfs_name" ;;
  esac
}

rootfs_tar_verbose_list() {
  case "$rootfs_name" in
    *.tar.zst) tar --use-compress-program=unzstd -tvf "$rootfs" ;;
    *.tar.gz) tar -tzvf "$rootfs" ;;
    *.tar) tar -tvf "$rootfs" ;;
    *) product_build_fail "Unsupported rootfs archive format: $rootfs_name" ;;
  esac
}

command -v tar >/dev/null 2>&1 || product_build_fail "required command missing: tar"
if ! rootfs_tar_list | awk '
  {
    path = $0
    sub(/^\.\//, "", path)
    if (path ~ /^\// || path == ".." || path ~ /^\.\.\// || path ~ /\/\.\.\//) invalid = 1
  }
  END { exit invalid ? 1 : 0 }
'; then
  product_build_fail "Rootfs archive contains an unsafe member path"
fi
if ! rootfs_tar_verbose_list | awk '
  # Debian base rootfs archives may contain standard device nodes and FIFOs.
  substr($0, 1, 1) !~ /^[-dlhpcb]$/ { invalid = 1 }
  END { exit invalid ? 1 : 0 }
'; then
  product_build_fail "Rootfs archive contains unsafe member types"
fi

case "$out_dir" in
  /*) artifact_dir=$out_dir ;;
  *) artifact_dir="$repo_root/$out_dir" ;;
esac
artifact_base="ly-route-${product}-${suite}-amd64-${size_label}.img"
image="$artifact_dir/$artifact_base"
compressed="$image.zst"
image_manifest="$compressed.manifest.json"
work_parent=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-disk-image.XXXXXX")
root_tree="$work_parent/rootfs"
mnt="$work_parent/mnt"
loop=""

cleanup_resources() {
  set +e
  for mount_path in "$mnt/boot/efi" "$mnt/dev/pts" "$mnt/dev" "$mnt/proc" "$mnt/sys" "$mnt" \
    "$root_tree/dev/pts" "$root_tree/dev" "$root_tree/proc" "$root_tree/sys"; do
    if mountpoint -q "$mount_path" 2>/dev/null; then
      umount "$mount_path" 2>/dev/null || umount -l "$mount_path" 2>/dev/null || true
    fi
  done
  if [ -n "$loop" ]; then
    losetup -d "$loop" 2>/dev/null || true
  fi
  set -e
}

cleanup() {
  cleanup_resources
  rm -rf "$work_parent"
}
trap cleanup EXIT INT TERM

mkdir -p "$artifact_dir" "$root_tree" "$mnt"
case "$rootfs_name" in
  *.tar.zst) tar --no-same-owner --no-same-permissions --use-compress-program=unzstd -xf "$rootfs" -C "$root_tree" ;;
  *.tar.gz) tar --no-same-owner --no-same-permissions -xzf "$rootfs" -C "$root_tree" ;;
  *.tar) tar --no-same-owner --no-same-permissions -xf "$rootfs" -C "$root_tree" ;;
  *) product_build_fail "Unsupported rootfs archive format: $rootfs_name" ;;
esac

prepare_real_directory() {
  directory=$1
  [ ! -L "$directory" ] || \
    product_build_fail "Rootfs archive contains an unsafe directory: ${directory#"$root_tree/"}"
  mkdir -p "$directory"
  [ -d "$directory" ] || product_build_fail "Rootfs directory is unavailable: ${directory#"$root_tree/"}"
}
for directory in "$root_tree/etc" "$root_tree/etc/apt" "$root_tree/etc/default" \
  "$root_tree/dev" "$root_tree/dev/pts" "$root_tree/proc" "$root_tree/sys"; do
  prepare_real_directory "$directory"
done

rootfs_manifest="$root_tree/usr/share/ly-route/artifact-manifest.json"
product_build_require_file "$rootfs_manifest"
product_build_require_file "$root_tree/etc/ly-route/product-manifest.json"
product_build_require_file "$root_tree/opt/ly-route/admin/capabilities.json"
product_build_require_file "$root_tree/opt/ly-route/admin/app.js"
product_build_require_file "$root_tree/etc/systemd/system/ly-route-control-api.service"
product_build_require_file "$root_tree/usr/lib/ly-route/firstboot.sh"

source_commit=$(node - "$PRODUCT_BUILD_PROFILE" "$PRODUCT_MANIFEST" "$rootfs_manifest" \
  "$root_tree/etc/ly-route/product-manifest.json" "$root_tree/opt/ly-route/admin/capabilities.json" \
  "$root_tree/opt/ly-route/admin/app.js" "$root_tree/etc/systemd/system/ly-route-control-api.service" \
  "$product" "$suite" "$rootfs_name" <<'NODE'
const { readFileSync } = require("node:fs");
const [profilePath, canonicalPath, artifactPath, installedPath, capabilitiesPath,
  appPath, servicePath, product, suite, rootfsName] = process.argv.slice(2);
const read = (path, label) => {
  try { return JSON.parse(readFileSync(path, "utf8")); }
  catch { console.error(`${label} must be valid JSON: ${path}`); process.exit(1); }
};
const profile = read(profilePath, "Build profile");
const canonical = read(canonicalPath, "Canonical product manifest");
const artifact = read(artifactPath, "Rootfs artifact manifest");
const installed = read(installedPath, "Installed product manifest");
const capabilities = read(capabilitiesPath, "Frontend capabilities");
if (artifact.product !== product) {
  console.error(`Rootfs product mismatch: expected ${product}, got ${String(artifact.product)}`);
  process.exit(1);
}
const same = (left, right) => JSON.stringify(left) === JSON.stringify(right);
const valid = artifact.schema_version === 1 && artifact.artifact_type === "rootfs" &&
  artifact.artifact_name === rootfsName && artifact.suite === suite && artifact.arch === "amd64" &&
  artifact.control_profile === "/etc/ly-route/product-manifest.json" &&
  artifact.frontend_bundle === "/opt/ly-route/admin" &&
  artifact.frontend_product === profile.frontend_product && artifact.database_path === profile.database_path &&
  artifact.config_path === profile.config_path && same(artifact.services, profile.services) &&
  same(artifact.systemd_units, profile.systemd_units) && same(artifact.packages, profile.required_packages) &&
  same(installed, canonical) && same(capabilities, canonical);
const app = readFileSync(appPath, "utf8");
const service = readFileSync(servicePath, "utf8");
if (!valid || !app.includes(`window.LY_ROUTE_PRODUCT_ENTRYPOINT = "${product}";`) ||
    !service.includes(`Environment=LY_ROUTE_DB_PATH=${profile.database_path}`) ||
    !service.includes(`Environment=LY_ROUTE_CONFIG_PATH=${profile.config_path}`)) {
  console.error(`Rootfs artifact manifest does not match canonical ${product} build profile`);
  process.exit(1);
}
process.stdout.write(artifact.source_commit);
NODE
)

rm -f "$image" "$compressed" "$image.sha256" "$compressed.sha256" \
  "$image_manifest" "$image_manifest.sha256"

if [ "$fixture" = 1 ]; then
  source_epoch=$(product_source_date_epoch)
  create_deterministic_tar "$root_tree" "$image" "$source_epoch"
else
  if [ "$(id -u)" -ne 0 ]; then
    product_build_fail "build-disk-image.sh must run as root for loop mounts and grub-install"
  fi
  for cmd in sgdisk losetup mkfs.ext4 mkfs.vfat mount umount mountpoint tar rsync grub-install \
    chroot blkid partprobe zstd sha256sum; do
    command -v "$cmd" >/dev/null 2>&1 || product_build_fail "required command missing: $cmd"
  done

  rm -f "$root_tree/etc/apt/sources.list"
  cat >"$root_tree/etc/apt/sources.list" <<EOF
deb ${LY_ROUTE_MIRROR:-http://deb.debian.org/debian} $suite main
deb ${LY_ROUTE_MIRROR:-http://deb.debian.org/debian} ${suite}-updates main
deb ${LY_ROUTE_SECURITY_MIRROR:-http://security.debian.org/debian-security} ${suite}-security main
EOF
  rm -f "$root_tree/etc/resolv.conf"
  cp /etc/resolv.conf "$root_tree/etc/resolv.conf"
  mount --bind /dev "$root_tree/dev"
  mount --bind /dev/pts "$root_tree/dev/pts"
  mount -t proc proc "$root_tree/proc"
  mount -t sysfs sysfs "$root_tree/sys"
  env -u TMPDIR chroot "$root_tree" apt-get -o Acquire::ForceIPv4=true update
  env -u TMPDIR chroot "$root_tree" env DEBIAN_FRONTEND=noninteractive \
    apt-get -o Acquire::ForceIPv4=true install -y linux-image-amd64 grub-pc-bin grub-efi-amd64-bin shim-signed dosfstools
  test -f "$root_tree/usr/bin/vpp"
  test -f "$root_tree/usr/bin/vppctl"
  test -f "$root_tree/usr/lib/systemd/system/vpp.service"
  test -f "$root_tree/etc/ly-route/vpp-command-map.json"
  umount "$root_tree/sys" "$root_tree/proc" "$root_tree/dev/pts" "$root_tree/dev"

  truncate -s "$size" "$image"
  sgdisk --zap-all "$image"
  sgdisk -n 1:2048:+2M -t 1:ef02 -c 1:BIOS-BOOT "$image"
  sgdisk -n 2:0:+256M -t 2:ef00 -c 2:EFI-SYSTEM "$image"
  sgdisk -n 3:0:0 -t 3:8300 -c 3:LY-ROUTE-ROOT "$image"

  modprobe loop 2>/dev/null || true
  if [ ! -c /dev/loop-control ]; then rm -f /dev/loop-control; mknod -m 660 /dev/loop-control c 10 237; fi
  for index in $(seq 0 63); do
    if [ ! -b "/dev/loop$index" ]; then rm -f "/dev/loop$index"; mknod -m 660 "/dev/loop$index" b 7 "$index"; fi
  done
  loop=$(losetup --find --partscan --show "$image")
  partprobe "$loop"
  sleep 1
  mkfs.vfat -F 32 -n LYROUTE_EFI "${loop}p2"
  mkfs.ext4 -F -L LYROUTE_ROOT "${loop}p3"
  mount "${loop}p3" "$mnt"
  prepare_real_directory "$root_tree/boot"
  mkdir -p "$mnt/boot/efi"
  mount "${loop}p2" "$mnt/boot/efi"
  rsync -aHAX --numeric-ids "$root_tree/" "$mnt/"
  rm -f "$mnt/dev/null" "$mnt/dev/zero" "$mnt/dev/random" "$mnt/dev/urandom"
  mknod -m 666 "$mnt/dev/null" c 1 3
  mknod -m 666 "$mnt/dev/zero" c 1 5
  mknod -m 666 "$mnt/dev/random" c 1 8
  mknod -m 666 "$mnt/dev/urandom" c 1 9
  mkdir -p "$mnt/dev/pts"

  root_uuid=$(blkid -s UUID -o value "${loop}p3")
  rm -f "$mnt/etc/fstab"
  cat >"$mnt/etc/fstab" <<EOF
UUID=$root_uuid / ext4 defaults,noatime 0 1
LABEL=LYROUTE_EFI /boot/efi vfat umask=0077 0 1
EOF
  rm -f "$mnt/etc/default/grub"
  cat >"$mnt/etc/default/grub" <<EOF
GRUB_DEFAULT=0
GRUB_TIMEOUT=3
GRUB_DISTRIBUTOR="Ly Route $product"
GRUB_CMDLINE_LINUX_DEFAULT="quiet"
GRUB_CMDLINE_LINUX="root=UUID=$root_uuid"
EOF
  mount --bind /dev "$mnt/dev"
  mount --bind /dev/pts "$mnt/dev/pts"
  mount -t proc proc "$mnt/proc"
  mount -t sysfs sysfs "$mnt/sys"
  chroot "$mnt" /usr/sbin/grub-install --target=i386-pc --boot-directory=/boot "$loop"
  chroot "$mnt" /usr/sbin/grub-install --target=x86_64-efi --efi-directory=/boot/efi --boot-directory=/boot --removable --no-nvram
  chroot "$mnt" /usr/sbin/grub-mkconfig -o /boot/grub/grub.cfg
  sync
  cleanup_resources
  loop=""

  if [ "$(stat -c %b "$image")" -lt 100000 ]; then
    product_build_fail "built image is unexpectedly sparse; rootfs copy likely failed"
  fi
fi

write_artifact_checksum "$image"
zstd -T2 -19 -f "$image" -o "$compressed"
write_artifact_checksum "$compressed"
image_hash=$(sha256sum "$image" | cut -d' ' -f1)
compressed_hash=$(sha256sum "$compressed" | cut -d' ' -f1)
write_product_artifact_manifest "$image_manifest" disk-image "$(basename "$compressed")" \
  "$product" "$suite" amd64 "$source_commit"
node - "$image_manifest" "$rootfs_name" "$rootfs_hash" "$image_hash" "$compressed_hash" <<'NODE'
const { readFileSync, writeFileSync } = require("node:fs");
const [path, rootfsArtifact, rootfsHash, imageHash, compressedHash] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(path, "utf8"));
manifest.rootfs_artifact = rootfsArtifact;
manifest.checksums = {
  rootfs_sha256: rootfsHash,
  image_sha256: imageHash,
  compressed_sha256: compressedHash,
};
writeFileSync(path, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
write_artifact_checksum "$image_manifest"
printf 'Built %s\nBuilt %s\nBuilt %s\n' "$image" "$compressed" "$image_manifest"
