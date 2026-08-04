#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-auto-install-iso.sh --product gateway --image FILE.img.zst [--out dist/iso] [--suite bookworm]

Builds a BIOS/UEFI hybrid Debian Live ISO that verifies and installs the embedded
Ly Route disk image. A single eligible target disk is installed automatically.
On multi-disk systems pass lyroute.target=/dev/DEVICE on the kernel command line.
EOF
}

product=
image=
out_dir=dist/iso
suite=bookworm
while [ "$#" -gt 0 ]; do
  case "$1" in
    --product) product=${2:-}; shift 2 ;;
    --image) image=${2:-}; shift 2 ;;
    --out) out_dir=${2:-}; shift 2 ;;
    --suite) suite=${2:-}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ "$product" = gateway ] || { echo "--product must be gateway" >&2; exit 2; }
[ "$suite" = bookworm ] || { echo "--suite must be bookworm" >&2; exit 2; }
[ -f "$image" ] || { echo "--image must point to an existing .img.zst" >&2; exit 2; }
case "$image" in *.img.zst) ;; *) echo "--image must end in .img.zst" >&2; exit 2 ;; esac
[ -f "$image.sha256" ] || { echo "image checksum is missing: $image.sha256" >&2; exit 1; }
image_raw=${image%.zst}
[ -f "$image_raw" ] || { echo "uncompressed sibling is required to record image size: $image_raw" >&2; exit 1; }
(cd "$(dirname "$image")" && sha256sum -c "$(basename "$image").sha256" >/dev/null)

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$out_dir" in /*) artifact_dir=$out_dir ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
mkdir -p "$artifact_dir"
work=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-iso.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM

payload_name=ly-route-gateway-x86_64.img.zst
image_size=$(stat -c %s "$image_raw")
image_hash=$(sha256sum "$image_raw" | awk '{print $1}')
compressed_hash=$(sha256sum "$image" | awk '{print $1}')
iso_name=ly-route-gateway-x86_64-autoinstall.iso
iso_path="$artifact_dir/$iso_name"

mkdir -p "$work/config/includes.chroot/opt/ly-route/install" \
  "$work/config/includes.chroot/usr/local/sbin" \
  "$work/config/includes.chroot/etc/systemd/system/multi-user.target.wants" \
  "$work/config/package-lists" "$work/config/hooks/live"
cp "$image" "$work/config/includes.chroot/opt/ly-route/install/$payload_name"
printf '%s  %s\n' "$compressed_hash" "$payload_name" > \
  "$work/config/includes.chroot/opt/ly-route/install/$payload_name.sha256"
printf '%s\n' "$image_size" > "$work/config/includes.chroot/opt/ly-route/install/image.size"
printf '%s\n' "$image_hash" > "$work/config/includes.chroot/opt/ly-route/install/image.sha256"

cat > "$work/config/includes.chroot/usr/local/sbin/ly-route-auto-install" <<'INSTALLER'
#!/bin/bash
set -eu

payload_dir=/opt/ly-route/install
payload=$payload_dir/ly-route-gateway-x86_64.img.zst
log=/var/log/ly-route-installer.log
exec > >(tee -a "$log") 2>&1

say() { printf '\n[Ly Route] %s\n' "$*"; }
fail() { say "INSTALLATION STOPPED: $*"; exit 1; }
cmdline=" $(cat /proc/cmdline) "
case "$cmdline" in *' lyroute.autoinstall=1 '*) ;; *) fail 'automatic installation was not enabled' ;; esac

expected_compressed=$(awk 'NR == 1 {print $1}' "$payload.sha256")
actual_compressed=$(sha256sum "$payload" | awk '{print $1}')
[ "$actual_compressed" = "$expected_compressed" ] || fail 'embedded image checksum mismatch'

