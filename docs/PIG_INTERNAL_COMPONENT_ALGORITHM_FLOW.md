# PIG Internal Algorithm Flow

This document describes the current unversioned TPS-only source architecture.
PIG has one upstream and one pre-forward admission transaction. It does not
route, tokenize input, estimate KV or Prefill cost, learn model capacity, or
rewrite application requests.

## Startup

```text
configuration loader
  -> validate one upstream, shadow/enforce mode, metrics cadence, and TPS reference
  -> derive /metrics from the upstream origin unless explicitly configured
  -> fetch one coherent vLLM or SGLang metrics sample
  -> validate backend kind, model identity, running, waiting, generation,
     preemption, and runtime-start evidence
  -> construct AdmissionController and publish the startup observation
  -> construct reporter/runtime and start the 500 ms observer
  -> construct the single-upstream HTTP proxy
```

Startup does not call `/v1/models`, send a completion, warm the backend, inspect
prefixes, or derive KV/context/Prefill policy. Optional KV/cache values parsed
from backend metrics are operator telemetry only and are not passed into the
Controller.

The model name is hashed into a stable runtime identity. Backend kind or model
identity drift closes admission. A changed runtime-start value or a rollback in
generation/preemption counters starts a new Controller epoch and clears
incompatible rolling evidence and reservations.

## Route and request ownership

The HTTP boundary evaluates policies in this order:

```text
LocalManagementRoutePolicy
  -> PublicRoutePolicy(method + canonical exact path)
  -> AuthenticationPolicy
  -> AdmissionRoutePolicy
```

Local management routes are handled by PIG. Unknown paths, wrong methods,
encoded aliases, prefixes, suffixes, trailing slashes, repeated slashes, and
non-origin request targets receive a local generic OpenAI-shaped 404. They do
not read the body, call admission, reserve capacity, or contact the backend.

The generation routes are:

```text
POST /v1/chat/completions
POST /v1/completions
POST /v1/responses
```

`GET /v1/models` is authenticated and forwarded without admission.

## Bounded shape scan

For a generation route, `internal/app/request` reads at most the configured
body limit under a bounded concurrent byte budget and restores the exact bytes
and Content-Length for forwarding. The parser performs one linear scan and
extracts only:

- Chat/Completions `n`;
- Completions `best_of`;
- Completions prompt-batch cardinality;
- `stream`, only to select response-usage parsing.

`DecodeSequences` is the complete request demand. For Completions it is prompt
batch cardinality multiplied by `max(n, best_of)`; for Chat it is `n`;
Responses uses one sequence. Duplicate equal fanout values are accepted.
Conflicting, non-positive, fractional, mixed-shape, or overflowing fanout is
unsupported.

Malformed JSON returns a local OpenAI-shaped 400. A valid but unsupported
shape produces request-scoped `invalid_request`, no reservation, and no change
to canonical node capacity. When inspection is unavailable because of the
scanner's byte/depth bound, content type, read failure, or concurrent byte
budget, the request receives a one-sequence `fallback` demand and continues
through normal atomic TPS admission. Scanner limits are not independent 429
gates. Input text, tools, multimodal payloads, declared output limits, cache
identity, and token counts are not admission inputs. Forwarded bytes remain
unchanged.

## Pre-forward transaction

```text
TPSRequestDemand{DecodeSequences, Source}
  -> admissionRuntime.Decide
       -> AdmissionController.Admit under one mutex
            -> project fresh observation + local sequence overlay
            -> compute current and post-admit sequences
            -> evaluate the pure TPS gate
            -> atomically create a sequence reservation when admitted
       -> reporter records the immutable decision outside the Controller lock
  -> enforce: protected decision becomes local OpenAI-shaped 429
  -> shadow: protected decision is observable but the request is forwarded
  -> admitted: mark forwarded, proxy unchanged body, mark first response,
     then terminate exactly once
```

The admission hot path uses fixed-size TPS buckets and O(1) overlay counters.
It does not scan live reservations. Observation reconciliation may scan the
bounded reservation map because it runs at the polling cadence rather than on
every request.

## Rolling TPS evidence

The Controller keeps 61 one-second buckets covering the latest 60 seconds. A
window is ready after at least four qualified samples and eight qualified
sequence-seconds. Each coherent metrics interval contributes cumulative output
token delta and active sequence-seconds.

The denominator is the maximum defensible exposure among backend endpoint
running, locally forwarded sequence exposure, and local first-response
exposure. This prevents a request completed between polls from disappearing
from the denominator. A zero-token interval qualifies only when local response
exposure proves Decode was active; pure Prefill or idle running is not invented
as a Decode stall.

