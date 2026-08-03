# Interface Discovery Investigation

## Symptom

On the latest Gitea-built firmware, the UI shows only `enp1s0` in `网络接口配置`, while other physical NICs that are link-down do not appear.

## Confirmed findings

- The frontend does not independently discover NICs. It renders whatever `/api/v1/interfaces` returns: `frontend/controller-shell/app.js:727-765`.
- The backend interface list comes from `interfaceRuntimeSnapshot()`: `backend/internal/httpapi/server.go:4626-4657`.
- If `server.interfaceTelemetry` exists, the backend uses that runtime source and returns its items directly: `backend/internal/httpapi/server.go:4627-4632`.
- If runtime telemetry is absent, the backend falls back to desired-state interfaces, then optionally overlays host discovery only when `LY_ROUTE_DISCOVER_HOST_INTERFACES=true`: `backend/internal/httpapi/server.go:4634-4643`.
- The default desired interface set is effectively a single `eth0` entry: `backend/internal/httpapi/server.go:1780-1783` and `backend/internal/httpapi/server.go:2287-2320`.

## Live access evidence

The SOCKS proxy path was first rechecked from the workspace after the user reported it was available:

- `10.18.5.227:10808` accepts TCP connections.
- SOCKS5 `CONNECT` to appliance ports `22`, `80`, `443`, `8080`, `8443`, `9090`, and `10000` is granted.
- SSH to `192.168.88.1:22` through `nc -X 5 -x 10.18.5.227:10808` times out during banner exchange before password authentication.
- HTTPS to `192.168.88.1:443` through the proxy fails during TLS handshake with `unexpected eof while reading`; `openssl s_client` reports no peer certificate and EOF after the client hello.
- HTTP probes to likely API ports return `Empty reply from server`.

After the cable was connected, HTTPS and SSH became usable through the same proxy path. The appliance now confirms the mismatch:

```text
ip -br link
lo               UNKNOWN        00:00:00:00:00:00 <LOOPBACK,UP,LOWER_UP>
enp1s0           UP             00:e2:69:2a:fb:57 <BROADCAST,MULTICAST,UP,LOWER_UP>
enp2s0           DOWN           00:e2:69:2a:fb:58 <NO-CARRIER,BROADCAST,MULTICAST,UP>
enp3s0           DOWN           00:e2:69:2a:fb:59 <NO-CARRIER,BROADCAST,MULTICAST,UP>
enp4s0           DOWN           00:e2:69:2a:fb:5a <NO-CARRIER,BROADCAST,MULTICAST,UP>
```

```text
/sys/class/net inventory
enp1s0 oper=up carrier=1 addr=00:e2:69:2a:fb:57 driver=igb device=0000:01:00.0
enp2s0 oper=down carrier=0 addr=00:e2:69:2a:fb:58 driver=igb device=0000:02:00.0
enp3s0 oper=down carrier=0 addr=00:e2:69:2a:fb:59 driver=igb device=0000:03:00.0
enp4s0 oper=down carrier=0 addr=00:e2:69:2a:fb:5a driver=igb device=0000:04:00.0
```

The running config confirms `LY_ROUTE_LAN_INTERFACE=enp1s0`; there is no `LY_ROUTE_DISCOVER_HOST_INTERFACES=true` in `/etc/ly-route/*.env`.

The live `/api/v1/interfaces` response contains only one item:

```text
items: [enp1s0]
capability: vpp_interface_runtime available=false reason="VPP apply receipt has no operations"
```

The appliance-local API (`https://127.0.0.1/api/v1/interfaces` and `http://127.0.0.1:8080/api/v1/interfaces`) returns the same single `enp1s0` item. The VPP apply receipt is:

```json
{"dry_run":false,"operations":[],"status":"ready"}
```

So this is now a verified application inventory mismatch: Linux sees four physical `igb` NICs, but the Ly Route API exposes only the configured management/default interface.

## Most likely root cause

The missing link-down NICs are most likely not a frontend filtering bug. The current backend behavior only exposes them when host discovery is explicitly enabled, and even then it replaces desired-state metadata with the discovered list rather than merging all interface sources.

That means:

1. if VPP telemetry is active and only reports one bound interface, the API will only show that one;
2. if telemetry is absent, the fallback path still starts from desired config, which defaults to one interface;
3. host discovery is gated behind `LY_ROUTE_DISCOVER_HOST_INTERFACES`, so link-down NICs stay hidden unless that flag is enabled.

## External Linux behavior

Linux normally exposes physical NICs in `/sys/class/net` and `ip link show` even when carrier is down. Relevant kernel docs confirm:

- `/sys/class/net/<iface>/carrier` reports physical link state (`0` down, `1` up).
- `/sys/class/net/<iface>/operstate` reports operational state such as `down`, `lowerlayerdown`, `dormant`, `up`.
- `ip link show` displays all interfaces by default; `up`/`down` filters are optional.

So if the device exists in the kernel but the UI does not show it, the first suspect is application-side inventory shaping, not Linux enumeration.

## Secondary suspects

- Host discovery only checks `/sys/class/net` entries that also have a `device` symlink: `backend/internal/httpapi/server.go:4695-4727`.
- The interface list stores `admin_state: "up"` for every discovered NIC regardless of actual state: `backend/internal/httpapi/server.go:4720-4726`.
- `localDatapathCapability()` only probes `ethtool` and `queues/rx-0`; it does not enumerate NIC presence.
- The frontend status cell is derived from `link_state` and `admin_state`, but it does not filter rows away: `frontend/controller-shell/app.js:750-765`.

## What to check when SSH/API access is restored

Run these on the appliance:

```sh
ip -br link
ls /sys/class/net
for n in /sys/class/net/*; do
  echo "== ${n##*/} =="
  cat "$n/operstate" 2>/dev/null
  cat "$n/carrier" 2>/dev/null
  readlink -f "$n/device" 2>/dev/null || true
done
curl -ksS https://127.0.0.1/api/v1/interfaces
curl -ksS https://127.0.0.1/api/v1/telemetry/interfaces
```

Interpretation:

- If `ip -br link` shows the missing NICs but `/api/v1/interfaces` does not, the bug is in backend shaping/gating.
- If the NICs are absent from `/sys/class/net`, the issue is kernel/driver/PCI detection, not Ly Route UI code.

## Working conclusion

The current issue is best treated as an interface-inventory policy bug: the UI is only as complete as the backend runtime snapshot, and that snapshot currently prefers a single managed interface unless host discovery is explicitly enabled.

## Target operating model

The appliance install target is:

- exactly one physical port is the protected management port;
- the management port keeps Linux ownership and must not be claimed by VPP/native dataplane drivers by default;
- the management port serves `192.168.88.1/24` and DHCP so the user can always reach the Web UI/SSH after installation;
- all other physical ports are data-port candidates;
- data ports must appear in the UI even when link-down, because the user configures their role/path from the management UI;
- data ports can later be assigned to WAN/LAN/internal/external roles and then consumed by VPP/native dataplane apply logic;
- DHCP on the management port is already confirmed working and is part of the protected baseline.

This means interface inventory and dataplane ownership must be separate concepts. Discovery should list all physical NICs, but default dataplane apply must exclude the management port unless an explicit future feature provides safe handoff, rollback, and management reachability preservation.

## Repair plan

1. Make host physical NIC discovery part of the default interface inventory path, not an opt-in debug path.
2. Merge sources instead of replacing them:
   - kernel host inventory from `/sys/class/net`;
   - desired-state metadata from stored interface config;
   - VPP/runtime telemetry when available.
3. Preserve desired metadata (`gateway_role`, `mode_role`, `candidate_scopes`) for configured interfaces while adding unconfigured physical NICs as configurable candidates.
4. Mark `LY_ROUTE_LAN_INTERFACE`/`192.168.88.1` as the protected management interface in the API response, with `gateway_role=lan` and a non-dataplane/default-protected ownership marker.
5. Keep link-down physical NICs visible with `link_state=down`, `carrier=0`, actual `mac`, driver, PCI address, and speed when available.
6. Treat VPP telemetry as status enrichment, not the authority for which physical NICs exist.
7. Ensure dataplane apply/probe logic excludes the protected management port by default and only considers non-management data-port candidates.
8. Add tests that simulate four kernel NICs (`enp1s0` up as management, `enp2s0/enp3s0/enp4s0` down as data-port candidates) and assert `/api/v1/interfaces`, `/api/v1/interfaces/<id>/stats`, bond creation validation, and `/api/v1/telemetry/interfaces` all see the same inventory.

Primary code targets:

- `backend/internal/httpapi/server.go:4626` (`interfaceRuntimeSnapshot`)
- `backend/internal/httpapi/server.go:4660` (`overlayInterfaceDesiredMetadata`)
- `backend/internal/httpapi/server.go:4695` (`localInterfaceInventory`)
- `backend/internal/httpapi/server_test.go:943` and nearby interface snapshot tests

Frontend fix is not required for row visibility, but manual QA should confirm the table, edit modal, role change, and bond member dropdown all handle the added link-down rows.
