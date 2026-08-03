# Runtime and Hardware Validation

This checklist defines mandatory full functional acceptance and subsequent
physical appliance validation. The control plane can compile, render, preview,
apply, audit, and expose desired/degraded runtime state, but healthy forwarding
requires receipts, readback, fresh functional evidence, and traffic.

## Mandatory Container Functional Acceptance

The complete matrix is maintained in the [Chinese Full Functional Acceptance Design](zh/container-network-validation.md). It includes API/state, real protocol and packet, browser, system-artifact, and hardware mappings for every product function.

- Run on a Linux host using network namespaces, veth, bridge, and macvlan or an equivalent isolated L2 topology. Docker macvlan is not run on Windows Docker Engine.
- Gateway includes the appliance/router rootfs, LAN clients, at least two WAN peers, distinct DNS upstreams, a client-selected external DNS bypass target, a real PPPoE server, DHCP peers, fixed and subscription proxy nodes, traffic generators, and capture points.
- DNS proves default TCP/UDP 53 interception, fixed WAN/upstream/proxy, DNS priority over PBR, TTL inheritance for answer traffic, proxying of the DNS request itself, and NODATA without fallback. DHCP proves full lease, option, static-binding, exhaustion, conflict, renew/release, restart, and online-user behavior.
- The Gateway must perform actual PPPoE discovery, authentication, session establishment, address acquisition, disconnect, and redial over the isolated L2 network. Injecting an already-connected state is not evidence.
- Exercise primary/backup, weighted, and five-tuple WAN groups; reject proxy logical WAN group members. Exercise fixed/fastest proxy nodes, protocol health, ping-primary ranking, hysteresis, failure failover, and recovery.
- Saturate both directions while measuring ping, DNS, short flows, interactive traffic, throughput, fairness, CPU, and memory for configurable rate limiting and built-in smart QoS.
- Orchestrator includes physical and bond logical endpoints plus observable service-node containers; prove `direct`, `drop`, ordered `via`, exact reverse traversal, and failed-node default bypass.
- Retain topology, image/commit hashes, configuration, pcaps, counters, API states, fault timeline, and recovery results. No covered feature is accepted until the complete repeatable matrix passes.

Target hardware remains required for NIC attachment, final throughput, driver,
thermal, and release evidence; it does not weaken the container functional gate.

## Preconditions

- Build or install the Ly Route rootfs on the target appliance.
- For the image produced by this repository, confirm the rootfs contains real
  FD.io VPP packages plus the Ly Route VPP apply adapter, source-built
  `smartdns`, and source-built `xray` packages:

```sh
dpkg-query -W libvppinfra vpp vpp-plugin-core ly-route-vpp-apply smartdns xray python3
test -x /usr/bin/vpp
test -x /usr/bin/vppctl
test -f /usr/lib/systemd/system/vpp.service
test -x /usr/sbin/smartdns
test -x /usr/bin/xray
test -x /usr/lib/ly-route/vpp-apply
test -f /etc/smartdns/smartdns.conf
test -f /etc/xray/config.json
```

- Install and enable systemd units for SmartDNS, Kea DHCP4, xray, pppd, and VPP.
- Set `/etc/ly-route/control-api.env` with local admin credentials.
- Enable runtime orchestration only on the appliance:

```sh
LY_ROUTE_ENABLE_SERVICE_RUNTIME=true
LY_ROUTE_SERVICE_ROOT=
```

- Connect at least one WAN uplink, one LAN client, and any available real PPPoE
  line. A real provider line adds compatibility evidence and does not replace
  the mandatory container PPPoE functional test.
- Assign dataplane interfaces explicitly. Never bind the management interface
  to VPP or DPDK. Probe all approved native candidates first and choose the best
  measured passing candidate. Probe VPP DPDK only when all native candidates
  fail. All active data interfaces, including bond members, must use the
  device-wide highest common eligible tier; otherwise keep the dataplane locked.

## Control Plane Smoke

1. Log in to the controller UI as admin.
2. Open `系统概况` for authoritative component health. Runtime apply remains a
   maintenance action, but System Maintenance does not duplicate status.
3. Click `刷新状态`.
4. Confirm every applicable component is present: SmartDNS, Kea DHCP, xray,
   PPPoE, VPP, and persistence. Linux firewall/policy-routing interception
   must not appear as a production forwarding dependency.
5. Click `预览运行态`.
6. Confirm service artifacts and VPP-native operations render without secret
   leakage. A Linux interception plan is a failure.
7. Click `应用运行态`.
8. Confirm the response is either `committed` or an explicit degraded/unavailable
   state with a reason. A hidden failure is not acceptable.

Equivalent API smoke:

```sh
curl -fsS http://127.0.0.1/api/v1/runtime/status
curl -fsS http://127.0.0.1/api/v1/runtime/preview
curl -fsS -X POST http://127.0.0.1/api/v1/runtime/apply \
  -H 'Content-Type: application/json' \
  --data '{}'
```

## Repository Acceptance Evidence

The current repository/container evidence is limited to the following checks:

- `go test ./...` under `backend/` passed.
- Desired-state write/read checks passed for interfaces, WAN links, WAN groups,
  route policies, NAT port maps, DNS policies, DHCP servers, DHCP static
  bindings, object groups, ACLs, IP/MAC bindings, threat-intel entries, and
  attack rules.
- DNS policy item routes were fixed and verified: create, `GET
  /api/v1/dns/policies/{id}`, delete, and post-delete `404` now use the same
  policy store.
