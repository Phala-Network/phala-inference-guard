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

PIG combines a performance health gate with two simple admission bounds:

- it never derives, learns, warms, or explores a sequence limit from TPS;
- it does not divide aggregate TPS by the reference to derive capacity;
- it accepts arbitrary healthy concurrency that the backend continues to serve
  without material performance degradation, up to the backend's configured
  scheduler running limit when that limit is known;
- it protects only subsequent requests after qualified TPS degradation,
  waiting, preemption, running-limit exhaustion, or same-observation window
  concurrency exhaustion is observed;
- it reopens from current evidence without retaining a learned low cap.

The intended production override remains:

```text
PREDICTIVE_TPS_REFERENCE=25
```

Default backend polling remains 500 ms. The default window concurrency is 32
Decode sequences. Production normally needs no explicit setting for that
default, but `PREDICTIVE_WINDOW_CONCURRENCY` provides an initialization override.
`PREDICTIVE_RUNNING_LIMIT` provides an optional initialization override when the
backend cannot expose its scheduler limit. Both values are available through the
same revisioned admin policy API as the TPS reference.

## 2. Running Limit And Window Concurrency

Without a concurrency or resource forecast, forwarding every arrival against
one unchanged observation for as long as 500 ms could create an arbitrarily
large blind burst. PIG therefore separates three controls:

- the TPS health gate detects actual degradation and never selects capacity;
- the running-limit gate prevents projected backend running demand from
  exceeding the upstream scheduler's configured maximum when that maximum is
  known;
- the window-concurrency gate limits new Decode sequences that have been
  admitted but are not yet reflected by backend metrics.

The atomic pre-forward bounds are:

```text
projected_running = observed_running + observed_waiting +
                    unobserved_sequences + request_decode_sequences

running gate fits = running_limit disabled OR
                    projected_running <= running_limit

window gate fits  = unobserved_sequences + request_decode_sequences <=
                    window_concurrency
```

Complete request fanout charges both gates atomically. Existing unobserved
sequences consume window concurrency until observation reconciliation proves
them visible. The window value does not limit total running sequences: after a
fresh backend observation absorbs admitted work, another cohort can enter. A
healthy backend can therefore sustain concurrency far above 32; it cannot
receive more than 32 still-unobserved sequences from PIG at once by default.

Exhausting either gate returns the existing OpenAI-shaped `429` immediately; PIG
does not queue or wait. The bounded subreasons are `running_limit` and
`window_concurrency`, so Router and operators can distinguish them from
`below_reference`, `waiting`, and `preemption`.

### 2.1 Upstream running-limit discovery

The production adapters do not pretend that a current-running gauge is a
maximum:

- vLLM standard metrics expose `vllm:num_requests_running` but not
  `max_num_seqs`. The production OpenAI API does not expose the scheduler
  maximum. Current vLLM `/server_info` is a development endpoint with an
  explicit production security warning, so PIG must not depend on it. vLLM uses
  `PREDICTIVE_RUNNING_LIMIT` or the admin API when an operator wants this gate;
  zero means unknown/disabled;
- SGLang standard metrics expose `sglang:num_running_reqs` but not the configured
  `max_running_requests`. Latest upstream has a different
  `sglang:max_running_requests_under_SLO` gauge whose own source notes that it
  has no setter; it is not scheduler capacity and must not be consumed;
- SGLang's internal `/server_info` returns resolved startup `server_args`.
  On dev SGLang v0.5.18 on 2026-08-26, both top-level and scheduler internal
  state reported exact integer `max_running_requests=256`. PIG may read the
  top-level field during its existing trusted startup capability probe. Missing,
  noninteger, duplicate-inconsistent, out-of-range, wrong-content-type, timeout,
  or unsupported responses leave the running limit unknown instead of guessing.

An explicit environment or admin value overrides automatic SGLang discovery.
The effective source (`sglang_server_info`, `environment`, `admin`, or
`unknown`) is observable. This is initialization/administration, not learning.

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
10. When TPS health is open, evaluate the running and window gates atomically.
    Protect with `running_limit` or `window_concurrency` when the complete demand
    does not fit; otherwise reserve and forward.

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
  -> running-limit and window-concurrency projection
  -> reserve lifecycle exposure only when admitted
  -> forward and reconcile terminal lifecycle
