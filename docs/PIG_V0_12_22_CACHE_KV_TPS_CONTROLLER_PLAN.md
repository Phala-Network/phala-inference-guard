# PIG v0.12.22 Cache-Aware KV And TPS Controller Plan

Status: active design and evidence plan; no v0.12.22 version has been assigned.

Baseline:

- branch: `codex/pig-v0.12.21-legacy-vllm-kv`
- source and remote HEAD: `acb3fc0ca8a3341dc27a23a0d4da5ac0e0d71ecd`
- baseline release: PIG v0.12.21
- worktree state at plan creation: clean and synchronized with
  `pig-origin/codex/pig-v0.12.21-legacy-vllm-kv`

This document is the execution authority for the next PIG controller work. It
supersedes no historical release record. Source acceptance, image publication,
deployment, and live acceptance remain separate states.

## 1. Objective

Maximize sustained successful throughput while keeping long-run mean active
TPS near or above the configured reference and preventing material waiting,
preemption, OOM, restart, or reservation-lifecycle regressions.

The configured `PREDICTIVE_TPS_REFERENCE=25` remains a long-run service-quality
reference. It is not a requirement that every 500 ms sample or every individual
request remain at 25 TPS. Short dips and bounded exploration are acceptable
when the longer window remains healthy.

The work has two independent optimization questions:

1. Can the TPS controller recover healthy capacity faster without repeatedly
   spending the same measured output surplus or admitting through current
   scheduler distress?
2. Can PIG account for expected incremental KV more accurately for cache-heavy
   work without treating aggregate historical cache hits as proof that the next
   request will hit?

The questions are implemented, tested, and released as separate attributable
slices. Passing one never authorizes the other.

## 2. Review Of The Two Proposals

### 2.1 TPS reference proposal

Keeping the reference at 25 is correct for the stated QoS objective. The quoted
example does not itself show a TPS rejection:

```text
reference                 25
window aggregate TPS     100.7
derived sequence limit       4
current sequences            2
post-admit sequences         3
```

The request fits because `3 <= 4`. A rejection observed elsewhere can instead
come from waiting, preemption, warming, idle refill, invalid evidence, an active
QoS-budget lease, unobserved work, or a different snapshot. Therefore the first
required change is reason-specific observability, not a speculative increase in
the sequence limit.

The current controller is already bounded:

- the 60-second window derives ordinary capacity;
- a qualified current sample can recover one sequence beyond current running;
- one low-flow probe is allowed;
- rolling surplus can fund at most `base + 1`;
- only one QoS-budget lease may remain active.

Removing the one-lease rule or allowing unrestricted current-rate ramping would
double-spend historical headroom and is out of scope.

### 2.2 Cache-aware KV proposal

The reported asymmetry is real in the current source:

```text
Prefill compute = input tokens * bounded cold fraction
KV projection   = observed KV + local full-KV reservations + request TotalKVTokens
TotalKVTokens   = InputKVTokens + FutureKVTokens
```

Recent cache evidence can currently reduce Prefill compute by at most 75%, but
does not reduce KV projection, reservation, context/input validation, or the
long-input class.

This is conservative, but it is not automatically wrong. A reused prefix stays
resident and attached while the request runs. The unresolved question is which
part is already represented by backend-observed used KV and whether attaching a
new request to shared prefix blocks causes additional physical allocation. That
answer can differ by vLLM/SGLang metric and scheduler implementation.

The following shortcut is forbidden:

```text
request TotalKVTokens *= 1 - recent_global_cache_hit_fraction
```

Aggregate recent hit rate describes previous work, not the current prefix. It
can under-reserve a cold request immediately after hot traffic.

The only admissible future form, and only when a trustworthy request-scoped
cache signal exists before forwarding, is a two-part liability:

```text
hard request liability = full future Decode KV + conservative uncached input KV
cold contingency       = full input KV - conservative uncached input KV
```

The contingency remains atomically tracked. A workload-level hit rate cannot
risk-discount it because it cannot distinguish a cold next request from a hot
one. Stale, missing, reset, ambiguous, workload-only, or distressed observations
use full-cold KV. Exact maximum input/context limits, long-input classes, and
future Decode KV never receive cache credit.

## 3. Current Evidence And Its Limits

The read-only GLM5.2 Grafana snapshot collected on 2026-08-26 showed:

| Window | Signal | Observed value |
| --- | --- | --- |
| 1 hour | backend cache-hit mean | 63.27% |
| 1 hour | PIG cache-hit / credit | 0% / 0% |
| 1 hour | backend KV mean / max | 3.23% / 22% |
| 1 hour | PIG KV reservation ratio mean / max | 9.87% / 100% |
| 1 hour | waiting / preemption | 0 / 0 |
| 1 hour | PIG mean-active TPS | about 68.4 |
| 6 hours | PIG mean-active TPS | about 66.6 |
| 6 hours | running mean / max | 0.718 / 6 |
| 6 hours | effective limit mean / max | 3 / 14 |
| 6 hours | running/limit mean / max | 21.21% / 62.5% |
| 6 hours | waiting / preemption | 0 / 0 |
| 6 hours | enforced rejects | about 219 |

This is diagnostic evidence only:

- the deployed metrics did not expose usable
  `pig_predictive_tps_decisions_total` or
  `pig_predictive_admission_protections_total` series;
- `last_reject_info` proves that a `tps_reference/load` event occurred but does
  not provide a rejection frequency by subreason;
- deployed source/image or collection drift is therefore unresolved;
- aggregate reject counts cannot be attributed to TPS, KV, Prefill, or another
  gate;
- current source exports `pig_predictive_request_aware_pressure` as a constant
  zero, so that panel cannot be treated as measured combined pressure;
- no matched successful-completion counter is available, so raw generation and
  inverse TPOT remain proxies rather than causal goodput evidence.

The 500 ms observer cadence and the current 4,096-token per-delta minimum also
explain why low-rate cache-heavy traffic can remain invisible to PIG. Each
individual interval must currently contain enough evidence; qualified evidence
is not accumulated across consecutive sub-threshold deltas.

## 4. Falsifiable Hypotheses

### H1: TPS healthy recovery

After reason-specific metrics are available, a bounded recovery candidate is
eligible only when one comparable six-hour observation contains at least 100
enforced healthy TPS protections and they account for at least 10% of final
inference protections. Those decisions must be attributable to stale or sparse
capacity evidence while all of the following are true:

- waiting is zero;
- preemption delta is zero;
- backend/runtime identity and observation interval are valid;
- KV and Prefill gates have headroom;
- the recent qualified per-active-sequence rate has QoS headroom;
- no outstanding marginal QoS lease owns the same surplus.

Compared with v0.12.21 on identical deterministic workloads, the candidate must
reduce healthy-state TPS protections by at least 50% and improve successful
completion goodput by at least 5%, without lowering sufficiently long
mean-active TPS below 25, increasing waiting/preemption from zero, worsening a
nonzero waiting/preemption baseline, or allowing more than one bounded
exploration wave. Output-token goodput is secondary when it is not
success-linked.

If the new metrics show that healthy TPS protection is not material, H1 is
false and TPS behavior remains byte-for-byte unchanged.

### H2: bounded cache-aware incremental KV

After backend source and fixture validation, a cache-aware KV candidate is
eligible only if PIG can distinguish, before forwarding the current request,
already represented shared-prefix KV from new physical allocation with a
conservative, model-neutral and request-scoped contract.

Compared with v0.12.21 on identical KV-pressure simulations, the candidate must
increase successful cache-heavy completions by at least 5% while producing no
unsafe post-admit KV projection, no additional waiting/preemption/OOM/restart,
no cold-after-hot bypass, and no reservation leak or double release.

If vLLM or SGLang metrics cannot support that distinction before forwarding, H2
is false for that backend and full-KV reservation remains in force. High
aggregate cache hit rate alone can never make H2 pass. PIG will not add a prefix
index, duplicate the backend cache, or depend on Router routing state merely to
make this experiment possible.

## 5. Execution Phases

### Phase 0: observability contract, no admission behavior change

Inventory and reuse current status/log/metric fields first. Add no parallel
metric family when an existing family can carry the same bounded fact. Repair
the export/collection contract and add only the missing source and fixture
coverage for:

- cumulative admission decisions by final reason, scope, enforcement mode, TPS
  result, and bounded TPS subreason;
- success-linked terminal request counters, split only by bounded
  terminal/protocol categories; success-linked generated tokens are exposed
  only when response usage makes them attributable, and otherwise remain
  explicitly unavailable rather than being inferred from raw backend
  generation;
