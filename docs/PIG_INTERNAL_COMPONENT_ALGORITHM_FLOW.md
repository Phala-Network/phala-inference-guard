# PIG v0.12.2 Internal Algorithm Flow

PIG v0.12.2 has one admission architecture and one upstream. Components are
separated by ownership so request parsing, backend observation, policy,
reservation lifecycle, proxying, and telemetry do not mutate each other's
state.

## Startup

```text
configuration loader
  -> validate one upstream and shadow/enforce mode
  -> derive /metrics from upstream origin
  -> probe coherent vLLM identity and exact KV capability
  -> run bounded cold-Prefill capability initialization
  -> freeze KV limits and Prefill classes
  -> construct Manager, request-aware Policy, Observer, Adapter, and Proxy
```

The observer polls at 500 ms by default. Capability and Prefill policy are
initialized once, not learned.

## Pre-forward decision

```text
HTTP admitted path
  -> bounded classifier acquires a scan token
  -> read body and restore exact bytes plus Content-Length
  -> reject malformed JSON as protocol HTTP 400
  -> estimate lexical input, output horizon, KV upper bound, and Prefill work
  -> capture fresh observer input
  -> Manager combines physical observation with every live reservation
  -> Policy evaluates post-admit hard KV, Prefill interference, and soft TPS
  -> Manager atomically decides and reserves in enforce
  -> proxy forwards unchanged request, or returns predictive HTTP 429
```

Shadow runs classification and policy but does not call the reserving Manager
path. It records a would-protect decision and forwards the original request.

## Request-size differentiation

The policy uses request cost, not a global request count. Under the same current
pressure:

- a regular request can fit;
- a weighted request consumes more aggregate Prefill budget;
- an exclusive request requires no competing long Prefill;
- a quiescent request requires a sufficiently idle backend;
- any request exceeding hard post-admit KV is protected.

TPS is a soft QoS constraint. A projected reduction can narrow the allowance or
protect a large marginal request, but the design does not turn every temporary
TPS dip into a global stop.

## Lifecycle

```text
reserved
  -> MarkForwarded
  -> MarkPrefillComplete on the first upstream response body byte
  -> Terminate exactly once
```

Response headers alone do not end Prefill ownership. PIG wraps the response
body reader and performs the transition exactly once after a positive read;
it does not parse or rewrite response content.

Terminal causes cover completion, upstream failure, timeout, client cancellation
or disconnect, local protection, expiration, and shutdown. The Manager retains
bounded tombstones so duplicate late events cannot release new capacity.

Observer reconciliation uses a sample window sequence. Reservations created
after a metrics scrape begins are not accidentally absorbed by that scrape.
Explicit backend epoch drift invalidates intake while preserving ownership of
old reservations until their terminal event.

## Observation failure

Fetch errors and incomplete metric sets are transient. They do not mutate the
last coherent state; freshness eventually closes enforce intake, and a coherent
sample can recover it.

Explicit model identity, KV capacity, or block-size drift and monotonic counter
reset invalidate the immutable epoch. Recovery requires reconstruction rather
than silently authorizing a new backend with the old profile.

## Telemetry projection

One call captures adapter telemetry for each local metrics response. That same
snapshot drives:

```text
predictive admission metrics
  + single-backend observation metrics
  + authoritative Router backpressure
  + temporary Router compatibility fields
```

This prevents a poll or request completion between writers from producing an
internally contradictory scrape.

Current-state inspection remains the primary Router projection. When an
enforced load rejection is request-specific and a one-block inspect request
still fits, the projection retains selective inspect capacity one for a bounded
1500 ms from the original rejection. Stronger current-state capacity zero wins.
The bounded projection changes no admission input, reservation, or observer
state and clears without another business request.

## Ownership map

| Owner | Responsibility |
| --- | --- |
| `internal/app/request` | Bounded read-only request inspection and exact body restoration |
| `internal/runtime/predictive/request_aware_policy.go` | Pure request-aware decision policy |
| `internal/runtime/predictive/manager.go` | Atomic reservation and reconciliation state |
| `internal/app/server/predictive_vllm_observer.go` | Coherent vLLM observation, freshness, cooldown, epoch detection |
| `internal/app/server/request_aware_predictive_adapter.go` | HTTP-facing decision translation and telemetry |
| `internal/app/server/proxy.go` | Protocol handling, forward/terminal transaction |
| `internal/app/server/metrics.go` | Single-snapshot observability projection |

No component owns routing, cache lookup, customer tiers, request rewriting,
backend priority, TTFT protection, or online policy learning.
