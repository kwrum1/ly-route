#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
pycache_root="${PYTHONPYCACHEPREFIX:-/tmp/ly-route-pycache}"

run() {
  printf '\n==> %s\n' "$*"
  "$@"
}

export PYTHONPYCACHEPREFIX="$pycache_root"
mkdir -p "$PYTHONPYCACHEPREFIX"
cd "$repo_root"

run sh -n "$repo_root/scripts/build-rootfs.sh"
run bash -n "$repo_root/scripts/build-vpp-bookworm-debs.sh"
run sh -n "$repo_root/scripts/build-controller-shell.sh"
run sh -n "$repo_root/scripts/build-upgrade-package.sh"
run sh -n "$repo_root/scripts/rootfs-runtime-smoke.sh"
run sh -n "$repo_root/scripts/validate-rootfs-scaffold.sh"
run sh -n "$repo_root/scripts/test-firstboot-env-migration.sh"
run sh -n "$repo_root/scripts/test-vpp-native-selection.sh"
run sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh"
run sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/migrate-control-env.sh"
run sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh"
run sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh"
run "$repo_root/scripts/test-firstboot-env-migration.sh"
run "$repo_root/scripts/test-vpp-native-selection.sh"
run node "$repo_root/scripts/test-controller-shell-gateway-characterization.mjs"
run "$repo_root/scripts/test-controller-shell-profile-isolation.sh"

tuning_tmp=$(mktemp -d)
run sh -c "LY_ROUTE_ROOT='$tuning_tmp' LY_ROUTE_PLATFORM_ARCH=aarch64 sh '$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh'"
run sh -c "grep -q 'LY_ROUTE_PLATFORM_PROFILE=arm' '$tuning_tmp/etc/ly-route/platform-tuning.env' && grep -q 'vm.swappiness = 10' '$tuning_tmp/etc/sysctl.d/90-ly-route-vpp.conf'"
rm -rf "$tuning_tmp"
tuning_tmp=$(mktemp -d)
run sh -c "LY_ROUTE_ROOT='$tuning_tmp' LY_ROUTE_PLATFORM_ARCH=x86_64 sh '$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh'"
run sh -c "grep -q 'LY_ROUTE_PLATFORM_PROFILE=x86' '$tuning_tmp/etc/ly-route/platform-tuning.env' && grep -q 'vm.swappiness = 1' '$tuning_tmp/etc/sysctl.d/90-ly-route-vpp.conf'"
rm -rf "$tuning_tmp"

runtime_check_tmp=$(mktemp -d)
run sh -c "LY_ROUTE_RUNTIME_READINESS='$runtime_check_tmp/readiness.json' LY_ROUTE_VPP_CAPABILITY_PROOF='$runtime_check_tmp/proof.json' LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=false LY_ROUTE_REQUIRED_COMMANDS=sh LY_ROUTE_REQUIRED_UNITS= '$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh'"
run test -s "$runtime_check_tmp/readiness.json"
run sh -c "LY_ROUTE_RUNTIME_READINESS='$runtime_check_tmp/strict.json' LY_ROUTE_VPP_CAPABILITY_PROOF='$runtime_check_tmp/strict-proof.json' LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=true LY_ROUTE_REQUIRED_COMMANDS=definitely-missing-ly-route-command LY_ROUTE_REQUIRED_UNITS= '$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh' >/tmp/ly-route-runtime-check.out 2>/tmp/ly-route-runtime-check.err; test \$? -ne 0 && grep -q definitely-missing-ly-route-command /tmp/ly-route-runtime-check.err"
rm -f /tmp/ly-route-runtime-check.out /tmp/ly-route-runtime-check.err
rm -rf "$runtime_check_tmp"

