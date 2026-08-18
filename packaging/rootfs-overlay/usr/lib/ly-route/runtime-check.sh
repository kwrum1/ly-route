#!/bin/sh
set -eu
umask 077

: "${LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED:=true}"
: "${LY_ROUTE_PRODUCT:=gateway}"
: "${LY_ROUTE_REQUIRED_COMMANDS:=vpp,vppctl,/usr/lib/ly-route/vpp-apply,smartdns,kea-dhcp4,xray,ipset,/usr/lib/ly-route/ly-route-pppoe-client,python3}"
: "${LY_ROUTE_REQUIRED_UNITS:=vpp.service,smartdns.service,kea-dhcp4-server.service,xray.service,ly-route-pppoe.target}"
: "${LY_ROUTE_RUNTIME_READINESS:=/var/lib/ly-route/runtime-readiness.json}"
: "${LY_ROUTE_VPP_CAPABILITY_PROOF:=/var/lib/ly-route/vpp-native-capabilities.json}"
: "${LY_ROUTE_VPP_DATA_INTERFACES:=}"
: "${LY_ROUTE_MANAGEMENT_INTERFACE:=}"
: "${LY_ROUTE_MANAGEMENT_SHARED:=false}"
: "${LY_ROUTE_VPP_PROOF_TTL_SECONDS:=315360000}"
: "${LY_ROUTE_SYSFS_ROOT:=/sys}"
: "${LY_ROUTE_VPP_NATIVE_BENCHMARK:=}"
: "${LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE:=false}"
: "${LY_ROUTE_DATAPLANE_ACTIVE_STATE:=/var/lib/ly-route/dataplane/active.json}"
: "${LY_ROUTE_ACTIVE_DPDK_READER:=/usr/lib/ly-route/active-dpdk-state.py}"

split_csv() {
  printf '%s' "$1" | tr ',' ' '
}

append_missing() {
  if [ -z "$1" ]; then
    printf '%s' "$2"
  else
    printf '%s %s' "$1" "$2"
  fi
}

json_array() {
  first=1
  printf '['
  for item in "$@"; do
    if [ "$first" -eq 0 ]; then
      printf ','
    fi
    first=0
    printf '"%s"' "$(printf '%s' "$item" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  done
  printf ']'
}

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

interface_name_safe() {
  case "$1" in
    ''|*[!A-Za-z0-9_.:-]*) return 1 ;;
    *) return 0 ;;
  esac
}

probe_native_candidate() {
  probe_interface=$1
  probe_hook=$2
  probe_mode=$3
  probe_vpp_interface="lyroute-proof-$$-$(date -u +%s)-$probe_hook"
  probe_delete="delete interface $probe_hook $probe_vpp_interface"
  probe_created=false
  case "$probe_hook:$probe_mode" in
    rdma:rdma_dv)
      probe_create="create interface rdma host-if $probe_interface name $probe_vpp_interface mode dv"
      ;;
    af_xdp:zero_copy)
      probe_create="create interface af_xdp host-if $probe_interface name $probe_vpp_interface zero-copy"
      ;;
    af_packet:linux_packet_socket)
      # An active AF_PACKET attachment is renamed to lyroute-$iface by the
      # apply transaction. Treat it as the proof instead of recreating it.
      # Recreating an existing host interface and cleaning it up by Linux
      # name would delete the live dataplane attachment.
      probe_vpp_interface="lyroute-$probe_interface"
      if vppctl show interface "$probe_vpp_interface" 2>/dev/null | grep -q "^$probe_vpp_interface"; then
        printf '%s\n' 10
        return 0
      fi
      probe_vpp_interface="host-$probe_interface"
      probe_create="create host-interface name $probe_interface"
      probe_delete="delete host-interface name $probe_interface"
      ;;
    *) return 1 ;;
  esac
  active_probe_kind=$probe_hook
  active_probe_name=$probe_vpp_interface
  [ "$probe_hook" = af_packet ] && active_probe_name=$probe_interface
  if ! vppctl $probe_create >/dev/null 2>&1; then
    active_probe_kind=
    active_probe_name=
    return 1
  fi
  probe_created=true
  if ! vppctl show interface "$probe_vpp_interface" 2>/dev/null | grep -q "^$probe_vpp_interface"; then
    # Only remove an object created by this probe. A failed or duplicate
    # probe must never remove an attachment owned by the apply transaction.
    vppctl $probe_delete >/dev/null 2>&1 || true
    active_probe_kind=
    active_probe_name=
    return 1
  fi
  if [ -n "$LY_ROUTE_VPP_NATIVE_BENCHMARK" ] && [ -x "$LY_ROUTE_VPP_NATIVE_BENCHMARK" ]; then
    probe_score=$("$LY_ROUTE_VPP_NATIVE_BENCHMARK" "$probe_interface" "$probe_hook" "$probe_mode" "$probe_vpp_interface" 2>/dev/null) || probe_score=
  else
    case "$probe_hook:$probe_mode" in
      rdma:rdma_dv) probe_score=120 ;;
      af_xdp:zero_copy) probe_score=100 ;;
      af_packet:linux_packet_socket) probe_score=10 ;;
      *) probe_score=90 ;;
    esac
  fi
  if [ "$probe_created" = true ]; then
    vppctl $probe_delete >/dev/null 2>&1 || true
  fi
  active_probe_kind=
  active_probe_name=
  case "$probe_score" in
    ''|*[!0-9.]*) return 1 ;;
  esac
  printf '%s\n' "$probe_score"
}

