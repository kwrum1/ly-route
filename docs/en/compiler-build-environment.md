# Compiler Toolchain and Line-ending Boundary

This rule fixes the compiler environment for release builds and explicit build
investigations. It is not a prerequisite for every hotfix; use the development
workflow for daily work.

## Fixed baseline

The compiler host and CI use the same baseline file:
`config/build/compiler-toolchain.env`.

The current baseline is:

- Go `go1.24.4`
- Linux `x86_64` compiler host; Debian Bookworm is the rootfs target
- `GOTOOLCHAIN=local`; Go must not download or switch toolchains automatically
- `CGO_ENABLED=1`, `gcc`, and `g++`
- `GOPROXY=https://goproxy.io,direct` and `GOSUMDB=off`
- UTC build time and LF text files

Run this before a release build or a dedicated build investigation:

```bash
bash scripts/verify-compiler-environment.sh
```

If a release check fails, stop the build. Do not hide environment drift by trying a random
Go version. A toolchain upgrade must update the baseline, CI, and compiler host
together and then be verified separately.

## Windows to Linux synchronization

`.gitattributes` declares `eol=lf` for source, scripts, configuration, and
documentation. SFTP, archives, and temporary copy paths do not necessarily apply
Git attributes, so normalize the complete source tree after synchronization:

```bash
bash scripts/normalize-source-line-endings.sh /root/ly-route
bash scripts/verify-compiler-environment.sh
```

The normalizer only handles the listed text extensions and excludes `.git`,
`dist`, build caches, downloaded VPP upstream sources, and binary artifacts. It
does not modify firmware images, databases, or third-party binaries.

## Required synchronization order

1. Verify that `/root/ly-route` is the intended repository path.
2. Replace the complete `backend/` tree; never synchronize only
   `internal/runtime/vpp/`.
3. Normalize line endings.
4. Run the compiler environment gate.
5. Run `go test -run '^$' ./...` inside `backend/`.
6. Build `./cmd/gateway-control` or the requested target.

Never compile directly from a stale VPP subdirectory. When many “undefined
type” errors appear, check source-tree completeness, Go version, and CRLF first;
only then treat the output as a real code defect.
