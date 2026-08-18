# Product Functional Acceptance Contract

> This document defines feature-batch acceptance, not a gate for every commit. Daily work follows [Development, Hotfix, and Acceptance Workflow](development-workflow.md).

## Pass Conditions

A feature is marked passed in a feature batch only when all four points exist:

1. An administrator entered or changed the setting in the real UI.
2. The backend received, persisted, and read back the final configuration.
3. VPP or the related service applied it and exposes observable runtime state.
4. An independent client observed the expected result with real packets.

A small fix does not require a new ISO or a full product rerun. Repeat the original scenario and the affected core smoke; run the complete scope only at batch closeout.

## Result Classes

| Result | Meaning | Action |
| --- | --- | --- |
| `PASS` | Product behaviour is proven by the four points | Keep evidence |
| `PRODUCT_FAIL` | The first failure is in product code or configuration generation | Repair source and hot-deploy |
| `FIXTURE_FAIL` | A test server, client, proxy node, or topology failed | Repair the fixture, not the product |
| `BLOCKED` | An external resource or permission is missing | Record it; do not rerun blindly |

## Gateway Batch Scope

Cover the designed Gateway features: management and interface roles, PPPoE, LAN/DHCP, IPv4/full-cone NAT, port mapping, multi-WAN groups, policy routing, transparent DNS interception and DNS policy, proxy WAN, GeoIP/GeoSite, IPv6 PD/RA, rate limiting, smart QoS, security, telemetry, configuration, and upgrade.

## Orchestrator Batch Scope

Cover only Orchestrator features: logical ingress/egress interfaces, groups, ordering, rule CRUD, any/IP/CIDR/range/IP-group/port/protocol matching, named paths, limits, security boundaries, node-failure bypass, and runtime telemetry.

Orchestrator acceptance does not repeat Gateway NAT, DHCP, DNS, PPPoE, or proxy acceptance.

## Batch Rules

- Collect independent scenarios in one batch, then repair the complete failure set.
- Preserve the existing environment, logs, and clients unless the topology really changes.
- Hot-deploy affected binaries or plugins first; freeze rootfs/ISO only after the batch passes.
- Performance, 64-byte packets, temperature, ESXi VMXNET3/VFIO, and physical PCI ownership are separate hardware tasks, not ordinary functional acceptance.
