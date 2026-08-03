#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
cleanup() {
  if [ "${LY_ROUTE_KEEP_TEST_TMP:-0}" = 1 ]; then
    printf 'kept test fixture: %s\n' "$tmp" >&2
  else
    rm -rf "$tmp"
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/bin"
cat > "$tmp/bin/systemctl" <<'EOF'
#!/usr/bin/env sh
exit 0
EOF
cat > "$tmp/bin/vppctl" <<'EOF'
#!/usr/bin/env sh
printf '%s\n' "$*" >> "$VPPCTL_LOG"
case "$*" in
  'show plugin') printf '%s\n' "$VPPCTL_PLUGINS" ;;
  'show cli')
    printf '%s\n' 'show dpdk interface'
    case "$VPPCTL_PLUGINS" in *ly_route_smart_qos_plugin.so*) printf '%s\n' 'show ly-route smart-qos' ;; esac
    ;;
  'show ly-route smart-qos')
    printf '%s\n' 'state locked' 'algorithm fq-codel' "qualification ${VPPCTL_SMART_QOS_QUALIFICATION:-development-single-worker}"
    ;;
  show\ hardware-interfaces\ *) printf '%s PCI %s\n' "$3" "${VPPCTL_ACTIVE_PCI:-}" ;;
  'show threads') printf '%s\n' "${VPPCTL_THREADS:-vpp_main}" ;;
  'show dpdk interface hqos placement') printf '%s\n' "${VPPCTL_HQOS_PLACEMENT:-}" ;;
  create\ interface\ af_xdp*)
    case "$*" in
      *' zero-copy') [ "${VPPCTL_AFXDP_SUCCESS:-0}" = 1 ] ;;
      *) exit 1 ;;
    esac
    ;;
  create\ interface\ rdma*) [ "${VPPCTL_RDMA_SUCCESS:-0}" = 1 ] ;;
  show\ interface\ *) printf '%s up\n' "$3" ;;
  *) exit 0 ;;
esac
EOF
cat > "$tmp/bin/native-benchmark" <<'EOF'
#!/usr/bin/env sh
case "$2" in
  rdma) printf '%s\n' "${VPPCTL_RDMA_SCORE:-90}" ;;
  af_xdp) printf '%s\n' "${VPPCTL_AFXDP_SCORE:-80}" ;;
  *) exit 1 ;;
esac
EOF
chmod 0755 "$tmp/bin/systemctl" "$tmp/bin/vppctl" "$tmp/bin/native-benchmark"

run_check() {
  scenario=$1
  management=$2
  data_interfaces=$3
  plugins=$4
  afxdp_success=$5
  management_shared=${6:-false}
  scenario_dir="$tmp/$scenario"
  mkdir -p "$scenario_dir" "$scenario_dir/sys"
  PATH="$tmp/bin:$PATH" \
    VPPCTL_LOG="$scenario_dir/vppctl.log" \
    VPPCTL_PLUGINS="$plugins" \
    VPPCTL_AFXDP_SUCCESS="$afxdp_success" \
    VPPCTL_RDMA_SUCCESS="${VPPCTL_RDMA_SUCCESS:-0}" \
    LY_ROUTE_VPP_NATIVE_BENCHMARK="$tmp/bin/native-benchmark" \
    LY_ROUTE_RUNTIME_READINESS="$scenario_dir/readiness.json" \
    LY_ROUTE_VPP_CAPABILITY_PROOF="$scenario_dir/proof.json" \
    LY_ROUTE_DATAPLANE_ACTIVE_STATE="$scenario_dir/active.json" \
    LY_ROUTE_ACTIVE_DPDK_READER="$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/active-dpdk-state.py" \
    LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=false \
    LY_ROUTE_REQUIRED_COMMANDS=sh \
    LY_ROUTE_REQUIRED_UNITS= \
    LY_ROUTE_MANAGEMENT_INTERFACE="$management" \
    LY_ROUTE_MANAGEMENT_SHARED="$management_shared" \
    LY_ROUTE_VPP_DATA_INTERFACES="$data_interfaces" \
    LY_ROUTE_SYSFS_ROOT="$scenario_dir/sys" \
    "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh"
}

