# Gateway Historical Functional Acceptance Ledger

> This file preserves historical acceptance evidence and environment notes. It
> is not a current development gate. Use it only for an explicit feature-batch,
> release, or hardware task; daily fixes follow
> [Development, Hotfix, and Acceptance Workflow](development-workflow.md).
> The older mandatory-gate wording below no longer blocks hotfixes.

This is the single functional acceptance ledger for the Ly Route egress gateway. A checked item means that the configuration was entered through the real UI, persisted and applied by the backend, and its expected packet behaviour was observed from an independent virtual LAN client. A healthy process, an API response, or a rendered page alone is not sufficient evidence.

The orchestrator is deliberately out of scope and will receive its own service-chain acceptance ledger.

## Current Test Environment

| Item | Value |
| --- | --- |
| Date | 2026-08-14 |
| Product | Ly Route egress gateway |
| Platform | ESXi virtual machine, functional validation mode |
| Data plane | VMXNET3 + AF_PACKET; functional validation only, not a production-performance claim |
| Management address | `10.1.18.125/24`, gateway `10.1.18.1` |
| Factory management address | `192.168.88.254/24`, gateway `192.168.88.1` |
| WAN test network | `ly-route-wan` and `ly-route-wan2`, each backed by a distinct PPPoE-server VM |
| LAN test network | `ly-route-lan`, with independent client VMs |

For each pass, retain the actual commands, UI/exported configuration, control-plane logs, VPP state, and client observations. Failed cases remain in place and are repeated after a fix.

## Historical Execution Rules (Traceability Only)

These are stop conditions, not recommendations. When any prerequisite fails, functional acceptance stops. A lab-fixture failure must not be filed as a product regression, product source must not be changed before the failing layer is known, and no feature may be declared passed.

### One source and one deployed version

- The worktree is the only repair source. The compiler host builds from a complete synchronization of that source and must not reuse stale backend fragments, plugin trees, or untraceable binaries.
- Record before and after every deployment: Git revision and worktree state plus SHA-256 values for `gateway-control`, all Ly Route VPP plugins, and frontend assets. The same values must be present on the tested VM.
- A hot fix follows one path only: edit worktree source, run focused tests, build on the compiler, replace the current VM program, record fingerprints, and repeat the same scenario. VM-only edits that are not returned to source are forbidden.
- Do not rebuild or reinstall the ISO during functional acceptance. Hot-deploy fixes into the frozen VM; only after full functional and reboot regression passes may the exact verified artifacts be frozen into an ISO.

### Persistent acceptance fixtures

- The PPPoE server, DNS authorities, VLESS test node, DHCPv6-PD service, external port services, and LAN-client initializer must be persistent systemd services or equivalent managed fixtures.
- Before judging Gateway proxy behavior, an independent Xray client must complete a real request through the same node. Xray 26.4 and newer blocks private and reserved targets in Freedom by default. A private acceptance target therefore requires a narrowly scoped server-side `finalRules` allow rule for that exact address, port, and protocol. `proxy/freedom: blocked target` is always a fixture failure.
- A fixture preflight runs before product tests and proves service state, expected TCP/UDP listeners, addresses and routes, PPPoE discovery, positive DNS answers for fixture names, and an independent proxy-node connection.
- A failed preflight is `FIXTURE_FAIL`, never a Gateway defect. Restarting any lab VM requires a new preflight before acceptance resumes.
- Every client probe target must pass a source-specific route check before the
  probe runs. In particular, a transparent-DNS destination must not be an
  address assigned to the fixture itself; `ip route get TARGET from CLIENT`
  must resolve through the LAN Gateway rather than the local table.

### Frozen topology and baseline

