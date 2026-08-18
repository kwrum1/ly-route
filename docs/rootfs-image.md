# LY-Route Dual-Product Image Builds

> This is an artifact reference, not a daily development gate. The active CI is
> `.github/workflows/gateway-release.yml`; daily repairs use
> `scripts/ci-verify.sh` and hot deployment. Old Gitea workflows and old
> firmware names are retired.

This repository builds separate Gateway and Orchestrator Debian-based rootfs artifacts for `amd64` and `arm64`. Product profile selection is mandatory: an image must contain only that profile's services, API/resources, UI, database, and migrations. Runtime conversion between products is unsupported.

The first-boot LAN/DHCP behavior below applies to Gateway images. Orchestrator images use a dedicated Linux management interface and must not acquire Gateway NAT, DNS/DHCP, proxy, or PPPoE services.
The installer selects and records a management interface by MAC/PCI identity.
The factory management default is `192.168.88.254/24` with gateway
`192.168.88.1`; LAN/DHCP addresses are product configuration, not a substitute
for the management address.

## Local Build

Install build tools on a Debian/Ubuntu builder:

```sh
sudo apt-get update
sudo apt-get install -y mmdebstrap qemu-user-static binfmt-support zstd ca-certificates golang-go
```

Build one architecture:

```sh
make rootfs ARCH=amd64
make rootfs ARCH=arm64
```

Commercial runtime builds can add packages from the configured mirror or a local
directory of `.deb` artifacts:

```sh
LY_ROUTE_EXTRA_PACKAGES=vpp,smartdns,xray make rootfs ARCH=amd64
LY_ROUTE_EXTRA_DEBS_DIR=/path/to/runtime-debs make rootfs ARCH=amd64
```

If runtime `.deb` packages are not available, build them from source first:

```sh
make runtime-debs
LY_ROUTE_EXTRA_DEBS_DIR=/root/ly-route/runtime-debs make rootfs ARCH=amd64
```

The source builder writes packages to `/root/ly-route/runtime-debs` by default.
VPP source is never inferred from a repository-local directory: every VPP build
must receive an explicitly pinned `LY_ROUTE_VPP_SRC` path. Temporary upstream
trees and plugin build caches are removed after the package build, so a stale
`vpp-master` or `govpp-master` directory cannot be reused as Ly Route source.
SmartDNS or Xray source trees may be supplied explicitly with
`LY_ROUTE_SMARTDNS_SRC` and `LY_ROUTE_XRAY_SRC` when the builder has no outbound
GitHub access.
Package architecture defaults to the build host and can be overridden with
`LY_ROUTE_RUNTIME_DEB_ARCH` when building native packages on a dedicated worker.
Run individual targets such as `./scripts/build-runtime-debs.sh smartdns`,
`./scripts/build-runtime-debs.sh xray`, `./scripts/build-runtime-debs.sh vpp-fdio`,
`./scripts/build-runtime-debs.sh vpp`, or `./scripts/build-runtime-debs.sh vpp-apply`
when you want to build one runtime package group at a time. `vpp-fdio` downloads
pinned FD.io Debian bookworm packages for `libvppinfra`, `vpp`,
`vpp-plugin-core`, the approved native-path plugins, and the matching VPP DPDK
plugin used by the controlled fallback. Source builds are an alternative
package source, not a forwarding fallback.

The target artifact validator must require the automatic selector and permit
DPDK only through that selector. AF_PACKET is a functional lab path only and
does not qualify as a production dataplane result.

Rootfs artifacts are written to `dist/rootfs/` as:

```text
ly-route-rootfs-bookworm-amd64.tar.zst
ly-route-rootfs-bookworm-amd64.tar.zst.sha256
ly-route-rootfs-bookworm-arm64.tar.zst
ly-route-rootfs-bookworm-arm64.tar.zst.sha256
```

Build a burnable 4 GiB `amd64` disk image from a generated rootfs:

```sh
sudo ./scripts/build-disk-image.sh \
  --rootfs dist/rootfs/ly-route-rootfs-bookworm-amd64.tar.zst \
  --out dist/disk-image-current \
  --size 4G

Build ophub/Armbian ARM firmware from a generated `arm64` rootfs:

```sh
sudo ./scripts/build-ophub-armbian-images.sh \
  --rootfs dist/rootfs/ly-route-rootfs-bookworm-arm64.tar.zst \
  --boards all \
  --out dist/ophub-armbian