probe_dpdk_interface() {
  probe_interface=$1
  dpdk_pci_address=
  dpdk_kernel_driver=
  dpdk_iommu_group=
  dpdk_mode=
  dpdk_iommu_protected=false
  dpdk_vfio_available=false
  dpdk_uio_available=false
  device_path="$LY_ROUTE_SYSFS_ROOT/class/net/$probe_interface/device"
  [ -e "$device_path" ] || return 1
  resolved_device=$(readlink -f "$device_path" 2>/dev/null) || return 1
  dpdk_pci_address=$(basename "$resolved_device")
  case "$dpdk_pci_address" in
    ????\:??\:??.?) ;;
    *) return 1 ;;
  esac
  driver_path="$resolved_device/driver"
  [ -e "$driver_path" ] || return 1
  dpdk_kernel_driver=$(basename "$(readlink -f "$driver_path" 2>/dev/null)")
  [ -n "$dpdk_kernel_driver" ] || return 1
  iommu_path="$resolved_device/iommu_group"
  if [ -e "$iommu_path" ] && [ -d "$LY_ROUTE_SYSFS_ROOT/module/vfio_pci" ]; then
    dpdk_iommu_group=$(basename "$(readlink -f "$iommu_path" 2>/dev/null)")
    case "$dpdk_iommu_group" in ''|*[!0-9]*) return 1 ;; esac
    dpdk_mode=vfio_pci
    dpdk_iommu_protected=true
    dpdk_vfio_available=true
  elif [ -d "$LY_ROUTE_SYSFS_ROOT/module/uio_pci_generic" ]; then
    dpdk_iommu_group=none
    dpdk_mode=uio_pci_generic
    dpdk_uio_available=true
  else
    return 1
  fi
  hugepages_file="$LY_ROUTE_SYSFS_ROOT/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
  [ -r "$hugepages_file" ] || return 1
  hugepages=$(cat "$hugepages_file" 2>/dev/null) || return 1
  case "$hugepages" in ''|*[!0-9]*) return 1 ;; esac
  [ "$hugepages" -gt 0 ] || return 1
  plugin_output=$(vppctl show plugin 2>/dev/null) || return 1
  printf '%s\n' "$plugin_output" | grep -q 'dpdk_plugin.so' || return 1
  return 0
}

