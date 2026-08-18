# Development, Hotfix, and Acceptance Workflow

> Current as of 2026-08-18. This is the single entry point for development workflow.
>
> Product scope, architecture, and UI requirements remain valid. Older rules that required full acceptance for every edit, pure-LOC limits, ESXi/VMXNET3 hardware gates, or ISO reinstallation for every fix are retired and must not block daily work.

## Goal

Keep a repair traceable without making every change wait for a release build:

`reproduce -> identify the first failing layer -> edit source -> focused check -> hot deploy -> repeat the same scenario`

Rootfs, ISO, performance, and physical-hardware work starts only for a feature-batch closeout or a release.

## Three Modes

### Daily development and hotfix (default)

1. Reproduce once and save the smallest useful evidence: command, first log error, or client result.
2. Classify the first failing layer: fixture, link/session, control plane, VPP/service, or client. A fixture failure is not a product edit.
3. Edit only the complete current source tree and run `bash scripts/dev-hotfix-check.sh`.
4. Build only the affected binary or plugin on the fixed compiler host. Use
   `scripts/build-hotfix-go.sh` for Go services and
   `scripts/seal-hotfix-artifact.sh` for other freshly built files. Outputs are
   stored under `dist/hotfix/` by source fingerprint, never under a generic
   reusable filename.
5. Use `scripts/hotfix-deploy.sh --manifest ...`. It derives the artifact from
   the manifest and rejects both stale source and SHA-256 mismatches before it
   copies the file, backs up the remote binary, restarts the named service, and
   checks that it is active.
6. Repeat the original UI/API/client scenario and record the result.

The hotfix is complete when the original scenario passes and the applicable core smoke passes: management access, VPP/service observability, and one basic forwarding or orchestration path.

### Feature acceptance batch

Run one batch when a product feature set is ready, not after every small edit. Gateway uses the live batch entry point; Orchestrator validates only interface orchestration, service chains, ordered groups/rules, matching, and bypass. It does not repeat Gateway NAT, DHCP, DNS, or PPPoE checks.

Classify each result as `PASS`, `PRODUCT_FAIL`, `FIXTURE_FAIL`, or `BLOCKED`. Collect independent failures first, then repair them together. The four evidence points for a feature are real UI input, backend persistence, runtime application, and an independent client packet result.

### Release build

After the feature batch passes, build the pinned Bookworm/VPP runtime, rootfs, ISO or ARM installer, upgrade package, checksums, manifests, and installation smoke. Release checks are not hotfix checks; VMXNET3/VFIO, physical PCI, performance, and temperature work is opt-in hardware work.

## Minimum Gates

| Gate | When | Pass condition |
| --- | --- | --- |
| G1 source | Every edit | `git diff --check`, shell syntax, and affected Go packages compile |
| G2 identity | Every hot deploy | Deployed SHA-256 equals the build output and the old file is backed up |
| G3 smoke | Behavioural fix | Original scenario passes and the affected runtime is observable |
| G4 release | Batch closeout/release | Feature batch, artifact chain, and install smoke pass |

Only G1-G3 are used during daily work. Other checks are evidence or diagnosis unless the task explicitly enters release mode.

## Result Classes

- `PASS`: the requested product behaviour is proven.
- `PRODUCT_FAIL`: the first failure is in product source, configuration generation, or runtime.
- `FIXTURE_FAIL`: the PPPoE server, proxy node, DNS authority, client, or lab topology is unavailable.
- `BLOCKED`: an external resource or permission is missing; no product conclusion is claimed.

## Retired Gates

The daily entry point no longer blocks on pure LOC/file-size limits, rootfs/ISO builds, all historical contract validators, VM teardown and recreation, ESXi/VMXNET3/VFIO/IOMMU, performance, 64-byte packets, or physical hardware. Compiler-environment verification and line-ending normalization belong to release/build troubleshooting, not every source edit. Old VPP trees, packages, images, and serial logs are historical evidence only.

Retiring a gate does not remove a product requirement. Gateway and Orchestrator scope is still accepted in feature batches.

## Fixed Entrypoints

```bash
bash scripts/ci-verify.sh
bash scripts/dev-hotfix-check.sh ./internal/runtime/vpp ./internal/httpapi
bash scripts/build-hotfix-go.sh --package ./cmd/gateway-control --name ly-route-control
bash scripts/clean-generated.sh
./scripts/test-gateway-live-batch-acceptance.sh
./scripts/ci-release-verify.sh
```

Any new gate must state the failure it prevents, its phase, the old check it replaces, and its removal condition before it can be added to the daily entry point.

## Context and Documentation Boundary

Start each task with the repository-root `AGENTS.md` and the target source
files. Load the product-boundary, UI, ISO, hardware, or architecture document
only when the task enters that area. Do not read the entire `docs/` tree,
historical ledgers, or archives after every context resume; search for the
specific feature, error, or path and read only the relevant section. Keep long
acceptance logs out of the working context and retain structured results plus
the current failure only.
