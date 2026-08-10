# PIG v0.12 Architecture-First QoS-Constrained Goodput Plan

Status: active architecture redesign. The last executable baseline remains
`cc412649ddb30bb808053f1e3945e8cb818b3dc6`; the documentation-only commit for
this plan may move branch HEAD without changing executable evidence. The running
c21 PIG is the rejected, unpublished local v0.12.9 image. No next patch version
is assigned. No image may be built until the architecture, production-shaped
simulator, and pre-version source gates in this document pass.

This file is the only current execution plan. Historical v0.12.3 through
v0.12.9 design and evidence remain available in Git history; they are not active
instructions. After context compression, resume from sections 3, 9, 12, and 15
instead of inheriting a historical candidate.

## 1. Objective

Maximize SLO-compliant completed-token goodput while:

- preventing admission-caused KV exhaustion and preemption;
- bounding per-user Decode TPS degradation rather than demanding zero loss;
- keeping an available upstream work-conserving for requests that fit current
  resource and QoS budgets; and
- making every authoritative decision before the request is forwarded.

The optimization order is:

1. hard safety: no candidate-caused KV overflow, preemption, restart, or leaked
   reservation;
2. QoS: per-user Decode TPS stays above the workload's frozen acceptance floor;
3. goodput: maximize SLO-compliant completed output tokens per wall-clock second;
4. efficiency: keep classification and admission overhead negligible relative
   to inference.

Backend observations update the next counterfactual state. They are not a
cooldown, retry trigger, learned coefficient, or substitute for pre-forward
prediction.

For each controlled GPU workload, freeze the Decode QoS floor before candidate
measurement at 85% of the matched no-enforcement shadow reference's per-user
Decode TPS p10. SLO-goodput counts completed output tokens only while the request
meets that floor. Report the raw token rate as a separate diagnostic.

A candidate is Pareto-promotable only when all repetitions have no candidate-
caused preemption, restart, fatal signal, or lifecycle failure; short-only median
SLO-goodput is at least 98% of v0.12.2; mixed and long-workload median
SLO-goodput is not below v0.12.2; and at least one material gain exists: 5%
higher median SLO-goodput, fewer preemptions, or fewer QoS-violation seconds
without lower goodput. Run at least three repetitions in both A/B and B/A order
and report every repetition.

## 2. Explicit non-goals

This release does not add:

- routing or backend selection;
- cache or prefix-hit inspection;
- a model-specific tokenizer, template, or asset bundle;
- request mutation, tiering, or priority injection;
- TTFT admission protection;
- online learning or active calibration;
- a TPS feedback controller;
- retry loops, reject cooldowns, or a long local request queue; or
- production Router or canary changes.

Default operation is `enforce`; `shadow` exists only as an explicit test mode.
The default observer interval is 500 ms. Production configuration should set
only deployment-specific endpoints, authentication, and secrets; algorithm
defaults should normally remain implicit.

## 3. Current truth and stop boundary

### 3.1 Authoritative environment

```text
CVM
  c21b7281-2c25-4453-8a68-f39ec42d03b4

workbench
  pig-v0124-workbench

repository
  /workspace/src/phala-inference-guard-r3

branch
  codex/pig-v0.11.0-request-aware

last executable baseline and upstream parent
  cc412649ddb30bb808053f1e3945e8cb818b3dc6
```

The plan-only commit that contains this document is not a new executable
baseline. Record its resulting HEAD after push, but keep every source, image,
and runtime claim anchored to `cc41264` until executable source is committed.

Do not use the old builder. Do not run executable Go or image gates on local
Windows. Do not restart the CVM or vLLM. A later runtime gate may replace only
PIG on c21.

### 3.2 Running stack

```text
PIG image
  ghcr.io/phala-network/phala-inference-guard:0.12.9-28f7328-local

PIG container ID
  503dccc4241350de304f687fd28cdb878499412b3720b1b3f8bbf8b37c554ee7

vLLM container
  d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552
```

The PIG image is local and unpublished. vLLM has not been restarted.

### 3.3 Rejected runtime result

The first mandatory sustained gate rejected v0.12.9:

