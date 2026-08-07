# PIG v0.12.2 Observability

PIG exports one predictive state. The request decision, status line, backend
projection, and Router compatibility values must come from one captured
telemetry snapshot per scrape.

## Endpoints

`/pig/metrics` returns PIG metrics. `/v1/metrics` appends a bounded copy of the
single upstream's Prometheus response. Both require the configured bearer token.
`/v1/upstream-status` returns:

| Code | Meaning |
| --- | --- |
| `0` | Fresh and open |
| `1` | Selective load protection; at least one hard-fit request can be inspected |
| `2` | Enforce intake closed |
| `3` | Shadow observation unavailable or status unknown |

## Admission metrics

The primary contract is:

- `pig_predictive_admission_mode_info` and
  `pig_predictive_admission_enforce` identify shadow versus enforce;
- `pig_predictive_admission_attempts_total` and
  `pig_predictive_admission_decisions_total{decision}` count active
  pre-forward decisions;
- `pig_predictive_admission_enforced_rejects_total` counts client-visible
  predictive 429 responses;
- `pig_predictive_admission_reservations`, forwarded-Prefill gauges, and retired
  reservation gauges expose lifecycle ownership;
- `pig_predictive_admission_prediction_duration_seconds` measures the active
  request-aware `Decide` path;
- `pig_predictive_admission_estimator_duration_seconds` measures bounded request
  classification and size estimation;
- `pig_predictive_admission_failures_total{phase}` exposes the five owned
  lifecycle phases: `close`, `decide`, `forward`, `prefill`, and `terminal`.
  Removed completion-learning and shadow-attribution phases are not exported as
  permanent zeroes.

Malformed JSON is not admission pressure. It increments only:

```text
pig_client_protocol_errors_total{reason="invalid_json"}
```

It must not increase predictive attempts, enforced rejects, general 429s, or
Router backpressure.

## Capability and observer metrics

`pig_predictive_capability_*` records the immutable startup profile: KV
capacity, block size, aligned soft/hard limits, cold-Prefill rate, and derived
Prefill boundaries.

`pig_backend_*` records the single upstream's coherent observed state and proxy
lifecycle: KV tokens, running, waiting, generation TPS validity, inflight,
accepted, completed, failures, and copy/proxy errors.

A partial scrape does not immediately zero these gauges. They retain the last
coherent observation until freshness expires. After expiration, validity and
intake metrics show the closed state.

## Request-aware decision metrics

`pig_predictive_request_aware_last_decision_info` carries bounded labels for
action, reason, pressure source, and Prefill class. Numeric gauges expose:

- estimated Prefill and selected input tokens;
- reserved, effective, post-admit, remaining, and allowance KV tokens;
- running, waiting, and effective sequence counts;
- aggregate, mean-active, and projected TPS proxies plus forecast validity;
- pending Prefill sequences/tokens and long/quiescent subsets;
- the equivalent last-decision post-admit Prefill state.

These fields explain why two requests can receive different decisions under the
same current backend pressure.

## Router projection

The authoritative fields are:

- `pig_predictive_router_backpressure_active`;
- `pig_predictive_router_backpressure_state_info`;
- `pig_predictive_router_inspect_capacity`;
- raw/effective running and global-limit gauges;
- activation and latest load-reject timestamps/counters.

Shadow always publishes inactive predictive Router backpressure and cannot
reduce capacity.

Enforce keeps a real load rejection visible as selective Router backpressure
for a bounded 1500 ms after the pre-forward decision when the current
one-block inspect probe would otherwise report open. This lets the live
1000-ms Router poll observe the protection while retaining inspect capacity one
for short traffic. A current snapshot that requires capacity zero remains
authoritative, and a fresh open snapshot clears the projection automatically
at the hold boundary. Scrapes and successful requests do not extend the hold.

The current Router parser still requires six compatibility names:

```text
pig_dynamic_observed_running_raw
pig_dynamic_observed_waiting_raw
pig_dynamic_observed_running
pig_dynamic_observed_waiting
pig_dynamic_global_limit_raw
pig_dynamic_global_limit
```

They are a read-only wire projection from the same predictive snapshot. They do
not represent a retained legacy controller. No other metric family from the
retired architecture is exported.

## Status log

The bounded periodic line starts with `PIG-v0.12.2` and includes mode, attempts,
fit/risk/unknown counts, enforced rejects, reservations, last action/reason,
Prefill estimate, KV post-admit values, TPS proxy/projection, Router scope and
inspect capacity, observer freshness/identity/running/waiting, and compatibility
running/limit values.

Every actual enforce protection must agree across:

1. HTTP 429;
2. predictive decision reason and counter;
3. bounded status/decision log;
4. upstream status;
5. predictive Router projection;
6. the six compatibility values in the same scrape.

A disagreement is a release stop condition because Router may otherwise keep
sending traffic to a node that PIG has already protected.
