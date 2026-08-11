# PIG v0.12 Clean Admission Rebuild Plan

Status: active architecture and execution authority. No next `0.12.x` version
is assigned. `PIG-v0.12.10` and its local image are diagnostic evidence only;
they are not releasable.

This plan supersedes implementation instructions in
`PIG_V0_12_3_QOS_CONSTRAINED_GOODPUT_REDESIGN_PLAN.md`. That document remains a
historical evidence ledger. After context compression, resume from sections 2,
5, 9, and 12 of this file and re-check the authoritative Git state before doing
any work.

## 1. Objective and non-goals

PIG must maximize completed-output-token goodput subject to:

1. no violation of the declared KV counterfactual or reservation lifecycle,
   and no observed admission-caused KV exhaustion in acceptance testing;
2. acceptable sustained per-user Decode TPS, while allowing isolated short
   dips;
3. minimized sustained preemption frequency, without sticky protection from a
   single historical event; and
4. bounded request-classification and decision overhead.

The authoritative decision occurs before forwarding. Backend metrics are the
next current-state observation, not delayed feedback control and not learning.

This rebuild does not add routing, backend selection, prefix/cache lookup,
model-specific tokenizer assets, exact tokenizer RPCs, TTFT admission, online
learning, request mutation, tiering, priority injection, retry cooldowns, or a
long local queue. PIG remains model-neutral and vLLM-backed for this release.

Defaults remain:

- enforce mode; shadow is explicit and test-only;
- 500-ms observation interval;
- startup-only capability discovery; and
- minimal production configuration, with complete explicit overrides allowed
  only for tests or exceptional deployments.

## 2. Authoritative state and boundaries

The authoritative source repository is the c21 workbench checkout. The audit
baseline was:

```text
repo    /workspace/src/phala-inference-guard-r3
branch  codex/pig-v0.11.0-request-aware
HEAD    3533e7c4f18ec5b27a276da5c49a157e4c521ccd
```

At the start of this rebuild the worktree and index were clean and matched the
pushed branch. The exact current baseline passed on c21 with:

```text
/usr/local/go/bin/go test ./...
```

This baseline proves only that the current implementation satisfies its current
tests. It does not accept its architecture or release behavior.

All Go, race, simulation, benchmark, build, and image work runs in c21. Windows
is limited to inspection, editing, Git diff review, and transfer. Do not use the
old builder. Do not restart the CVM or vLLM for ordinary iteration; replace only
PIG when runtime testing is authorized by the later gates.

Every coherent source or documentation update is committed and pushed. An
image is built only after the complete pre-image source gate passes, remains
local through runtime acceptance, and is uploaded only after all acceptance
gates pass. Router, production traffic, other CVMs, and production Compose are
outside this goal.

## 3. Architecture audit and rebuild decision

The current predictive production path is not retained as a base for further
field-level fixes. The audit found these structural defects:

1. `manager.go` and `request_aware_manager.go` jointly own observation,
   reservation, derived state, scope, and transaction behavior. Four similar
   decision APIs expose different combinations of external/current observation
   and reserve/no-reserve semantics.
2. `RequestCostInput` receives selection and safety token estimates, but
   `RequestCost` discards the selection estimate. HTTP and the simulator pass a
   second integer beside the supposedly canonical cost.
3. `VirtualStateInterval` always has identical lower and upper values;
   physical and active KV are always equal; confidence is always one; and
   several context/prefill fields are algebraic duplicates. These abstractions
   add branches and invariants without representing current uncertainty.
4. `requestAwarePredictiveAdapter` owns mode, transaction orchestration,
   close behavior, lifecycle handles, counters, last-decision state, log
   throttling, Router compatibility inspection, transition history, and HTTP
   reason mapping.
5. The Observer and Manager retain separate copies of the same observation.
   Admission reads the Manager copy while metrics/status read the Observer copy.
6. A metrics/status scrape reevaluates capacity and mutates Router activation
   state inside the Adapter. Reporting therefore has business-orchestration
   responsibilities.
7. The production default is enforce, but core interfaces and fields are still
   named `predictiveShadow*`. Shadow currently does not reserve admitted
   requests, so burst behavior is not an enforce-equivalent hypothetical.
