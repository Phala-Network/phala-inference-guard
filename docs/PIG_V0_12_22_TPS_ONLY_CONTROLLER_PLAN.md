# PIG v0.12.22 TPS-Only Controller Plan

Status: active source plan; version not assigned; image and deployment not
authorized.

Formal branch: `codex/pig-v0.12.22-tps-only`.

Plan authority: this document supersedes
`PIG_V0_12_22_CACHE_KV_TPS_CONTROLLER_PLAN.md` and every earlier PIG admission
algorithm plan where they conflict. Public route, authentication, local
management, proxy streaming, response, and attestation contracts remain out of
scope and must not regress.

## 1. Objective

Replace the current multi-resource predictive admission policy with one small,
pre-forward TPS controller that:

1. treats `PREDICTIVE_TPS_REFERENCE` as a sufficiently long mean output-TPS per
   active Decode sequence objective, not as a per-poll hard floor;
2. maximizes accepted, successful completion goodput while keeping the rolling
   mean near or above the configured reference;
3. admits or protects before forwarding, while observations from completed or
   in-flight work only improve the next decision;
4. recovers capacity at the configured 500 ms observation cadence and cannot
   low-flow self-lock;
5. keeps backend waiting absent or short-lived without retaining a learned low
   cap after waiting clears;
6. is measurably better than exact PIG `v0.8.13` on frozen, deterministic,
   production-shaped traces before any release claim.

The controlled reference for initial tests is `25 tok/s/active Decode
sequence`. Production keeps one intended policy override:

```text
PREDICTIVE_TPS_REFERENCE=25
```

The source default may remain zero to make TPS admission explicitly opt-in in
nonproduction environments. Default observation polling remains 500 ms.

## 2. TPS-Only Means TPS-Only

No request may be protected because of any of the following:

- current or projected KV tokens, KV ratio, block geometry, or cache capacity;
- cache hit rate, cache evidence, prefix identity, or cache credit;
- input/context token count or model context limit;
- Prefill class, Prefill token budget, exclusive/quiescent long-input policy,
  or estimated Prefill seconds;
- TTFT, queue duration, request body bytes, tokenizer confidence, or a learned
  global concurrency cap.

KV, cache, Prefill, and request-size telemetry may remain available to operators
as optional observations, but the TPS controller must not import those fields
or fail closed when they are missing. There must be a causal test that holds TPS
state constant, varies each optional field across extreme values, and proves the
same decision and reservation.

Backend waiting and preemption are not separately configurable capacity gates.
They are immediate evidence that the currently projected TPS concurrency is not
safe to expand. While either is present, the controller admits no new marginal
sequence and reports a bounded `tps_reference` subreason. This hold lasts only
for that observation: it does not lower the rolling base, create a learned cap,
start a cooldown, or require consecutive clear samples. The first fresh 500 ms
observation with zero waiting/preemption returns immediately to normal TPS
selection.

Protocol-invalid requests still receive their existing local client error.
Controller absence, stale TPS telemetry, runtime identity drift, arithmetic
overflow, or lifecycle corruption remain availability failures and may fail
closed. Those are not resource-limit policies.

## 3. Baseline Review

### 3.1 Exact v0.8.13

Tag `v0.8.13`, exact commit
`8c224dbfb28a1e5019b7c2b524760cee707703de`, is a feedback-driven learned-cap
pipeline. It combines a global limit with TPS yellow/red thresholds, TTFT, KV
yellow/red, running, waiting, preemption, queue pressure, Prefill transition grace, smoothed capacity,
representative-load detection, consecutive healthy samples, and gradual step-up
state. Its recovery can depend on traffic proving a previously learned cap and
on several independent signals becoming green.

That architecture has three material disadvantages for the new objective:

1. different signals can suppress capacity after the signal that caused the
   suppression has disappeared;
2. sparse traffic may not provide the representative load needed to relearn a
   larger cap, creating low-flow self-lock or slow recovery;
