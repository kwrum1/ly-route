# LY-Route Architecture

LY-Route produces two separate appliance products from shared source. Product selection is fixed at build/install time; Gateway and Orchestrator do not convert into each other at runtime and do not share configuration databases.

## Product architecture

| Dimension | Gateway | Orchestrator |
| --- | --- | --- |
| Purpose | Routed egress gateway | Transparent traffic orchestration |
| Data interfaces | LAN and WAN interfaces/groups | Exactly two logical interfaces per orchestration group: `lan_facing` and `wan_facing`; either endpoint may be a physical port or bond |
| Management | Control plane on the appliance | Separate Linux management interface |
| Core behavior | Routing, NAT, DNS/DHCP, proxying, traffic control, security | Ordered policy; `via`, `direct`, `drop`; forward chain and exact reverse chain |
| Excluded scope | Runtime product conversion | NAT, DNS/DHCP, proxy, Top Domains/domain objects, OAF/DPI/application identification/user-behavior auditing, bridge, cloud, HA/VRRP |

Shared code may provide authentication, configuration transactions, auditing, telemetry primitives, packaging, and UI infrastructure. Every product artifact still has an explicit service, API, resource, route, database, and migration allow-list.

## Runtime layers

1. **Product UI and API** validate intent and expose actual status.
2. **Independent persistence** stores versioned product configuration and migrations.
3. **Transaction coordinator** preflights, renders a candidate, applies it, verifies runtime state, and commits or rolls back.
4. **VPP datapath** performs forwarding through an automatically selected native or DPDK attachment and exports counters and health.
5. **Reconciliation and telemetry** compare desired and observed state after changes and restarts.

The control plane must never report a draft as applied state. Partial datapath application is a failure and must be rolled back or locked for repair.

## Datapath policy

Datapath selection is automatic and evidence-driven for every assigned data NIC. The policy order is:

1. Probe all approved VPP-native high-performance candidates, initially RDMA DV and VPP AF_XDP zero-copy, then select the best passing native candidate using attachment proof and measured packet rate, latency, loss, CPU cost, and NUMA locality.
2. If no native candidate passes, probe VPP DPDK as a controlled high-performance fallback. DPDK must pass plugin/package, PCI ownership, IOMMU/VFIO, hugepage, queue/RSS, NUMA, attach/detach, traffic, and rollback gates.
3. If neither tier passes, keep the Linux management plane reachable and lock forwarding with per-interface diagnostics.

Policy tier wins over score: DPDK is not selected while an eligible native candidate exists. AF_XDP copy mode, generic Linux XDP forwarding, AF_PACKET, kernel IP forwarding, and other compatibility paths are never candidates. Static PCI/driver matching is only a hint; selection requires a fresh runtime proof and is reconciled after reboot, driver/firmware change, NIC replacement, or topology change.

Both products use this selector and select one device-wide highest common eligible tier across every active data interface, including bond members. Gateway cannot mix tiers by WAN/port and Orchestrator cannot mix tiers by group/port. Loss of common eligibility triggers a transactional device-wide reselection; no common eligible tier locks the datapath.

Gateway intercepts LAN TCP/UDP 53 into SmartDNS before ordinary PBR or generic proxy capture. Ordered first-match DNS policy chooses a fixed WAN/upstream/proxy, fixed answer, or NODATA. The DNS request follows that selection, and answer IPs receive client/domain/egress bindings that override ordinary PBR until TTL expiry. Unmatched queries and matched-but-unavailable egresses return NODATA without lower-rule, default-upstream, or ordinary-route fallback. DNS rules do not directly own NAT or generic next-hop fields, and port-53 interception does not imply DoH/DoT decryption.

Gateway WAN groups are limited to primary/backup, weighted distribution, and stable five-tuple distribution; proxy logical WANs are never group members. Subscription-backed policy routes select either a stable fixed node ID or a fastest-node pool. Fastest selection verifies real proxy protocol health before ranking primarily by rolling ping RTT and uses tolerance, minimum dwell, consecutive failure/recovery thresholds, and explicit switch reasons. Existing healthy sessions are not interrupted by default.

Gateway QoS has user-configurable rate limiting plus an internally tuned smart-QoS layer. Smart QoS targets CAKE/SQM-like shaping, per-flow/host fairness, and AQM under saturated uplink and downlink, but does not expose low-level queue parameters and must not downgrade traffic into ordinary Linux forwarding merely to use a Linux qdisc.

An Orchestrator group has two non-overlapping logical endpoints, each backed by a physical port or bond. Failed service nodes are bypassed by default by atomically recompiling the remaining forward chain and exact reverse chain; no healthy nodes means `direct`. Recovery requires consecutive health and bidirectional packet-flow proof.

The [full acceptance design](zh/container-network-validation.md) traces every normative function to executable contract, packet/protocol, browser, system-artifact, and hardware evidence. Linux-container topology includes clients, distinct DNS upstreams and an attempted external-DNS bypass, DHCP, WAN peers, PPPoE server, proxy nodes, traffic generators, and orchestration service nodes. Target hardware remains mandatory for NIC attachment, throughput, cost, temperature, and driver release evidence.

## Configuration semantics

Policy fields use typed values rather than ambiguous free text: explicit `any`, host, CIDR, continuous range, and stable object references. Gateway may use IP/domain groups where the target policy has valid domain semantics; Orchestrator uses IP groups and retains its no-domain-object boundary. Interface addresses remain host prefixes and never accept `any`, ranges, or groups. See the [Configuration Expression Model](configuration-model.md).

## Current implementation boundary

Gateway has the broader end-to-end control-plane and UI surface. Orchestrator already has product isolation, topology/group models, policy compilation, symmetric-chain primitives, telemetry code, and foundational pages, but its full transaction/runtime/UI loop remains planned work. See [Implementation Status](implementation-status.md) and the [Work Plan](work-plan.md).
