#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-auto-install-iso.sh --product gateway --image FILE.img.zst [--out dist/iso] [--suite bookworm]

Builds a BIOS/UEFI hybrid Debian Live ISO that verifies and installs the embedded
Ly Route disk image. The installer enumerates disks and NICs, asks for the target
disk and management NIC, writes static management settings, and records stable
MAC/PCI interface identities before rebooting into the appliance.

Optional kernel parameters support unattended lab installs:
  lyroute.target=/dev/DEVICE
  lyroute.management-mac=AA:BB:CC:DD:EE:FF
    lyroute.ip=192.168.88.254/24 lyroute.gateway=192.168.88.1 lyroute.confirm=yes
EOF
}

product=
image=
out_dir=dist/iso
suite=bookworm
mirror=${LY_ROUTE_MIRROR:-https://deb.debian.org/debian}
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
manifest="$image.manifest.json"
[ -f "$manifest" ] || { echo "image manifest is required: $manifest" >&2; exit 1; }
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$repo_root/packaging/runtime-boundaries/gateway.sh"
node - "$manifest" <<'NODE'
const { readFileSync } = require("node:fs");
const manifest = JSON.parse(readFileSync(process.argv[2], "utf8"));
const envList = (name) => (process.env[name] || "").split(/\s+/).filter(Boolean);
const requiredPlugins = envList("LY_ROUTE_GATEWAY_VPP_PLUGINS");
const requiredPackages = envList("LY_ROUTE_GATEWAY_RUNTIME_PACKAGES");
const requiredFiles = envList("LY_ROUTE_GATEWAY_RUNTIME_FILES_COMMON");
const requiredUnits = envList("LY_ROUTE_GATEWAY_RUNTIME_UNITS");
if (manifest.product !== "gateway" || manifest.arch !== "amd64" ||
    !requiredPlugins.every((item) => manifest.runtime_plugins?.includes(item)) ||
    !requiredPackages.every((item) => manifest.runtime_packages?.includes(item)) ||
    !requiredFiles.every((item) => manifest.runtime_files?.includes(item)) ||
    !requiredUnits.every((item) => manifest.runtime_units?.includes(item))) {
  throw new Error("disk image manifest lacks the complete Gateway runtime boundary attestation");
}
NODE
runtime_plugins_json=$(node - "$manifest" <<'NODE'
const { readFileSync } = require("node:fs");
const manifest = JSON.parse(readFileSync(process.argv[2], "utf8"));
process.stdout.write(JSON.stringify(manifest.runtime_plugins));
NODE
)
runtime_packages_json=$(node - "$manifest" <<'NODE'
const { readFileSync } = require("node:fs");
const manifest = JSON.parse(readFileSync(process.argv[2], "utf8"));
process.stdout.write(JSON.stringify(manifest.runtime_packages));
NODE
)
runtime_files_json=$(node - "$manifest" <<'NODE'
const { readFileSync } = require("node:fs");
const manifest = JSON.parse(readFileSync(process.argv[2], "utf8"));
process.stdout.write(JSON.stringify(manifest.runtime_files));
NODE
)
runtime_units_json=$(node - "$manifest" <<'NODE'
const { readFileSync } = require("node:fs");
const manifest = JSON.parse(readFileSync(process.argv[2], "utf8"));
process.stdout.write(JSON.stringify(manifest.runtime_units));
NODE
)
(if [ -f "$image_raw" ]; then :; else
  manifest="$image.manifest.json"
  [ -f "$manifest" ] || { echo "image manifest is required when the raw image is absent: $manifest" >&2; exit 1; }
fi)
(cd "$(dirname "$image")" && sha256sum -c "$(basename "$image").sha256" >/dev/null)

boot_splash="$repo_root/packaging/iso-assets/ly-route-router-splash.png"
[ -s "$boot_splash" ] || { echo "Ly Route boot splash is missing: $boot_splash" >&2; exit 1; }
case "$out_dir" in /*) artifact_dir=$out_dir ;; *) artifact_dir="$repo_root/$out_dir" ;; esac
mkdir -p "$artifact_dir"
build_tmp_root=${LY_ROUTE_BUILD_TMPDIR:-${TMPDIR:-/var/tmp}}
mkdir -p "$build_tmp_root"
if findmnt -no OPTIONS -T "$build_tmp_root" 2>/dev/null | tr ',' '\n' | grep -qx nodev; then
  echo "ISO build directory is mounted nodev: $build_tmp_root; set LY_ROUTE_BUILD_TMPDIR to a device-capable filesystem" >&2
  exit 1
fi
work=$(mktemp -d "$build_tmp_root/ly-route-iso.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM

payload_name=ly-route-gateway-x86_64.img.zst
if [ -f "$image_raw" ]; then
  image_size=$(stat -c %s "$image_raw")
  image_hash=$(sha256sum "$image_raw" | awk '{print $1}')
else
  image_size=${LY_ROUTE_IMAGE_SIZE_BYTES:-}
  case "$(basename "$image")" in
    *-4g.img.zst) image_size=${image_size:-4294967296} ;;
  esac
  [ -n "$image_size" ] || { echo "LY_ROUTE_IMAGE_SIZE_BYTES is required for an image without a raw sibling" >&2; exit 1; }
  image_hash=$(python3 - "$image.manifest.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    manifest = json.load(source)
print(manifest.get("checksums", {}).get("image_sha256", ""), end="")
PY
  )
  [ -n "$image_hash" ] || { echo "image manifest does not contain checksums.image_sha256" >&2; exit 1; }
fi
compressed_hash=$(sha256sum "$image" | awk '{print $1}')
iso_name=ly-route-gateway-x86_64-installer.iso
iso_path="$artifact_dir/$iso_name"

# Keep one unambiguous ISO baseline in the output directory. Older acceptance
# variants must not be mistaken for the current production installer.
find "$artifact_dir" -maxdepth 1 -type f \( \
  -name 'ly-route-gateway*.iso' \
  -o -name 'ly-route-gateway*.iso.sha256' \
  -o -name 'ly-route-gateway*.iso.manifest.json' \
  -o -name 'ly-route-gateway*.iso.manifest.json.sha256' \
\) -delete

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
set -euo pipefail

payload_dir=/opt/ly-route/install
payload=$payload_dir/ly-route-gateway-x86_64.img.zst
log=/var/log/ly-route-installer.log
exec > >(tee -a "$log") 2>&1

# Informational output must stay out of command substitutions used to collect
# selected interface records. The service mirrors stderr to the same console.
say() { printf '\n[Ly Route] %s\n' "$*" >&2; }
fail() { say "Installation stopped: $*"; exit 1; }
cmdline=" $(cat /proc/cmdline) "
case "$cmdline" in *' lyroute.autoinstall=1 '*) ;; *) fail 'installer mode is not enabled' ;; esac

# The ESXi remote console injects keys into the graphical virtual terminal,
# not into its optional serial-log device. Bind the installer service to tty1
# so both local hardware consoles and ESXi use the same visible input path.
prompt_tty=/dev/tty1
[ -r "$prompt_tty" ] && [ -w "$prompt_tty" ] || prompt_tty=/dev/console
[ -r "$prompt_tty" ] && [ -w "$prompt_tty" ] || fail 'installer console is unavailable'

ask() {
  local prompt=$1 default=$2 answer
  printf '%s' "$prompt" > "$prompt_tty"
  read -r answer < "$prompt_tty" || fail 'cannot read installer input'
  printf '%s\n' "${answer:-$default}"
}

cmdline_value() {
  local key=$1 token
  for token in $(cat /proc/cmdline); do
    case "$token" in "$key"=*) printf '%s\n' "${token#*=}"; return 0 ;; esac
  done
  return 1
}

list_nics() {
  local path name mac state device pci driver
  for path in /sys/class/net/*; do
    [ -e "$path" ] || continue
    name=$(basename "$path")
    [ "$name" = lo ] && continue
    mac=$(cat "$path/address" 2>/dev/null || true)
    state=$(cat "$path/operstate" 2>/dev/null || printf unknown)
    device=$(readlink -f "$path/device" 2>/dev/null || true)
    pci=$(basename "$device")
    case "$pci" in ????\:??\:??.?) ;; *) pci=unknown ;; esac
    driver=none
    [ -e "$device/driver" ] && driver=$(basename "$(readlink -f "$device/driver")")
    printf '%s|%s|%s|%s|%s\n' "$name" "$mac" "$pci" "$state" "$driver"
  done
}

choose_management_nic() {
  local requested=${1:-} rows=() row index selected
  while IFS= read -r row; do rows+=("$row"); done < <(list_nics)
  [ "${#rows[@]}" -gt 0 ] || fail 'no Ethernet interfaces were found'
  if [ -n "$requested" ]; then
    for row in "${rows[@]}"; do
      [ "$(printf '%s' "$row" | cut -d'|' -f1)" = "$requested" ] && { printf '%s\n' "$row"; return; }
      [ "$(printf '%s' "$row" | cut -d'|' -f2)" = "$requested" ] && { printf '%s\n' "$row"; return; }
    done
    fail "requested management interface or MAC was not found: $requested"
  fi
  say 'Available interfaces (the management interface remains Linux-owned)'
  index=1
  for row in "${rows[@]}"; do printf '  %d) %s\n' "$index" "${row//|/  }" >&2; index=$((index + 1)); done
  if [ "${#rows[@]}" -eq 1 ]; then selected=1; else selected=$(ask 'Select management interface [1]:' 1); fi
  [[ "$selected" =~ ^[0-9]+$ ]] && [ "$selected" -ge 1 ] && [ "$selected" -le "${#rows[@]}" ] || fail 'invalid management interface selection'
  printf '%s\n' "${rows[$((selected - 1))]}"
}

candidate_driver() {
  local name=$1 device
  device=$(readlink -f "/sys/class/net/$name/device" 2>/dev/null || true)
  [ -e "$device/driver" ] && basename "$(readlink -f "$device/driver")" || printf none
}

probe_interface() {
  local row=$1 name=$2 driver=$3 native= dpdk= dpdk_mode= iommu= reason= candidates= selected= state=locked
  iommu=$(readlink -f "/sys/class/net/$name/device/iommu_group" 2>/dev/null || true)
  # The live environment does not run the installed VPP instance. This is a
  # hardware preflight only; first boot runtime-check.sh performs the actual
  # VPP attachment proof before any data interface is enabled.
  if [ "$driver" = vmxnet3 ]; then
    native='{"hook":"af_packet","mode":"linux_packet_socket","tier":"vpp_native","verified":"installer_acceptance","native":true,"high_performance":false,"acceptance_only":true,"kernel_driver":"vmxnet3"}'
  elif [ -e "/sys/class/net/$name/queues/rx-0" ] && [ "$driver" != none ] &&
       grep -q '^CONFIG_XDP_SOCKETS=y$' "/boot/config-$(uname -r)" 2>/dev/null; then
    native='{"hook":"af_xdp","mode":"zero_copy","tier":"vpp_native","verified":"hardware_preflight"}'
  fi
  if [ -n "$iommu" ] && { [ -d /sys/module/vfio_pci ] || modprobe vfio-pci >/dev/null 2>&1; }; then
    dpdk_mode=vfio_pci
  elif [ -d /sys/module/uio_pci_generic ] || modprobe uio_pci_generic >/dev/null 2>&1; then
    dpdk_mode=uio_pci_generic
  fi
  if [ -n "$dpdk_mode" ]; then
    dpdk="{\"hook\":\"dpdk\",\"mode\":\"$dpdk_mode\",\"tier\":\"vpp_dpdk\",\"verified\":\"hardware_preflight\"}"
  fi
  candidates=$(printf '%s' "$native${native:+,}$dpdk")
  if [ "$driver" = vmxnet3 ] && [ -n "$native" ]; then selected=$native; state=ready; reason=VMXNET3-AF_PACKET-acceptance
  elif [ -n "$native" ]; then selected=$native; state=ready; reason=VPP-native-preflight
  elif [ -n "$dpdk" ]; then selected=$dpdk; state=ready; reason=DPDK-preflight
  else reason='no verified VPP-native or DPDK prerequisites'; fi
  printf '%s|%s|%s|%s|%s' "$row" "$state" "$reason" "$selected" "$candidates"
}

# The embedded compressed payload is verified when the ISO is built. The raw
# image is verified again after decompression and before rebooting, which avoids
# hashing the same slow optical-media payload twice during installation.

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
  targets=()
  while read -r candidate type; do
    [ "$type" = disk ] || continue
    if eligible "$candidate"; then targets+=("$candidate"); fi
  done < <(lsblk -dnpo NAME,TYPE)
  [ "${#targets[@]}" -gt 0 ] || fail 'no eligible installation disk was found'
  say 'Installation disks (the selected disk will be formatted)'
  index=1
  for candidate in "${targets[@]}"; do
    printf '  %d) %s\n' "$index" "$(lsblk -dnpo NAME,SIZE,MODEL "$candidate")"
    index=$((index + 1))
  done
  if [ "${#targets[@]}" -eq 1 ]; then disk_selection=1; else disk_selection=$(ask 'Select installation disk [1]:' 1); fi
  [[ "$disk_selection" =~ ^[0-9]+$ ]] && [ "$disk_selection" -ge 1 ] && [ "$disk_selection" -le "${#targets[@]}" ] || fail 'invalid installation disk selection'
  target=${targets[$((disk_selection - 1))]}
fi

requested_management=$(cmdline_value lyroute.management-mac || true)
management_row=$(choose_management_nic "$requested_management")
management_name=$(printf '%s' "$management_row" | cut -d'|' -f1)
management_mac=$(printf '%s' "$management_row" | cut -d'|' -f2)
management_pci=$(printf '%s' "$management_row" | cut -d'|' -f3)
management_ip=$(cmdline_value lyroute.ip || true)
if [ -z "$management_ip" ]; then
  management_ip=$(ask 'Management IPv4/CIDR [192.168.88.254/24]:' 192.168.88.254/24)
fi
management_gateway=$(cmdline_value lyroute.gateway || true)
if [ -z "$management_gateway" ]; then
  management_gateway=$(ask 'Management gateway [192.168.88.1]:' 192.168.88.1)
fi
python3 - "$management_ip" "$management_gateway" <<'PY' || fail 'invalid management address or gateway; the gateway must be in the management subnet'
import ipaddress
import sys

interface = ipaddress.IPv4Interface(sys.argv[1])
gateway = ipaddress.IPv4Address(sys.argv[2])
if gateway not in interface.network:
    raise SystemExit(1)
PY

say 'Data-interface capability preflight (management excluded)'
data_rows=() data_state=ready
vmxnet3_afpacket_acceptance=false
while IFS= read -r row; do
  [ -n "$row" ] || continue
  name=$(printf '%s' "$row" | cut -d'|' -f1)
  [ "$name" = "$management_name" ] && continue
  probed=$(probe_interface "$row" "$name" "$(candidate_driver "$name")")
  data_rows+=("$probed")
  [ "$(printf '%s' "$row" | cut -d'|' -f5)" = vmxnet3 ] && vmxnet3_afpacket_acceptance=true
  state=$(printf '%s' "$probed" | cut -d'|' -f6)
  [ "$state" = ready ] || data_state=locked
done < <(list_nics)

say "Management: $management_name  $management_mac  $management_ip  gateway $management_gateway"
for row in "${data_rows[@]}"; do say "Data: ${row//|/  }"; done
if [ "$data_state" = locked ]; then
  say 'Some data interfaces failed preflight. Installation may continue, but the data plane remains locked until runtime verification succeeds.'
fi
  confirmation=$(cmdline_value lyroute.confirm || true)
  if [ -z "$confirmation" ]; then
    confirmation=$(ask 'Enter yes to format the selected disk and install:' '')
  fi
[ "$confirmation" = yes ] || fail 'installation cancelled'
say "Installing to $target. All data on this disk will be erased."
swapoff -a 2>/dev/null || true
wipefs -a "$target" 2>/dev/null || true
zstd -dc "$payload" | dd of="$target" bs=16M conv=fsync status=progress
sync

expected_image=$(cat "$payload_dir/image.sha256")
image_size=$(cat "$payload_dir/image.size")
actual_image=$(head -c "$image_size" "$target" | sha256sum | awk '{print $1}')
[ "$actual_image" = "$expected_image" ] || fail 'installed disk verification failed'

udevadm settle 2>/dev/null || true
partprobe "$target" 2>/dev/null || true
root_partition=$(lsblk -nrpo NAME,LABEL "$target" | awk '$2 == "LYROUTE_ROOT" {print $1; exit}')
[ -b "$root_partition" ] || root_partition=$(blkid -o device -t LABEL=LYROUTE_ROOT 2>/dev/null | head -1 || true)
# A newly written image can expose the partition table before udev has
# populated filesystem labels. The root filesystem is the only ext4 partition
# in the appliance image, so use that as a safe, layout-independent fallback.
[ -b "$root_partition" ] || root_partition=$(lsblk -nrpo NAME,FSTYPE,TYPE "$target" | awk '$2 == "ext4" && $3 == "part" {print $1; exit}')
[ -b "$root_partition" ] || fail 'installed root partition was not found'
mount "$root_partition" /mnt
mkdir -p /mnt/etc/systemd/network
cat > /mnt/etc/systemd/network/05-ly-route-management.network <<EOF
[Match]
MACAddress=$management_mac

[Network]
Address=$management_ip
DHCP=no
LinkLocalAddressing=ipv4
IPv6AcceptRA=yes
Gateway=$management_gateway
EOF
chmod 0644 /mnt/etc/systemd/network/05-ly-route-management.network
install_map=/mnt/etc/ly-route/installed-network.json
mkdir -p "$(dirname "$install_map")"
python3 - "$install_map" "$management_name" "$management_mac" "$management_pci" "$management_ip" "$management_gateway" "$data_state" "${data_rows[@]}" <<'PY'
import json
import os
import sys
from datetime import datetime, timezone

path, mgmt_name, mgmt_mac, mgmt_pci, cidr, gateway, state, *rows = sys.argv[1:]
interfaces = []
for row in rows:
    fields = row.split("|")
    if len(fields) < 9:
        continue
    name, mac, pci, link, driver, probe_state, reason, selected, candidates = fields[:9]
    try:
        candidate_data = json.loads("[" + candidates + "]") if candidates else []
        selected_data = json.loads(selected) if selected else None
    except json.JSONDecodeError:
        candidate_data, selected_data = [], None
    interfaces.append({
        "name": name, "mac": mac, "pci": pci, "link": link, "driver": driver,
        "state": probe_state, "reason": reason, "selected": selected_data,
        "candidates": candidate_data,
    })
document = {
    "schema_version": 1,
    "source": "ly-route-installer",
    "installed_at": datetime.now(timezone.utc).isoformat(),
    "management": {"name": mgmt_name, "mac": mgmt_mac, "pci": mgmt_pci,
                    "cidr": cidr, "gateway": gateway, "vpp_owned": False},
    "data_interfaces": interfaces,
    "dataplane": {"state": "preflight_ready" if state == "ready" else "locked",
                   "management_excluded": True,
                   "selection_order": ["vpp_native", "vpp_dpdk"],
                   "runtime_verification_required": True},
}
with open(path, "w", encoding="utf-8") as target:
    json.dump(document, target, ensure_ascii=False, indent=2)
    target.write("\n")
os.chmod(path, 0o600)
PY
cat > /mnt/etc/ly-route/installer-network.env <<EOF
LY_ROUTE_INSTALLER_NETWORK=/etc/ly-route/installed-network.json
LY_ROUTE_MANAGEMENT_FALLBACK_CIDR=$management_ip
LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY=$management_gateway
LY_ROUTE_MANAGEMENT_INTERFACE=$management_name
LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE=$vmxnet3_afpacket_acceptance
EOF
chmod 0600 /mnt/etc/ly-route/installer-network.env
runtime_env=/mnt/etc/ly-route/runtime.env
runtime_env_tmp="$runtime_env.tmp.$$"
if [ -f "$runtime_env" ]; then
  grep -v '^LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE=' "$runtime_env" > "$runtime_env_tmp" || true
else
  : > "$runtime_env_tmp"
fi
printf 'LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE=%s\n' "$vmxnet3_afpacket_acceptance" >> "$runtime_env_tmp"
chmod 0644 "$runtime_env_tmp"
mv -f "$runtime_env_tmp" "$runtime_env"
umount /mnt
say 'Installation complete. Remove the installation media; the system will reboot.'
sleep 5
systemctl reboot --force
INSTALLER
chmod 0755 "$work/config/includes.chroot/usr/local/sbin/ly-route-auto-install"

cat > "$work/config/includes.chroot/etc/systemd/system/ly-route-auto-install.service" <<'EOF'
[Unit]
Description=Ly Route automatic appliance installer
After=live-config.service local-fs.target dev-tty1.device
Before=getty.target
Conflicts=getty@tty1.service
ConditionKernelCommandLine=lyroute.autoinstall=1

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/ly-route-auto-install
StandardInput=tty-force
StandardOutput=tty
StandardError=tty
TTYPath=/dev/tty1
TTYReset=yes

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
gpgv
debian-archive-keyring
python3
findutils
pciutils
ethtool
kmod
EOF

# The Debian live-build version on CI resolves its ISOLINUX source from
# /root/isolinux inside the build chroot. Populate that directory from the
# real Debian package files rather than live-build's legacy symlinks.
isolinux_bin=$(dpkg -L isolinux | awk '/\/isolinux\.bin$/ {print; exit}')
vesamenu=$(dpkg -L syslinux-common | awk '/\/vesamenu\.c32$/ {print; exit}')
[ -f "$isolinux_bin" ] || { echo "isolinux.bin package file is missing" >&2; exit 1; }
[ -f "$vesamenu" ] || { echo "vesamenu.c32 package file is missing" >&2; exit 1; }
syslinux_bios_dir=$(dirname "$vesamenu")
mkdir -p "$work/config/includes.chroot/root/isolinux"
cp -aL "$syslinux_bios_dir/." "$work/config/includes.chroot/root/isolinux/"
cp -L "$isolinux_bin" "$work/config/includes.chroot/root/isolinux/isolinux.bin"
cp -L "$boot_splash" "$work/config/includes.chroot/root/isolinux/splash.png"
cp -L "$boot_splash" "$work/config/includes.chroot/root/isolinux/splash800x600.png"
mkdir -p "$work/config/bootloaders/isolinux"
cp -aL "$syslinux_bios_dir/." "$work/config/bootloaders/isolinux/"
cp -L "$isolinux_bin" "$work/config/bootloaders/isolinux/isolinux.bin"
cp -L "$boot_splash" "$work/config/bootloaders/isolinux/splash.png"
cp -L "$boot_splash" "$work/config/bootloaders/isolinux/splash800x600.png"
[ -s "$work/config/includes.chroot/root/isolinux/isolinux.bin" ] || { echo "isolinux.bin is missing from live-build assets" >&2; exit 1; }
[ -s "$work/config/includes.chroot/root/isolinux/vesamenu.c32" ] || { echo "vesamenu.c32 is missing from live-build assets" >&2; exit 1; }
[ -s "$work/config/bootloaders/isolinux/isolinux.bin" ] || { echo "isolinux.bin is missing from live-build config assets" >&2; exit 1; }
[ -s "$work/config/bootloaders/isolinux/vesamenu.c32" ] || { echo "vesamenu.c32 is missing from live-build config assets" >&2; exit 1; }

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
      --security false --mirror-bootstrap "$mirror" --mirror-chroot "$mirror" \
      --mirror-binary "$mirror" --debootstrap-options '--include=gpgv' \
      --linux-packages linux-image \
      --bootappend-live 'boot=live components console=tty0 systemd.show_status=true lyroute.autoinstall=1' \
      --iso-volume 'LY_ROUTE_INSTALL' --memtest none
    # Debian live-build defaults both BIOS and UEFI menus to an infinite wait.
    # Keep a short recovery window, then enter the hardware-aware installer.
    install -D -m 0644 /usr/share/live/build/bootloaders/isolinux/isolinux.cfg \
      config/bootloaders/isolinux/isolinux.cfg
    sed -i 's/^timeout .*/timeout 10/' config/bootloaders/isolinux/isolinux.cfg
    sed -i \
      -e 's/Debian GNU\/Linux [^ ]* ([^)]*)/Ly Route Appliance/g' \
      -e 's/Live system/Start Ly Route Installer/g' \
      -e 's/Utilities/Installer Tools/g' \
      config/bootloaders/isolinux/isolinux.cfg
    install -D -m 0644 "$boot_splash" config/bootloaders/isolinux/splash.png
    install -D -m 0644 /usr/share/live/build/bootloaders/grub-pc/config.cfg \
      config/bootloaders/grub-pc/config.cfg
    sed -i '1a set timeout=1' config/bootloaders/grub-pc/config.cfg
    sed -i \
      -e 's/Debian GNU\/Linux [^ ]* ([^)]*)/Ly Route Appliance/g' \
      -e 's/Live system/Start Ly Route Installer/g' \
      -e 's/Utilities/Installer Tools/g' \
      config/bootloaders/grub-pc/config.cfg
    # lb config may recreate the build user's home; stage assets after it.
    mkdir -p /root/isolinux
    cp -aL "$syslinux_bios_dir/." /root/isolinux/
    cp -L "$isolinux_bin" /root/isolinux/isolinux.bin
    cp -L "$boot_splash" /root/isolinux/splash.png
    test -s /root/isolinux/isolinux.bin
    test -s /root/isolinux/vesamenu.c32
    lb build
  )
  built=$(find "$work" -maxdepth 1 -type f -name '*.hybrid.iso' -print -quit)
  [ -n "$built" ] || { echo "live-build did not produce a hybrid ISO" >&2; exit 1; }
  mv "$built" "$iso_path"
