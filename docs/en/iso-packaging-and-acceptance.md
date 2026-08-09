# Frozen ISO Packaging Flow and Full Acceptance Checklist

> Status: frozen baseline, 2026-08-09.
>
> Scope: Ly Route Gateway and Ly Route Orchestrator. Their source trees,
> frontends, backends, runtime services, and release packages remain isolated.

## 1. Baseline conclusion

The packaging flow is frozen, but the current installer is still awaiting a
clean installation run:

- Canonical artifact: `ly-route-gateway-x86_64-installer.iso`.
- The previous SHA-256 and `Installation complete` lines came from an older
  ISO and a reused serial log. They are not evidence for the current artifact.
- The previous compressed disk payload used Bookworm-compatible VPP runtime
  libraries, but it did not contain a verified VMXNET3 DMA fix and is not a
  release baseline.
- VPP packages built directly on the compiler host require
  `GLIBC_2.38`/`libc6 >= 2.42` and are rejected. VPP must be rebuilt in the
  Bookworm target environment and pass section 6 before ISO injection.

Installation and system-function acceptance are separate gates.

## 2. Frozen packaging procedure

### 2.1 Input rules

1. Build only from the current complete source tree. Do not mix the old cropped
   `/root/ly-route` tree, old rootfs archives, old VPP debs, or old ISOs.
2. The release target is Debian Bookworm amd64.
3. VPP must be built by `scripts/build-vpp-bookworm-debs.sh`; runtime, plugin,
   and development packages must all be `25.10.0-release`.
4. The extra-deb directory and the rootfs dpkg database must agree: VPP package
   versions must match, dependencies must be Bookworm (`libc6 >= 2.34`), no
   `libc6 >= 2.38`, `libc6 >= 2.42`, or `libssl3t64` may appear, and the highest
   GLIBC requirement of `libvppinfra.so` must be `GLIBC_2.34` or lower.

### 2.2 Fixed build order

```bash
set -euo pipefail
repo=/root/ly-route-worktrees/codex-handoff-20260730
cd "$repo"
export LY_ROUTE_MIRROR=https://mirrors.ustc.edu.cn/debian
export LY_ROUTE_SECURITY_MIRROR=https://mirrors.ustc.edu.cn/debian-security
export LY_ROUTE_EXTRA_DEBS_DIR=/root/ly-route/runtime-debs-gateway-bookworm-amd64

./scripts/build-rootfs.sh --product gateway --arch amd64 --out dist/rootfs-release
rootfs=dist/rootfs-release/ly-route-rootfs-gateway-bookworm-amd64.tar.zst
./scripts/build-disk-image.sh --product gateway --rootfs "$rootfs" \
  --out dist/x86-release --size 4G
image=dist/x86-release/ly-route-gateway-bookworm-amd64-4g.img.zst
./scripts/build-auto-install-iso.sh --product gateway --image "$image" \
  --out dist/iso-release
```

`build-auto-install-iso.sh` deletes every stale `ly-route-gateway*.iso` and
associated checksum/manifest from its output directory before building. A
successful build leaves exactly one canonical ISO and its three sidecars.

### 2.3 Required artifact checks

```bash
sha256sum -c dist/x86-release/*.img.sha256
sha256sum -c dist/x86-release/*.img.zst.sha256
sha256sum -c dist/x86-release/*.manifest.json.sha256
sha256sum -c dist/iso-release/*.iso.sha256
file dist/iso-release/*.iso
xorriso -indev dist/iso-release/*.iso -report_el_torito plain
```

The ISO must be ISO 9660 with BIOS and UEFI boot entries, contain the fixed
payload name `ly-route-gateway-x86_64.img.zst`, and have a manifest whose
embedded-payload hash matches the disk image archive.

### 2.4 ESXi installation gate

1. Upload the canonical ISO, verify its SHA-256 on the datastore, then remove
   every other root-level `ly-route-gateway*.iso`. Do not remove unrelated ISOs
   or virtual-machine directories.
2. Clean the old acceptance VM and retain only the intended system disk and
   test NICs.
3. Attach the new ISO and verify that the datastore prefix appears exactly once.
4. Start a new serial log for this run and require target-disk
   selection/confirmation, write
   progress, `Installation complete`, and the reboot countdown.
5. Eject the ISO, boot the installed disk, and only then start function tests.

Changing any packaging script, VPP package set, rootfs package list, installer
script, or payload layout invalidates the baseline. Generate a new dated output
directory and repeat all checks; never hand-compose an ISO or reuse old files.

## 3. Known issue register

| ID | Product | Issue and evidence | Status |
|---|---|---|---|
| ISO-001 | Gateway | Canonical ISO must boot, write `/dev/sda`, complete installation, and reboot; prior serial evidence was stale | Retest |
| ISO-002 | Gateway | Video and serial boot are now verified on ESXi; repeat after the interactive-installer rebuild | Retest |
| ISO-003 | Gateway | The installer reached disk selection but its `StandardInput=null` unit could not read `/dev/tty`; `ttyS0` binding and read fallback are pending rebuild | Open |
| PKG-001 | Gateway | Disk payload is Bookworm-compatible, but the VM is still booting an older installation | Retest |
| VPP-001 | Gateway | VPP service, `vppctl`, and data interfaces are not functional | Open |
| NIC-001 | Gateway | Management/data ownership and native VMXNET3 evidence are incomplete | Pending |
| DNS-001 | Gateway | SmartDNS, transparent interception, and VPP DNS proxy chain are not verified | Pending |
| GW-001 | Gateway | UI-configured PPPoE must obtain a real `10.1.18.0/24` WAN address | Pending |
| GW-002 | Gateway | LAN DHCP, NAT, port mapping, and IPv6 PD/RA are not fully verified | Pending |
| GW-003 | Gateway | GeoIP/GeoSite domestic-direct and foreign-proxy traffic/DNS split is not verified | Pending |
| GW-004 | Gateway | QoS, security, IP groups/ranges/CIDR, and any semantics are not fully verified | Pending |
| GW-005 | Gateway | Runtime dashboard, users, top connections/domains, charts, configuration and upgrade are not fully verified | Pending |
| ORCH-001 | Orchestrator | Independent product image and service set are not yet accepted | Pending |
| ORCH-002 | Orchestrator | Group ordering, rule CRUD, path display, ACL and limits are not yet data-plane accepted | Pending |

