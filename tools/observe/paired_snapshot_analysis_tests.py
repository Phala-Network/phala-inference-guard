#!/usr/bin/env python3

from __future__ import annotations

import json
import math
import tempfile
import unittest
from pathlib import Path

from paired_snapshot_analysis import (
    analyze_paired_captures,
    bucket_table_delta,
    counter_delta,
    histogram_delta,
    load_capture,
)
from prometheus_text import parse_prometheus


class PrometheusParserTests(unittest.TestCase):
    def test_colon_names_multiple_series_and_escaped_labels(self) -> None:
        snapshot = parse_prometheus(
            '# TYPE vllm:generation_tokens_total counter\n'
            'vllm:generation_tokens_total{engine="0",model_name="a\\\\b\\\"c\\nline"} 4.2e+01\n'
            'vllm:generation_tokens_total{engine="1",model_name="other"} 8\n'
        )
        series = snapshot.series("vllm:generation_tokens_total")
        self.assertEqual(len(series), 2)
        self.assertEqual(series[0].labels["model_name"], 'a\\b"c\nline')
        self.assertEqual(sum(item.value for item in series), 50)

    def test_special_float_values_are_supported(self) -> None:
        snapshot = parse_prometheus("a +Inf\nb -Inf\nc NaN\n")
        self.assertEqual(snapshot.one("a").value, math.inf)
        self.assertEqual(snapshot.one("b").value, -math.inf)
        self.assertTrue(math.isnan(snapshot.one("c").value))

    def test_duplicate_exact_series_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "duplicate series"):
            parse_prometheus('a{x="1"} 1\na{x="1"} 2\n')


class CounterEvidenceTests(unittest.TestCase):
    def test_counter_delta_requires_exact_series_identity(self) -> None:
        start = parse_prometheus('x_total{engine="0"} 10\n')
        end = parse_prometheus('x_total{engine="1"} 12\n')
        result = counter_delta(start, end, "x_total")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(result["reason"], "series_identity_mismatch")

    def test_counter_reset_is_not_reported_as_progress(self) -> None:
        start = parse_prometheus('x_total{engine="0"} 10\n')
        end = parse_prometheus('x_total{engine="0"} 2\n')
        result = counter_delta(start, end, "x_total")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(result["reason"], "counter_reset")

    def test_counter_delta_preserves_label_breakdown(self) -> None:
        start = parse_prometheus('x_total{reason="a"} 10\nx_total{reason="b"} 20\n')
        end = parse_prometheus('x_total{reason="a"} 13\nx_total{reason="b"} 28\n')
        result = counter_delta(start, end, "x_total")
        self.assertEqual(result["status"], "available")
        self.assertEqual(result["delta"], 11)
        self.assertEqual(
            {row["labels"]["reason"]: row["delta"] for row in result["series"]},
            {"a": 3, "b": 8},
        )

    def test_counter_selector_does_not_double_count_hierarchical_lanes(self) -> None:
        start = parse_prometheus(
            'x_total{lane="global"} 10\nx_total{lane="child"} 7\n'
        )
        end = parse_prometheus(
            'x_total{lane="global"} 15\nx_total{lane="child"} 11\n'
        )
        result = counter_delta(start, end, "x_total", {"lane": "global"})
        self.assertEqual(result["status"], "available")
        self.assertEqual(result["delta"], 5)