3. the large configuration and state surface makes a protection difficult to
   attribute to the long-run TPS objective.

The exact tag remains the comparison oracle. It is not copied into production
source and no prose claim about it substitutes for an executable frozen trace.

### 3.2 Current v0.12.21/branch behavior

The current TPS controller is already materially simpler than v0.8.13. It has:

- a 60-second active-sequence-weighted TPS window;
- `floor(aggregate TPS / reference)` as the ready base sequence limit;
- a one-sequence current-rate recovery candidate;
- one low-flow probe;
- a bounded rolling-surplus candidate of at most `base + 1`;
- one atomic outstanding QoS-budget lease.

The Redpill example is an admit, not a rejection:

```text
aggregate TPS = 100.7
reference     = 25
base limit    = floor(100.7 / 25) = 4
current       = 2
post-admit    = 3
decision      = admit because 3 <= 4
```

The current branch is not TPS-only. Its policy still evaluates context, KV,
Prefill, and TPS gates; startup still requires KV/model capability; reservations
still own KV/Prefill liabilities; and commit
`5b2d0276b9a383a8558d2bef50920a52bc880f33` added cache-Prefill evidence
accumulation. All of those TPS-external decision dependencies must be removed or
separated before source acceptance.

## 4. Target Architecture

```text
canonical inference request
  -> decode sequence count and declared output bound
  -> fresh backend identity/running/waiting/generation/preemption observation
  -> rolling TPS state plus unobserved local sequence leases
  -> post-admit TPS counterfactual
  -> one atomic TPS decision and sequence lease
  -> forward / first response / terminal lifecycle
  -> next observation reconciles sequence exposure and rolling TPS evidence
```

### 4.1 Request contract

The controller consumes a small immutable `TPSRequestDemand`:

- positive Decode sequence count;
- optional declared output-token limit;
- explicit confidence/fallback source for the sequence count.

Request tokenization and input-token estimates do not enter the decision. A
well-formed supported inference request whose detailed token estimator is
unsupported or uncertain falls back conservatively to one Decode sequence when
the protocol shape does not prove a larger batch. It is not rejected as
`invalid_request` merely because KV/Prefill estimation is unavailable. Proven
batch multiplicity remains charged in full.

The output bound may cap the duration used by a surplus lease. An unknown output
bound uses the existing bounded control horizon and cannot create an unbounded
lease.

### 4.2 Observation contract

The admission-owned backend observation contains only:

- stable runtime/backend/model identity and start epoch;
- observed time and maximum age;
- running and waiting sequence counts;
- cumulative generated output tokens;
- cumulative preemption/retraction count.

KV capacity, block size, used KV, cache counters, cache hit rate, input/Prefill
counters, and `/v1/models` context metadata are not required to initialize or
keep TPS admission available. Backend adapters may parse optional operator
telemetry through a separate reporting value, but cannot pass it to TPS policy.

### 4.3 TPS limit selection

For a ready rolling window:

```text
base_limit = max(1, floor(rolling_aggregate_output_tps / reference))
```

The current sequence demand is the maximum of coherent backend demand and
unabsorbed local leases. The post-admit value includes the complete request
batch before the decision.

The selected limit is the maximum eligible value among:

1. the rolling base limit;
2. at most one current-rate recovery step (`current + 1`) when the current
   per-active-sequence rate has at least 5% headroom and the projected rate has
   at least 95% of the reference;
3. at most one rolling-surplus step (`base + 1`) when the bounded predicted TPS
   debt fits accumulated surplus and no other surplus lease exists.

Waiting or a current preemption disables all marginal admission for exactly the
current observation. It does not mutate candidate 1 or survive a fresh clear
observation. Idle and warming states permit only a bounded probe and can advance
again on the next valid 500 ms observation; there is no hidden one-second hold,
cooldown, or consecutive-green counter.

