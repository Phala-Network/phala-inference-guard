# PIG Internal Algorithm Flow

PIG has one upstream and one atomic pre-forward admission transaction. It does
not route, tokenize input, estimate KV or Prefill cost, learn capacity, or
rewrite application requests.

## Startup

```text
load and validate infrastructure plus TPS/bound initialization
  -> derive /metrics unless explicitly configured
  -> fetch one coherent vLLM or SGLang metrics sample
  -> validate backend kind, model identity, running, waiting, generation,
     preemption, and runtime-start evidence
  -> explicit running limit wins
  -> otherwise, SGLang may read strict same-origin /server_info once
  -> construct AdmissionController with TPS reference and fixed bounds
  -> publish startup observation and start the 500 ms observer
  -> construct the single-upstream HTTP proxy
```

vLLM standard metrics do not expose a trusted configured maximum, so no limit
is inferred from current running. SGLang discovery accepts only a coherent
top-level positive integer `max_running_requests`; any failure leaves the limit
`unknown/0`. This is initialization, not learning. Startup sends no inference
request and does not call `/v1/models` to construct policy.

The model name is hashed into runtime identity. Identity drift closes
admission. A changed runtime-start value or generation/preemption counter
rollback advances the controller epoch, clears incompatible TPS evidence and
reservations, and fences old handles.

## Route and request ownership

```text
LocalManagementRoutePolicy
  -> PublicRoutePolicy(method + canonical exact path)
  -> AuthenticationPolicy
  -> AdmissionRoutePolicy
```

PIG handles local management routes itself. Unknown paths, wrong methods,
encoded aliases, prefixes, suffixes, trailing or repeated slashes, and
non-origin targets receive a local generic OpenAI-shaped 404 without body read,
admission, reservation, or backend call.

Generation admission applies to `POST /v1/chat/completions`,
`POST /v1/completions`, and `POST /v1/responses`. `GET /v1/models` is
authenticated but has no sequence reservation.

`internal/app/request` owns a bounded, one-pass JSON shape scan and restores the
exact bytes for forwarding. It extracts only Decode fanout (`n`, Completions
`best_of`, prompt-batch cardinality) and streaming state for response-usage
parsing. Input text, tools, multimodal content, declared output limits, token
counts, and cache identity are not admission features.

Malformed JSON returns local 400. Unsafe fanout is request-scoped protection.
When bounded inspection is unavailable, the request receives one labelled
fallback sequence through the same atomic policy.

## Admission transaction

```text
TPSRequestDemand{DecodeSequences, Source}
  -> AdmissionController.Admit under one mutex
       -> project fresh observation plus local overlay
       -> evaluate pure TPS health
       -> evaluate projected running bound
       -> evaluate same-observation window bound
       -> atomically create complete-fanout reservation only when all fit
  -> reporter records immutable decision
  -> enforce protection: local OpenAI-shaped 429, no backend call
  -> shadow protection: record only, forward without reservation
  -> admit: mark forwarded, proxy unchanged request, mark first response,
     terminate exactly once
```

The hot decision path is O(1) and does not scan live reservations. Observation
reconciliation may scan the bounded reservation map at polling cadence.

## TPS health

The controller keeps 61 one-second buckets covering the latest 60 seconds. A
window is ready after at least four qualified samples and eight qualified
sequence-seconds. Each coherent interval contributes generation-token delta
and the maximum defensible Decode exposure among backend running, local
forwarded exposure, local response exposure, and bounded fallback liability.

A zero-token interval qualifies only when local response exposure proves Decode
activity. Without reliable Decode evidence, idle or Prefill is not fabricated
as a zero-TPS stall. The latest qualified interval is stored separately from
the rolling window.

The pure gate is:

```text
reference disabled                         -> open/disabled
waiting > 0                                -> protect/waiting
fresh preemption delta > 0                 -> protect/preemption
rolling window not ready                   -> open/warming
latest interval not qualified              -> open/no_current_evidence
rolling mean >= reference                  -> open/healthy_window
latest qualified mean >= reference         -> open/recovered_current
rolling and latest both below reference    -> protect/below_reference
```

TPS never selects or learns a concurrency limit. One current dip cannot close a
healthy long window, while one qualified current recovery does not wait for old
low samples to expire.

## Running and window bounds

When TPS health is open:

```text
projected_running = raw_running + raw_waiting
                  + unobserved_sequences + request_decode_sequences

projected_window = unobserved_sequences + request_decode_sequences
```

The running gate fits when its limit is disabled or `projected_running <=
running_limit`. The window gate fits when `projected_window <=
window_concurrency`. Arithmetic overflow protects availability. The complete
fanout is charged atomically; partial reservation is impossible.

The window bound limits only work not yet reflected by backend metrics. Once a
fresh observation reconciles it, a healthy backend can admit another cohort.
It is not a total sustainable-concurrency cap.

## Reservation lifecycle

```text
Reserved -> Forwarded -> ActiveDecode -> Terminal exactly once
```

Each reservation owns complete Decode fanout. The overlay tracks unobserved
sequences, sequence liabilities, live reservations, and residual debt. A
request terminated before forwarding is removed immediately. Successful active
Decode completion releases immediately. Forwarded failures, cancellation,
disconnect, timeout, or shutdown retain bounded debt until an observation
watermark proves coverage. Duplicate terminal calls and stale-epoch handles are
no-ops.

`StartSampleWindow` captures the event watermark before metrics I/O. Publishing
reconciles only events at or before that watermark, preventing both missing and
double-counted concurrent work.

The window-concurrency histogram samples unreconciled sequences before each
successful reconciliation. It is observer-path work and never adds a bucket
loop to request admission.

## Policy administration

The revisioned controller policy contains TPS reference, window concurrency,
running limit, and running-limit source. A PATCH can update any subset under one
compare-and-swap. Only a changed TPS reference resets TPS buckets; bound changes
leave health evidence intact. An admin running-limit value, including zero,
sets source `admin`.

## Reporting and Router projection

```text
Controller snapshot + reporter snapshot
  -> metrics and fixed histograms
  -> compact status/protection logs
  -> /v1/upstream-status
  -> Router compatibility projection
```

Reporting never reruns policy. A request-scoped invalid shape does not close
Router capacity. Current load or availability protection can project
backpressure from the canonical one-sequence capacity inspection. Shadow never
applies Router backpressure. Response usage is goodput observability only.

## Ownership map

| Owner | Responsibility |
| --- | --- |
| `internal/domain/request` | Strict one-pass request-shape parser |
| `internal/app/request` | Bounded body ownership and exact restoration |
| `internal/admission/tps_window.go` | Rolling/latest TPS evidence and denominator selection |
| `internal/admission/tps_gate.go` | Pure TPS health predicate |
| `internal/admission/admission_bounds.go` | Pure running/window projection |
| `internal/admission/controller.go` | Atomic decision, reservation, lifecycle, epoch fencing, histogram state |
| `internal/admission/policy_update.go` | Revisioned partial policy update |
| `internal/app/server/predictive_running_limit.go` | Strict optional SGLang initialization discovery |
| `internal/app/server/admission_backend_observer.go` | Normalized vLLM/SGLang observation publication |
| `internal/app/server/admission_runtime.go` | Controller/reporter service boundary |
| `internal/app/server/proxy.go` | Mode mapping, forwarding, terminal transaction |
| `internal/app/server/metrics.go` | Snapshot formatting and Router compatibility |

No component owns backend routing, customer tiers, request rewriting,
tokenizer/model profiles, KV/Prefill/input admission, TTFT protection, queueing,
or online learning.