- The functional topology is fixed to a management network, dedicated PPPoE WAN, dedicated LAN, persistent server VM, and multiple independent LAN clients.
- During one regression round, do not add/remove NICs, change port groups, change dataplane mode, reinstall the system, or reuse management as the business LAN.
- A necessary topology change creates a new baseline ID and invalidates prior PPPoE, LAN/DHCP, NAT, DNS, proxy, ACL, QoS, and IPv6 runtime evidence.
- The dual-PPPoE-WAN baseline uses two different access concentrators, two different upstream MAC addresses, and two isolated WAN port groups. Two sessions on one AC cover only a shared-upstream case and do not prove the normal home multi-WAN scenario.
- Before marking dual WAN as passed, verify both the `encap-if-index` of each `show pppoe session` entry and the physical `stacked-on` interface in its probe FIB. A correct session ID whose FIB is stacked on the other WAN is a `PRODUCT_FAIL`.

### Layered diagnosis before edits

“The client cannot access the Internet” is not a diagnosis. Find and record the first failing layer in this order: fixture; link/session/address/route; UI/control/persistence; VPP interface/FIB/ACL/ABF/NAT/QoS/plugin; SmartDNS/Kea/Xray service path; independent-client packets and return path. Do not edit code before that layer is known. Traffic reaching Xray while its remote fixture port is absent, or transparent DNS interception receiving NXDOMAIN from a fixture authority, is a fixture defect.

### Batched regression

- One regression entry point checks fixture preflight, PPPoE, DHCP, IPv4 NAT, IPv6 PD/RA, TCP/UDP 53 interception, DNS policy, proxy, port mapping, ACL, and QoS.
- Each case emits `PASS`, `PRODUCT_FAIL`, or `FIXTURE_FAIL` with timestamp, configuration generation, counter deltas, and client evidence. One failure must not stop unrelated evidence collection.
- Collect the full failure set, repair it as a batch, then rerun failed cases and the passed core baseline. Serial one-off environments and repair/reboot loops are prohibited.

### Scope and defect closure

- Validate every originally designed home-gateway function, including multi-WAN groups in primary/backup, weighted, and five-tuple modes. Do not extend this run into throughput, 64-byte packet performance, extra hardware matrices, VMXNET3 native DMA/VFIO, or undesigned capabilities.
- AF_PACKET is only the ESXi functional transport and must not alter the physical production boundary of native VPP first, DPDK fallback, and locked state when neither qualifies.
- A product defect requires a minimal reproducer, pre-fix evidence, source change, focused automated test, deployed fingerprints, post-fix evidence, and core regression. Source, compiler output, hot-deployed VM, and final ISO must remain one artifact chain.

## Evidence Required for Every Check

1. **UI**: the setting is created or changed from the administrator console and can be read back.
2. **Control plane**: API and persisted service configuration match the UI and survive the relevant service restart.
3. **Data plane**: VPP, proxy, DNS, DHCP, or related runtime state matches the setting.
4. **Client**: an independent LAN client obtains the expected result with real packets.
5. **Recovery**: persistent settings are re-tested after reboot.

## VM and Physical-Hardware Evidence Boundary

- ESXi, VMXNET3, and AF_PACKET prove only the UI-to-control-plane-to-data-plane functional loop. They do not prove production performance or exclusive NIC ownership.
- Physical hardware requires separate evidence for driver discovery, PCI/VFIO ownership transfer, the native VPP fast path, DPDK fallback, reboot recovery, and link-failure recovery.
- VM-specific fixes must not alter the physical-NIC selection order: native VPP fast path first, DPDK fallback second, and a locked state when neither qualified path is available.
- Interface enumeration, rename, capability-probe, and ownership fixes must prove that existing VPP interfaces are preserved, the management interface is excluded, and physical PCI identity remains stable.

### Cross-environment fix gates

For every dataplane fix, classify it as common control/dataplane logic or a
VM-only compatibility adapter. A VM adapter may make acceptance possible, but
must not change the physical production selection order or turn VMXNET3/
AF_PACKET into a physical high-performance claim. NIC-related changes require
separate physical-hardware evidence before they can be checked off:

| Status | Physical regression item | Required evidence |
| --- | --- | --- |
| [ ] | Driver and PCI discovery | `lspci -nnk`, driver, MAC, and PCI address |
| [ ] | Management-NIC exclusion | Linux retains the management NIC; it is absent from the VPP/DPDK data-NIC set |
| [ ] | Data-NIC ownership | Driver/PCI ownership before and after binding plus VPP hardware-interface output |
| [ ] | Multi-port consistency | All selected data ports use the same qualified tier; no unknown mixed tier |
| [ ] | Lock on failure | If native and DPDK are both unqualified, no partial dataplane starts and the reason is recorded |
| [ ] | Reboot and link recovery | MAC/PCI mapping and ownership remain stable after reboot and link down/up |

Passing the ESXi scenario therefore only allows functional acceptance to
continue. Physical driver, DMA, IOMMU/VFIO, queue, and ownership results must be
recorded separately and cannot be inferred from VM evidence.

## Locked Lessons From This Acceptance Round

The following rules are based on failures that already occurred in this
project. They are mandatory acceptance rules, not suggestions:

1. **No version drift**: the worktree, compiler host, hot-deployed VM, runtime
   snapshot, and ISO must belong to one artifact chain. A VM-only edit that was
   not returned to source, rebuilt, and fingerprinted is not a fix. Old control
   binaries, frontend bundles, and VPP plugins must not remain in deployment
   directories where they can be selected accidentally.
2. **A missing post-reboot fixture is not a product regression**: PPPoE, DNS,
   proxy, DHCPv6-PD, external port services, and client networking must be
   persistent services. After any lab-VM reboot, run fixture preflight and check
   listeners, addresses, and routes first. An incomplete fixture is only
   `FIXTURE_FAIL`.
3. **Do not mix topologies**: adding/removing NICs, changing port groups,
   changing LAN/WAN addresses, switching AF_PACKET/VPP/DPDK modes, or
   reinstalling creates a new baseline. Results from the old baseline cannot be
   reused.
4. **Do not erase the scene before analysis**: archive the failing state,
   database configuration, VPP runtime, service logs, client output, and
   counters before repair. Do not reset the database, delete policies, rebuild
   the VM, or recreate fixtures merely to obtain a clean-looking test.
5. **Do not repair serially by guesswork**: collect all independent results in
   one batch, repair the complete failure set, then rerun the failures and the
   PPPoE/LAN/DHCP/NAT/DNS/proxy core baseline. Avoid restarting and rebuilding
   the lab after every individual test.
6. **“The client cannot access the Internet” is not a root cause**: identify
   the first failing layer in order: fixture, link/session, control plane, VPP,
   service path, and client. No source edit is allowed before that layer has
   evidence.
7. **API or systemd health is not feature acceptance**: real UI configuration,
   final backend persistence, committed runtime transaction, VPP/service
   readback, and independent-client packets are all required. After a control
   service restart, the status may still describe the previous transaction;
   apply once and verify the new transaction ID before judging the result.
8. **QoS requires a matched control**: bytes written by a TCP sender are not a
   dataplane throughput proof. Record matched and unmatched ports, VPP policer
   `conform/exceed/violate` deltas, ACL hits, and client results together. Do
   not check QoS from one transfer number.
9. **Do not expand the scope**: this round validates the originally designed
   home-gateway features only. Performance, 64-byte packets, extra hardware
   matrices, ESXi VMXNET3 native DMA/VFIO, and undesigned capabilities are
   separate tasks and must not block or alter the functional path.
10. **Gateway and Orchestrator are separate products**: Orchestrator does not
    participate in Gateway NAT, DHCP, DNS, PPPoE, proxy, or Gateway QoS
    acceptance. Their source, runtime, and evidence directories must not be
    mixed.
11. **A full policy replay deletes the complete union first**: before rebuilding
    route policies, delete the union of policies in the prior snapshot,
    explicitly deleted policies, and currently desired policy IDs. Depending on
    an incomplete prior snapshot can leave a same-ID ACL/ABF object that blocks
    the replacement policy.
12. **Runtime readback follows the implementation mode**: ordinary policies are
    read back through ACL/ABF. Large GeoIP policies are read back through the
    pre-NAT Radix/LPM classifier and its FIB next hop, including enabled state,
    LAN prefix, priority, rule count, table ID, and NAT bypass. A Radix policy is
    not required to have an ACL/ABF object.