```text
requests                         24
successes                        14
request-scoped 429               10
other failures                    0
Decode p10/p50          183.669/187.798
completed-token goodput    1303.997 token/s
peak running/waiting             12/0
peak reservations/KV       12/13.495%
preemptions                       0
final reservations/waiting        0/0
final Router status               0
fatal scan                         0
```

With a 1,298-token candidate and twelve active Decode requests, v0.12.9 used:

```text
(post-admit total pending Prefill tokens) * active Decode sequences

2 pending: 2596 * 12 = 31152 -> admit
3 pending: 3894 * 12 = 46728 -> reject
```

A rejected worker immediately submitted its next queued request, causing the
ten-request 429 cascade. EOF lifecycle timing was not the root cause.

### 3.4 Uncommitted experiment

Exactly three executable files are dirty. They change the Decode formula to
`candidate Prefill * active Decode sequences` and add focused tests:

```text
internal/runtime/predictive/decode_envelope.go
internal/runtime/predictive/decode_envelope_test.go
internal/runtime/predictive/request_aware_policy.go
```

This experiment is not accepted. It fixes the observed overprotection but can
underprotect when many individually small Prefills accumulate. Keep it
uncommitted until the architecture in section 9 replaces or rejects it. Do not
run the prepared v0.12.10 identity script.

The exact old-source focused red is retained at:

```text
/workspace/evidence/pig-vnext-marginal-cascade-red-r1-cc41264
SHA256SUMS SHA-256
  e29428f1773ee27fb1989f1e03514e68949cdd03f0c32d3462ea183bd58c2c8a
```

## 4. Reflection: why the prior loop failed

The v0.12.3 through v0.12.9 sequence repeatedly followed the wrong order:

1. a local symptom received a narrow source fix;
2. focused and full source tests passed;
3. a patch version and image were created;
4. only then did a production-shaped workload test the architectural premise;
5. the next patch repaired the newly exposed symptom while retaining most of
   the same unproven state model.

Specific examples were missing Decode protection, double-counted phase state,
stale terminal accounting, successful EOF timing, and finally the state-total
Decode multiplier. Each red was useful, but the process versioned hypotheses
before testing the decisive workload.

The previous plan also grew into an audit log of many superseded versions. Its
top-level conclusion and later corrective sections could disagree after context
compression. This document therefore keeps only the current contract, current
evidence, and next gates. Git history remains the historical record.

One additional architecture defect and one unmeasured efficiency risk were
hidden by the patch loop:

- the production HTTP adapter and simulator construct materially different
  `RequestCost` values; and
- the Manager recomputes admission state by scanning every live reservation on
  every decision. That is O(n), but it is not a demonstrated bottleneck at the
  supported concurrency and must be benchmarked before adding cached counters.

## 5. End-to-end architecture

The only supported production transaction is:

```text
HTTP path/auth/body bound
  -> request classifier and model-neutral estimator
  -> immutable backend capability profile
  -> fresh vLLM observation
  -> Manager atomic counterfactual and reservation
  -> pure ordered admission policy
  -> one decision outcome and protection scope
  -> HTTP forward or immediate 429
  -> reservation forward commit
  -> first upstream response bytes mark Prefill complete
  -> clean 2xx EOF or fallback terminal releases exactly once
  -> observer reconciliation updates the next counterfactual
  -> the same decision record feeds logs, metrics, status, and Router projection
```

### 5.1 Ownership

| Component | Owns | Must not own |
|---|---|---|
| Request classifier | admitted paths, bounded body read, protocol validation, output horizon field extraction | policy, backend state |
| Work estimator | model-neutral Prefill selection estimate and conservative input safety upper | exact token IDs, cache state, admission |
| RequestCost builder | one canonical rolling KV/Decode-horizon cost contract | HTTP, scheduler simulation, policy thresholds |
| Capability initializer | immutable model identity, KV capacity/block size/hard limit, size geometry | active probing, learned speed, runtime threshold mutation |
| vLLM observer | fresh KV/running/waiting/generation/preemption facts and identity epoch | request classification, reservations, admission |
| Manager | atomic observed-plus-reserved state, lifecycle, reconciliation, bounded aggregates | HTTP mapping, policy threshold choice |
| ResourceSafetyGate | post-admit hard KV fit | Prefill classes, TPS, Router status |
| PrefillQoSGate | request class, pending Prefill work, Decode-active Prefill budget, long-request exclusivity | KV safety, lifecycle, HTTP mapping |
| Adapter | translate the policy outcome to HTTP and expose one coherent decision record | hidden policy or delayed unlock state |
| Reporting | logs, metrics, status, Router-compatible projection | changing a decision |
| Simulator | deterministic arrival, scheduler, lifecycle, and objective replay using production contracts | claiming GPU capacity from synthetic constants |