explicit=
for token in $(cat /proc/cmdline); do
  case "$token" in lyroute.target=*) explicit=${token#lyroute.target=} ;; esac
done

live_source=$(findmnt -n -o SOURCE /run/live/medium 2>/dev/null || true)
live_parent=
if [ -n "$live_source" ]; then
  live_parent=$(lsblk -ndo PKNAME "$live_source" 2>/dev/null | head -1 || true)
  [ -z "$live_parent" ] || live_parent=/dev/$live_parent
fi

eligible() {
  candidate=$1
  [ -b "$candidate" ] || return 1
  [ -w "$candidate" ] || return 1
  [ "$candidate" != "$live_parent" ] || return 1
  size=$(blockdev --getsize64 "$candidate" 2>/dev/null || printf 0)
  required=$(cat "$payload_dir/image.size")
  [ "$size" -ge "$required" ]
}

if [ -n "$explicit" ]; then
  target=$(readlink -f "$explicit")
  eligible "$target" || fail "requested target is missing, too small, read-only, or is the live medium: $explicit"
else
  targets=
  while read -r candidate type; do
    [ "$type" = disk ] || continue
    if eligible "$candidate"; then targets="$targets $candidate"; fi
  done <<EOF
$(lsblk -dnpo NAME,TYPE)
EOF
  set -- $targets
  [ "$#" -eq 1 ] || fail "expected exactly one eligible target disk, found $#; set lyroute.target=/dev/DEVICE"
  target=$1
fi

say "Installing to $target. All data on this disk will be erased."
swapoff -a 2>/dev/null || true
wipefs -a "$target" 2>/dev/null || true
zstd -dc "$payload" | dd of="$target" bs=16M conv=fsync status=progress
sync

expected_image=$(cat "$payload_dir/image.sha256")
image_size=$(cat "$payload_dir/image.size")
actual_image=$(head -c "$image_size" "$target" | sha256sum | awk '{print $1}')
[ "$actual_image" = "$expected_image" ] || fail 'target disk verification failed'
say 'Installation complete. Remove the installation media; the system will reboot.'
sleep 5
systemctl reboot --force
INSTALLER
chmod 0755 "$work/config/includes.chroot/usr/local/sbin/ly-route-auto-install"

cat > "$work/config/includes.chroot/etc/systemd/system/ly-route-auto-install.service" <<'EOF'
[Unit]
Description=Ly Route automatic appliance installer
After=live-config.service local-fs.target
ConditionKernelCommandLine=lyroute.autoinstall=1

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/ly-route-auto-install
StandardInput=tty-force
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target
EOF
ln -s ../ly-route-auto-install.service \
  "$work/config/includes.chroot/etc/systemd/system/multi-user.target.wants/ly-route-auto-install.service"

cat > "$work/config/package-lists/installer.list.chroot" <<'EOF'
live-boot
live-config
linux-image-amd64
isolinux
syslinux-common
systemd-sysv
busybox
zstd
util-linux
coreutils
gdisk
parted
e2fsprogs
ca-certificates
EOF

# The Debian live-build version on CI resolves its ISOLINUX source from
# /root/isolinux inside the build chroot. Keep the complete bundled set there
# so the ISO build does not depend on a host-only path.
bootloader_dir=/usr/share/live/build/bootloaders/isolinux
[ -d "$bootloader_dir" ] || { echo "live-build ISOLINUX assets are missing: $bootloader_dir" >&2; exit 1; }
mkdir -p "$work/config/includes.chroot/root/isolinux"
cp -aL "$bootloader_dir/." "$work/config/includes.chroot/root/isolinux/"
[ -s "$work/config/includes.chroot/root/isolinux/isolinux.bin" ] || { echo "isolinux.bin is missing from live-build assets" >&2; exit 1; }
[ -s "$work/config/includes.chroot/root/isolinux/vesamenu.c32" ] || { echo "vesamenu.c32 is missing from live-build assets" >&2; exit 1; }

cat > "$work/config/hooks/live/0100-installer-permissions.hook.chroot" <<'EOF'
#!/bin/sh
set -e
chmod 0755 /usr/local/sbin/ly-route-auto-install
EOF
chmod 0755 "$work/config/hooks/live/0100-installer-permissions.hook.chroot"

if [ "${LY_ROUTE_ISO_FIXTURE:-0}" = 1 ]; then
  tar -C "$work" -cf "$iso_path" config
else
  for command in lb xorriso sha256sum; do command -v "$command" >/dev/null || { echo "required command missing: $command" >&2; exit 1; }; done
  (
    cd "$work"
    lb config --mode debian --distribution "$suite" --architectures amd64 \
      --binary-images iso-hybrid --archive-areas main --apt-recommends false \
      --security false --linux-packages none \
      --bootappend-live 'boot=live components quiet lyroute.autoinstall=1' \
      --iso-volume 'LY_ROUTE_INSTALL' --memtest none
    lb build
  )
  built=$(find "$work" -maxdepth 1 -type f -name '*.hybrid.iso' -print -quit)
  [ -n "$built" ] || { echo "live-build did not produce a hybrid ISO" >&2; exit 1; }
  mv "$built" "$iso_path"
fi

sha256sum "$iso_path" > "$iso_path.sha256"
cat > "$iso_path.manifest.json" <<EOF
{
  "schema_version": 1,
  "artifact_type": "automatic-installer-iso",
  "product": "$product",
  "suite": "$suite",
  "arch": "amd64",
  "image_size": $image_size,
  "image_sha256": "$image_hash",
  "embedded_image_sha256": "$compressed_hash"
}
EOF
sha256sum "$iso_path.manifest.json" > "$iso_path.manifest.json.sha256"
printf 'Built %s\n' "$iso_path"
