# LY-Route Current Implementation Inventory

Updated: 2026-08-04
Audited branch: `main`
Current commit: `29e95b1` (GitHub production pipeline and current Gateway source)

The public repository now has a newly designed release workflow. Source and
product verification is a required gate before x86 or ARM artifacts start.
The latest artifact run is still building; this document must not be read as
hardware certification or as a claim that a physical appliance has passed.

## Release position

LY-Route is two immutable build-time products: an egress Gateway and a transparent Traffic Orchestrator. The current integration passes the `33/33` real protocol, packet, production-runtime, negative-contract, dataplane-contract, and browser container subset. The full release registry records `37/46` scenarios passed (`80.4%`). This is meaningful integration evidence, but it is not a production-release claim: Gateway security fault, telemetry, operations/UI, artifact, ARM64, and both products' target-hardware scenarios remain open.

## Shared platform

- Product-specific API, persistence, rootfs, service, resource, and frontend isolation is implemented.
- Authentication, roles, audit redaction, snapshots, import/export, and cross-product guards are implemented.
- Management supports `exclusive` and `shared_lan`. Shared mode creates a stock VPP LCP host interface so the management IP may share the logical LAN physical port while ordinary forwarding remains in VPP. Real namespace evidence covers management access, concurrent forwarding, idempotence, and cleanup; appliance reboot and management-loss rollback remain gates.
- All active data ports use one highest common qualified dataplane tier. Measured VPP-native AF_XDP zero-copy or RDMA-DV is preferred. Transactional VPP DPDK/VFIO is the fallback when native candidates fail. Linux forwarding, AF_PACKET, generic XDP, nftables TProxy, and Linux policy-routing interception are not production fallbacks.
- The custom `ly_route_smart_qos` VPP plugin is packaged and runtime-qualified independently of the NIC tier. It uses one multiworker scheduler, exact five-tuple flow queues, host/flow DRR, CoDel AQM, and token-bucket shaping.

## Gateway

Container-proven paths include arbitrary-target IPv4/IPv6 TCP/UDP 53 interception, source identity and original DNS response-source preservation, DNS policy priority over ordinary PBR, fail-closed DNS behavior, SmartDNS TTL handling, Kea DHCP, PPPoE PAP/CHAP with IPCP/IPv6CP, NAT44, security ACL, VPP MACIP binding/spoof rejection/threat-list blocking and generation deletion recovery, protocol-aware VPP SYN/UDP/ICMP/ICMPv6 threshold enforcement and alert counters, WAN primary/backup and load-balancing behavior, user policers, proxy fastest-node failover, shared-LAN management, and both browser suites.

The two QoS layers are now distinct and active:

- User-configurable VPP policer limits.
- Immutable internal VPP Smart QoS. The two-worker packet test measured approximately 1.919 Mbps at a 2 Mbps setting, equal aggregate host shares in both directions, and maximum loaded ping of 33.5 ms upload and 38.5 ms download.

The Gateway UI now accepts explicit Smart QoS download bandwidth on LAN and upload bandwidth on WAN. Values are mandatory and typed; the runtime locks instead of guessing link speed.

Open Gateway gates include proxy-originated DNS egress coverage, Smart QoS long-run and reboot evidence, security telemetry/audit and fault rollback, complete maintenance workflows, all-page responsive UI acceptance, ARM64 artifacts, appliance upgrade/recovery, and target-NIC performance.

## Orchestrator

The Orchestrator owns one unique logical WAN and one unique logical LAN. Each may be a physical port or a bond. An orchestration group is created only after WAN/LAN exist and contains exactly two unused physical ports: one LAN-facing and one WAN-facing. Groups cannot use bonds.

Implemented and container-proven behavior includes authenticated topology persistence, ordered strategy groups and rules, symmetric VPP ACL/ABF service chaining, default forwarding to LAN, failed-node bypass, recovery, production-binary runtime readback, snapshot restore, restart recovery, physical and bond packet paths, product-specific browser flows, and shared-LAN management. Negative contracts reject cross-product resources, domains, invalid ownership, bad references, loops, unauthorized writes, and forged imports without changing persistent state.

The Orchestrator excludes NAT, DNS, DHCP, PPPoE, WAN groups, port mapping, domains, proxy egress, and Top Domains. Traffic Control uses the neutral `/api/v1/flow-control/policies` resource, typed rate-rule editing, runtime apply, and API readback; the Gateway alias and built-in Smart QoS status are unavailable to Orchestrator. Physical service-chain plus policer traffic and bond direct/drop/security/traffic-control paths now have packet evidence. Remaining gates include combined fault rollback, long-run reconciliation, artifacts, ARM64, and target hardware.

## Verification baseline

- `scripts/ci-verify.sh`: passed before the latest status update.
- `scripts/run-container-acceptance.sh`: `33/33` passed on 2026-08-01.
- `.sisyphus/full-acceptance/validate_full_acceptance.py`: `37/46` passed (`80.4%`).
- Smart QoS: production plugin build/package/readback and two-worker bidirectional packet test passed.

The authoritative forward plan is [work-plan.md](work-plan.md). The product-manager view is [zh/product-manager-status-report.md](zh/product-manager-status-report.md).