- TPS base limit, current-rate candidate limit, QoS-budget candidate limit,
  selected limit, current/post-admit sequences, active lease count, and whether
  the request fit before the marginal budget path;
- cache evidence state: raw deltas, accumulation span, evidence tokens, hit and
  credit fraction, finite credit budget, spent/refunded tokens, and invalidation
  reason;
- KV accounting split: observed backend KV, full request input KV, future Decode
  KV, expected incremental input KV, cold contingency, local reservation, and
  post-admit projection;
- a real request-aware pressure contract, or removal/renaming of the constant
  zero metric and its misleading documentation.

Metrics must remain bounded-cardinality. No request body, prompt, API token,
prefix hash, customer ID, model-specific tokenizer output, or reservation ID is
exported as a label. Logs may carry numeric decision fields but no body or
secret.

Phase 0 must prove that metrics, logs, status, and the returned admission result
agree for admit/protect/invalid paths. It does not change a decision.

### Phase 1: focused TPS red tests and candidate

Before production code, add tests that fail specifically on the selected H1
behavior:

- the Redpill example admits at limit 4 and is not mislabeled a rejection;
- a healthy low-flow burst never exceeds one atomic exploration wave from one
  observation;
- multiple concurrent requests on the same snapshot cannot double-spend the
  current-rate step or rolling surplus;
- idle and warming recover at the 500 ms cadence without a hidden one-second
  hold;
- stale current-rate evidence falls back to the long window;
- waiting or current preemption immediately prevents exploration;
- an active marginal lease prevents a replacement lease until lifecycle
  reconciliation releases or transfers the debt;
- cancellation, error, disconnect, success, timeout, and epoch reset preserve
  exact ownership and never leak or double release;
- occasional sub-reference samples are allowed, while the deterministic long
  window remains at or above the reference;
- a low-utilization, waiting-free workload does not sustain TPS protection as
  the dominant final reason.

Implement only the smallest policy needed for the red failure. Candidate limit
selection stays separate from reservation ownership and QoS debt accounting.
Do not add a public tuning knob until a stable internal policy passes the full
matrix.

### Phase 2: cache evidence accumulation, Prefill semantics unchanged

Test a bounded accumulator for consecutive coherent deltas that individually
fall below 4,096 tokens:

- accumulation spans no more than the declared short lifetime;
- a qualifying total produces the same bounded Prefill credit as one coherent
  equivalent delta;
- a counter rollback, runtime epoch change, adapter change, invalid ratio,
  stale interval, waiting, or preemption clears or suppresses the evidence;
- zero-delta polls do not extend evidence indefinitely;
- credit expenditure and refund remain atomic and bounded by observed hit
  tokens;
- a hot interval followed by cold deltas lowers and then removes credit rather
  than preserving the old fraction;
- multiple admissions cannot spend more credit than the accumulated budget.

This phase changes evidence formation only. It does not reduce KV liability and
must keep all existing Prefill, input, class, and reservation tests green.

### Phase 3: backend KV/cache semantic proof

Audit exact supported vLLM and SGLang source/metric contracts:

- whether reported used KV includes retained reusable prefix blocks;
- whether a cache-hit request attaches to existing physical blocks or allocates
  a duplicate logical/physical reservation;
- when shared blocks become reclaimable;
- how block reference counts, copy-on-write suffixes, chunked Prefill, DP/TP/PP,
  retraction/preemption, and backend restart affect accounting;
- whether token-level cache counters can be aligned closely enough with the KV
  occupancy observation and the current request to support conservative
  incremental accounting before forwarding.

Any request-scoped signal must have explicit provenance, authentication,
normalization, epoch, age, and cancellation semantics. An untrusted client
header, Router hint without a separately verified contract, response arriving
after forwarding, or first-byte event is not capacity evidence.

Record source commits and fixture provenance in this document. Aggregate-only,
unsupported, late, or ambiguous evidence closes H2 without behavior code and
falls back to full-KV reservation.

### Phase 4: bounded cache-aware KV red tests and candidate

Only after Phase 3 passes, add red tests for:

- a credible hot workload can spend a finite incremental-input KV allowance;
- a request without trustworthy request-scoped hit evidence immediately after
  hot traffic receives full input KV charge;