runtime_debs_tmp=$(mktemp -d)
rootfs_smoke_tmp=$(mktemp -d)
run sh -c "LY_ROUTE_RUNTIME_DEBS_DIR='$runtime_debs_tmp' '$repo_root/scripts/build-runtime-debs.sh' vpp-apply"
run sh -c "test -f '$runtime_debs_tmp'/ly-route-vpp-apply_*_*.deb"
run sh -c "LY_ROUTE_RUNTIME_DEBS_DIR='$runtime_debs_tmp' LY_ROUTE_FDIO_CACHE_DIR='$repo_root/runtime-debs' '$repo_root/scripts/build-runtime-debs.sh' vpp-fdio"
for vpp_package in libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk; do
  run sh -c "test -f '$runtime_debs_tmp'/${vpp_package}_25.10-release_amd64.deb"
done
run sh -c "LY_ROUTE_RUNTIME_DEBS_DIR='$runtime_debs_tmp' LY_ROUTE_FDIO_CACHE_DIR='$repo_root/runtime-downloads' '$repo_root/scripts/build-runtime-debs.sh' vpp-smart-qos"
run sh -c "test -f '$runtime_debs_tmp'/ly-route-vpp-smart-qos_*_amd64.deb && dpkg-deb -c '$runtime_debs_tmp'/ly-route-vpp-smart-qos_*_amd64.deb | grep -q '/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_smart_qos_plugin.so'"
run sh -c "LY_ROUTE_RUNTIME_DEBS_DIR='$runtime_debs_tmp' LY_ROUTE_FDIO_CACHE_DIR='$repo_root/runtime-downloads' '$repo_root/scripts/build-runtime-debs.sh' vpp-security-guard"
run sh -c "test -f '$runtime_debs_tmp'/ly-route-vpp-security-guard_*_amd64.deb && dpkg-deb -c '$runtime_debs_tmp'/ly-route-vpp-security-guard_*_amd64.deb | grep -q '/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_security_guard_plugin.so'"
run sh -c "LY_ROUTE_RUNTIME_DEBS_DIR='$runtime_debs_tmp' LY_ROUTE_FDIO_CACHE_DIR='$repo_root/runtime-downloads' '$repo_root/scripts/build-runtime-debs.sh' vpp-orchestrator"
run sh -c "test -f '$runtime_debs_tmp'/ly-route-vpp-orchestrator_*_amd64.deb && dpkg-deb -c '$runtime_debs_tmp'/ly-route-vpp-orchestrator_*_amd64.deb | grep -q '/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_orchestrator_plugin.so'"
vpp_apply_tmp=$(mktemp -d)
run sh -c "dpkg-deb -x '$runtime_debs_tmp'/ly-route-vpp-apply_*_*.deb '$vpp_apply_tmp'"
cat > "$vpp_apply_tmp/operations.json" <<'EOF'
{"operations":[{"Name":"vpp.abf.policy","Resource":"proxy-ci","RequestID":"ci"}]}
EOF
cat > "$vpp_apply_tmp/vpp-command-map.json" <<'EOF'
{"operations":{"vpp.abf.policy:proxy-ci":["show version"]}}
EOF
run sh -c "LY_ROUTE_VPP_APPLY_DRY_RUN=true LY_ROUTE_VPP_COMMAND_MAP='$vpp_apply_tmp/vpp-command-map.json' LY_ROUTE_VPP_RECEIPT='$vpp_apply_tmp/receipt.json' '$vpp_apply_tmp/usr/lib/ly-route/vpp-apply' '$vpp_apply_tmp/operations.json'"
run sh -c "grep -q 'dry-run' '$vpp_apply_tmp/receipt.json' && grep -q 'vpp.abf.policy' '$vpp_apply_tmp/receipt.json'"
cat > "$vpp_apply_tmp/native-valid.json" <<'EOF'
{"operations":[{"Name":"vpp.dataplane.attach","Resource":"eth1","RequestID":"ci","vppctl_commands":["?create interface af_xdp host-if eth1 name lyroute-eth1 zero-copy","set interface state lyroute-eth1 up","show hardware-interfaces lyroute-eth1","show interface lyroute-eth1"]}]}
EOF
run sh -c "LY_ROUTE_VPP_APPLY_DRY_RUN=true LY_ROUTE_VPP_RECEIPT='$vpp_apply_tmp/native-valid-receipt.json' '$vpp_apply_tmp/usr/lib/ly-route/vpp-apply' '$vpp_apply_tmp/native-valid.json'"
run sh -c "grep -q 'dry-run' '$vpp_apply_tmp/native-valid-receipt.json' && grep -q 'create interface af_xdp' '$vpp_apply_tmp/native-valid-receipt.json' && ! grep -q 'native-driver-attach' '$vpp_apply_tmp/native-valid-receipt.json'"
run sh -c "LY_ROUTE_VPP_APPLY_DRY_RUN=true LY_ROUTE_VPP_COMMAND_MAP='$vpp_apply_tmp/missing-map.json' LY_ROUTE_VPP_RECEIPT='$vpp_apply_tmp/failed-receipt.json' '$vpp_apply_tmp/usr/lib/ly-route/vpp-apply' '$vpp_apply_tmp/operations.json' >/tmp/ly-route-vpp-apply.out 2>/tmp/ly-route-vpp-apply.err; test \$? -ne 0 && grep -q 'missing VPP command mapping' /tmp/ly-route-vpp-apply.err && grep -q '\"status\": \"failed\"' '$vpp_apply_tmp/failed-receipt.json' && grep -q 'missing VPP command mapping' '$vpp_apply_tmp/failed-receipt.json'"
printf '{' > "$vpp_apply_tmp/invalid-operations.json"
run sh -c "LY_ROUTE_VPP_APPLY_DRY_RUN=true LY_ROUTE_VPP_RECEIPT='$vpp_apply_tmp/invalid-receipt.json' '$vpp_apply_tmp/usr/lib/ly-route/vpp-apply' '$vpp_apply_tmp/invalid-operations.json' >/tmp/ly-route-vpp-invalid.out 2>/tmp/ly-route-vpp-invalid.err; test \$? -ne 0 && grep -q '\"status\": \"failed\"' '$vpp_apply_tmp/invalid-receipt.json' && grep -q 'failed to load VPP operations JSON' '$vpp_apply_tmp/invalid-receipt.json'"
rm -rf "$vpp_apply_tmp"
rm -f /tmp/ly-route-vpp-apply.out /tmp/ly-route-vpp-apply.err /tmp/ly-route-vpp-invalid.out /tmp/ly-route-vpp-invalid.err
run sh -c "LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1 LY_ROUTE_EXTRA_DEBS_DIR='$runtime_debs_tmp' '$repo_root/scripts/build-rootfs.sh' --product gateway --arch amd64 --out '$rootfs_smoke_tmp'"
if command -v zstd >/dev/null 2>&1; then
  rootfs_smoke_artifact="$rootfs_smoke_tmp/ly-route-rootfs-gateway-bookworm-amd64.tar.zst"
  rootfs_smoke_compressor=unzstd