8. Reservation handles identify only a request ID. A stale handle is not fenced
   by an epoch and reservation generation if an ID is reused.
9. The simulator names the current candidate `v0.12.10`, and many tests mutate
   Adapter private fields. Those tests protect implementation shape and version
   identity instead of only public behavior.
10. `ResourceSafetyGate` mixes observation validity, context plausibility, and
    KV fit. The current loose KV estimate is also used as a context ceiling,
    causing a proven false-reject path.

The retained concepts are sound, but the ownership model is not. The rebuild
therefore keeps only the independently useful kernels and replaces the stateful
transaction and application boundary.

### 3.1 Keep, rewrite, and delete

| Area | Decision | Required result |
|---|---|---|
| bounded HTTP body preservation and protocol classification | keep, then rename only where needed | request bytes and headers remain unchanged |
| model-neutral lexical estimator | keep the bounded scanner; correct sampling and reservation semantics behind tests | no model asset, RPC, FFI, or learning |
| vLLM startup probe, metadata lookup, Prometheus parser | keep with narrow interfaces | immutable identity, context, KV capacity, and block size |
| capability profile | keep the startup-only concept; simplify its consumers | no runtime threshold mutation |
| positive-only reservation overlay and sample watermarks | preserve the invariants, rewrite the owner | one atomic Controller owns them |
| context, KV, and Prefill decisions | rewrite as three independent pure Gates | each input contains only consumed fields |
| `Manager` plus request-aware Manager extension | delete after replacement | one `AdmissionController` API |
| request-aware Adapter | delete after replacement | thin HTTP mapping plus application facade |
| duplicate Observer decision snapshot | delete | Controller snapshot is the admission truth |
| `VirtualStateInterval`, duplicate KV fields, constant confidence, duplicate cost fields | delete | explicit minimal request and state records |
| `predictiveShadow*` production naming | delete | neutral admission names; shadow exists only as an HTTP mode |
| version-named current simulator policy | delete | `candidate`; frozen old baselines keep explicit historical names |
| Router metric wire names | retain as compatibility output only | pure projection cannot affect admission |

## 4. Target architecture

```text
HTTP request
  -> RequestClassifier
  -> FastWorkEstimator
  -> RequestWorkBuilder
  -> AdmissionController.Admit          one lock / one transaction
       current coherent Observation
       + live Reservation overlay
       -> StateProjector
       -> AdmissionPolicy
            ContextGate
            KVGate
            PrefillGate
            canonical minimum-probe scope
       -> immutable DecisionRecord
       -> reservation with epoch + generation token when admitted
  -> HTTPAdmissionAdapter
       enforce: reject protected decisions
       shadow: forward protected decisions
       both modes: track lifecycle for policy-admitted requests
  -> upstream
  -> forwarded / first byte / terminal lifecycle events

VLLMObserver
  -> AdmissionController.PublishObservation

AdmissionController.Snapshot
  -> AdmissionReporter
  -> logs, PIG metrics, status, and pure Router compatibility projection
```

### 4.1 Responsibilities

`RequestClassifier` owns path and protocol validation, the bounded body read,
body restoration, output-horizon extraction, and timing. It knows no backend
state or policy.

`FastWorkEstimator` produces a model-neutral point estimate and an explicit KV
reservation estimate. It knows no capacity, request ID, admission mode, or
backend metrics.

`RequestWorkBuilder` validates and block-rounds one complete immutable request
record. Selection input tokens are part of the record and are never passed as a
parallel integer.

`AdmissionController` is the only mutable owner of the current observation,
epoch, event sequence, reservation generation, reservation map, and atomic
check-plus-reserve transaction. It owns no HTTP, logs, Prometheus formatting,
or Router wire names.

The Controller derives an absolute live-reservation bound from the immutable
hard KV limit and block size. This bound is a corruption/resource-exhaustion
guard, not the normal request-count admission algorithm. Admission still uses
KV and Prefill work. Counter overflow, reaching an impossible bound, or an
invalid internal transition closes availability instead of wrapping, evicting,
or guessing.

