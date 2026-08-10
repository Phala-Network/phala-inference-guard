# PIG v0.12 Architecture-First QoS-Constrained Goodput Plan

Status: active architecture reset. This document is the only execution plan for
the current goal. Historical v0.12 plans, images, runtime results, and rejected
source experiments remain evidence only; they are not implementation authority.

The current executable development HEAD is `9c2cbc3`; the architecture-reset
plan is commit `021f146`. Later status-only or executable commits do not inherit
old evidence automatically. No next `0.12.x` version is assigned. No image may
be built, uploaded, or deployed until every pre-version gate in section 13
passes on one exact pushed commit.

After context compression, resume from sections 3, 8, 11, and 14. Re-read the
current Git status before inheriting any checklist item.

## 1. Objective and decision priority

PIG must maximize SLO-compliant completed-token goodput while:

- preventing admission-caused KV exhaustion and preemption;
- keeping long-window per-user Decode TPS acceptably stable;
- allowing occasional short TPS dips instead of treating every low sample as a
  failure;
- remaining work-conserving for every request that fits the current resource
  and QoS budgets; and
- making the authoritative admit/protect decision before forwarding.

The order is:

1. resource and lifecycle safety;
2. sustained Decode QoS, not instantaneous perfection;
3. maximum completed-token goodput among candidates satisfying 1 and 2;
4. bounded classification and decision overhead.

Backend feedback updates the next current-state prediction. It is not a retry,
cooldown, learned coefficient, delayed lock, or substitute for the pre-forward
counterfactual.

## 2. Scope and non-goals

This goal changes only PIG in the nested `phala-inference-guard` repository. It
does not add or change:

- routing or backend selection;
- cache or prefix-hit inspection;
- model-specific tokenizer assets or templates;
- request mutation, tiering, or priority injection;
- TTFT admission protection;
- online learning, active calibration, or a TPS feedback controller;
- retries, reject cooldowns, or a long local request queue;
- Router source, production Compose, vLLM, or another CVM.

The default mode is `enforce`; `shadow` is an explicit test-only setting. The
default observation interval is 500 ms. Production configuration should contain
only deployment-specific endpoints, authentication, and secrets unless an
all-or-none test override is deliberately selected.

## 3. Current truth and stop boundary

### 3.1 Authoritative environment

```text
CVM             c21b7281-2c25-4453-8a68-f39ec42d03b4
workbench       pig-v0124-workbench
repository      /workspace/src/phala-inference-guard-r3
branch                  codex/pig-v0.11.0-request-aware
executable HEAD         9c2cbc3
architecture plan       021f146
```

Do not run Go, race, simulation, benchmark, binary, or image gates on Windows.
Do not use the old builder. Do not restart the CVM or vLLM. A later accepted
runtime gate may replace only PIG on c21 with `--no-deps`.

### 3.2 Accepted evidence through `9c2cbc3`

- `7115dbe`: HTTP and simulator share the canonical `RequestCost` builder.
- `378584d`: raw vLLM phase facts, canonical probe scope, and current-capacity
  ownership are documented.
- `6d2c0e1`: production-shaped shared-worker simulator is available.
- `d93ea02`: Observer samples and reservation state are atomically owned by the
  Manager; Adapter decisions and Router inspection no longer read a separate
  Observer snapshot.
- `07666ef`: reservation state is the last coherent observation plus a
  positive-only live-request overlay; terminal events never subtract from the
  observed base or create completion credits.
- `9c2cbc3`: immutable capability schema v3 removes context `/8` and `/2`
  scaling, derives maximum admissible input and startup budgets, and fails
  closed when coherent vLLM model metadata is unavailable.
- Frozen old-policy simulator result: 24 arrivals, 14 admits, 10 size protects,
  14 completions, zero preemptions, and exact final drain.

The old-policy red report is:

```text
/workspace/evidence/pig-v012-worker-oldred-r1-378584d/report.json
SHA-256 a461a8923ae7d0f5f2954d41b9636725854ee72236bc8df52d1a73ce72e59b22
```

### 3.3 Rejected experiment archive

The uncommitted Gate/Manager experiment was archived and removed from the
worktree after `021f146`. The repository was then verified clean.