One mutable quantity has one owner. A value may be copied into telemetry, but
two policy components may not independently charge the same risk under different
names.

## 6. Request work model

The estimator deliberately produces two different input quantities:

```text
selection_prefill_tokens
  bounded model-neutral lexical estimate used for Prefill/QoS admission

safety_input_tokens
  max(whole-body conservative upper, selection estimate), used for hard KV
```

For recognized multimodal inputs, the conservative input upper replaces the
lexical URL/marker estimate for Prefill selection. Unsupported, malformed,
overflowing, unknown-length, oversized, or classifier-saturated request shapes
fail closed before forwarding.

The current lexical estimator is intentionally approximate. It traverses the
bounded JSON body, samples at most 64 bytes in up to four windows per string,
adds message/template evidence, and does no vocabulary lookup, model asset load,
FFI, network call, or request mutation. Exact tokenizer parity is not required
for this release.

### 6.1 Canonical RequestCost

The HTTP path and simulator must call one pure RequestCost builder with:

```text
selection Prefill estimate
safety input upper
bounded rolling Decode horizon
KV block size
manifest/identity epoch
```

It produces:

```text
input KV              = round_up(safety input, block)
total reserved KV     = round_up(safety input + Decode horizon, block)
future rolling KV     = total reserved KV - input KV
active context        = safety input + Decode horizon
future context        = Decode horizon
Decode sequences      = 1
```

The default 256 Decode tokens are a rolling reservation horizon, not the full
declared output maximum. After each fresh observation, materialized KV moves to
the observed base while the live request retains the future horizon. Tests must
prove that this horizon is neither dropped nor double-counted.

The simulator's current behavior of using `reservedTokens` as `InputTokens`
while setting future KV, future context, and Decode horizon to zero is invalid
and must be removed.

## 7. Immutable capability and observations

At startup PIG obtains from vLLM metrics:

- backend kind and model identity;
- exact KV capacity in tokens;
- KV block size;
- current KV, running, waiting, generation, and preemption counters.

It obtains `max_model_len` from model metadata when available and otherwise uses
the bounded fallback. The capability profile is immutable for one identity
epoch:

```text
hard KV limit = block_align_down(KV capacity * 0.88)
effective span = min(max model length, hard KV limit)
regular        = min(64K, effective span / 8)
exclusive      = min(256K, effective span / 2)
quiescent      = min(512K, effective span)
aggregate      = exclusive
```

These are geometry defaults, not measurements of Prefill speed or guaranteed
TPS. Explicit overrides remain a complete all-or-none test/deployment escape
hatch, but production should normally omit them.

The current c21 profile is:

```text
KV capacity       862437
KV block size          64
hard KV limit      758912
regular             32768
exclusive          131072
quiescent          262144
aggregate          131072
```

Its geometry implies an effective model span and quiescent boundary of 262,144
tokens. Therefore c21 cannot serve as evidence for a 512K/650K upstream contract.
Those classes remain in the portable design and require a later upstream whose
declared model context and hard KV capacity actually support them.

The observer polls every 500 ms. A sample is usable only if identity, capacity,
block size, counters, and freshness remain coherent. Identity/capacity drift or
counter reset closes intake for that epoch; transient fetch failure becomes
stale only after the maximum age.

`AggregateTPSProxy`, `MeanActiveTPSProxy`, and `TPSValid` are diagnostics only.
No current Gate consumes them. This release must not be described as a runtime
TPS predictor. Decode TPS is a controlled acceptance metric; runtime protection
comes from the pre-forward Prefill budget in section 9.