class HistogramEvidenceTests(unittest.TestCase):
    def test_histogram_quantiles_use_bucket_delta_and_interpolation(self) -> None:
        start = parse_prometheus(
            'h_bucket{le="1"} 10\nh_bucket{le="2"} 20\n'
            'h_bucket{le="+Inf"} 20\nh_count 20\nh_sum 25\n'
        )
        end = parse_prometheus(
            'h_bucket{le="1"} 15\nh_bucket{le="2"} 30\n'
            'h_bucket{le="+Inf"} 30\nh_count 30\nh_sum 38\n'
        )
        result = histogram_delta(start, end, "h")
        self.assertEqual(result["status"], "available")
        self.assertEqual(result["count"], 10)
        self.assertAlmostEqual(result["quantiles"]["p50"]["value"], 1.0)
        self.assertAlmostEqual(result["quantiles"]["p95"]["value"], 1.9)

    def test_histogram_quantile_in_infinite_bucket_is_bounded_not_faked(self) -> None:
        start = parse_prometheus(
            'h_bucket{le="1"} 0\nh_bucket{le="+Inf"} 0\nh_count 0\nh_sum 0\n'
        )
        end = parse_prometheus(
            'h_bucket{le="1"} 1\nh_bucket{le="+Inf"} 10\nh_count 10\nh_sum 20\n'
        )
        result = histogram_delta(start, end, "h")
        self.assertEqual(result["quantiles"]["p95"]["status"], "lower_bounded")
        self.assertEqual(result["quantiles"]["p95"]["lower_bound"], 1)
        self.assertNotIn("value", result["quantiles"]["p95"])

    def test_histogram_schema_change_is_unavailable(self) -> None:
        start = parse_prometheus(
            'h_bucket{le="1"} 1\nh_bucket{le="+Inf"} 1\nh_count 1\nh_sum 1\n'
        )
        end = parse_prometheus(
            'h_bucket{le="2"} 2\nh_bucket{le="+Inf"} 2\nh_count 2\nh_sum 2\n'
        )
        result = histogram_delta(start, end, "h")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(result["reason"], "bucket_schema_mismatch")

    def test_non_monotonic_histogram_delta_is_unavailable(self) -> None:
        start = parse_prometheus(
            'h_bucket{le="1"} 0\nh_bucket{le="2"} 2\n'
            'h_bucket{le="+Inf"} 2\nh_count 2\nh_sum 2\n'
        )
        end = parse_prometheus(
            'h_bucket{le="1"} 5\nh_bucket{le="2"} 6\n'
            'h_bucket{le="+Inf"} 7\nh_count 7\nh_sum 7\n'
        )
        result = histogram_delta(start, end, "h")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(result["reason"], "non_monotonic_bucket_delta")

    def test_mixed_engine_bucket_schemas_are_not_aggregated(self) -> None:
        start = parse_prometheus(
            'h_bucket{engine="0",le="1"} 0\n'
            'h_bucket{engine="0",le="+Inf"} 0\n'
            'h_bucket{engine="1",le="2"} 0\n'
            'h_bucket{engine="1",le="+Inf"} 0\n'
            'h_count{engine="0"} 0\nh_count{engine="1"} 0\n'
            'h_sum{engine="0"} 0\nh_sum{engine="1"} 0\n'
        )
        end = parse_prometheus(
            'h_bucket{engine="0",le="1"} 1\n'
            'h_bucket{engine="0",le="+Inf"} 1\n'
            'h_bucket{engine="1",le="2"} 1\n'
            'h_bucket{engine="1",le="+Inf"} 1\n'
            'h_count{engine="0"} 1\nh_count{engine="1"} 1\n'
            'h_sum{engine="0"} 1\nh_sum{engine="1"} 2\n'
        )
        result = histogram_delta(start, end, "h")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(
            result["reason"], "bucket_schema_inconsistent_across_series"
        )

    def test_grouped_bucket_delta_rejects_non_monotonic_outcome(self) -> None:
        start = parse_prometheus(
            'input_bucket{le="1",outcome="admitted"} 0\n'
            'input_bucket{le="+Inf",outcome="admitted"} 2\n'
        )
        end = parse_prometheus(
            'input_bucket{le="1",outcome="admitted"} 5\n'
            'input_bucket{le="+Inf",outcome="admitted"} 6\n'
        )
        result = bucket_table_delta(start, end, "input_bucket")
        self.assertEqual(result["status"], "unavailable")
        self.assertEqual(result["reason"], "non_monotonic_bucket_delta")


def _backend(epoch: int, generated: int, prompt: int, stop: int) -> str:
    return f'''process_start_time_seconds {epoch}
vllm:generation_tokens_total{{engine="0",model_name="model-a"}} {generated}
vllm:prompt_tokens_total{{engine="0",model_name="model-a"}} {prompt}
vllm:num_preemptions_total{{engine="0",model_name="model-a"}} 0
vllm:num_requests_running{{engine="0",model_name="model-a"}} 1
vllm:num_requests_waiting{{engine="0",model_name="model-a"}} 0
vllm:prefix_cache_queries_total{{engine="0",model_name="model-a"}} {prompt}
vllm:prefix_cache_hits_total{{engine="0",model_name="model-a"}} {prompt // 2}
vllm:request_success_total{{engine="0",finished_reason="stop",model_name="model-a"}} {stop}
vllm:request_success_total{{engine="0",finished_reason="length",model_name="model-a"}} 0
vllm:request_success_total{{engine="0",finished_reason="abort",model_name="model-a"}} 0
vllm:request_success_total{{engine="0",finished_reason="error",model_name="model-a"}} 0
'''


