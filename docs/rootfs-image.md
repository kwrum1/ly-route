# Gateway release artifacts

The production workflow is `.github/workflows/gateway-release.yml`. It builds
Gateway artifacts only; Orchestrator packaging remains paused.

## x86-64

- A 4 GiB GPT disk image with BIOS and removable UEFI GRUB boot support.
- An `amd64` online upgrade package accepted by the Gateway firmware API.

## ARM64 / Armbian

The ARM job runs on GitHub's native ARM64 runner and builds VPP from the pinned
source commit. It emits:

- an Armbian Bookworm one-click installer containing the complete ARM64 runtime;
- an `arm64` online upgrade package.

The installer overlays Ly Route onto an existing Armbian system. It preserves
the board kernel, DTB, bootloader, partition layout and `/boot`. It does not bind
NICs to VFIO during installation. Runtime data-plane selection occurs only after
the administrator assigns LAN and WAN interfaces.

## Build commands

```sh
./scripts/build-rootfs.sh --product gateway --arch amd64 --out dist/rootfs
sudo ./scripts/build-disk-image.sh --product gateway \
  --rootfs dist/rootfs/ly-route-rootfs-gateway-bookworm-amd64.tar.zst \
  --out dist/x86 --size 4G
./scripts/build-upgrade-package.sh --product gateway --arch amd64 --out dist/upgrade
```

Complete rootfs builds require `LY_ROUTE_EXTRA_DEBS_DIR` to contain the matching
VPP, Ly Route plugins, SmartDNS, DNS adapter, Xray and native PPPoE packages.
The CI workflow is the authoritative example.

## Factory access

- Management fallback: `192.168.88.1/24` on the first Ethernet interface.
- Web console: `https://192.168.88.1/`.
- Login: `admin` / `password`; changing the password is mandatory on first login.
- Forwarding remains locked until LAN/WAN assignment and data-plane capability
  proof succeed.
