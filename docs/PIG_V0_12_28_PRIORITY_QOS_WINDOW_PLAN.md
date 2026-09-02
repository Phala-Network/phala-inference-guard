# PIG v0.12.28 Priority QoS Window Plan

Status: active design and implementation plan. No CVM deployment is in scope.

## 1. Correction and release boundary

PIG v0.12.27 is withdrawn. Its published GHCR tags remain immutable and are
not deleted or overwritten, but they must not be used for a new deployment:

```text
ghcr.io/phala-network/phala-inference-guard:v0.12.27
ghcr.io/phala-network/phala-inference-guard:v0.12.27-ad1b8f8ad173
digest: sha256:5f4c197c9ad8eb3ac9d61abef3657664e63ef579efc3e569c5655439780423e0
```

The withdrawn behavior was an authenticated `X-User-Tier: premium` fast path
that bypassed every PIG admission gate. That is incompatible with the current
QoS objective. Version 0.12.28 is a corrective source release; it is not
deployed or published until its tests and clean-builder evidence pass.

## 2. Objective

Keep one admission contract for every public inference request while giving a
trusted high-priority request the first opportunity inside a short admission
batch:

1. premium and basic requests both pass authentication, classification, the TPS
   controller, and reservation lifecycle checks;
2. `X-User-Tier: premium` only changes ordering among requests already waiting
   in the same local admission batch. Premium still obeys the TPS reference,
   but may proceed when non-TPS waiting, running-limit, or window-concurrency
   protections would reject basic traffic;
3. the batch is deliberately much shorter than the 500 ms backend observation
   interval (target coalescing delay 2 ms), so a single request is not held for
   a full polling interval;
4. each queued request has a hard 50 ms local wait bound; queue-full, timeout,
   cancellation, shutdown, or backend/controller unavailability returns an
   OpenAI-shaped 429/availability response without a backend call;
5. basic traffic cannot be starved indefinitely: an aged basic request is
   selected before newer premium requests, and every queue entry is bounded;
6. only the underlying admission controller owns reservations and backend
   waiting. A local priority queue does not increment backend waiting metrics or
   alter Router compatibility metrics.

The goal is QoS-preserving throughput, not strict global priority. Requests
that arrive after a batch has already dispatched cannot preempt a request that
has already received a reservation.

## 3. Design

### 3.0 State boundaries

These four values deliberately describe different layers:

- `running` is the number of requests the backend scheduler is executing now;
- `waiting` is the number already accepted by the backend but waiting in its
  scheduler, not the number waiting in PIG;
- `window` is PIG's short-term admission budget between observations, charged
  by `DecodeSequences`; it is neither the backend's total running limit nor a
  backend waiting count;
- a `reservation` is PIG's atomic pre-forward promise that the request's
  projected demand fits. It must be terminated or reconciled on every terminal,
  cancellation, disconnect, timeout, reset, and shutdown path.

The priority queue operates only before a reservation is obtained. It never
relabels local queue depth as backend `waiting`, and it cannot revoke a
reservation that has already been forwarded.

### 3.1 Priority classification

The existing exact, single-value `X-User-Tier: premium` classifier remains the
only source of the high-priority bit. Missing, duplicate, unknown, or malformed
values are basic. The bit is carried in `TPSRequestDemand` only for local
ordering and policy selection. It does not affect the measured TPS reference:
both tiers still fail closed when TPS evidence is invalid or below the required
reference. For premium only, backend waiting/preemption signals and the
running/window load bounds are non-blocking; reservation accounting remains
enabled.

### 3.2 Bounded priority admission service

In enforce mode the HTTP server wraps the existing admission service with a
single worker and a bounded queue (maximum 64 entries). The worker waits at
most 2 ms from the oldest queued entry to coalesce a burst, then selects:

1. an entry that has reached the 50 ms deadline, in oldest-first order (it is
   rejected as a timeout, rather than waiting longer);
2. otherwise an aged basic entry (at least 25 ms old), oldest-first;
3. otherwise the highest priority, oldest-first.

The selected request makes exactly one call to the underlying admission
service. A hard TPS/controller-availability decision is never retried in a
loop. This keeps the hot path O(queue depth), prevents a hidden long wait, and
leaves TPS accounting and reservation lifecycle in the existing atomic
controller. Basic-only window/running protections remain authoritative for
basic traffic; premium's non-TPS bypass is a deliberate policy choice, not an
unbounded direct-forward path.

The queue has explicit lifecycle handling for context cancellation, queue full,
worker shutdown, delegate panic/invalid result, reservation cleanup when a
client cancels during dispatch, and idempotent close. A reservation returned by
the delegate is terminated before a canceled result is delivered; no dropped
queue result may leak a reservation or residual debt.

Shadow mode remains pass-through so test-only shadow configuration does not add
latency or change traffic behavior.

### 3.3 Metrics and logs

Underlying admission attempts and reservations retain their existing meaning.
Queue rejects use structured reasons (`priority_queue_full`,
`priority_queue_timeout`, or `priority_queue_canceled`) and are exposed through
the normal PIG reject counters. Queue depth and lifecycle counters are local
diagnostics only; they are not rendered as backend `waiting`.

## 4. Test-first acceptance gates

The focused red/green tests must prove:

- premium no longer bypasses admission, including under TPS protection and
  full running/window bounds;
- premium and basic arriving in one batch are dispatched to the delegate in
  premium-first order, while already-dispatched basic work is never revoked;
- an oversized premium demand cannot bypass a hard controller rejection;
- queue full and 50 ms timeout return 429 with backend call count zero;
- canceled and shutdown entries leave queue depth zero and do not leak a
  reservation, residual debt, or scanner body reservation;
- a continuously arriving premium stream cannot starve an aged basic entry;
- delegate rejection remains authoritative and is not retried or converted to
  a bypass;
- malformed/duplicate/unknown tier headers stay basic;
- local management routes remain local and are unaffected by tier;
- existing chat/completions/Responses, streaming, structured output, tool-call,
  exact-route/auth, vLLM/SGLang observation, five-line Router metrics, and
  reservation lifecycle contracts remain green;
- race and deterministic tests cover concurrent enqueue, dispatch, cancel,
  close, and reservation termination.

## 5. Three review passes

1. Model/causality: verify priority is consumed by the pre-forward queue and
   the explicit premium policy, while the TPS/reference and availability gates
   remain authoritative and no 500 ms fixed wait is introduced.
2. Safety/lifecycle: inspect queue bounds, lock ownership, cancellation,
   delegate panic, close, timeout, reservation release, and starvation bound.
3. Evidence/release: run the clean approved remote-builder matrix, record exact
   source and evidence hashes, push the source branch, and publish no image until
   all gates pass.

## 6. Execution status

- [x] Withdraw v0.12.27 semantics in documentation; keep its immutable image
      tags untouched.
- [ ] Add priority demand type and bounded enforce-mode queue.
- [ ] Add focused regression, lifecycle, race, and metrics tests.
- [ ] Run focused/full/race/vet/build/simulation gates on the approved remote
      builder; do not run Go tests locally.
- [ ] Perform the three review passes and record evidence.
- [ ] Assign/push v0.12.28 source only after the source gates pass.
- [ ] Build/publish v0.12.28 only after acceptance; deployment remains a
      separate user-authorized step.