fi

# live-build regenerates the boot menus during `lb build`, so apply the product
# labels to the final ISO tree and replay the original El Torito boot metadata.
boot_tree="$work/boot-menu"
mkdir -p "$boot_tree"
for boot_path in /isolinux/menu.cfg /isolinux/live.cfg /isolinux/stdmenu.cfg \
  /isolinux/utilities.cfg /boot/grub/grub.cfg; do
  boot_name=$(printf '%s' "$boot_path" | tr '/' '_')
  xorriso -osirrox on -indev "$iso_path" \
    -extract "$boot_path" "$boot_tree/$boot_name" >/dev/null 2>&1
done
sed -i \
  -e 's/Boot menu/Ly Route Appliance/g' \
  -e 's/Utilities/Installer Tools/g' \
  -e 's/Back\.\./Back/g' \
  "$boot_tree/_isolinux_menu.cfg"
sed -i \
  -e 's/Live system (amd64)/Start Ly Route Installer (amd64)/g' \
  -e 's/Live system (amd64 fail-safe mode)/Ly Route Safe Boot (amd64)/g' \
  "$boot_tree/_isolinux_live.cfg" "$boot_tree/_boot_grub_grub.cfg"
sed -i \
  -e 's/Press ENTER to boot or TAB to edit a menu entry/Press ENTER to start or TAB to edit/g' \
  "$boot_tree/_isolinux_stdmenu.cfg"
