# Ly Route Working Rules

Read this file first. Do not preload the whole `docs/` tree.

## Default Repair Loop

1. Reproduce once and identify the first failing layer.
2. Change only the owning source files.
3. Run `bash scripts/dev-hotfix-check.sh <affected-go-packages>`.
4. Build or seal one affected artifact under `dist/hotfix/`.
5. Deploy only through `scripts/hotfix-deploy.sh`; an artifact without a
   matching source fingerprint manifest must be rejected.
6. Repeat the same user scenario, then run the smallest applicable smoke test.

Do not build a rootfs or ISO during a hotfix. Do not use files from
`.codex-tmp`, old rootfs trees, previous `dist/` directories, or copied remote
binaries as current evidence.

## Read Documents On Demand

- Process or gate change: `docs/zh/development-workflow.md`
- Product behavior or acceptance scope: `docs/zh/product-functional-boundary.md`
  and `docs/zh/product-functional-qa.md`
- UI task: `docs/zh/ui-design.md`
- ISO/release task only: `docs/zh/iso-packaging-and-acceptance.md`
- Hardware task only: `docs/zh/runtime-hardware-validation.md`
- Architecture change only: `docs/zh/architecture.md`

Historical ledgers and `docs/archive/` are evidence, not default context.
Search for the specific feature or error before opening a long document.

## Worktree And Artifacts

- Keep source changes in Git commits; do not use stash as long-term storage.
- Run `bash scripts/clean-generated.sh --apply` to remove only allowlisted,
  reproducible outputs.
- Every hotfix artifact must have a manifest containing its source fingerprint
  and SHA-256. Rebuild after any source change; deployment refuses a mismatch.
- Release artifacts are created only by the release workflow from a committed
  revision. Hotfix files are never promoted directly into an ISO.
