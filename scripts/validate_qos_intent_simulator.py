#!/usr/bin/env python3
"""Validate QoS intent simulator evidence stays deterministic and coherent."""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SIMULATOR_PATH = ROOT / "scripts" / "qos_intent_simulator.py"
EVIDENCE_PATH = ROOT / ".sisyphus" / "evidence" / "qos-simulation-default.json"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def load_simulator():
    spec = importlib.util.spec_from_file_location("qos_intent_simulator", SIMULATOR_PATH)
    require(spec is not None and spec.loader is not None, "failed to load simulator module spec")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def load_json(path: Path):
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def strategies_by_name(report: dict[str, object]) -> dict[str, dict[str, object]]:
    strategies = report["strategies"]
    require(isinstance(strategies, list), "strategies must be a list")
    named = {item["strategy"]: item for item in strategies}
    require(len(named) == len(strategies), "strategy names must be unique")
    return named


def validate_packet_accounting(strategy: dict[str, object]) -> None:
    metrics = strategy["metrics_by_class"]
    require(isinstance(metrics, dict), "metrics_by_class must be an object")
    for traffic_class, values in metrics.items():
        generated = values["generated_packets"]
        accounted = values["delivered_packets"] + values["dropped_packets"] + values["pending_packets"]
        require(
            generated == accounted,
            f"{strategy['strategy']} {traffic_class}: generated packets do not match delivered+dropped+pending",
        )
        require(values["pending_bytes"] >= 0, f"{strategy['strategy']} {traffic_class}: pending bytes negative")


def validate_report(report: dict[str, object]) -> None:
    require(report["scenario"] == "saturated_link_im_office_gaming_protection", "scenario changed")
    require(report["duration_ms"] == 10_000, "default duration changed")
    require(report["link_mbps"] == 100.0, "default link speed changed")
    require(report["offered_mbps"] > report["link_mbps"], "default scenario must be saturated")

    strategies = strategies_by_name(report)
    expected = {"no_qos", "vpp_default_mark_and_police", "capability_gated_shaper_scheduler_aqm"}
    require(set(strategies) == expected, "strategy set changed")

    for strategy in strategies.values():
        validate_packet_accounting(strategy)

    no_qos = strategies["no_qos"]["goal_evaluation"]
    production = strategies["vpp_default_mark_and_police"]["goal_evaluation"]
    research = strategies["capability_gated_shaper_scheduler_aqm"]["goal_evaluation"]

    require(no_qos["passed"] is False, "baseline unexpectedly passed")
    require(production["passed"] is False, "production mark-and-police unexpectedly passed")
    require(research["passed"] is True, "capability-gated research model must pass default goals")
    require(
        production["observed"]["link_utilization_percent"] < production["thresholds"]["link_utilization_percent_min"],
        "production default should fail because it protects latency by underfilling the link",
    )

    alignment = report["contract_alignment"]
    require(alignment["production_algorithm"] == "vpp_intent_class_qos", "production algorithm changed")
    require("fq_codel" in alignment["not_production_defaults"], "FQ-CoDel must remain non-production by default")
    require("cake" in alignment["not_production_defaults"], "CAKE must remain non-production by default")


def main() -> int:
    simulator = load_simulator()
    report = simulator.build_report(10_000, 100.0)
    validate_report(report)
    require(load_json(EVIDENCE_PATH) == report, "QoS evidence is stale; regenerate qos-simulation-default.json")
    print("validated QoS intent simulator and default evidence")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