```text
/workspace/evidence/pig-v012-rejected-prearchitecture-gate-r1-021f146

tracked.patch SHA-256
  74138a2ecdf26637efcf716c0ebfbf551b3d99a506f4f5ce5b6d3126663f2134

prefill_qos_gate.go SHA-256
  29c2d5a0820185b77e4444f2b0d32551a8d1ce4df47034cb7f2700c37b08b301

prefill_qos_gate_test.go SHA-256
  271764b597e35caf80d972c01a78c0e3461a73bc9d8b346f4589fae182017e65
```

The Windows `tmp/pig-arch-source-r1` copy remains scratch evidence only and
must not be uploaded as source.

The running c21 PIG remains the rejected, unpublished local v0.12.9 image. No
source in this plan has been built or deployed.

### 3.4 Observation-contract source gate

The focused old path failed exactly because Adapter converted an unreadable
Observer snapshot into `metrics_stale`. At `d93ea02`, the same test admits from
the Manager-owned observation and creates one reservation. Validation on c21:

```text
go test ./internal/runtime/predictive \
  -run TestCurrentRequestAwareDecisionUsesAtomicallyPublishedObservation -count=1

go test ./internal/app/server \
  -run TestRequestAwareAdapterDecisionDoesNotReadObserverSnapshot -count=1

go test ./... -count=1
go test -race ./internal/runtime/predictive ./internal/app/server \
  ./internal/simulation/requestaware -count=1
go vet ./...
go build ./...

result: all green
```

This proves the observation ownership slice only. It does not accept the old
reservation assimilation model or the old admission policy.

### 3.5 Positive reservation overlay source gate

Before `07666ef`, the focused red test terminated an observed request and
incorrectly produced one retired reservation, one completion credit, and
virtual KV zero. At `07666ef`, terminal release deletes only the live
reservation: the stale observation remains 96 KV until the next coherent
sample, with no retired or negative-credit state.

Coverage is sequence-based rather than inferred from aggregate metric deltas:

- before a sample whose start follows Prefill completion, a live request adds
  its full conservative cost;
- after such a sample, it adds only future Decode KV/context;
- a sample overlapping Prefill completion remains conservative until the next
  500-ms sample; and
- EOF, cancellation, error, timeout, and duplicate terminal paths never
  rewrite the observed base.

Validation on c21 at `07666ef`:

```text
go test ./internal/runtime/predictive -count=1
go test ./internal/runtime/predictive ./internal/app/server \
  ./internal/simulation/requestaware -count=1
go test -race ./internal/runtime/predictive ./internal/app/server \
  ./internal/simulation/requestaware -count=1
go test ./... -count=1
go vet ./...
go build ./...

result: all green
```

The response lifecycle gate permits conservative rejection against the one
stale observation but requires immediate admission after the next coherent
idle observation. This is a maximum staleness boundary, not final policy
acceptance; later probe/policy and long-window goodput gates must recover useful
work without restoring negative credits.

Five-run decision benchmarks on c21 remained at zero allocations. New results
were about 0.23 us at 0 reservations, 0.33 us at 1, 3.57--3.63 us at 48,
18.3--18.7 us at 256, and 0.29--0.30 ms at the 4,096 stress bound. The common
`eaebfba` baseline was about 0.23 us, 3.46--3.55 us, and 17.7--18.1 us at
0/48/256. The small difference does not justify reintroducing cached aggregate
state.

### 3.6 Immutable capability profile source gate

At `9c2cbc3`, capability initialization is startup-only and model-neutral:

- vLLM metrics still provide model identity, exact KV capacity, and block size;
- `/v1/models` must provide the same unique model identity and a positive
  `max_model_len`; no 512K guess or metadata fallback remains;
- the maximum admissible input is the block-aligned minimum of model context
  and hard KV capacity, less the 256-token rolling Decode horizon;
- request-size semantics remain fixed at 64K/256K/512K while the upstream
  ceiling only controls reachability;
- contended and open aggregate budgets are respectively
  `min(64K, maximum_input)` and `min(256K, maximum_input)`; and
- the max-model-length plus four Prefill test overrides are all-or-none, while
  production defaults leave all five unset.

The c21 vLLM endpoint was read without changing runtime state and returned one
model, `google/gemma-4-31B-it`, with `max_model_len=262144`. Redirects,
non-success responses, oversized or malformed bodies, missing/negative model
length, identity mismatch, and ambiguous model lists all fail initialization in
tests rather than authorizing a guessed context.

Validation on c21 at `9c2cbc3`:

```text
go test ./internal/runtime/predictive ./internal/config/pigconfig \
  ./internal/observability/metrics ./internal/app/server \
  ./internal/simulation/requestaware -count=1

go test -race ./internal/runtime/predictive ./internal/config/pigconfig \
  ./internal/observability/metrics ./internal/app/server \
  ./internal/simulation/requestaware -count=1

go test ./... -count=1
go vet ./...
go build ./...

result: all green
```

The metadata request is bounded to 64 KiB and occurs only at initialization; it
adds no per-request hot-path work. Fixed size bands are classification semantics,
not instantaneous TPS gates. This slice does not accept the existing Prefill or
Decode policy: the long-window, work-conserving QoS behavior remains the next
pure-policy slice. No image was built, uploaded, or deployed, and the running
c21 PIG remains the rejected local v0.12.9 image.

## 4. Reflection: why repeated patch versions failed

The prior loop versioned local hypotheses before validating the state model:

1. one runtime symptom received a narrow formula or lifecycle patch;
2. focused source tests passed;
3. a patch version and image were created;
4. only then did a production-shaped workload test the architecture;
5. the next patch retained most of the same ownership and state assumptions.

That exposed missing Decode protection, phase double counting, stale terminal
accounting, EOF timing, an invalid state-total multiplier, and finally a 14/24
overprotection cascade.

The current source still has four architectural liabilities:

- the Adapter reads an Observer snapshot before entering the Manager lock, so
  observation fields and reservation state can come from different moments;
- the Manager tries to infer request ownership inside aggregate vLLM metrics
  and maintains assimilation plus retired negative credits;
- `waiting > 0` is being turned into an unconditional new-intake lock even when
  a small request still fits bounded contention budgets; and
- automatic Prefill limits divide context span by 8 and 2, which has no causal
  relationship to Prefill interference and produced unnecessarily strict
  32K/131K limits on c21.

The reset removes those assumptions instead of wrapping another Gate around
them.

## 5. End-to-end architecture

```text
bounded HTTP classification
  -> model-neutral lexical work estimate
  -> canonical RequestCost
  -> AdmissionController atomic transaction
       latest coherent observation
       + reservation overlay
       -> derived counterfactual
       -> ResourceSafetyGate
       -> PrefillQoSGate
       -> candidate/probe scope proof
       -> optional reservation
  -> immutable AdmissionDecisionRecord
  -> forward or immediate OpenAI-compatible 429
  -> lifecycle events update reservation
  -> Observer atomically replaces current observation
  -> CurrentCapacityRecord drives status and Router compatibility
```

### 5.1 Ownership

| Component | Owns | Must not own |
|---|---|---|
| Request classifier | supported paths, bounded body read, protocol validation, output horizon extraction | backend state or policy |
| Work estimator | model-neutral selection estimate and conservative KV input estimate | token IDs, cache state, admission |
| RequestCost builder | block-rounded input KV, rolling Decode horizon, manifest identity | HTTP, observations, policy |
| Capability initializer | immutable KV geometry, model context ceiling, reachable size bands | runtime learning or threshold mutation |
| Observer | raw coherent vLLM sample and sample watermarks | reservations, phase ownership, decisions |
| AdmissionController | latest observation, reservation lifecycle, one atomic decision/capacity transaction | HTTP mapping or telemetry formatting |
| State projector | pure `observation + overlay -> DerivedState` | mutation or policy |
| ResourceSafetyGate | post-admit hard KV fit | Prefill class or HTTP scope |
| PrefillQoSGate | contention regime, pending Prefill budget, long-request ownership | KV, lifecycle, telemetry |
| AdmissionPolicy | ordered pure Gate evaluation and canonical-probe scope proof | mutation or HTTP |
| Adapter | classification result mapping, forward/429, lifecycle guard | observation reads, reason-based scope guessing |
| Reporting | logs/metrics/status from immutable records | rerunning policy or holding stale rejects |
| Simulator | deterministic workers, scheduler, lifecycle, and objective replay using production contracts | synthetic GPU claims |

One mutable fact has one owner. Observer samples are pushed into the Controller;
the Adapter no longer passes a separately read `RequestAwareInput` into a
decision.

## 6. Request work and immutable capability

### 6.1 Request estimates

The estimator produces two independent quantities:

```text
selection_prefill_tokens
  fast model-neutral lexical estimate used for request-size differentiation

safety_input_tokens
  conservative input estimate used only for hard KV accounting
```