Preventing waiting before it appears depends on atomic projection: backend
running plus waiting, complete proven batch multiplicity, and every locally
admitted sequence not yet visible in backend metrics are included before a new
lease is granted. No two concurrent requests can both consume the same apparent
headroom.

Occasional below-reference intervals are allowed. The objective is the
qualified, active-sequence-weighted rolling mean, and the surplus lease makes
that tolerance explicit and bounded.

### 4.4 Ownership and lifecycle

`TPSReservationLedger` owns only:

- Decode sequence liabilities not yet visible in backend running/waiting;
- forwarded and response-active sequence exposure;
- optional ownership of the single marginal surplus lease;
- runtime epoch and monotonic lifecycle sequence numbers.

It does not own KV, input, Prefill, cache-credit, context, or first-byte KV
coverage state. Reserve, forward, first response, success, client cancel,
disconnect, upstream error, timeout, response EOF, panic cleanup, counter reset,
runtime restart, policy update, and shutdown must each reconcile exactly once.

## 5. Falsifiable Acceptance Contract

### H1: removing non-TPS gates improves usable throughput

On identical deterministic traces, TPS-only source must never protect because a
KV/cache/Prefill/input field changes. For healthy traces whose qualified mean
TPS stays at or above 25, it must:

- admit at least as many successful requests as v0.8.13 on every trace;
- improve successful completion goodput or admitted successful requests by at
  least 5% on at least one previously overprotected mixed-load trace;
- reduce total protections and protection time versus v0.8.13;
- produce no sustained low-flow lock.

If it cannot show a throughput improvement on any trace without violating the
QoS contract, the new policy is not better and no version/image is authorized.

### H2: TPS quality remains bounded

Across steady, burst, oscillating, and low-flow traces whose backend TPS
capacity is stable enough for the reference:

- the qualified 60-second active-sequence-weighted mean TPS is at least the
  configured reference after warmup, or any deficit is fully covered by the
  bounded surplus accounting and resolves within its declared horizon;
- a single sub-reference poll does not collapse the limit;
- sustained degradation stops new marginal admission without terminating
  existing streams;
- waiting/preemption/stale evidence never grants optimistic expansion;
- any observed waiting/preemption stops marginal intake for at most that
  observation and zero waiting resumes normal selection on the next fresh poll;
- no more than one marginal exploration lease exists;
- no reservation leak, double release, stale-epoch reuse, or counter rollback
  bypass occurs.

Separate cache-hot/cold, short/long-input, and mixed-input traces model
exogenous Prefill stalls. Because input size does not enter TPS-only admission,
the controller cannot promise to prevent the first previously unobserved stall
before forwarding. It must not compound that stall after the first fresh 500 ms
TPS observation, and it must recover without retaining a learned low cap. This
is an explicit consequence of the user-selected TPS-only boundary, not a green
claim that extreme input has no QoS effect.

The simulator may model backend capacity, Prefill stalls, and KV pressure to
stress the TPS outcome, but those model variables cannot become production
admission gates. Simulated successful completion goodput is counted only when
the frozen backend oracle marks an admitted request successful and terminal;
raw admits are not treated as completions.

### H3: recovery is faster and simpler than v0.8.13

When the trace returns to healthy output TPS with no waiting/preemption:

- one additional supported sequence becomes eligible no later than the first
  fresh 500 ms observation that proves it;
- subsequent increases require fresh evidence or a bounded surplus lease;
- idle/sparse traffic can probe from zero/one sequence without waiting for
  representative-load or consecutive-green learning;
- the decision reason is one of `open`, `tps_reference`, or an explicit
  availability/integrity failure, never an obsolete KV/Prefill/input reason.

## 6. Execution Phases

### Phase 0: freeze truth and remove abandoned cache work

1. Preserve exact v0.8.13 commit
   `8c224dbfb28a1e5019b7c2b524760cee707703de` and current branch provenance.
2. Add red tests proving optional KV/cache/Prefill/input changes currently alter
   decisions or availability.