`StateProjector` is a pure fold of observation plus reservations. It performs
no policy decision and no mutation.

`AdmissionPolicy` is pure. It evaluates the three Gates in order and evaluates
the canonical minimum request against the same immutable pre-admit state to
derive request/load scope.

`ReservationHandle` carries controller epoch, request ID, and reservation
generation. Forwarded, first-byte, and terminal transitions are monotonic and
idempotent. A stale handle can never mutate a newer reservation with a reused
request ID.

`HTTPAdmissionAdapter` maps protocol results and immutable decisions to forward
or OpenAI-compatible 429 behavior. It does not read observations or infer
scope.

`AdmissionReporter` owns counters, last records, log suppression, and optional
reporting-only transition history. A reporter failure cannot change admission.
Reading business state is immutable; any reporting history is isolated from the
Controller and cannot be read by policy.

`RouterCompatibilityView` is a pure compatibility projection from the current
capacity record. It preserves required `pig_dynamic_*` fields without making
PIG a router.

SOLID does not mean one interface or wrapper per function. Concrete types are
the default inside a package. Introduce an interface only at an actual I/O,
clock, Controller, or reporting test seam; do not retain a forwarding wrapper,
type alias, or compatibility method solely to keep an old test compiling. The
final production decision surface is intentionally small:

```text
Controller: Admit, PublishObservation, StartSampleWindow, Snapshot, Close
ReservationHandle: MarkForwarded, MarkFirstByte, Terminate
HTTP/runtime boundary: Decide, Snapshot, Close
```

Lifecycle mutation remains reachable only through fenced handles, even if its
internal Controller methods are separate. No public fake-request inspection,
external-observation decision, or reserve flag is added.

## 5. Minimal domain model and algorithm

### 5.1 Request work

The canonical request record contains only:

```text
manifest identity
selection input tokens
KV-reservation input tokens
rolling Decode horizon tokens
block-rounded total KV tokens
block-rounded future Decode KV tokens
```

The selection estimate chooses the portable request-size class and performs a
context plausibility check. The KV-reservation estimate is a risk margin for
capacity accounting; it is not an exact tokenizer result and is not reused as
an exact context ceiling.

There is no model-neutral mathematical token upper bound smaller than a very
loose byte-level bound for every possible tokenizer. The Controller can prove
that its declared estimated KV counterfactual never exceeds the configured
limit; it cannot turn a model-neutral estimate into exact backend tokenization.
Release safety therefore requires all three layers together: deterministic
Controller accounting, fixed startup headroom, and estimator/GPU error evidence.
No document or metric may shorten that claim to "exact KV safety."

The current whole-body `bytes/2` upper estimate is not accepted automatically
as the new reservation formula: the c21 oracle showed roughly 3x actual tokens
for representative long text, which is throughput-hostile. Before the Builder
is accepted, a fixed offline estimator matrix must select and document one
model-neutral bounded formula. The formula must:

- use robust fixed-window evidence so one small prefix cannot dominate a long
  string;
- explicitly detect escape-heavy and high-entropy/unbroken shapes rather than
  assuming every alphanumeric run is natural prose;
- retain conservative multimodal fallback because URL length is not media
  expansion cost;
- use a fixed margin and startup KV headroom, not online learning; and
- report underestimation and overestimation separately across natural text,
  code, schemas, multilingual/CJK, escapes, high entropy/base64, and maximum
  body size.

The candidate formulas and acceptance thresholds are registered before the
matrix runs. One bounded comparison selects the lowest-overreservation fixed
formula whose observed underestimation stays inside the reserved estimator plus
KV-headroom envelope for every registered fixture. A failed matrix changes the
formula or closes unsupported shapes; it does not start an open-ended tuning
loop. No runtime calibration, per-model state, or hash-pinned asset is
introduced. Results from one exact tokenizer are target evidence, not proof for
every model.

### 5.2 Immutable capability

At startup PIG obtains and freezes:

```text
model identity hash
max_model_len
KV capacity tokens
KV block size
block-aligned hard KV limit
maximum plausible input after the rolling Decode horizon
reachable portable Prefill bands
```

