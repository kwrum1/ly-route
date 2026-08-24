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

- Process or gate change: `docs/product-functional-qa.md`
- Product behavior or acceptance scope: `docs/whitepaper.md`
  and `docs/product-functional-qa.md`
- ISO/release task only: `docs/release-and-installation.md` and
  `docs/rootfs-image.md`
- Hardware task only: `docs/implementation-status.md`
- Architecture change only: `docs/architecture.md`

Git history is the historical ledger. Search for the specific feature or error
before opening a long document.

## Worktree And Artifacts

- Keep source changes in Git commits; do not use stash as long-term storage.
- Run `bash scripts/clean-generated.sh --apply` to remove only allowlisted,
  reproducible outputs.
- Every hotfix artifact must have a manifest containing its source fingerprint
  and SHA-256. Rebuild after any source change; deployment refuses a mismatch.
- Release artifacts are created only by the release workflow from a committed
  revision. Hotfix files are never promoted directly into an ISO.

## OMO And Agent Routing

Global OMO defaults, model routing, and the five-request budget live in the
user-level `~/.codex/AGENTS.md`; do not duplicate them here. This repository
adds only its local constraints: the lead owns integration, child scopes must
not overlap, and cross-layer work follows the two-batch workflow in
`docs/product-functional-qa.md`.

After compaction, reload this file, the current failure ledger, and only the
matched workflow/source section. Do not preload the full `docs/` tree or old
acceptance logs.
