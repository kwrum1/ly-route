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
startup=/etc/vpp/startup.conf
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
    selected = interface.get("selected")
    if not isinstance(selected, dict):
        continue
    if selected.get("tier") == "vpp_dpdk" and selected.get("hook") == "dpdk":
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
  printf '%s\n' 'VPP ownership preflight: native path selected; Linux retains NIC drivers'
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

check_group_viable() {
  pci=$1
  device=/sys/bus/pci/devices/$pci
  group_link=$device/iommu_group
  if [ -e "$group_link" ]; then
    group=$(basename "$(readlink -f "$group_link")")
    group_dir=/sys/kernel/iommu_groups/$group/devices
    [ -d "$group_dir" ] || fail "$pci has no readable IOMMU group $group"
    for member in "$group_dir"/*; do
      [ -e "$member" ] || continue
      member_pci=$(basename "$member")
      [ "$member_pci" = "$pci" ] && continue
      member_driver=$(driver_name "$member_pci")
      if ! is_selected "$member_pci" && [ -n "$member_driver" ]; then
        fail "$pci IOMMU group $group is shared by $member_pci ($member_driver)"
      fi
    done
    return 0
  fi
  fail "$pci has no isolated IOMMU group"
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
  bind_one "$pci"
  printf '%s\n' "VPP ownership prepared: ${iface:-pci} $pci"
done

[ -f "$startup" ] || fail 'VPP startup configuration is missing'
sed -i '/^# BEGIN LY ROUTE DPDK$/,/^# END LY ROUTE DPDK$/d' "$startup"
if grep -q 'plugin dpdk_plugin.so { disable }' "$startup"; then
  sed -i 's/plugin dpdk_plugin\.so { disable }/plugin dpdk_plugin.so { enable }/' "$startup"
else
  fail 'VPP startup configuration does not declare the DPDK plugin'
fi
{
  printf '\n# BEGIN LY ROUTE DPDK\n'
  printf 'dpdk {\n'
  for pci in $pci_rows; do
    case "$pci" in
      [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f].[0-7]) ;;
      *) fail "invalid selected DPDK PCI address: $pci" ;;
    esac
    printf '  dev %s\n' "$pci"
  done
  printf '}\n# END LY ROUTE DPDK\n'
} >> "$startup"
