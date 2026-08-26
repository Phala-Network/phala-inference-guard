# PIG v0.12.23 TPS Health Gate Plan

Status: active design and execution plan. Version `0.12.23` is reserved for the
new behavior but must not be assigned in source, tagged, imaged, or deployed
until the source and clean-builder gates pass.

Formal starting branch: `codex/pig-v0.12.22-tps-only` at exact baseline commit
`ffa65f2375a83c4c9a69a601d7902c273fedc487`.

This plan supersedes the capacity-selection behavior in
`PIG_V0_12_22_TPS_ONLY_CONTROLLER_PLAN.md`. Public route, authentication, local
management, proxy, streaming, response, and attestation contracts remain
unchanged.

## 1. Objective

Maximize accepted, successful completion goodput while keeping the sufficiently
long mean output TPS per active Decode sequence near or above
`PREDICTIVE_TPS_REFERENCE`.

PIG is a performance health gate, not a concurrency allocator:

- it has no configured, learned, inferred, warming, or exploration sequence
  limit;
- it does not divide aggregate TPS by the reference to derive capacity;
- it does not reject because current, post-admit, unobserved, reserved, or batch
  sequence counts exceed a limit;
- it accepts arbitrary healthy concurrency that the backend continues to serve
  without material performance degradation;
- it protects only subsequent requests after qualified TPS degradation,
  waiting, or preemption is observed;
- it reopens from current evidence without retaining a learned low cap.

The intended production override remains:

```text
PREDICTIVE_TPS_REFERENCE=25
```

Default backend polling remains 500 ms. No new public tuning parameter is added.

## 2. Explicit Tradeoff And Blind-Window Pacing

Without a concurrency or resource forecast, forwarding every arrival against
one unchanged observation for as long as 500 ms could create an arbitrarily
large blind burst. PIG therefore separates sustainable concurrency from
observation-lag pacing:

- there is no final or learned concurrency cap;
- each observation epoch has a dynamic blind-flight budget for new Decode
  sequences not yet reflected by backend metrics;
- exhausting that budget does not produce a protection decision or `429`; the
  request waits for the next observation and is then evaluated again;
- a fresh healthy observation replenishes the budget, so sustained healthy
  concurrency continues to grow instead of stopping at a capacity estimate;
- only degradation, waiting, preemption, or an availability failure produces a
  final rejection.

The initial deterministic budget is:

```text
observed_tps_support = floor(
  max(rolling_aggregate_tps, latest_qualified_aggregate_tps) /
  tps_reference
)

blind_flight_budget = max(1, observed_running, observed_tps_support)
```

Only valid nonnegative TPS evidence participates. During warming or without a
qualified interval, the TPS-derived term is zero. Complete request fanout
charges the budget atomically. Existing unobserved sequences consume the same
epoch budget until observation reconciliation proves them visible. The budget
does not limit total running sequences: after every fresh healthy 500 ms sample,
another dynamic cohort can enter.

This design deliberately turns blind burst size into short local backpressure,
not false capacity `429`s. It can add up to an observation interval of admission
delay to excess burst members. It does not add a user-configurable queue size,
concurrency limit, or model threshold. Waiting requests hold no reservation and
are woken by observation publication; context cancellation and observation
staleness still terminate safely.

Waiting and preemption are immediate current-observation pressure evidence and
pause new intake. They are not converted into a learned cap, cooldown, or
consecutive-clear requirement. Existing streams are never terminated by this
gate.

## 3. Decision Contract

The gate evaluates fresh normalized backend observations and the immutable TPS
window snapshot in this order:

1. TPS reference disabled: admit with `disabled`.
2. Invalid, missing, stale, drifted, or corrupt controller state: retain the
   existing availability protection.
3. Backend waiting is nonzero: protect with `waiting`.
4. The latest preemption delta is nonzero: protect with `preemption`.
5. The rolling window is not ready: health is open with `warming`.
6. No reliable Decode denominator exists for the current interval: health is
   open with
   `no_current_evidence`; this includes idle and pure Prefill intervals.