```

`LY_ROUTE_OPHUB_BOARDS` maps directly to ophub `rebuild -b` values such as
`all`, `amlogic`, `rockchip`, `allwinner`, `s905x3`, `rk3588`, or a device name.
`LY_ROUTE_OPHUB_BASE_IMAGE_URL` can pin the base Armbian image; otherwise the
latest ophub trunk arm64 image is downloaded from GitHub releases.

The GitHub workflow builds the current Gateway x86 and ARM artifacts on main,
tags, or an explicit release dispatch. Pull Requests run only the fast source
gate. Network downloads are pinned in the workflow and are not part of the
hotfix loop.
```

The x86 physical-machine disk image artifacts are written as:

```text
ly-route-bookworm-amd64-4g.img
ly-route-bookworm-amd64-4g.img.sha256
ly-route-bookworm-amd64-4g.img.zst
ly-route-bookworm-amd64-4g.img.zst.sha256
```

The current physical-firmware acceptance build artifacts are:

```text
dist/rootfs-acceptance-fixed/ly-route-rootfs-bookworm-amd64.tar.zst
dist/rootfs-acceptance-fixed/ly-route-rootfs-bookworm-amd64.tar.zst.sha256
dist/disk-image-acceptance-fixed/ly-route-bookworm-amd64-4g.img
dist/disk-image-acceptance-fixed/ly-route-bookworm-amd64-4g.img.sha256
dist/disk-image-acceptance-fixed/ly-route-bookworm-amd64-4g.img.zst
dist/disk-image-acceptance-fixed/ly-route-bookworm-amd64-4g.img.zst.sha256
```

These artifacts were produced by injecting the validated `ly-route-control`
binary into the previously verified VPP runtime image after the DNS policy item
route fix. Static checks confirmed that the image contains the same control
binary hash as the local validation binary, VPP and `vppctl`, SmartDNS, xray,
the `LY_ROUTE_ENABLE_SERVICE_RUNTIME=true` runtime environment, the native VPP
configuration, and the Gateway `192.168.88.1/24` first-boot LAN fallback.
The same physical `amd64` disk image was also boot-tested under ESXi as a validation environment. That validation confirmed the firstboot/networkd fix that writes a runtime LAN `.network` file for the selected interface before reconfiguring it, plus the runtime-plan fix that avoids generating proxy/QoS VPP operations when no explicit proxy or traffic-control configuration exists.

Rootfs tarballs are intermediate build inputs only. Current release names and
upload rules are defined in `.github/workflows/gateway-release.yml`.

## Legacy Rockchip Armbian Helper

Rockchip boot media is board-specific. The supported board manifest is derived from the Rockchip DTS names and boot-flow rules in `VIKINGYFY/immortalwrt`:

```sh
make rockchip-boards
```

Build a Rockchip/Armbian-style image from an `arm64` rootfs and board-specific boot assets:

```sh
sudo ./scripts/build-rockchip-armbian-image.sh \
  --rootfs dist/rootfs/ly-route-rootfs-bookworm-arm64.tar.zst \
  --board friendlyarm_nanopi-r6s \
  --kernel /path/to/Image \
  --initrd /path/to/initrd.img \
  --dtb /path/to/rk3588s-nanopi-r6s.dtb \
  --uboot /path/to/nanopi-r6s-rk3588s-u-boot-rockchip.bin \
  --out dist/rockchip-armbian
```

The script writes an extlinux boot partition, root partition, board metadata under `/etc/ly-route/rockchip-board.env`, and compressed image artifacts named like `rockchip-armv8-friendlyarm_nanopi-r4s-26.06.13-07.24.08.img.gz`. It follows immortalwrt's Rockchip loader placement convention by writing a combined loader blob at sector `64`.

This helper is retained for targeted local experiments only. Production ARM firmware is built by GitHub `.github/workflows/firmware.yml` through the ophub model database and is published in `ly-route-armbian-*` releases.

## Runtime Smoke

Run the non-hardware runtime payload smoke against a generated rootfs before
handing it to hardware or VM validation:

```sh
./scripts/rootfs-runtime-smoke.sh dist/rootfs-full-runtime-current/ly-route-rootfs-bookworm-amd64.tar.zst
```

