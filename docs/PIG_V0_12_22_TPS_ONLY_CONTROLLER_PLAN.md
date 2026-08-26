# PIG v0.12.22 TPS-Only Controller Plan

Status: closed dev baseline. Source, image publication, and isolated dev test
completed, but the concurrency-limit behavior failed the throughput objective
and must not be promoted to production. The corrective work continues in
`PIG_V0_12_23_TPS_HEALTH_GATE_PLAN.md`.

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
6. compares against exact PIG `v0.8.13` and production-shaped traces to absorb
   lessons, explain regressions, and guide follow-up work without turning one
   baseline or numeric result into a hard release gate.

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

The branch base was not TPS-only. Its policy still evaluated context, KV,
Prefill, and TPS gates; startup still required KV/model capability; reservations
still owned KV/Prefill liabilities; and commit
`5b2d0276b9a383a8558d2bef50920a52bc880f33` added cache-Prefill evidence
accumulation. All of those TPS-external decision dependencies must be removed or
separated before the TPS-only source can be considered coherent.

## 4. Target Architecture

```text
canonical inference request
  -> bounded JSON shape scan and Decode sequence count
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
- explicit request or canonical-fallback source for the sequence count.

Request tokenization and input-token estimates do not enter the decision. A
single bounded parser validates JSON and extracts only endpoint-supported
fanout (`n`, Completions `best_of`, and Completions prompt-batch cardinality).
Proven batch multiplicity is charged in full. Ambiguous, invalid, or
overflowing fanout becomes request-scoped `invalid_request`, creates no
reservation, and cannot reduce canonical node capacity. If the bounded scanner
cannot prove fanout because of its byte/depth limit, content type, read failure,
or concurrent byte budget, the request uses a labelled one-sequence fallback
through the same atomic TPS transaction. A scanner boundary cannot independently
cause a 429. There is no detailed token estimator and no declared output
lifetime in the Controller contract.

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
observation. Warming state permits only a bounded probe. Ready-idle state may
refill atomically up to its still-valid rolling base; once that evidence ages
out, the window becomes unready and returns to the bounded warming rule. There
is no hidden one-second hold, cooldown, or consecutive-green counter.

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

## 5. Engineering And Learning Contract

### H1: removing non-TPS gates improves usable throughput

On identical deterministic traces, TPS-only source must never protect because a
KV/cache/Prefill/input field or declared output lifetime changes. Historical
v0.8.13 and production windows are comparison experience: record where the new
controller admits more or less work, why the decision differs, and whether the
result suggests over-protection or QoS risk. Do not require a fixed percentage,
one model-specific fixture, or every trace to improve before source can advance.

Low-flow self-lock, reservation leaks, stale-epoch reuse, non-atomic batch
admission, and decisions that consume retired non-TPS fields remain correctness
failures rather than benchmark thresholds.

### H2: TPS quality remains bounded

Across steady, burst, oscillating, and low-flow traces whose backend TPS
capacity is stable enough for the reference, inspect whether:

- the qualified 60-second active-sequence-weighted mean trends around the
  configured reference without treating every below-reference interval as a
  failure;
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

The simulator may model backend capacity, Prefill stalls, and KV pressure as
external trace effects, but those variables cannot become production admission
gates. Simulated results are diagnostic evidence, not a release oracle. Raw
admits are never relabeled as successful completion goodput.

### H3: recovery is faster and simpler than v0.8.13

When the trace returns to healthy output TPS with no waiting/preemption, inspect
and retain the step-by-step evidence that:

- one additional supported sequence becomes eligible no later than the first
  fresh 500 ms observation that proves it;
- subsequent increases require fresh evidence or a bounded surplus lease;
- idle/sparse traffic can probe from zero/one sequence without waiting for
  representative-load or consecutive-green learning;
- a ready-idle snapshot can refill only up to its unexpired rolling base, and
  every batch is charged by complete Decode fanout even when current is zero;
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
4. Make valid supported shapes produce one bounded sequence demand; reject
   ambiguous or overflowing fanout as request-scoped protection without a
   reservation.
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

On the approved nonproduction builder, run exact v0.8.13 evidence and candidate
source against identical frozen trace manifests when the old oracle remains
executable. Otherwise retain the historical result as context and state that it
is not directly comparable. Retain raw per-step decisions and summaries for:

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

The exact candidate must also complete these correctness and build checks:

```text
git diff --check
gofmt check
focused tests
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Run repeated benchmarks for small-request admission/rollback, observation
publication, and the bounded large-body request path. Record median `ns/op`,
`B/op`, and latency distributions beside the pre-change source. The historical
5% delta and 100 ms extreme-input budget are diagnostic references, not hard
release gates: investigate a clear regression, explain the cause, and prefer the
simpler/faster implementation when correctness is unchanged. No noisy or
model-specific sample is allowed to masquerade as a universal threshold.

