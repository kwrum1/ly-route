# Visual Review Checklist

## No Panabit Visual Inheritance

- [x] No Panabit logos, product images, favicon, or source assets are referenced.
- [x] No `paui-*`, `layui-*`, `--pa-*`, Panabit header, Panabit menu, or Panabit login visual language is used.
- [x] Dense legacy table stacks are replaced by card, topology, panel, and responsive grid primitives.
- [x] Legacy blue/gray office-admin styling is replaced with project-owned dark glass, cyan, amber, and semantic status tokens.
- [x] Button, badge, table, modal, empty-state, and form styles are defined through design-system tokens.

## No Unlicensed Third-Party Assets

- [x] No UniFi logos, icons, screenshots, trademarks, or proprietary assets are copied.
- [x] Navigation mark and topology shapes are CSS/text primitives created for this repository.
- [x] SVG charts are inline geometric primitives, not downloaded assets.
- [x] Font stacks use locally available system families without bundling third-party font files.

## Required Product States

- [x] Left navigation shell is defined.
- [x] Global mode badge is visible and marked initialization-fixed.
- [x] Dataplane status is visible in the shell.
- [x] Health and degraded states have explicit semantic tokens and badge patterns.
- [x] Gateway and Bridge persona examples are present at design-system level.
- [x] Proxy egress is displayed as a WAN-list participant while retaining `proxy_egress` semantic labeling.
- [x] Topology canvas primitives include controller node, links, active state, and degraded state.
- [x] Charts, cards, tables, forms, empty states, and modals have reusable primitives.
