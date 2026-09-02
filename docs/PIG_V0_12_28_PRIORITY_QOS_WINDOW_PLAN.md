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
- [x] Add priority demand type and bounded enforce-mode queue.
- [x] Add focused regression, lifecycle, race, and metrics tests.
- [x] Run focused/full/race/vet/build/simulation gates on the approved remote
      builder; do not run Go tests locally.
- [x] Perform the three review passes and record evidence.
- [x] Assign/push v0.12.28 source only after the source gates pass.
- [ ] Build/publish v0.12.28 only after acceptance; deployment remains a
      separate user-authorized step.

## 7. Source acceptance evidence (2026-09-02)

The accepted source HEAD is:

```text
branch: codex/pig-v0.12.28-priority-tps
commit: 9f2e4e6 (full: 9f2e4e65bc02f5e514226a8600a452adc87fff8c)
pig-origin: https://github.com/Phala-Network/phala-inference-guard.git
```

The branch is pushed to `pig-origin` and the auxiliary `origin` mirror. The
clean source archive used by the builder is:

```text
archive: pig-v01228-priority-tps-9f2e4e6.tar.gz
sha256: 5bc01d694b7124e03bf630ebcc9bcc2ee52108c0ea638ef4d6902f19acd83eb4
```

Builder evidence is from app `ff40ee31b95e89ebb242c223514adc715ac8a301` using
the immutable Go image:

```text
golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac
go version: go1.24.13 linux/amd64
evidence directory: /var/volatile/dstack/persistent/.cache/pig-v01228-priority/green-9f2e4e6-r1/src
```

The following commands all returned exit status 0 in one clean container;
Go tests were not run on the Windows host:

```text
gofmt -l .                         (empty)
go test ./internal/admission ./internal/app/server ./internal/simulation/tpscontrol -count=1
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
```

An earlier focused run exposed two defects before acceptance: the queue wrapper
hid the runtime `UpdatePolicy` interface, and the premium TPS regression fixture
was still in the TPS warm-up state. The first was fixed by delegating
`UpdatePolicy`; the second was corrected by supplying enough sequence-seconds
evidence to exercise the intended below-reference branch. The final archive was
then rebuilt and all gates were rerun.

## 8. Three review passes

### 8.1 Model and causality

- `X-User-Tier: premium` is classified only after route, authentication, and
  body classification; it is carried into the real pre-forward `Decide` call.
- Premium bypasses only waiting/preemption/running/window load protections;
  the controller's TPS/reference, observation validity, runtime identity, and
  reservation checks remain authoritative.
- The local queue coalesces for 2 ms and has a 50 ms hard bound; it never waits
  for a 500 ms observation interval, invents backend waiting, or retries a TPS
  rejection. Basic requests are aged after 25 ms so premium cannot starve them.
- Warming remains an explicit controller state: with insufficient qualified TPS
  evidence the controller admits to bootstrap observation for both priorities;
  once evidence is ready, a below-reference TPS window rejects premium too.

### 8.2 Safety and lifecycle

- Queue depth is bounded at 64. Full, timeout, cancellation, close, panic, and
  invalid-result paths return protection without a backend call.
- Controller reservations remain owned by the underlying admission runtime.
  Every forwarded result retains the existing success/error/cancel/timeout/
  disconnect cleanup; a cancellation race terminates any reservation before a
  canceled result is returned.
- The queue delegates `UpdatePolicy` and records local queue protections through
  an optional decision-recorder interface, so policy API, logs, and evidence do
  not disappear behind the wrapper. The queue does not rewrite backend `waiting`
  or Router compatibility metrics.
- `go test -race ./...` passed, covering concurrent enqueue/dispatch/cancel/
  close and reservation cleanup fixtures. The controller's finite
  `maximumReservations` remains an overflow failsafe, not a premium capacity
  entitlement.

### 8.3 Evidence and release boundary

- Source, archive, builder image, tests, and Git push are recorded separately.
  No v0.12.28 image has been built or published, and no CVM has been restarted,
  deployed, or sent synthetic inference traffic.
- v0.12.27 remains withdrawn and immutable; no historical tag was overwritten.
- The next release step, if authorized, is a pinned builder image build with
  SBOM/provenance and registry digest verification. Until that separate step is
  authorized and passes, v0.12.28 is a validated source candidate only.

## 9. Decision

The source implementation satisfies the current target at the source and clean
builder-test layers: high-priority requests are preferentially ordered and may
avoid non-TPS load gates, but they cannot bypass the TPS/reference gate or
reservation lifecycle. Keep the candidate at v0.12.28 and do not publish or
deploy an image in this task. Any future policy change must update this plan and
rerun the focused/full/race gates before publication.
