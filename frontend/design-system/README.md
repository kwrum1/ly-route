# Ly Route Design System

This folder establishes the original visual foundation for the full-version controller UI. The preserved `panabit-real/admin/` files remain functional reference only; this system deliberately avoids Panabit assets, `layui`/`paui` classes, old table density, and old button or header styling.

## Aesthetic Direction

- Purpose: help appliance operators see persona, dataplane health, degraded state, and topology before drilling into configuration tables.
- Tone: calm luminous network operations, with dark glass surfaces, cyan control light, amber degraded state, and compact but breathable panels.
- Constraint: no copied UniFi or Panabit proprietary assets; all icons and shapes are CSS/text primitives owned by the project.
- Memorable point: the topology canvas and shell health rail make the controller feel like a live network map, not a legacy admin form stack.

## Token Coverage

- Color: ink, paper, glass, line, primary, accent, semantic status, chart, and topology colors in `tokens.css`.
- Type: display, body, monospace stacks plus hero, heading, body, small, and caption scales.
- Spacing: 4px-based scale from `--space-1` to `--space-24`, plus shell sizing and content max width.
- Radius: extra-small through pill tokens.
- Elevation: soft, panel, lift, primary glow, and warning glow shadows.
- Density: compact, default, and roomy multipliers for controls and future tables.
- Components: shell nav, global badges, page headers, cards, tables, forms, empty states, modals, chart strokes, topology nodes, and topology links.

## Shell Behavior

- Left navigation is persistent on desktop and stacks above content on tablet and mobile widths.
- Global mode badge is visible in the left rail and is labeled initialization-fixed.
- Dataplane status and degraded state are visible in the shell before page content.
- Page header pattern includes section kicker, title, explanatory summary, health badge, and primary action.
- Content uses a 12-column responsive grid with 1440, 1024, and 768 width rules.

## Persona Examples

- Gateway persona displays `wan`, `lan`, `service`, and logical `proxy_egress` entries while keeping proxy egress visibly distinct from physical WAN.
- Bridge persona displays `uplink`, `downlink`, `service`, `bypass`, and `monitor` language with transparent L2 and loop guard states.
- Policy examples show `mode_guard` first in the table to preserve the frozen product contract.

## Static Preview

Open `frontend/design-system/index.html` from a static server to review the shell concept. The page has no backend dependency and is intended as implementation reference for later frontend tasks.