### Phase 5: source evidence and later release

This plan authorizes plan/source/test commits and pushes only. It does not
authorize an image, Compose change, CVM/container restart, Router mutation, or
synthetic production request.

Release layers remain separate:

1. plan reviewed;
2. red tests explain the removed behavior;
3. source correctness checks complete;
4. deterministic v0.8.13 comparison recorded as historical context;
5. full clean-builder matrix and benchmarks recorded and reviewed;
6. source committed and pushed;
7. `0.12.x` version assigned;
8. image built and registry digest verified;
9. deployment separately authorized;
10. canary and matched live observation reviewed for obvious regressions.

No source-only result proves production superiority. A later canary should
compare matched traffic using successful completion goodput, mean-active TPS,
below-reference episodes, waiting, preemption, errors/restarts, and protection
reason rates. These observations guide follow-up work; a single window, model,
or exact numeric boundary is not a hard promotion or rollback rule.

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

- source goal: complete this TPS-only plan through reviewed and pushed source;
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
  passed the focused builder test at exact commit
  `a08aea482de8462b02b828217fa88627dcc0af1a`; focused output SHA-256 is
  `536f78c935034cd16b54f695e823981587d8447ddec6d65d8ef5e762fcb2ced0`
  and empty formatting output SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- TPS-only startup/observation red tests: exact commit
  `23c3fa4400786d340bf6f2e913b7dfbfc45f1e19` proved that the inherited
  controller still rejected identity-only initialization and a TPS observation
  without KV/cache telemetry, while the full Controller test for one-poll
  waiting/preemption hold, first-clear recovery, and same-snapshot atomic
  reservation already passed. The raw test output SHA-256 is
  `3ad02ef477770a70413013c370070a333f1cba5bc030d1446f5f739fe8986dc4`;
- TPS-only factory dependency cleanup: the current source archive SHA-256 is
  `7d8e6378aabb8ab9afd776329ade0b78f547c2b6108c17a0534c617207937832`.
  On the independent builder `4f167f6e-4c50-415f-99f2-94b65652beba`, pinned
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`
  (`go1.24.13 linux/amd64`) passed the two default-factory dependency tests,
  vLLM/SGLang observer tests, coherent SGLang startup, and dynamic TPS policy
  API tests. The focused output SHA-256 is
  `bec609e512a6b7c9b385a4a2a7bca2350827f737f15c325d3912cd2b8332c802`;
  the empty formatting output SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
  A preceding run stopped at non-empty formatting output before executing any
  test; it was corrected and is not treated as behavioral evidence;
- TPS-external configuration retirement red: exact pushed commit
  `79e24887095250894253b1f9cba6b6dc507671ae`, source archive SHA-256
  `0915471a2ccdf80d76c44174e00052c5a6417ec5b4f57dcdb6d6bb44fd527bc8`,
  failed for the intended two reasons: the typed configuration still owned the
  six KV/model-length/Prefill fields, and invalid retired environment values
  still changed loading. The red output SHA-256 is
  `4a3ce123eeeec9f1d4850c3b230bb04f97b72cbdafa28a65c58e7854b87b579d`;
- TPS-external configuration retirement green: current source archive SHA-256
  `b3c6dfe7204b3bf4e8e693d69e5e55a2cea0b888834870f7a4e923e98a9ee0c2`
  passed the complete `internal/config/pigconfig` package plus the focused
  default-factory and dynamic TPS policy API tests on the independent builder.
  The output SHA-256 is
  `f15d1400444f547a1c0a7e9619a175e0cc24d3fa4d1b837a5fef5e4b7015eb52`;
  the empty formatting output SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
  `README.md` and `docs/ADVANCED.md` now describe TPS-only source semantics and
  identify those six old settings as ignored/retired;
- final TPS-only source removes the complete KV/Prefill/input/tokenizer
  admission stack, the inherited cache accumulator, the request-aware and TPS
  debt simulators, their retired configuration surface, and the dead tokenizer
  oracle/fixture. `scripts/verify-no-legacy-mode.sh` now rejects reintroduction
  of those packages and files. The Controller contract is limited to Decode
  sequence demand, normalized TPS observations, atomic projection, and the
  reservation lifecycle;
- implementation review corrections completed before the final matrix:
  `demand_source=request` remains observable instead of being normalized to
  `unknown`; ready-idle fanout cannot bypass the sequence limit; a still-valid
  rolling base can refill a ready-idle controller atomically instead of forcing
  it through the warming limit; and waiting/preemption affects marginal intake
  for only the current observation, with no sticky learned cap or cooldown;
- bounded scanner fallback red/green: source archive
  `90b04cef6955817635bf7dd4c62f3d87d1cf88ad2083a5049fbe75661c97571f`
  reproduced `body_too_large`, depth-limit, and non-JSON request shapes as
  scanner-induced `429` responses with zero backend calls (red output SHA-256
  `519630abb970d0459c28589e4948b1ec291815a73f324e1db4eca2d7aa180f49`).
  The corrected source archive
  `3d35d9091344d035123c45e95b3d0f0e26bb058dfc5069dba828571862ac311f`
  routes scanner byte/depth/resource uncertainty through a labelled
  one-sequence TPS fallback, while malformed JSON remains a local OpenAI-shaped
  `400` and proven fanout ambiguity remains request-scoped protection. Focused
  green output SHA-256 is
  `75788a07baa2d1abc85d001501a9c2cc2454b2df989dc5d776181409cd57d899`;
- final executable source archive SHA-256 is
  `2cb348d805d1a1c3d094833db51ed63defa70d0a66c08dc5cfd752bdee68a8f0`.
  The fixed builder was `4f167f6e-4c50-415f-99f2-94b65652beba` through helper
  target `ff40ee31b95e89ebb242c223514adc715ac8a301`, using
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`
  and `go1.24.13 linux/amd64`;
