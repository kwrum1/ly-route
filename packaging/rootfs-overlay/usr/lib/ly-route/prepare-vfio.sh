#!/bin/sh
set -eu

fail() {
  printf '%s\n' "VPP ownership locked: $*" >&2
  exit 1
}

modprobe vfio >/dev/null 2>&1 || true
modprobe vfio-pci >/dev/null 2>&1 || true

network=/etc/ly-route/installed-network.json
rows_file=/etc/ly-route/vfio-devices
management_pci=
pci_rows=
if [ -r "$rows_file" ]; then
  management_pci=$(awk -F'|' '$4 == "management" {print $3; exit}' "$rows_file")
  pci_rows=$(awk -F'|' '$4 == "data" {print $3}' "$rows_file")
elif [ -r "$network" ]; then
  network_rows=$(python3 - "$network" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    document = json.load(source)
management = document.get("management", {})
print("management|" + str(management.get("pci", "")).strip())
for interface in document.get("data_interfaces", []):
    print("data|" + str(interface.get("pci", "")).strip())
PY
  ) || fail 'installer NIC mapping is invalid'
  management_pci=$(printf '%s\n' "$network_rows" | awk -F'|' '$1 == "management" {print $2; exit}')
  pci_rows=$(printf '%s\n' "$network_rows" | awk -F'|' '$1 == "data" && $2 != "" {print $2}')
else
  printf '%s\n' 'VPP ownership preflight skipped: no installer NIC mapping'
  exit 0
fi
[ -n "$management_pci" ] || fail 'management PCI identity is missing'
if [ -z "$pci_rows" ]; then
  printf '%s\n' 'VPP ownership preflight: no data PCI identity is configured'
  exit 0
fi

is_selected() {
  candidate=$1
  case " $pci_rows " in
    *" $candidate "*) return 0 ;;
    *) return 1 ;;
  esac
}

driver_name() {
  device=$1
  basename "$(readlink -f "/sys/bus/pci/devices/$device/driver" 2>/dev/null || true)"
}

is_virtual_vmxnet3() {
  vmx_pci=$1
  vmx_device=/sys/bus/pci/devices/$vmx_pci
  [ -r "$vmx_device/vendor" ] || return 1
  [ -r "$vmx_device/class" ] || return 1
  [ "$(cat "$vmx_device/vendor" 2>/dev/null)" = 0x15ad ] || return 1
  case "$(cat "$vmx_device/class" 2>/dev/null)" in
    0x0200*) return 0 ;;
    *) return 1 ;;
  esac
}

is_pci_bridge() {
  bridge_pci=$1
  class_file=/sys/bus/pci/devices/$bridge_pci/class
  [ -r "$class_file" ] || return 1
  case "$(cat "$class_file" 2>/dev/null)" in
    0x0604*) return 0 ;;
    *) return 1 ;;
  esac
}

check_group_viable() {
  pci=$1
  device=/sys/bus/pci/devices/$pci
  group_link=$device/iommu_group
  if [ -e "$group_link" ]; then
    group=$(basename "$(readlink -f "$group_link")")
    group_dir=/sys/kernel/iommu_groups/$group/devices
    [ -d "$group_dir" ] || fail "$pci has no readable IOMMU group $group"
    virtual_vmxnet3=false
    if is_virtual_vmxnet3 "$pci"; then
      virtual_vmxnet3=true
    fi
    for member in "$group_dir"/*; do
      [ -e "$member" ] || continue
      member_pci=$(basename "$member")
      [ "$member_pci" = "$pci" ] && continue
      member_driver=$(driver_name "$member_pci")
      # VMware places VMXNET3 behind a virtual PCI root port. The root port
      # is not a data-plane function and may be detached only on this path.
      if [ "$virtual_vmxnet3" = true ] && is_pci_bridge "$member_pci"; then
        continue
      fi
      if ! is_selected "$member_pci" && [ -n "$member_driver" ]; then
        fail "$pci IOMMU group $group is shared by $member_pci ($member_driver)"
      fi
    done
    return 0
  fi
  parameter=/sys/module/vfio/parameters/enable_unsafe_noiommu_mode
  [ -r "$parameter" ] && [ "$(cat "$parameter" 2>/dev/null)" = Y ] || \
    fail "$pci has no IOMMU group and VFIO_NOIOMMU is unavailable"
}

bind_group_bridges_for_virtual_vmxnet3() {
  pci=$1
  is_virtual_vmxnet3 "$pci" || return 0
  device=/sys/bus/pci/devices/$pci
  # ESXi can expose several virtual root ports in one IOMMU group. Only the
  # direct parent of this VMXNET3 function participates in its ownership path;
  # attempting to bind every empty sibling port returns EINVAL and used to
  # abort the whole preflight.
  parent_path=$(dirname "$(readlink -f "$device")")
  parent_pci=$(basename "$parent_path")
  is_pci_bridge "$parent_pci" || return 0
  parent_driver=$(driver_name "$parent_pci")
  if [ "$parent_driver" != vfio-pci ]; then
    printf '%s\n' vfio-pci > "/sys/bus/pci/devices/$parent_pci/driver_override" || \
      fail "$parent_pci cannot set vfio-pci driver override"
    if [ -n "$parent_driver" ]; then
      printf '%s\n' "$parent_pci" > "/sys/bus/pci/drivers/$parent_driver/unbind" || \
        fail "$parent_pci cannot unbind $parent_driver"
    fi
    printf '%s\n' "$parent_pci" > /sys/bus/pci/drivers/vfio-pci/bind || \
      fail "$parent_pci cannot bind vfio-pci"
  fi
  [ "$(driver_name "$parent_pci")" = vfio-pci ] || \
    fail "$parent_pci bridge ownership is not vfio-pci"
}

bind_one() {
  pci=$1
  current_driver=$(driver_name "$pci")
  if [ "$current_driver" != vfio-pci ]; then
    printf '%s\n' vfio-pci > "/sys/bus/pci/devices/$pci/driver_override" || \
      fail "$pci cannot set vfio-pci driver override"
    if [ -n "$current_driver" ]; then
      printf '%s\n' "$pci" > "/sys/bus/pci/drivers/$current_driver/unbind" || \
        fail "$pci cannot unbind $current_driver"
    fi
    printf '%s\n' "$pci" > /sys/bus/pci/drivers/vfio-pci/bind || \
      fail "$pci cannot bind vfio-pci"
  fi
  [ "$(driver_name "$pci")" = vfio-pci ] || fail "$pci ownership is not vfio-pci"
}

for pci in $pci_rows; do
  [ "$pci" = "$management_pci" ] && fail "data mapping includes management PCI $pci"
  [ -e "/sys/bus/pci/devices/$pci" ] || fail "configured data PCI $pci is absent"
  check_group_viable "$pci"
done

for pci in $pci_rows; do
  iface=
  for net in /sys/bus/pci/devices/$pci/net/*; do
    [ -e "$net" ] || continue
    iface=$(basename "$net")
    break
  done
  [ -z "$iface" ] || ip link set dev "$iface" down 2>/dev/null || true
  bind_group_bridges_for_virtual_vmxnet3 "$pci"
  bind_one "$pci"
  printf '%s\n' "VPP ownership prepared: ${iface:-pci} $pci"
done
