#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-rockchip-armbian-image.sh --rootfs ROOTFS --board BOARD [options]

Build a Rockchip/Armbian-style bootable SD/eMMC image from a Ly Route arm64
rootfs. The board list is derived from VIKINGYFY/immortalwrt Rockchip DTS names.

Required:
  --rootfs FILE          ly-route-rootfs-*-arm64.tar.zst
  --board NAME          Board from config/rockchip-boards.tsv

Options:
  --out DIR             Output directory, default dist/rockchip-armbian
  --size SIZE           Image size, default 4G
  --suite SUITE         Debian suite label in output name, default bookworm
  --board-file FILE     Board manifest, default config/rockchip-boards.tsv
  --kernel FILE         Kernel Image/vmlinuz to place in /boot
  --initrd FILE         Initrd to place in /boot
  --dtb FILE            Compiled DTB. If omitted, the script searches the rootfs.
  --uboot FILE          Optional combined Rockchip U-Boot loader blob.
  --loader-bootloader FILE  Optional ophub BOOTLOADER_IMG/idbloader image.
  --loader-mainline FILE    Optional ophub MAINLINE_UBOOT image.
  --loader-trust FILE       Optional ophub TRUST_IMG image.
  --cmdline TEXT        Kernel cmdline suffix.
  --release-timestamp TEXT  Artifact timestamp, default YY.MM.DD-HH.MM.SS UTC.
  --list-boards         Print supported board names.

Rockchip loader placement follows immortalwrt's image rules: one combined loader
blob is written at sector 64. If your board requires separate idbloader/uboot/trust
images, combine them first or use a board-specific loader from Armbian/U-Boot.
EOF
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
board_file="$repo_root/config/rockchip-boards.tsv"
rootfs=""
board=""
out_dir="dist/rockchip-armbian"
size="4G"
suite="bookworm"
kernel=""
initrd=""
dtb=""
uboot=""
loader_bootloader=""
loader_mainline=""
loader_trust=""
cmdline="net.ifnames=1 quiet"
release_timestamp="${LY_ROUTE_RELEASE_TIMESTAMP:-}"

list_boards() {
  awk -F '\t' 'BEGIN { printf "TARGET\tSOC\tDTB\tBOOT_FLOW\n" } /^[^#]/ { printf "%s\t%s\t%s\t%s\n", $1, $2, $3, $4 }' "$board_file"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --rootfs) rootfs="${2:-}"; shift 2 ;;
    --board) board="${2:-}"; shift 2 ;;
    --out) out_dir="${2:-}"; shift 2 ;;
    --size) size="${2:-}"; shift 2 ;;
    --suite) suite="${2:-}"; shift 2 ;;
    --board-file) board_file="${2:-}"; shift 2 ;;
    --kernel) kernel="${2:-}"; shift 2 ;;
    --initrd) initrd="${2:-}"; shift 2 ;;
    --dtb) dtb="${2:-}"; shift 2 ;;
    --uboot) uboot="${2:-}"; shift 2 ;;
    --loader-bootloader) loader_bootloader="${2:-}"; shift 2 ;;
    --loader-mainline) loader_mainline="${2:-}"; shift 2 ;;
    --loader-trust) loader_trust="${2:-}"; shift 2 ;;
    --cmdline) cmdline="${2:-}"; shift 2 ;;
    --release-timestamp) release_timestamp="${2:-}"; shift 2 ;;
    --list-boards) list_boards; exit 0 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "build-rockchip-armbian-image.sh must run as root for loop mounts" >&2
  exit 1
fi
if [ -z "$rootfs" ] || [ ! -f "$rootfs" ]; then
  echo "--rootfs is required and must point to an existing arm64 tarball" >&2
  exit 2
fi
if [ -z "$board" ]; then
  echo "--board is required. Use --list-boards." >&2
  exit 2
fi

board_row=$(awk -F '\t' -v board="$board" '$1 == board { print }' "$board_file")
if [ -z "$board_row" ]; then
  echo "Unknown board: $board" >&2
  list_boards >&2
  exit 2
fi

IFS=$'\t' read -r board_name soc dtb_name boot_flow uboot_device immortalwrt_dts <<EOF
$board_row
EOF

for cmd in sgdisk losetup mkfs.ext4 mkfs.vfat mount umount tar rsync blkid partprobe zstd sha256sum; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command missing: $cmd" >&2
    exit 1
  fi
done

