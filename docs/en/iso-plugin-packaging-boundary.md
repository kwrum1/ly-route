# ISO Plugin Packaging Boundary

> This boundary applies only to rootfs/ISO release builds, not daily hotfixes.
> Hotfix the affected plugin first and follow the development workflow.

This is a hard gate for the Gateway ISO. It prevents a release from containing
the control-plane binaries while omitting the VPP plugins required by the data
plane.

## Scope

- Gateway: `rootfs → disk image → installer ISO` must carry and verify the
  following VPP plugins.
- Orchestrator: it carries only the Orchestrator plugin and must not include the
  Gateway PPPoE, Smart QoS, security, DNS-intercept, or pre-NAT-route plugins.
- The same boundary applies to amd64 and arm64. Plugins must match both the
  target architecture and the VPP version in the rootfs.

## Required Gateway plugins

| Runtime package | Required VPP plugin |
|---|---|
| `ly-route-vpp-pppoe-client` | `ly_route_pppoe_client_plugin.so` |
| `ly-route-vpp-smart-qos` | `ly_route_smart_qos_plugin.so` |
| `ly-route-vpp-security-guard` | `ly_route_security_guard_plugin.so` |
| `ly-route-vpp-dns-intercept` | `ly_route_dns_intercept_plugin.so` |
| `ly-route-vpp-pre-nat-route` | `ly_route_pre_nat_route_plugin.so` |

The Gateway must also carry these runtime adapters and dependencies. They are
not VPP `.so` plugins, but omitting any of them makes the installed product
incomplete:

| Package/dependency | Required installed content |
|---|---|
| `ly-route-dns-vpp-proxy` | `/usr/lib/ly-route/ly-route-dns-vpp-proxy`, the v6 alias, and both systemd units |
| `ly-route-vpp-apply` | `/usr/lib/ly-route/vpp-apply` |
| `vpp` | `/usr/bin/vpp` and `/usr/bin/vppctl` |
| `smartdns`, `kea-dhcp4-server`, `xray`, `ipset` | Their runtime binaries and product service dependencies |

The plugin directories are:

- amd64: `/usr/lib/x86_64-linux-gnu/vpp_plugins/`
- arm64: `/usr/lib/aarch64-linux-gnu/vpp_plugins/`

The PPPoE control binary `/usr/lib/ly-route/ly-route-pppoe-client` is not a
replacement for `ly_route_pppoe_client_plugin.so`.

## Build gates

1. `build-runtime-debs.sh` must produce the complete Gateway runtime package
   set for the target architecture.
2. `build-rootfs.sh` must verify all required packages, all five non-empty
   VPP `.so` files, the DNS adapter, VPP control programs, and service files.
3. `build-disk-image.sh` must re-check packages, files, units, and plugins
   after extracting the rootfs and write `runtime_packages`, `runtime_files`,
   `runtime_units`, and `runtime_plugins` to the image manifest.
4. `build-auto-install-iso.sh` accepts only a Gateway image with all four
   complete attestations. If one plugin, adapter, or service dependency is
   missing, ISO creation stops.
5. After installation, `ly-route-runtime-check.service` must verify
   `vppctl show plugin`, the corresponding CLI, live capability, and service
   dependencies. A static file is not runtime proof.
6. Physical-hardware installation additionally requires evidence for driver
   discovery, stable MAC/PCI mapping, management-NIC exclusion, data-NIC
   ownership, and VPP/plugin architecture compatibility. VMXNET3/AF_PACKET
   acceptance cannot substitute for this evidence.
7. Each build removes stale Ly Route ISO files and their checksum/manifest
   sidecars. An old ISO, image, or serial log cannot be reused as acceptance
   evidence.

## Release checks

```bash
./scripts/build-runtime-debs.sh all
./scripts/build-rootfs.sh --product gateway --arch amd64
./scripts/build-disk-image.sh --product gateway --rootfs <rootfs>
./scripts/build-auto-install-iso.sh --product gateway --image <image>
```

Retain at least this evidence after installation:

```bash
vppctl show plugin
systemctl status ly-route-runtime-check.service
cat /usr/share/ly-route/artifact-manifest.json
```

If any plugin gate fails, the Gateway ISO is a failed artifact and must not
enter installation acceptance or production release.
