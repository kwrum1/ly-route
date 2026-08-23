# Ly Route Gateway UI Design System

## Product Context

Ly Route is an operational router console. The interface is designed for repeated configuration, diagnosis, and comparison, not for marketing. The visual direction combines the mature information density of Panabit with the restrained blue-and-white clarity of iKuai, while preserving Ly Route's existing information architecture and API behavior.

## Design Principles

1. **Operational first**: the current state, value, and next action must be visible without explanatory copy.
2. **Stable geometry**: loading, empty, and populated states keep the same dimensions. Tables use fixed row heights and ten-row pages.
3. **Progressive truth**: fast API results render immediately. A slow optional endpoint must not block CPU, memory, interface, or traffic data.
4. **Router semantics only**: show interfaces, links, users, policies, egresses, and traffic. Never expose fixture names, internal state keys, or developer diagnostics in normal pages.
5. **Detail on demand**: dense lists stay scannable; row details open in a right-side drawer without losing list context.

## Visual Language

- Canvas: `#f4f7fb`
- Surface: `#ffffff`
- Subtle surface: `#f8fafc`
- Header surface: `#f2f6fb`
- Primary text: `#172b43`
- Secondary text: `#60748a`
- Muted text: `#8a9aac`
- Border: `#d9e3ee`
- Strong border: `#c6d5e5`
- Primary blue: `#2878d0`
- Primary hover: `#1f67b5`
- Info blue: `#4b97e3`
- Success: `#18875f`
- Warning: `#b7791f`
- Danger: `#c64040`

No gradients, decorative blobs, oversized hero copy, nested cards, or negative letter spacing. Radius is 4px for controls and 6px for panels.

## Typography

Use the local system stack: `Inter`, `Segoe UI`, `Microsoft YaHei`, sans-serif. Body text is 14px, compact labels 12px, panel headings 16px, and page headings 20px. Numeric telemetry uses tabular figures. Font size never scales with viewport width.

## Layout

- Header: 60px fixed height.
- Sidebar: 246px desktop width; off-canvas below 900px.
- Workspace: fills the remaining viewport with 16px spacing.
- Page title: compact 48px band.
- Dashboard panels: full-width bands or a clear grid, never cards inside cards.
- Tables: 42px header, 48px rows, ten visible row slots, pager aligned bottom-right.
- Charts: 240-280px stable height with fixed axes, legend, hover tooltip, and zero-data baseline.

## Components

### System Overview

- A compact status strip replaces the decorative hero.
- CPU and memory retain horizontal utilization bars.
- Device information always exposes uptime, current IP count, system time, and hardware platform.
- WAN trend shows upload plus download totals for every direct WAN, WAN group, and proxy WAN, with one color per egress.

### Traffic Overview

- Top metrics show current upstream, downstream, active connections, online users, and available WAN egresses.
- Upstream and downstream are rendered as clear line/area charts with shared interaction, restrained fills, readable axes, and a time-range segmented control.
- Egress legend can toggle individual series without shifting layout.

### Online Users

- IP addresses are actionable links.
- Selecting an IP opens a large centered session workspace containing identity, traffic totals, matched policy, selected egress, and active connection rows from existing telemetry.
- Missing telemetry is represented by an empty table state, never invented values.

### Network Interfaces

- The inventory contains physical interfaces and aggregate interfaces only.
- LAN/WAN are role badges on physical or aggregate interfaces, not standalone inventory rows.
- Aggregate member interfaces are hidden from the top-level inventory while the aggregate exists.
- Physical identity prefers the stable interface ID (`ethN`) and shows the system name (`ensN`) as secondary information.

## Interaction And States

- Data refreshes automatically every five seconds; no manual refresh action is shown.
- Loading placeholders preserve final dimensions.
- Empty states use concise product language such as `暂无连接记录`.
- Row actions stay right-aligned and never alter row height.
- Drawers and modals trap focus, close with Escape, and restore focus to the invoking control.
- Controls have visible focus rings and a minimum 36px interactive height on desktop, 44px on mobile.

## Responsive Rules

- At 900px, sidebar becomes off-canvas and dashboard grids collapse to one column.
- Data tables remain fixed-density and scroll horizontally inside their own region; the page itself must not scroll horizontally.
- Drawer width is `min(640px, 100vw)` and becomes full-screen on narrow phones.
- Labels may wrap, but values and buttons may not overlap neighboring content.

## Accepted Constraints

- The current frontend is vanilla JavaScript and CSS. This iteration keeps that architecture.
- Existing routes and backend mutations remain unchanged.
- Detail views compose existing online-user, top-session, policy-hit, route-policy, WAN, and proxy telemetry. Backend extensions are only justified when the UI cannot truthfully obtain a required device fact.

## Router Operations Refinement

- System overview prioritizes CPU, memory, uptime, online IPv4 users, system time, platform, and the active logical WAN trend.
- Traffic charts sample every five seconds, retain 24 hours, use compact local-time axes, and expose full timestamps in tooltips.
- Top connections aggregate mappings observed in the latest five seconds by source IP. Top domains aggregate SmartDNS audit events for the latest 24 hours.
- Online-user rows refresh status, connection count, rate, uptime, and totals every five seconds.
- Session details use a compact modal, 20 rows per page, and explicit NAT mapping terminology for full-cone sessions.