```

- Request parsing validates protocol and reports Decode multiplicity; it never
  selects performance capacity.
- Backend adapters normalize counters and identity; they never choose policy.
- The TPS window exclusively owns time weighting, denominator selection,
  qualification, rolling mean, and latest qualified interval evidence.
- The TPS gate is a pure health predicate over an immutable snapshot.
- A running-limit policy consumes one immutable initialized/administered value;
  it never infers capacity from TPS.
- A window-concurrency policy consumes one immutable revisioned value and the
  unobserved overlay; it never changes TPS health.
- The reservation ledger owns sequence exposure and terminal reconciliation;
  unobserved sequence totals only consume window concurrency and never define
  sustainable concurrency.
- HTTP code maps decisions and emits bounded evidence; it never recomputes TPS.

Remove the obsolete QoS-budget layer and all fields used only to select or spend
a sequence limit: `qosBudgeted`, QoS-budget leases/subreasons, sequence limit,
post-admit sequence count, and current-sequence capacity comparison. Keep
unobserved sequences, sequence liabilities, live reservations, and residual
debts where required for exposure accounting, lifecycle correctness, and the
window-concurrency projection.

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
- effective running limit, its source, projected running, window concurrency,
  and projected unobserved window demand;
- decision result and one bounded subreason;
- enforced reject counters attributable to `waiting`, `preemption`, or
  `below_reference`, `running_limit`, or `window_concurrency`.

Delete TPS-derived sequence-limit, post-admit, and QoS-budget metrics/log fields.
Do not log
request bodies, tokens, credentials, prefixes, user identifiers, or unbounded
labels.

## 6. Test-First Execution

### Phase 1: freeze the v0.12.22 failure

Add focused tests matching the dev risks and the revised contract:

- a healthy request that fits running and window bounds must not be rejected by
  a lower TPS-derived sequence cap;
- a same-observation burst admits complete fanout exactly through the configured
  window concurrency, then rejects the next request with
  `window_concurrency` and zero reservation;
- a request whose projected running exceeds a known running limit rejects with
  `running_limit` and zero reservation;
- changing TPS evidence cannot change either configured bound, and changing a
  bound cannot relabel TPS health.

The first test must fail on v0.12.22 because its derived sequence limit is
exhausted. The other tests must fail because v0.12.22 lacks these independent
policy contracts. No failure may come from invalid fixture state.

### Phase 2: implement the health gate

1. Extend the TPS window snapshot with its latest qualified interval result.
2. Replace capacity selection with the decision contract in section 3.
3. Add strict optional SGLang startup discovery plus explicit initialization and
   admin overrides for running limit.
4. Add atomic running-limit and window-concurrency gates.
5. Remove QoS-budget policy and reservation ownership.
6. Remove obsolete decision fields and reconcile logs, metrics, status, tests,
   simulation, README, and advanced documentation.
7. Preserve controller freshness, identity, atomic lifecycle, and runtime-reset
   safety.

Focused coverage must prove:

- warming and idle do not reject high concurrency without pressure evidence;
- healthy rolling TPS admits regardless of raw, unobserved, reserved, or batch
  sequence counts when both explicit bounds fit;
- a same-epoch burst spends window concurrency atomically and excess demand gets
  an immediate 429 with no reservation or backend call;
- a known running limit cannot be overspent by concurrent requests;
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
- SGLang startup discovery accepts only coherent `max_running_requests`; vLLM
  standard metrics never fabricate a maximum from current running.

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
- dev/backend capability audit on 2026-08-26 confirmed that SGLang-B standard
  metrics contains only current `sglang:num_running_reqs`, while internal
  `/server_info` and its scheduler state both return
  `max_running_requests=256`. vLLM upstream source exposes only current
  `vllm:num_requests_running` in standard metrics; its `/server_info` belongs to
  explicitly unsafe development endpoints. These findings produced the strict
  discovery contract in section 2.1;
- the first focused red test is valid at exact pushed commit
  `90c6d56b7dd39eda1495174768088999a2521996`, source archive SHA-256
  `3b5f315d5cf676828dcdc89c53e32ec2e84e53d2f01bb09bff7e8a5e38971d92`.
  On builder `4f167f6e-4c50-415f-99f2-94b65652beba` with pinned Go image
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`,
  `TestV01223HealthyWindowDoesNotTurnBlindFlightBudgetIntoConcurrencyCap`
  failed for the intended behavior: healthy TPS with `running=1`,
  `unobserved=3`, and the fourth window admission was rejected at inherited
  `sequenceLimit=4`, `postAdmit=5`, subreason `qos_budget_unobserved`. The red
  output SHA-256 is
  `50719fc92444cddbcf7641f627ff6911f5aa7144a6c63f2f72bd76363439ea05`;
- the immediate next step is to add the independent window/running gate contract
  and replace TPS capacity selection.
