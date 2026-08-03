# LY-Route UI Visual Baseline

The normative specification is maintained in [Chinese](zh/ui-design.md).

The UI keeps the current menu order, routes, dashboard modules, content positions, table columns, primary actions, form sections, and workflow locations. iKuai 4.0 is referenced only for its light blue/white visual language, white content surfaces, restrained borders/shadows, and modern control styling. It is not an information-architecture redesign.

Panabit is referenced only for mature configuration semantics: explicit `any`, host, CIDR, continuous IP range, reusable IP/domain groups, policy-group common conditions, searchable object selectors, reference protection, and actionable import validation. No third-party code, branding, screenshots, or assets may enter the product.

Existing fields are upgraded in place to typed selectors. Gateway supports IP/domain groups where policy semantics allow them; Orchestrator retains its no-domain-object boundary. See [Configuration Expression Model](configuration-model.md) and UI work packages U1/U2 in the [Work Plan](work-plan.md).

Gateway WAN groups expose only primary/backup, weighted, and five-tuple modes and reject proxy logical WAN members. Subscription proxy routes expose fixed-node and fastest-node selectors in the existing field position; fastest mode shows the effective node, rolling ping, health, probe time, and switch reason without exposing internal hysteresis knobs. QoS is visibly separated into configurable policy rate limiting and built-in smart QoS; the latter shows enablement, bandwidth, loaded latency, and health but no low-level CAKE/SQM-like tuning.

The Gateway DNS page shows that TCP/UDP 53 is intercepted by default and DNS selection outranks ordinary PBR. Its rule editor contains only fixed line/upstream/proxy resolution, fixed answer, and NODATA actions—not generic NAT/route fields—and exposes matched rule, actual egress, upstream, TTL binding, failure reason, and blocked external-DNS bypass evidence.