- Runtime preview rendered 8 service artifacts, 4 VPP operations, 2 DHCP server
  plans, 1 DNS policy, nftables/TProxy plan, Linux policy routing plan, and all
  expected component states: SmartDNS, Kea, xray, PPPoE, VPP, nftables/TProxy,
  Linux routing, and persistence.
- `scripts/ci-verify.sh` and the amd64 rootfs runtime smoke are repository gates;
  they do not prove a physical packet path.
- Rootfs and disk-image checksum/compression inspection proves artifact
  structure, not boot, NIC binding, service health, PPPoE, or forwarding.
- The amd64 release candidate must additionally pass real packet-path tests in
  disposable containers. ARM is limited to build, package-install, and startup
  smoke until a target board is supplied.
- A status of `running` without an apply receipt, readback, and fresh functional
  evidence is invalid. Historical appliance observations in older audit files
  are not current acceptance evidence.

The repository environment did not have secure external validation variables
available during that pass. Set these before running the appliance-only checks:

```sh
export VCENTER_PASSWORD='...'
export LY_ROUTE_PROXY_SUBSCRIPTION_URL='...'
```

Do not place these values in shell history, documentation, CI logs, or command
arguments. Live proxy traffic and low-copy handoff require a concrete upstream
transport and traffic harness; a subscription fetch alone is not runtime proof.

## Service Runtime Checks

### SmartDNS

1. Apply a DNS policy that should resolve direct domains and reject the default
   miss path. Proxy-bound DNS policies must fail with an explicit proxy DNS
   endpoint error until a concrete upstream transport is configured; do not
   treat that as a successful proxy DNS validation.
2. Verify SmartDNS is active:

```sh
systemctl is-active smartdns.service
```

3. Confirm Ly Route rendered SmartDNS syntax, not JSON:

```sh
grep -R '^address /' /etc/smartdns/conf.d
grep -R '^nameserver /' /etc/smartdns/conf.d
smartdns -f -c /etc/smartdns/smartdns.conf -p /run/smartdns-validation.pid
kill "$(cat /run/smartdns-validation.pid)"
```

4. Query from a LAN client and confirm the DNS decision matches the policy.

### Kea DHCP4

1. Configure a LAN DHCP server and at least one static binding.
2. Apply runtime state.
3. Verify Kea is active:

```sh
systemctl is-active kea-dhcp4-server.service
```

4. Renew a LAN client lease and confirm address, router, and DNS options.
5. Confirm static binding leases land on the expected address.

### xray Proxy

1. Configure a proxy egress and DNS/proxy policy that selects it.
2. Apply runtime state.
3. Verify xray is active:

```sh
systemctl is-active xray.service
```

4. From a LAN client, generate traffic matching the policy.
5. Confirm the xray inbound listener and outbound tag match runtime preview.

### PPPoE

1. Configure a PPPoE WAN link with test credentials.
2. Apply runtime state.
3. Verify the pppd unit is active:

```sh
systemctl is-active pppd@ly-route.service
```

4. Confirm a PPP interface is created, receives an address, and installs the
   intended route or DNS information.

## Dataplane Checks

### VPP

1. Verify VPP is active and reachable by its control socket/API.
2. Confirm every compiled VPP operation shown in `/api/v1/runtime/preview` has
   inline `vppctl_commands` or a matching `/etc/ly-route/vpp-command-map.json`
   entry. Map keys may be operation-only such as `vpp.abf.policy` or operation
   plus resource such as `vpp.abf.policy:proxy-egress-default`.
3. Apply runtime state and confirm `/var/lib/ly-route/vpp-apply-receipt.json`
   records every VPP command with status `applied`. Missing command mappings must
   fail the apply instead of silently falling back.
4. Inspect VPP interface, policy, and service-chain state.
5. Send LAN-to-WAN traffic and confirm counters move on the expected VPP path.
6. Remove or invalidate the selected native VPP path and confirm the selector
   tests VPP DPDK, records why it accepts or rejects it, and never captures the
   management interface.
7. Invalidate both native and DPDK candidates and confirm management remains
   reachable while forwarding is locked.

### VPP-native service paths

1. Confirm the rendered VPP operations contain the approved native service
   handoff, DNS interception, or service-chain operations.
2. Inspect VPP sessions, ACLs, ABF/service-chain state, and counters.
3. Generate matching client traffic and capture both directions.
4. Confirm original destination semantics, selected egress, failure bypass, and
   non-matching direct traffic from packet evidence.
5. If no native operation can be applied, the feature must remain explicitly
   unavailable; it must not silently fall back to Linux interception.

## Failure and Recovery

1. Stop SmartDNS, Kea, xray, pppd, and VPP one at a time.
2. Refresh runtime status after each stop.
3. Confirm the UI/API reports a degraded or unavailable component with a reason.
4. Re-apply runtime state and confirm service restart behavior.
5. Submit a deliberately invalid runtime resource and confirm the apply fails
   before claiming success.
6. Reboot the appliance and confirm management UI/API return on LAN.
7. In each injected failure case, send a forwarding probe and confirm no
   dataplane traffic passes while the management plane remains reachable.

## Evidence To Capture

- `/api/v1/runtime/status` before and after apply.
- `/api/v1/runtime/preview` for the tested configuration.
- `/api/v1/runtime/apply` response including transaction ID.
- `systemctl status` output for SmartDNS, Kea, xray, pppd, and VPP.
- VPP interface/policy counters.
- VPP-native rules, sessions, and packet counters.
- DHCP client lease details.
- DNS query results.
- Proxy traffic proof from client and appliance logs.
- Reboot and recovery timestamps.

Only mark hardware E2E complete after the evidence above is captured on the
target appliance.