## 8. Atomic state and lifecycle

The Manager is the single synchronization boundary. Under one lock it must:

1. validate manifest, epoch, request ID, and canonical RequestCost;
2. combine the last coherent observation with all not-yet-absorbed reservations;
3. construct the post-admit counterfactual;
4. run the pure policy;
5. create a reservation only for an enforce-mode admit; and
6. return the decision and monotonic manager sequence.

The request lifecycle is:

```text
Reserved
  -> ForwardedPrefill
  -> ActiveDecode
  -> Terminal
```

Every transition is monotonic and idempotent at the adapter guard. Terminal
causes include completed, local reject, client cancel/disconnect, upstream
failure, timeout, expiry, epoch invalidation, and shutdown. Every path releases
exactly once. Duplicate or impossible transitions fail without reusing state.

For streaming responses, first response body bytes are the available proxy for
Prefill completion. For non-streaming responses, vLLM may not emit body bytes
until generation is complete; PIG must keep the request conservatively pending
until the first byte or terminal event rather than inventing a phase transition.
Error bodies must not leave an active Decode reservation after terminal release.
Streaming and non-streaming lifecycle tests are both mandatory.

Observation reconciliation uses sample start/finish watermarks. A reservation
can move from locally unabsorbed to backend-absorbed only when the sample window
and observed materialized floor can cover it. Ambiguity remains conservative.
If an absorbed request terminates before the next observation, a bounded retired
ledger subtracts its materialized floor from the stale sample until a later
sample covers the terminal event.

### 8.1 Hot-path efficiency decision

The current Manager recomputes its counterfactual by scanning live reservations.
This keeps one source of truth and avoids a second mutable counter ledger, but
cost grows linearly with concurrency. Do not replace it speculatively.

First benchmark admission with 1, 48, 256, and 4,096 live reservations. If the
supported production range remains below the section 12 latency budget, keep
recomputation for simplicity and correctness. Only a measured failure may
introduce incremental aggregates. That follow-up must keep a slow recomputation
oracle in tests and prove equality after reserve, forward, Prefill-complete,
terminal, reconciliation, cancellation, epoch, overflow, and race transitions.

## 9. Admission policy redesign

The old state-total Decode multiplier is rejected because it produced the live
2-admit/10-reject cascade. The uncommitted candidate-only multiplier is also not
the target architecture because many small candidates can accumulate without a
combined Decode-QoS bound.

The next candidate removes `DecodeEnvelope` as an independent multiplier. One
`PrefillQoSGate` owns both aggregate pending Prefill work and the stricter
Decode-active Prefill budget.

### 9.1 Counterfactual inputs

```text
candidate selection Prefill tokens
post-admit pending Prefill tokens and class counts
post-admit hard KV
effective active Decode sequences
observed waiting
fresh preemption delta
immutable capability geometry
```

`effective active Decode` is observed running corrected by same-observation
completed credits, plus Prefill-complete local reservations not yet absorbed by
the observation. Waiting is separate and is never counted as active Decode.

### 9.2 Ordered decisions

1. **Validity and availability**

   Invalid request cost, stale metrics, identity drift, closed epoch, overflow,
   or impossible state returns availability or request protection before any
   upstream call.

2. **Hard resource safety**

   Reject with load scope when conservative post-admit KV exceeds the immutable
   hard KV limit. This Gate uses safety input and rolling Decode horizon, never
   the smaller lexical estimate.

3. **Prefill class and long-request ownership**

   - regular: may coexist only while the applicable aggregate budget fits;
   - weighted: uses the aggregate budget and is blocked by pending exclusive or
     quiescent work;
   - exclusive: at most one long Prefill and no active Decode;
   - quiescent: requires no active Decode, waiting, or pending Prefill.

   Unknown pending Prefill work from startup or an unattributed backend request
   cannot be assigned a token budget. It therefore load-protects new admission
   until a fresh observation clears it; this state has no timer or sticky latch.

   A fresh preemption delta may only tighten non-regular admission until the
   next coherent sample. It does not create a cooldown or learned parameter.

