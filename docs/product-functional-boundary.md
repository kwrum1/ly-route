# Product Functional Boundary

This document is the normative scope contract for LY-Route. Status and sequencing are maintained separately in [Implementation Status](implementation-status.md) and the [Work Plan](work-plan.md).

## Shared rules

- Gateway and Orchestrator are distinct build-time products with separate artifacts, profiles, databases, migrations, API allow-lists, UI routes, services, and upgrades.
- Runtime conversion and cross-product configuration import are prohibited.
- Shared infrastructure is permitted only when product capability tests prove that no excluded service, API, resource, page, or migration enters an artifact.
- The shared selector probes assigned NICs and chooses the best proven VPP-native high-performance candidate; VPP DPDK is used only when no native candidate passes. Every active data interface on an appliance uses the device-wide highest common eligible tier; neither product may mix tiers by port or orchestration group. If both tiers fail, forwarding stays locked while management remains reachable.
- AF_XDP copy, generic Linux XDP forwarding, AF_PACKET, kernel forwarding, and other compatibility degradation are excluded.

## Gateway scope

Gateway is a routed LAN/WAN egress appliance.

Included capabilities: interface and WAN-group management, routing, NAT, DNS, DHCP, proxying, PPPoE, traffic policy/QoS, security policy, telemetry, runtime diagnostics, backup/restore, upgrade, and maintenance.

- WAN groups support only primary/backup, weighted load distribution, and five-tuple load distribution. Proxy logical WANs cannot join a WAN group.
- Gateway intercepts LAN TCP/UDP port 53 into local SmartDNS by default. DNS is an independent ordered first-match engine that selects a fixed WAN/upstream/proxy, fixed answer, or NODATA. DNS-selected egress overrides ordinary policy routing, and traffic attributable to an answer inherits that egress until TTL expiry. An unmatched rule or unavailable matched egress returns NODATA without falling through to a default upstream, lower rule, or ordinary route.
- A subscription-backed proxy route can select a fixed node or the fastest node. Fastest-node selection gates actual proxy health first, ranks primarily by rolling ping latency, and includes hysteresis, minimum dwell, failure failover, and recovery qualification.
- The dedicated proxy virtual interface is full-proxy by default. VPP alone owns direct-versus-proxy and domestic-versus-external policy; Xray only transports traffic already selected by VPP and must not duplicate business routing or DNS policy. Linux nftables is limited to delivering that dedicated service-interface traffic to Xray.
- QoS has two independent layers: user-configurable rate limiting and internally integrated smart QoS whose low-level algorithm parameters are not user-editable. Smart QoS targets CAKE/SQM-like fair queueing, AQM, and low loaded latency without bypassing the selected VPP high-performance path.

Every configuration surface must persist independently, apply transactionally, and report observed runtime status.

## Orchestrator scope

Orchestrator is a transparent traffic orchestration appliance with an exclusive or shared-LAN Linux management address.

- One logical WAN side and one logical LAN side.
- Logical WAN and logical LAN may each be backed by one physical port or one bond. Aggregation is allowed only for these two roles.
- Each orchestration group contains exactly two distinct, unused physical ports: one `lan_facing` and one `wan_facing`. Bonds and bridges are prohibited inside a group.
- Physical ports and bond members cannot overlap or be reused across LAN, WAN, management-exclusive mode, or orchestration groups. Shared management may overlap only the logical LAN port or LAN bond.
- Ordered policy resolves to `via`, `direct`, or `drop`.
- Security deny is evaluated before traffic control; traffic control is evaluated before chain selection.
- A `via` forward flow traverses its ordered service chain; the return flow traverses the exact same instances in reverse order.
- A failed service node is bypassed by default: it is atomically removed from the effective chain, the remaining forward and strict-reverse chains stay symmetric, and no healthy nodes is equivalent to `direct`. Recovery must pass health gates before rejoining.
- All active logical data interfaces, bond members, and symmetric chains must pass the automatic datapath gates before forwarding opens. The whole appliance uses its highest common eligible tier; mixed tiers are prohibited.

## Configuration boundary

- Policy address matches use explicit typed `any`, host, CIDR, range, and stable object references.
- Gateway supports IP groups and domain groups only where the policy/runtime has real domain semantics.
- Orchestrator supports IP groups but no domain groups.
- WAN/LAN interface addresses are host prefixes, not policy selectors; DHCP pools are ranges; translated NAT endpoints follow resource-specific single-address/interface constraints.

Explicitly excluded: NAT, DNS/DHCP server, proxy gateway, Top Domains/domain objects, OAF/DPI/application identification/user-behavior auditing, bridge mode, cloud management, HA, and VRRP.

## Release boundary

Each product must ship independently for x86_64 and ARM64 and pass the complete matrix in the [Chinese Full Functional Acceptance Design](zh/container-network-validation.md): contract/state, container real protocols and packets, browser, system artifact, and hardware gates. Gateway covers DNS/DHCP, routing/NAT, WAN, PPPoE, proxy, QoS, security, telemetry, and maintenance; Orchestrator covers its complete policy, endpoint, symmetric-chain, bypass, telemetry, and operations scope. A feature is not shipped solely because its schema, route, mock, page, or unit test exists.
