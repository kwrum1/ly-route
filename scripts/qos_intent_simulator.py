#!/usr/bin/env python3
"""Deterministic QoS intent simulator for contract evidence.

This is intentionally not a dataplane implementation. It compares the current
VPP-first production contract against a capability-gated SQM-style research
model so we can reason about saturation behavior without claiming production
queueing support that is not contracted yet.
"""

from __future__ import annotations

import argparse
import json
from collections import defaultdict, deque
from dataclasses import dataclass
from pathlib import Path
from typing import Deque, Iterable


CLASS_ORDER = ["control", "interactive", "business_critical", "default", "bulk", "abusive"]

VPP_PRODUCTION_TARGETS = [
    "vpp.qos.classify",
    "vpp.qos.record",
    "vpp.qos.store",
    "vpp.qos.egress-map",
    "vpp.qos.mark",
    "vpp.policer",
]

CAPABILITY_GATED_TARGETS = [
    "vpp.qos.adapter-shaper",
    "vpp.qos.adapter-queue-reservation",
    "vpp.dpdk.hqos-wred",
]


@dataclass(frozen=True)
class FlowProfile:
    name: str
    traffic_class: str
    packet_bytes: int
    interval_ms: int
    offset_ms: int = 0


@dataclass(frozen=True)
class Packet:
    flow: str
    traffic_class: str
    size: int
    created_ms: int


@dataclass
class TokenBucket:
    rate_mbps: float
    burst_ms: int
    tokens: float = 0.0

    @property
    def rate_bytes_per_ms(self) -> float:
        return self.rate_mbps * 125.0

    @property
    def burst_bytes(self) -> float:
        return self.rate_bytes_per_ms * self.burst_ms

    def refill(self) -> None:
        self.tokens = min(self.tokens + self.rate_bytes_per_ms, self.burst_bytes)

    def allow(self, packet_size: int) -> bool:
        if self.tokens < packet_size:
            return False
        self.tokens -= packet_size
        return True


def default_flows() -> list[FlowProfile]:
    flows = [
        FlowProfile("router_keepalive", "control", 128, 100),
        FlowProfile("im_chat", "interactive", 220, 40, 3),
        FlowProfile("game_udp", "interactive", 180, 16, 7),
        FlowProfile("office_saas", "business_critical", 900, 20, 11),
        FlowProfile("web_browsing", "default", 1200, 2),
        FlowProfile("video_stream", "default", 1400, 1, 1),
    ]
    for index in range(10):
        flows.append(FlowProfile(f"bulk_download_{index + 1}", "bulk", 1500, 1, index % 3))
    for index in range(2):
        flows.append(FlowProfile(f"cloud_sync_{index + 1}", "bulk", 1500, 2, index))
    return flows


def generated_packets(flows: Iterable[FlowProfile], now_ms: int) -> list[Packet]:
    packets: list[Packet] = []
    for flow in flows:
        if now_ms >= flow.offset_ms and (now_ms - flow.offset_ms) % flow.interval_ms == 0:
            packets.append(Packet(flow.name, flow.traffic_class, flow.packet_bytes, now_ms))
    return packets