The smoke verifies the tarball checksum when present, installed package records,
runtime binaries/config files, runtime environment wiring, and systemd unit
references for the Go control plane. By default it expects the full runtime
payloads `vpp`, `ly-route-vpp-apply`, `smartdns`, and `xray`. CI can narrow the check
for partial tar-only smoke builds with `LY_ROUTE_ROOTFS_REQUIRED_PACKAGES` and
`LY_ROUTE_ROOTFS_REQUIRED_FILES`.

If `systemd-nspawn` is unavailable, the script reports that only static rootfs
runtime checks ran. Set `LY_ROUTE_ROOTFS_LIVE_REQUIRED=true` on a capable VM
runner to make missing live-boot tooling fail the smoke gate.

## Runtime Defaults

- Factory default management is the installer-selected interface at `192.168.88.254/24`; the LAN address and WAN are configured by the administrator.
- Factory default enables Kea DHCP on that LAN with pool `192.168.88.100-192.168.88.199`; router and DNS options point to `192.168.88.1`.
- The admin UI is served by nginx from `/opt/ly-route/admin` on HTTPS port `443`; HTTP port `80` redirects to HTTPS.
- On first boot, `/usr/lib/ly-route/firstboot.sh` generates `/etc/ly-route/tls/admin.crt` and `/etc/ly-route/tls/admin.key` when they are absent. The self-signed certificate includes `192.168.88.1`, `127.0.0.1`, and `ly-route.local` subject alternative names.
- `/api/v1/*` is reverse-proxied to the local Go control-plane service at `127.0.0.1:8080`.
- The admin UI is reachable at the saved management address after installation.
- Factory admin login is `admin` / `password`; the first login requires a password change.
- Admin credentials are not hardcoded. Set `LY_ROUTE_ADMIN_USERNAME` and `LY_ROUTE_ADMIN_PASSWORD` in `/etc/ly-route/control-api.env` before enabling authenticated writes. Optional readonly login uses `LY_ROUTE_READONLY_USERNAME` and `LY_ROUTE_READONLY_PASSWORD`.
- `LY_ROUTE_ENABLE_SERVICE_RUNTIME=true` is set through `/etc/ly-route/runtime.env`, so runtime apply/status uses systemd-backed service orchestration.
- `ly-route-runtime-check.service` runs before the control API. The test image keeps `LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=false` so the management plane boots and reports explicit degraded runtime components when VPP, SmartDNS, Kea, xray, or the native PPPoE client are incomplete. Set it to `true` only for production images where all required runtime daemons are present.

## Startup Order and Degraded Health

The packaged appliance starts in this order:

1. `systemd-networkd.service` brings up the factory LAN fallback.
2. `ly-route-firstboot.service` writes the selected LAN/network defaults.
3. Runtime services such as `vpp.service`, `smartdns.service`, `kea-dhcp4-server.service`, `xray.service`, and `ly-route-pppoe@.service` are enabled when their packages are present. Linux firewall and policy-routing interception are not part of the production forwarding path.
4. `ly-route-runtime-check.service` records missing runtime commands or units in `/var/lib/ly-route/runtime-readiness.json`.
5. `ly-route-control-api.service` starts the Go control plane on `127.0.0.1:8080`.
6. `nginx.service` serves `/opt/ly-route/admin` and proxies `/api/v1/*` to the local control API.

The management plane must remain reachable when `LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=false`. In that mode `/api/v1/health` reports dependency states for VPP, SmartDNS, Kea, xray, PPPoE, persistence, and the research-gated transparent proxy handoff instead of hiding unavailable services. Production images can set `LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=true` to fail boot readiness when required runtime daemons are missing.

The fallback can be disabled by changing `/etc/ly-route/appliance.env` in the rootfs:

```sh
LY_ROUTE_MANAGEMENT_FALLBACK=no
```

Create `/etc/ly-route/control-api.env` on the appliance to enable local admin login:

```sh
LY_ROUTE_ADMIN_USERNAME=admin
LY_ROUTE_ADMIN_PASSWORD=replace-this-before-use
LY_ROUTE_READONLY_USERNAME=readonly
LY_ROUTE_READONLY_PASSWORD=replace-this-before-use
LY_ROUTE_SESSION_COOKIE_SECURE=false
```