else
  rootfs_smoke_artifact="$rootfs_smoke_tmp/ly-route-rootfs-gateway-bookworm-amd64.tar.gz"
  rootfs_smoke_compressor='gzip -d'
fi
run test -f "$rootfs_smoke_artifact"
run sh -c "LY_ROUTE_ROOTFS_REQUIRED_PACKAGES='' LY_ROUTE_ROOTFS_REQUIRED_FILES='/usr/lib/ly-route/vpp-apply /usr/lib/ly-route/vpp-apply-default /usr/lib/ly-route/policy-routing-apply-default /var/lib/ly-route/vpp/operations.json /etc/ly-route/default-config.json /etc/ly-route/runtime.env /etc/ly-route/vpp-command-map.json /etc/kea/kea-dhcp4.conf /etc/systemd/network/10-ethernet-dhcp.network /etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf' '$repo_root/scripts/rootfs-runtime-smoke.sh' '$rootfs_smoke_artifact'"
rootfs_smoke_name=$(basename "$rootfs_smoke_artifact")
run sh -c "cd '$rootfs_smoke_tmp' && sha256sum -c '$rootfs_smoke_name.sha256'"
rootfs_extract_tmp=$(mktemp -d)
run sh -c "tar --use-compress-program='$rootfs_smoke_compressor' -xf '$rootfs_smoke_artifact' -C '$rootfs_extract_tmp' ./usr/lib/ly-route/vpp-apply ./usr/lib/ly-route/vpp-apply-default ./usr/lib/ly-route/policy-routing-apply-default ./var/lib/ly-route/vpp/operations.json ./etc/ly-route/default-config.json ./etc/ly-route/runtime.env ./etc/ly-route/vpp-command-map.json ./etc/kea/kea-dhcp4.conf ./etc/systemd/network/10-ethernet-dhcp.network ./etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf"
run test -x "$rootfs_extract_tmp/usr/lib/ly-route/vpp-apply"
run sh -c "grep -q 'LY_ROUTE_VPP_OPERATIONS:=/var/lib/ly-route/vpp/operations.json' '$rootfs_extract_tmp/usr/lib/ly-route/vpp-apply-default'"
run sh -c "grep -q 'LY_ROUTE_POLICY_ROUTING_RECEIPT' '$rootfs_extract_tmp/usr/lib/ly-route/policy-routing-apply-default'"
run sh -c "grep -q 'LY_ROUTE_VPP_COMMAND_MAP=/etc/ly-route/vpp-command-map.json' '$rootfs_extract_tmp/etc/ly-route/runtime.env'"
run sh -c "grep -q '\"operations\"' '$rootfs_extract_tmp/etc/ly-route/vpp-command-map.json'"
run sh -c "grep -q '\"active_path\": \"dataplane_locked\"' '$rootfs_extract_tmp/etc/ly-route/default-config.json' && grep -q '\"interfaces\": \[\]' '$rootfs_extract_tmp/etc/ly-route/default-config.json'"
run sh -c "grep -q '\"interfaces\": \[\"eth0\"\]' '$rootfs_extract_tmp/etc/kea/kea-dhcp4.conf'"
run sh -c "grep -q 'DHCP=no' '$rootfs_extract_tmp/etc/systemd/network/10-ethernet-dhcp.network'"
run sh -c "grep -q 'After=ly-route-firstboot.service' '$rootfs_extract_tmp/etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf'"
rm -rf "$rootfs_extract_tmp"
rm -rf "$runtime_debs_tmp" "$rootfs_smoke_tmp"

