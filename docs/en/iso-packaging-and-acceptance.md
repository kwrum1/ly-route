# ISO Packaging and Installation Acceptance

> This document covers release packaging and installation smoke only. Hotfixes do not run this document; use [Development, Hotfix, and Acceptance Workflow](development-workflow.md).

## Artifact

The canonical x86 Gateway installer is:

`ly-route-gateway-x86_64-installer.iso`

The build also emits checksums, a manifest, a disk image, and an upgrade package. Each build uses a separate output directory and removes stale Ly Route artifacts in that directory. An old ISO or serial log is never current evidence.

The factory management address is `192.168.88.254/24` with gateway `192.168.88.1`. The installer records management and data interface mappings by MAC/PCI identity. The management NIC remains Linux-owned; the runtime selector handles data interfaces.

## Release Procedure

```bash
set -euo pipefail
repo=/root/ly-route
cd "$repo"

./scripts/build-rootfs.sh --product gateway --arch amd64 --out dist/rootfs
rootfs=dist/rootfs/ly-route-rootfs-gateway-bookworm-amd64.tar.zst
./scripts/build-disk-image.sh --product gateway --rootfs "$rootfs" \
  --out dist/x86 --size 4G
image=dist/x86/ly-route-gateway-bookworm-amd64-4g.img.zst
./scripts/build-auto-install-iso.sh --product gateway --image "$image" \
  --out dist/iso
```

Runtime packages, plugins, and rootfs must come from one build with matching architecture, VPP version, and Bookworm dependencies. The plugin list is defined by `docs/en/iso-plugin-packaging-boundary.md` and the build manifest; a file existing on disk is not runtime proof.

## Minimum Release Checks

```bash
./scripts/ci-release-verify.sh
sha256sum -c dist/iso/*.iso.sha256
file dist/iso/*.iso
xorriso -indev dist/iso/*.iso -report_el_torito plain
```

The release check proves source compilation, product package boundaries, rootfs scaffolding, bootable ISO structure, and sidecar checksums. Feature behaviour is accepted in the feature batch before release and is not repeated here.

## Installation Smoke

1. Use the one ISO from the current build and create a fresh console or serial log.
2. Confirm disk and management-NIC selection, payload write, write verification, and reboot.
3. After booting from disk, confirm management address, UI/API, VPP/related services, and the persisted MAC/PCI map.
4. Run one basic smoke: management access and affected dataplane observability.

ESXi, VMXNET3, AF_PACKET, VFIO, IOMMU, physical PCI ownership, performance, and temperature are separate hardware tasks. They do not block daily hotfixes and VM results are not physical-performance evidence.

## Repackaging After a Fix

- Repair a functional bug in the current environment first with the hotfix flow.
- Rebuild the ISO only when rootfs, plugins, installer logic, or release scripts changed, or when the feature batch is being frozen.
- Preserve the previous log in an archive directory; clean stale ISO sidecars and manifests from the output directory without deleting unrelated files.
- Record the new ISO name, SHA-256, source revision, and build time in one manifest.
