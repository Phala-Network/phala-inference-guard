# PIG Observability

This document describes the current TPS-only source contract. Metrics, logs,
Router projection, and the policy API format immutable Controller snapshots;
none of them reruns policy or changes admission.

## Local endpoints

- `/pig/metrics` returns PIG metrics.
- `/v1/metrics` returns PIG metrics followed by a bounded copy of the
  single upstream's native Prometheus response.
- `/v1/upstream-status` returns the Router-facing capacity status.
- `GET/PATCH /admin/v1/predictive-policy` reads or atomically updates the
  TPS reference.

Metrics and policy endpoints preserve their configured bearer requirements and
are handled locally. They are never forwarded through the public inference
whitelist.

`/v1/upstream-status` values are:

| Code | Meaning |
| --- | --- |
| `0` | Open, or shadow mode with a fresh valid observation |
| `1` | Load protection while the canonical one-sequence inspect demand still fits |
| `2` | Enforce intake is closed |
| `3` | Shadow observation is unavailable or status is unknown |

## Admission totals

Use these counters to separate decisions from emitted HTTP responses:

```text
pig_predictive_admission_attempts_total
pig_predictive_admission_decisions_total{decision="fit|risk|unknown"}
pig_predictive_admission_outcomes_total{outcome="admitted|request_protected|load_protected|availability_protected"}
pig_predictive_admission_protections_total{reason,scope}
pig_predictive_admission_enforced_rejects_total
pig_rejected_total
```

`attempts_total` counts Controller evaluations. The fixed protection reasons are:

```text
controller_unavailable observation_missing observation_invalid
observation_stale invalid_request tps_reference runtime_identity_drift
resource_exhausted counter_overflow closed
```

`enforced_rejects_total` increments only when enforce mode emits a
pre-forward predictive 429. `pig_rejected_total` is the broader local 429
counter. Shadow decisions remain observable but do not increase enforced
rejects or Router backpressure.

Malformed JSON is a client protocol error:

```text
pig_client_protocol_errors_total{reason="invalid_json"}
```

It does not count as an admission attempt or 429. A disallowed method/path or
non-canonical request target increments only:

```text
pig_route_not_allowed_total
```

Route rejection does not read the request body, create a reservation, call the
backend, or change admission/backend counters.

## Request shape evidence

The request scanner exports:

```text
pig_predictive_scanner_inflight
pig_predictive_scanner_reserved_body_bytes
pig_predictive_scanner_saturated_total
pig_predictive_classifier_outcomes_total{outcome}
pig_predictive_request_streaming_total{state}
pig_predictive_request_decode_fanout_total{bucket}
pig_predictive_admission_decode_fanout_total{bucket,outcome}
```

Classifier outcomes distinguish supported requests, body limits/content type,
saturation/read failure, malformed JSON, unsupported request shape, bounded
`shape_scan_limit`, and unsupported endpoint. Fanout buckets are fixed and
contain no request, model, user, body, or token-value labels.

The last-decision metric uses `demand_source="request"` for a classified
request and `demand_source="fallback"` for a scanner-limited one-sequence
request or the canonical Router-capacity inspection:

```text
pig_predictive_tps_last_decision_info{
  action,reason,pressure_source,result,subreason,demand_source
}
pig_predictive_tps_request_decode_sequences
```

Any other source is normalized to `unknown`.

## TPS state

The primary current-state gauges are:

```text
pig_predictive_tps_reference
pig_predictive_tps_window_ready
pig_predictive_tps_window_qualified_samples
pig_predictive_tps_window_qualified_sequence_samples
pig_predictive_tps_window_qualified_sequence_seconds
pig_predictive_tps_window_aggregate
pig_predictive_tps_window_mean_active
pig_predictive_tps_current_interval_aggregate
pig_predictive_tps_current_interval_mean_active
pig_predictive_tps_current_interval_mean_active_valid
pig_predictive_tps_observed_running
pig_predictive_tps_observed_waiting
pig_predictive_tps_generation_delta
pig_predictive_tps_preemption_delta
pig_predictive_tps_unobserved_sequences
pig_predictive_tps_sequence_limit
pig_predictive_tps_current_sequences
pig_predictive_tps_post_admit_sequences
pig_predictive_tps_qos_budget_leases
pig_predictive_tps_last_decision_qos_budgeted
```

`window_aggregate` is output tokens per wall second over qualified intervals.
`window_mean_active` is output tokens per active sequence-second and is the
long-run QoS view. `sequence_limit/current/post_admit` explain the actual
pre-forward counterfactual. A healthy mean alone does not prove an admit if the
request fanout would exceed the selected limit.