case "$out_dir" in
  /*) artifact_dir="$out_dir" ;;
  *) artifact_dir="$repo_root/$out_dir" ;;
esac
mkdir -p "$artifact_dir"

safe_size=$(printf '%s' "$size" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]._-')
if [ -z "$safe_size" ]; then
  echo "Invalid --size value: $size" >&2
  exit 2
fi
work_parent=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-rockchip-armbian.XXXXXX")
root_tree="$work_parent/rootfs"
mnt="$work_parent/mnt"
loop=""

setup_loop_devices() {
  modprobe loop 2>/dev/null || true
  if [ ! -c /dev/loop-control ]; then
    rm -f /dev/loop-control
    mknod -m 660 /dev/loop-control c 10 237
  fi
  for index in $(seq 0 63); do
    if [ ! -b "/dev/loop$index" ]; then
      rm -f "/dev/loop$index"
      mknod -m 660 "/dev/loop$index" b 7 "$index"
    fi
  done
}

if [ -z "$release_timestamp" ]; then
  release_timestamp=$(date -u +%y.%m.%d-%H.%M.%S)
fi
safe_timestamp=$(printf '%s' "$release_timestamp" | tr -cd '[:alnum:]._-')
if [ -z "$safe_timestamp" ]; then
  echo "Invalid release timestamp: $release_timestamp" >&2
  exit 2
fi
image="$artifact_dir/rockchip-armv8-${board_name}-${safe_timestamp}.img"

cleanup() {
  (
  set +e
  if mountpoint -q "$mnt/boot"; then umount "$mnt/boot" 2>/dev/null || umount -l "$mnt/boot" 2>/dev/null || true; fi
  if mountpoint -q "$mnt"; then umount "$mnt" 2>/dev/null || umount -l "$mnt" 2>/dev/null || true; fi
  if [ -n "$loop" ]; then losetup -d "$loop"; fi
  rm -rf "$work_parent"
  )
}
trap cleanup EXIT INT TERM

cleanup
rm -rf "$work_parent"
mkdir -p "$root_tree" "$mnt"
tar --numeric-owner --use-compress-program=unzstd -xf "$rootfs" -C "$root_tree"

if [ -z "$kernel" ]; then
  kernel=$(find "$root_tree/boot" -maxdepth 1 -type f \( -name 'Image-*' -o -name 'vmlinuz-*' -o -name 'Image' -o -name 'vmlinuz' \) 2>/dev/null | sort | tail -n 1 || true)
fi
if [ -z "$initrd" ]; then
  initrd=$(find "$root_tree/boot" -maxdepth 1 -type f \( -name 'initrd.img-*' -o -name 'initramfs-*' -o -name 'uInitrd' \) 2>/dev/null | sort | tail -n 1 || true)
fi
if [ -z "$dtb" ]; then
  dtb=$(find "$root_tree" -type f \( -path "*/rockchip/${dtb_name}.dtb" -o -name "${dtb_name}.dtb" \) 2>/dev/null | sort | tail -n 1 || true)
fi
if [ -z "$uboot" ] && [ -z "$loader_bootloader" ]; then
  uboot=$(find "$root_tree" -type f \( -path "*/${uboot_device}/u-boot-rockchip.bin" -o -name "${uboot_device}-u-boot-rockchip.bin" -o -name "${uboot_device}.bin" \) 2>/dev/null | sort | tail -n 1 || true)
fi

if [ -z "$kernel" ] || [ ! -f "$kernel" ]; then
  echo "No kernel image found. Provide --kernel from an Armbian/Rockchip kernel package." >&2
  exit 1
fi
if [ -z "$dtb" ] || [ ! -f "$dtb" ]; then
  echo "No compiled DTB found for $board ($dtb_name). Provide --dtb or install a kernel package containing it." >&2
  echo "immortalwrt DTS reference: $immortalwrt_dts" >&2
  exit 1
fi
if [ -n "$loader_bootloader" ] && [ ! -f "$loader_bootloader" ]; then
  echo "Ophub bootloader image not found for $board: $loader_bootloader" >&2
  exit 1
fi
if [ -n "$loader_mainline" ] && [ ! -f "$loader_mainline" ]; then
  echo "Ophub mainline U-Boot image not found for $board: $loader_mainline" >&2
  exit 1
fi
if [ -n "$loader_trust" ] && [ ! -f "$loader_trust" ]; then
  echo "Ophub trust image not found for $board: $loader_trust" >&2
  exit 1
