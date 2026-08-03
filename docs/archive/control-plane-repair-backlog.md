# Control Plane Repair Backlog

## Scope

This document captures the next control-plane/UI repair items reported during live appliance testing. It is intentionally a diagnosis and repair plan, not an implementation patch.

## Bug 1: Optional capabilities make the whole appliance degraded

### Symptom

The shell shows repeated degraded text such as:

```text
控制面 显式降级模式 gateway · 版本 dev
部分能力不可用，概况保持可刷新。
pppoe: pppd@ly-route.service is not active ...
transparent_proxy_handoff: low-copy handoff remains research-gated
```

### Root cause

- `/api/v1/health` marks the whole appliance degraded when any dependency has `available=false`: `backend/internal/httpapi/server.go:1068`.
- `pppoe` is always included in global capabilities by checking `pppd@ly-route.service`: `backend/internal/httpapi/server.go:1248`.
- `transparent_proxy_handoff` is hard-coded as `available=false`: `backend/internal/httpapi/server.go:1257`.
- The frontend combines `health.dependencies` and `/api/v1/capabilities.items`, which can duplicate the same degraded reasons: `frontend/controller-shell/app.js:192`.

### Product rule

For a home router, PPPoE is optional. If no PPPoE WAN is configured, inactive `pppd@ly-route.service` must not degrade the whole appliance. Research-gated capabilities must not be visible as user-facing degraded errors.

### Repair plan

1. Add capability severity/classification: `required`, `optional`, `configured`, `research_hidden`.
2. `/api/v1/health.status` should degrade only when required capabilities fail.
3. PPPoE should report `not_configured` unless a PPPoE WAN profile exists.
4. `transparent_proxy_handoff` should be hidden from global health or reported only in a developer/research diagnostics view.
5. Frontend should dedupe capability reasons and show only actionable items.

## Bug 2: Add/Edit policy and DHCP actions do not visibly save

### Symptom

Adding route policies, DHCP entries, or similar resources gives no reliable success/failure feedback and the new item does not appear in the UI.

### Confirmed code path

- Backend mutation support exists: `handleDesiredMutation()` validates, saves to SQLite, and returns `runtime_state=desired_not_applied`: `backend/internal/httpapi/server.go:2363` and `backend/internal/httpapi/server.go:2418`.
- Route policy and DHCP endpoints are registered: `backend/internal/httpapi/server.go:382` and `backend/internal/httpapi/server.go:408`.
- Frontend resource endpoints are registered: `frontend/controller-shell/app.js:1131`.
- Add/Edit actions mostly only call `openModal(...)`: `frontend/controller-shell/app.js:922` through `frontend/controller-shell/app.js:930`.
- The OK button only calls `pendingModalSubmit` when it is set; otherwise it just closes the modal and shows `已保存`: `frontend/controller-shell/app.js:1405`.
- `pendingModalSubmit` is currently wired for delete confirmation and password change, not for most Add/Edit forms: `frontend/controller-shell/app.js:48`, `frontend/controller-shell/app.js:889`, and `frontend/controller-shell/app.js:1167`.

### Root cause

This is primarily a frontend submission bug, not a VPP/NIC takeover bug. The UI opens forms but does not serialize most form data, POST/PATCH it to the resource endpoint, show backend validation errors, then refresh resources.

There is a second UX gap: even after a desired config save succeeds, runtime effect requires `/api/v1/config/apply`. The UI must distinguish:

- saved to desired config;
- validation failed;
- apply pending;
- apply succeeded;
- apply failed with rollback/error details.

### Repair plan

1. Add per-page payload builders for route policy, DHCP server, DHCP static binding, interface role, WAN/LAN, object groups, and firewall/security resources.
2. On modal OK, call the correct endpoint with `POST` for create and `PATCH`/`PUT` for edit.
3. Display backend error messages from `error.message` instead of generic `已保存`.
4. On success, close modal, toast `已保存，待应用`, and refresh resource endpoints.
5. Surface a visible `应用配置` CTA when any item has `runtime_state=desired_not_applied`.
6. After `/api/v1/config/apply`, show committed/apply_failed state and refresh runtime/status resources.

## Bug 3: Runtime operations and config management duplicate content

### Symptom

`配置管理` and `运行态操作` overlap. `配置管理` currently includes runtime operations and firmware update UI, making the system section confusing.

### Confirmed code path

- Menu contains both `配置管理` and `运行态操作`: `frontend/controller-shell/app.js:26` and `frontend/controller-shell/app.js:27`.
- `system/sys_config` renders `systemConfigOperationsHtml()`: `frontend/controller-shell/app.js:351`.
- `systemConfigOperationsHtml()` embeds `renderRuntimeOperationsHtml()` and `renderFirmwareOperationsHtml()`: `frontend/controller-shell/app.js:369`.
- `system/firmware_update` already renders firmware operations separately: `frontend/controller-shell/app.js:354`.

### Repair plan

1. Keep runtime preview/apply/status only under `运行态操作`.
2. Keep firmware upload/update only under `固件更新`.
3. Remove duplicated runtime and firmware blocks from `配置管理`.
4. Re-scope `配置管理` to actual backup/restore/import/export/factory reset, or remove it if those controls are not ready.

## Bug 4: Add protected management-port editor

### Requirement

The system needs a management-port editor that can modify:

- management interface;
- management IP/CIDR;
- subnet mask;
- gateway;
- DHCP pool/router/DNS values tied to the management subnet.

### Current sources

- Firstboot chooses the first non-loopback NIC and writes `LY_ROUTE_LAN_INTERFACE` / `LY_ROUTE_LAN_CIDR`: `packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh:49` and `packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh:72`.
- Default management CIDR is `192.168.88.1/24`: `packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh:5`.
- Kea DHCP default is also tied to `192.168.88.0/24` and router/name-server `192.168.88.1`: `packaging/rootfs-overlay/etc/ly-route/default-config.json:51`.

### Safety rule

Changing the management port or IP can lock the user out. The editor must require explicit confirmation and produce a staged/apply flow with rollback guidance.

### Repair plan

1. Add a backend management-network resource that reads current interface/CIDR/gateway/DHCP settings.
2. Add a validation endpoint that rejects invalid CIDR, gateway outside subnet, DHCP pool outside subnet, and data-port/VPP-owned interface selection.
3. Add an apply endpoint that updates Linux address config, Kea DHCP config, and persisted desired config atomically where possible.
4. Show the new URL after apply, for example `https://<new-management-ip>/`.
5. Keep management interface excluded from VPP/native dataplane by default.
