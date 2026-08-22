#!/usr/bin/env python3
"""Reset-aware analysis for fixed-interval PIG serving observations."""

from __future__ import annotations

import csv
import math
import statistics
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping, Sequence


Row = Mapping[str, str]
HORIZONS: dict[str, tuple[float, int, float]] = {
    # name: (wall-clock horizon, minimum samples, intended sample interval)
    "release": (30 * 60, 360, 5),
    "stability": (6 * 60 * 60, 720, 30),
    "delayed": (24 * 60 * 60, 1440, 60),
}
VLLM_SOURCE_MAPPING = {
    "backend_family": "vllm",
    "schema": "pig.observer-csv.vllm.v1",
    "generation_tokens": "vllm:generation_tokens_total (sum series)",
    "preemptions": "vllm:num_preemptions_total (sum series)",
    "running": "vllm:num_requests_running (sum series)",
    "waiting": "vllm:num_requests_waiting (sum series)",
    "kv_usage": "vllm:kv_cache_usage_perc (maximum series)",
    "cache_queries": "vllm:prefix_cache_queries_total (sum series)",
    "cache_hits": "vllm:prefix_cache_hits_total (sum series)",
    "successful_completion_goodput": None,
}
RUNTIME_SERVICE_COMPLETE_FIELDS = (
    "pig_metrics_ok",
    "vllm_metrics_ok",
    "gpu_ok",
    "containers_ok",
)
COMPLETE_FIELDS = RUNTIME_SERVICE_COMPLETE_FIELDS + ("router_ok",)
RUNTIME_IDENTITY_FIELDS = (
    "compose_sha256",
    "pig_container_id",
    "pig_started_at",
    "vllm_container_id",
    "vllm_started_at",
    "haproxy_container_id",
    "haproxy_started_at",
    "ingress_container_id",
    "ingress_started_at",
    "pig_tps_reference",
)
PIG_COUNTER_IDENTITY = (
    "compose_sha256",
    "pig_container_id",
    "pig_started_at",
)
VLLM_COUNTER_IDENTITY = (
    "compose_sha256",
    "vllm_container_id",
    "vllm_started_at",
)
ROUTER_COUNTER_IDENTITY = ("compose_sha256",)
ROUTER_IDENTITY_FIELDS = ("router_config_digest",)


def number(value: str | None) -> float:
    if value is None or value == "":
        return math.nan
    try:
        return float(value)
    except ValueError:
        return math.nan


def boolean(value: str | None) -> bool:
    return str(value).strip().lower() in {"1", "1.0", "true"}


def finite(value: str | None) -> bool:
    return math.isfinite(number(value))