The current approximate lexical scan is intentionally model-neutral. It does no
vocabulary lookup, model asset load, template execution, FFI, network request,
or request mutation. Exact tokenizer parity is not required. Unsupported,
malformed, overflowing, unknown-length, or classifier-saturated inputs fail
before forwarding. Recognized multimodal inputs use the conservative estimate
for both quantities because URL length is not media expansion cost.

Without a model tokenizer, `safety_input_tokens` is a conservative forecast,
not a mathematical token-count upper bound. Safety claims therefore combine
that forecast with immutable KV headroom and controlled estimator-error tests.
Before versioning, report underestimation and overestimation separately for
multilingual prose, code, JSON/tool schemas, escape-heavy strings, high-entropy
or base64-like content, and maximum-body inputs. Any tested underestimation that
can consume the hard headroom blocks release; do not hide it by renaming the
point estimate an exact token count.

The canonical builder receives selection estimate, safety estimate, bounded
rolling Decode horizon, KV block size, and manifest epoch. It returns:

```text
input KV          = round_up(safety input, block)
total KV          = round_up(safety input + Decode horizon, block)
future KV         = total KV - input KV
active context    = safety input + Decode horizon
future context    = Decode horizon
Decode sequences  = 1
```

The default 256-token Decode horizon is rolling, not the declared full output
maximum. A fresh observation materializes generated KV while each live Decode
reservation retains the future horizon.

### 6.2 Startup-only adaptive profile

The immutable profile is initialized once per model identity epoch from vLLM:

- model identity and `max_model_len`;
- exact KV token capacity and block size;
- block-aligned hard KV limit;
- maximum admissible input after the default rolling horizon.

`max_model_len` has no silent 512K fallback. If coherent model metadata is not
available, enforce initialization remains unavailable unless a complete,
explicit capability override is supplied. A guessed context ceiling cannot
authorize a long request.

Prefill size semantics remain portable and understandable:

```text
regular       < 64K
weighted      64K .. <256K
exclusive     256K .. <512K
quiescent     >=512K
```

The numeric boundaries are block-aligned when materialized in a capability
profile. Their semantic values do not otherwise change with context length.

The upstream ceiling makes unsupported bands unreachable; it does not rescale
64K to `context/8` or 256K to `context/2`. Therefore a 262K upstream supports
regular, weighted, and a narrow exclusive band, while 512K/650K behavior must
be tested on an upstream that can actually fit it.

Initial budgets are:

```text
contended pending-Prefill budget = min(64K, maximum admissible input)
open aggregate Prefill budget    = min(256K, maximum admissible input)
```

KV geometry and reachability adapt at startup; Prefill thresholds do not learn
or mutate. Complete all-or-none overrides remain a test escape hatch. The hard
KV ratio remains the current conservative default until controlled GPU evidence
justifies one explicit design change; it is not searched online.

## 7. Atomic observation and reservation model

### 7.1 Observation contract

One coherent observation contains:

```text
identity epoch, observation sequence, observed time
sample start and finish Manager sequences
KV capacity, block size, used KV
raw running, raw waiting
generation delta and preemption delta
freshness and validity
```

`running` is not renamed Decode and `waiting` is not renamed executing Prefill.
They are raw scheduler facts. Identity/capacity drift, counter reset, or stale
age closes availability for the epoch.

The Observer calls `AdmissionController.UpdateObservation`. Publication and
reconciliation happen under the Controller lock as one operation. There is no
second Observer snapshot read in the request path.

An enforce-mode ownership epoch starts only after one coherent observation with
`running=0` and `waiting=0`. This proves that no unknown request from before a
PIG restart remains in vLLM. After ownership is established, every forwarded
request must have a live PIG reservation. A sample that cannot be reconciled
with that ingress contract becomes availability protection until a coherent
idle rebase; PIG must not invent the token cost of opaque upstream work. This is
a startup/epoch safety condition, not the normal policy for tracked waiting.

### 7.2 Reservation lifecycle

```text
Reserved -> ForwardedPrefill -> ActiveDecode -> Terminal
```

- admission and reservation creation are atomic;
- `MarkForwarded`, first response bytes, and terminal are monotonic/idempotent;
- streaming first bytes are the available Prefill-complete evidence;
- non-streaming responses remain Prefill-pending until body bytes or terminal;
- cancel, disconnect, error, timeout, expiry, epoch invalidation, and shutdown
  release exactly once.