def percentile(values: list[float], pct: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = int(round((pct / 100.0) * (len(ordered) - 1)))
    return round(ordered[index], 3)


def jitter_p95(delays: list[float]) -> float | None:
    if len(delays) < 2:
        return None
    deltas = [abs(delays[index] - delays[index - 1]) for index in range(1, len(delays))]
    return percentile(deltas, 95)


def empty_stats() -> dict[str, dict[str, float | int | None]]:
    return {
        item: {
            "generated_packets": 0,
            "delivered_packets": 0,
            "dropped_packets": 0,
            "pending_packets": 0,
            "pending_bytes": 0,
            "delivered_mbps": 0.0,
            "p50_delay_ms": None,
            "p95_delay_ms": None,
            "max_delay_ms": None,
            "p95_jitter_ms": None,
        }
        for item in CLASS_ORDER
    }


def summarize(
    generated: dict[str, int],
    dropped: dict[str, int],
    delivered_bytes: dict[str, int],
    delays: dict[str, list[float]],
    pending: dict[str, tuple[int, int]],
    duration_ms: int,
) -> dict[str, dict[str, float | int | None]]:
    stats = empty_stats()
    seconds = duration_ms / 1000.0
    for traffic_class in CLASS_ORDER:
        class_delays = delays[traffic_class]
        stats[traffic_class].update(
            {
                "generated_packets": generated[traffic_class],
                "delivered_packets": len(class_delays),
                "dropped_packets": dropped[traffic_class],
                "pending_packets": pending[traffic_class][0],
                "pending_bytes": pending[traffic_class][1],
                "delivered_mbps": round((delivered_bytes[traffic_class] * 8.0) / seconds / 1_000_000, 3),
                "p50_delay_ms": percentile(class_delays, 50),
                "p95_delay_ms": percentile(class_delays, 95),
                "max_delay_ms": round(max(class_delays), 3) if class_delays else None,
                "p95_jitter_ms": jitter_p95(class_delays),
            }
        )
    return stats


def total_delivered_mbps(stats: dict[str, dict[str, float | int | None]]) -> float:
    return round(sum(float(item["delivered_mbps"] or 0.0) for item in stats.values()), 3)


def evaluate_goals(stats: dict[str, dict[str, float | int | None]], link_mbps: float) -> dict[str, object]:
    interactive_p95 = stats["interactive"]["p95_delay_ms"]
    interactive_jitter = stats["interactive"]["p95_jitter_ms"]
    business_p95 = stats["business_critical"]["p95_delay_ms"]
    delivered_mbps = total_delivered_mbps(stats)
    utilization_percent = round((delivered_mbps / link_mbps) * 100.0, 3)
    checks = {
        "interactive_p95_under_50ms": interactive_p95 is not None and interactive_p95 <= 50,
        "interactive_jitter_under_20ms": interactive_jitter is not None and interactive_jitter <= 20,
        "business_p95_under_150ms": business_p95 is not None and business_p95 <= 150,
        "link_utilization_at_least_90_percent": utilization_percent >= 90,
    }
    return {
        "passed": all(checks.values()),
        "checks": checks,
        "observed": {
            "total_delivered_mbps": delivered_mbps,
            "link_utilization_percent": utilization_percent,
        },
        "thresholds": {
            "interactive_p95_ms_max": 50,
            "interactive_jitter_p95_ms_max": 20,
            "business_critical_p95_ms_max": 150,
            "link_utilization_percent_min": 90,
        },
    }


def require_expected_report(report: dict[str, object]) -> None:
    strategies_value = report.get("strategies")
    if not isinstance(strategies_value, list):
        raise SystemExit("report is missing strategies")

    strategies: dict[str, dict[str, object]] = {}
    for item in strategies_value:
        if not isinstance(item, dict):
            raise SystemExit("strategy entry must be an object")
        name = item.get("strategy")
        if not isinstance(name, str):
            raise SystemExit("strategy entry is missing a name")
        strategies[name] = item

    expected_pass = {
        "no_qos": False,
        "vpp_default_mark_and_police": False,
        "capability_gated_shaper_scheduler_aqm": True,
    }
    missing = sorted(set(expected_pass) - set(strategies))
    if missing:
        raise SystemExit(f"report is missing strategies: {', '.join(missing)}")

    for name, should_pass in expected_pass.items():
        goal = strategies[name].get("goal_evaluation")
        if not isinstance(goal, dict):
            raise SystemExit(f"{name}: missing goal_evaluation")
        passed = goal.get("passed")
        if passed is not should_pass:
            raise SystemExit(f"{name}: expected passed={should_pass}, got {passed}")

        metrics = strategies[name].get("metrics_by_class")
        if not isinstance(metrics, dict):
            raise SystemExit(f"{name}: missing metrics_by_class")
        for traffic_class in CLASS_ORDER:
            stats = metrics.get(traffic_class)
            if not isinstance(stats, dict):
                raise SystemExit(f"{name}.{traffic_class}: missing class metrics")
            generated = stats.get("generated_packets")
            delivered = stats.get("delivered_packets")
            dropped = stats.get("dropped_packets")
            pending = stats.get("pending_packets")
            if not all(isinstance(item, int) for item in [generated, delivered, dropped, pending]):
                raise SystemExit(f"{name}.{traffic_class}: packet counters must be integers")
            if generated != delivered + dropped + pending:
                raise SystemExit(
                    f"{name}.{traffic_class}: generated={generated} does not equal "
                    f"delivered+dropped+pending={delivered + dropped + pending}"
                )


def enqueue_with_limit(
    queue: Deque[Packet],
    packet: Packet,
    queue_limit_bytes: int,
    queued_bytes: int,
) -> tuple[int, bool]:
    if queued_bytes + packet.size > queue_limit_bytes:
        return queued_bytes, False
    queue.append(packet)
    return queued_bytes + packet.size, True


def pending_from_queue(queue: Deque[Packet]) -> dict[str, tuple[int, int]]:
    pending_counts = defaultdict(int)
    pending_bytes = defaultdict(int)
    for packet in queue:
        pending_counts[packet.traffic_class] += 1
        pending_bytes[packet.traffic_class] += packet.size
    return {item: (pending_counts[item], pending_bytes[item]) for item in CLASS_ORDER}


def pending_from_class_queues(queues: dict[str, Deque[Packet]]) -> dict[str, tuple[int, int]]:
    return {
        traffic_class: (len(queue), sum(packet.size for packet in queue))
        for traffic_class, queue in queues.items()
    }


def run_fifo_strategy(strategy: str, duration_ms: int, link_mbps: float, flows: list[FlowProfile]) -> dict[str, object]:
    capacity_bytes_per_ms = link_mbps * 125.0
    queue: Deque[Packet] = deque()
    queued_bytes = 0
    queue_limit_bytes = 8_000_000 if strategy == "no_qos" else 1_000_000
    generated = defaultdict(int)
    dropped = defaultdict(int)
    delivered_bytes = defaultdict(int)
    delays: dict[str, list[float]] = defaultdict(list)
    policers = {
        "default": TokenBucket(link_mbps * 0.25, 20),
        "bulk": TokenBucket(link_mbps * 0.60, 10),
        "abusive": TokenBucket(link_mbps * 0.01, 5),
    }
    for bucket in policers.values():
        bucket.tokens = bucket.burst_bytes
    link_credit = 0.0

    for now_ms in range(duration_ms):
        link_credit = min(link_credit + capacity_bytes_per_ms, queue_limit_bytes)
        for bucket in policers.values():
            bucket.refill()
        for packet in generated_packets(flows, now_ms):
            generated[packet.traffic_class] += 1
            if strategy == "vpp_default_mark_and_police":
                bucket = policers.get(packet.traffic_class)
                if bucket and not bucket.allow(packet.size):
                    dropped[packet.traffic_class] += 1
                    continue
            queued_bytes, accepted = enqueue_with_limit(queue, packet, queue_limit_bytes, queued_bytes)
            if not accepted:
                dropped[packet.traffic_class] += 1

        while queue and queue[0].size <= link_credit:
            packet = queue.popleft()
            queued_bytes -= packet.size
            link_credit -= packet.size
            delays[packet.traffic_class].append(now_ms - packet.created_ms)
            delivered_bytes[packet.traffic_class] += packet.size

    return {
        "strategy": strategy,
        "capability_scope": "production_default" if strategy == "vpp_default_mark_and_police" else "baseline",
        "description": "single FIFO with no QoS" if strategy == "no_qos" else "VPP contract-aligned mark/record/store/egress-map plus selective policer simulation",
        "metrics_by_class": summarize(generated, dropped, delivered_bytes, delays, pending_from_queue(queue), duration_ms),
    }


def run_capability_gated_strategy(duration_ms: int, link_mbps: float, flows: list[FlowProfile]) -> dict[str, object]:
    capacity_bytes_per_ms = link_mbps * 125.0
    queue_limit_bytes = 1_000_000
    queues: dict[str, Deque[Packet]] = {item: deque() for item in CLASS_ORDER}
    queued_bytes = defaultdict(int)
    generated = defaultdict(int)
    dropped = defaultdict(int)
    delivered_bytes = defaultdict(int)
    delays: dict[str, list[float]] = defaultdict(list)
    policers = {
        "bulk": TokenBucket(link_mbps, 10),
        "abusive": TokenBucket(link_mbps * 0.01, 5),
    }
    for bucket in policers.values():
        bucket.tokens = bucket.burst_bytes
    service_order = ["control", "interactive", "business_critical", "default", "bulk", "abusive"]
    link_credit = 0.0

    for now_ms in range(duration_ms):
        link_credit = min(link_credit + capacity_bytes_per_ms, queue_limit_bytes)
        for bucket in policers.values():
            bucket.refill()
        for packet in generated_packets(flows, now_ms):
            generated[packet.traffic_class] += 1
            bucket = policers.get(packet.traffic_class)
            if bucket and not bucket.allow(packet.size):
                dropped[packet.traffic_class] += 1
                continue
            class_queue = queues[packet.traffic_class]
            if queued_bytes[packet.traffic_class] + packet.size > queue_limit_bytes:
                dropped[packet.traffic_class] += 1
                continue
            class_queue.append(packet)
            queued_bytes[packet.traffic_class] += packet.size

        made_progress = True
        while made_progress and link_credit > 0:
            made_progress = False
            for traffic_class in service_order:
                queue = queues[traffic_class]
                if queue and queue[0].size <= link_credit:
                    packet = queue.popleft()
                    queued_bytes[traffic_class] -= packet.size
                    link_credit -= packet.size
                    delays[traffic_class].append(now_ms - packet.created_ms)
                    delivered_bytes[traffic_class] += packet.size
                    made_progress = True
                    if link_credit <= 0:
                        break

    return {
        "strategy": "capability_gated_shaper_scheduler_aqm",
        "capability_scope": "research_only_capability_gate",
        "description": "SQM-style comparison model with class queues, priority service, bulk policing, and local bottleneck control; not a stock VPP production claim",
        "metrics_by_class": summarize(generated, dropped, delivered_bytes, delays, pending_from_class_queues(queues), duration_ms),
    }


def build_report(duration_ms: int, link_mbps: float) -> dict[str, object]:
    flows = default_flows()
    offered_mbps = sum((flow.packet_bytes * 8.0 / flow.interval_ms) / 1000.0 for flow in flows)
    strategies = [
        run_fifo_strategy("no_qos", duration_ms, link_mbps, flows),
        run_fifo_strategy("vpp_default_mark_and_police", duration_ms, link_mbps, flows),
        run_capability_gated_strategy(duration_ms, link_mbps, flows),
    ]
    for strategy in strategies:
        strategy["goal_evaluation"] = evaluate_goals(strategy["metrics_by_class"], link_mbps)

    return {
        "scenario": "saturated_link_im_office_gaming_protection",
        "duration_ms": duration_ms,
        "link_mbps": link_mbps,
        "offered_mbps": round(offered_mbps, 3),
        "classes": CLASS_ORDER,
        "flows": [flow.__dict__ for flow in flows],
        "contract_alignment": {
            "production_algorithm": "vpp_intent_class_qos",
            "production_targets": VPP_PRODUCTION_TARGETS,
            "capability_gated_targets": CAPABILITY_GATED_TARGETS,
            "not_production_defaults": ["codel", "fq_codel", "cake", "linux_tc_qdisc", "wred", "dpdk_hqos"],
            "ui_rule": "Expose business intent, protection status, and degraded state; keep queue/AQM internals hidden.",
        },
        "strategies": strategies,
        "recommendation": {
            "production_first_step": "Compile business intent to VPP classify/record/store/egress-map/mark and selective policer.",
            "research_next_step": "Use capability-gated shaping/scheduler/AQM evidence only if mark-and-police cannot satisfy latency and utilization goals under saturation.",
            "must_not_claim": "This simulation does not prove stock VPP has CAKE/FQ-CoDel/queue reservation/shaping in the production path.",
        },
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run QoS intent simulation evidence.")
    parser.add_argument("--duration-ms", type=int, default=10_000)
    parser.add_argument("--link-mbps", type=float, default=100.0)
    parser.add_argument("--output", type=Path, default=None, help="Write JSON evidence to this path; stdout when omitted.")
    parser.add_argument(
        "--require-pass",
        action="store_true",
        help="Fail unless the expected saturated-link simulation outcome is preserved.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.duration_ms <= 0:
        raise SystemExit("--duration-ms must be positive")
    if args.link_mbps <= 0:
        raise SystemExit("--link-mbps must be positive")
    report = build_report(args.duration_ms, args.link_mbps)
    if args.require_pass:
        require_expected_report(report)
    content = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(content, encoding="utf-8")
    else:
        print(content, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