7. Rolling mean active TPS is at or above the reference: admit with
   `healthy_window`, even if one current interval is below the reference.
8. Rolling mean is below the reference but the latest qualified interval mean
   is at or above the reference: admit with `recovered_current` so recovery does
   not wait for the 60-second window to flush.
9. Rolling and latest qualified interval means are both below the reference:
   protect with `below_reference` until either becomes healthy.
10. When health is open, atomically reserve against the current observation's
    remaining blind-flight budget. If the complete demand does not fit, return a
    nonterminal `defer_until_observation` result with no reservation; the HTTP
    runtime waits and retries after the observation sequence advances.

This OR-based recovery preserves the long-average objective: one short bad poll
does not close a healthy long window, while a fresh healthy poll reopens a
degraded long window immediately. It adds no hysteresis knob, sequence cap,
model threshold, or learner.

The TPS window owns current-interval qualification and denominator selection.
The gate must not independently divide by raw ending concurrency because that
can report false health while requests start or finish between polls. A tracked
Decode interval with zero generated tokens is qualified zero TPS; a pure
Prefill interval without known Decode exposure is not qualified TPS evidence.

## 4. Architecture And SOLID Boundaries

```text
request shape and Decode fanout validation
  -> fresh normalized backend observation
  -> TPS window updates rolling and latest qualified interval evidence
  -> pure TPS health decision
  -> observation-epoch blind-flight budget or wait notification
  -> reserve lifecycle exposure only when admitted
  -> forward and reconcile terminal lifecycle
```

- Request parsing validates protocol and reports Decode multiplicity; it never
  selects performance capacity.
- Backend adapters normalize counters and identity; they never choose policy.
- The TPS window exclusively owns time weighting, denominator selection,
  qualification, rolling mean, and latest qualified interval evidence.
- The TPS gate is a pure health predicate over an immutable snapshot.
- A separate observation pacing policy derives one epoch's blind-flight budget;
  it cannot change TPS health or return a final load rejection.
- The reservation ledger owns sequence exposure and terminal reconciliation;
  unobserved sequence totals only consume blind-flight pacing and never define
  sustainable concurrency.
- HTTP code maps decisions and emits bounded evidence; it never recomputes TPS.

Remove the obsolete QoS-budget layer and all fields used only to select or spend
a sequence limit: `qosBudgeted`, QoS-budget leases/subreasons, sequence limit,
post-admit sequence count, and current-sequence capacity comparison. Keep
unobserved sequences, sequence liabilities, live reservations, and residual
debts where required for exposure accounting, lifecycle correctness, and the
observation-epoch blind-flight budget.

Observation publication owns a race-safe generation notification. A deferred
request receives the immutable observation sequence and notification handle
from the same locked decision. It creates no reservation before waking. On
wake, every request re-enters the complete atomic decision; concurrent wakeups
cannot overspend the new budget. Controller close/reset wakes all waiters so no
goroutine remains stranded.

The hot decision path remains O(1), allocation-free after bounded request-shape
parsing, and independent of the number of live reservations.

## 5. Observability Contract

Logs, status, and metrics must expose the health decision without pretending a
capacity estimate exists:

- configured TPS reference;
- rolling readiness, qualified samples/sequence-seconds, aggregate TPS, and
  mean active TPS;
- latest interval qualified flag, aggregate TPS, mean active TPS, and
  denominator evidence;
- running, waiting, preemption delta, unobserved sequences, liabilities, and
  live reservations as observations only;
- decision result and one bounded subreason;
- blind-flight budget, unobserved consumption, deferred request count, wait
  duration, wake cause, and retry outcome;
- enforced reject counters attributable to `waiting`, `preemption`, or
  `below_reference`.

Delete sequence-limit, post-admit, and QoS-budget metrics/log fields. A deferred
decision is not counted as an enforced rejection or Router-visible protection.
Do not log
request bodies, tokens, credentials, prefixes, user identifiers, or unbounded
labels.