def percentile(values: Sequence[float], quantile: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    if len(ordered) == 1:
        return ordered[0]
    position = quantile * (len(ordered) - 1)
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    weight = position - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def distribution(values: Iterable[float]) -> dict[str, float | int | None]:
    finite_values = [value for value in values if math.isfinite(value)]
    if not finite_values:
        return {"count": 0, "mean": None, "p05": None, "p50": None, "p95": None,
                "min": None, "max": None}
    return {
        "count": len(finite_values),
        "mean": statistics.fmean(finite_values),
        "p05": percentile(finite_values, 0.05),
        "p50": percentile(finite_values, 0.50),
        "p95": percentile(finite_values, 0.95),
        "min": min(finite_values),
        "max": max(finite_values),
    }


def identity(row: Row, fields: Sequence[str]) -> tuple[str, ...]:
    return tuple(str(row.get(field, "")) for field in fields)


def identity_transition_count(rows: Sequence[Row], fields: Sequence[str]) -> int:
    return sum(
        identity(previous, fields) != identity(current, fields)
        for previous, current in zip(rows, rows[1:])
    )


@dataclass(frozen=True)
class CounterDelta:
    field: str
    delta: float
    covered_seconds: float
    accepted_intervals: int
    missing_intervals: int
    reset_intervals: int
    identity_change_intervals: int

    @property
    def rate(self) -> float | None:
        if self.covered_seconds <= 0:
            return None
        return self.delta / self.covered_seconds

    def as_dict(self) -> dict[str, float | int | str | None]:
        return {
            "field": self.field,
            "delta": self.delta,
            "covered_seconds": self.covered_seconds,
            "rate_per_second": self.rate,
            "accepted_intervals": self.accepted_intervals,
            "missing_intervals": self.missing_intervals,
            "reset_intervals": self.reset_intervals,
            "identity_change_intervals": self.identity_change_intervals,
        }


def counter_delta(
    rows: Sequence[Row],
    field: str,
    identity_fields: Sequence[str],
    validity_fields: Sequence[str] = (),
) -> CounterDelta:
    total = 0.0
    covered = 0.0
    accepted = missing = resets = identity_changes = 0
    for previous, current in zip(rows, rows[1:]):
        if validity_fields and not all(
            boolean(row.get(validity))
            for row in (previous, current)
            for validity in validity_fields
        ):
            missing += 1
            continue
        elapsed = number(current.get("elapsed_seconds")) - number(previous.get("elapsed_seconds"))
        left = number(previous.get(field))
        right = number(current.get(field))
        if not all(math.isfinite(value) for value in (elapsed, left, right)) or elapsed <= 0:
            missing += 1
            continue
        if identity(previous, identity_fields) != identity(current, identity_fields):
            identity_changes += 1
            continue
        if right < left:
            resets += 1
            continue
        total += right - left
        covered += elapsed
        accepted += 1
    return CounterDelta(field, total, covered, accepted, missing, resets, identity_changes)


def fixed_gauge_delta(rows: Sequence[Row], field: str) -> float | None:
    values = [number(row.get(field)) for row in rows if finite(row.get(field))]
    if len(values) < 2:
        return None
    return values[-1] - values[0]


def gauge_distribution(rows: Sequence[Row], field: str) -> dict[str, float | int | None]:
    return distribution(number(row.get(field)) for row in rows)


def occupancy(rows: Sequence[Row], predicate: Callable[[Row], bool]) -> float | None:
    if not rows:
        return None
    return sum(1 for row in rows if predicate(row)) / len(rows)


def longest_duration(
    rows: Sequence[Row],
    predicate: Callable[[Row], bool],
) -> float:
    longest = current = 0.0
    for previous, row in zip(rows, rows[1:]):
        elapsed = number(row.get("elapsed_seconds")) - number(previous.get("elapsed_seconds"))
        if elapsed <= 0 or not math.isfinite(elapsed):
            current = 0.0
            continue
        if predicate(previous) and predicate(row):
            current += elapsed
            longest = max(longest, current)
        else:
            current = 0.0
    return longest


class ObservationWindow:
    """Validated CSV rows plus completeness and identity evidence."""

    def __init__(self, rows: Sequence[Row]) -> None:
        if len(rows) < 2:
            raise ValueError("at least two observation samples are required")
        self.rows = list(rows)
        self.complete_rows = [
            row for row in self.rows if all(boolean(row.get(field)) for field in COMPLETE_FIELDS)
        ]
        elapsed = [number(row.get("elapsed_seconds")) for row in self.rows]
        if any(not math.isfinite(value) for value in elapsed):
            raise ValueError("every sample must contain finite elapsed_seconds")
        if any(right <= left for left, right in zip(elapsed, elapsed[1:])):
            raise ValueError("elapsed_seconds must be strictly increasing")

    @classmethod
    def from_csv(cls, path: Path) -> "ObservationWindow":
        with path.open(newline="", encoding="utf-8") as handle:
            return cls(list(csv.DictReader(handle)))

    @property
    def duration_seconds(self) -> float:
        return number(self.rows[-1].get("elapsed_seconds")) - number(
            self.rows[0].get("elapsed_seconds")
        )

    @property
    def intervals(self) -> list[float]:
        return [
            number(current.get("elapsed_seconds")) - number(previous.get("elapsed_seconds"))
            for previous, current in zip(self.rows, self.rows[1:])
        ]

    @property
    def median_interval(self) -> float:
        return statistics.median(self.intervals)

    @property
    def nominal_interval(self) -> float:
        # median_low preserves the lower cadence for a two-interval gap test,
        # while one unusually fast scrape cannot make every normal interval
        # look late.
        return statistics.median_low(self.intervals)

    def identity_values(self, field: str) -> list[str]:
        return sorted({str(row.get(field, "")) for row in self.complete_rows})

    def identity_transitions(self) -> int:
        return identity_transition_count(self.complete_rows, RUNTIME_IDENTITY_FIELDS)

    def maximum_interval(self) -> float:
        return max(self.intervals)

    def sample_summary(self) -> dict[str, Any]:
        median = self.median_interval
        expected_from_span = round(self.duration_seconds / median) + 1
        return {
            "total": len(self.rows),
            "complete": len(self.complete_rows),
            "completeness_fraction": len(self.complete_rows) / len(self.rows),
            "duration_seconds": self.duration_seconds,
            "interval_seconds": distribution(self.intervals),
            "nominal_interval_seconds": self.nominal_interval,
            "expected_from_observed_span": expected_from_span,
            "missing_from_observed_span": max(0, expected_from_span - len(self.rows)),
            "maximum_interval_seconds": self.maximum_interval(),
            "started_at_utc": self.rows[0].get("timestamp_utc") or None,
            "ended_at_utc": self.rows[-1].get("timestamp_utc") or None,
        }


def analyze_counter_group(
    rows: Sequence[Row],
    fields: Sequence[str],
    identity_fields: Sequence[str],
    validity_fields: Sequence[str],
) -> dict[str, dict[str, float | int | str | None]]:
    return {
        field: counter_delta(rows, field, identity_fields, validity_fields).as_dict()
        for field in fields
    }


def counter_ratio(
    numerator: Mapping[str, float | int | str | None],
    denominator: Mapping[str, float | int | str | None],
) -> float | None:
    top = numerator.get("delta")
    bottom = denominator.get("delta")
    if not isinstance(top, (int, float)) or not isinstance(bottom, (int, float)):
        return None
    if not math.isfinite(float(top)) or not math.isfinite(float(bottom)) or bottom <= 0:
        return None
    return float(top) / float(bottom)


def overprotection_candidates(rows: Sequence[Row], reference: float) -> dict[str, Any]:
    count = 0
    duration = 0.0
    longest = current = 0.0
    for previous, row in zip(rows, rows[1:]):
        if not all(
            boolean(candidate.get(field))
            for candidate in (previous, row)
            for field in COMPLETE_FIELDS
        ):
            current = 0.0
            continue
        elapsed = number(row.get("elapsed_seconds")) - number(previous.get("elapsed_seconds"))
        attempts_before = number(previous.get("router_use1_19_attempts"))
        attempts_after = number(row.get("router_use1_19_attempts"))
        if not all(math.isfinite(value) for value in (elapsed, attempts_before, attempts_after)):
            current = 0.0
            continue
        offered_demand = attempts_after > attempts_before
        mean_tps = number(row.get("pig_tps_window_mean_active"))
        candidate = (
            offered_demand
            and boolean(row.get("pig_backpressure_active"))
            and boolean(row.get("pig_tps_window_ready"))
            and math.isfinite(mean_tps)
            and mean_tps >= reference
            and number(row.get("gpu_utilization_percent")) < 40
            and number(row.get("vllm_waiting")) == 0
            and number(row.get("vllm_kv_usage_fraction")) < 0.20
            and str(row.get("pig_backpressure_scope", "")) == "load"
        )
        if candidate:
            count += 1
            duration += elapsed
            current += elapsed
            longest = max(longest, current)
        else:
            current = 0.0
    return {
        "candidate_intervals": count,
        "candidate_duration_seconds": duration,
        "longest_candidate_duration_seconds": longest,
        "interpretation": (
            "screening signal only; request-fit and matched-cohort evidence are required"
        ),
    }


def checkpoint_qualification(
    window: ObservationWindow,
    horizon: str | None,
    integrity_stop_reasons: Sequence[str],
) -> dict[str, Any]:
    if horizon not in HORIZONS:
        return {
            "horizon": horizon or "unscoped",
            "formal_checkpoint_eligible": False,
            "qualification_reasons": ["horizon_unscoped"],
        }
    required_duration, minimum_samples, intended_interval = HORIZONS[horizon]
    reasons = list(integrity_stop_reasons)
    if len(window.rows) < minimum_samples:
        reasons.append("insufficient_samples")
    # A collector samples at t=0 and stops at the horizon. The last sample is
    # one intended interval before stop, so this is the maximum honest grace.
    minimum_span = required_duration - intended_interval
    if window.duration_seconds < minimum_span:
        reasons.append("insufficient_observed_span")
    if window.median_interval > intended_interval * 1.5:
        reasons.append("sampling_cadence_too_slow")
    return {
        "horizon": horizon,
        "required_duration_seconds": required_duration,
        "minimum_samples": minimum_samples,
        "intended_interval_seconds": intended_interval,
        "observed_duration_seconds": window.duration_seconds,
        "observed_samples": len(window.rows),
        "formal_checkpoint_eligible": not reasons,
        "qualification_reasons": reasons,
    }


def analyze(window: ObservationWindow, horizon: str | None = None) -> dict[str, Any]:
    rows = window.complete_rows
    all_rows = window.rows
    runtime_rows = [
        row
        for row in all_rows
        if all(boolean(row.get(field)) for field in RUNTIME_SERVICE_COMPLETE_FIELDS)
    ]
    router_rows = [row for row in all_rows if boolean(row.get("router_ok"))]
    references = [number(row.get("pig_tps_reference")) for row in rows]
    references = [value for value in references if math.isfinite(value)]
    reference = statistics.median(references) if references else 0.0
    ready_under_load = [
        row
        for row in rows
        if boolean(row.get("pig_tps_window_ready")) and number(row.get("vllm_running")) > 0
    ]
    below_reference = lambda row: (
        number(row.get("pig_tps_window_mean_active")) < reference
    )

    pig_counters = analyze_counter_group(
        all_rows,
        (
            "pig_accepted_total",
            "pig_completed_total",
            "pig_failed_total",
            "pig_proxy_errors_total",
            "pig_enforced_rejects_total",
            "pig_fit_total",
            "pig_risk_total",
        ),
        PIG_COUNTER_IDENTITY,
        ("pig_metrics_ok", "containers_ok"),
    )
    vllm_counters = analyze_counter_group(
        all_rows,
        (
            "vllm_generation_tokens_total",
            "vllm_preemptions_total",
            "vllm_prefix_queries_total",
            "vllm_prefix_hits_total",
        ),
        VLLM_COUNTER_IDENTITY,
        ("vllm_metrics_ok", "containers_ok"),
    )
    router_counters = analyze_counter_group(
        all_rows,
        (
            "router_use1_19_processed",
            "router_use1_19_attempts",
            "router_use1_19_429",
            "router_use1_19_selected_cache",
            "router_use1_19_selected_load",
            "router_use1_19_selected_order",
        ),
        ROUTER_COUNTER_IDENTITY,
        ("router_ok",),
    )
    latency_counters = analyze_counter_group(
        all_rows,
        (
            "pig_prediction_count",
            "pig_prediction_sum_seconds",
            "pig_body_read_count",
            "pig_body_read_sum_seconds",
            "pig_estimator_count",
            "pig_estimator_sum_seconds",
            "pig_pre_forward_count",
            "pig_pre_forward_sum_seconds",
        ),
        PIG_COUNTER_IDENTITY,
        ("pig_metrics_ok", "containers_ok"),
    )
    raw_generation = vllm_counters["vllm_generation_tokens_total"]
    query_delta = vllm_counters["vllm_prefix_queries_total"]["delta"]
    hit_delta = vllm_counters["vllm_prefix_hits_total"]["delta"]
    cache_share = hit_delta / query_delta if query_delta > 0 else None

    restart_fields = ("pig_restarts", "vllm_restarts", "haproxy_restarts", "ingress_restarts")
    restart_deltas = {
        field: fixed_gauge_delta(runtime_rows, field) for field in restart_fields
    }
    new_restarts = {
        field: delta for field, delta in restart_deltas.items() if delta is not None and delta > 0
    }
    oom_fields = ("pig_oom", "vllm_oom", "haproxy_oom", "ingress_oom")
    observed_oom = {
        field: any(boolean(row.get(field)) for row in runtime_rows) for field in oom_fields
    }
    runtime_identity_transitions = identity_transition_count(
        runtime_rows, RUNTIME_IDENTITY_FIELDS
    )
    router_identity_keys_present = any(
        field in row for row in router_rows for field in ROUTER_IDENTITY_FIELDS
    )
    if not router_identity_keys_present:
        router_identity_status = "not_collected"
        router_identity_transitions = 0
    elif any(
        str(row.get(field, "")) == ""
        for row in router_rows
        for field in ROUTER_IDENTITY_FIELDS
    ):
        router_identity_status = "incomplete"
        router_identity_transitions = 0
    else:
        router_identity_status = "collected"
        router_identity_transitions = identity_transition_count(
            router_rows, ROUTER_IDENTITY_FIELDS
        )
    formal_stop_reasons: list[str] = []
    if len(window.complete_rows) != len(window.rows):
        formal_stop_reasons.append("incomplete_samples")
    if window.maximum_interval() > window.nominal_interval * 1.5:
        formal_stop_reasons.append("sample_gap")
    if runtime_identity_transitions:
        formal_stop_reasons.append("runtime_identity_changed")
    if router_identity_status == "incomplete":
        formal_stop_reasons.append("router_identity_incomplete")
    elif router_identity_transitions:
        formal_stop_reasons.append("router_identity_changed")
    if new_restarts:
        formal_stop_reasons.append("container_restarted")
    if any(observed_oom.values()):
        formal_stop_reasons.append("oom_observed")
    non_running = sorted(
        {
            field.removesuffix("_status")
            for field in ("pig_status", "vllm_status", "haproxy_status", "ingress_status")
            if any(
                str(row.get(field, "")).lower() != "running" for row in runtime_rows
            )
        }
    )
    if non_running:
        formal_stop_reasons.append("container_not_running")
    missing_identity_fields = sorted(
        {
            field
            for field in RUNTIME_IDENTITY_FIELDS
            if any(str(row.get(field, "")) == "" for row in runtime_rows)
        }
    )
    if missing_identity_fields:
        formal_stop_reasons.append("runtime_identity_incomplete")
    critical_counters = {
        "pig": {
            key: pig_counters[key]
            for key in (
                "pig_accepted_total",
                "pig_completed_total",
                "pig_failed_total",
                "pig_proxy_errors_total",
                "pig_enforced_rejects_total",
            )
        },
        "backend": {
            key: vllm_counters[key]
            for key in ("vllm_generation_tokens_total", "vllm_preemptions_total")
        },
        "router": {
            key: router_counters[key]
            for key in (
                "router_use1_19_processed",
                "router_use1_19_attempts",
                "router_use1_19_429",
            )
        },
    }
    reset_fields = sorted(
        key
        for group in critical_counters.values()
        for key, result in group.items()
        if int(result["reset_intervals"] or 0) > 0
    )
    missing_counter_fields = sorted(
        key
        for group in critical_counters.values()
        for key, result in group.items()
        if int(result["accepted_intervals"] or 0) == 0
        and int(result["missing_intervals"] or 0) > 0
    )
    if reset_fields:
        formal_stop_reasons.append("critical_counter_reset")
    if missing_counter_fields:
        formal_stop_reasons.append("critical_metric_missing")

    runtime_critical_counters = {
        group: critical_counters[group] for group in ("pig", "backend")
    }
    runtime_reset_fields = sorted(
        key
        for group in runtime_critical_counters.values()
        for key, result in group.items()
        if int(result["reset_intervals"] or 0) > 0
    )
    runtime_missing_counter_fields = sorted(
        key
        for group in runtime_critical_counters.values()
        for key, result in group.items()
        if int(result["accepted_intervals"] or 0) == 0
        and int(result["missing_intervals"] or 0) > 0
    )
    router_reset_fields = sorted(
        key
        for key, result in critical_counters["router"].items()
        if int(result["reset_intervals"] or 0) > 0
    )
    router_missing_counter_fields = sorted(
        key
        for key, result in critical_counters["router"].items()
        if int(result["accepted_intervals"] or 0) == 0
        and int(result["missing_intervals"] or 0) > 0
    )

    runtime_service_stop_reasons: list[str] = []
    if len(runtime_rows) != len(all_rows):
        runtime_service_stop_reasons.append("runtime_service_samples_incomplete")
    if window.maximum_interval() > window.nominal_interval * 1.5:
        runtime_service_stop_reasons.append("sample_gap")
    if runtime_identity_transitions:
        runtime_service_stop_reasons.append("runtime_identity_changed")
    if new_restarts:
        runtime_service_stop_reasons.append("container_restarted")
    if any(observed_oom.values()):
        runtime_service_stop_reasons.append("oom_observed")
    if non_running:
        runtime_service_stop_reasons.append("container_not_running")
    if missing_identity_fields:
        runtime_service_stop_reasons.append("runtime_identity_incomplete")
    if runtime_reset_fields:
        runtime_service_stop_reasons.append("critical_counter_reset")
    if runtime_missing_counter_fields:
        runtime_service_stop_reasons.append("critical_metric_missing")

    matched_routing_stop_reasons = list(runtime_service_stop_reasons)
    if len(router_rows) != len(all_rows):
        matched_routing_stop_reasons.append("router_samples_incomplete")
    if router_identity_status == "incomplete":
        matched_routing_stop_reasons.append("router_identity_incomplete")
    elif router_identity_transitions:
        matched_routing_stop_reasons.append("router_identity_changed")
    if router_reset_fields:
        matched_routing_stop_reasons.append("router_counter_reset")
    if router_missing_counter_fields:
        matched_routing_stop_reasons.append("router_metric_missing")

    checkpoint = checkpoint_qualification(window, horizon, formal_stop_reasons)

    known_predictive_decisions = (
        float(pig_counters["pig_fit_total"]["delta"] or 0)
        + float(pig_counters["pig_risk_total"]["delta"] or 0)
    )
    enforced_protections = float(
        pig_counters["pig_enforced_rejects_total"]["delta"] or 0
    )
    protection_share = (
        enforced_protections / known_predictive_decisions
        if known_predictive_decisions > 0
        else None
    )

    return {
        "schema_version": "pig.observation-analysis.v1",
        "source_mapping": VLLM_SOURCE_MAPPING,
        "checkpoint": checkpoint,
        "samples": window.sample_summary(),
        "qos": {
            "tps_reference": reference,
            "ready_under_load_samples": len(ready_under_load),
            "controller_trailing_mean_active_tps": gauge_distribution(
                ready_under_load, "pig_tps_window_mean_active"
            ),
            "ready_under_load_below_reference_fraction": occupancy(
                ready_under_load, below_reference
            ),
            "ready_under_load_below_reference_longest_seconds": longest_duration(
                all_rows,
                lambda row: all(boolean(row.get(field)) for field in COMPLETE_FIELDS)
                and boolean(row.get("pig_tps_window_ready"))
                and number(row.get("vllm_running")) > 0
                and below_reference(row),
            ),
            "preemptions": vllm_counters["vllm_preemptions_total"],
            "waiting": gauge_distribution(rows, "vllm_waiting"),
        },
        "throughput": {
            "successful_completion_goodput": None,
            "successful_completion_goodput_unavailable_reason": (
                "the collected backend generation counter is not success-linked"
            ),
            "raw_generation_throughput": raw_generation,
            "proxy_completed_requests": pig_counters["pig_completed_total"],
            "proxy_failed_requests": pig_counters["pig_failed_total"],
            "proxy_errors": pig_counters["pig_proxy_errors_total"],
        },
        "admission": {
            "pig": pig_counters,
            "router": router_counters,
            "known_predictive_decisions": known_predictive_decisions,
            "enforced_protections": enforced_protections,
            "protection_share_of_known_decisions": protection_share,
            "protection_share_denominator_note": (
                "fit plus risk decisions; the legacy observer CSV has no unknown-decision field"
            ),
            "backpressure_duty_cycle": occupancy(
                rows, lambda row: boolean(row.get("pig_backpressure_active"))
            ),
            "overprotection_screen": overprotection_candidates(all_rows, reference),
        },
        "pre_forward_latency": {
            "prediction_mean_seconds": counter_ratio(
                latency_counters["pig_prediction_sum_seconds"],
                latency_counters["pig_prediction_count"],
            ),
            "body_read_mean_seconds": counter_ratio(
                latency_counters["pig_body_read_sum_seconds"],
                latency_counters["pig_body_read_count"],
            ),
            "estimator_mean_seconds": counter_ratio(
                latency_counters["pig_estimator_sum_seconds"],
                latency_counters["pig_estimator_count"],
            ),
            "total_pre_forward_mean_seconds": counter_ratio(
                latency_counters["pig_pre_forward_sum_seconds"],
                latency_counters["pig_pre_forward_count"],
            ),
            "p95_p99": None,
            "p95_p99_unavailable_reason": (
                "the legacy observer CSV collected histogram count and sum but not buckets"
            ),
            "counters": latency_counters,
        },
        "resources": {
            "gpu_utilization_percent": gauge_distribution(rows, "gpu_utilization_percent"),
            "gpu_memory_used_mib": gauge_distribution(rows, "gpu_memory_used_mib"),
            "kv_usage_fraction": gauge_distribution(rows, "vllm_kv_usage_fraction"),
            "running": gauge_distribution(rows, "vllm_running"),
        },
        "cache": {
            "query_tokens": vllm_counters["vllm_prefix_queries_total"],
            "hit_tokens": vllm_counters["vllm_prefix_hits_total"],
            "backend_hit_share": cache_share,
            "pig_valid_fraction": occupancy(rows, lambda row: boolean(row.get("pig_cache_valid"))),
            "pig_hit_fraction_when_valid": gauge_distribution(
                [row for row in rows if boolean(row.get("pig_cache_valid"))],
                "pig_cache_hit_fraction",
            ),
        },
        "runtime_integrity": {
            "integrity_eligible": not formal_stop_reasons,
            "formal_stop_reasons": formal_stop_reasons,
            "runtime_identity_transitions": runtime_identity_transitions,
            "restart_deltas": restart_deltas,
            "oom_observed": observed_oom,
            "non_running_components": non_running,
            "missing_identity_fields": missing_identity_fields,
            "critical_counter_resets": reset_fields,
            "missing_critical_counter_fields": missing_counter_fields,
            "critical_counter_integrity": critical_counters,
            "compose_sha256_values": window.identity_values("compose_sha256"),
            "pig_container_ids": window.identity_values("pig_container_id"),
            "vllm_container_ids": window.identity_values("vllm_container_id"),
            "vllm_started_at_values": window.identity_values("vllm_started_at"),
        },
        "component_integrity": {
            "runtime_service": {
                "integrity_eligible": not runtime_service_stop_reasons,
                "stop_reasons": runtime_service_stop_reasons,
                "complete_samples": len(runtime_rows),
                "total_samples": len(all_rows),
                "critical_counter_resets": runtime_reset_fields,
                "missing_critical_counter_fields": runtime_missing_counter_fields,
            },
            "matched_routing": {
                "integrity_eligible": not matched_routing_stop_reasons,
                "stop_reasons": matched_routing_stop_reasons,
                "complete_samples": len(window.complete_rows),
                "total_samples": len(all_rows),
                "critical_counter_resets": router_reset_fields,
                "missing_critical_counter_fields": router_missing_counter_fields,
                "router_identity_status": router_identity_status,
                "router_identity_transitions": router_identity_transitions,
                "identity_note": (
                    "not_collected means the legacy CSV cannot prove Router config "
                    "identity; pair this result with a Router snapshot identity gate"
                    if router_identity_status == "not_collected"
                    else None
                ),
            },
        },
    }
