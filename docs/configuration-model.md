# LY-Route Configuration Expression Model

The normative model is maintained in [Chinese](zh/configuration-model.md).

Address matches are typed values, not ambiguous strings: explicit `any`, host, CIDR prefix, continuous range, IP-group reference, and—only where Gateway domain semantics exist—domain-group reference. Object references use stable IDs; ranges are same-family and ordered; expansion is deterministic and visible in preview.

The model also standardizes `any`/single/range/group port selectors, protocol selectors, interface/egress references, schedules, policy-group inherited conditions, object import diagnostics, cycle detection, usage tracking, and deletion protection.

Interface addresses remain host prefixes; they cannot be `any`, ranges, or groups. Orchestrator supports IP selectors and IP groups but retains its no-domain-object boundary. Legacy string fields require a versioned, non-guessing migration. Delivery is tracked by work packages C1/C2 in the [Work Plan](work-plan.md).

WAN-group mode is a closed enum: `primary_backup`, `weighted`, or `five_tuple`; members reference real WANs and reject proxy logical WANs at schema, API, import, and UI layers. A subscription-backed proxy egress carries a discriminated `fixed_node` or `fastest_node` selector. Fixed mode references a stable node ID; fastest mode references a candidate set and persists the effective node, probe time, rolling latency, health, and switch reason without leaking subscription content or secrets.

User rate-limit intent and built-in smart-QoS intent are separate. The former stores typed match conditions and explicit up/down rates. The latter stores enablement, bandwidth source/result, and read-only operational evidence; its qdisc/AQM tuning is not user-editable configuration.

DNS rules have stable IDs, explicit order, client selectors, and exact/suffix/domain-group selectors. Their discriminated action is `resolve_via`, `fixed_answer`, or `nodata`; `resolve_via` references a fixed WAN, upstream, or proxy and cannot contain generic NAT/route/next-hop fields. Default TCP/UDP 53 interception is system intent, not an optional user `any` rule. Answers create TTL-bound `{client_scope, domain, answer_ip, dns_egress_id, expires_at}` runtime bindings that are evaluated before ordinary PBR and atomically removed on expiry or invalidation. No schema switch enables hidden fallback.