- the final builder matrix completed the legacy-source audit, formatting check,
  `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, five
  admission benchmark runs, five 4 MiB request-shape benchmark runs, and the
  deterministic TPS simulation. The result archive SHA-256 is
  `e0687831ecf0b415a57b7ce40702ab885365e943acadbf6f0da9ddd87882c06c`.
  Material log SHA-256 values are: legacy audit
  `455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0`,
  tests `5eb7ee320960b5fa472d77f7345a3426537a06d8a78413c51b473b897cd7fd73`,
  race `8dc62b6a289d04b46b16c2e89f3d7a31c3c0cb080359048f1cb74ca725c3d832`,
  admission benchmarks
  `2b518fcc302886ba5cacbdc74fad5c2780e2b921aaae04ce0dbd7be948421eb3`,
  request-shape benchmarks
  `1e0f4974936edf88b871c966749ccc1a2ced5ae537e3005d1754b5f714fd7f79`,
  and simulation
  `7334b11c325bd3f2a5630463945f5ca1477d8433af5c9a0b8c5c57c8b1c455b1`.
  Empty formatting, vet, and build logs each hash to
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- diagnostic benchmark observations were `350.0-374.9 ns/op` for Controller
  snapshots, `795.6-919.8 ns/op` for protected admission,
  `647.9-729.6 ns/op` for admit/cancel, `453.9-504.0 us/op` for publication
  with 4096 reservations, and `6.919-7.542 ms/op` (`556-606 MB/s`) for the 4 MiB
  request-shape scan. These are implementation observations only, not hard
  acceptance thresholds or production-throughput claims;
- final three-pass review conclusion: the request path has a causal TPS-only
  contract and scanner uncertainty no longer creates low-load protection; the
  reservation ledger, counter reset, epoch transition, concurrent arrival, and
  terminal paths passed race/lifecycle coverage; dead source, configuration,
  metrics, simulations, oracle assets, and documentation were removed or
  reconciled. GLM-5.2, exact v0.8.13, historical traffic windows, and benchmark
  numbers supplied useful failure patterns but no fixed model, percentage,
  latency, or window became a source promotion rule;
- read-only GLM-5.2 production feedback on 2026-08-26 found approximately
  `470-473` enforced PIG rejections in the preceding rolling 12-hour window
  while backend waiting and preemption remained zero. The running v0.12.17
  reason mix was dominated by retired Prefill/input/KV gates, and a smaller
  TPS group showed low-flow residual-liability behavior. This is design
  feedback for generic fast clear-state recovery and TPS-only separation, not
  a model-specific threshold, exact-fixture release gate, or permission to
  weaken same-snapshot atomic reservations;
- user clarification on 2026-08-26: production feedback and baseline
  comparisons are experience to absorb, not hard acceptance thresholds. Keep
  universal correctness, race, lifecycle, and build verification, but do not
  block or approve a candidate solely from one exact model/window/percentage;
- source stage: implementation, documentation, focused red/green evidence, and
  complete builder verification were committed as
  `ea4d474aeea97a16aeea74ee5a1d4a3817bb61fb` and pushed to
  `pig-origin/codex/pig-v0.12.22-tps-only`;
- release stage opened by explicit user authorization on 2026-08-26: version
  `0.12.22` is assigned in the runtime identity, OCI label, and release identity
  test.

## 10. v0.12.22 Release And Dev Evidence

- exact release source is commit
  `ffa65f2375a83c4c9a69a601d7902c273fedc487`; branch and pushed tag
  `v0.12.22` resolve to that commit. The release archive SHA-256 is
  `5412116fbbbe1f66b3d1ad4d5459d12dfb680cb9d31e578488ded8e76cf9b5cd`;
- the valid clean-builder run used CVM
  `4f167f6e-4c50-415f-99f2-94b65652beba`, helper revision
  `ff40ee31b95e89ebb242c223514adc715ac8a301`, pinned Go image
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`,
  and `go1.24.13 linux/amd64`. Its evidence is
  `/var/volatile/dstack/persistent/.cache/pig-v01222-release/build-ffa65f2-r2`.
  Legacy audit, formatting, `go test ./...`, `go test -race ./...`,
  `go vet ./...`, `go build ./...`, and the production image contract passed;
