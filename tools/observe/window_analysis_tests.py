#!/usr/bin/env python3

from __future__ import annotations

import unittest

from window_analysis import ObservationWindow, analyze, counter_delta, overprotection_candidates


def sample(elapsed: int, **overrides: object) -> dict[str, str]:
    values: dict[str, object] = {
        "timestamp_utc": f"2026-08-22T00:{elapsed // 60:02d}:{elapsed % 60:02d}Z",
        "elapsed_seconds": elapsed,
        "pig_metrics_ok": 1,
        "vllm_metrics_ok": 1,
        "router_ok": 1,
        "gpu_ok": 1,
        "containers_ok": 1,
        "compose_sha256": "compose-a",
        "pig_container_id": "pig-a",
        "pig_started_at": "pig-start-a",
        "vllm_container_id": "vllm-a",
        "vllm_started_at": "vllm-start-a",
        "haproxy_container_id": "haproxy-a",
        "haproxy_started_at": "haproxy-start-a",
        "ingress_container_id": "ingress-a",
        "ingress_started_at": "ingress-start-a",
        "pig_tps_reference": 25,
        "pig_tps_window_ready": 1,
        "pig_tps_window_mean_active": 50,
        "pig_backpressure_active": 0,
        "pig_backpressure_scope": "none",
        "vllm_running": 2,
        "vllm_waiting": 0,
        "vllm_kv_usage_fraction": 0.1,
        "gpu_utilization_percent": 60,
        "gpu_memory_used_mib": 100,
        "router_use1_19_attempts": 100,
        "router_use1_19_processed": 90,
        "router_use1_19_429": 10,
        "router_use1_19_selected_cache": 50,
        "router_use1_19_selected_load": 40,
        "router_use1_19_selected_order": 0,
        "pig_accepted_total": 90,
        "pig_completed_total": 80,
        "pig_failed_total": 1,
        "pig_proxy_errors_total": 0,
        "pig_enforced_rejects_total": 10,
        "pig_fit_total": 90,
        "pig_risk_total": 10,
        "vllm_generation_tokens_total": 1000,
        "vllm_preemptions_total": 0,
        "vllm_prefix_queries_total": 500,
        "vllm_prefix_hits_total": 250,
        "pig_cache_valid": 1,
        "pig_cache_hit_fraction": 0.5,
        "pig_prediction_count": 100,
        "pig_prediction_sum_seconds": 1,
        "pig_body_read_count": 100,
        "pig_body_read_sum_seconds": 0.5,
        "pig_estimator_count": 100,
        "pig_estimator_sum_seconds": 0.25,
        "pig_pre_forward_count": 100,
        "pig_pre_forward_sum_seconds": 2,
        "pig_restarts": 0,
        "vllm_restarts": 0,
        "haproxy_restarts": 0,
        "ingress_restarts": 0,
        "pig_oom": 0,
        "vllm_oom": 0,
        "haproxy_oom": 0,
        "ingress_oom": 0,
        "pig_status": "running",
        "vllm_status": "running",
        "haproxy_status": "running",
        "ingress_status": "running",
    }
    values.update(overrides)
    return {key: str(value) for key, value in values.items()}


class CounterDeltaTests(unittest.TestCase):
    def test_reset_is_excluded_without_discarding_later_progress(self) -> None:
        rows = [
            sample(0, counter=100),
            sample(30, counter=110),
            sample(60, counter=3),
            sample(90, counter=8),
        ]
        result = counter_delta(rows, "counter", ("vllm_container_id",))
        self.assertEqual(result.delta, 15)
        self.assertEqual(result.covered_seconds, 60)
        self.assertEqual(result.reset_intervals, 1)

    def test_identity_change_is_not_counted_as_counter_progress(self) -> None:
        rows = [
            sample(0, counter=100),
            sample(30, counter=110),
            sample(60, counter=3, vllm_container_id="vllm-b"),
            sample(90, counter=8, vllm_container_id="vllm-b"),
        ]
        result = counter_delta(rows, "counter", ("vllm_container_id",))
        self.assertEqual(result.delta, 15)
        self.assertEqual(result.identity_change_intervals, 1)
        self.assertEqual(result.reset_intervals, 0)

    def test_missing_scrape_is_not_bridged(self) -> None:
        rows = [
            sample(0, counter=100),
            sample(30, counter=110, vllm_metrics_ok=0),
            sample(60, counter=120),
            sample(90, counter=125),
        ]
        result = counter_delta(
            rows,
            "counter",
            ("vllm_container_id",),
            ("vllm_metrics_ok",),
        )
        self.assertEqual(result.delta, 5)
        self.assertEqual(result.covered_seconds, 30)
        self.assertEqual(result.missing_intervals, 2)