sed -i \
  -e 's/Hardware Detection Tool (HDT)/Ly Route Hardware Detection (HDT)/g' \
  "$boot_tree/_isolinux_utilities.cfg"
sed -i \
  -e 's/Utilities\.\.\./Installer Tools.../g' \
  "$boot_tree/_boot_grub_grub.cfg"
rebranded_iso="$work/ly-route-gateway-x86_64-installer-rebranded.iso"
xorriso -indev "$iso_path" -outdev "$rebranded_iso" \
  -map "$boot_tree/_isolinux_menu.cfg" /isolinux/menu.cfg \
  -map "$boot_tree/_isolinux_live.cfg" /isolinux/live.cfg \
  -map "$boot_tree/_isolinux_stdmenu.cfg" /isolinux/stdmenu.cfg \
  -map "$boot_tree/_isolinux_utilities.cfg" /isolinux/utilities.cfg \
  -map "$boot_tree/_boot_grub_grub.cfg" /boot/grub/grub.cfg \
  -map "$boot_splash" /isolinux/splash.png \
  -map "$boot_splash" /isolinux/splash800x600.png \
  -boot_image any replay -commit >/dev/null 2>&1
mv "$rebranded_iso" "$iso_path"

sha256sum "$iso_path" > "$iso_path.sha256"
cat > "$iso_path.manifest.json" <<EOF
{
  "schema_version": 1,
  "artifact_type": "hardware-aware-installer-iso",
  "product": "$product",
  "suite": "$suite",
  "arch": "amd64",
  "image_size": $image_size,
  "image_sha256": "$image_hash",
  "embedded_image_sha256": "$compressed_hash",
  "runtime_packages": $runtime_packages_json,
  "runtime_files": $runtime_files_json,
  "runtime_units": $runtime_units_json,
  "runtime_plugins": $runtime_plugins_json
}
EOF
sha256sum "$iso_path.manifest.json" > "$iso_path.manifest.json.sha256"
printf 'Built %s\n' "$iso_path"
