# PIG Internal Algorithm Flow

This document describes the current unversioned development architecture. PIG
has one predictive-admission path and exactly one upstream. Ownership boundaries
keep request inspection, backend observation, policy, lifecycle, proxying, and
telemetry from becoming competing controllers.

## Startup

```text
configuration loader
  -> validate one upstream and shadow/enforce mode
  -> derive /metrics from the upstream origin
  -> probe coherent vLLM model identity and exact KV geometry
  -> read one matching positive max_model_len from /v1/models
  -> derive immutable hard KV/input limits and fixed Prefill bands/budgets
  -> construct Manager, RequestAwarePolicy, Observer, Adapter, and Proxy
```

Automatic metadata is fail-closed: `/v1/models` must contain exactly one model
whose ID matches the metric identity. There is no geometry fallback. A complete
explicit capability override is available for controlled tests.

Startup performs no completion, warmup, cache lookup, or performance probe.
The observer polls every 500 ms by default. KV geometry, maximum input, and
Prefill policy are initialized once for the backend epoch and are not learned.

## Pre-forward transaction

```text
protected HTTP path
  -> bounded classifier acquires a scan token
  -> read body and preserve exact bytes plus Content-Length
  -> malformed JSON becomes protocol HTTP 400
  -> estimate lexical input, safety input, Decode horizon, and block-rounded KV
  -> Manager captures one coherent observation plus every live reservation
  -> ResourceSafetyGate evaluates freshness, identity, input ceiling, and KV
  -> PrefillQoSGate evaluates current request class and pending Prefill work
  -> if protected, evaluate the canonical minimum request under the same lock
  -> assign request/load/availability scope atomically
  -> in enforce, reserve before forwarding or return HTTP 429
  -> forward the original request bytes unchanged
```

The canonical probe uses selection input `1`, safety input `1`, a 256-token
Decode horizon, and production block rounding. It creates no reservation.

Shadow performs the same request inspection and pure evaluation, records the
counterfactual result, and forwards. It creates no reservation and publishes no
predictive Router capacity reduction.

## Resource safety

`ResourceSafetyGate` owns only strict resource and validity conditions:

- a fresh observation and valid backend identity;
- valid non-negative bounded geometry;
- request safety input not above `maximum_admissible_input`;
- effective KV equal to the coherent observed base plus the positive live
  reservation overlay; and
- post-admit KV not above the immutable hard limit.

An invalid, stale, input-limit, overflow, or KV result takes precedence over
Prefill QoS. TPS, generation deltas, cache state, TTFT, and customer identity do
not enter this Gate.

## Prefill QoS and work conservation

`PrefillQoSGate` classifies estimated Prefill work using fixed, block-aligned
boundaries:

| Input estimate | Class |
| --- | --- |
| `<64K` | regular |
| `64K..<256K` | weighted |
| `256K..<512K` | exclusive |
| `>=512K` | quiescent |

The current state is contended only when at least one present-time signal is
true: local active Decode, raw backend running, raw backend waiting, or a fresh
preemption delta. Generation progress and instantaneous TPS are telemetry; they
cannot select the regime.

The work-conserving rules are:

- under contention, only regular requests are eligible and fitting regular
  pending Prefill work shares the contended budget, normally 64K;
- when open, regular and weighted requests share the aggregate budget,
  normally 256K;
- exclusive requires no pending Prefill sequence;
- quiescent requires no pending Prefill and no current Decode/running/waiting;
- a pending exclusive or quiescent local Prefill owns the long-work lane and
  blocks new Prefill until first byte or terminal; and
- unknown pending work conservatively prevents non-regular admission.

Consequently, a 49K regular request can fit under Decode contention, while a
96K weighted request can be rejected as request-specific and a following 1K
regular request can enter immediately. Sixteen 4K regular requests can fill a
64K contended budget; the next request becomes load-scoped only when the
canonical minimum request also cannot fit.

