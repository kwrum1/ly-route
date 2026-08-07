# LY-Route

LY-Route is a dual-product network appliance codebase:

- **Gateway** is a routed egress gateway for LAN/WAN, multi-WAN, routing/NAT, DNS/DHCP, proxying, traffic policy, security, telemetry, and maintenance.
- **Orchestrator** is a transparent two-arm traffic orchestrator with paired physical ports, ordered policy, and symmetric service chaining.

Products are selected at build time and ship with separate profiles, services, resources, configuration, UI, and artifacts. They cannot be converted at runtime.

The appliance automatically probes assigned NICs and selects the highest-ranked proven VPP-native high-performance path. VPP DPDK is the controlled fallback when no native candidate passes. If neither tier satisfies the performance and safety gates, the management plane remains available while forwarding stays locked.

Documentation: [English](docs/README.md) | [简体中文](docs/zh/README.md)

Start with the [implementation inventory](docs/implementation-status.md), [functional boundary](docs/product-functional-boundary.md), and [work plan](docs/work-plan.md). Build and image details are in [Root Filesystem Image](docs/rootfs-image.md).