The configured reference remains a business target. Observed rates update the
window but never rewrite the reference or create a learned cap.

## TPS gate

When the reference is zero, the TPS gate is disabled. For a ready window:

```text
base_limit = max(1, floor(window_aggregate_tps / reference))
current = raw_running + raw_waiting + unobserved_local_sequences
post_admit = current + request_decode_sequences
```

The selected limit may increase by one through two bounded mechanisms:

1. current-rate recovery permits `raw_running + 1` when the current
   per-sequence rate has at least 5% headroom and, above one running sequence,
   the projected rate remains at least 95% of the reference;
2. one QoS-budget lease permits `base + 1` when current TPS is at least the
   reference and rolling surplus covers the predicted deficit until the next
   observation.

Only one QoS-budget lease may be live. Multi-sequence requests cannot use that
one-sequence lease. Unobserved local sequences, waiting, preemption, invalid
intervals, or insufficient surplus make it ineligible.

Any observed waiting or fresh preemption stops marginal admission for that
observation. It does not change the rolling base, start a cooldown, require
consecutive clear samples, or persist after the next fresh clear observation.

Before the window is ready, intake is bounded around two sequences while fresh
current-rate evidence may allow one further step. Ready-idle state may refill
atomically up to its still-valid rolling base. Once the 60-second evidence ages
out the window becomes unready and returns to warming. Complete request fanout
must satisfy the limit even when current demand is zero; there is no special
idle bypass, one-second timer, or background learner.

## Reservation lifecycle

```text
Reserved
  -> Forwarded
  -> ActiveDecode on first positive upstream response-body read
  -> Terminal exactly once
```

Each reservation owns the complete Decode sequence fanout. The overlay tracks:

- sequences not yet visible in backend running/waiting;
- all outstanding sequence liabilities;
- live reservations and residual debts;
- ownership of the single QoS-budget lease.

A request terminated before forwarding is removed immediately. Successful
ActiveDecode completion releases immediately. Forwarded failures, cancellation,
disconnect, timeout, or shutdown retain bounded residual debt until a sample
watermark proves the exposure is covered. Duplicate terminal calls and stale
epoch handles are no-ops.

`StartSampleWindow` captures the event watermark before metrics I/O. Publishing
the sample reconciles only events at or before that watermark. This prevents a
concurrent request from being counted neither locally nor upstream, while also
preventing permanent double counting after it becomes visible.

## Observation failure and shutdown

A fetch failure or incomplete sample leaves the last coherent observation
unchanged. Normal age calculation closes enforce intake after its freshness
deadline. The next coherent sample recovers immediately.

Numeric overflow, impossible aggregate state, identity drift, or lifecycle
corruption fails closed. Runtime counter reset advances the epoch and fences all
old handles. Shutdown stops the observer first, then closes Controller intake
and clears owned state.

## Reporting and Router projection

```text
Controller snapshot + reporter snapshot
  -> metrics
  -> compact status/protection logs
  -> /v1/upstream-status
  -> Router compatibility projection
```

Reporting never reruns policy and cannot mutate Controller state. A
request-scoped invalid shape does not close Router capacity. A load or
availability protection can project backpressure from the canonical one-
sequence capacity decision. A scanner fallback request is reported with
`demand_source=fallback`; it does not mutate canonical node capacity. Shadow
mode never applies Router backpressure.

Response-usage evidence is separate from admission. It may count exact
successful completion tokens for observability, but it does not feed the TPS
decision.

## Ownership map

| Owner | Responsibility |
| --- | --- |
| `internal/domain/request` | Strict one-pass JSON shape parser |
| `internal/app/request` | Bounded body ownership, shape classification, exact restoration |
| `internal/admission/tps_window.go` | Fixed-size rolling TPS evidence |
| `internal/admission/tps_gate.go` | Pure sequence-limit selection |
| `internal/admission/qos_budget.go` | One bounded rolling-surplus step |
| `internal/admission/controller.go` | Atomic decision, reservation, lifecycle, and epoch fencing |
| `internal/app/server/admission_backend_observer.go` | vLLM/SGLang TPS observation publication |
| `internal/app/server/admission_runtime.go` | Controller/reporter service boundary |
| `internal/app/server/proxy.go` | Route policy, mode mapping, forwarding, and terminal transaction |
| `internal/app/server/metrics.go` | Snapshot formatting and Router compatibility |

No component owns routing, customer tiers, request rewriting, tokenizer/model
profiles, KV/Prefill/input admission, TTFT protection, or online learning.