The bounded decision reason matrix is:

```text
pig_predictive_tps_decisions_total{result,subreason}
```

Subreasons include `warming`, `idle`, `base_rate`,
`current_rate`, `waiting`, `preemption`, and the bounded
`qos_budget_*` outcomes. This matrix is the preferred way to distinguish
healthy base admission from waiting holds or surplus-lease ineligibility.

The rolling denominator is auditable through:

```text
pig_predictive_tps_denominator_selections_total{source}
pig_predictive_tps_denominator_sequence_seconds_total{source}
```

Sources distinguish endpoint running, local forwarded exposure, local response
exposure, fallback liability, ties, and no usable denominator.

## Reservation and lifecycle state

```text
pig_predictive_admission_reservations
pig_predictive_admission_virtual_decode_sequences
pig_predictive_admission_sequence_liabilities
pig_predictive_admission_residual_debts
pig_predictive_admission_failures_total{phase}
```

Lifecycle phases are exactly `close`, `decide`, `forward`,
`first_byte`, and `terminal`. A sustained nonzero residual-debt or
failure delta warrants lifecycle investigation; a transient liability until
the next observer watermark is expected.

## Timing

The four histograms separate Controller time from request inspection:

```text
pig_predictive_admission_prediction_duration_seconds
pig_predictive_admission_body_read_duration_seconds
pig_predictive_admission_shape_scan_duration_seconds
pig_predictive_admission_pre_forward_duration_seconds
```

`pre_forward` includes body read, shape scan, and decision. Benchmark and
production timing are diagnostic experience. Do not turn one body size, host,
percentile, or numeric value into a universal hard acceptance gate.

The retired `pig_predictive_admission_estimator_duration_seconds` metric is
not emitted. Dashboards must use `shape_scan_duration_seconds`.

## Successful completion goodput

```text
pig_predictive_successful_completion_tokens_total
pig_predictive_response_usage_outcomes_total{outcome}
pig_predictive_response_completion_tokens_total{bucket}
```

The label-free token counter sums exact successful
`usage.completion_tokens` for Chat/Completions and
`usage.output_tokens` for Responses. It increments only when the endpoint and
streaming format are known, the upstream response is eligible 2xx JSON/SSE, a
single valid final usage record is observed, and the PIG proxy terminal is
success.

Unavailable, malformed, censored, incomplete, non-2xx, timed-out, or
disconnected responses add no tokens. The bucket metric counts completed
requests by completion-token range; it is not a token sum. Response usage is
observability only and never feeds admission.

A process-stable goodput rate is:

```promql
rate(pig_predictive_successful_completion_tokens_total[5m])
```

Always pair it with request success/error rates and process identity. Counter
reset across a restart invalidates a naive window delta.

## Backend and Router views

`pig_backend_*` exposes proxy lifecycle plus current TPS-observer running,
waiting, freshness, and backend kind. `/v1/metrics` also includes the
upstream's native metrics, where operators can inspect KV/cache/Prefill values.
Those optional values are not admission inputs in TPS-only source and must not
be interpreted as a hidden gate.

Router compatibility is exposed through:

```text
pig_predictive_router_backpressure_active
pig_predictive_router_backpressure_applied
pig_predictive_router_backpressure_state_info{scope,reason,source}
pig_predictive_router_inspect_capacity
pig_dynamic_observed_running_raw
pig_dynamic_observed_running
pig_dynamic_observed_waiting_raw
pig_dynamic_observed_waiting
pig_dynamic_global_limit_raw
pig_dynamic_global_limit
```

Use the raw/effective pairs to audit whether PIG actually projected protection
to Router. Request-scoped invalid shapes do not close canonical node capacity.

## Logs

At `info`, PIG emits compact stable lines:

- `component=admission event=protection` for protected decisions, with mode,
  enforcement, reason/scope, TPS result/subreason, sequence tuple, backend
  running/waiting, rolling mean/reference, QoS lease count, and suppression;
- `component=controller event=status` at the configured interval, with
  counters, capacity, TPS state, reservations, Router projection, and observer
  freshness.

At `debug`, `event=protection_detail` adds bounded numeric lifecycle
and TPS evidence. It records no request body, prompt, prefix, token, bearer
value, customer identity, or secret.

## Retired predictive metrics

Current TPS-only source does not emit admission capability geometry, cache
credit, Prefill lifecycle/class, input/KV work, declared-output comparison, or
request-output-limit metrics. Historical dashboards and observation scripts
that query these families must be treated as historical or migrated before use.
