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

## OMO And Agent Routing

- OMO Codex Light is installed. The current session is the lead and owns the
  final diagnosis, source edits, integration, and user report.
- OMO workflow is the default for every task; do not wait for a keyword or a
  UI toggle. Start in LIGHT mode and escalate automatically when the change
  crosses layers, affects security/concurrency, or enters a release decision.
- The API limit is five concurrent requests. Reserve one slot for the lead;
  use at most four child agents, and only for independent write scopes or
  independent evidence collection. A narrow one-file fix stays local.
- Route roles by cost and difficulty: `gpt-5.6-luna` with low reasoning for
  search and inventory; `gpt-5.6-terra` with medium reasoning for ordinary
  implementation, review, and QA; `gpt-5.6-sol` with high reasoning for root
  cause, architecture, migration, and release decisions. `gpt-5.5` is the
  fallback when Sol or Terra is unavailable.
- Use two bounded batches for cross-layer work: first collect independent
  evidence (no speculative edits), then implement disjoint fixes and run one
  focused regression. Do not let multiple agents investigate or edit the same
  failure. A worker must return the first failing layer, changed paths, command
  result, and artifact/source fingerprint.
- Full feature acceptance runs once at batch closeout. UI-only, documentation,
  formatting, or unchanged-path edits do not restart Gateway/Orchestrator
  acceptance. Reuse the retained topology and cache results by source
  fingerprint plus scenario id.
- `ultrawork` and `ulw-loop` are explicit batch modes, not defaults. Do not
  start an autonomous loop for a single fix, and stop when the original
  scenario, applicable smoke, and artifact identity are proven.
- After context compaction, reload this file and the current failure ledger
  only. Search and open the relevant document section on demand; never preload
  the full `docs/` tree or historical acceptance logs.