probe_active_dpdk_interface() {
  probe_interface=$1
  [ -x "$LY_ROUTE_ACTIVE_DPDK_READER" ] || return 1
  [ -f "$LY_ROUTE_DATAPLANE_ACTIVE_STATE" ] || return 1
  active_identity=$(python3 "$LY_ROUTE_ACTIVE_DPDK_READER" "$LY_ROUTE_DATAPLANE_ACTIVE_STATE" "$probe_interface" 2>/dev/null) || return 1
  old_ifs=$IFS
  IFS='|'
  set -- $active_identity
  IFS=$old_ifs
  [ "$#" -eq 5 ] || return 1
  dpdk_pci_address=$1
  dpdk_kernel_driver=$2
  dpdk_iommu_group=$3
  active_vpp_interface=$4
  dpdk_mode=$5
  device_path="$LY_ROUTE_SYSFS_ROOT/bus/pci/devices/$dpdk_pci_address"
  [ -d "$device_path" ] || return 1
  driver_path="$device_path/driver"
  [ -e "$driver_path" ] || return 1
  current_driver=$(basename "$(readlink -f "$driver_path" 2>/dev/null)")
  dpdk_iommu_protected=false
  dpdk_vfio_available=false
  dpdk_uio_available=false
  case "$dpdk_mode" in
    vfio_pci)
      [ "$current_driver" = vfio-pci ] || return 1
      iommu_path="$device_path/iommu_group"
      [ -e "$iommu_path" ] || return 1
      [ "$(basename "$(readlink -f "$iommu_path" 2>/dev/null)")" = "$dpdk_iommu_group" ] || return 1
      [ -d "$LY_ROUTE_SYSFS_ROOT/module/vfio_pci" ] || return 1
      dpdk_iommu_protected=true
      dpdk_vfio_available=true
      ;;
    uio_pci_generic)
      [ "$current_driver" = uio_pci_generic ] || return 1
      [ "$dpdk_iommu_group" = none ] || return 1
      [ -d "$LY_ROUTE_SYSFS_ROOT/module/uio_pci_generic" ] || return 1
      dpdk_uio_available=true
      ;;
    *) return 1 ;;
  esac
  hugepages_file="$LY_ROUTE_SYSFS_ROOT/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
  [ -r "$hugepages_file" ] || return 1
  hugepages=$(cat "$hugepages_file" 2>/dev/null) || return 1
  case "$hugepages" in ''|*[!0-9]*) return 1 ;; esac
  [ "$hugepages" -gt 0 ] || return 1
  plugin_output=$(vppctl show plugin 2>/dev/null) || return 1
  printf '%s\n' "$plugin_output" | grep -q 'dpdk_plugin.so' || return 1
  hardware_output=$(vppctl show hardware-interfaces "$active_vpp_interface" 2>/dev/null) || return 1
  printf '%s\n' "$hardware_output" | grep -q "$active_vpp_interface" || return 1
  printf '%s\n' "$hardware_output" | grep -q "$dpdk_pci_address" || return 1
  interface_output=$(vppctl show interface "$active_vpp_interface" 2>/dev/null) || return 1
  printf '%s\n' "$interface_output" | grep -q "$active_vpp_interface" || return 1
  return 0
}

active_probe_kind=
active_probe_name=
proof_tmp=
cleanup_runtime_check() {
  if [ -n "$active_probe_kind" ] && [ -n "$active_probe_name" ] && command -v vppctl >/dev/null 2>&1; then
    if [ "$active_probe_kind" = af_packet ]; then
      vppctl delete host-interface name "$active_probe_name" >/dev/null 2>&1 || true
    else
      vppctl delete interface "$active_probe_kind" "$active_probe_name" >/dev/null 2>&1 || true
    fi
  fi
  if [ -n "$proof_tmp" ]; then
    rm -f "$proof_tmp"
  fi
}
trap cleanup_runtime_check EXIT INT TERM

if command -v modprobe >/dev/null 2>&1; then
  modprobe vfio-pci >/dev/null 2>&1 || true
  modprobe uio_pci_generic >/dev/null 2>&1 || true
fi

missing_commands=""
for command_name in $(split_csv "$LY_ROUTE_REQUIRED_COMMANDS"); do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    missing_commands=$(append_missing "$missing_commands" "$command_name")
  fi
done

missing_units=""
for unit_name in $(split_csv "$LY_ROUTE_REQUIRED_UNITS"); do
  if ! systemctl list-unit-files "$unit_name" >/dev/null 2>&1; then
    missing_units=$(append_missing "$missing_units" "$unit_name")
  fi