The semantic size bands remain understandable and portable:

```text
regular       <64K
weighted      64K .. <256K
exclusive     256K .. <512K
quiescent     >=512K
```

The upstream context ceiling makes bands unreachable; it does not rescale them
to arbitrary fractions of context length. Initial contended and open aggregate
budgets remain `min(64K, maximum input)` and `min(256K, maximum input)` until
controlled c21 evidence justifies one explicit source change. Prefill and KV
parameters do not learn or mutate after initialization.

### 5.3 Observation and projection

One coherent observation contains:

```text
identity epoch and observation sequence
sample start and finish Controller event sequences
observed time and maximum age
KV capacity, block size, and used KV
raw running and waiting
generation delta for telemetry and preemption delta for one-sample regime evidence
```

The Controller rejects stale, invalid, regressed, or identity-drifted
observations. A healthy busy backend can initialize; it does not require an idle
sample. The projected state contains effective KV, known pending Prefill tokens,
pending size-class ownership, local active Decode count, and raw backend
running/waiting.

A transient fetch or invalid non-drift sample leaves the last coherent
observation unchanged; normal age calculation then closes availability when it
becomes stale. Confirmed model identity, KV capacity, block-size, counter-reset,
or sequence drift invalidates the Controller epoch immediately and permanently
for that process. It is not silently rebased onto a different backend identity.

No guessed ownership is subtracted from vLLM aggregates. Reservations contribute
only positive overlay:

```text
admitted or forwarded Prefill       full request KV + pending selection work
first byte without covering sample  full request KV, no longer pending Prefill
first byte plus covering sample     future Decode KV only
terminal                            reservation removed
```

A stale observation can temporarily overcount completed work until the next
500-ms sample. No negative credit, cooldown, or timer is created.

### 5.4 Ordered pure policy

1. Controller availability: coherent identity and fresh observation.
2. ContextGate: selection estimate is within the upstream plausible input.
3. KVGate:

   ```text
   observed used KV + positive reservation overlay + candidate total KV
     <= immutable hard KV limit
   ```

4. PrefillGate:

   - contention means local active Decode, raw running, raw waiting, or a fresh
     preemption delta;
   - under contention, only regular requests fit and post-admit known pending
     Prefill stays within the contended budget;
   - open regular and weighted requests share the open aggregate budget;
   - exclusive requests require no pending Prefill and become the sole long
     Prefill owner;
   - quiescent requests require no pending Prefill, local Decode, raw running,
     or raw waiting; and
   - unknown preexisting waiting may restrict long requests but cannot create a
     permanent minimum-request lock.

5. Scope: evaluate the canonical minimum request on the same state. If it fits,
   the real rejection is request-scoped; if it does not, protection is
   load-scoped. Invalid/stale state remains availability-scoped.

Work-conserving invariants:

- a rejected large request cannot close a following fitting small request;
- `waiting > 0`, one low TPS sample, or one preemption cannot independently
  reject all traffic;
- no rejection survives a Controller state change unless the current state
  still produces it;
- capacity reopens immediately after lifecycle or observation state makes the
  minimum request fit; and
- hard KV and reservation lifecycle invariants are never relaxed to improve
  goodput.

TPS and generation-rate observations remain diagnostics for this release. The
policy does not claim to forecast an exact TPS number. Its deliberately simple
QoS prediction is: current Decode/queue evidence selects the contention regime,
and candidate plus pending Prefill size predicts whether additional Prefill work
stays inside the fixed interference envelope. Long-window per-user TPS validates
that envelope at runtime; it is not an instantaneous feedback lock. If the
envelope fails GPU acceptance, change one explicit source policy and repeat the
matrix instead of adding a learned or delayed controller.

## 6. Shadow, enforce, HTTP, and lifecycle semantics

The Controller has one production transaction: `Admit`. It always creates a
reservation when the policy admits. There are no public external-observation,
reserve/no-reserve, or fake-request inspection variants.

Mode is applied only at the HTTP boundary:

- enforce forwards admitted decisions and rejects protected decisions;
- shadow forwards both, but still tracks a reservation for policy-admitted
  requests so an admitted burst has enforce-equivalent hypothetical state; and