## 6. Test-First Execution

### Phase 1: freeze the v0.12.22 failure

Add focused tests matching both dev risks:

- a ready, healthy TPS window with zero waiting/preemption must not issue a
  final rejection merely because prior requests remain unobserved;
- a burst larger than the same-observation blind-flight budget must defer excess
  requests rather than forwarding the entire burst or returning a capacity
  `429`.

The first test must fail on v0.12.22 because its derived sequence limit is
exhausted. The second must fail because v0.12.22 has no nonterminal observation
backpressure result. Neither failure may come from invalid fixture state.

### Phase 2: implement the health gate

1. Extend the TPS window snapshot with its latest qualified interval result.
2. Replace capacity selection with the decision contract in section 3.
3. Add atomic observation-epoch pacing and cancellation-safe wake/retry wiring.
4. Remove QoS-budget policy and reservation ownership.
5. Remove obsolete decision fields and reconcile logs, metrics, status, tests,
   simulation, README, and advanced documentation.
6. Preserve controller freshness, identity, atomic lifecycle, and runtime-reset
   safety.

Focused coverage must prove:

- warming and idle do not reject high concurrency without pressure evidence;
- healthy rolling TPS admits regardless of raw, unobserved, reserved, or batch
  sequence counts once observation-paced waiters receive healthy refreshes;
- a same-epoch burst spends one atomic blind-flight budget, excess demand waits
  without a reservation or 429, and wakeup cannot double-spend;
- canceled, stale, reset, and closed deferred requests cannot leak or hang;
- one low current interval does not close a healthy rolling window;
- sustained low rolling plus low current TPS protects;
- current recovery reopens immediately while the long window is still low;
- waiting/preemption protect for their current observation and the first fresh
  clear healthy observation reopens;
- pure Prefill is not mislabeled zero-TPS degradation;
- tracked Decode with zero tokens is qualified degradation;
- stale metrics, counter rollback, runtime reset, cancellation, failure, and
  terminal races remain correct;
- vLLM and SGLang adapters produce the same normalized TPS contract.

### Phase 3: three reviews and clean-builder verification

Record three reviews and correct every issue before release assignment:

1. Model and causality: prove only qualified TPS/waiting/preemption changes a
   pre-forward decision and sequence counts do not.
2. Safety and lifecycle: prove no stale evidence, exposure leak, double release,
   reset bypass, or race.
3. Evidence and release: prove focused red/green validity, documentation and
   observability consistency, and exact clean-builder provenance.

Run on the approved remote builder, not local Windows:

```text
scripts/verify-no-legacy-mode.sh
gofmt check
go test ./...
go test -race ./...
go vet ./...
go build ./...
focused hot-path benchmarks
deterministic TPS health traces
```

### Phase 4: release and isolated dev test

Only after Phase 3 succeeds:

1. assign `0.12.23`, commit, push, and create an immutable tag;
2. build on the remote builder and verify OCI version/revision and production
   image contract;
3. push immutable and release tags, then verify the registry digest;
4. over SSH, update only `phala-inference-guard-b` on dev CVM
   `19a2d062-af63-49eb-807d-84ddfbbc905a`; never use `phala deploy` or restart
   the CVM, SGLang, PIG-A, or HAProxy;
5. retain runtime secrets without printing them;
6. verify health/auth/routes/chat/streaming with minimal requests, then exercise
   representative sequential and concurrent traffic without turning one
   numeric result into a universal hard gate;
7. inspect TPS, waiting, preemption, reservation cleanup, errors, and restarts.

Source must be pushed for each accepted source update. A registry image is
published only after the complete builder verification succeeds.

## 7. Current State

- `v0.12.22` source, tag, image, and dev PIG-B runtime are reproducible baseline
  evidence; they are not production candidates;
- no v0.12.23 behavior code, version assignment, image, or deployment exists;
- the immediate next step is the focused red test pair from Phase 1.
