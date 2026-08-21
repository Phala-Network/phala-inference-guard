# PIG Observability

This document describes the current development source contract. A metrics
scrape captures one adapter telemetry snapshot; predictive metrics, the current
Router projection, and compatibility values are formatted from that snapshot.

## Endpoints

`/pig/metrics` returns PIG metrics. `/v1/metrics` appends a bounded copy of the
single upstream's Prometheus response. Both require the configured bearer token.
`GET` and `PATCH /admin/v1/predictive-policy` expose and atomically update the
runtime TPS reference policy. The admin endpoint always requires the same
single-value bearer authentication and never proxies to the backend.
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

PIG does not parse or aggregate backend TTFT histograms and exports no derived
`pig_backend_observed_ttft_*` zero placeholders. TTFT is not an admission gate;
when operational TTFT analysis is needed, use the backend's native metrics.

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

Runtime policy changes are exported with fixed cardinality:

```text
pig_predictive_policy_revision
pig_predictive_policy_last_updated_at_seconds
pig_predictive_policy_updates_total{result="applied"}
pig_predictive_policy_updates_total{result="invalid"}
pig_predictive_policy_updates_total{result="conflict"}
pig_predictive_policy_updates_total{result="failed"}
```

Decision logs include `policy_revision`; the periodic status line includes
`policy=<revision>/<startup|runtime_api>`. A changed TPS reference clears the
qualified TPS window, so metrics show the new reference with `ready=0` until
new post-revision evidence qualifies. Existing reservation and QoS lease gauges
must not drop merely because policy changed.

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

In particular, `pig_dynamic_global_limit` is the Router compatibility value
needed to represent the current open/closed projection. It is not the number of
arbitrary requests that the GPU can safely hold, and its meaning is not
comparable to the retired request-count limiter. Do not use either of these as a
cross-version utilization or saturation query:

```promql
pig_dynamic_observed_running / pig_dynamic_global_limit
pig_dynamic_observed_running_raw / pig_dynamic_global_limit_raw
```

Use the metric that represents the question instead. Examples:

```promql
# Current backend work, without the compatibility projection.
pig_backend_observed_running{name="upstream"}
pig_backend_observed_waiting{name="upstream"}

# Backend KV occupancy.
pig_backend_kv_active_tokens{name="upstream"}
/
clamp_min(pig_backend_kv_capacity_tokens{name="upstream"}, 1)

# Whether the current canonical minimum request is under Router backpressure.
pig_predictive_router_backpressure_active

# Cumulative admission outcomes and bounded protection attribution.
sum by (outcome) (rate(pig_predictive_admission_outcomes_total[5m]))
sum by (reason, scope) (rate(pig_predictive_admission_protections_total[5m]))
```

The first two queries describe backend work and KV load. The backpressure gauge
describes the current Router-facing state. The cumulative rates describe what
PIG admitted or protected over the selected window, including request-specific
protection that deliberately leaves the node open for smaller work. They must
not be collapsed into one synthetic `running / global_limit` percentage.

## Logs and cross-surface agreement

All runtime records use three stable leading fields:

```text
level=<info|warn|debug|error> component=<runtime|capability|controller|admission|policy> event=<name>
```

The Go logger supplies one UTC timestamp with microsecond precision. Ordinary
records do not repeat a second `observed_at` timestamp.

The startup record contains build identity, mode, observation cadence,
freshness, status interval, TPS reference, and effective log level. Capability
initialization is a one-time record of the frozen profile and its source/reason.
Policy updates are separate `component=policy event=update` records.
Fatal startup or serving errors use `level=error component=runtime event=exit`.

Default admission records are emitted only for policy protections. They use
`event=protection`, `warn` for an enforced reject, and `info` for a shadow
counterfactual. The compact fields are:

```text
mode enforced action reason scope prefill_class input_estimate_confidence
input_tokens prefill_compute_tokens cache_credit_tokens
kv_tokens=<effective/post-admit/remaining>
backend=<running/waiting>
sequences=<current/post-admit/limit>
tps=<mean-active/reference> tps_ready policy_revision suppressed
```

Each bounded action/reason/scope/enforcement signature emits at most once per
five seconds. Alternating reasons cannot reset another signature's interval.
`suppressed` reports how many equivalent records were omitted since that
signature's previous line. Admission counters and metrics are never sampled or
suppressed by logging.

`PIG_LOG_LEVEL=debug` keeps the compact record and adds
`event=protection_detail` with the complete numeric decision snapshot. Debug is
for short diagnostic windows; the default is `info`. Neither form contains
prompts, request bodies, credentials, request IDs, or endpoint hosts.

The compact periodic `component=controller event=status` line defaults to one
record every 30 seconds. Its slash-delimited groups are documented in order:

```text
counts=<attempts/admitted/request-protected/load-protected/availability-protected>
last=<last-action/last-reason>
capacity=<current-action/current-reason>
prefill=<current-canonical-class/current-canonical-input>
kv=<current-effective/current-canonical-post-admit/current-canonical-remaining>
cache=<valid/hit-fraction/credit-fraction>
tps=<mean-active/reference>
sequences=<current/post-admit/limit>
router=<active/scope/inspect-capacity>
observer=<fresh/intake-open>
backend=<running/waiting>
```

The current Controller snapshot, not the last request, supplies capacity, KV,
cache, TPS, sequence, Router, observer, and backend groups. The last request is
limited to the explicitly named `last` group. Detailed compatibility gauges
remain in metrics instead of being repeated in every status line.

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