run sh -c "cd '$repo_root/backend' && go test ./..."

for validator in \
  "$repo_root/.sisyphus/api-contracts/validate_api_contracts.py" \
  "$repo_root/.sisyphus/auth-contracts/validate_auth_contracts.py" \
  "$repo_root/.sisyphus/dataplane-contracts/validate_dataplane_contracts.py" \
  "$repo_root/.sisyphus/ha-contracts/validate_ha_contracts.py" \
  "$repo_root/.sisyphus/implementation-contracts/validate_contracts.py" \
  "$repo_root/.sisyphus/maintenance-contracts/validate_maintenance_contracts.py" \
  "$repo_root/.sisyphus/migration-guardrails/validate_migration_guardrails.py" \
  "$repo_root/.sisyphus/observability-contracts/validate_observability_contracts.py" \
  "$repo_root/.sisyphus/persistence-contracts/validate_persistence_contracts.py" \
  "$repo_root/.sisyphus/policy-contracts/validate_policy_contracts.py" \
  "$repo_root/.sisyphus/release-readiness/validate_release_readiness.py" \
  "$repo_root/.sisyphus/security-contracts/validate_security_contracts.py" \
  "$repo_root/.sisyphus/service-contracts/validate_service_contracts.py" \
  "$repo_root/.sisyphus/full-acceptance/validate_full_acceptance.py" \
  "$repo_root/scripts/validate_qos_intent_simulator.py"
do
  run python3 "$validator"
  run python3 -m py_compile "$validator"
done

run python3 "$repo_root/.sisyphus/full-acceptance/test_validate_full_acceptance.py"
run python3 -m py_compile "$repo_root/.sisyphus/full-acceptance/test_validate_full_acceptance.py"

run "$repo_root/scripts/validate-rootfs-scaffold.sh"

printf '\nCI verification completed.\n'