setup_dpdk_fixture() {
  scenario=$1
  interface_name=$2
  scenario_dir="$tmp/$scenario"
  device="$scenario_dir/sys/devices/pci0000:00/0000:03:00.0"
  driver="$scenario_dir/sys/bus/pci/drivers/ixgbe"
  iommu="$scenario_dir/sys/kernel/iommu_groups/17"
  mkdir -p "$scenario_dir/sys/class/net/$interface_name" "$device" "$driver" "$iommu" \
    "$scenario_dir/sys/module/vfio_pci" "$scenario_dir/sys/kernel/mm/hugepages/hugepages-2048kB"
  ln -s "$device" "$scenario_dir/sys/class/net/$interface_name/device"
  ln -s "$driver" "$device/driver"
  ln -s "$iommu" "$device/iommu_group"
  printf '%s\n' 64 > "$scenario_dir/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
}

setup_active_dpdk_fixture() {
  scenario=$1
  interface_name=$2
  scenario_dir="$tmp/$scenario"
  device="$scenario_dir/sys/devices/pci0000:00/0000:03:00.0"
  driver="$scenario_dir/sys/bus/pci/drivers/vfio-pci"
  iommu="$scenario_dir/sys/kernel/iommu_groups/17"
  mkdir -p "$device" "$driver" "$iommu" "$scenario_dir/sys/bus/pci/devices" \
    "$scenario_dir/sys/module/vfio_pci" "$scenario_dir/sys/kernel/mm/hugepages/hugepages-2048kB"
  ln -s "$device" "$scenario_dir/sys/bus/pci/devices/0000:03:00.0"
  ln -s "$driver" "$device/driver"
  ln -s "$iommu" "$device/iommu_group"
  printf '%s\n' 64 > "$scenario_dir/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
  cat > "$scenario_dir/active.json" <<EOF
{"path":{"tier":"vpp_dpdk","smart_qos":true,"attachments":[{"linux_interface":"$interface_name","vpp_interface":"TenGigabitEthernet3/0/0","tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","pci_address":"0000:03:00.0","kernel_driver":"ixgbe","iommu_group":"17"}],"prerequisites":[]},"snapshot":{"transaction_id":"active-test","startup_config":"","devices":[]},"applied_at":"2026-08-01T00:00:00Z"}
EOF
  chmod 0600 "$scenario_dir/active.json"
}

run_check native eth0 eth1 af_xdp_plugin.so 1
grep -q '"dataplane_state": "native_ready"' "$tmp/native/readiness.json"
grep -q '"hook":"af_xdp"' "$tmp/native/proof.json"
grep -Eq 'create interface af_xdp host-if eth1 name lyroute-proof-[0-9]+-[0-9]+-af_xdp zero-copy' "$tmp/native/vppctl.log"
if grep -q 'host-if eth0' "$tmp/native/vppctl.log"; then
  echo "management interface reached VPP probe" >&2
  exit 1
fi

VPPCTL_RDMA_SUCCESS=1 VPPCTL_RDMA_SCORE=50 VPPCTL_AFXDP_SCORE=90 \
  run_check native-benchmark eth0 eth1 'rdma_plugin.so af_xdp_plugin.so' 1
grep -q '"hook":"rdma"' "$tmp/native-benchmark/proof.json"
grep -q '"hook":"af_xdp"' "$tmp/native-benchmark/proof.json"
grep -q '"performance_score":90' "$tmp/native-benchmark/proof.json"

run_check unsupported eth0 eth1 unrelated_plugin.so 0
grep -q '"dataplane_state": "dataplane_locked"' "$tmp/unsupported/readiness.json"
grep -q 'runtime_capability_proof:eth1' "$tmp/unsupported/readiness.json"
grep -q '"proofs":\[\]' "$tmp/unsupported/proof.json"

setup_dpdk_fixture dpdk eth1
run_check dpdk eth0 eth1 dpdk_plugin.so 0
grep -q '"dataplane_state": "dpdk_ready"' "$tmp/dpdk/readiness.json"
grep -q '"tier":"vpp_dpdk"' "$tmp/dpdk/proof.json"
grep -q '"pci_address":"0000:03:00.0"' "$tmp/dpdk/proof.json"
grep -q '"kernel_driver":"ixgbe"' "$tmp/dpdk/proof.json"
grep -q '"iommu_group":"17"' "$tmp/dpdk/proof.json"
grep -q '"dpdk_plugin_available":true' "$tmp/dpdk/proof.json"

setup_active_dpdk_fixture dpdk-active eth1
VPPCTL_ACTIVE_PCI=0000:03:00.0 VPPCTL_SMART_QOS_QUALIFICATION=production \
  run_check dpdk-active eth0 eth1 'dpdk_plugin.so ly_route_smart_qos_plugin.so' 0