done

mkdir -p "$(dirname "$LY_ROUTE_RUNTIME_READINESS")"
status=ready
if [ -n "$missing_commands" ] || [ -n "$missing_units" ]; then
  status=missing-runtime
fi

dataplane_state=dataplane_locked
dataplane_failures=
proof_items=
proof_first=1
all_native=true
all_dpdk=true
smart_qos_plugin_available=false
if command -v vppctl >/dev/null 2>&1 && plugin_output=$(vppctl show plugin 2>/dev/null) && cli_output=$(vppctl show cli 2>/dev/null) && smart_qos_output=$(vppctl show ly-route smart-qos 2>/dev/null | tr -d '\r'); then
  if printf '%s\n' "$plugin_output" | grep -q 'ly_route_smart_qos_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'show ly-route smart-qos' &&
     printf '%s\n' "$smart_qos_output" | grep -qx 'algorithm fq-codel' &&
     printf '%s\n' "$smart_qos_output" | grep -qx 'qualification production'; then
    smart_qos_plugin_available=true
  fi
fi
if [ "$LY_ROUTE_PRODUCT" = orchestrator ]; then
  orchestrator_plugin_available=false
  if command -v vppctl >/dev/null 2>&1 &&
     plugin_output=$(vppctl show plugin 2>/dev/null) &&
     cli_output=$(vppctl show cli 2>/dev/null) &&
     orchestrator_output=$(vppctl show ly-route orchestrator 2>/dev/null | tr -d '\r') &&
     printf '%s\n' "$plugin_output" | grep -q 'ly_route_orchestrator_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'show ly-route orchestrator' &&
     printf '%s\n' "$orchestrator_output" | grep -Eq '^state (locked|running)$'; then
    orchestrator_plugin_available=true
  fi
  if [ "$orchestrator_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-orchestrator-plugin)
    status=missing-runtime
  fi
fi
if [ "$LY_ROUTE_PRODUCT" = gateway ]; then
  if [ "$smart_qos_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-smart-qos-plugin)
    status=missing-runtime
  fi
  pppoe_client_plugin_available=false
  if command -v vppctl >/dev/null 2>&1 &&
     plugin_output=$(vppctl show plugin 2>/dev/null) &&
     cli_output=$(vppctl show cli 2>/dev/null) &&
     printf '%s\n' "$plugin_output" | grep -q 'ly_route_pppoe_client_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'set ly-route pppoe-client'; then
    pppoe_client_plugin_available=true
  fi
  if [ "$pppoe_client_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-pppoe-client-plugin)
    status=missing-runtime
  fi
  security_guard_plugin_available=false
  if command -v vppctl >/dev/null 2>&1 &&
     plugin_output=$(vppctl show plugin 2>/dev/null) &&
     cli_output=$(vppctl show cli 2>/dev/null) &&
     guard_output=$(vppctl show ly-route security-guard 2>/dev/null | tr -d '\r') &&
     printf '%s\n' "$plugin_output" | grep -q 'ly_route_security_guard_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'show ly-route security-guard'; then
    security_guard_plugin_available=true
  fi
  if [ "$security_guard_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-security-guard-plugin)
    status=missing-runtime
  fi
  dns_intercept_plugin_available=false
  if command -v vppctl >/dev/null 2>&1 &&
     plugin_output=$(vppctl show plugin 2>/dev/null) &&
     cli_output=$(vppctl show cli 2>/dev/null) &&
     printf '%s\n' "$plugin_output" | grep -q 'ly_route_dns_intercept_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'show ly-route dns-intercept'; then
    dns_intercept_plugin_available=true
  fi
  if [ "$dns_intercept_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-dns-intercept-plugin)
    status=missing-runtime
  fi
  pre_nat_route_plugin_available=false
  if command -v vppctl >/dev/null 2>&1 &&
     plugin_output=$(vppctl show plugin 2>/dev/null) &&
     cli_output=$(vppctl show cli 2>/dev/null) &&
     printf '%s\n' "$plugin_output" | grep -q 'ly_route_pre_nat_route_plugin.so' &&
     printf '%s\n' "$cli_output" | grep -q 'show ly-route pre-nat-route'; then
    pre_nat_route_plugin_available=true
  fi
  if [ "$pre_nat_route_plugin_available" != true ]; then
    missing_commands=$(append_missing "$missing_commands" vpp-pre-nat-route-plugin)
    status=missing-runtime
  fi
fi
case "$LY_ROUTE_VPP_PROOF_TTL_SECONDS" in
  ''|*[!0-9]*)
    dataplane_failures=$(append_missing "$dataplane_failures" proof_ttl_valid)
    LY_ROUTE_VPP_PROOF_TTL_SECONDS=315360000
    ;;
esac
if [ "$LY_ROUTE_VPP_PROOF_TTL_SECONDS" -lt 1 ] || [ "$LY_ROUTE_VPP_PROOF_TTL_SECONDS" -gt 315360000 ]; then
  dataplane_failures=$(append_missing "$dataplane_failures" proof_ttl_valid)
  LY_ROUTE_VPP_PROOF_TTL_SECONDS=315360000
fi
observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
valid_until=$(date -u -d "@$(($(date -u +%s) + LY_ROUTE_VPP_PROOF_TTL_SECONDS))" +%Y-%m-%dT%H:%M:%SZ)
case "$LY_ROUTE_MANAGEMENT_SHARED" in
  true|false) ;;
  *)
    dataplane_failures=$(append_missing "$dataplane_failures" management_shared_valid)
    LY_ROUTE_MANAGEMENT_SHARED=false
    ;;
esac
if [ -z "$LY_ROUTE_MANAGEMENT_INTERFACE" ]; then
  dataplane_failures=$(append_missing "$dataplane_failures" management_identified)
fi
# The active dataplane transaction is authoritative when the appliance
# environment leaves the data-interface list empty. This avoids hard-coded
# NIC names and keeps capability proof valid across reboot.
if [ -z "$LY_ROUTE_VPP_DATA_INTERFACES" ] && [ -f "$LY_ROUTE_DATAPLANE_ACTIVE_STATE" ]; then
  discovered_interfaces=$(python3 - "$LY_ROUTE_DATAPLANE_ACTIVE_STATE" <<'PY'
import json
import os
import re
import stat
import sys

token = re.compile(r"^[A-Za-z0-9_.:-]+$")
path = sys.argv[1]
try:
    metadata = os.lstat(path)
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or stat.S_IMODE(metadata.st_mode) & 0o077:
        raise RuntimeError("unsafe active dataplane state")
    with open(path, "r", encoding="utf-8") as source:
        state = json.load(source)
    active = state.get("path")
    if not isinstance(active, dict) or active.get("tier") not in {"vpp_dpdk", "vpp_native"}:
        raise RuntimeError("no active high-performance dataplane")
    attachments = active.get("attachments")
    if not isinstance(attachments, list):
        raise RuntimeError("active dataplane attachments are missing")
    names = []
    for item in attachments:
        if not isinstance(item, dict):
            continue
        name = str(item.get("linux_interface", "")).strip()
        if name and token.fullmatch(name) and name not in names:
            names.append(name)
    if not names:
        raise RuntimeError("active dataplane has no safe Linux interfaces")
    print(",".join(names))
except (OSError, UnicodeError, json.JSONDecodeError, RuntimeError):
    raise SystemExit(1)
PY
) || discovered_interfaces=
  if [ -n "$discovered_interfaces" ]; then
    LY_ROUTE_VPP_DATA_INTERFACES=$discovered_interfaces
  fi
fi
if [ -z "$LY_ROUTE_VPP_DATA_INTERFACES" ]; then
  dataplane_failures=$(append_missing "$dataplane_failures" data_assignment_present)
fi
for interface_name in $(split_csv "$LY_ROUTE_VPP_DATA_INTERFACES"); do
  if ! interface_name_safe "$interface_name"; then
    dataplane_failures=$(append_missing "$dataplane_failures" "interface_name_safe:$interface_name")
    continue
  fi
  if [ "$interface_name" = "$LY_ROUTE_MANAGEMENT_INTERFACE" ] && [ "$LY_ROUTE_MANAGEMENT_SHARED" != true ]; then
    dataplane_failures=$(append_missing "$dataplane_failures" "management_excluded:$interface_name")
    continue
  fi
  native_candidate=
  vmxnet3_af_packet_candidate=
  dpdk_candidate=
  interface_mac=$(cat "$LY_ROUTE_SYSFS_ROOT/class/net/$interface_name/address" 2>/dev/null || true)
  if command -v vppctl >/dev/null 2>&1 && plugin_output=$(vppctl show plugin 2>/dev/null); then
    interface_driver=none
    interface_device="$LY_ROUTE_SYSFS_ROOT/class/net/$interface_name/device"
    if [ -e "$interface_device/driver" ]; then
      interface_driver=$(basename "$(readlink -f "$interface_device/driver" 2>/dev/null)")
    fi
    if [ "$LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE" = true ] && [ "$interface_driver" = vmxnet3 ] &&
       printf '%s\n' "$plugin_output" | grep -q 'af_packet_plugin.so' &&
       ip link set dev "$interface_name" promisc on >/dev/null 2>&1 &&
       native_score=$(probe_native_candidate "$interface_name" af_packet linux_packet_socket); then
      vmxnet3_af_packet_candidate="{\"tier\":\"vpp_native\",\"hook\":\"af_packet\",\"mode\":\"linux_packet_socket\",\"source\":\"runtime_probe\",\"runtime_verified\":true,\"native\":true,\"high_performance\":false,\"acceptance_only\":true,\"observed_at\":\"$observed_at\",\"valid_until\":\"$valid_until\",\"performance_score\":$native_score,\"kernel_driver\":\"vmxnet3\",\"mac_address\":\"$(json_escape "$interface_mac")\",\"smart_qos_plugin_available\":$smart_qos_plugin_available}"
      native_candidate=$vmxnet3_af_packet_candidate
    fi
    native_specs='rdma rdma_dv rdma_plugin.so af_xdp zero_copy af_xdp_plugin.so'
    # VMXNET3 is validated through AF_PACKET only. Its unsupported native
    # candidates must not be probed or selected; physical NICs retain the
    # normal native-path detection below.
    if [ "$LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE" = true ] && [ "$interface_driver" = vmxnet3 ]; then
      native_specs=
    fi
    for native_spec in 'rdma rdma_dv rdma_plugin.so' 'af_xdp zero_copy af_xdp_plugin.so'; do
      [ -n "$native_specs" ] || continue
      set -- $native_spec
      native_hook=$1
      native_mode=$2
      native_plugin=$3
      if printf '%s\n' "$plugin_output" | grep -q "$native_plugin"; then
        if native_score=$(probe_native_candidate "$interface_name" "$native_hook" "$native_mode"); then
          candidate="{\"tier\":\"vpp_native\",\"hook\":\"$native_hook\",\"mode\":\"$native_mode\",\"source\":\"runtime_probe\",\"runtime_verified\":true,\"native\":true,\"high_performance\":true,\"observed_at\":\"$observed_at\",\"valid_until\":\"$valid_until\",\"performance_score\":$native_score,\"smart_qos_plugin_available\":$smart_qos_plugin_available}"
          if [ -n "$native_candidate" ]; then native_candidate="$native_candidate,$candidate"; else native_candidate=$candidate; fi
        fi
      fi
    done
  fi
  if [ -z "$native_candidate" ]; then all_native=false; fi
  dpdk_source=runtime_probe
  if command -v vppctl >/dev/null 2>&1 && probe_active_dpdk_interface "$interface_name"; then
    dpdk_source=active_runtime_readback
    dpdk_score=80
    [ "$dpdk_mode" = uio_pci_generic ] && dpdk_score=75
    dpdk_candidate="{\"tier\":\"vpp_dpdk\",\"hook\":\"dpdk\",\"mode\":\"$dpdk_mode\",\"source\":\"$dpdk_source\",\"runtime_verified\":true,\"native\":false,\"high_performance\":true,\"observed_at\":\"$observed_at\",\"valid_until\":\"$valid_until\",\"performance_score\":$dpdk_score,\"pci_address\":\"$(json_escape "$dpdk_pci_address")\",\"kernel_driver\":\"$(json_escape "$dpdk_kernel_driver")\",\"iommu_group\":\"$(json_escape "$dpdk_iommu_group")\",\"iommu_protected\":$dpdk_iommu_protected,\"vfio_available\":$dpdk_vfio_available,\"uio_pci_available\":$dpdk_uio_available,\"hugepages_available\":true,\"dpdk_plugin_available\":true,\"smart_qos_plugin_available\":$smart_qos_plugin_available}"
  elif command -v vppctl >/dev/null 2>&1 && probe_dpdk_interface "$interface_name"; then
    dpdk_score=80
    [ "$dpdk_mode" = uio_pci_generic ] && dpdk_score=75
    dpdk_candidate="{\"tier\":\"vpp_dpdk\",\"hook\":\"dpdk\",\"mode\":\"$dpdk_mode\",\"source\":\"$dpdk_source\",\"runtime_verified\":true,\"native\":false,\"high_performance\":true,\"observed_at\":\"$observed_at\",\"valid_until\":\"$valid_until\",\"performance_score\":$dpdk_score,\"pci_address\":\"$(json_escape "$dpdk_pci_address")\",\"kernel_driver\":\"$(json_escape "$dpdk_kernel_driver")\",\"iommu_group\":\"$(json_escape "$dpdk_iommu_group")\",\"iommu_protected\":$dpdk_iommu_protected,\"vfio_available\":$dpdk_vfio_available,\"uio_pci_available\":$dpdk_uio_available,\"hugepages_available\":true,\"dpdk_plugin_available\":true,\"smart_qos_plugin_available\":$smart_qos_plugin_available}"
  else
    all_dpdk=false
  fi
  if [ -z "$native_candidate" ] && [ -z "$dpdk_candidate" ]; then
    dataplane_failures=$(append_missing "$dataplane_failures" "runtime_capability_proof:$interface_name")
    continue
  fi
  candidates=$native_candidate
  if [ -n "$candidates" ] && [ -n "$dpdk_candidate" ]; then candidates="$candidates,$dpdk_candidate"; elif [ -n "$dpdk_candidate" ]; then candidates=$dpdk_candidate; fi
  if [ "$proof_first" -eq 0 ]; then
    proof_items="$proof_items,"
  fi
  proof_first=0
  proof_items="$proof_items{\"linux_interface\":\"$(json_escape "$interface_name")\",\"candidates\":[$candidates]}"
done
if [ -z "$dataplane_failures" ]; then
  if [ "$all_native" = true ]; then
    dataplane_state=native_ready
  elif [ "$all_dpdk" = true ]; then
    dataplane_state=dpdk_ready
  else
    dataplane_failures=$(append_missing "$dataplane_failures" common_dataplane_tier)
  fi
fi
mkdir -p "$(dirname "$LY_ROUTE_VPP_CAPABILITY_PROOF")"
proof_tmp="$LY_ROUTE_VPP_CAPABILITY_PROOF.tmp.$$"
printf '{"management_interface":"%s","proofs":[%s]}\n' "$(json_escape "$LY_ROUTE_MANAGEMENT_INTERFACE")" "$proof_items" > "$proof_tmp"
mv -f "$proof_tmp" "$LY_ROUTE_VPP_CAPABILITY_PROOF"
proof_tmp=

{
  printf '{\n'
  printf '  "status": "%s",\n' "$status"
  printf '  "dataplane_state": "%s",\n' "$dataplane_state"
  printf '  "dataplane_failures": '
  # shellcheck disable=SC2086
  json_array $dataplane_failures
  printf ',\n'
  printf '  "missing_commands": '
  # shellcheck disable=SC2086
  json_array $missing_commands
  printf ',\n'
  printf '  "missing_units": '
  # shellcheck disable=SC2086
  json_array $missing_units
  printf '\n}\n'
} > "$LY_ROUTE_RUNTIME_READINESS"

if [ "$status" != "ready" ] && [ "$LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED" = "true" ]; then
  cat "$LY_ROUTE_RUNTIME_READINESS" >&2
  exit 1
fi