- future Decode KV is always charged in full;
- maximum input/context and long-input class never change;
- extreme 256K/512K/650K+ inputs cannot consume a general cache-risk allowance;
- same-snapshot concurrent requests atomically share one finite contingency
  budget and cannot overbook it;
- backend waiting, preemption/retraction, stale evidence, invalid counters,
  counter rollback, epoch reset, or identity/config drift disable the allowance;
- reserve, forward, first byte, success, cancel, disconnect, error, timeout, and
  residual-debt reconciliation preserve exact KV ownership;
- observed KV growth consumes or reconciles contingency once, without a gap or
  double charge;
- first byte and wall-clock age alone never release input KV; only coherent
  backend coverage reconciliation may replace the full contribution;
- vLLM fixtures cannot satisfy SGLang semantics and vice versa.

Expected incremental KV and cold contingency are explicit value objects only if
Phase 3 establishes a request-scoped signal. The KV gate consumes a projection;
it does not own backend metric parsing. The reservation ledger owns lifecycle
state; it does not infer cache probability.

## 6. Safety Invariants

Every phase must preserve:

1. All admission checks and reservation mutations occur in one controller
   transaction.
2. Every admitted inference request has one reservation before forwarding.
3. Every lifecycle transition replaces the old contribution atomically.
4. Every terminal path releases or carries residual debt exactly once.
5. Backend epoch/reset invalidates stale observations and reconciles or
   invalidates liabilities only through the existing atomic rebind contract.
6. Waiting, preemption/retraction, invalid capacity, or stale identity fail back
   to the conservative behavior.
7. Context/input limits, long-input classes, Prefill exclusivity, future Decode
   KV, and hard KV capacity are never relaxed by global cache history.
8. No controller decision depends on a future feedback sample. Feedback only
   improves the next pre-forward estimate.
9. The default 500 ms polling interval remains effective; no new implicit
   one-second admission hold is introduced.
10. Disabled TPS reference preserves the existing disabled semantics.
11. No cache signal changes authentication, route policy, request body, response
    streaming, or backend protocol behavior.
12. Backend version/adapter ambiguity disables incremental KV accounting without
    making an otherwise healthy backend unavailable.

## 7. Efficiency And SOLID Boundaries

The hot path must remain allocation-bounded and independent of backend network
calls. Counter accumulation is O(1) per observation and admission projection is
O(1) per request.

Responsibilities remain narrow:

- backend adapters parse and validate backend-specific metrics;
- observation state derives coherent short-lived evidence;
- TPS policy selects a limit from immutable projected state;
- cache/KV risk policy returns explicit accounting, without mutating ledgers;
- the admission controller performs the atomic decision and reservation update;
- reservation lifecycle owns contribution reconciliation;
- observability renders already-computed facts.

Do not introduce interfaces merely for naming. Add an abstraction only when it
supports at least two real policies or isolates backend-specific semantics from
admission. Avoid public configuration for experimental constants.

Acceptance benchmarks in the approved remote environment:

- median `ns/op` and `B/op` across five small-request admission benchmark runs
  regress by no more than 5% versus the exact baseline, with both raw series
  retained; a noisier result is inconclusive rather than passing;
- accepted 4-MiB classification/admission p99 remains below 100 ms;
- no unbounded allocation growth under mixed small/large concurrency;
- race detector, deterministic simulations, and lifecycle property tests pass.

## 8. Evidence And Release Gates

Executable Go tests, race tests, simulations, and benchmarks must run only on an
approved nonproduction remote builder or CVM. Do not use a production serving
node as a workbench. The exact source commit must pass:

```text
git diff --check
gofmt check
focused admission/metrics/backend tests
go test ./...
go test -race ./...
go vet ./...
go build ./...
deterministic TPS and request-aware simulations
hot-path and 4-MiB request benchmarks
```

For each behavior slice, record:

- exact commit and dirty-state proof;
- exact remote environment and toolchain;
- focused red command/output proving the intended baseline failure;
- green focused and full-matrix output hashes;
- before/after simulation inputs and results;
- observability contract fixtures;
- decision: reject, retain experiment, or accept source.

Release states are independent:

1. plan accepted;
2. red test accepted;
3. source green;
4. full remote matrix green;
5. source committed and pushed;
6. version assigned;
7. image built and registry digest verified;
8. deployment separately authorized;
9. live canary and matched observation accepted.