The non-streaming rule is deliberately conservative and can reduce admission
while a long response is generated. Its goodput impact must be measured as a
separate mandatory workload. PIG may not guess an individual phase from an
aggregate generation counter or mutate a non-streaming request into streaming.

### 7.3 Conservative overlay, without inferred subtraction

Derived state is always the latest observed state plus positive reservation
charges. It never subtracts guessed request ownership from aggregate metrics.

```text
Reserved or ForwardedPrefill
  charge full RequestCost

ActiveDecode before a sample whose start follows first-byte sequence
  charge full RequestCost

ActiveDecode after such a sample
  charge only rolling future KV/context

Terminal
  remove reservation; a stale observation may temporarily overcount it until
  the next 500-ms sample, but no retired negative credit is applied
```

This may conservatively double count materializing Prefill KV for at most the
Prefill phase. It cannot undercount by falsely declaring a request absorbed.
The 500-ms observation and immediate lifecycle events bound normal recovery.

Pending Prefill work is the sum of selection estimates for all non-terminal
reservations that have not reached first bytes, including admitted requests not
yet forwarded. No O(n) cache is added until benchmarks show the simple scan
violates the decision budget.

## 8. Admission policy

### 8.1 Derived state

The Controller constructs one immutable pre-admit state:

```text
effective KV
pending Prefill tokens and class ownership
local ActiveDecode count
raw observed running/waiting
recent-sample generation/preemption evidence
capability profile and observation freshness
```

`contended` is true when any local Decode is active or the fresh observation
shows running, waiting, generation progress, or a preemption delta. It is a
regime selector, not a full-intake lock and not an exact Decode-user count.
This rule applies after the ownership epoch in section 7.1 is established.

### 8.2 Ordered decision

1. **Validity and availability**

   Invalid cost or request shape is request protection. Stale metrics, identity
   drift, closed epoch, or impossible state is availability protection.

2. **Hard resource safety**

   Protect when:

   ```text
   observed used KV + live reservation overlay + candidate total KV
     > immutable hard KV limit
   ```

   This uses `safety_input_tokens` and the rolling Decode horizon.

3. **Prefill QoS and size differentiation**

   In a contended state:

   - only regular candidates are eligible;
   - post-admit pending Prefill must stay within the contended budget;
   - `waiting > 0`, ambiguous running, or one fresh preemption does not by
     itself reject a fitting regular request.

   In an open state:

   - regular and weighted requests share the open aggregate budget;
   - an exclusive request requires no pending Prefill and becomes the sole
     long-Prefill owner;
   - a quiescent request requires no pending Prefill, local Decode, raw running,
     or raw waiting;
   - pending exclusive/quiescent work blocks new work until first bytes or
     terminal.

   A request above the upstream maximum admissible input is request protection.
   A preemption delta only selects the contended regime for its owning fresh
   sample; it creates no timer, cooldown, or learned state.

4. **Protection scope on the same snapshot**

   For a valid resource or QoS protection, evaluate the immutable canonical
   minimum-production probe against the same pre-admit state:

   ```text
   probe admits  -> request_protect
   probe rejects -> load_protect
   ```

   The probe uses a one-token selection/safety input, production rolling Decode
   horizon, and the same KV block rounding as real traffic. Availability and
   malformed request outcomes keep direct scope.

### 8.3 Work-conserving invariants

- one large rejected request cannot close capacity for a following small one;
- a historical reject cannot outlive its Manager sequence;
- waiting or ambiguous running may reduce the eligible class/budget but cannot
  unconditionally lock all minimum traffic;
- no empty-state cooldown, retry timer, or traffic-dependent unlock exists;
- current capacity reopens immediately when lifecycle or observation state
  makes the canonical probe fit.

### 8.4 Required examples

```text
12 workers x 2 requests, each about 1298 input tokens
  all 24 should fit the 64K contended pending budget and complete

49K candidate with active Decode
  regular and eligible if aggregate pending remains <=64K and hard KV fits

96K candidate with active Decode
  weighted and request-protected; a fitting minimum probe keeps node capacity
  open

many small Prefills with active Decode
  admit until cumulative pending reaches 64K, then protect without sticky lock

256K..512K request
  exclusive; only on an otherwise open Prefill state and no active contention

>512K request on a capable upstream
  quiescent; only when Decode, waiting, running, and pending Prefill are empty
```