- a shadow-protected request is deliberately unreserved because enforce would
  not have admitted it. Later real vLLM observations conservatively absorb its
  actual work.

The lifecycle is:

```text
Reserved -> ForwardedPrefill -> ActiveDecode -> Terminal
```

Duplicate events return false without mutation. Cancel, disconnect, timeout,
upstream error, success, expiry, epoch invalidation, and shutdown release at
most once. Streaming and non-streaming bodies preserve current conservative
first-byte semantics; no request is mutated to improve phase visibility.

Shutdown first stops the Observer, then atomically closes Controller intake and
terminates all remaining reservations with a shutdown cause. Later calls through
old handles are fenced no-ops. A test that needs a new identity constructs a new
Controller; production does not reuse or rebase the old epoch.

## 7. Observability and external compatibility

Every decision produces one immutable record containing request estimates,
class, pre-admit state, post-admit KV, pending Prefill before/after, action,
reason, scope, observation sequence, Controller sequence, and reservation token
identity without exposing user data.

HTTP outcome, logs, PIG metrics, `/v1/upstream-status`, and Router compatibility
must all derive from that decision or a current capacity record. They may not
rerun different reason logic.

Existing externally consumed metric names remain during v0.12, including
`pig_predictive_*` and the narrow `pig_dynamic_*` Router compatibility surface.
Source types and business logic do not retain legacy `requestAware` or
`predictiveShadow` names merely to preserve metric spelling.

Logs and metrics must show every enforced protection class and reason. Router
capacity is derived from the current minimum-request capacity, never from a
historical reject. Reporting has bounded label cardinality and contains no
request IDs, model prompts, tokens, credentials, endpoint secrets, or public
infrastructure addresses.

## 8. Test-first rebuild slices

Each slice begins with behavior-level red tests on c21, then the smallest
coherent implementation, deletion of replaced code, complete focused gates,
commit, and push. No compatibility alias or old implementation remains after
the slice that replaces it.

No Controller lock may cover HTTP, logging, metrics formatting, clock lookup,
or other I/O. Pass `now` into the transaction. One Controller fold produces the
decision/capacity state; the Adapter and Reporter may not trigger additional
reservation scans for the same record. The estimator adds no second body copy
and no additional unbounded full-body pass.

### Slice A: minimal request domain and estimator contract

- introduce the complete canonical `RequestWork` record;
- add robust fixed-window estimator fixtures and the offline safety/efficiency
  matrix;
- separate context selection from KV reservation;
- delete duplicate interval, active/physical, confidence, and parallel-token
  fields after all callers migrate; and
- benchmark maximum body p50/p99 and allocation bounds.

### Slice B: Controller and pure policy

- add one `AdmissionController.Admit` transaction;
- add fenced reservation tokens and the observation publication transaction;
- implement pure StateProjector, ContextGate, KVGate, PrefillGate, and canonical
  scope evaluation;
- migrate deterministic simulation to the Controller;
- rename the current candidate independently of a release version; and
- delete both old Manager files and request-aware policy types.

### Slice C: observer and application boundary

- publish observations only to the Controller;
- remove the Observer's duplicate decision snapshot;
- replace the request-aware Adapter and all `predictiveShadow*` production
  names with the thin runtime/HTTP boundary;
- make shadow track admitted lifecycles;
- isolate Reporter and pure Router compatibility projection; and
- delete tests that mutate private implementation fields, replacing them with
  contract tests.

### Slice D: full source acceptance

- prove HTTP body/header preservation and OpenAI-compatible outcomes;
- prove all lifecycle and observation event orders under race;
- prove logs, metrics, status, and Router projection agree;
- run deterministic required scenarios and alternate-order replay;
- run complete Go test, race, vet, build, simulation, and benchmark gates; and
- perform three independent code-review passes before assigning a version.

Structural deletion gates for Slice D:

- no production `Manager`, `requestAware*`, or `predictiveShadow*` symbol;
- no current candidate named for a release version;
- no parallel selection-token argument outside canonical `RequestWork`;
- no duplicate lower/upper, physical/active, or constant-confidence domain
  fields; and
