#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python_bin=${LY_ROUTE_PYTHON:-python3}
if [ "${LY_ROUTE_PYTHON+x}" = x ]; then
  python_shim_dir=$(mktemp -d)
  trap 'rm -rf "$python_shim_dir"' EXIT HUP INT TERM
  printf '#!/bin/sh\nexec "%s" "$@"\n' "$python_bin" >"$python_shim_dir/python3"
  chmod +x "$python_shim_dir/python3"
  PATH=$python_shim_dir:$PATH
fi

required_files="
$repo_root/scripts/build-rootfs.sh
$repo_root/scripts/build-runtime-debs.sh
$repo_root/scripts/build-disk-image.sh
$repo_root/scripts/build-auto-install-iso.sh
$repo_root/scripts/rootfs-runtime-smoke.sh
$repo_root/packaging/runtime-boundaries/gateway.sh
$repo_root/scripts/test-firstboot-env-migration.sh
$repo_root/scripts/test-vpp-native-selection.sh
$repo_root/scripts/test-vpp-tuning.sh
$repo_root/scripts/test-dns-ipset-sync.sh
$repo_root/packaging/rootfs-overlay/etc/systemd/network/10-ethernet-dhcp.network
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-firstboot.service
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/migrate-control-env.sh
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-runtime-check.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-vpp-apply.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-vpp-session-enable.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-vpp-tune.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/vpp.service.d/10-ly-route-tuning.conf
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-recovery.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-dns-ipset-sync.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-dns-ipset-sync.timer
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-dns-vpp-v6-namespace.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-dns-vpp-session.service
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-pppoe.target
$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-pppoe@.service
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/active-dpdk-state.py
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/recover-runtime.sh
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-ipset-sync.py
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-v6-namespace-apply
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-session-apply
$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/enable-vpp-session.sh
$repo_root/packaging/rootfs-overlay/etc/ly-route/vcl.conf
$repo_root/packaging/rootfs-overlay/etc/ly-route/vcl-v6.conf
$repo_root/packaging/rootfs-overlay/etc/ly-route/default-config.json
$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env
$repo_root/packaging/rootfs-overlay/etc/kea/kea-dhcp4.conf
$repo_root/packaging/rootfs-overlay/etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf
$repo_root/packaging/nginx/ly-route-admin.conf
$repo_root/backend/cmd/gateway-control/main.go
$repo_root/backend/cmd/orchestrator-control/main.go
$repo_root/backend/gateway/main.go
$repo_root/backend/orchestrator/main.go
$repo_root/backend/internal/httpapi/server.go
$repo_root/backend/internal/httpapi/auth.go
$repo_root/frontend/gateway/index.html
$repo_root/frontend/gateway/styles.css
$repo_root/frontend/gateway/app.js
$repo_root/frontend/orchestrator/index.html
$repo_root/frontend/orchestrator/styles.css
$repo_root/frontend/orchestrator/app.js
$repo_root/.github/workflows/gateway-release.yml
$repo_root/docs/rootfs-image.md
"

printf '%s\n' "$required_files" | while IFS= read -r file; do
  [ -n "$file" ] || continue
  if [ ! -f "$file" ]; then
    echo "missing required file: $file" >&2
    exit 1
  fi
done

sh -n "$repo_root/scripts/build-rootfs.sh"
bash -n "$repo_root/scripts/build-runtime-debs.sh"
sh -n "$repo_root/scripts/rootfs-runtime-smoke.sh"
sh -n "$repo_root/scripts/test-firstboot-env-migration.sh"
sh -n "$repo_root/scripts/test-vpp-native-selection.sh"
sh -n "$repo_root/scripts/test-vpp-tuning.sh"
sh -n "$repo_root/scripts/test-dns-ipset-sync.sh"
sh -n "$repo_root/scripts/validate-rootfs-scaffold.sh"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/migrate-control-env.sh"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-v6-namespace-apply"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-session-apply"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/enable-vpp-session.sh"
sh -n "$repo_root/scripts/test-vpp-dns-transparent-abf.sh"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh"
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh"
"$python_bin" -m py_compile "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/active-dpdk-state.py"

case "$(uname -s)" in
  Linux*)
    "$repo_root/scripts/test-firstboot-env-migration.sh"
    "$repo_root/scripts/test-dns-ipset-sync.sh"
    "$repo_root/scripts/test-vpp-tuning.sh"
    ;;
  *)
    echo "Linux runtime integration checks skipped on $(uname -s); syntax and compile checks remain active"
    ;;
esac
sh -n "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/recover-runtime.sh"
# The release entrypoint owns the focused behavioral test matrix. This
# standalone scaffold validator only needs to prove every package still
# compiles; running the historical suite here would silently reintroduce
# retired Linux/pppd fixture contracts into the native-VPP release gate.
(cd "$repo_root/backend" && go test -run '^$' ./...)