This plan currently authorizes work through source commit/push. It does not
authorize image publication, production deployment, restart, Router mutation,
or synthetic production traffic. No v0.12.22 version is assigned until source
acceptance, and any later version remains in the `0.12.x` line.

## 9. Plan Review Record

Three reviews are mandatory before behavior code:

1. model and causality review: complete. The review checked the Redpill
   arithmetic against `tps_gate.go`, distinguished aggregate from per-request
   cache evidence, and checked whether the stated throughput objective had a
   success-linked measure. It corrected the plan so aggregate cache telemetry
   cannot authorize KV discount, made H2 conditional on a pre-forward
   request-scoped signal, and added terminal success/goodput observability;
2. safety and lifecycle review: complete. The review covered reserve/forward/
   first-byte/terminal/residual-debt paths, epoch rebind, same-snapshot
   concurrency, streaming visibility, and provenance of any future cache hint.
   It corrected the plan so first byte or time cannot release KV, success-linked
   tokens remain unavailable when protocol usage is absent, request hints must
   be authenticated and epoch-bound, and adapter ambiguity falls back to full
   KV without closing a healthy backend;
3. evidence, SOLID, efficiency, and release-scope review: complete. The review
   checked for duplicate observability, unbounded labels, subjective promotion
   language, hot-path complexity, test-environment authority, version claims,
   and separation of source/image/deployment states. It changed Phase 0 to
   inventory and repair existing metrics before adding fields, added numeric H1
   and H2 entry/promotion thresholds, defined the benchmark regression gate,
   and retained the explicit no-image/no-deploy boundary.

Each review must record its finding and the resulting document correction. A
review that finds no issue must state which assumptions and failure modes were
checked rather than only saying "passed".

## 10. Current Execution State

- baseline and source audit: complete;
- live read-only diagnostic audit: complete, but reason attribution is missing;
- focused plan draft: complete;
- plan review 1, model and causality: complete;
- plan review 2, safety and lifecycle: complete;
- plan review 3, evidence/SOLID/efficiency/release scope: complete;
- all mandatory plan reviews: complete;
- Phase 0 source audit: complete. Existing bounded admission protection, TPS
  decision, and successful completion-token counters are already present in the
  current source; their absence in the inspected live series is deployed-image
  or collection drift, not authorization to duplicate them;
- Phase 0 focused red test: passed as a valid red test on builder CVM
  `4f167f6e-4c50-415f-99f2-94b65652beba`, app
  `ff40ee31b95e89ebb242c223514adc715ac8a301`, using pinned Go image
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
  Exact pushed red-test commit `472c831fd70c09f775c780da9086a4d7cfc3d58a`
  failed only because `pig_predictive_request_aware_pressure` remained in
  production output; focused test exit was 1;
- Phase 0 production source: the synthetic constant-zero request-aware pressure
  metric has been removed. Exact green source commit
  `e9fdb709f0ae7d93005826ab388154c8014ce23a` passed focused and metrics-package
  tests with output SHA-256
  `d8a502e6f41450fa87aa5a5d643b4c89f163db6994cd7225d954d3b7ecc63523`
  and `d19fcff70f4c788429f6494638b9a0440622640702ab8226ca5679ef0f1b19d1`;
  formatting output was empty;
- Phase 0 exact-commit full remote matrix: complete. `go test ./...` output
  SHA-256 was
  `34e9d0b2791991dc7dc535dd3908285bdd45923069306e9dd5566c2c3c4103c8`,
  race output SHA-256 was
  `f985b96fd6debd6aae5f58c0e721053ff92147fbc255905e2ae37d4de4ce1a10`,
  and `go vet ./...` plus `go build ./...` both produced the empty-output
  SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- Phase 1 TPS behavior entrance gate: pending a comparable six-hour observation
  from a source/image that actually exports the existing reason-specific
  counters. Without deployment authorization, H1 is not yet eligible and TPS
  behavior remains unchanged;
- Phase 2 focused cache-accumulation red test: authored; remote red evidence
  pending. It requires two coherent 2,048-token deltas within one second to
  qualify the existing 4,096-token evidence minimum while preserving full KV;
- TPS behavior: unchanged;
- cache-aware KV behavior: unchanged;
- remote test environment: not yet identified as approved nonproduction;
- image/deployment: not authorized and not started.
