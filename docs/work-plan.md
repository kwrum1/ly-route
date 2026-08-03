# LY-Route Dual-Product Work Plan

This is the canonical forward plan. The detailed execution contract is maintained in [Chinese](zh/work-plan.md); the current evidence baseline is in [Implementation Status](implementation-status.md).

The target is two immutable build-time products:

- **Gateway**: a routed LAN/WAN egress appliance with routing, NAT, DNS/DHCP, proxying, traffic control, security, telemetry, and maintenance.
- **Orchestrator**: a transparent, two-arm traffic orchestrator with a configurable management network, paired logical endpoints backed by physical ports or bonds, ordered policy, symmetric service chaining, and failed-node default bypass.

Assigned NICs are probed automatically. The selector chooses the best measured, proven VPP-native high-performance candidate first and probes VPP DPDK only when every native candidate fails. Every active data interface on either product uses one device-wide highest common eligible tier. AF_XDP copy, generic Linux XDP forwarding, AF_PACKET, and Linux forwarding are never compatibility fallbacks. If neither high-performance tier passes, management remains reachable while forwarding stays locked.

Gateway scope fixes WAN groups to primary/backup, weighted, and five-tuple modes with no proxy group members; subscription routes support fixed or health-gated fastest nodes. DNS transparently intercepts every LAN-originated TCP/UDP destination port 53, including clients that manually configure public or ISP DNS addresses, then applies the Gateway DNS policy and selected egress before ordinary PBR. QoS includes user rate limits and internally tuned smart QoS. The detailed [full acceptance matrix](zh/container-network-validation.md) covers every function; no missing, skipped, or mock-only mandatory case may be called accepted.

Current DNS dataplane evidence proves the VPP ACL/ABF interception and VCL adapter for arbitrary IPv4/IPv6 targets over UDP/TCP, original response-source preservation, source-client policy identity, DNS-selected egress priority over ordinary PBR, TTL handling, and fail-closed upstream behavior. Proxy-originated DNS egress and appliance recovery remain mandatory gates.

Current QoS evidence proves both the user VPP policer and the production `ly_route_smart_qos` VPP plugin under two-worker bidirectional saturation. Remaining QoS work is fault/rollback, restart, long-duration, rate/worker matrix, UI operational evidence, and target-hardware qualification. Smart QoS is independent of whether the selected NIC tier is VPP-native or DPDK.

## Delivery sequence

| Wave | Work | Exit gate |
| --- | --- | --- |
| 0 | Freeze the integrated baseline; replace stale documentation and plans | One authoritative baseline and a passing full verification run |
| 1 | Implement measured native-first/DPDK-fallback NIC selection; wire the Orchestrator backend; run Gateway packet E2E | Selector and Orchestrator backend loops are complete; Gateway primary packet path passes |
| 2 | Starting with both login pages, QA and modify only the presentation/interaction changes explicitly requested by the product manager; require full-container approval before the next page | Every page has targeted source changes, desktop/mobile QA, two complete containers, and explicit product approval |
| 3 | Inventory VPP/backend/API capabilities, propose useful extensions, and implement only product-approved candidates | Every approved extension has an end-to-end API/runtime/dataplane/UI/verification closure |
| 4 | Run complete container protocol/packet, fault, recovery, performance, and browser acceptance | Both products pass the full acceptance matrix without mock-only mandatory cases |
| 5 | Build production firmware, validate upgrades/rollback and target hardware, and assemble reproducible release evidence | Signed hardware reports and traceable release artifacts |

The detailed plan defines B0, datapath D1, configuration C1-C2, Gateway G1-G3, Orchestrator O1-O5, UI/evolution stages U1-U2, management M1, and release R1-R3. The [UI and capability-extension plan](zh/ui-rearchitecture-plan.md) uses a strict page-approval loop beginning with both login pages: current-state QA, only the product manager's requested edits, two full-container checks, and explicit approval before moving on. Capability exploration begins only after the presentation phase. Completion requires API, persistence, runtime, datapath, UI, repeatable container packet/fault tests, recovery, artifact, and documentation agreement.

## M1: Shared or Exclusive Management Network

This is a release-blocking cross-product item added after the current audit.

The VPP/LCP runtime slice is implemented and container-proven for idempotent shared creation, Linux management access, concurrent LAN-to-WAN forwarding, and exclusive cleanup. Gateway and Orchestrator UI/API readback are implemented; Orchestrator topology ownership is mode-aware and allows the management interface only as LAN. Appliance reboot/recovery, transactional management-loss rollback, and physical-NIC evidence remain release-blocking.

- Add a typed `management_network` model with `interface`, `mode` (`exclusive` or `shared_lan`), `address`, `prefix`, and optional `gateway`.
- In `exclusive` mode, the physical interface remains Linux-owned and is excluded from VPP/native/DPDK data assignments.
- In `shared_lan` mode, the management address is assigned to the LAN logical interface. It may be the LAN gateway address, for example `10.10.10.254/24` on `eth2`; the same interface continues to carry LAN data traffic.
- Validate address/prefix/gateway family, prevent overlap with WAN and other LAN networks, reject a management address on a proxy or WAN group, and protect the last reachable management endpoint during preview, apply, rollback, and recovery.
- Replace the current blanket physical-interface exclusion with mode-aware ownership. Shared LAN remains a data interface while management control traffic is protected by VPP ACL/control-plane safeguards. Do not introduce nftables, TProxy, Linux policy-routing, or low-performance fallback.
- Complete transactional management-loss rollback, reboot recovery, two-port appliance, and physical-NIC tests for both modes before release.

## Orchestrator Product Boundary

The Orchestrator is an inline traffic steering product, not a second Gateway.

- It owns one unique logical LAN side and one unique logical WAN side. Each side
  may be one physical interface or one aggregation group; aggregation is allowed
  only for these two logical sides.
- The topology must establish LAN and WAN before an orchestration group can be
  created. Every group contains exactly two physical ports, one LAN-facing and one
  WAN-facing, with a custom name. Groups cannot contain bonds, logical LAN/WAN
  interfaces, or a third port.
- The navigation order is system overview, traffic overview, online users, top
  connections, NIC settings, orchestration settings, traffic orchestration,
  traffic control, security, IP management, system users, configuration, and
  runtime maintenance. Top domains are removed.
- Gateway-only concerns are excluded from the Orchestrator: NAT, DNS policy,
  DHCP, WAN groups, port mapping, domain management, proxy egress, and Gateway
  routing. The Orchestrator keeps traffic control and security control.
- Traffic orchestration uses ordered strategy groups and ordered rule details.
  Group order is the primary precedence and rule sequence is the secondary
  precedence. Unmatched traffic follows the default path to LAN. A rule may select
  any eligible orchestration group; group reorder is a transactional operation
  with preview, apply, readback, rollback, and packet evidence.
- Orchestrator Traffic Control must provide editable, typed rate policies with
  API persistence, transactional VPP apply/readback, rollback, and packet
  evidence. It must not expose Gateway DNS/NAT semantics or use the Gateway
  built-in Smart QoS status card as a substitute for rate-policy management.
