# Development and Build Source Boundary

This rule prevents stale compiler directories from being mistaken for current
source and producing misleading missing-type errors.
It applies to compiler synchronization and release builds, not every hotfix;
use the development workflow for daily work.

## Canonical source

- The Gateway backend is built only from the complete repository `backend/`
  directory.
- Compiler synchronization replaces the complete `backend/` tree. It must not
  update only `backend/internal/runtime/vpp/` or another subset.
- `backend/internal/runtime/vpp/` is part of the current Ly Route backend. It is
  not an independent Go module and must not be compiled by itself.

## Upstream VPP source

- `vpp-master/`, `govpp-master/`, and `build/vpp-*` are temporary upstream
  source or build caches, not Ly Route backend source.
- An upstream directory whose commit identity cannot be verified is stale and
  must be removed and fetched again.
- VPP package builds must set `LY_ROUTE_VPP_SRC` explicitly. Build scripts must
  not silently fall back to a repository-local stale directory.

## Compiler synchronization

1. Verify that the destination is the compiler repository's complete
   `backend/` directory.
2. Remove the old `backend/` and synchronize the current complete tree.
3. Run `bash scripts/normalize-source-line-endings.sh /root/ly-route`.
4. Run `bash scripts/verify-compiler-environment.sh` to enforce the pinned Go
   toolchain and host environment.
5. Run `go test -run '^$' ./...` inside `backend/` to compile every package.
6. Build `./cmd/gateway-control`; never build from a VPP subdirectory.
7. Remove temporary upstream VPP trees and plugin build caches after use.

If the destination contains only a subset such as `internal/runtime/vpp`, the
sync has failed. Replace the complete tree instead of copying individual files
until errors disappear.

See [`compiler-build-environment.md`](compiler-build-environment.md) for the
toolchain, CRLF/LF, and Windows-to-Linux synchronization boundary.

## Local Retention Boundary

A developer workspace may retain only the authoritative `ly-route-github/`
worktree, the small SSH/synchronization/ESXi tools, and the fixtures plus one
SHA-256-identified runtime snapshot for the active acceptance round.

ISO/IMG/QCOW2/VMDK images, DEB/SO files, upgrade and rootfs archives, QEMU
installers, downloaded VPP or Armbian trees, compiler caches, `build/`,
`dist/`, `runtime-debs/`, `.codex-build/`, `.tmp*/`, demo `artifacts/`, old
serial logs, screenshots, CI logs, remote-edit copies, and version-suffixed
repair binaries are reproducible and must be removed after use. Do not retain
multiple `fix`, `v2`, `final`, or `bootstrap` variants.

Only one rollback snapshot may be exported directly from the tested system.
It records SHA-256 values for the control program, frontend assets, and every
Ly Route VPP plugin, and is replaced after the next version passes acceptance.

Large third-party firmware caches must never enter Git history. CI downloads
them by pinned version and checksum or uses the CI cache service. If old Git
history already contains such firmware, do not rewrite shared history while
the worktree has uncommitted source; local shallow metadata may be used when
it points to the exact same remote commit.
