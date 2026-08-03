# Product Verification Contract

Verification is evidence-based. The live inventory is [Implementation Status](implementation-status.md), the normative scope is [Product Functional Boundary](product-functional-boundary.md), and remaining work is in the [Work Plan](work-plan.md).

## Required gates

1. **Static and unit gates**: formatting, lint, schema, backend unit/integration, frontend unit, profile isolation, and OpenAPI contract checks.
2. **UI gates**: U1/U2 design-system migration, product-specific menus/routes, successful and failed operations, observed-state refresh, accessibility, desktop/tablet/mobile viewports, and visual regression evidence.
3. **Configuration gates**: typed `any`, host, CIDR, range, and applicable object references survive save/export/import/apply/readback; object rename/delete and invalid input behave deterministically.
4. **Runtime gates**: transactional apply, rollback, restart reconciliation, drift detection, and management availability when the datapath is locked.
5. **Full functional gates**: follow the [full acceptance design](zh/container-network-validation.md) and trace every product function to executable assertions. Gateway includes default TCP/UDP 53 interception, fixed DNS egress and DNS-over-PBR TTL behavior, complete DHCP, routing/NAT, PPPoE, WAN modes, proxy, QoS, security, telemetry, UI, and system artifacts. Orchestrator includes complete endpoints, policy, symmetric chains, bypass, telemetry, UI, and artifacts. A missing, skipped, or mock-only mandatory case blocks full acceptance.
6. **Artifact gates**: x86_64/ARM64 clean install, upgrade, rollback, uninstall, manifest, hash, product isolation, and reproducibility.
7. **Hardware gates**: device-wide highest common tier, native-first/DPDK-fallback selection, throughput, latency, loss, CPU/memory cost, thermal behavior, reboot recovery, rollback, and unsupported-hardware diagnostics. Container functional acceptance does not replace hardware release evidence.

Fastest-proxy evidence includes proxy-protocol health, rolling ping RTT, stable better-node selection, hysteresis, failure failover, and recovery qualification. Smart-QoS evidence compares unloaded and bidirectionally saturated RTT/p95/p99, jitter, loss, throughput, host/flow fairness, CPU, and memory against locked per-platform baselines. Orchestrator bypass evidence proves the failed node sees no new traffic, the remaining reverse chain is exact, and no healthy nodes results in direct forwarding without loops or half-applied state.

DNS evidence proves arbitrary client TCP/UDP 53 is intercepted; DNS-selected WAN A overrides PBR-selected WAN B for the query and answer-attributable traffic until TTL expiry; proxy DNS requests themselves enter the proxy; unmatched or unavailable selections return NODATA without fallback.

Evidence must identify product, architecture, commit, artifact hash, topology, commands, expected result, actual result, and retained logs. A check is not complete when it depends on an unregistered CI runner, unavailable target hardware, or unexecuted packet path.