4. **Decode-active Prefill budget**

   When `effective active Decode > 0`:

   ```text
   candidate selection Prefill <= regular
   post-admit total pending Prefill <= regular
   ```

   A candidate larger than `regular` is request-scoped because a smaller request
   can fit. Exhaustion of the post-admit total is load-scoped because current
   pending work blocks even a fitting class until it progresses.

   When no Decode is active, the normal aggregate and long-request rules apply.

This design deliberately does not multiply pending Prefill by Decode count. The
linear multiplier has no established vLLM scheduler or GPU-capacity meaning.
The active/non-active regime protects Decode structurally while the single
Prefill owner bounds accumulation.

### 9.3 Required examples on c21 geometry

```text
12 replacement requests, each 1298 tokens
  post-admit pending = 15576 <= regular 32768
  expected: all fitting replacements admit, subject to hard KV

49K request with four active Decode users
  candidate = 50176 > regular 32768
  expected: request-scoped pre-forward protection

many small Prefills with active Decode
  cumulative pending cannot exceed 32768
  expected: no candidate-only accumulation bypass

650K request on an upstream whose model context and hard KV both fit
  quiescent class
  expected: only when Decode, waiting, and pending Prefill are all zero and hard
  KV fits; otherwise immediate request/load protection without a local queue
```

These are architecture expectations, not accepted behavior. Production-shaped
simulation and controlled GPU tests must decide whether the budget is Pareto-
safe. Do not weaken the threshold merely to make a required scenario green.

## 10. Decision, HTTP, and reporting contract

Policy returns one semantic result:

```text
admit
request_protect
load_protect
availability_protect
```

with one reason, manager sequence, and counterfactual snapshot. The adapter maps
all protect outcomes to immediate OpenAI-compatible 429 responses in enforce
mode. Shadow mode records the decision but creates no reservation and forwards.

The same immutable decision record must update:

- decision log and suppression counters;
- attempt, fit, risk, reject, reason, and scope metrics;
- last decision and last enforced reject state;
- current reservation and pending-work metrics;
- `/v1/upstream-status`; and
- Router-compatible running/waiting/global-limit projection.

Request-scoped protection must not close the node, because a smaller request may
fit. Load protection projects constrained capacity. Availability protection
projects unavailable. No reporting component may recompute policy from partial
fields or keep protection active after the owning manager sequence changes.
Remove the fixed 1,500-ms recent-reject projection hold: current inspection of
the latest observation and Manager state owns Router compatibility. Historical
reject time remains telemetry only and cannot delay recovery beyond fresh state.

## 11. Production-shaped deterministic simulator

The simulator is a contract and causality test, not a GPU oracle.

### 11.1 Required structure

- Use the production RequestCost builder from section 6.
- Represent clients as deterministic workers with per-worker request queues.
- A completion, cancellation, failure, or 429 releases that worker's next
  request at the same simulated time before the next observation poll.
- Define a stable tie-break order for poll, terminal, worker release, arrival,
  decision, and scheduler service; replay in alternate policy orders must be
  byte-identical.
- Keep running and waiting disjoint.
- Give Prefill and Decode service only to requests selected by the simulated
  scheduler; waiting requests receive neither service nor materialized KV.
- Exercise the real Manager lifecycle and reporting scope mapping.
- Separate structural assertions from synthetic goodput diagnostics.

### 11.2 Mandatory scenarios before versioning

1. twelve workers, two requests per worker, exact 1,298-input selection and
   1,024 requested output with the production rolling Decode horizon;
2. frozen old source reproduces two second-wave admits and ten cascading 429s;
3. new candidate produces 24/24 admission, no cascade, and exact final drain;
4. many individually fitting small Prefills stop at the Decode-active aggregate
   budget and later recover without low-flow self-lock;
5. 49K with four active Decode users is protected before forwarding;
6. weighted, exclusive, quiescent, waiting, stale, preemption, cancellation,
   disconnect, timeout, duplicate terminal, observation overlap, and epoch drift;
7. near-KV concurrent arrivals never exceed the hard limit;
8. no running/waiting double count and no service for waiting requests;
9. Manager state remains exact across all lifecycle transitions; if benchmark
   evidence requires cached aggregates, they equal the slow oracle;