3. Remove the v0.12.22 cache accumulator and its plan-specific tests.
4. Keep the synthetic constant-zero metric removal because it is an independent
   observability correction.
5. Freeze deterministic trace inputs and capture exact v0.8.13 oracle outputs
   on the approved nonproduction builder.

### Phase 1: extract the TPS-only vertical slice

1. Introduce `TPSRequestDemand`, `TPSBackendObservation`,
   `TPSProjectedState`, and `TPSReservationLedger` as small value/ownership
   boundaries.
2. Make the controller build and validate only the TPS counterfactual.
3. Delete context/KV/Prefill/cache gates from policy composition rather than
   leaving disabled branches or no-op configuration.
4. Make uncertain detailed token estimates fall back to a bounded sequence
   demand instead of causing request protection.
5. Retain atomic check-and-reserve under the existing controller mutex.

### Phase 2: correct TPS recovery and risk behavior

Add focused red tests before changing any TPS rule:

- the Redpill `100.7/25/current 2/post 3` fixture admits at limit 4;
- waiting/preemption stop marginal intake for one observation, never lower the
  rolling base, and recover on the first fresh clear observation;
- current-rate recovery grants only one sequence for one observation;
- concurrent arrivals cannot double-spend that step or the surplus lease;
- warming and idle recover at 500 ms without low-flow self-lock;
- one poor poll is tolerated while sustained poor TPS protects;
- unknown output uses the bounded horizon;
- batch multiplicity is reserved atomically;
- all terminal and runtime-reset paths reconcile once.

Only implement a behavior change for a focused red failure. Do not add another
learner, threshold family, hidden timer, or public tuning knob.

### Phase 3: remove dead configuration and observability

Production policy configuration must converge on:

```text
PREDICTIVE_TPS_REFERENCE=25
```

Mode may be explicitly set to `shadow` during tests; production remains enforce
by default. Remove KV hard ratios, max-input overrides, Prefill thresholds,
cache-credit settings, and any dynamic management fields that can no longer
affect behavior. Do not keep deceptive no-op knobs for compatibility.

Logs, metrics, status, and dynamic policy API must agree on:

- rolling/base/current-rate/surplus/selected TPS limits;
- current/post-admit sequences;
- decision result and bounded subreason;
- active marginal lease count;
- success-linked request and completion-token outcomes when protocol evidence
  exists.

Remove obsolete KV/Prefill/cache decision fields from admission logs and status.
Optional backend health telemetry may remain in a clearly separate backend
section. Labels remain bounded and no body, token, prefix, customer, or secret is
logged.

### Phase 4: deterministic comparison and clean-builder matrix

On the approved nonproduction builder, run exact v0.8.13 and candidate source
against identical frozen trace manifests. Retain raw per-step decisions and
summaries for:

- healthy steady load;
- low-flow ramp and restart;
- burst then recovery;
- one-poll dip and sustained degradation;
- single-poll and sustained waiting/preemption episodes, including immediate
  recovery after the first fresh clear sample;
- short/long and cache-hot/cold mixes with identical TPS outcomes;
- unknown output and multi-sequence batches;
- cancellations, errors, disconnects, timeouts, counter rollback, epoch reset,
  and concurrent arrivals.

The exact candidate must also pass:

```text
git diff --check
gofmt check
focused tests
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Run five benchmark repetitions for small-request admission/rollback and
observation publication. Median `ns/op` and `B/op` may regress by no more than
5% from the pre-change v0.12.21 source; a noisy result is inconclusive. The
accepted 4-MiB request path remains below 100 ms p99.

### Phase 5: source acceptance and later release

This plan authorizes plan/source/test commits and pushes only. It does not
authorize an image, Compose change, CVM/container restart, Router mutation, or
synthetic production request.

Release layers remain separate:

1. plan reviewed;
2. red tests valid;
3. source green;
4. deterministic v0.8.13 comparison accepted;
5. full clean-builder matrix and benchmarks accepted;
6. source committed and pushed;
7. `0.12.x` version assigned;
8. image built and registry digest verified;
9. deployment separately authorized;
10. canary and matched live observation accepted.

No source-only result proves production superiority. A later canary must compare
matched traffic using successful completion goodput, mean-active TPS, below-
reference episodes, waiting, preemption, errors/restarts, and protection reason
rates.

## 7. SOLID And Efficiency Boundaries

- Request parsing discovers demand; it does not choose a limit.
- Backend adapters normalize TPS evidence; they do not implement policy.
- The TPS window owns time-weighted evidence; it does not own reservations.
- Limit selection is a pure function of projected TPS state and demand.
- The reservation ledger owns lifecycle and atomic marginal-lease ownership.
- HTTP runtime maps a decision to forward or OpenAI-shaped protection; it does
  not recompute capacity.
- Observability formats immutable snapshots and cannot change a decision.

The request hot path must remain O(1) after bounded request metadata extraction,
with no scan over live reservations. Rolling state, lease counts, and sequence
liabilities remain fixed-size or O(1) aggregates. No per-prefix, per-user,
per-model, or unbounded learned map is allowed.

## 8. Plan Review Record

Three reviews were completed before behavior code:

1. Model and causality review: the first draft risked retaining KV capability
   startup and using input tokens as an uncalibrated TPS cost. The plan now
   removes KV/model-context availability dependencies and forbids input-token
   admission until a trustworthy unit conversion exists. It also corrected the
   Redpill example to an admit and retained bounded average-TPS surplus rather
   than enforcing every poll.
2. Safety and lifecycle review: the review checked unknown request shapes,
   batches, warming, idle, waiting, preemption, stale observations, concurrent
   arrivals, all terminal paths, counter rollback, and runtime epoch changes.
   The user then clarified that waiting should be kept near zero without sticky
   protection. The plan therefore retains an observation-scoped marginal-intake
   hold for waiting/preemption, forbids learned cap/cooldown/consecutive-clear
   state, requires immediate recovery on the first fresh clear poll, retains
   fail-closed integrity failures, and makes the reservation ledger
   sequence-only.
3. SOLID, efficiency, evidence, and release review: the review checked whether
   disabled KV code/no-op config would remain, whether optional telemetry could
   leak back into policy, whether the v0.8.13 comparison was executable, and
   whether source success could be mistaken for a release. It requires deletion
   rather than disabled branches, causal invariance tests, frozen dual-version
   traces, five-run benchmarks, exact hashes, and a strict no-image/no-deploy
   boundary.

## 9. Current Execution State

- active goal: execute this TPS-only plan;
- formal branch: `codex/pig-v0.12.22-tps-only`;
- branch creation base: pushed commit
  `702a0b03c87bec6bc9293a6528041726682e59c5`;
- exact comparison baseline: tag `v0.8.13`, commit
  `8c224dbfb28a1e5019b7c2b524760cee707703de`;
- plan reviews: three complete;
- TPS-only red tests: valid on the approved builder at exact commit
  `dad7861a5c8a073e4fb826c9fb5a8df4befde6f2`; the focused command exited `1`
  for the four intended behavioral reasons (`input_limit`, `kv_capacity`,
  `prefill_budget`, and missing-estimate `invalid_request`). The raw output
  SHA-256 is
  `6e7c22a014a219a7de375b42502a4af5c144dcbff9c942931739c61133c05d52`;
  the empty `gofmt -d` output SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
  The builder image was
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
  Two preceding runner attempts were rejected as environment evidence because
  login-shell PATH hid `gofmt`, then the builder host lacked `base64`; neither
  attempt executed a source test successfully;
- TPS-only source: the first sequence-demand/policy/reservation vertical slice
  is implemented locally and awaits exact-commit builder verification;
- inherited cache accumulator: present and explicitly noncompliant; removal
  pending Phase 0;
- TPS behavior: unchanged from branch base;
- version/image/deployment: not authorized and not started.