fi
if [ -z "$loader_bootloader" ] && { [ -z "$uboot" ] || [ ! -f "$uboot" ]; }; then
  echo "No combined Rockchip U-Boot loader found for $board ($uboot_device). Install u-boot-rockchip in the rootfs or provide --uboot." >&2
  exit 1
fi

rm -f "$image" "$image.gz" "$image.sha256" "$image.gz.sha256"
truncate -s "$size" "$image"
sgdisk --zap-all "$image"
sgdisk -n 1:32768:+256M -t 1:0700 -c 1:LYROUTE_BOOT "$image"
sgdisk -n 2:0:0 -t 2:8300 -c 2:LYROUTE_ROOT "$image"

setup_loop_devices
loop=$(losetup --find --partscan --show "$image")
partprobe "$loop"
sleep 1
mkfs.vfat -F 32 -n LYROUTEBOOT "${loop}p1"
mkfs.ext4 -F -L LYROUTE_ROOT "${loop}p2"
mount "${loop}p2" "$mnt"
mkdir -p "$mnt/boot"
mount "${loop}p1" "$mnt/boot"
rsync -aHAX --numeric-ids "$root_tree/" "$mnt/"
mkdir -p "$mnt/boot/dtb/rockchip" "$mnt/boot/extlinux"
cp "$kernel" "$mnt/boot/Image"
if [ -n "$initrd" ] && [ -f "$initrd" ]; then
  cp "$initrd" "$mnt/boot/initrd.img"
fi
cp "$dtb" "$mnt/boot/dtb/rockchip/${dtb_name}.dtb"
root_uuid=$(blkid -s UUID -o value "${loop}p2")
cat > "$mnt/etc/fstab" <<EOF
UUID=$root_uuid / ext4 defaults,noatime 0 1
LABEL=LYROUTEBOOT /boot vfat defaults 0 2
EOF
cat > "$mnt/boot/extlinux/extlinux.conf" <<EOF
TIMEOUT 30
DEFAULT ly-route

LABEL ly-route
  MENU LABEL Ly Route ${suite} ${board_name}
  LINUX /Image
  FDT /dtb/rockchip/${dtb_name}.dtb
EOF
if [ -n "$initrd" ] && [ -f "$initrd" ]; then
  printf '  INITRD /initrd.img\n' >> "$mnt/boot/extlinux/extlinux.conf"
fi
cat >> "$mnt/boot/extlinux/extlinux.conf" <<EOF
  APPEND root=UUID=$root_uuid rootwait rw ${cmdline}
EOF
cat > "$mnt/etc/ly-route/rockchip-board.env" <<EOF
LY_ROUTE_BOARD=$board_name
LY_ROUTE_SOC=$soc
LY_ROUTE_DTB=$dtb_name
LY_ROUTE_BOOT_FLOW=$boot_flow
LY_ROUTE_UBOOT_DEVICE=$uboot_device
LY_ROUTE_IMMORTALWRT_DTS=$immortalwrt_dts
EOF

sync
umount "$mnt/boot"
umount "$mnt"

if [ -n "$loader_bootloader" ]; then
  if [ -n "$loader_mainline" ]; then
    dd if="$loader_bootloader" of="$image" conv=fsync,notrunc bs=512 seek=64
    dd if="$loader_mainline" of="$image" conv=fsync,notrunc bs=512 seek=16384
    if [ -n "$loader_trust" ]; then
      dd if="$loader_trust" of="$image" conv=fsync,notrunc bs=512 seek=24576
    fi
  else
    dd if="$loader_bootloader" of="$image" conv=fsync,notrunc bs=512 seek=64
  fi
else
  dd if="$uboot" of="$image" conv=fsync,notrunc bs=512 seek=64
fi

sha256sum "$image" > "$image.sha256"
gzip -f -k "$image"
sha256sum "$image.gz" > "$image.gz.sha256"

cat > "$artifact_dir/rockchip-armv8-${board_name}-${safe_timestamp}.manifest" <<EOF
board=$board_name
soc=$soc
dtb=$dtb_name
boot_flow=$boot_flow
uboot_device_name=$uboot_device
loader_bootloader=${loader_bootloader:-}
loader_mainline=${loader_mainline:-}
loader_trust=${loader_trust:-}
immortalwrt_dts=$immortalwrt_dts
rootfs=$rootfs
kernel=$kernel
initrd=${initrd:-none}
uboot=${uboot:-none}
image=$image
EOF

printf 'built Rockchip Armbian-style image: %s.gz\n' "$image"