13. **Large rule replays have fixed batching and an idempotency gate**: VPP
    commands use `512` entries per batch by default; the legacy ESXi-specific
    batch size of `32` must not return. After changing policy lifecycle,
    snapshot decoding, or batching, submit the identical real UI configuration
    twice. Both applies must be `committed/running`, services must be healthy,
    and independent-client core traffic must pass before closing the defect.

### Result classes and stop conditions

The batch regression may emit only these four result classes:

| Result | Meaning | Next action |
| --- | --- | --- |
| `PASS` | All evidence layers exist and client behavior is correct | Record evidence and preserve the scene |
| `PRODUCT_FAIL` | The first failure is in product source, generated configuration, or runtime apply | Record a minimal reproducer and repair |
| `FIXTURE_FAIL` | PPPoE/DNS/proxy/server/client prerequisites are not valid | Repair only the fixture and repeat the scene |
| `BLOCKED` | Network, hardware, or permissions prevent evidence collection | Preserve the scene; do not claim pass |

`FIXTURE_FAIL` and `BLOCKED` stop the related product conclusion. A
`PRODUCT_FAIL` requires preserving evidence before editing source. An unclassified
failure cannot advance to the next phase.

## Installation and Base Runtime

| Status | Capability | Current evidence | Notes |
| --- | --- | --- | --- |
| [x] | Clean ISO installation and first boot | Completed in an ESXi fresh VM | Installer flow executed |
| [x] | MAC/PCI based interface mapping | `ens32` management, `ens33` WAN, `ens34` LAN | Not dependent on Linux `eth0` ordering |
| [x] | Management IP and SSH reachability | Developer machine connected to `10.1.18.125` | Acceptance-specific override |
| [x] | Web login and forced first password change | `admin/password` requires a change | Exercised in a real browser |
| [x] | VPP startup and AF_PACKET data interfaces | VPP CLI works; `lyroute-ens33` and `lyroute-ens34` are up | Virtual functional path only |
| [x] | Services recover after reboot | VPP, control services, Xray and SmartDNS start | Does not prove policy functionality |
| [ ] | Factory `192.168.88.254/24` first-install regression | Final ISO regression pending | Current VM intentionally uses a test subnet |

## Functional Checklist

The Chinese ledger contains the detailed per-feature checklist used during acceptance. The same required areas are: WAN and native PPPoE, LAN and DHCP, IPv4 NAT and inbound port mapping, ACL statefulness, DNS interception and DoH bootstrap selection, geoip/geosite routing, proxy WAN fixed/fastest node selection, IPv6 PD/RA, QoS including proxy traffic, telemetry, backup/restore, management interface changes, firmware upgrade, and complete reboot recovery.

All of those functional items remain unchecked. The current evidence proves only installation and base runtime, not end-to-end gateway functionality.

## Open Issues

| ID | State | Issue | Rule |
| --- | --- | --- | --- |
| G-001 | Isolated | ESXi VMXNET3 native DMA/VFIO is unstable with multiple ports | AF_PACKET is used for this functional run; it is not claimed as the production fast path |
| G-002 | Guarded | DHCP on a management-shared network can broadcast to the developer LAN | LAN/DHCP validation uses dedicated `ly-route-lan` |
| G-003 | Pending | VPP QoS runtime adapter currently reports unavailable | QoS cannot pass until it is proven with real traffic |
| G-004 | Fixed, regression pending | Runtime capability probing deleted an existing AF_PACKET attachment as if it were a temporary probe | Existing `lyroute-*` interfaces are now reused and cleanup is limited to objects created by the current probe; reboot and physical-probe regression remain |
| G-005 | Fixed, regression pending | LAN UI readback compared a Linux interface name with a logical interface ID and reported a false mismatch | Readback now resolves interface aliases; repeat save and refresh during UI regression |
| G-006 | Fixed, reboot regression pending | The native PPPoE client plugin was absent from the image | The plugin is now loaded, the Gateway has a real PPPoE session, and an independent LAN client received HTTP `200` through VPP NAT and PPPoE. Reboot persistence remains a separate check. |
| G-007 | Fixed, proxy egress regression pending | IPv4 route policies were evaluated after NAT rewrote the LAN source, so original-source policies could fall through | `ly_route_pre_nat_route_plugin.so` classifies before NAT and selects the target FIB. The controller reads the actual VPP data-LAN prefix, writes rules, and verifies readback. |
| G-008 | Lab-upstream blocked | The ESXi test network cannot reach the public VLESS endpoint or public DoH endpoints | The client-to-proxy intake route is evidenced by VPP counters and the Xray TPROXY interface. Remote proxy egress must be repeated when the lab has upstream Internet. |
| G-009 | Fixed, reboot regression pending | A catch-all pre-NAT route policy captured DHCP client packets before ABF and LCP could deliver them to Kea | Every generated pre-NAT plan now installs a priority-zero UDP `68 -> 67` bypass for Discover, Request, and Renew. Closure requires a real UI apply followed by an independent client lease, and the same probe runs in the batch gate. |