- builder attempt `r1` is invalid evidence because its runner could hide a
  failed legacy audit behind a pipeline without `pipefail`; it was terminated
  and was not reused;
- the candidate image ID was
  `sha256:ebe955794b20762dbc232513159c1752e2e229dc683df2585d30143d6da6ee34`.
  GHCR tags `0.12.22` and `0.12.22-ffa65f2375a8` resolve to digest
  `sha256:8558b874374de0efad69d270d65660f5fa842df5a5c789031559cf51dccec42c`,
  with OCI version `0.12.22` and OCI revision equal to the exact source commit;
- the isolated dev target was CVM
  `19a2d062-af63-49eb-807d-84ddfbbc905a`, service
  `phala-inference-guard-b`. Because it is a dev image, the guest Compose was
  read over SSH and only PIG-B was recreated. No `phala deploy`, CVM restart,
  SGLang recreation, PIG-A recreation, or HAProxy recreation occurred;
- the live Compose SHA-256 changed from
  `5969632bfb0f1286c9da98bbf2394a19598b6d774229367ff7834a32e983da2e`
  to `1bcc2d8502b50f9a9ab723c68582f2ef26d05ef9b41c8fe0cec3a8dfd94996e2`.
  PIG-B ran image digest `sha256:8558b874...` as container
  `dfb95c88d0654825d3eb390e2e2a945e3b44aa1c231d73ec75ef752ec145da4`
  with restart count zero. Runtime secret values were inherited without echo;
- dev runner attempts `r1` through `r4` rolled back safely. They failed because
  of BusyBox `cp --preserve=all`, incorrect unauthenticated metrics expectation,
  incorrect unauthenticated upstream-status expectation, and BusyBox
  `xargs -0`, respectively. These were runner defects rather than PIG crashes.
  Final attempt `r5` passed and retained evidence at
  `/var/volatile/dstack/persistent/.cache/pig-v01222-release/dev-b-ffa65f2-r5`;
- real-chain checks passed health, authenticated models, authenticated PIG
  metrics, authenticated upstream status, local OpenAI-shaped route blocking,
  normal chat, and streaming through terminal `[DONE]`. Unauthorized protected
  endpoints returned `401`; `POST /v1/tokenize` and wrong-method
  `POST /v1/models` returned local `404` responses.

## 11. Dev Throughput Finding And Decision

The release is functionally deployable but its admission behavior is not a
throughput improvement. Six sequential requests returned six `200` responses.
An eight-request burst at concurrency four returned three `200` responses and
five PIG `429` responses while the backend had one running sequence, zero
waiting, and no preemption. The decisive projection was:

```text
backend running             = 1
local unobserved admissions = 3
projected current           = 4
post-admit                  = 5
sequence limit              = 4
subreason                   = qos_budget_unobserved
```

All reservations and liabilities returned to zero after the burst, so this was
not a lifecycle leak. It was a policy failure: low-concurrency observations
produced a low derived limit, while unobserved requests prevented the controller
from acquiring evidence at a higher concurrency. The result is a conservative
closed loop even when the backend can sustain materially higher concurrency.

Decision on 2026-08-26:

- keep the published `v0.12.22` tag and digest immutable as reproducible dev
  baseline evidence;
- do not promote `v0.12.22` to production nodes;
- do not repair this by raising a static sequence limit, adding a model-specific
  threshold, or allowing a larger bounded exploration cap;
- retire sequence-limit selection completely. Continue with a TPS health gate
  that rejects new work only after degradation evidence, as specified in the
  v0.12.23 plan.