These expectations are structural. Controlled GPU evidence decides whether the
static 64K/256K budgets satisfy the long-window QoS contract; thresholds are not
weakened merely to make one scenario green.

## 9. Decision, HTTP, reporting, and Router contract

Each request returns one immutable `AdmissionDecisionRecord`:

```text
outcome: admit | request_protect | load_protect | availability_protect
reason, manager sequence, observation sequence
candidate cost, derived pre-state, post-admit counterfactual
```

In enforce mode, protect outcomes map to immediate OpenAI-compatible 429. In
shadow mode the same pure policy is evaluated, no reservation is created, and
the request forwards. Shadow validates policy causality, not enforce lifecycle.

The decision record is the only source for request logs and decision counters.
Every enforced protection must be visible with reason and scope in both logs and
metrics.

Status and Router compatibility do not reuse the last request decision. The
Controller evaluates the canonical probe without reserving and emits one
immutable `CurrentCapacityRecord` with the owning Manager/observation sequences.

- request protection leaves the node open for smaller work;
- load protection advertises no additional capacity while retaining current
  running accounting;
- availability protection advertises unavailable;
- a new lifecycle or observation sequence recomputes capacity immediately.

Delete the fixed 1,500-ms recent-reject hold. Reject timestamps remain
telemetry only. Reporting may format records but may not rerun Gates, infer
scope from reason strings, or merge fields from different sequences.

## 10. QoS and goodput acceptance contract

Hard safety remains strict in every repetition:

- zero admission-caused preemptions, KV-limit violations, restart, or fatal;
- zero reservation leak, double release, impossible transition, or final
  running/waiting mismatch caused by PIG.

Decode QoS is intentionally statistical. A single low sample, p10 dip, warmup
transition, or request boundary does not reject a candidate.

For each controlled workload, freeze a matched shadow/no-enforcement reference
before comparing candidates. Use test-client lifecycle to determine active
Decode users and compute time-weighted per-user Decode TPS. Report p10, p50,
minimum, and every raw repetition, but accept QoS using sustained windows:

- candidate whole-run time-weighted mean is at least 85% of the matched
  reference in the median of at least three repetitions;
- for sustained runs containing at least ten valid 30-second rolling windows,
  no more than 10% of those windows are below 70% of the matched reference; and
- one isolated low window is diagnostic, while two or more consecutive low
  windows require causal review before promotion.

For workloads with fewer than ten valid rolling windows, use the whole
active-Decode interval plus the consecutive-window review; do not turn one
instantaneous sample into a hard gate.

Goodput is completed output tokens per wall-clock second. SLO-compliant goodput
counts a workload repetition when its sustained QoS contract passes; it does
not discard individual tokens because one instantaneous TPS sample was low.
Among safe, QoS-compliant candidates, select the highest median goodput. Raw
throughput, rejection count, GPU utilization, and p10 remain separate
diagnostics.

Run A/B and B/A order with at least three repetitions. Any hard-safety incident
fails the candidate. A noisy single QoS repetition does not automatically fail
when the median, rolling-window budget, and order check pass; retain and explain
all repetitions.

## 11. Deterministic simulation and required scenarios

The simulator is a structural causality test, not a GPU performance oracle. It
must use production `RequestCost`, Controller, Policy, lifecycle, decision
records, and capacity records.

Required scenarios before versioning:

1. 12 workers x 2 requests reproduce frozen old 14/24 and new 24/24 behavior.
2. Many small Prefills stop at 64K under contention and recover on lifecycle,
   with no no-flow or low-flow self-lock.
3. Waiting/ambiguous running still permit a fitting regular minimum request.
4. A 96K contended candidate is request-protected while the next 1K request
   admits on the same observation.
5. Weighted, exclusive, quiescent, and above-upstream-ceiling cases obey the
   class table and do not globally lock request-scoped failures.
6. Near-KV concurrent arrivals cannot exceed the hard counterfactual.
7. Observation publication and decision cannot mix sequences.
8. Enforce startup waits for one coherent idle ownership sample; after
   ownership, tracked waiting permits bounded regular admission while an
   irreconcilable opaque-work epoch fails availability rather than guessing.
9. Reservation overlay transitions full -> future only after first byte plus a
   covering sample; terminal never applies a negative observation credit.
10. Streaming, non-streaming, cancel, disconnect, timeout, upstream error,
   duplicate event, epoch drift, and shutdown release exactly once.