def _target_pig(version: str, accepted: int, protected: int) -> str:
    return f'''pig_info{{version="{version}"}} 1
pig_backend_requests_total{{name="upstream",decision="accepted"}} {accepted}
pig_backend_requests_total{{name="upstream",decision="failed"}} 0
pig_backend_completed_total{{name="upstream"}} {accepted - 1}
pig_backend_proxy_errors_total{{name="upstream"}} 0
pig_predictive_admission_decisions_total{{decision="fit"}} {accepted}
pig_predictive_admission_decisions_total{{decision="risk"}} {protected}
pig_predictive_admission_protections_total{{reason="tps_reference",scope="load"}} {protected}
pig_predictive_tps_decisions_total{{result="protected",subreason="qos_budget_unobserved"}} {protected}
pig_predictive_request_streaming_total{{state="true"}} {accepted + protected}
pig_predictive_output_limit_comparison_total{{actual_bucket="le_64",declared_bucket="le_256"}} {accepted}
pig_predictive_admission_selection_input_tokens_bucket{{le="1024",outcome="admitted"}} {accepted}
pig_predictive_admission_selection_input_tokens_bucket{{le="+Inf",outcome="admitted"}} {accepted}
'''


def _comparator(pig_accepted: int, generated: int, prompt: int, stop: int) -> str:
    return f'''pig_version_info{{version="PIG-v0.8.12"}} 1
pig_backend_requests_total{{name="a",decision="accepted"}} {pig_accepted}
pig_backend_requests_total{{name="a",decision="failed"}} 0
pig_backend_completed_total{{name="a"}} {pig_accepted - 1}
pig_backend_proxy_errors_total{{name="a"}} 0
pig_requests_total{{lane="global",decision="accepted"}} {pig_accepted}
pig_completed_total{{lane="global"}} {pig_accepted - 1}
pig_rejected_total{{lane="global"}} 0
''' + _backend(200, generated, prompt, stop)