for token in Panabit "assets/" VRRP vrrp login-logo admin-logo login-background "--pa-"; do
  if grep -R --line-number --fixed-strings --exclude='*.dat' -- "$token" \
    "$repo_root/frontend/gateway" \
    "$repo_root/frontend/orchestrator" \
    "$repo_root/Dockerfile" \
    "$repo_root/docker-compose.yml" \
    "$repo_root/packaging" \
    "$repo_root/.github/workflows/gateway-release.yml" >/tmp/ly-route-rootfs-grep.txt; then
    cat /tmp/ly-route-rootfs-grep.txt >&2
    exit 1
  fi
done

if ! grep -q 'DHCP=no' "$repo_root/packaging/rootfs-overlay/etc/systemd/network/10-ethernet-dhcp.network"; then
  echo "factory default networking must not request WAN DHCP on every Ethernet interface" >&2
  exit 1
fi

if grep -q '"gateway_role": "wan"' "$repo_root/packaging/rootfs-overlay/etc/ly-route/default-config.json"; then
  echo "factory default config must not preconfigure a WAN interface" >&2
  exit 1
fi

if ! grep -q '"active_path": "dataplane_locked"' "$repo_root/packaging/rootfs-overlay/etc/ly-route/default-config.json" || ! grep -q '"interfaces": \[\]' "$repo_root/packaging/rootfs-overlay/etc/ly-route/default-config.json"; then
  echo "factory default config must keep forwarding locked without explicit interface assignments" >&2
  exit 1
fi

if ! grep -q '"interfaces": \["eth0"\]' "$repo_root/packaging/rootfs-overlay/etc/kea/kea-dhcp4.conf"; then
  echo "factory Kea config does not bind DHCP to eth0" >&2
  exit 1
fi

if ! grep -q '192.168.88.100 - 192.168.88.199' "$repo_root/packaging/rootfs-overlay/etc/kea/kea-dhcp4.conf"; then
  echo "factory Kea config does not expose the default LAN DHCP pool" >&2
  exit 1
fi

if ! grep -q 'build-runtime-debs.sh smartdns' "$repo_root/.github/workflows/gateway-release.yml" || ! grep -q 'build-runtime-debs.sh xray' "$repo_root/.github/workflows/gateway-release.yml"; then
  echo "GitHub x86 firmware workflow does not package SmartDNS and xray runtime services" >&2
  exit 1
fi

if ! grep -q "required_runtime_packages='libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply'" "$repo_root/scripts/build-rootfs.sh" ||
   ! grep -q 'product.*gateway.*required_runtime_packages' "$repo_root/scripts/build-rootfs.sh"; then
  echo "product rootfs builder does not select runtime packages by product profile" >&2
  exit 1
fi

if ! grep -q 'openssh-server' "$repo_root/scripts/build-rootfs.sh" || ! grep -q 'ssh.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "physical rootfs does not include and enable SSH" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_SYSTEM_ROOT_PASSWORD=admin12345' "$repo_root/packaging/rootfs-overlay/etc/ly-route/appliance.env"; then
  echo "factory root SSH password is not provisioned" >&2
  exit 1
fi

if ! grep -q 'mmdebstrap --architectures="$deb_arch"' "$repo_root/scripts/build-runtime-debs.sh" || ! grep -q 'chroot "$chroot_dir"' "$repo_root/scripts/build-runtime-debs.sh"; then
  echo "SmartDNS is not built inside the target Debian environment" >&2
  exit 1
fi

if ! grep -q 'listen 80' "$repo_root/packaging/nginx/ly-route-admin.conf"; then
  echo "admin nginx config does not listen on port 80" >&2
  exit 1
fi

if ! grep -q 'proxy_pass http://127.0.0.1:8080/api/v1/' "$repo_root/packaging/nginx/ly-route-admin.conf"; then
  echo "admin nginx config does not proxy the local control API" >&2
  exit 1
fi
if ! grep -q 'client_max_body_size 5g;' "$repo_root/packaging/nginx/ly-route-admin.conf"; then
  echo "admin nginx config does not allow firmware uploads" >&2
  exit 1
fi

if ! grep -q 'ly-route-control-api.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "control API service is not enabled by the rootfs builder" >&2
  exit 1
fi

if ! grep -q 'kea-dhcp4-server.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "factory LAN DHCP service is not enabled by the rootfs builder" >&2
  exit 1
fi

if ! grep -q 'After=ly-route-firstboot.service' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/kea-dhcp4-server.service.d/ly-route-firstboot.conf"; then
  echo "factory LAN DHCP service is not ordered after firstboot LAN setup" >&2
  exit 1