10. decision outcome, HTTP scope, logs, metrics, status, and Router projection
    remain coherent for request, load, and availability protection.

Synthetic Prefill speed and Decode TPS constants may test deterministic
scheduler mechanics. They cannot prove real per-user TPS, model portability, or
GPU throughput.

## 12. Implementation boundaries and SOLID

Implement only vertical slices that reach the real pre-forward decision path.

- **Single responsibility:** estimator estimates; RequestCost builder normalizes;
  Manager owns mutable state; pure Gates decide; adapter maps; reporting reports.
- **Open/closed:** policy tests use interfaces and immutable inputs; adding a
  future Gate must not change lifecycle ownership.
- **Liskov:** for the same explicit snapshot and counterfactual, shadow and
  enforce call the same pure policy. Shadow deliberately creates no reservation,
  so later concurrent state differs; shadow evidence cannot prove enforce-mode
  lifecycle behavior.
- **Interface segregation:** Gates receive only fields they consume. Remove TPS,
  running, or other telemetry fields from Gate inputs when unused.
- **Dependency inversion:** HTTP and simulation depend on the canonical
  RequestCost builder and policy interfaces rather than duplicating formulas.

Efficiency gates on c21 workbench:

- supported maximum-body classification plus estimation p99 below 100 ms;
- pure policy plus Manager decision p99 below 100 microseconds at the tested
  production concurrency range;
- pure Gate evaluation has zero allocations and an admitted reservation has a
  small, measured, bounded allocation cost;
- decision latency is reported at 1, 48, 256, and 4,096 live reservations; and
- maps, retired state, log labels, and metric cardinality remain bounded.

The user accepts sub-100-ms extreme-input classification latency. Correctness
and bounded behavior take priority over optimizing the already-small lexical
hint.

## 13. Source and evidence workflow

All executable tests run in `pig-v0124-workbench` on c21 with:

```text
/usr/local/go/bin/go
/usr/local/go/bin/gofmt
```

For each coherent source update:

1. add focused red evidence against the exact old source or current failing
   commit;
2. implement the smallest architecture-consistent vertical slice;
3. run focused green and affected race tests;
4. inspect the diff and staged paths;
5. commit and push the coherent development update without assigning a release
   version; and
6. record source commit, commands, exit status, and material artifact hashes.

Do not leave a growing unpushed implementation merely to avoid a version bump.
A development commit is not a release identity. No image is built from a
focused-only or partially accepted commit.

## 14. Pre-version and release gates

### 14.1 Before assigning a version

Run on the exact pushed development commit:

- formatting and `git diff --check`;
- focused and affected tests;
- `go test ./...` and `go vet ./...`;
- targeted and full race tests;
- `go build ./...` and production binary build;
- canonical RequestCost parity tests;
- deterministic simulation twice plus byte comparison;
- policy-order replay;
- lifecycle/property/overflow/epoch tests;
- classification and hot-path benchmarks; and
- three recorded reviews:
  1. model, causality, and objective;
  2. safety, atomicity, lifecycle, and failure paths;
  3. SOLID, efficiency, evidence, and scope.

Any unresolved ownership overlap, RequestCost drift, lifecycle ambiguity,
unbounded state, low-flow lock, or required-scenario loss keeps versioning
blocked.

### 14.2 Versioned source

After pre-version acceptance, assign one next `0.12.x` identity across binary,
metrics, logs, simulator schema, tests, and evidence names. Commit and push.
Freeze an exact source archive and rerun the complete matrix plus independent
verification. Pre-version green is not inherited across executable identity
changes.

### 14.3 Image and c21 runtime

Only after versioned source acceptance:

1. build one local-only immutable image on c21;
2. verify image architecture, entrypoint, user, labels, binary identity,
   production contract, and zero-startup-inference behavior;
3. keep the image unpublished;
4. replace only PIG on c21 with `--no-deps`; and
5. prove vLLM container/image/start time, CVM, and GPU process are unchanged.

The first GPU workload is the unchanged sustained gate. Require:

```text
24/24 successful completions
0 request-scoped 429
0 other failures
0 preemptions
0 final reservations
0 final waiting
0 final Router backpressure
0 fatal signals
Decode TPS and goodput within the frozen acceptance contract
```