class CausalityTests(unittest.TestCase):
    def test_overprotection_screen_requires_offered_demand(self) -> None:
        rows = [
            sample(0, router_use1_19_attempts=100),
            sample(
                30,
                router_use1_19_attempts=100,
                pig_backpressure_active=1,
                pig_backpressure_scope="load",
                gpu_utilization_percent=10,
            ),
            sample(
                60,
                router_use1_19_attempts=101,
                pig_backpressure_active=1,
                pig_backpressure_scope="load",
                gpu_utilization_percent=10,
            ),
        ]
        result = overprotection_candidates(rows, 25)
        self.assertEqual(result["candidate_intervals"], 1)
        self.assertEqual(result["candidate_duration_seconds"], 30)

    def test_overprotection_screen_does_not_bridge_incomplete_samples(self) -> None:
        rows = [
            sample(0, router_use1_19_attempts=100),
            sample(30, router_ok=0, router_use1_19_attempts=101),
            sample(
                60,
                router_use1_19_attempts=102,
                pig_backpressure_active=1,
                pig_backpressure_scope="load",
                gpu_utilization_percent=10,
            ),
        ]
        self.assertEqual(overprotection_candidates(rows, 25)["candidate_intervals"], 0)

    def test_raw_generation_is_not_reported_as_successful_goodput(self) -> None:
        result = analyze(
            ObservationWindow(
                [sample(0), sample(30, vllm_generation_tokens_total=1300)]
            )
        )
        self.assertIsNone(result["throughput"]["successful_completion_goodput"])
        self.assertEqual(
            result["throughput"]["raw_generation_throughput"]["delta"], 300
        )
        self.assertEqual(result["source_mapping"]["backend_family"], "vllm")
        self.assertIsNone(
            result["source_mapping"]["successful_completion_goodput"]
        )

    def test_pre_forward_mean_uses_counter_deltas(self) -> None:
        result = analyze(
            ObservationWindow(
                [
                    sample(0),
                    sample(
                        30,
                        pig_prediction_count=110,
                        pig_prediction_sum_seconds=1.2,
                        pig_pre_forward_count=110,
                        pig_pre_forward_sum_seconds=2.5,
                    ),
                ]
            )
        )
        self.assertAlmostEqual(
            result["pre_forward_latency"]["prediction_mean_seconds"], 0.02
        )
        self.assertAlmostEqual(
            result["pre_forward_latency"]["total_pre_forward_mean_seconds"], 0.05
        )


class EvidenceGateTests(unittest.TestCase):
    def test_runtime_restart_invalidates_formal_checkpoint(self) -> None:
        result = analyze(
            ObservationWindow(
                [
                    sample(0),
                    sample(
                        30,
                        vllm_started_at="vllm-start-b",
                        vllm_restarts=1,
                        vllm_generation_tokens_total=10,
                    ),
                ]
            )
        )
        integrity = result["runtime_integrity"]
        self.assertFalse(integrity["integrity_eligible"])
        self.assertIn("runtime_identity_changed", integrity["formal_stop_reasons"])
        self.assertIn("container_restarted", integrity["formal_stop_reasons"])

    def test_incomplete_scrape_invalidates_formal_checkpoint(self) -> None:
        result = analyze(
            ObservationWindow(
                [sample(0), sample(30, router_ok=0), sample(60)]
            )
        )
        self.assertIn(
            "incomplete_samples", result["runtime_integrity"]["formal_stop_reasons"]
        )

    def test_large_sampling_gap_invalidates_formal_checkpoint(self) -> None:
        result = analyze(ObservationWindow([sample(0), sample(30), sample(120)]))
        self.assertIn("sample_gap", result["runtime_integrity"]["formal_stop_reasons"])

    def test_one_fast_sample_does_not_make_nominal_cadence_a_gap(self) -> None:
        result = analyze(
            ObservationWindow([sample(0), sample(1), sample(31), sample(61)])
        )
        self.assertNotIn("sample_gap", result["runtime_integrity"]["formal_stop_reasons"])

    def test_stopped_container_invalidates_formal_checkpoint(self) -> None:
        result = analyze(
            ObservationWindow([sample(0), sample(30, vllm_status="exited")])
        )
        self.assertIn(
            "container_not_running", result["runtime_integrity"]["formal_stop_reasons"]
        )

    def test_partial_window_cannot_pass_a_formal_horizon(self) -> None:
        result = analyze(
            ObservationWindow([sample(0), sample(30)]), horizon="stability"
        )
        self.assertTrue(result["runtime_integrity"]["integrity_eligible"])
        self.assertFalse(result["checkpoint"]["formal_checkpoint_eligible"])
        self.assertIn(
            "insufficient_samples", result["checkpoint"]["qualification_reasons"]
        )

    def test_complete_release_horizon_is_eligible(self) -> None:
        rows = [
            sample(
                index * 5,
                pig_accepted_total=90 + index,
                pig_completed_total=80 + index,
                pig_fit_total=90 + index,
                vllm_generation_tokens_total=1000 + index * 100,
                router_use1_19_attempts=100 + index,
                router_use1_19_processed=90 + index,
                pig_prediction_count=100 + index,
                pig_prediction_sum_seconds=1 + index / 1000,
                pig_body_read_count=100 + index,
                pig_body_read_sum_seconds=0.5 + index / 1000,
                pig_estimator_count=100 + index,
                pig_estimator_sum_seconds=0.25 + index / 1000,
                pig_pre_forward_count=100 + index,
                pig_pre_forward_sum_seconds=2 + index / 1000,
            )
            for index in range(360)
        ]
        result = analyze(ObservationWindow(rows), horizon="release")
        self.assertTrue(result["runtime_integrity"]["integrity_eligible"])
        self.assertTrue(result["checkpoint"]["formal_checkpoint_eligible"])
        self.assertEqual(result["checkpoint"]["qualification_reasons"], [])


if __name__ == "__main__":
    unittest.main()