fi

if ! grep -q 'ly-route-runtime-check.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "runtime readiness service is not enabled by the rootfs builder" >&2
  exit 1
fi

if ! grep -q 'ly-route-vpp-apply.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "VPP apply service is not enabled by the rootfs builder" >&2
  exit 1
fi

if ! grep -q 'ly-route-recovery.service' "$repo_root/scripts/build-rootfs.sh"; then
  echo "runtime recovery service is not enabled by the rootfs builder" >&2
  exit 1
fi

for package in kea-dhcp4-server ipset curl python3; do
  if ! grep -q "$package" "$repo_root/scripts/build-rootfs.sh"; then
    echo "rootfs builder does not include runtime package $package" >&2
    exit 1
  fi
done

if ! grep -q 'LY_ROUTE_EXTRA_PACKAGES' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not expose commercial extra package hook" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_EXTRA_DEBS_DIR' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not expose local .deb package hook" >&2
  exit 1
fi

if ! grep -q 'dpkg-deb -x' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not unpack local .deb packages in tar-only smoke builds" >&2
  exit 1
fi

for target in smartdns xray vpp vpp-apply vpp-pppoe-client vpp-smart-qos vpp-security-guard; do
  if ! grep -q "$target" "$repo_root/scripts/build-runtime-debs.sh"; then
    echo "runtime .deb source builder does not expose target $target" >&2
    exit 1
  fi
done

if ! grep -q '^runtime-debs:' "$repo_root/Makefile"; then
  echo "Makefile does not expose runtime-debs target" >&2
  exit 1
fi

if ! grep -q 'vpp-apply' "$repo_root/.github/workflows/gateway-release.yml" ||
   ! grep -q 'build-runtime-debs.sh' "$repo_root/.github/workflows/gateway-release.yml"; then
  echo "rootfs image workflow does not build the local runtime adapter package" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_EXTRA_DEBS_DIR' "$repo_root/.github/workflows/gateway-release.yml"; then
  echo "rootfs image workflow does not inject local runtime .deb packages" >&2
  exit 1
fi

if ! grep -q 'go build .*./cmd/\$product-control' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not build the product-specific Go control API binary" >&2
  exit 1
fi

if ! grep -q 'ExecStart=/usr/lib/ly-route/ly-route-control' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service"; then
  echo "control API service does not start the Go control API binary" >&2
  exit 1
fi

if ! grep -q 'After=.*ly-route-runtime-check.service' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service"; then
  echo "control API service is not gated by runtime readiness" >&2
  exit 1
fi

if ! grep -q 'Requires=.*ly-route-firstboot.service' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-control-api.service"; then
  echo "control API service is not gated by firstboot LAN detection" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_ENABLE_SERVICE_RUNTIME=true' "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
  echo "firmware runtime env does not enable service runtime" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_VPP_APPLY_COMMAND=' "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
  echo "firmware runtime env does not expose VPP apply command hook" >&2
  exit 1
fi

for required_command in vppctl /usr/lib/ly-route/vpp-apply; do
  if ! grep -q "$required_command" "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
    echo "firmware runtime env does not require $required_command" >&2
    exit 1
  fi
done

if ! grep -q 'LY_ROUTE_VPP_COMMAND_MAP=' "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
  echo "firmware runtime env does not expose VPP command map hook" >&2
  exit 1
fi

if [ ! -f "$repo_root/packaging/rootfs-overlay/etc/ly-route/vpp-command-map.json" ]; then
  echo "firmware rootfs overlay does not provide a VPP command map" >&2
  exit 1
fi

if ! grep -q '"operations"' "$repo_root/packaging/rootfs-overlay/etc/ly-route/vpp-command-map.json"; then
  echo "VPP command map does not expose an operations object" >&2
  exit 1
fi

for forbidden in no-zero-copy native-driver-auto; do
  if grep -R -i --line-number --fixed-strings -- "$forbidden" \
    "$repo_root/scripts/build-runtime-debs.sh" \
    "$repo_root/packaging/rootfs-overlay/etc/vpp/startup.conf" \
    "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env" \
    "$repo_root/packaging/rootfs-overlay/etc/ly-route/default-config.json" \
    "$repo_root/packaging/rootfs-overlay/etc/ly-route/vpp-command-map.json" \
    "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/tune-vpp.sh" \
    "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh" \
    "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh" >/tmp/ly-route-forbidden-dataplane.txt; then
    cat /tmp/ly-route-forbidden-dataplane.txt >&2
    exit 1
  fi
done

if ! grep -q '^LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE=false$' \
  "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
  echo "VMXNET3 AF_PACKET acceptance must be disabled in the baseline rootfs" >&2
  exit 1