- no test outside its owning package changes a concrete implementation's
  private policy, observation, reporter, or Controller fields.

Frozen historical baseline names and external metric string literals are the
only allowed legacy references.

## 9. Required deterministic and source gates

At minimum, tests must cover:

1. concurrent near-KV arrivals never exceed the hard counterfactual;
2. sample publication and admission never mix observation sequences;
3. old-epoch or old-generation handles cannot affect a new reservation using
   the same request ID;
4. every terminal path releases once, with no leak or double release;
5. first-byte plus covering-sample overlay transition is exact;
6. startup on healthy non-idle state does not self-lock;
7. stale/invalid state closes availability and the first coherent sample
   reopens it without a timer;
8. many small Prefills stop at the bounded budget and recover immediately;
9. waiting or ambiguous running still permits a fitting minimum request;
10. a 96K contended request is request-protected while a following 1K request
    admits on the same observation;
11. regular, weighted, exclusive, quiescent, and over-context requests obey
    their independent Gates and scopes;
12. one large rejection never creates low-flow/no-flow self-lock or Router
    mislock;
13. shadow and enforce produce the same decisions and admitted reservation
    evolution for the same supplied observation/lifecycle stream; only HTTP
    rejection differs. Shadow-only protected work that is actually forwarded
    may later change real observations and is reported separately;
14. metrics/status reads do not mutate Controller business state;
15. HTTP, logs, metrics, status, and Router compatibility agree on action,
    reason, scope, and current capacity;
16. repeated simulation and alternate scheduling order are deterministic;
17. maximum-body classification plus estimation p99 is below 100 ms on c21;
18. Controller decisions allocate zero with existing reservations, remain below
    100 microseconds at 256 live reservations, and below 1 millisecond at 4,096;
19. reservation/event/observation sequence overflow, impossible bounds, and
    concurrent shutdown fail closed without wraparound or stale-handle reuse;
    and
20. estimator under/over error and latency are reported separately, without
    presenting an exact-model oracle as portable proof.

The complete source gate is:

```text
gofmt check
git diff --check
go test focused packages
go test ./...
go test -race focused controller/server/simulation packages
go vet ./...
go build ./...
deterministic simulation command and acceptance audit
request estimator, policy, Controller, and HTTP hot-path benchmarks
secret and generated-artifact audit
```

All commands use `/usr/local/go/bin/go` in the c21 workbench. Record exact HEAD,
commands, exit codes, and SHA-256 hashes for simulation and benchmark reports.

## 10. Version, image, and c21 runtime gates

Only after section 9 passes on one pushed commit may the next `0.12.x` identity
be assigned. The version change is a separate exact commit with identity tests.

Only after identity acceptance may a local-only image be built. Validate image
labels, binary version, source commit, architecture, startup, health,
authenticated metrics, `/v1/models`, normal chat, streaming, forced tool call,
structured output, and request preservation before replacing the existing PIG.

Runtime iteration is PIG-only. Preserve the current vLLM container and CVM.
Required c21 workloads include:

- sustained regular Decode and the 12-by-2 small-request case;
- low flow after request, upstream, cancel, and timeout failures;
- same-snapshot bursts and near-KV concurrent arrivals;
- streaming and non-streaming lifecycle drain;
- regular/weighted/exclusive long-input cases supported by the 262K context;
- an intentionally over-context request proving upstream rejection occurs
  without GPU scheduling growth and all PIG/Router state drains;
- estimator-oracle cases, including prefix perturbation and high entropy; and
- repeated matched shadow/enforce or no-enforcement comparisons.

For every run record successes, request/load/availability rejects, completion
goodput, time-weighted per-user Decode TPS, p10/p50/minimum diagnostics,
30-second rolling windows, KV usage, running/waiting, reservations, PIG and
vLLM restart/OOM state, and preemptions.

QoS acceptance remains statistical:

- candidate median whole-run time-weighted per-user Decode TPS is at least 85%
  of the matched reference;
- for runs with at least ten valid 30-second windows, no more than 10% are below
  70% of the matched reference;
- one isolated low window is diagnostic, while consecutive low windows require
  causal review; and
