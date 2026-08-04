# Release and CI

The authoritative workflow is
[`gateway-release.yml`](../../.github/workflows/gateway-release.yml). It is a
new pipeline based on the current source tree and does not depend on the old
Gitea workflow.

## Required Artifacts

On pull requests and `main`, the workflow verifies the source and uploads:

- x86-64 4 GiB BIOS/UEFI burn image (`.img.zst`);
- x86-64 Gateway upgrade package;
- ARM64 Armbian Bookworm one-click installer;
- ARM64 Gateway upgrade package.

On a `v*` tag, the release job also publishes these files to GitHub Releases,
creates a combined `SHA256SUMS`, and emits build provenance attestations.

## Build Gates

The verification job runs the frontend bundle build, all Go tests, product
builder contracts, rootfs scaffold validation, shell syntax checks, and a
private-material scan. Artifact jobs run only after this gate passes.

x86 uses FD.io VPP Debian packages. ARM64 uses the native GitHub ARM64 runner
and builds VPP from the pinned source commit. The ARM checkout also fetches the
matching VPP release tag and verifies `git describe`, because VPP's CMake
version metadata requires tag history.

SmartDNS is built in a root-owned `mmdebstrap` chroot and its output ownership
is returned to the runner before subsequent unprivileged package steps. The
same ownership boundary is applied after rootfs creation so upgrade packages
cannot fail when a root build creates `dist` directories.

The data-plane policy is common across x86 and ARM: the highest common
qualified VPP native path is preferred, DPDK is the fallback, and forwarding
is locked when no qualified path is available. The Armbian installer preserves
the board kernel, DTB, bootloader, partitions, and `/boot`.