grep -q '"dataplane_state": "dpdk_ready"' "$tmp/dpdk-active/readiness.json"
grep -q '"source":"active_runtime_readback"' "$tmp/dpdk-active/proof.json"
grep -q '"linux_interface":"eth1"' "$tmp/dpdk-active/proof.json"
grep -q '"kernel_driver":"ixgbe"' "$tmp/dpdk-active/proof.json"
grep -q '"smart_qos_plugin_available":true' "$tmp/dpdk-active/proof.json"
if [ -e "$tmp/dpdk-active/sys/class/net/eth1" ]; then
  echo "active DPDK fixture unexpectedly retained a Linux netdev" >&2
  exit 1
fi

setup_active_dpdk_fixture dpdk-active-no-smart-qos eth1
VPPCTL_ACTIVE_PCI=0000:03:00.0 run_check dpdk-active-no-smart-qos eth0 eth1 dpdk_plugin.so 0
grep -q '"dataplane_state": "dpdk_ready"' "$tmp/dpdk-active-no-smart-qos/readiness.json"
grep -q '"smart_qos_plugin_available":false' "$tmp/dpdk-active-no-smart-qos/proof.json"

setup_active_dpdk_fixture dpdk-active-development-smart-qos eth1
VPPCTL_ACTIVE_PCI=0000:03:00.0 VPPCTL_SMART_QOS_QUALIFICATION=development-single-worker \
  run_check dpdk-active-development-smart-qos eth0 eth1 'dpdk_plugin.so ly_route_smart_qos_plugin.so' 0
grep -q '"dataplane_state": "dpdk_ready"' "$tmp/dpdk-active-development-smart-qos/readiness.json"
grep -q '"smart_qos_plugin_available":false' "$tmp/dpdk-active-development-smart-qos/proof.json"

mkdir -p "$tmp/dpdk-no-iommu/sys/class/net/eth1" "$tmp/dpdk-no-iommu/sys/devices/pci0000:00/0000:03:00.0" \
  "$tmp/dpdk-no-iommu/sys/bus/pci/drivers/ixgbe" "$tmp/dpdk-no-iommu/sys/module/vfio_pci" \
  "$tmp/dpdk-no-iommu/sys/kernel/mm/hugepages/hugepages-2048kB"
ln -s "$tmp/dpdk-no-iommu/sys/devices/pci0000:00/0000:03:00.0" "$tmp/dpdk-no-iommu/sys/class/net/eth1/device"
ln -s "$tmp/dpdk-no-iommu/sys/bus/pci/drivers/ixgbe" "$tmp/dpdk-no-iommu/sys/devices/pci0000:00/0000:03:00.0/driver"
printf '%s\n' 64 > "$tmp/dpdk-no-iommu/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
run_check dpdk-no-iommu eth0 eth1 dpdk_plugin.so 0
grep -q '"dataplane_state": "dataplane_locked"' "$tmp/dpdk-no-iommu/readiness.json"
grep -q 'runtime_capability_proof:eth1' "$tmp/dpdk-no-iommu/readiness.json"

run_check management eth0 eth0 af_xdp_plugin.so 1
grep -q '"dataplane_state": "dataplane_locked"' "$tmp/management/readiness.json"
grep -q 'management_excluded:eth0' "$tmp/management/readiness.json"
if grep -Eq 'host-if eth0|hardware-interfaces .*eth0' "$tmp/management/vppctl.log"; then
  echo "management assignment executed interface-level VPP probes" >&2
  exit 1
fi

run_check shared-management eth0 eth0 af_xdp_plugin.so 1 true
grep -q '"dataplane_state": "native_ready"' "$tmp/shared-management/readiness.json"
grep -q '"linux_interface":"eth0"' "$tmp/shared-management/proof.json"
grep -Eq 'create interface af_xdp host-if eth0 name lyroute-proof-[0-9]+-[0-9]+-af_xdp zero-copy' "$tmp/shared-management/vppctl.log"

run_check invalid-shared-mode eth0 eth0 af_xdp_plugin.so 1 shared
grep -q '"dataplane_state": "dataplane_locked"' "$tmp/invalid-shared-mode/readiness.json"
grep -q 'management_shared_valid' "$tmp/invalid-shared-mode/readiness.json"

printf 'VPP native selection scenarios passed\n'
