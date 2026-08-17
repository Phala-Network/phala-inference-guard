# PIG Observability

This document describes the current development source contract. A metrics
scrape captures one adapter telemetry snapshot; predictive metrics, the current
Router projection, and compatibility values are formatted from that snapshot.

## Endpoints

`/pig/metrics` returns PIG metrics. `/v1/metrics` appends a bounded copy of the
single upstream's Prometheus response. Both require the configured bearer token.
`/v1/upstream-status` returns:

| Code | Meaning |
| --- | --- |
| `0` | Open, or shadow mode with a fresh valid observation |
| `1` | Current load protection while the canonical inspect request still has positive capacity |
| `2` | Current enforce intake closed |
| `3` | Shadow observation unavailable or status unknown |

## Admission metrics

The primary contract is:

- `pig_predictive_admission_mode_info` and
  `pig_predictive_admission_enforce` identify shadow versus enforce;
- `pig_predictive_admission_attempts_total` and
  `pig_predictive_admission_decisions_total{decision}` count pre-forward
  evaluations;
- `pig_predictive_admission_enforced_rejects_total` counts HTTP requests for
  which the proxy actually emitted a predictive response instead of
  forwarding;
- `pig_predictive_admission_reservations` and the forwarded-Prefill gauges
  expose current lifecycle ownership;
- prediction, body-read, estimator, and total pre-forward histograms separate
  policy cost from request inspection cost; and
- `pig_predictive_admission_failures_total{phase}` exposes the five owned
  lifecycle phases: `close`, `decide`, `forward`, `prefill`, and `terminal`.

Completed-Decode credits, retired reservations, retired evictions, completion
learning, and shadow attribution are not current state and are not exported as
permanent zero-valued metrics.

Malformed JSON is client protocol failure, not admission pressure. It increments
only:

```text
pig_client_protocol_errors_total{reason="invalid_json"}
```

It must not increase predictive attempts, predictive rejects, general 429s, or
Router backpressure.

## Capability and observer metrics

`pig_predictive_capability_*` records the immutable startup profile: model/KV
identity geometry, aligned hard limit, maximum admissible input, fixed Prefill
class boundaries, and current open/contended Prefill budgets. The profile source
is `automatic` or `explicit`; initialization reason is `metadata` or
`explicit_override`. There is no metadata fallback and no measured or learned
Prefill rate.

`pig_backend_*` records the single upstream's coherent observed state and proxy
lifecycle: KV tokens, running, waiting, generation TPS validity, inflight,
accepted, completed, failures, and copy/proxy errors. The instantaneous TPS
proxy and latest generation delta are diagnostics only. They are distinct from
the qualified sustained window consumed by `TPSGate` when a positive reference
is configured.

A partial scrape does not immediately zero gauges or close intake. PIG retains
the last coherent observation until freshness expires. A coherent replacement
sample recovers current state without a separate cooldown or timer.

## Request-aware decision metrics

`pig_predictive_request_aware_last_decision_info` carries bounded labels for
action, reason, pressure source, and Prefill class. Current actions are
`admit`, `size_protect`, and `hard_protect`. Current policy reasons are:

```text
open controller_unavailable observation_missing observation_invalid
observation_stale invalid_request input_limit kv_capacity
prefill_contention prefill_budget prefill_exclusive prefill_quiescent
tps_reference capability_drift resource_exhausted counter_overflow closed
```

Numeric gauges expose:

- selected/estimated input and reserved tokens;
- effective, post-admit, remaining, and allowance KV tokens;
- raw running/waiting plus effective sequence diagnostics;
- aggregate and mean-active TPS proxies as telemetry only;
- current pending Prefill sequences/tokens and long/quiescent owners; and
- the equivalent pending-Prefill state captured for the last decision.

The current pending gauges may advance after a decision because lifecycle state
has changed. The `last_decision_*` gauges preserve the decision-time values so
operators can distinguish those two snapshots.

The sustained TPS contract is exported without request/user/model labels:

```text
pig_predictive_tps_reference
pig_predictive_tps_window_ready
pig_predictive_tps_window_qualified_samples
pig_predictive_tps_window_qualified_sequence_seconds
pig_predictive_tps_window_aggregate
pig_predictive_tps_window_mean_active
pig_predictive_tps_unobserved_sequences
pig_predictive_tps_sequence_limit
pig_predictive_tps_current_sequences
pig_predictive_tps_post_admit_sequences
```

The reference and window values describe the current Controller-owned trailing
window. The last three values are the canonical minimum request's current
pre-forward projection. A not-ready window with a positive reference reports
the bounded warming limit, so an operator can distinguish cold-start
protection from a mature rate-derived limit.
`pig_predictive_tps_unobserved_sequences` is the bounded local contribution not
yet covered by the latest metrics watermark; it normally returns to zero on the
next coherent poll and makes same-poll protection auditable.
`pig_predictive_request_aware_*_tps_proxy` remains the latest-interval diagnostic
and must not be interpreted as the sustained policy window.

Periodic status logs distinguish `last=<action>/<reason>` from the live
`capacity=<action>/<reason>` canonical probe. TPS protection caused by a metrics
update is therefore visible before another request produces a decision log.

Every enforced protection records a bounded last-reject reason, source, scope,
and timestamp. The timestamp is telemetry only: it neither keeps intake closed
nor overrides a later current-capacity result.

## Router projection

Router-visible state is a pure projection of `AdmissionController.Snapshot`.
The snapshot contains the canonical minimum-request decision already evaluated
from the same Controller-owned observation and reservation overlay used by
admission; the reporting path never reruns policy or reserves work:

```text
candidate protected + canonical admitted    -> request scope
candidate protected + canonical protected   -> load scope
stale, invalid identity, or unavailable      -> availability scope
```

A request-scoped 429 is fully visible in decision metrics and logs, but does
not close a node whose current canonical request fits. Load and availability
protection publish non-open current capacity. As soon as a new observation or
lifecycle event makes the canonical probe fit, capacity recovers immediately.
There is no recent-reject hold.

The authoritative Router fields are:

- `pig_predictive_router_backpressure_active`;
- `pig_predictive_router_backpressure_state_info`;
- `pig_predictive_router_inspect_capacity`;
- raw/effective running and global-limit gauges; and
- transition counters/timestamps plus the separate last-reject telemetry.

Shadow always publishes inactive predictive Router backpressure and never
reduces capacity.

The current Router parser also requires six compatibility names:

```text
pig_dynamic_observed_running_raw
pig_dynamic_observed_waiting_raw
pig_dynamic_observed_running
pig_dynamic_observed_waiting
pig_dynamic_global_limit_raw
pig_dynamic_global_limit
```

They are a read-only projection of the predictive snapshot, not a retained
request-count controller. No other retired dynamic-QoS behavior is authoritative.

## Logs and cross-surface agreement

The startup line records build identity, mode, 500-ms default observation
cadence, and freshness. Capability initialization logs the frozen profile and
its source/reason. Bounded decision logs include enforced state, runtime and
HTTP reasons, scope, request class/estimate, KV counterfactual, current
running/waiting, the watermark-bounded unobserved sequence reservation, TPS
diagnostics, and pending Prefill state.

The periodic status line includes mode, attempts, outcomes, actual enforced
rejects, reservations, last action/reason, Prefill class/estimate, KV values,
TPS telemetry, current Router scope/capacity, observer validity, and the six
compatibility values.

For every enforce protection, audit agreement across:

1. the client-visible HTTP result;
2. decision reason, source, and scope telemetry;
3. bounded decision/status logs;
4. the current `/v1/upstream-status` result;
5. the current predictive Router projection; and
6. the six compatibility values from the same scrape.

Agreement is scope-aware. A request-scoped rejection must remain visible while
current Router capacity stays open for fitting smaller work. A load- or
availability-scoped protection must publish the matching non-open current
capacity. Any hidden reject or stale capacity clamp is a release stop condition.
