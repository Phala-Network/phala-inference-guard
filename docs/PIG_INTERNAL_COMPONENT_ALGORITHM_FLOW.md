# PIG Internal Algorithm Flow

This document describes the current unversioned clean-admission development
architecture. PIG has one predictive-admission transaction and exactly one
upstream. Request inspection, backend observation, admission state, HTTP mode,
lifecycle, and reporting have separate owners so no telemetry or compatibility
path becomes a second controller.

## Startup

```text
configuration loader
  -> validate one upstream, shadow/enforce mode, and optional TPS reference
  -> derive /metrics from the upstream origin
  -> probe coherent vLLM model identity and exact KV geometry
  -> read one matching positive max_model_len from /v1/models
  -> freeze block-aligned KV/input limits and fixed Prefill bands/budgets
  -> construct AdmissionController and publish the startup observation
  -> construct AdmissionReporter and AdmissionRuntime
  -> start VLLMObserver, then construct the single-upstream Proxy
```

Automatic metadata is fail-closed: `/v1/models` must contain exactly one model
whose ID matches the metric identity. There is no geometry fallback. A complete
explicit capability override exists only for controlled tests or an audited
non-default deployment.

Startup performs no completion, warmup, cache lookup, or performance probe. The
Observer polls every 500 ms by default. Model identity, KV geometry, maximum
input, and Prefill policy are immutable for the Controller lifetime and are not
learned. The optional TPS reference is a deployment business target; observed
Decode rates update a bounded trailing window but never rewrite the reference.

## Pre-forward transaction

```text
protected HTTP path
  -> RequestClassifier acquires one bounded scan token
  -> read the bounded body and restore exact bytes plus Content-Length
  -> malformed JSON becomes protocol HTTP 400
  -> FastWorkEstimator creates one RequestEstimate
  -> AdmissionRuntime.Decide
       -> AdmissionController.Admit(now, estimate), under one lock
            -> derive block-rounded RequestWork from immutable capability
            -> project coherent observation + positive reservation aggregate
            -> evaluate ContextGate, KVGate, PrefillGate, and optional TPSGate
            -> evaluate canonical minimum work on the same projected state
            -> create immutable DecisionRecord
            -> atomically reserve when admitted
       -> AdmissionReporter records the immutable decision outside Controller
  -> HTTP boundary applies mode
       enforce protected: OpenAI-compatible HTTP 429, no upstream call
       shadow protected: forward without a hypothetical reservation
       admitted in either mode: track the same reservation lifecycle
  -> forward the original application request unchanged
```

An unsupported or temporarily unclassifiable estimate becomes an observable
`invalid_request` decision. It is request-scoped and reserves nothing, so it
cannot close canonical node capacity or lock a later fitting request. In shadow
it is forwarded without a hypothetical reservation.

The canonical minimum work is derived by the same immutable capability and
block rounding as a business request. It creates no reservation. There is no
public inspect-and-reserve variant and no separate shadow policy.

## State and pure Gates

Controller availability requires an open Controller and one coherent, fresh
observation. `StateProjector` then adds the checked positive reservation
aggregate to the observation. It performs no policy decision and no mutation.

The ordered pure Gates are:

1. `ContextGate`: the selection estimate and Decode horizon must fit the frozen
   maximum input and upstream context;
2. `KVGate`: observed KV + reservation overlay + candidate total KV must not
   exceed the block-aligned hard limit; and
3. `PrefillGate`: the candidate class and pending Prefill aggregate must fit the
   current open or contended interference envelope; and
4. `TPSGate`: when configured and warmed, the post-admit sequence count must fit
   the sustained rate-derived envelope.

Invalid/stale Controller state is availability-scoped. For a valid state, a
protected candidate is request-scoped when the canonical minimum work still
fits and load-scoped when that same minimum work does not fit.

## Prefill QoS and work conservation

`PrefillGate` classifies selection work using fixed, block-aligned boundaries:

| Input estimate | Class |
| --- | --- |
| `<64K` | regular |
| `64K..<256K` | weighted |
| `256K..<512K` | exclusive |
| `>=512K` | quiescent |

The state is contended when local active Decode, raw backend running, raw
backend waiting, or a fresh preemption delta is present. Generation progress
and instantaneous TPS are telemetry; they cannot directly admit or reject.

The work-conserving rules are:

- under contention, only regular requests are eligible and share the contended
  pending-Prefill budget, normally 64K;
- when open, regular and weighted requests share the aggregate budget,
  normally 256K;
- exclusive work requires no pending Prefill sequence and becomes the sole
  long-Prefill owner;
- quiescent work additionally requires no active Decode, raw running, or raw
  waiting; and
- a local pending exclusive or quiescent Prefill blocks new Prefill until its
  first byte or terminal transition.

A large request-specific protection does not mutate capacity. A following small
request is evaluated immediately against the current state. One low TPS value,
one large rejection, or low/no traffic cannot create a cooldown or self-lock.

## Sustained TPS reference

`PREDICTIVE_TPS_REFERENCE=0` or omission disables `TPSGate` and preserves the
Context/KV/Prefill decision contract. A positive reference is output tokens per
second per active Decode sequence. The Controller aggregates qualified
generation, wall seconds, and sequence-seconds into fixed one-second buckets
over the latest 60 seconds.

A positive generation delta qualifies even when a short request completes
between two polls; it uses one sequence when neither endpoint exposes running
work. A zero-generation interval qualifies only when PIG has an `ActiveDecode`
reservation at an endpoint. This counts a genuine tracked Decode stall without
misclassifying pure Prefill or idle `running` as TPS zero. Runtime reset clears
the buckets.

After at least four samples and eight sequence-seconds:

```text
base = max(1, floor(window_aggregate_tps / reference))
explore = base + 1 only when
  window_mean_active_tps >= 1.05 * reference and
  window_aggregate_tps / (base + 1) >= 0.95 * reference
sequence_limit = base or explore

tracked = pending_prefill_sequences + local_active_decode
current = max(raw_running, tracked)
post_admit = current + 1
```

The checked `max` avoids double counting work already visible upstream while
covering reservations not visible in metrics. Disabled state has no TPS
hot-path projection. Warming state permits at most two total sequences, or the
already observed `raw_running` value when it is larger, and therefore cannot
admit an unbounded cold-start burst. Ready idle state always permits one atomic
probe. There is no sticky degraded flag, rejection hold, cooldown, online
reference learning, or background performance probe.

## Atomic decision, reservation, and scope

`AdmissionController` is the only mutable business-state owner. Under one lock
it owns:

- the immutable capability and current coherent observation;
- runtime, sample, observation, and event sequences;
- the epoch-fenced reservation map; and
- a checked O(1) reservation aggregate used by `Admit` and `Snapshot`.

Admission check and reservation creation are one transaction. Reservation IDs
are monotonic within an epoch and are never client request IDs. Counter
overflow, impossible aggregate state, or the derived live-reservation bound
fails closed instead of wrapping, evicting, or guessing. Observation
reconciliation may scan the bounded map; admission and snapshot do not.

## Reservation lifecycle and observation overlay

```text
Reserved
  -> ForwardedPrefill
  -> ActiveDecode on the first positive upstream body read
  -> Terminal exactly once
```

Response headers alone do not end Prefill ownership. PIG wraps the response
body reader without parsing or rewriting content. Successful 2xx EOF can
terminate early; the outer HTTP defer remains as an idempotent safety net.
Non-2xx, transport failure, timeout, cancellation, disconnect, and shutdown
retain their explicit terminal causes.

Reservations contribute only positive work:

```text
Reserved / ForwardedPrefill      full request KV + pending Prefill work
ActiveDecode before coverage     full request KV, no pending Prefill
ActiveDecode after coverage      future Decode KV only
Terminal before forward          remove immediately
Terminal after covered input     remove; observation remains authoritative
Terminal before covered input    retain full KV as residual debt
```

`StartSampleWindow` captures the Controller event watermark before metrics I/O.
A first-byte or terminal event is covered only when its sequence is no later
than that watermark. Residual debt therefore disappears on the first definitely
post-terminal coherent sample, normally within one polling interval, without a
guessed expiration timer or negative reconciliation credit. Duplicate and
old-epoch handle calls are fenced no-ops.

The same publication transaction snapshots local active Decode ownership and
updates/expires the sustained TPS buckets. `AdmissionController.Snapshot(now)`
read-only filtering excludes old buckets, so historical throughput cannot remain
authoritative only because a later interval was idle or pure Prefill.

## Observation failure, reset, and shutdown

Fetch errors and incomplete non-drift samples leave the last coherent state
unchanged. Normal age calculation closes availability if it becomes stale; the
next coherent sample reopens it immediately.

Model identity, maximum context, KV capacity, or block-size drift permanently
closes that Controller because its immutable capability no longer applies. A
generation/preemption counter decrease is a possible backend runtime reset. For
an automatically initialized profile, the Observer first revalidates model
identity and `max_model_len` with one bounded metadata request. Failure does not
publish the sample; changed metadata closes capability availability. Only a
same-capability reset sample atomically advances the epoch, clears old
reservations, residual debt, and the TPS window, resets deltas, and reopens from
that sample.

Shutdown first stops the Observer, then closes Controller intake and terminates
all remaining reservations. Later calls through old handles are no-ops.

## Telemetry and Router compatibility

```text
AdmissionController.Snapshot(now)
  + AdmissionReporter.Snapshot()
  -> one admissionTelemetrySnapshot
       -> PIG decision/capability metrics
       -> backend observation metrics
       -> status log and /v1/upstream-status
       -> pure Router compatibility projection
```

Reporting never reruns policy and cannot mutate Controller state. A reporter
callback panic cannot change admission. Decision telemetry preserves the last
candidate reason and scope, while Router capacity uses the current canonical
minimum-work decision. A request-scoped 429 can therefore remain visible while
the node stays open for smaller work. There is no recent-reject hold.

External `pig_predictive_request_aware_*` metric spellings remain narrow wire
compatibility only; no request-aware Manager, Policy, Adapter, or shadow state
exists in the production execution path.

## Ownership map

| Owner | Responsibility |
| --- | --- |
| `internal/app/request` | Bounded JSON classification, estimate inputs, timing, and exact body restoration |
| `internal/domain/kvadmission` | Fixed model-neutral lexical estimator and `RequestEstimate` construction |
| `internal/domain/predictive` | Minimal immutable estimate/work records and checked block rounding |
| `internal/runtime/predictive/capability_profile.go` | Startup-only capability geometry, fixed Prefill derivation, and validation |
| `internal/admission` | Controller, state projection, pure Gates, atomic reservation, lifecycle, and reconciliation |
| `internal/app/server/admission_vllm_observer.go` | Metrics I/O and observation publication only |
| `internal/app/server/admission_runtime.go` | Thin Controller/Reporter service boundary and shutdown ordering |
| `internal/app/server/admission_http.go` | Panic-safe HTTP-facing decision translation |
| `internal/app/server/admission_reporter.go` | Bounded counters, last records, and suppressed decision logs |
| `internal/app/server/admission_projection.go` | Pure current-capacity and Router compatibility projection |
| `internal/app/server/proxy.go` | Protocol handling, mode mapping, proxying, and terminal transaction |
| `internal/app/server/metrics.go` | Pure formatting from one telemetry snapshot |

No component owns routing, prefix-cache lookup, customer tiers, request
rewriting, backend priority, TTFT protection, or online policy learning.