def _write_capture(
    root: Path,
    stamp: str,
    target_backend: str,
    target_pig: str,
    comparator: str,
    target_pig_version: str = "",
    target_pig_started: str = "pig-start-a",
    router_digest: str = "router-a",
    router_counter: int = 100,
    router_enabled: bool = True,
    router_omit: str | None = None,
) -> Path:
    path = root / stamp
    path.mkdir()
    manifest = {
        "captured_at": stamp,
        "target_upstream": "use1-19",
        "comparator_upstream": "use1-4c",
        "target_pig_version": target_pig_version,
        "comparator_pig_version": "PIG-v0.8.12",
        "target_pig_image": "registry/pig:0.12.18@sha256:abc",
        "target_pig_started": target_pig_started,
        "target_backend_epoch": "100",
        "comparator_backend_epoch": "200",
        "compose_sha256": "compose-a",
    }
    (path / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (path / "target-pig.prom").write_text(target_pig, encoding="utf-8")
    (path / "target-backend.prom").write_text(target_backend, encoding="utf-8")
    (path / "comparator-combined.prom").write_text(comparator, encoding="utf-8")
    router_routes = [
        {
            "upstream_name": upstream,
            "enabled": router_enabled,
            "processed": router_counter,
            "upstream_attempts": router_counter,
            "upstream_429": router_counter,
            "selected_by_cache": router_counter,
            "selected_by_load": router_counter,
            "selected_by_order": router_counter,
            "cache_rejected_by_pressure": router_counter,
            "pressure_passthrough": router_counter,
        }
        for upstream in ("use1-19", "use1-4c")
    ]
    if router_omit is not None:
        for route in router_routes:
            route.pop(router_omit, None)
    (path / "router.json").write_text(
        json.dumps(
            {
                "upstream_config_digest": router_digest,
                "routes": router_routes,
            }
        ),
        encoding="utf-8",
    )
    return path


class PairedCaptureTests(unittest.TestCase):
    def test_version_is_derived_without_overwriting_raw_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            capture = load_capture(path)
            self.assertEqual(capture.manifest["target_pig_version"], "")
            self.assertEqual(capture.identity["target_pig_version"]["value"], "PIG-v0.12.18")
            self.assertEqual(capture.identity["target_pig_version"]["source"], "pig_info")

    def test_epoch_change_blocks_all_backend_rates(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(101, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertFalse(result["evidence"]["comparison_eligible"])
            self.assertIn("target_backend_epoch_changed", result["evidence"]["errors"])
            self.assertEqual(result["target"]["backend"]["raw_generation_tokens"]["status"], "unavailable")

    def test_valid_pair_reports_raw_work_but_not_success_token_goodput(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 3610, 7220, 101),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 1810, 3620, 81),
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertTrue(result["evidence"]["comparison_eligible"])
            self.assertEqual(result["comparison"]["status"], "descriptive_only")
            self.assertEqual(
                result["comparison"]["causal_pig_effect"]["reason"],
                "traffic_cohort_not_matched",
            )
            self.assertEqual(result["wall_time_seconds"], 3600)
            self.assertEqual(result["target"]["backend"]["raw_generation_tokens"]["rate_per_second"], 1)
            self.assertEqual(result["target"]["backend"]["terminal_requests"]["by_finished_reason"]["stop"], 100)
            self.assertEqual(
                result["target"]["throughput"]["successful_completion_goodput"],
                {"status": "unavailable", "reason": "success_token_linkage_not_exported"},
            )
            self.assertEqual(
                result["comparator"]["pig"]["tps_decisions"],
                {"status": "unavailable", "reason": "not_exported"},
            )
            self.assertEqual(
                result["target"]["pig"]["tps_decisions"]["by_labels"]["result=protected,subreason=qos_budget_unobserved"],
                10,
            )

    def test_model_identity_drift_blocks_comparison(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            changed = _backend(100, 110, 220, 11).replace("model-a", "model-b")
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                changed,
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertFalse(result["evidence"]["comparison_eligible"])
            self.assertIn("target_model_identity_changed", result["evidence"]["errors"])

    def test_router_change_blocks_matched_routing_but_not_runtime_integrity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
                router_digest="router-b",
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertTrue(result["evidence"]["runtime_integrity_eligible"])
            self.assertFalse(result["evidence"]["matched_routing_eligible"])
            self.assertFalse(result["evidence"]["comparison_eligible"])

    def test_router_counter_reset_blocks_matched_routing_but_not_runtime(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
                router_counter=100,
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
                router_counter=10,
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertTrue(result["evidence"]["runtime_integrity_eligible"])
            self.assertFalse(result["evidence"]["matched_routing_eligible"])
            self.assertFalse(result["evidence"]["comparison_eligible"])
            self.assertIn(
                "target_router_counter_reset:processed",
                result["evidence"]["errors"],
            )
            self.assertIn(
                "comparator_router_counter_reset:processed",
                result["evidence"]["errors"],
            )

    def test_missing_required_router_counter_blocks_matched_routing(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
                router_omit="processed",
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
                router_omit="processed",
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertTrue(result["evidence"]["runtime_integrity_eligible"])
            self.assertFalse(result["evidence"]["matched_routing_eligible"])
            self.assertIn(
                "target_router_counter_unavailable:processed:field_not_exported",
                result["evidence"]["errors"],
            )

    def test_disabled_router_route_is_not_matched_traffic_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
                router_enabled=False,
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
                router_enabled=False,
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertTrue(result["evidence"]["runtime_integrity_eligible"])
            self.assertFalse(result["evidence"]["matched_routing_eligible"])
            self.assertIn(
                "target_router_disabled",
                result["evidence"]["errors"],
            )

    def test_target_pig_restart_blocks_runtime_comparison(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            start = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            end = _write_capture(
                root,
                "2026-08-22T01:00:00Z",
                _backend(100, 110, 220, 11),
                _target_pig("PIG-v0.12.18", 110, 11),
                _comparator(110, 110, 220, 11),
                target_pig_started="pig-start-b",
            )
            result = analyze_paired_captures(load_capture(start), load_capture(end))
            self.assertFalse(result["evidence"]["runtime_integrity_eligible"])
            self.assertIn("target_pig_started_changed", result["evidence"]["errors"])

    def test_recorded_source_hash_mismatch_is_not_silently_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = _write_capture(
                root,
                "2026-08-22T00:00:00Z",
                _backend(100, 10, 20, 1),
                _target_pig("PIG-v0.12.18", 10, 1),
                _comparator(10, 10, 20, 1),
            )
            (path / "SHA256SUMS").write_text(
                "0" * 64 + "  manifest.json\n", encoding="utf-8"
            )
            capture = load_capture(path)
            self.assertIn("recorded_sha256_mismatch:manifest.json", capture.consistency_errors)


if __name__ == "__main__":
    unittest.main()