## 4. Acceptance checklists

### Gateway

- [ ] Clean canonical ISO installation with a new per-run serial log.
- [ ] VPP package contents, dependencies, service startup, and `vppctl` are valid.
- [ ] Static management address works; management NIC is not VPP-owned.
- [ ] Remaining NICs use MAC/PCI mapping and the selected high-performance path.
- [ ] UI-configured PPPoE dials the real server and reaches `10.1.18.0/24`.
- [ ] LAN address, DHCP, outbound NAT, port mapping, and IPv6 PD/RA work with a real client.
- [ ] All client DNS port 53 traffic is intercepted, including manually configured DNS.
- [ ] GeoSite-hit DNS uses domestic bootstrap; non-hit DoH uses foreign bootstrap.
- [ ] Domestic GeoIP/GeoSite traffic uses PPPoE; foreign default traffic uses proxy WAN.
- [ ] Fixed/fastest proxy-node selection and failure switching work.
- [ ] Functional rate limiting, built-in smart QoS, security rules, IP groups, CIDR/range, and any semantics work.
- [ ] Dashboard counters and WAN total up/down charts reflect real interfaces.
- [ ] Backup/restore, management settings, upgrade, reboot, and rollback boundaries work.

### Orchestrator

- [ ] Independent frontend/backend/image/service set; no NAT, DHCP, or DNS pseudo-features.
- [ ] Only unassigned interfaces can form a two-port directed orchestration group.
- [ ] Chinese group names, collapse, title-bar drag ordering, and rule CRUD work.
- [ ] Rules support any, IP/CIDR/range/IP group, protocol, ports, and named path groups.
- [ ] Multiple virtual clients prove ACL direction, matching priority, and limits with real packets.
- [ ] Status, path traffic, and connection state match the API and remain correctly laid out for empty data.

## 5. Fast regression loop

Every regression starts from a clean VM/disk and rebuilt PPPoE/client network
namespaces. Record the browser request, final API configuration, service log,
and data-plane packet result. A checkbox is allowed only when all four are true:

`UI configuration accepted` + `backend received final configuration` +
`data plane applied it` + `clean regression passed`.

## 6. Fixed repair workflow for “booted but did not take ownership”

This is a mandatory gate. The repeated failure had two causes:

1. VMXNET3 data NICs were left on the Linux driver. VPP rejected
   `create interface vmxnet3` with `device not bound to vfio-pci or
   uio_pci_generic`, leaving only `local0`.
2. VFIO binding alone is not proof of DMA support. A VPP plugin without the
   `vlib_pci_map_dma` VMXNET3 changes produced DMAR faults in this virtual PCI
   environment. The VPP package must be built with the VMXNET3 VFIO DMA patch.

### 6.1 The only permitted repair order

1. **Reproduce first.** Collect `systemctl status`, `journalctl -b`,
   `vppctl show interface`, PCI driver ownership, and kernel DMA errors.
2. **Hot-repair the local image.** Mount only the canonical raw image root
   partition and inject scripts or target Bookworm packages. Never inject VPP
   packages built against the compiler host distribution into a Bookworm image.
3. **Package gate.** Run `dpkg-deb -f` dependency checks for every VPP deb,
   verify architecture/version/libc/SSL compatibility with Bookworm, and verify
   `vpp-plugin-core` came from source built with
   `0001-vmxnet3-map-vfio-dma.patch`.
4. **Validate the hot fix.** Require `ly-route-vfio-preflight` and `vpp` to be
   active, data PCI devices owned by `vfio-pci`, successful VPP data-interface
   creation, and no continuing `DMAR`, `PTE Write access is not set`, or
   `device not bound` messages.
5. **Only then rebuild the ISO.** Eject old media, attach the new canonical ISO,
   require disk/NIC selection, write progress, `Installation complete`, and the
   reboot countdown, eject the ISO, and boot from the installed disk.
6. **Record the run.** Keep only `ly-route-gateway-x86_64-installer.iso` and its
   checksum/manifest sidecars. Every run gets a new serial log; incomplete
   evidence remains pending, never passed.

### 6.2 Path-selection boundaries

- Linux always retains the management NIC; VPP must not own it.
- Physical data NICs use the highest verified native VPP path, with DPDK as the
  fallback only when the native path is unavailable.
- ESXi VMXNET3 must use the VPP VMXNET3 driver with VFIO/UIO PCI ownership;
  Linux retaining `vmxnet3` is not a valid native-VPP result.
- AF_XDP/AF_PACKET are lab/container paths only and cannot replace a production
  dataplane conclusion.
- Production packages must be built in a Debian Bookworm target environment.
  Host-built packages requiring `libc6 >= 2.38` or `libssl3t64` are rejected.

### 6.3 Current gates

- [ ] Bookworm VPP packages build successfully and `vpp-plugin-core` contains
      the VMXNET3 DMA fix.
- [ ] The local mounted-image hot fix transfers ownership and has no DMA fault.
- [ ] An ISO injected from that verified image completes one clean install and
      boots from the system disk.
- [ ] The installed management UI is reachable, data NICs are VPP-owned, and a
      new serial log proves this run.
