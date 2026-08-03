# LY-Route Whitepaper

## Product thesis

LY-Route applies one local-first control-plane foundation to two deliberately separate appliances. Gateway owns routed egress services; Orchestrator owns transparent, policy-driven service chaining. This separation prevents a broad gateway schema from leaking into a narrowly defined orchestrator and keeps installation, upgrades, failures, and operator expectations deterministic.

## Gateway

Gateway targets branch and edge egress. It manages LAN/WAN and three WAN-group modes, routing/NAT, DNS/DHCP, fixed/fastest subscription proxy egress, two-layer QoS, security, telemetry, audit, rollback, and maintenance. Proxy logical WANs cannot join WAN groups. LAN TCP/UDP 53 is intercepted locally; ordered DNS policy selects fixed egress independently and overrides ordinary PBR for answer-attributable traffic until TTL expiry. Runtime status is read back from the enforcing services.

## Orchestrator

Orchestrator has an independent or shared-LAN Linux management address and one logical LAN/WAN transit path. Logical LAN and WAN may each use one physical port or one bond. Each orchestration group has exactly two unused physical ports: one `lan_facing` and one `wan_facing`; group bonds are prohibited. Ordered policy first applies security denial, then traffic control, then selects `via`, `direct`, or `drop`. A `via` return flow must traverse the exact selected service instances in reverse order; failed service nodes are bypassed by default while the remaining chain stays symmetric.

It is not a second gateway: NAT, DNS/DHCP, proxy gateway, domain objects, OAF/DPI/application identification/user-behavior auditing, bridge product mode, cloud management, HA, and VRRP are outside its scope.

## Technical direction

The Go control plane validates product-specific intent, stores versioned state, compiles a candidate, applies it transactionally, verifies observed state, and commits or rolls back. A shared selector probes each assigned NIC, chooses the best proven VPP-native high-performance candidate, and uses VPP DPDK only when no native candidate passes. Every active data interface on the appliance uses one highest common eligible tier. This is a high-performance fallback, not compatibility degradation. If neither tier passes, management remains reachable and forwarding remains locked with diagnostics; copy-mode or Linux forwarding paths are not used.

## Delivery standard

Schema and UI presence are not completion. Full software acceptance follows the [complete matrix](zh/container-network-validation.md) across DNS/DHCP, routing/NAT, PPPoE, WAN/proxy failure, saturated-link QoS, security/telemetry, symmetric chains, bypass, browser, and system artifacts. Release evidence additionally covers target hardware, product isolation, and independent x86_64/ARM64 install, upgrade, and rollback.