Runtime service orchestration is enabled in the appliance image. Keep the
commercial readiness gate enabled on production appliances with VPP, SmartDNS,
Kea, xray, and the native PPPoE client installed and managed by systemd:

```sh
LY_ROUTE_ENABLE_SERVICE_RUNTIME=true
LY_ROUTE_COMMERCIAL_RUNTIME_REQUIRED=true
LY_ROUTE_REQUIRED_COMMANDS=vpp,vppctl,/usr/lib/ly-route/vpp-apply,smartdns,kea-dhcp4,xray,/usr/lib/ly-route/ly-route-pppoe-client
LY_ROUTE_REQUIRED_UNITS=vpp.service,smartdns.service,kea-dhcp4-server.service,xray.service,ly-route-pppoe.target
LY_ROUTE_VPP_APPLY_COMMAND=/usr/lib/ly-route/vpp-apply
LY_ROUTE_VPP_COMMAND_MAP=/etc/ly-route/vpp-command-map.json
LY_ROUTE_VPP_RECEIPT=/var/lib/ly-route/vpp-apply-receipt.json
```

The builder installs Debian-packaged runtime dependencies where available
(`apt`, `kea-dhcp4-server`, `ppp`, `python3`, `python3-minimal`,
and `curl`). VPP, SmartDNS, and xray must be provided by `LY_ROUTE_EXTRA_PACKAGES`,
`LY_ROUTE_EXTRA_DEBS_DIR`, or packages created by
`scripts/build-runtime-debs.sh` so the readiness gate can pass.
`scripts/build-runtime-debs.sh smartdns` packages the source-built SmartDNS
binary with `/etc/smartdns/smartdns.conf` and `/etc/smartdns/conf.d/`;
`scripts/build-runtime-debs.sh xray` packages the source-built xray binary with
`/etc/xray/config.json`.
`LY_ROUTE_VPP_APPLY_COMMAND` must point to the commercial VPP adapter that
consumes `/var/lib/ly-route/vpp/operations.json`, resolves each operation through
inline `vppctl_commands` or `/etc/ly-route/vpp-command-map.json`, executes the
mapped `vppctl` commands, and writes `/var/lib/ly-route/vpp-apply-receipt.json`.
Missing command mappings fail the apply instead of silently falling back.

Use `docs/runtime-hardware-validation.md` for the appliance-only validation
checklist covering live VPP, SmartDNS, Kea, xray, PPPoE, VPP-native service
paths, and recovery evidence.

## Gitea Actions

The workflow at `.gitea/workflows/ci.yml` runs the repository verification gate:
Go backend tests, product contract validators, QoS simulator validation, rootfs
scaffold checks, and a smoke rootfs artifact build.

The workflow at `.gitea/workflows/rootfs-image.yml` builds both architectures on
push, pull request, or manual dispatch and uploads rootfs tarballs as Gitea
artifacts. On `amd64` runners it also builds the local `ly-route-vpp-apply`
runtime adapter package and injects it through `LY_ROUTE_EXTRA_DEBS_DIR`; build
SmartDNS and xray with `make runtime-debs` or their individual script targets
before producing commercial images. VPP source packages should be built on a
native worker with the required VPP build dependencies.

To use it:

1. Push this repository to your Gitea instance.
2. Enable Actions for the repository.
3. Attach an Actions runner with enough privileges to run `sudo apt-get` and `mmdebstrap`.
4. Trigger `ci` for validation and `rootfs-image` for full installable rootfs
   artifacts.

No Gitea credentials are embedded in this repository. Remote creation, secrets,
and pushes should be configured outside the source tree.

Example remote setup after you create the empty repository in Gitea:

```sh
git remote add gitea ssh://git@gitea.example.com/your-org/ly-route.git
git push -u gitea main
```

## Validation

Run the scaffold checks without building a full rootfs:

```sh
make ci
make validate-rootfs
```

For CI smoke tests where `mmdebstrap` is unavailable or intentionally bypassed,
set `LY_ROUTE_ROOTFS_ALLOW_TAR_ONLY=1` to build an overlay-only tarball. Scaffold
validation checks scripts, workflow files, Go backend tests, root UI scope, DHCP
defaults, nginx admin access configuration, and the Go control API packaging path.
A complete Debian rootfs build still requires `mmdebstrap`.
