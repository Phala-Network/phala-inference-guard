#!/usr/bin/env python3

"""Semantic analysis of paired PIG/backend Prometheus captures.

This operator-only module never bridges a restart, counter reset, missing
series, label drift, or histogram schema change. Raw backend token counters
remain explicitly distinct from success-linked completion goodput.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
import hashlib
import json
import math
from pathlib import Path
from typing import Any, Mapping

from prometheus_text import MetricSeries, PrometheusSnapshot, parse_prometheus


CAPTURE_FILES = (
    "manifest.json",
    "target-pig.prom",
    "target-backend.prom",
    "comparator-combined.prom",
    "router.json",
)

BACKEND_COUNTERS = {
    "raw_generation_tokens": "vllm:generation_tokens_total",
    "raw_prompt_tokens": "vllm:prompt_tokens_total",
    "preemptions": "vllm:num_preemptions_total",
    "cache_queries": "vllm:prefix_cache_queries_total",
    "cache_hits": "vllm:prefix_cache_hits_total",
    "terminal_requests": "vllm:request_success_total",
}

BACKEND_HISTOGRAMS = {
    "request_prompt_tokens": "vllm:request_prompt_tokens",
    "request_generation_tokens": "vllm:request_generation_tokens",
    "time_to_first_token_seconds": "vllm:time_to_first_token_seconds",
    "time_per_output_token_seconds": "vllm:request_time_per_output_token_seconds",
    "e2e_request_latency_seconds": "vllm:e2e_request_latency_seconds",
}

TARGET_PIG_TABLES = {
    "admission_decisions": "pig_predictive_admission_decisions_total",
    "protections": "pig_predictive_admission_protections_total",
    "tps_decisions": "pig_predictive_tps_decisions_total",
    "tps_denominator_selections": "pig_predictive_tps_denominator_selections_total",
    "tps_denominator_sequence_seconds": (
        "pig_predictive_tps_denominator_sequence_seconds_total"
    ),
    "input_size_outcomes": (
        "pig_predictive_admission_selection_input_tokens_bucket"
    ),
    "streaming_shape": "pig_predictive_request_streaming_total",
    "output_limit_matrix": "pig_predictive_output_limit_comparison_total",
}

TARGET_PIG_HISTOGRAMS = {
    "prediction_seconds": "pig_predictive_admission_prediction_duration_seconds",
    "body_read_seconds": "pig_predictive_admission_body_read_duration_seconds",
    "estimator_seconds": "pig_predictive_admission_estimator_duration_seconds",
    "pre_forward_seconds": "pig_predictive_admission_pre_forward_duration_seconds",
}


def _identity_map(
    snapshot: PrometheusSnapshot,
    metric_name: str,
    selector: Mapping[str, str] | None = None,
) -> dict[tuple[tuple[str, str], ...], MetricSeries]:
    return {
        series.identity: series
        for series in snapshot.series(metric_name)
        if selector is None
        or all(series.labels.get(name) == value for name, value in selector.items())
    }


def _label_key(labels: Mapping[str, str]) -> str:
    if not labels:
        return "unlabeled"
    return ",".join(
        f"{name}={value}" for name, value in sorted(labels.items())
    )


def _unavailable(reason: str, **details: Any) -> dict[str, Any]:
    result: dict[str, Any] = {"status": "unavailable", "reason": reason}
    result.update(details)
    return result


def counter_delta(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    metric_name: str,
    selector: Mapping[str, str] | None = None,
) -> dict[str, Any]:
    start_map = _identity_map(start, metric_name, selector)
    end_map = _identity_map(end, metric_name, selector)
    if not start_map or not end_map:
        return _unavailable(
            "missing_metric",
            metric=metric_name,
            start_series=len(start_map),
            end_series=len(end_map),
        )
    if set(start_map) != set(end_map):
        return _unavailable(
            "series_identity_mismatch",
            metric=metric_name,
            missing_at_end=[
                dict(identity)
                for identity in sorted(set(start_map) - set(end_map))
            ],
            added_at_end=[
                dict(identity)
                for identity in sorted(set(end_map) - set(start_map))
            ],
        )
    rows: list[dict[str, Any]] = []
    resets: list[dict[str, str]] = []
    nonfinite: list[dict[str, str]] = []
    total = 0.0
    for identity in sorted(start_map):
        before = start_map[identity].value
        after = end_map[identity].value
        labels = dict(identity)
        if not math.isfinite(before) or not math.isfinite(after):
            nonfinite.append(labels)
            continue
        if after < before:
            resets.append(labels)
            continue
        delta = after - before
        total += delta
        if delta != 0:
            rows.append(
                {
                    "labels": labels,
                    "start": before,
                    "end": after,
                    "delta": delta,
                }
            )
    if nonfinite:
        return _unavailable(
            "nonfinite_counter",
            metric=metric_name,
            affected_series=nonfinite,
        )
    if resets:
        return _unavailable(
            "counter_reset", metric=metric_name, affected_series=resets
        )
    return {
        "status": "available",
        "metric": metric_name,
        "delta": total,
        "series_count": len(start_map),
        "changed_series_count": len(rows),
        "zero_delta_series_count": len(start_map) - len(rows),
        "series": rows,
    }


def bucket_table_delta(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    metric_name: str,
) -> dict[str, Any]:
    try:
        start_groups = _bucket_groups(start, metric_name)
        end_groups = _bucket_groups(end, metric_name)
    except ValueError as error:
        return _unavailable(
            "invalid_bucket_schema", metric=metric_name, detail=str(error)
        )
    if not start_groups or not end_groups:
        return _unavailable("missing_metric", metric=metric_name)
    if set(start_groups) != set(end_groups):
        return _unavailable("series_identity_mismatch", metric=metric_name)
    validation = _validate_group_schemas(start_groups, end_groups, metric_name)
    if validation is not None:
        return validation
    output_groups: list[dict[str, Any]] = []
    for identity in sorted(start_groups):
        before = start_groups[identity]
        after = end_groups[identity]
        cumulative: list[tuple[float, float]] = []
        for bound in sorted(before):
            start_value = before[bound].value
            end_value = after[bound].value
            if not math.isfinite(start_value) or not math.isfinite(end_value):
                return _unavailable("nonfinite_counter", metric=metric_name)
            if end_value < start_value:
                return _unavailable(
                    "counter_reset",
                    metric=metric_name,
                    labels=dict(identity),
                    bound=_json_bound(bound),
                )
            cumulative.append((bound, end_value - start_value))
        if any(
            cumulative[index][1] < cumulative[index - 1][1]
            for index in range(1, len(cumulative))
        ):
            return _unavailable(
                "non_monotonic_bucket_delta",
                metric=metric_name,
                labels=dict(identity),
            )
        output_groups.append(
            {
                "labels": dict(identity),
                "total": cumulative[-1][1],
                "buckets": [
                    {"le": _json_bound(bound), "cumulative_count": value}
                    for bound, value in cumulative
                ],
                "quantiles": {
                    "p50": _quantile(cumulative, 0.50),
                    "p95": _quantile(cumulative, 0.95),
                    "p99": _quantile(cumulative, 0.99),
                },
            }
        )
    return {
        "status": "available",
        "metric": metric_name,
        "groups": output_groups,
    }


def _bucket_groups(
    snapshot: PrometheusSnapshot,
    metric_name: str,
) -> dict[tuple[tuple[str, str], ...], dict[float, MetricSeries]]:
    groups: dict[
        tuple[tuple[str, str], ...], dict[float, MetricSeries]
    ] = {}
    for series in snapshot.series(metric_name):
        labels = dict(series.labels)
        raw_bound = labels.pop("le", None)
        if raw_bound is None:
            raise ValueError(f"bucket lacks le label: {metric_name}")
        if raw_bound in ("+Inf", "Inf"):
            bound = math.inf
        elif raw_bound == "-Inf":
            bound = -math.inf
        else:
            try:
                bound = float(raw_bound)
            except ValueError as error:
                raise ValueError(
                    f"invalid histogram bound {raw_bound}: {metric_name}"
                ) from error
        identity = tuple(sorted(labels.items()))
        if bound in groups.setdefault(identity, {}):
            raise ValueError(
                f"duplicate histogram bound {raw_bound}: {metric_name}"
            )
        groups[identity][bound] = series
    return groups


def _histogram_groups(
    snapshot: PrometheusSnapshot,
    metric_base: str,
) -> dict[tuple[tuple[str, str], ...], dict[float, MetricSeries]]:
    return _bucket_groups(snapshot, f"{metric_base}_bucket")


def _validate_group_schemas(
    start_groups: dict[tuple[tuple[str, str], ...], dict[float, MetricSeries]],
    end_groups: dict[tuple[tuple[str, str], ...], dict[float, MetricSeries]],
    metric_name: str,
) -> dict[str, Any] | None:
    for identity in sorted(start_groups):
        before = start_groups[identity]
        after = end_groups[identity]
        if set(before) != set(after):
            return _unavailable(
                "bucket_schema_mismatch",
                metric=metric_name,
                labels=dict(identity),
                start_bounds=[_json_bound(value) for value in sorted(before)],
                end_bounds=[_json_bound(value) for value in sorted(after)],
            )
    schemas = {tuple(sorted(group)) for group in start_groups.values()}
    schemas.update(tuple(sorted(group)) for group in end_groups.values())
    if len(schemas) != 1:
        return _unavailable(
            "bucket_schema_inconsistent_across_series", metric=metric_name
        )
    for phase, groups in (("start", start_groups), ("end", end_groups)):
        for identity, group in groups.items():
            values = [group[bound].value for bound in sorted(group)]
            if any(not math.isfinite(value) for value in values):
                return _unavailable("nonfinite_counter", metric=metric_name)
            if any(
                values[index] < values[index - 1]
                for index in range(1, len(values))
            ):
                return _unavailable(
                    "non_monotonic_bucket_cumulative",
                    metric=metric_name,
                    phase=phase,
                    labels=dict(identity),
                )
    return None


def _quantile(
    cumulative: list[tuple[float, float]], quantile: float
) -> dict[str, Any]:
    total = cumulative[-1][1]
    if total <= 0:
        return _unavailable("no_observations")
    rank = quantile * total
    previous_bound = 0.0
    previous_count = 0.0
    for bound, count in cumulative:
        if count < rank:
            previous_bound = bound
            previous_count = count
            continue
        if math.isinf(bound):
            return {"status": "lower_bounded", "lower_bound": previous_bound}
        bucket_count = count - previous_count
        if bucket_count <= 0:
            return {"status": "available", "value": bound}
        fraction = (rank - previous_count) / bucket_count
        value = previous_bound + (bound - previous_bound) * fraction
        return {"status": "available", "value": value}
    return _unavailable("quantile_rank_not_found")


def histogram_delta(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    metric_base: str,
) -> dict[str, Any]:
    try:
        start_groups = _histogram_groups(start, metric_base)
        end_groups = _histogram_groups(end, metric_base)
    except ValueError as error:
        return _unavailable(
            "invalid_bucket_schema", metric=metric_base, detail=str(error)
        )
    if not start_groups or not end_groups:
        return _unavailable("missing_metric", metric=metric_base)
    if set(start_groups) != set(end_groups):
        return _unavailable("series_identity_mismatch", metric=metric_base)
    validation = _validate_group_schemas(
        start_groups, end_groups, metric_base
    )
    if validation is not None:
        return validation
    aggregate: dict[float, float] = {}
    for identity in sorted(start_groups):
        before = start_groups[identity]
        after = end_groups[identity]
        for bound in sorted(before):
            start_value = before[bound].value
            end_value = after[bound].value
            if not math.isfinite(start_value) or not math.isfinite(end_value):
                return _unavailable("nonfinite_counter", metric=metric_base)
            if end_value < start_value:
                return _unavailable(
                    "counter_reset",
                    metric=metric_base,
                    labels=dict(identity),
                    bound=_json_bound(bound),
                )
            aggregate[bound] = (
                aggregate.get(bound, 0.0) + end_value - start_value
            )
    bounds = sorted(aggregate)
    if not bounds or bounds[-1] != math.inf:
        return _unavailable("missing_infinite_bucket", metric=metric_base)
    cumulative = [(bound, aggregate[bound]) for bound in bounds]
    for index in range(1, len(cumulative)):
        if cumulative[index][1] < cumulative[index - 1][1]:
            return _unavailable(
                "non_monotonic_bucket_delta", metric=metric_base
            )
    count = counter_delta(start, end, f"{metric_base}_count")
    total_sum = counter_delta(start, end, f"{metric_base}_sum")
    if count["status"] != "available":
        return _unavailable(
            "invalid_count", metric=metric_base, evidence=count
        )
    if total_sum["status"] != "available":
        return _unavailable(
            "invalid_sum", metric=metric_base, evidence=total_sum
        )
    if not math.isclose(
        cumulative[-1][1], count["delta"], rel_tol=0, abs_tol=1e-9
    ):
        return _unavailable(
            "count_bucket_mismatch",
            metric=metric_base,
            count=count["delta"],
            infinite_bucket=cumulative[-1][1],
        )
    return {
        "status": "available",
        "metric": metric_base,
        "count": count["delta"],
        "sum": total_sum["delta"],
        "mean": (
            total_sum["delta"] / count["delta"]
            if count["delta"] > 0
            else None
        ),
        "buckets": [
            {"le": _json_bound(bound), "cumulative_count": value}
            for bound, value in cumulative
        ],
        "quantiles": {
            "p50": _quantile(cumulative, 0.50),
            "p95": _quantile(cumulative, 0.95),
            "p99": _quantile(cumulative, 0.99),
        },
    }


def _json_bound(value: float) -> float | str:
    if value == math.inf:
        return "+Inf"
    if value == -math.inf:
        return "-Inf"
    return value


def _metric_label_identity(
    snapshot: PrometheusSnapshot,
    metric_names: tuple[str, ...],
    label: str,
) -> dict[str, Any]:
    values: set[str] = set()
    sources: list[str] = []
    for metric_name in metric_names:
        series = snapshot.series(metric_name)
        if not series:
            continue
        sources.append(metric_name)
        for item in series:
            if label in item.labels:
                values.add(item.labels[label])
    if not values:
        return _unavailable(
            "label_not_exported", label=label, metrics=sources
        )
    if len(values) != 1:
        return _unavailable(
            "multiple_label_values", label=label, values=sorted(values)
        )
    return {
        "status": "available",
        "value": next(iter(values)),
        "metrics": sources,
    }


def _single_gauge(
    snapshot: PrometheusSnapshot, metric_name: str
) -> dict[str, Any]:
    series = snapshot.series(metric_name)
    if len(series) != 1:
        return _unavailable(
            "missing_or_ambiguous_gauge",
            metric=metric_name,
            series=len(series),
        )
    value = series[0].value
    if not math.isfinite(value):
        return _unavailable("nonfinite_gauge", metric=metric_name)
    return {"status": "available", "metric": metric_name, "value": value}


def _sum_gauge(
    snapshot: PrometheusSnapshot, metric_name: str
) -> dict[str, Any]:
    series = snapshot.series(metric_name)
    if not series:
        return _unavailable("missing_metric", metric=metric_name)
    if any(not math.isfinite(item.value) for item in series):
        return _unavailable("nonfinite_gauge", metric=metric_name)
    return {
        "status": "available",
        "metric": metric_name,
        "value": sum(item.value for item in series),
        "series_count": len(series),
    }


def _derive_pig_version(
    snapshot: PrometheusSnapshot,
    manifest: Mapping[str, Any],
    manifest_field: str,
    image_field: str | None,
) -> dict[str, Any]:
    for metric_name in ("pig_info", "pig_version_info"):
        values = {
            item.labels["version"]
            for item in snapshot.series(metric_name)
            if item.labels.get("version")
        }
        if len(values) == 1:
            return {"value": next(iter(values)), "source": metric_name}
        if len(values) > 1:
            return {
                "value": None,
                "source": metric_name,
                "error": "multiple_metric_versions",
                "values": sorted(values),
            }
    manifest_value = str(manifest.get(manifest_field, "")).strip()
    if manifest_value:
        return {
            "value": manifest_value,
            "source": f"manifest.{manifest_field}",
        }
    if image_field:
        image = str(manifest.get(image_field, "")).strip()
        if image:
            tail = image.rsplit("/", 1)[-1]
            if ":" in tail:
                tag = tail.split(":", 1)[1].split("@", 1)[0]
                if tag:
                    return {
                        "value": tag,
                        "source": f"manifest.{image_field}.tag",
                    }
    return {
        "value": None,
        "source": None,
        "error": "version_unavailable",
    }


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


@dataclass(frozen=True)
class PairedCapture:
    path: Path
    manifest: dict[str, Any]
    target_pig: PrometheusSnapshot
    target_backend: PrometheusSnapshot
    comparator_combined: PrometheusSnapshot
    router: dict[str, Any]
    identity: dict[str, Any]
    source_sha256: dict[str, str]
    recorded_sha256: dict[str, str]
    consistency_errors: tuple[str, ...]


def load_capture(path: str | Path) -> PairedCapture:
    capture_path = Path(path)
    missing = [
        name for name in CAPTURE_FILES if not (capture_path / name).is_file()
    ]
    if missing:
        raise ValueError(
            f"capture lacks required files: {', '.join(missing)}"
        )
    manifest = json.loads(
        (capture_path / "manifest.json").read_text(encoding="utf-8")
    )
    router = json.loads(
        (capture_path / "router.json").read_text(encoding="utf-8")
    )
    target_pig = parse_prometheus(
        (capture_path / "target-pig.prom").read_text(encoding="utf-8")
    )
    target_backend = parse_prometheus(
        (capture_path / "target-backend.prom").read_text(encoding="utf-8")
    )
    comparator = parse_prometheus(
        (capture_path / "comparator-combined.prom").read_text(
            encoding="utf-8"
        )
    )
    target_epoch = _single_gauge(
        target_backend, "process_start_time_seconds"
    )
    comparator_epoch = _single_gauge(
        comparator, "process_start_time_seconds"
    )
    target_model = _metric_label_identity(
        target_backend, tuple(BACKEND_COUNTERS.values()), "model_name"
    )
    comparator_model = _metric_label_identity(
        comparator, tuple(BACKEND_COUNTERS.values()), "model_name"
    )
    identity = {
        "target_pig_version": _derive_pig_version(
            target_pig,
            manifest,
            "target_pig_version",
            "target_pig_image",
        ),
        "comparator_pig_version": _derive_pig_version(
            comparator, manifest, "comparator_pig_version", None
        ),
        "target_backend_epoch": target_epoch,
        "comparator_backend_epoch": comparator_epoch,
        "target_model": target_model,
        "comparator_model": comparator_model,
    }
    source_sha256 = {
        name: _sha256(capture_path / name) for name in CAPTURE_FILES
    }
    recorded_sha256: dict[str, str] = {}
    sums_path = capture_path / "SHA256SUMS"
    if sums_path.is_file():
        for line_number, line in enumerate(
            sums_path.read_text(encoding="utf-8").splitlines(), start=1
        ):
            parts = line.strip().split(maxsplit=1)
            if len(parts) != 2:
                raise ValueError(
                    f"invalid SHA256SUMS line {line_number}: {capture_path}"
                )
            digest, name = parts
            name = name.lstrip("*")
            if name not in CAPTURE_FILES:
                continue
            if (
                len(digest) != 64
                or any(character not in "0123456789abcdefABCDEF" for character in digest)
            ):
                raise ValueError(
                    f"invalid SHA-256 on line {line_number}: {capture_path}"
                )
            if name in recorded_sha256:
                raise ValueError(
                    f"duplicate SHA256SUMS entry for {name}: {capture_path}"
                )
            recorded_sha256[name] = digest.lower()
    consistency_errors: list[str] = []
    if recorded_sha256:
        for name in CAPTURE_FILES:
            if name not in recorded_sha256:
                consistency_errors.append(f"recorded_sha256_missing:{name}")
            elif recorded_sha256[name] != source_sha256[name]:
                consistency_errors.append(f"recorded_sha256_mismatch:{name}")
    for side in ("target", "comparator"):
        field = f"{side}_backend_epoch"
        manifest_value = str(manifest.get(field, "")).strip()
        derived = identity[field]
        if manifest_value and derived.get("status") == "available":
            try:
                matches = math.isclose(
                    float(manifest_value),
                    derived["value"],
                    rel_tol=0,
                    abs_tol=1e-6,
                )
            except ValueError:
                matches = False
            if not matches:
                consistency_errors.append(f"{field}_manifest_mismatch")
    for side in ("target", "comparator"):
        field = f"{side}_pig_version"
        raw_version = str(manifest.get(field, "")).strip()
        derived_version = identity[field]
        if (
            raw_version
            and derived_version.get("source") in ("pig_info", "pig_version_info")
            and raw_version != derived_version.get("value")
        ):
            consistency_errors.append(f"{field}_manifest_mismatch")
    return PairedCapture(
        path=capture_path,
        manifest=manifest,
        target_pig=target_pig,
        target_backend=target_backend,
        comparator_combined=comparator,
        router=router,
        identity=identity,
        source_sha256=source_sha256,
        recorded_sha256=recorded_sha256,
        consistency_errors=tuple(consistency_errors),
    )


def _parse_time(value: Any) -> datetime:
    raw = str(value)
    if raw.endswith("Z"):
        raw = f"{raw[:-1]}+00:00"
    parsed = datetime.fromisoformat(raw)
    if parsed.tzinfo is None:
        raise ValueError("captured_at must include a timezone")
    return parsed


def _same_available_value(
    start: dict[str, Any], end: dict[str, Any]
) -> bool:
    return (
        start.get("status") == "available"
        and end.get("status") == "available"
        and start.get("value") == end.get("value")
    )


def _same_derived_value(
    start: dict[str, Any], end: dict[str, Any]
) -> bool:
    return (
        start.get("value") is not None
        and start.get("value") == end.get("value")
    )


def _rate(result: dict[str, Any], wall_time: float) -> dict[str, Any]:
    output = dict(result)
    if output.get("status") == "available":
        output["rate_per_second"] = output["delta"] / wall_time
    return output


def _force_unavailable(reason: str, metric: str) -> dict[str, Any]:
    return _unavailable(reason, metric=metric)


def _breakdown(result: dict[str, Any]) -> dict[str, Any]:
    if result.get("status") != "available":
        return result
    output = dict(result)
    output["by_labels"] = {
        _label_key(row["labels"]): row["delta"]
        for row in result["series"]
    }
    return output


def _backend_analysis(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    wall_time: float,
    identity_ok: bool,
) -> dict[str, Any]:
    counters: dict[str, dict[str, Any]] = {}
    for semantic, metric in BACKEND_COUNTERS.items():
        result = (
            counter_delta(start, end, metric)
            if identity_ok
            else _force_unavailable("backend_identity_changed", metric)
        )
        counters[semantic] = _rate(result, wall_time)
    terminal = counters.pop("terminal_requests")
    if terminal.get("status") == "available":
        by_reason: dict[str, float] = {}
        for row in terminal["series"]:
            reason = row["labels"].get("finished_reason", "unknown")
            by_reason[reason] = by_reason.get(reason, 0.0) + row["delta"]
        terminal["by_finished_reason"] = dict(sorted(by_reason.items()))
        terminal["non_error_terminal"] = sum(
            value
            for reason, value in by_reason.items()
            if reason not in ("abort", "error")
        )
    histograms = {
        semantic: (
            histogram_delta(start, end, metric)
            if identity_ok
            else _force_unavailable("backend_identity_changed", metric)
        )
        for semantic, metric in BACKEND_HISTOGRAMS.items()
    }
    queries = counters["cache_queries"]
    hits = counters["cache_hits"]
    if (
        queries.get("status") != "available"
        or hits.get("status") != "available"
    ):
        cache_share = _unavailable("cache_counter_unavailable")
    elif queries["delta"] <= 0:
        cache_share = _unavailable("no_cache_queries")
    elif hits["delta"] > queries["delta"]:
        cache_share = _unavailable(
            "cache_hits_exceed_queries",
            queries=queries["delta"],
            hits=hits["delta"],
        )
    else:
        cache_share = {
            "status": "available",
            "queries": queries["delta"],
            "hits": hits["delta"],
            "hit_share": hits["delta"] / queries["delta"],
        }
    return {
        **counters,
        "terminal_requests": terminal,
        "cache_hit_share": cache_share,
        "endpoint_gauges": {
            "running_start": _sum_gauge(
                start, "vllm:num_requests_running"
            ),
            "running_end": _sum_gauge(end, "vllm:num_requests_running"),
            "waiting_start": _sum_gauge(
                start, "vllm:num_requests_waiting"
            ),
            "waiting_end": _sum_gauge(end, "vllm:num_requests_waiting"),
        },
        "histograms": histograms,
    }


def _pig_backend_counters(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    wall_time: float,
) -> dict[str, Any]:
    metrics = {
        "requests": "pig_backend_requests_total",
        "completed": "pig_backend_completed_total",
        "proxy_errors": "pig_backend_proxy_errors_total",
    }
    return {
        name: _rate(
            _breakdown(counter_delta(start, end, metric)), wall_time
        )
        for name, metric in metrics.items()
    }


def _target_pig_analysis(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    wall_time: float,
) -> dict[str, Any]:
    tables = {
        semantic: _breakdown(counter_delta(start, end, metric))
        for semantic, metric in TARGET_PIG_TABLES.items()
        if semantic != "input_size_outcomes"
    }
    return {
        "backend": _pig_backend_counters(start, end, wall_time),
        **tables,
        "input_size_outcomes": bucket_table_delta(
            start,
            end,
            TARGET_PIG_TABLES["input_size_outcomes"],
        ),
        "pre_forward_histograms": {
            semantic: histogram_delta(start, end, metric)
            for semantic, metric in TARGET_PIG_HISTOGRAMS.items()
        },
    }


def _comparator_pig_analysis(
    start: PrometheusSnapshot,
    end: PrometheusSnapshot,
    wall_time: float,
) -> dict[str, Any]:
    return {
        "backend": _pig_backend_counters(start, end, wall_time),
        "legacy_requests": _breakdown(
            counter_delta(
                start, end, "pig_requests_total", {"lane": "global"}
            )
        ),
        "legacy_completed": _breakdown(
            counter_delta(
                start, end, "pig_completed_total", {"lane": "global"}
            )
        ),
        "legacy_rejected": _breakdown(
            counter_delta(
                start, end, "pig_rejected_total", {"lane": "global"}
            )
        ),
        "legacy_response_status": _breakdown(
            counter_delta(
                start,
                end,
                "pig_response_status_class_total",
                {"lane": "global"},
            )
        ),
        "admission_decisions": _unavailable("not_exported"),
        "protections": _unavailable("not_exported"),
        "tps_decisions": _unavailable("not_exported"),
        "tps_denominator_selections": _unavailable("not_exported"),
        "tps_denominator_sequence_seconds": _unavailable("not_exported"),
        "input_size_outcomes": _unavailable("not_exported"),
        "streaming_shape": _unavailable("not_exported"),
        "output_limit_matrix": _unavailable("not_exported"),
    }


def _route_by_upstream(
    router: Mapping[str, Any], upstream: str
) -> Mapping[str, Any] | None:
    routes = router.get("routes")
    if not isinstance(routes, list):
        return None
    matches = [
        route for route in routes if route.get("upstream_name") == upstream
    ]
    return matches[0] if len(matches) == 1 else None


def _router_route_delta(
    start: Mapping[str, Any],
    end: Mapping[str, Any],
    upstream: str,
    wall_time: float,
) -> dict[str, Any]:
    before = _route_by_upstream(start, upstream)
    after = _route_by_upstream(end, upstream)
    if before is None or after is None:
        return _unavailable("route_not_exported", upstream=upstream)
    fields = (
        "processed",
        "upstream_attempts",
        "upstream_429",
        "selected_by_cache",
        "selected_by_load",
        "selected_by_order",
        "cache_rejected_by_pressure",
        "pressure_passthrough",
    )
    deltas: dict[str, Any] = {}
    for field in fields:
        start_value = before.get(field)
        end_value = after.get(field)
        if not isinstance(start_value, (int, float)) or not isinstance(
            end_value, (int, float)
        ):
            deltas[field] = _unavailable("field_not_exported")
        elif end_value < start_value:
            deltas[field] = _unavailable(
                "counter_reset", start=start_value, end=end_value
            )
        else:
            delta = end_value - start_value
            deltas[field] = {
                "status": "available",
                "delta": delta,
                "rate_per_second": delta / wall_time,
            }
    return {
        "status": "available",
        "upstream": upstream,
        "enabled_start": before.get("enabled"),
        "enabled_end": after.get("enabled"),
        "counters": deltas,
    }


def _required_statuses(
    target_backend: dict[str, Any],
    comparator_backend: dict[str, Any],
    target_pig: dict[str, Any],
    comparator_pig: dict[str, Any],
) -> dict[str, str]:
    fields = {
        "target.backend.raw_generation_tokens": target_backend[
            "raw_generation_tokens"
        ],
        "target.backend.raw_prompt_tokens": target_backend[
            "raw_prompt_tokens"
        ],
        "target.backend.preemptions": target_backend["preemptions"],
        "target.backend.cache_queries": target_backend["cache_queries"],
        "target.backend.cache_hits": target_backend["cache_hits"],
        "target.backend.terminal_requests": target_backend[
            "terminal_requests"
        ],
        "comparator.backend.raw_generation_tokens": comparator_backend[
            "raw_generation_tokens"
        ],
        "comparator.backend.raw_prompt_tokens": comparator_backend[
            "raw_prompt_tokens"
        ],
        "comparator.backend.preemptions": comparator_backend["preemptions"],
        "comparator.backend.cache_queries": comparator_backend["cache_queries"],
        "comparator.backend.cache_hits": comparator_backend["cache_hits"],
        "comparator.backend.terminal_requests": comparator_backend[
            "terminal_requests"
        ],
        "target.pig.backend.requests": target_pig["backend"]["requests"],
        "target.pig.backend.completed": target_pig["backend"]["completed"],
        "target.pig.backend.proxy_errors": target_pig["backend"][
            "proxy_errors"
        ],
        "comparator.pig.backend.requests": comparator_pig["backend"][
            "requests"
        ],
        "comparator.pig.backend.completed": comparator_pig["backend"][
            "completed"
        ],
        "comparator.pig.backend.proxy_errors": comparator_pig["backend"][
            "proxy_errors"
        ],
    }
    return {
        path: str(value.get("status", "unknown"))
        for path, value in fields.items()
    }


def _rate_ratio(
    target: dict[str, Any], comparator: dict[str, Any]
) -> dict[str, Any]:
    if (
        target.get("status") != "available"
        or comparator.get("status") != "available"
    ):
        return _unavailable("source_metric_unavailable")
    denominator = comparator.get("rate_per_second")
    if not isinstance(denominator, (int, float)) or denominator <= 0:
        return _unavailable("comparator_rate_not_positive")
    return {
        "status": "available",
        "target_rate_per_second": target["rate_per_second"],
        "comparator_rate_per_second": denominator,
        "target_to_comparator_ratio": target["rate_per_second"] / denominator,
    }


def _semantic_comparison(
    target_backend: dict[str, Any],
    comparator_backend: dict[str, Any],
    wall_time: float,
    eligible: bool,
) -> dict[str, Any]:
    target_terminal = target_backend["terminal_requests"]
    comparator_terminal = comparator_backend["terminal_requests"]
    if (
        target_terminal.get("status") == "available"
        and comparator_terminal.get("status") == "available"
        and comparator_terminal.get("non_error_terminal", 0) > 0
    ):
        terminal_ratio = {
            "status": "available",
            "target_rate_per_second": (
                target_terminal["non_error_terminal"] / wall_time
            ),
            "comparator_rate_per_second": (
                comparator_terminal["non_error_terminal"] / wall_time
            ),
            "target_to_comparator_ratio": (
                target_terminal["non_error_terminal"]
                / comparator_terminal["non_error_terminal"]
            ),
        }
    else:
        terminal_ratio = _unavailable("terminal_request_evidence_unavailable")
    target_cache = target_backend["cache_hit_share"]
    comparator_cache = comparator_backend["cache_hit_share"]
    if (
        target_cache.get("status") == "available"
        and comparator_cache.get("status") == "available"
    ):
        cache_delta = {
            "status": "available",
            "target_hit_share": target_cache["hit_share"],
            "comparator_hit_share": comparator_cache["hit_share"],
            "target_minus_comparator": (
                target_cache["hit_share"] - comparator_cache["hit_share"]
            ),
        }
    else:
        cache_delta = _unavailable("cache_evidence_unavailable")
    return {
        "status": "descriptive_only" if eligible else "ineligible",
        "causal_pig_effect": _unavailable("traffic_cohort_not_matched"),
        "successful_completion_goodput": _unavailable(
            "success_token_linkage_not_exported"
        ),
        "raw_generation_work_rate": _rate_ratio(
            target_backend["raw_generation_tokens"],
            comparator_backend["raw_generation_tokens"],
        ),
        "raw_prompt_work_rate": _rate_ratio(
            target_backend["raw_prompt_tokens"],
            comparator_backend["raw_prompt_tokens"],
        ),
        "non_error_terminal_request_rate": terminal_ratio,
        "cache_hit_share": cache_delta,
        "warning": (
            "Ratios describe the observed traffic cohorts and do not prove a PIG behavior effect."
        ),
    }


def analyze_paired_captures(
    start: PairedCapture, end: PairedCapture
) -> dict[str, Any]:
    wall_time = (
        _parse_time(end.manifest.get("captured_at"))
        - _parse_time(start.manifest.get("captured_at"))
    ).total_seconds()
    if wall_time <= 0:
        raise ValueError("end capture must be later than start capture")

    errors = list(start.consistency_errors) + list(end.consistency_errors)
    target_epoch_ok = _same_available_value(
        start.identity["target_backend_epoch"],
        end.identity["target_backend_epoch"],
    )
    comparator_epoch_ok = _same_available_value(
        start.identity["comparator_backend_epoch"],
        end.identity["comparator_backend_epoch"],
    )
    target_model_ok = _same_available_value(
        start.identity["target_model"], end.identity["target_model"]
    )
    comparator_model_ok = _same_available_value(
        start.identity["comparator_model"],
        end.identity["comparator_model"],
    )
    if not target_epoch_ok:
        errors.append("target_backend_epoch_changed")
    if not comparator_epoch_ok:
        errors.append("comparator_backend_epoch_changed")
    if not target_model_ok:
        errors.append("target_model_identity_changed")
    if not comparator_model_ok:
        errors.append("comparator_model_identity_changed")
    if (
        target_model_ok
        and comparator_model_ok
        and start.identity["target_model"]["value"]
        != start.identity["comparator_model"]["value"]
    ):
        errors.append("target_comparator_model_mismatch")
    if not _same_derived_value(
        start.identity["target_pig_version"],
        end.identity["target_pig_version"],
    ):
        errors.append("target_pig_version_changed")
    if not _same_derived_value(
        start.identity["comparator_pig_version"],
        end.identity["comparator_pig_version"],
    ):
        errors.append("comparator_pig_version_changed")
    for field in (
        "compose_sha256",
        "target_upstream",
        "comparator_upstream",
        "target_pig_image",
        "target_pig_started",
    ):
        if start.manifest.get(field) != end.manifest.get(field):
            errors.append(f"{field}_changed")
    start_router_digest = start.router.get("upstream_config_digest")
    end_router_digest = end.router.get("upstream_config_digest")
    router_identity_ok = (
        isinstance(start_router_digest, str)
        and bool(start_router_digest)
        and start_router_digest == end_router_digest
    )
    if not start_router_digest or not end_router_digest:
        errors.append("router_config_identity_unavailable")
    elif not router_identity_ok:
        errors.append("router_config_identity_changed")

    target_backend = _backend_analysis(
        start.target_backend,
        end.target_backend,
        wall_time,
        target_epoch_ok and target_model_ok,
    )
    comparator_backend = _backend_analysis(
        start.comparator_combined,
        end.comparator_combined,
        wall_time,
        comparator_epoch_ok and comparator_model_ok,
    )
    target_pig = _target_pig_analysis(
        start.target_pig, end.target_pig, wall_time
    )
    comparator_pig = _comparator_pig_analysis(
        start.comparator_combined, end.comparator_combined, wall_time
    )
    required = _required_statuses(
        target_backend, comparator_backend, target_pig, comparator_pig
    )
    unavailable_required = sorted(
        path for path, status in required.items() if status != "available"
    )
    errors.extend(
        f"required_evidence_unavailable:{path}"
        for path in unavailable_required
    )
    errors = sorted(set(errors))

    optional: dict[str, str] = {}
    for side, backend in (
        ("target", target_backend),
        ("comparator", comparator_backend),
    ):
        for name, value in backend["histograms"].items():
            if value.get("status") != "available":
                optional[
                    f"{side}.backend.histograms.{name}"
                ] = value.get("reason", "unknown")
    for name, value in target_pig["pre_forward_histograms"].items():
        if value.get("status") != "available":
            optional[
                f"target.pig.pre_forward_histograms.{name}"
            ] = value.get("reason", "unknown")

    target_upstream = str(start.manifest.get("target_upstream", ""))
    comparator_upstream = str(
        start.manifest.get("comparator_upstream", "")
    )
    runtime_errors = [
        error
        for error in errors
        if error
        not in (
            "router_config_identity_changed",
            "router_config_identity_unavailable",
        )
    ]
    comparison_eligible = not errors
    return {
        "schema_version": "pig.paired-snapshot-analysis.v1",
        "wall_time_seconds": wall_time,
        "captures": {
            "start": {
                "captured_at": start.manifest.get("captured_at"),
                "path": str(start.path),
                "raw_manifest": start.manifest,
                "derived_identity": start.identity,
                "source_sha256": start.source_sha256,
                "recorded_sha256": start.recorded_sha256,
            },
            "end": {
                "captured_at": end.manifest.get("captured_at"),
                "path": str(end.path),
                "raw_manifest": end.manifest,
                "derived_identity": end.identity,
                "source_sha256": end.source_sha256,
                "recorded_sha256": end.recorded_sha256,
            },
        },
        "evidence": {
            "runtime_integrity_eligible": not runtime_errors,
            "matched_routing_eligible": router_identity_ok,
            "comparison_eligible": comparison_eligible,
            "errors": errors,
            "required_fields": required,
            "unavailable_required_fields": unavailable_required,
            "optional_unavailable_fields": optional,
        },
        "target": {
            "identity": end.identity,
            "backend": target_backend,
            "pig": target_pig,
            "router": _router_route_delta(
                start.router, end.router, target_upstream, wall_time
            ),
            "throughput": {
                "successful_completion_goodput": _unavailable(
                    "success_token_linkage_not_exported"
                ),
                "raw_generation_work": target_backend[
                    "raw_generation_tokens"
                ],
            },
        },
        "comparator": {
            "identity": {
                "comparator_pig_version": end.identity[
                    "comparator_pig_version"
                ],
                "comparator_backend_epoch": end.identity[
                    "comparator_backend_epoch"
                ],
                "comparator_model": end.identity["comparator_model"],
            },
            "backend": comparator_backend,
            "pig": comparator_pig,
            "router": _router_route_delta(
                start.router,
                end.router,
                comparator_upstream,
                wall_time,
            ),
            "throughput": {
                "successful_completion_goodput": _unavailable(
                    "success_token_linkage_not_exported"
                ),
                "raw_generation_work": comparator_backend[
                    "raw_generation_tokens"
                ],
            },
        },
        "comparison": _semantic_comparison(
            target_backend,
            comparator_backend,
            wall_time,
            comparison_eligible,
        ),
        "interpretation_limits": [
            "vLLM generation token counters measure raw scheduler work, not success-linked completion goodput",
            "vLLM request_generation_tokens histograms do not carry finished_reason labels",
            "snapshot endpoint gauges are not time-weighted running or waiting distributions",
            "Router identity changes require traffic conclusions to be split at the change time",
        ],
    }