A fresh preemption selects contention for its single coherent observation. It
does not create cooldown state. One low TPS sample does not reject anything.

## Atomic scope and Router capacity

When a candidate is protected, Manager evaluates the canonical probe using the
same lock, observation sequence, event sequence, and reservation snapshot:

```text
canonical admits       -> request scope
canonical protects     -> load scope
stale/unavailable/invalid canonical state -> availability scope
```

The Adapter consumes that scope; it does not infer scope from reason strings.
Current Router capacity is a separate non-reserving canonical evaluation, not
the last business-request decision. Reject timestamps are telemetry only. A
new observation or lifecycle event therefore restores capacity as soon as the
current canonical probe fits, without a hold timer or another business request.

## Reservation lifecycle and observation overlay

```text
reserved
  -> MarkForwarded
  -> MarkPrefillComplete on the first positive upstream body read
  -> Terminate exactly once
```

Response headers alone do not end Prefill ownership. PIG wraps the response
body reader and performs the first-byte transition once; it does not parse or
rewrite response content.

Terminal causes cover completion, upstream failure, timeout, client
cancellation/disconnect, local protection, expiration, and shutdown. Bounded
tombstones prevent duplicate late terminal events from releasing newer state.

Manager owns one coherent observed base plus a positive-only overlay of live
reservations. Sample start/finish watermarks decide when a Prefill-complete
reservation is covered by a later observation. A reservation created after a
scrape starts cannot be accidentally absorbed by that scrape. Terminal events
remove only their local reservation; they never subtract from the observed
base or create completed-Decode credits. A later coherent sample replaces the
base.

## Observation failure and epoch changes

Fetch errors and incomplete metric sets leave the last coherent state intact.
Freshness eventually closes enforce intake; the next coherent sample can
recover it immediately.

Model identity, KV capacity, or block-size drift, and monotonic generation or
preemption counter reset invalidate the immutable epoch. Recovery requires
adapter reconstruction rather than authorizing a new backend with an old
profile or old reservations.

## Telemetry projection

One adapter telemetry capture drives:

```text
predictive admission metrics
  + coherent backend observation metrics
  + current canonical Router projection
  + six Router compatibility fields
```

Decision telemetry preserves the enforced candidate reason/scope. Router
telemetry separately describes current canonical capacity. This distinction is
intentional: a large request may receive a visible request-scoped 429 while the
node remains open for smaller work. Load or availability protection must
publish non-open current capacity. There is no recent-verdict overlay.

## Ownership map

| Owner | Responsibility |
| --- | --- |
| `internal/app/request` | Bounded read-only JSON inspection, estimate inputs, and exact body restoration |
| `internal/app/server/predictive_capability_initializer.go` | One bounded metadata read and automatic/explicit initialization selection |
| `internal/runtime/predictive/capability_profile.go` | Pure KV geometry, maximum input, block alignment, fixed Prefill derivation, and profile validation |
| `internal/runtime/predictive/resource_safety_gate.go` | Pure observation/identity/input/KV safety |
| `internal/runtime/predictive/prefill_qos_gate.go` | Pure request classification, contention selection, and Prefill ownership/budgets |
| `internal/runtime/predictive/request_aware_policy.go` | Two-Gate composition and precedence |
| `internal/runtime/predictive/request_aware_manager.go` | Atomic candidate/canonical decision and scope |
| `internal/runtime/predictive/manager.go` | Reservation lifecycle, sample barriers, and positive overlay |
| `internal/app/server/predictive_vllm_observer.go` | Coherent vLLM observation, freshness, one-sample preemption signal, and epoch detection |
| `internal/app/server/request_aware_predictive_adapter.go` | HTTP-facing translation, current Router inspection, and telemetry capture |
| `internal/app/server/proxy.go` | Protocol handling and forward/terminal transaction |
| `internal/app/server/metrics.go` | Single-snapshot observability projection |

No component owns routing, prefix-cache lookup, customer tiers, request
rewriting, backend priority, TTFT protection, or online policy learning.