11. Stale metrics close availability and the first coherent sample reopens it
    without a timer.
12. Fresh preemption selects only its fresh contended state and creates no
    cooldown.
13. Request/load/availability outcomes are identical across HTTP, logs,
    metrics, status, and Router projection.
14. A last large reject cannot hold Router capacity after a Manager sequence
    change; no 1,500-ms hold remains.
15. Repeated simulation and alternate event-order replay are byte-identical.
16. Streaming and non-streaming workloads both drain exactly; non-streaming
    conservatism is reported rather than bypassed with inferred phase changes.
17. Estimator fixtures report point/safety error for natural text, code,
    multilingual, schema, escape-heavy, high-entropy, and maximum-body inputs.

Synthetic Prefill speed and Decode TPS can test mechanics but cannot prove GPU
QoS, model portability, or throughput.

## 12. SOLID and efficiency constraints

- **Single responsibility:** the Controller owns state and atomicity; pure
  projectors/Gates decide; Adapter maps; Reporting reports.
- **Open/closed:** adding a future Gate consumes immutable derived input and
  does not change lifecycle ownership.
- **Liskov:** shadow and enforce evaluate the same policy snapshot; only enforce
  reserves.
- **Interface segregation:** Gate inputs contain only fields they consume.
- **Dependency inversion:** HTTP and simulation depend on the same RequestCost,
  Controller, Policy, and record interfaces.

Do not add cached aggregate counters before measurement. Benchmark decisions at
1, 48, 256, and 4,096 live reservations. Keep the O(n) scan while repeated
256-reservation operations remain below 100 microseconds, the 4,096 stress
bound remains below 1 millisecond, and both allocate zero. These limits keep
the normal production path strict while remaining far inside the accepted
100-ms extreme-request budget. If either limit fails or scaling becomes
nonlinear, add a measured incremental aggregate behind a slow recomputation
oracle and prove equality across every lifecycle/observation transition.

Maximum-body classification plus estimation p99 must remain below 100 ms on
c21. Pure Gate evaluation should allocate zero; admitted reservation allocation
must be measured and bounded. Maps, labels, logs, and metrics must have bounded
cardinality.

## 13. Implementation and release gates

### 13.1 Architecture reset

1. Commit and push this plan alone.
2. Save and hash the current remote dirty patch as rejected evidence.
3. Restore only the known experimental paths to pushed `6d2c0e1`.
4. Confirm clean status before executable work.

### 13.2 Vertical slices

1. Observation/Controller contract: write red tests for atomic publication and
   prohibit Adapter-supplied observation input.
2. Reservation overlay: replace assimilation/retired subtraction with the
   positive overlay and sample barrier; pass lifecycle and race tests.
3. Capability profile: remove `/8` and `/2`; add maximum admissible input and
   reachable fixed bands.
4. Pure policy: implement ResourceSafetyGate, PrefillQoSGate, contention/open
   budgets, and atomic canonical-probe scope.
5. Adapter/reporting: consume decision/capacity records, expose every enforced
   protection, and remove reason-based scope plus recent-reject hold.
6. Simulator: exercise production contracts and all section 11 scenarios.
7. Benchmarks and full pre-version matrix.

Each slice starts with a focused red for the intended behavior, reaches the real
pre-forward path, passes focused and affected race gates on c21, receives a diff
review, and is committed/pushed without assigning a release version. Do not
accumulate a large unpushed implementation.

### 13.3 Before assigning a version

On one exact pushed development commit run:

- `gofmt`, `git diff --check`, focused tests, and affected race tests;
- `go test ./...`, `go vet ./...`, `go build ./...`, production binary build,
  and full race matrix;
- canonical RequestCost parity and estimator edge cases;
- deterministic simulator twice, byte comparison, and alternate order replay;
- lifecycle, property, overflow, epoch, scope, reporting, low/no-flow, and
  concurrency tests;
- maximum-body and 1/48/256/4096-reservation benchmarks; and
- three recorded reviews: causality/objective, safety/lifecycle, and
  SOLID/efficiency/evidence.

Any sequence drift, undercount, leak, scope mismatch, hidden protection,
low-flow lock, required-scenario failure, or unexplained benchmark regression
blocks versioning.

### 13.4 Version, image, and runtime boundaries

Only after pre-version acceptance:

1. assign one next `0.12.x` identity and push versioned source;
2. rerun the complete matrix on that exact identity;
3. build one local-only image on c21 and validate provenance/entrypoint/user;
4. replace only PIG with `--no-deps`, proving vLLM/CVM/GPU identity unchanged;
5. run the unchanged 24/24 sustained gate, then targeted accumulation, size,
   class, recovery, lifecycle, and near-KV GPU workloads;
6. run the ordered QoS/goodput Pareto matrix and independent audit;
7. upload only the exact image ID that passes every gate.

Production Router enable and 30-minute real-traffic observation remain a later,
separately authorized boundary.

## 14. Active checklist

- [x] v0.12.9 sustained 14/24 overprotection retained as rejected evidence.
- [x] canonical RequestCost builder shared by HTTP and simulator.
- [x] shared-worker old-policy red reproduced and hashed.
- [x] architecture re-reviewed after repeated patch failures.
- [x] sustained-window QoS replaces instantaneous low-value rejection.
- [x] waiting is defined as contention evidence, not unconditional full lock.
- [x] atomic Controller ownership and positive-only reservation overlay defined.
- [x] fixed portable size bands replace context `/8` and `/2` scaling.
- [x] architecture-reset plan committed and pushed alone as `021f146`.
- [x] dirty Gate/Manager experiment saved, hashed, and removed from worktree.
- [x] Observation/Controller slice passes focused/full/race/vet/build gates and
  is pushed as `d93ea02`.
- [x] positive reservation overlay slice passes lifecycle/race/full/vet/build
  gates and is pushed as `07666ef`; 1/48/256/4096 decision benchmarks allocate
  zero and remain inside the revised measured limits.
- [x] immutable capability/profile slice passes focused/full/race/vet/build
  gates and is pushed as `9c2cbc3`.
- [ ] pure policy implements resource safety, work-conserving Prefill QoS, and
  canonical-probe scope without instantaneous TPS rejection.
- [ ] decision/capacity reporting is coherent and recent-reject hold is removed.
- [ ] all deterministic scenarios pass without low-flow or request-scope lock.
- [ ] complete pre-version matrix and three code reviews pass.
- [ ] one next 0.12.x identity is assigned and accepted.
- [ ] one local image passes source/image/c21 PIG-only runtime gates.
- [ ] sustained and targeted GPU tests satisfy safety, long-window QoS, and
  goodput acceptance.
- [ ] ordered Pareto matrix and independent audit pass.
- [ ] exact accepted image is uploaded.

## 15. Three-pass review record

### Pass 1: model, causality, and objective

- replaced instantaneous/p10 failure semantics with sustained-window QoS;
- retained pre-forward prediction while limiting feedback to current facts;
- removed the arbitrary context `/8` and `/2` Prefill scaling;
- changed 49K-under-Decode from forced reject to a fitting regular example;
- kept TPS out of the runtime Gate and in controlled acceptance measurement.

### Pass 2: safety, state, and lifecycle

- moved observation publication into the Controller transaction;
- removed inferred metric ownership and retired negative credits from the
  target state model;
- defined a positive-only overlay and first-byte plus sample barrier;
- kept stale/identity/KV safety hard while allowing fitting regular work under
  waiting or ambiguous running;
- required exact-once release for every terminal path.

### Pass 3: SOLID, throughput, and evidence

- reduced policy to two resource owners and one atomic scope proof;
- separated per-request decisions from current-capacity reporting;
- removed delayed reject projection and low-flow unlock timers;
- retained O(n) recomputation until benchmarks justify complexity;
- separated plan, source, version, image, runtime, registry, and production
  evidence; and
- froze the current dirty experiment instead of bending the architecture to
  finish it.

## 16. Stop rules

- Do not complete the current dirty Gate experiment incrementally.
- Do not version, build, deploy, or upload while an earlier checklist gate is
  open.
- Do not make a request-size rejection globally close a node that can fit the
  canonical probe.
- Do not turn waiting, one preemption sample, or one low TPS point into a sticky
  full-intake lock.
- Do not add retry, cooldown, learning, cache inspection, or a local long queue
  to hide a failing scenario.
- Do not treat simulation TPS as GPU evidence.
- Do not inherit executable evidence across source changes.
- Do not modify Router, production Compose, vLLM, or another CVM in this goal.
- If the static 64K/256K policy cannot satisfy both sustained QoS and goodput,
  stop at architecture review instead of releasing another narrow patch.