Failure stops all further GPU work and returns to architecture review.

A sustained green unlocks, in order:

- Decode-active many-small-Prefill accumulation;
- 49K with four Decode users;
- weighted, exclusive, and quiescent cases;
- low/no-flow and rejection recovery;
- cancellation, disconnect, timeout, stale, and epoch recovery;
- near-KV concurrency; and
- a completely new ordered Pareto matrix with independent audit.

Upload is allowed only for the exact image ID that passes every source, image,
GPU, Pareto, provenance, and independent-audit gate. Production Router enable,
canary, and 30-minute real-traffic observation remain a later, separately
authorized boundary.

## 15. Active checklist

- [x] v0.12.9 sustained red retained and classified as policy overprotection.
- [x] frozen `cc41264` focused cascade red retained and hashed.
- [x] whole-path source audit completed from classifier through reporting.
- [x] TPS confirmed as telemetry only, not current admission input.
- [x] HTTP/simulator RequestCost drift identified.
- [x] per-decision reservation scan identified as an unmeasured efficiency risk.
- [x] old state-total and candidate-only multiplier risks documented.
- [ ] this architecture plan passes three review passes, is committed alone,
  and is pushed while the three executable experiments remain unstaged.
- [ ] canonical RequestCost builder red/green and production/simulation parity.
- [ ] worker-driven replacement-wave simulator reproduces old red.
- [ ] unified PrefillQoSGate passes focused green; Manager scan benchmarks decide
  whether any aggregate optimization is justified.
- [ ] all mandatory production-shaped scenarios pass without low-flow lock.
- [ ] coherent development source is committed and pushed without version bump.
- [ ] complete pre-version matrix and three code reviews pass.
- [ ] one next 0.12.x identity is assigned and versioned source is accepted.
- [ ] one local image passes contract and c21 PIG-only runtime gates.
- [ ] sustained and targeted GPU gates pass.
- [ ] ordered Pareto matrix and independent audit pass.
- [ ] exact accepted image is uploaded.

## 16. Stop rules

- Do not version, build, deploy, or upload while an earlier checklist gate is
  open.
- Do not turn a request-size failure into a global node lock.
- Do not increase queue wait, add retry, add cooldown, or weaken thresholds to
  hide a failing scenario.
- Do not treat synthetic TPS as GPU evidence.
- Do not inherit source, image, or GPU evidence across executable changes.
- Do not modify Router, production Compose, vLLM, or another CVM in this goal.
- If the unified Prefill budget fails either work conservation or Decode QoS,
  stop and revise the architecture before writing another patch.

## 17. Plan review record

Pass 1, architecture and causality:

- traced the real path from bounded JSON classification through estimator,
  capability initialization, observer, Manager, Policy, HTTP, lifecycle, and
  reporting;
- confirmed TPS fields are telemetry only;
- rejected both the old state-total multiplier and the unbounded candidate-only
  multiplier;
- corrected the PIG container/image identity label and the c21 262K versus
  portable 512K/650K evidence boundary; and
- replaced a speculative O(1) Manager rewrite with a benchmark-first decision.

Pass 2, safety and lifecycle:

- fixed ownership of unknown Prefill state, rolling Decode horizon, observation
  assimilation, and exact-once terminal behavior;
- separated streaming first-byte phase evidence from conservative non-streaming
  behavior;
- clarified that shadow shares a pure policy but not enforce-mode reservation
  evolution; and
- removed the fixed 1,500-ms Router projection hold from the target design so a
  fresh current state, not historical rejection time, controls recovery.

Pass 3, SOLID, efficiency, evidence, and overdesign:

- reduced the active plan from a multi-version audit log to the current
  architecture, evidence, gates, and stop rules while preserving history in Git;
- separated plan, executable source, versioned source, image, c21 runtime, GPU,
  registry, and production evidence layers;
- required a shared RequestCost builder instead of duplicate formulas;
- required measured latency/allocation scaling before state-cache complexity;
  and
- corrected `cc41264` from a soon-stale branch HEAD claim to the last executable
  baseline, so the plan-only commit cannot inherit or invalidate code evidence.