## Current Conclusion

The strict run has verified UI submission and control-plane readback, native PPPoE session establishment, LAN/DHCP basic configuration, IPv4 NAT with an independent-client HTTP `200` response, NAT mode readback, inbound port mapping, base VLESS/Reality intake, SmartDNS basic resolution, and VPP transaction rollback/persistence. These are not a production-release claim: DNS policy precedence, GeoIP/GeoSite end-to-end routing, remote proxy egress, IPv6 PD/RA, QoS, security, telemetry, backup/restore, upgrade, and full reboot recovery remain open.
# Xray node DNS boundary

- A hostname used by an Xray node is resolved by the control plane before the runtime Xray configuration is rendered; TLS/Reality SNI keeps the original hostname.
- Resolution is pinned to the built-in foreign DoH bootstrap DNS set: `1.1.1.1`, `1.0.0.1`, `8.8.8.8`, `8.8.4.4`, and `9.9.9.9`.
- The control plane tries the fixed servers in order. If one is unavailable it tries the next; if all fail, the Xray runtime configuration is rejected instead of falling back to host DNS.
- The resolver never inherits `/etc/resolv.conf`, client DNS, dataplane DNS policy, or environment overrides.
- These servers bootstrap the node endpoint only. User-configured DoH remains the DNS-policy upstream.

## Runtime apply and evidence boundary

- A production runtime apply uses the typed VPP gateway transaction. It must
  validate the desired graph through VPP readback before the service phase is
  considered committed.
- `/var/lib/ly-route/vpp/operations.json` is persisted for boot recovery. Its
  `vpp-apply-receipt.json` belongs to that recovery replay only and must not be
  used as the receipt for a typed runtime transaction.
- Runtime acceptance requires one committed transaction, typed gateway
  evidence, live VPP readback, and all configured services healthy. A stale
  recovery receipt is not a product failure when the typed transaction and
  live readback are valid.
- Any future mismatch must first be classified as `PRODUCT_FAIL` or
  `FIXTURE_FAIL`; do not repeat the full acceptance run without preserving the
  first failing transaction and its evidence.
- A route or proxy policy must never capture DHCPv4 client traffic. Every
  pre-NAT policy generation installs the priority-zero UDP `68 -> 67` bypass,
  and each runtime-apply regression must obtain a lease from an independent
  LAN client before DHCP is considered healthy.

## NAT behavior boundary

- Gateway defaults to VPP NAT44-ED (endpoint-dependent) behavior. An omitted NAT behavior must never silently switch modes.
- Full-cone NAT is an explicit `full_cone` choice and uses VPP NAT44-EI (endpoint-independent) at runtime.
- NAT44-ED and NAT44-EI are mutually exclusive gateway-wide modes. Static mappings, port mappings, dynamic PPPoE egress, and return-path protection must use the same mode; mixed configuration is rejected while the last known-good runtime state is retained.
- Full-cone acceptance requires VPP EI interface/address/mapping readback and a real UDP session from an independent LAN client. A successful API call or rendered configuration alone is not acceptance evidence.