- hard KV or lifecycle corruption fails immediately, while one isolated
  preemption is judged only by the matched repetition and recurrence rules.

Among safe QoS-compliant candidates, choose the highest median completed-output
token goodput. Raw admits, prompt TPS, total-token TPS, GPU utilization, and one
microbenchmark are diagnostics, not substitutes for this objective.

The accepted local image then undergoes ordered Pareto repetitions and an
independent source/evidence audit. Only after those gates pass may that exact
digest be uploaded. Production deployment and Router enablement require a
separate explicit authorization and are not implied by image publication.

## 11. Three-pass design review record

### Pass 1: objective, model, and causality

Completed on the first complete draft. The review found that the initial wording
could imply an exact model-neutral token/KV upper bound and could imply a
numerical TPS predictor. The plan now distinguishes deterministic accounting
from estimator risk, requires headroom plus estimator/GPU evidence, limits the
offline formula comparison, and defines the simple causal QoS prediction as
contention regime plus candidate/pending Prefill work. Context selection and KV
reservation remain independent. Feedback is only the next current observation,
and goodput is optimized only inside sustained QoS acceptance.

### Pass 2: state, safety, and lifecycle

Completed after Pass 1 revisions. The review added an explicit derived
live-reservation bound, fail-closed sequence/counter overflow, permanent
identity-epoch invalidation, last-coherent-observation behavior for transient
fetch failures, shutdown ordering, and old-handle fencing. One Controller owns
the observation and reservation transaction; the Observer has no decision copy.
No negative reconciliation credit, silent rebase, ID-only handle, or
reporting-owned business state remains in the target design.

### Pass 3: SOLID, efficiency, evidence, and release scope

Completed after Pass 2 revisions. The review rejected interface proliferation
and compatibility wrappers, fixed the final public transaction surface, banned
I/O and repeated folds under the Controller lock, added explicit structural
deletion gates, and kept external metric spelling separate from source naming.
The source, version, image, c21 runtime, registry, and production layers remain
independent evidence gates. The plan contains no permission to restart vLLM or
the CVM, upload an unaccepted image, modify Router, or enable production traffic.

## 12. Active checklist

- [x] freeze v0.12.10 as diagnostic-only evidence and retain rollback assets.
- [x] confirm authoritative c21 HEAD is clean and pushed.
- [x] reproduce the current full Go test baseline on exact HEAD.
- [x] audit HTTP, request cost, Manager/policy, observer, reporting, Router
  compatibility, config, simulation, and legacy ownership.
- [x] decide to replace the transaction/application architecture while keeping
  only independently valid kernels.
- [x] complete and record three design-review passes; revise this plan after
  each pass.
- [x] commit and push the new authority plan before production-code changes
  (`5bb0ef2`).
- [ ] Slice A passes and is pushed.
- [ ] Slice B passes, old Manager/policy code is deleted, and the slice is
  pushed.
- [ ] Slice C passes, old Adapter/shadow/duplicate-observation code is deleted,
  and the slice is pushed.
- [ ] Slice D complete source acceptance and three code reviews pass on one
  exact pushed commit.
- [ ] assign exactly one next `0.12.x` identity.
- [ ] build and validate one local-only image.
- [ ] complete c21 PIG-only compatibility, lifecycle, long-input, low-flow,
  QoS, goodput, and Pareto gates.
- [ ] complete independent source/evidence audit.
- [ ] upload only the exact accepted digest.

## 13. Stop rules

Stop and fix the architecture rather than adding a compatibility patch if:

- a decision depends on state not owned by the Controller;
- selection tokens travel outside the canonical request record;
- a new public reserve/no-reserve or external-observation decision variant is
  proposed;
- a stale handle can address a reservation without epoch and generation;
- HTTP/log/metrics/Router derive different action, reason, or scope;
- metrics/status influence admission state;
- a model asset, tokenizer RPC, cache lookup, learning loop, TTFT Gate,
  cooldown, or request mutation reappears;
- a test passes only by retaining a private field, legacy alias, or release
  version name; or
- image, deployment, or production claims exceed the exact completed evidence
  layer.