fi

for required_lcp in 'linux_cp_plugin.so { enable }' 'linux_nl_plugin.so { enable }'; do
  if ! grep -Fq "$required_lcp" "$repo_root/packaging/rootfs-overlay/etc/vpp/startup.conf"; then
    echo "VPP startup configuration is missing shared-LAN control-plane support: $required_lcp" >&2
    exit 1
  fi
done

runtime_check="$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh"
for required_dpdk_guard in iommu_group vfio_pci nr_hugepages dpdk_plugin.so pci_address kernel_driver iommu_protected; do
  if ! grep -q -- "$required_dpdk_guard" "$runtime_check"; then
    echo "DPDK fallback capability probe is missing safety guard: $required_dpdk_guard" >&2
    exit 1
  fi
done
if grep -R -i --line-number -E 'enable_unsafe_noiommu|no[-_ ]?iommu.*(enable|allow)|vfio.*noiommu' \
  "$repo_root/packaging/rootfs-overlay" "$repo_root/backend" >/tmp/ly-route-unsafe-vfio.txt; then
  cat /tmp/ly-route-unsafe-vfio.txt >&2
  exit 1
fi

if grep -q '^data_if=' "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh"; then
  echo "firstboot must not automatically assign a data interface" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_VPP_RECEIPT=' "$repo_root/packaging/rootfs-overlay/etc/ly-route/runtime.env"; then
  echo "firmware runtime env does not expose VPP apply receipt path" >&2
  exit 1
fi

if ! grep -q 'Requires=vpp.service' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-vpp-apply.service"; then
  echo "VPP apply service must hard-require vpp.service" >&2
  exit 1
fi

if grep -q 'vpp.service is not active' "$repo_root/backend/internal/runtime/service/runtime.go" "$repo_root/scripts/build-rootfs.sh"; then
  echo "VPP runtime apply must not skip missing vpp.service" >&2
  exit 1
fi

if ! grep -q 'ly-route-pppoe.target' "$repo_root/scripts/build-rootfs.sh" ||
   ! grep -q 'ly-route-pppoe-client' "$repo_root/scripts/build-rootfs.sh"; then
  echo "gateway rootfs is not wired to the native PPPoE client" >&2
  exit 1
fi

if ! grep -q 'vpp-pppoe-client' "$repo_root/.github/workflows/gateway-release.yml" ||
   ! grep -q 'build-runtime-debs.sh' "$repo_root/.github/workflows/gateway-release.yml"; then
  echo "gateway release workflow does not build the native PPPoE VPP plugin" >&2
  exit 1
fi
for plugin in ly_route_pppoe_client_plugin.so ly_route_smart_qos_plugin.so ly_route_security_guard_plugin.so; do
  if ! grep -R -q -- "$plugin" "$repo_root/scripts/rootfs-runtime-smoke.sh" "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/runtime-check.sh"; then
    echo "gateway runtime gate does not verify VPP plugin $plugin" >&2
    exit 1
  fi
  if ! grep -q -- "$plugin" "$repo_root/packaging/runtime-boundaries/gateway.sh"; then
    echo "gateway packaging boundary does not declare VPP plugin $plugin" >&2
    exit 1
  fi
done

for component in ly-route-dns-vpp-proxy ly-route-vpp-apply smartdns kea-dhcp4-server xray ipset; do
  if ! grep -q -- "$component" "$repo_root/packaging/runtime-boundaries/gateway.sh"; then
    echo "gateway packaging boundary does not declare runtime component $component" >&2
    exit 1
  fi
done
for builder in build-rootfs.sh build-disk-image.sh build-auto-install-iso.sh; do
  if ! grep -q 'packaging/runtime-boundaries/gateway.sh' "$repo_root/scripts/$builder"; then
    echo "$builder does not enforce the Gateway runtime packaging boundary" >&2
    exit 1
  fi
done

if ! grep -q 'LY_ROUTE_POLICY_ROUTING_RECEIPT' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not write an explicit policy routing receipt for empty desired state" >&2
  exit 1
fi

if ! grep -q '/usr/lib/ly-route/vpp-apply-default' "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-vpp-apply.service"; then
  echo "VPP systemd service is not wired to persistent apply script" >&2
  exit 1
fi

if ! grep -q 'LY_ROUTE_VPP_OPERATIONS:=/var/lib/ly-route/vpp/operations.json' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder does not wire the persistent VPP apply script to the runtime adapter" >&2
  exit 1
fi

if grep -q 'VPP apply command has not been rendered yet' "$repo_root/scripts/build-rootfs.sh"; then
  echo "rootfs builder still writes a placeholder VPP apply script" >&2
  exit 1
fi

echo "rootfs scaffold validation passed"
