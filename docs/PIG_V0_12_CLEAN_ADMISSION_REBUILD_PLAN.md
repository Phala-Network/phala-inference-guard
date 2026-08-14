# PIG v0.12 Clean Admission Rebuild Plan

Status: active architecture and execution authority. The rejected
`PIG-v0.12.11` history is retained below. The replacement `PIG-v0.12.12`
builder-local image from exact executable revision `bc95131` has passed source,
image-contract, c21 lifecycle, long-input, matched QoS, completion-goodput, and
ordered Pareto and exact registry publication gates. The accepted digest is
recorded in section 10.9. Production deployment, production traffic, and Router
changes remain unauthorized by this acceptance.

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

The 2026-08-11 architecture re-review started from pushed HEAD `5ac250c` plus
the uncommitted Slice A red/green estimator and request-domain work. That work
is not accepted merely because its focused tests pass. In particular, this
re-review changes the slice topology and lifecycle model before any old caller
is migrated.

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
11. A forwarded reservation is deleted immediately at terminal. If the request
    was admitted, forwarded, and completed before any later metrics sample
    covered it, the old observation does not contain its KV and the deletion
    temporarily undercounts real work.
12. The old target design permanently closed a Controller after every backend
    counter reset. That would turn an otherwise recoverable same-capability
    vLLM restart into a PIG self-lock until PIG was restarted.
13. The first rebuild slicing still proposed migrating doomed Manager/Policy
    callers to the new request record and deleting those callers one slice
    later. That is throw-away compatibility work, not a clean rebuild.
14. Recomputing the complete reservation-map fold for every admission makes the
    hot path O(live requests). The map is required for fenced lifecycle state,
    but admission and reporting need a checked aggregate overlay maintained by
    the same transaction.

The retained concepts are sound, but the ownership model is not. The rebuild
therefore keeps only the independently useful kernels and replaces the stateful
transaction and application boundary.

### 3.1 Keep, rewrite, and delete

| Area | Decision | Required result |
|---|---|---|
| bounded HTTP body preservation and protocol classification | keep, then rename only where needed | request bytes and headers remain unchanged |
| model-neutral lexical estimator | keep the bounded JSON scanner; replace blind sampling with measured complete lexical counting and fixed reservation semantics | no model asset, RPC, FFI, or learning |
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
       -> RequestEstimate
  -> AdmissionController.Admit(now, estimate)
       -> pure RequestWork construction from immutable capability
                                            one lock / one transaction
       current coherent Observation
       + checked aggregate Reservation overlay
       -> StateProjector
       -> AdmissionPolicy
            ContextGate
            KVGate
            PrefillGate
            canonical minimum-probe scope
       -> immutable DecisionRecord
       -> reservation with epoch + monotonic internal ID when admitted
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

`FastWorkEstimator` produces one `RequestEstimate` containing a model-neutral
selection estimate, an explicit KV-reservation estimate, and a rolling Decode
horizon. It knows no capacity, request ID, admission mode, or backend metrics.

`AdmissionController.Admit` uses its immutable capability to validate and
block-round the estimate into one internal `RequestWork` before acquiring the
transaction lock. Selection input tokens are part of these records and are
never passed as a parallel integer. No legacy manifest identity is carried by
the request: one Controller is already scoped to exactly one immutable
capability fingerprint.

`AdmissionController` is the only mutable owner of the current observation,
runtime epoch, event sequence, monotonic reservation ID, reservation map,
checked aggregate overlay, and atomic check-plus-reserve transaction. It owns
no HTTP, logs, Prometheus formatting, or Router wire names. The external or
client request ID is never a reservation-map key.

The Controller derives an absolute live-reservation bound from the immutable
hard KV limit and block size. This bound is a corruption/resource-exhaustion
guard, not the normal request-count admission algorithm. Admission still uses
KV and Prefill work. Counter overflow, reaching an impossible bound, or an
invalid internal transition closes availability instead of wrapping, evicting,
or guessing.

`StateProjector` is a pure combination of observation plus the aggregate
overlay. It performs no policy decision and no mutation. Admit and Snapshot are
O(1) in the number of live reservations. Observation publication may scan the
bounded map to apply sample-watermark transitions; tests compare the aggregate
against a slow reference fold after every transition.

`AdmissionPolicy` is pure. It evaluates the three Gates in order and evaluates
the canonical minimum request against the same immutable pre-admit state to
derive request/load scope.

`ReservationHandle` carries Controller runtime epoch and a never-reused
internal reservation ID. Forwarded, first-byte, and terminal transitions are
monotonic and idempotent. Sequence overflow closes intake rather than reusing an
ID. A stale handle therefore cannot address any reservation in a later epoch.

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
Controller: Admit(now, estimate), PublishObservation, StartSampleWindow,
            Snapshot(now), Close
ReservationHandle: MarkForwarded, MarkFirstByte, Terminate
HTTP/runtime boundary: Decide, Snapshot, Close
```

Lifecycle mutation remains reachable only through fenced handles, even if its
internal Controller methods are separate. No public fake-request inspection,
external-observation decision, or reserve flag is added.

## 5. Minimal domain model and algorithm

### 5.1 Request work

The estimator output contains only:

```text
selection input tokens
KV-reservation input tokens
rolling Decode horizon tokens
```

The Controller derives one internal immutable request-work record containing:

```text
selection input tokens
KV-reservation input tokens
rolling Decode horizon tokens
block-rounded input KV tokens
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

- inspect the complete bounded raw string values so neither a small prefix nor
  an unsampled region can dominate or evade the estimate;
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

The Controller rejects stale or internally invalid observations. A healthy busy
backend can initialize; it does not require an idle sample. The projected state
contains effective KV, known pending Prefill tokens, pending size-class
ownership, local active Decode count, and raw backend running/waiting.

A transient fetch or invalid non-drift sample leaves the last coherent
observation unchanged; normal age calculation then closes availability when it
becomes stale. Confirmed model identity, maximum context, KV capacity, or block
size drift closes capability availability and requires a fresh capability
initialization; PIG never silently applies the old geometry to a different
backend.

A confirmed monotonic-counter reset with the same capability fingerprint is a
backend runtime restart, not capability drift. Publishing the first coherent
post-reset sample atomically advances the Controller runtime epoch, fences and
clears all old reservations and residual debts, resets delta telemetry, and
reopens availability from that sample. Old handles are no-ops. This recovery
does not learn or mutate Prefill/KV parameters and does not require restarting
PIG.

No guessed ownership is subtracted from vLLM aggregates. Reservations contribute
only positive overlay:

```text
admitted or forwarded Prefill       full request KV + pending selection work
first byte without covering sample  full request KV, no longer pending Prefill
first byte plus covering sample     future Decode KV only
terminal before forward             reservation removed immediately
terminal after input was covered    reservation removed; observed KV remains
terminal before input was covered   full KV retained as residual debt until a
                                    sample started after terminal is published
```

`StartSampleWindow` captures the Controller epoch and event sequence before
metrics I/O. A first-byte or terminal transition is covered only when its event
sequence is no later than that start watermark. Events during the scrape remain
overlaid. This makes completion-before-poll conservative without a fixed
cooldown: a residual debt disappears on the first definitely post-terminal
sample, normally within one 500-ms interval. A stale observation may overcount;
it never receives negative credit.

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
- completion-before-poll debt reopens on the first covering observation rather
  than a time-based cooldown; and
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

The live lifecycle is:

```text
Reserved -> ForwardedPrefill -> ActiveDecode -> Terminal
```

Duplicate events return false without mutation. Cancel, disconnect, timeout,
upstream error, success, epoch invalidation, and shutdown terminate at most
once. There is no guessed expiry timer that can delete still-live backend work.
The HTTP boundary must install rollback and terminal defers so every local path
is explicit. A terminated reservation may remain internally as non-live
residual KV debt only until a covering observation; it accepts no later handle
transition. Streaming and non-streaming bodies preserve current conservative
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
coherent implementation, complete focused gates, commit, and push. Foundation
slices are explicitly not production behavior until the atomic HTTP cutover.
No old caller is migrated merely to keep a doomed Manager or Adapter alive, and
no compatibility alias remains after the cutover that replaces it.

No Controller lock may cover HTTP, logging, metrics formatting, clock lookup,
or other I/O. Pass `now` into Admit and Snapshot. The checked aggregate overlay
produces the decision/capacity state without a per-request reservation scan;
the Adapter and Reporter may not trigger a second fold for the same record. The
estimator adds no second body copy. It may perform a second lightweight lexical
scan of string values inside the existing 4-MiB request bound because c21
maximum-body p99 must remain below 100 ms; an unbounded body pass is forbidden.
Native latency/allocation acceptance is excluded from race-instrumented builds;
the same functional estimator corpus still runs under `go test -race`.

### Slice A: minimal estimate/work domain and estimator contract

- introduce `RequestEstimate` plus the internal derived `RequestWork` record;
- add complete bounded lexical estimator fixtures and the offline safety/efficiency
  matrix;
- separate context selection from KV reservation;
- do not migrate any old Manager, Policy, Adapter, or simulator caller; this
  slice's new `RequestEstimate` is not yet consumed by the real HTTP decision
  transaction. The shared retained estimator is corrected in place, so an
  unreleased build of the old path can observe its better selection hint; this
  is source behavior only and authorizes no runtime replacement; and
- benchmark maximum body p50/p99 and allocation bounds.

### Slice B: Controller and pure policy

- add one `AdmissionController.Admit` transaction;
- add same-capability epoch recovery, fenced reservation handles, residual
  completion debt, checked O(1) aggregates, and observation publication;
- implement pure StateProjector, ContextGate, KVGate, PrefillGate, and canonical
  scope evaluation;
- migrate deterministic simulation to the Controller;
- rename the current candidate independently of a release version; and
- do not add adapters to or modify the old Manager/Policy path.

### Slice C: atomic observer/application cutover and old-path deletion

- publish observations only to the Controller;
- remove the Observer's duplicate decision snapshot;
- switch the real HTTP pre-forward decision path directly from the old
  request-aware Adapter to the thin new runtime/HTTP boundary in one commit;
- make shadow track admitted lifecycles;
- isolate Reporter and pure Router compatibility projection; and
- delete old `RequestCost`/`VirtualState` duplicates, both Manager files,
  request-aware Policy/Gates, the request-aware Adapter, and all
  `predictiveShadow*` production names only after their real callers are on the
  new path; and
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
- no legacy manifest-bound request cost or parallel selection-token argument;
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
3. an old-epoch or already-terminal internal handle cannot affect any later
   reservation;
4. every terminal path releases once, with no leak or double release;
5. completion before the first covering poll retains positive KV debt, then the
   first definitely post-terminal sample removes it without a cooldown;
6. first-byte plus covering-sample overlay transition is exact;
7. startup on healthy non-idle state does not self-lock;
8. a same-capability backend counter reset advances epoch and recovers from its
   first coherent sample, while capability drift remains closed;
9. stale/invalid state closes availability and the first coherent sample
   reopens it without a timer;
10. many small Prefills stop at the bounded budget and recover immediately;
11. waiting or ambiguous running still permits a fitting minimum request;
12. a 96K contended request is request-protected while a following 1K request
    admits on the same observation;
13. regular, weighted, exclusive, quiescent, and over-context requests obey
    their independent Gates and scopes;
14. one large rejection never creates low-flow/no-flow self-lock or Router
    mislock;
15. shadow and enforce produce the same decisions and admitted reservation
    evolution for the same supplied observation/lifecycle stream; only HTTP
    rejection differs. Shadow-only protected work that is actually forwarded
    may later change real observations and is reported separately;
16. metrics/status reads do not mutate Controller business state;
17. HTTP, logs, metrics, status, and Router compatibility agree on action,
    reason, scope, and current capacity;
18. repeated simulation and alternate scheduling order are deterministic;
19. maximum-body classification plus estimation p99 is below 100 ms on c21;
20. Controller decisions allocate zero with existing reservations, do not scale
    linearly with live-reservation count, and remain below 100 microseconds at
    both 256 and 4,096 live reservations;
21. the checked aggregate equals a slow reference fold after every lifecycle,
    sample, terminal-debt, reset, and shutdown transition;
22. reservation/event/observation sequence overflow, impossible bounds, and
    concurrent shutdown fail closed without wraparound or stale-handle reuse;
    and
23. estimator under/over error and latency are reported separately, without
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

### 10.1 Accepted local-image and primary-runtime evidence

The exact executable candidate is:

```text
source revision: cf1e53e517e9630945a4808e2ee1c2c6b89a4664
image tag: ghcr.io/phala-network/phala-inference-guard:0.12.11-cf1e53e-local
image ID: sha256:13a645d77a2cb0822e232f286f62353b43ea43a6c6de41b427b2c6f44857c509
OCI version: 0.12.11
RepoDigests: []
```

Local image build evidence is retained at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-local-image-r2.bQ2AK4
```

The exact image passed isolated startup/contract, real-request, request-byte
preservation, and shadow/enforce canaries:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-image-canary-r3.Lgs6WN
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-image-request-suite-r1.qnW6Iw
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-request-preservation-r1.OCANbV
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-shadow-enforce-r1.iIcHgT
```

The first primary-runtime replacement attempt failed before candidate startup
because copied Compose labels were rendered as `key = value`; automatic
rollback restored v0.12.10. Its evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-runtime-replace-r1.W3cDjB
```

The corrected replacement did not copy Compose-internal labels. It sampled
PIG and vLLM drain state three times at 500-ms intervals, stopped and retained
only the old PIG as `pig-v0124-runtime-v01210-rollback`, and started the exact
candidate with the same network, alias, ports, socket mounts, and non-predictive
environment. No `PREDICTIVE_*` environment variable is present. The source
defaults produced enforce mode, automatic metadata capability, and a 500-ms
observer. The vLLM container ID, image ID, StartedAt, and restart count remained
unchanged. Evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-runtime-replace-r2.bsSDqu
```

Primary runtime smoke then passed normal, streaming, forced-tool, and strict
JSON-schema requests. All four admissions were `fit`; enforced rejects,
reservations, pending Prefills, lifecycle failures, raw vLLM running, and raw
vLLM waiting all drained to zero. Whole request times were 67.8--113.1 ms.
Evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-runtime-smoke-r1.B3kHx9
```

This accepts the local image and primary-runtime smoke layer only. It does not
accept the remaining lifecycle, long-input, low/no-flow, QoS, goodput, Pareto,
registry, production, or Router layers.

### 10.2 Primary-runtime protocol and terminal-path evidence

The first primary-runtime failure/lifecycle batch exercised malformed JSON,
unsupported content type, unknown-length chunked input, an upstream model
error, and a client-side 100-ms cancellation. Each case was followed by a
normal supported request after the applicable no-flow interval. The observed
HTTP results were `400`, `429`, `429`, `404`, and client timeout respectively;
every following request returned `200`. After every case and at the final
snapshot:

```text
reservations=0
pending Prefills=0
intake open=1
Router backpressure=0
dynamic_global_limit=0
vLLM running=0
vLLM waiting=0
lifecycle failures=0
```

Evidence is retained at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-runtime-lifecycle-r1.MJYhKO
```

This accepts those primary-runtime terminal paths and their low/no-flow reopen
contract. It does not replace isolated fake-upstream transport, timeout,
metrics-failure, reset, or capability-drift gates.

### 10.3 Long-input hard failure and runtime rollback

The first real long-input batch rejected the v0.12.11 candidate. Repeated
single-character words produced the following exact vLLM usage versus PIG
selection estimates:

```text
actual prompt tokens    PIG selection estimate    ratio
60,013                  15,014                    4.00x
96,013                  24,014                    4.00x
250,013                 62,514                    4.00x
```

The fixed 1.5x KV-reservation margin therefore did not cover this supported
text shape. A 270K-word request was estimated as 67,514 tokens and classified
as weighted, so PIG admitted it and vLLM returned its own context-length 400.
PIG should have protected this request before forwarding. This is a hard
request-estimation and reservation-counterfactual failure, not a QoS threshold
or runtime-lifecycle failure.

The batch stopped immediately. PIG reservations, pending Prefills, vLLM
running/waiting/KV, and preemptions were all zero afterward. After three further
drained samples, only PIG was rolled back: v0.12.11 remains as the stopped
`pig-v0124-runtime-v01211-failed-underestimate` evidence container,
`PIG-v0.12.10` again owns port 18080, and the vLLM container identity and
StartedAt remain unchanged. Evidence is retained at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01211-runtime-long-input-r1.7MdEfF
```

No further GPU QoS or goodput test may use this candidate. The next source
slice must add the repeated-short-lexeme fixture as red evidence, rebuild the
model-neutral estimator without a model asset or tokenizer RPC, rerun the full
oracle/latency/source matrix, assign a new `0.12.x` identity only after source
acceptance, build a new local-only image, and repeat runtime gates from the
beginning.

### 10.4 Estimator rebuild review and pre-commit source acceptance

The replacement estimator was reviewed three times after the v0.12.11
rejection. Each review changed the candidate rather than merely approving the
previous revision.

1. Model and causality review: the first red test reproduced the original
   defect at both the pure estimator and real HTTP pre-forward boundary.
   Sixty thousand independent "x " lexemes produced a selection estimate
   near 15K before the fix. A 270K-lexeme request reached the fake backend
   instead of ContextGate. The replacement settles each complete ASCII lexical
   run as ceil(run bytes / 4), charges bounded string whitespace separately,
   and makes the 270K request return request-scoped input_limit before any
   upstream call. A following small request is admitted, proving there is no
   low-flow self-lock.
2. Safety and SOLID review: expanded offline vLLM oracle probes found a second
   structural high-density shape. Repeated digits can be one token per byte,
   while the first rebuild still grouped them at four bytes per token. Digits
   are now charged individually; overflow, invalid helper input, request
   preservation, ContextGate causality, and lifecycle reopen are covered by
   focused tests. The estimator remains a pure request-domain component. It
   creates no state, performs no request mutation, and adds no dependency from
   the Controller to HTTP, vLLM, a tokenizer asset, or a learning service.
3. Throughput and evidence review: a proposed two-token surcharge for every
   complete two- or three-byte lexical run was rejected. It covered the
   tokenizer-specific "qz " counterexample, but it also estimated repeated
   common "of " and "the " text at about 122K tokens when vLLM reported
   about 60K. That would move ordinary 60K work from regular to weighted and
   protect it under active Decode, directly harming the goodput objective.
   The surcharge was removed. Common short-word fixtures remain in the oracle
   matrix to prevent that over-protection regression.

The accepted fixed, model-neutral hot path is therefore:

    one complete bounded JSON-string scan
      -> settle each ASCII letter/underscore run at ceil(run bytes / 4)
      -> charge each ASCII digit as one token
      -> charge string whitespace at ceil(total whitespace bytes / 32)
      -> retain punctuation, Unicode-leading-byte, escape/schema, multimodal,
         and high-entropy rules
      -> selection estimate
      -> fixed 9/8 KV-reservation margin
      -> one RequestEstimate consumed by Context/KV/Prefill gates

This is O(body bytes), allocation-free inside EstimateJSON, and has no
vocabulary lookup, model identity branch, tokenizer asset, RPC, FFI, cache
lookup, runtime calibration, or online learning. Selection and KV reservation
remain distinct: ContextGate uses selection, while Controller-owned
block-rounded KV work uses the 9/8 reservation.

The fixed 9/8 margin is not an arbitrary relaxation. The preregistered
candidate order is 9/8, 5/4, 3/2, 2/1; 9/8 is the first candidate for which
every accepted frozen oracle fixture has selection no lower than actual,
reservation headroom of at least about 10%, and reservation no greater than
2.25x actual. Eleven accepted fixtures cover repeated common short words,
single-character words, dense digits, ordinary long words, CJK, code, escapes,
high entropy, and large tool schema. Representative final results are:

    shape                    actual   selection   reservation
    "x " x 60K               60,013      61,893        69,630
    "of " x 60K              60,013      61,893        69,630
    "the " x 60K             60,013      61,893        69,630
    "01" x 64K              131,085     131,090       147,477
    "word " x 64K            65,549      67,602        76,053
    CJK 64K                  65,549      65,554        73,749
    code fixture            131,084     150,034       168,789

One diagnostic counterexample is deliberately not hidden: on the current
Gemma4 oracle, "qz " x 60K is about 120K tokens while a model-neutral
run-length estimate is about 62K. Characters alone cannot distinguish that
vocabulary-dependent shape from common "of " text, which is about 60K
tokens. Closing every such case without a model tokenizer requires a
throughput-hostile byte-level ceiling. This release therefore does not claim
exact ContextGate parity for adversarial vocabulary-dependent strings. The
declared contract remains best-effort model-neutral sizing plus immutable KV
headroom and real runtime safety/goodput validation. If production requires an
exact context guarantee later, that is a separate product decision to add a
backend-equivalent tokenizer contract, not another lexical special case.

Final 4 MiB ordinary-build estimator evidence remained below the user-approved
100 ms extreme-input budget with zero allocations:

    shape               p50          p99          allocations
    one long string     24.97 ms     34.00 ms     0
    "x " lexemes        31.86 ms     40.91 ms     0
    "qz " lexemes       27.36 ms     37.19 ms     0
    dense digits        21.74 ms     26.93 ms     0
    many strings        37.63 ms     47.29 ms     0

The final dirty-tree pre-commit gate is:

    /workspace/tmp/pig-v01212-estimator-final-precommit-r1.POulLw

It passed gofmt, git diff --check, go test ./... -count=1, focused
race tests for kvadmission/admission/server/simulation, go vet ./...,
go build ./..., two byte-identical simulations with acceptance="passed",
the corrected production-only legacy audit, and credential/generated-artifact
scans. Key SHA-256 values are:

    go-test-all              a372e1751c8be4238fbfa13dcee691b3ef36500ce729dd0f479ad3cde5200f2c
    go-race                  2ce4ed0f3714b4ba97af5121578c47a2d5d1ad35a2899a86f0ced2c2d6579031
    simulation-1/2           253f9b13b54b8db7a26f64ebf104e06d7be9d0b3a3dad0698b998367babe1bcc
    oracle                   c0e713523df3cf8b43c6890ca46de9830f74c8a40343de63a9e5e36653dba196
    estimator-performance    cfd685933a829f9dee1aecb2661000c29f76672f6af0d60846aae66aad6ff558
    executable-diff          9e93cd32b6f8830e7667fded01e5ff1e8d8d02a67a604d864cecdf94b37bcd75

This accepts the pre-commit source candidate only. It does not yet accept an
exact pushed commit, assign v0.12.12, build an image, replace the c21 PIG,
validate real long-input/GPU QoS/goodput behavior, publish a digest, modify
Router, or use production traffic.

The complete estimator slice was then committed and pushed exactly as:

    commit  8ddd87b32889ec1adaa2433e307f0c9e2a565814
    branch  codex/pig-v0.11.0-request-aware

HEAD and origin matched and the worktree was clean. The exact pushed source
was re-tested independently at:

    /workspace/tmp/pig-v01212-estimator-postpush-r1.uMh3V0

That gate again passed the complete Go suite, focused race packages, vet,
build, the eleven-fixture oracle, two byte-identical accepted simulations,
legacy verification, diff validation, and untracked-file audit. Key exact-
commit SHA-256 values are:

    go-test-all        c1b412d9b19a42b3b44323213ba9a54ea6e4efdee7078a050fdb399677e131db
    go-race            41bd0a6b8ad0078cad0e5da97afcca59ba6fc287a0a647f33f269a8b2b7ee31f
    oracle             50ce24c94b0faefb1d7759b4262fba3abce7f04c772bece26deae2afcb80795f
    simulation-1/2     253f9b13b54b8db7a26f64ebf104e06d7be9d0b3a3dad0698b998367babe1bcc

This accepts the rebuilt estimator source layer on the exact pushed commit.
It does not accept a release identity, image, c21 runtime, real GPU long-input,
QoS/goodput/Pareto, registry publication, Router change, or production
traffic.

### 10.5 Replacement v0.12.12 identity assignment

After exact pushed estimator acceptance, both local and remote Git tag checks
were empty for 0.12.12 and v0.12.12. Authenticated GHCR manifest GET requests,
using the registry Bearer-token challenge flow rather than HEAD or direct Basic
authentication, returned 404 for both candidate tags. The replacement identity
was assigned exactly once as:

    runtime  PIG-v0.12.12
    OCI      0.12.12
    commit   74b5c76bab33f02f2e0f6cbc394459053b6d0550

The identity commit changes exactly Dockerfile, server_types.go, and
release_identity_test.go. It is pushed; HEAD and origin match, and the
worktree is clean. The exact pushed identity gate is:

    /workspace/tmp/pig-v01212-identity-postpush-r1.YiyJX9

It passed the release identity tests, go test ./... -count=1, go build ./...,
empty local/remote Git tag checks, and repeated authenticated registry GET
404 checks. Key SHA-256 values are:

    release-test   3e3ddca2867e87c281484ec8d032eaca40d3038f967ee45e6e6e9c85f4b9e6a4
    go-test-all    142fdc1e2dfde8ecfc01c7a3e7650e64aa10830813b406b0a5269638bef3e154
    build          e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
    registry       08b4780f365265f783a309766521b2ce642bbcfc55b3e0d0b1989818b627660f

This accepts only the unique v0.12.12 source identity. It does not build,
upload, deploy, or runtime-accept an image and does not modify vLLM, the CVM,
Router, or production traffic.

### 10.6 v0.12.12 local-only image and isolated contract acceptance

The image was built from an exact clean archive of pushed commit
`bc9513117c36f1021896e17825289c94945b79e5`. It is local to c21 and has this
identity:

```text
tag       ghcr.io/phala-network/phala-inference-guard:0.12.12-bc95131-local
image ID  sha256:3670d0db8d1e472a505d2feb68d4aeff9cb261ebabc4eb13014688524d9b51a6
version   0.12.12
revision  bc9513117c36f1021896e17825289c94945b79e5
digests   []
```

The build evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-image-build-r1.UpaX28
```

Its recorded `SHA256SUMS` independently verify. The `PASS` summary SHA-256 is
`166eb40e3095e8574325acb2c1ff770f11b09be669c49462b2b5f487f08ada72`.
This is a builder-local image, not a registry image; it has no RepoDigest and
was not retagged from the rejected v0.12.11 image.

Three ordered isolated image gates then passed without replacing the primary
PIG, restarting vLLM or the CVM, changing Router, or sending production
traffic:

1. Startup and automatic capability canary:

   ```text
   /var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-image-canary-r2.saChKs
   ```

   `/healthz`, `/v1/models`, and `/pig/metrics` returned 200. The image started
   in default enforce mode with no explicit `PREDICTIVE_*` setting, derived the
   automatic metadata profile, and used the required 500-ms observer interval.
   The summary SHA-256 is
   `1375c6765b8e50f319b3c3c68a020dc11dc94cdf6068eb8cbe68933771c109f6`.

2. Request-preservation contract:

   ```text
   /var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-request-preservation-r1.ZM7E17
   ```

   The forwarded body SHA-256, declared and observed length, content type, and
   custom header were unchanged; the response was valid, reservations drained,
   and neither the credential nor prompt appeared in PIG logs. The summary
   SHA-256 is
   `13471f23d3723409a6256afa8cb43fea8076532646a280b7b0127ae1eaabd172`.

3. Shadow/enforce decision and forward-causality contract:

   ```text
   /var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-shadow-enforce-r1.uSWiiI
   ```

   The enforce container inherited no `PREDICTIVE_*` setting, while the shadow
   container had exactly the test-only
   `PREDICTIVE_ADMISSION_MODE=shadow` override. The same chunked request
   produced `protect / invalid_request / request` in both policies. Enforce
   returned 429 and the direct vLLM request-count delta was zero. Shadow
   forwarded the decision, returned 200, and the direct vLLM request-count
   delta was one. Logs and metrics agreed on action, reason, scope, mode, and
   enforcement; enforce recorded one enforced reject while shadow recorded
   none. Both reservations drained, intake reopened, and a following supported
   request returned 200 in each mode. The summary SHA-256 is
   `25e8d899de6ff378a4d57154b4828bb2fe62cc88964c945086d2d63d4fe18368`.

All isolated v0.12.12 test containers were removed. Before and after each gate,
the primary v0.12.10 PIG and existing vLLM retained their container identity,
start time, and restart count. This accepts the local image and its isolated
startup, preservation, and shadow/enforce contracts only. Primary-runtime API,
long-input, fake-upstream, low/no-flow, GPU QoS/goodput/Pareto, registry, and
production gates remain open. This documentation-only acceptance does not
change the executable bytes or the image revision above.

Runtime iteration is PIG-only. Preserve the current vLLM container and CVM.

### 10.7 c21 primary-runtime, lifecycle, long-input, and fake-upstream acceptance

The accepted local-only image replaced only the c21 primary PIG. v0.12.10 was
stopped and preserved as `pig-v0124-runtime-v01210-rollback`; vLLM and the CVM
were not restarted or replaced. The successful replacement evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-runtime-replace-r3.WzKbSc
```

The new primary container used image ID
`sha256:3670d0db8d1e472a505d2feb68d4aeff9cb261ebabc4eb13014688524d9b51a6`,
reported `PIG-v0.12.12`, default enforce mode and automatic metadata, inherited
no explicit `PREDICTIVE_*` setting, returned 200 for models and normal chat,
and drained reservations and pending Prefill. The PIG-only unavailable window
was one whole second at the runner's POSIX clock resolution. The vLLM ID,
StartedAt and restart count remained unchanged. The summary SHA-256 is
`78380b1c723b79bfbe3901ca22e586c5f449e85026174b9fe4187ea116804c1e`.

Two earlier replacement runners automatically restored v0.12.10 before this
success. The first emitted Docker labels as `key = value`; the second assumed
GNU `date +%s%3N` on the BusyBox host. Both were runner portability defects,
not image or admission failures. Each rollback was independently checked for
v0.12.10 health and an unchanged running vLLM before retrying.

Primary success-path API acceptance is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-primary-api-success-r1.vXy9BD
```

Normal, streaming, forced-tool and strict JSON-schema requests returned 200.
All four incremented admission fit exactly once, returned valid protocol
results, and individually drained reservation, pending Prefill, pending token,
and intake state. No lifecycle failure counter, credential, or prompt leak was
found. The summary SHA-256 is
`e7dd5a31ef274ef1d047e747753c351349363d4b07dcea33ffbbcd85c63233f8`.

Primary error and low/no-flow acceptance is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-primary-failure-lifecycle-r2.q1UZfW
```

Malformed JSON returned 400 before admission/upstream. Unsupported content and
chunked transfer each returned request-scoped 429 before upstream and exposed
the same `invalid_request` protection in logs and metrics. An upstream model
miss returned the real 404. A client was terminated only after live evidence
showed one PIG reservation and one vLLM running request. Every failure then
reached zero reservations, pending Prefill, pending tokens, vLLM running and
waiting; an immediate following small request returned 200. The matrix added
nine attempts, seven fits and two enforced risks with zero lifecycle failures.
The summary SHA-256 is
`8b3bf4578e4879f4da3a29e2b1e035221d44893e98bfd38bcc3c46d53f9d826e`.

Real 262K-vLLM long-input acceptance is aggregated at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-real-long-acceptance-r2.CHuSsk
```

The admitted sequential requests reported 60,013, 96,013 and 250,013 actual
prompt tokens. Their maximum PIG pending Prefill values were 61,893, 99,018 and
257,831 tokens. Each produced one upstream call, vLLM running reached one,
waiting stayed zero, and the preemption delta was zero. The 250K request took
72 seconds and reached maximum observed KV usage 0.256413 without an OOM,
restart or preemption. After every request, PIG and vLLM fully drained.

Both repeated `"x "` 270K and dense-digit 270K inputs returned request-scoped
429 with the authoritative Context Gate reason `input_limit`. The vLLM request
and prompt-token deltas were zero, preemption delta was zero, logs and metrics
exposed the protection, and the following small request returned 200. The
aggregate summary SHA-256 is
`6ec2a9843506e1222145e736231a3713be03f6f2d18c3b443d25564c17f43607`.
An initial runner expected the non-contract label `context_limit`; the source
and this plan both define `input_limit`, so the accepted protection slice was
rerun with the correct wire contract rather than changing production code.

High-context behavior that cannot be forwarded to the real 262K vLLM used a
bounded, ignored, test-only Go fake upstream. Its binary SHA-256 was
`471f2babccf7e35d95e9cc2c9c8c4b79926e621a50d2d93b652f65ce9371259a`.
The aggregate acceptance is:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-fake-runtime-acceptance-r1.9TPu1T
```

Automatic metadata for a 650K context derived maximum input 665,344 and the
65,536 / 262,144 / 524,288 Prefill boundaries with 262,144 aggregate budget.
The observed model-neutral selections were 278,447 for the exclusive case,
525,947 for the 512K quiescent case and 649,697 for the 650K quiescent case.
While an exclusive or quiescent request was forwarded but still absent from
fake backend metrics, PIG already published one reservation and respectively
278,447 or 525,947 pending Prefill tokens. Before any HTTP reject, Router
compatibility metrics showed active/applied backpressure, raw running zero,
effective running one, and dynamic global limit one. A second request returned
429 without a second upstream call using `prefill_exclusive/load` or
`prefill_quiescent/load`; after completion all state drained and a request
returned 200. The corrected high-context summary SHA-256 is
`5f12aad5e78e89dc6a40aade3725bc3e19138d5abbf03bf7ba17dc2ad72e3970`.

Same-capability generation/preemption/start-time reset increased the runtime
epoch from one to two and reopened without restarting PIG. A transient metrics
failure admitted from the last coherent observation; after its 600-ms maximum
age, PIG returned availability-scoped `observation_stale`, made Router
backpressure active/applied with dynamic global limit one, made no upstream
call, and reopened after metrics recovered. The stale/recovery summary SHA-256
is `0f2dec29d5441bb4a6cf381399a03575d2a6f19fe3348a9f59d8b435f37e83ad`.
A true KV-capacity capability drift permanently closed intake even after the
fake geometry was restored; both requests returned 429 without an upstream
call while the test PIG remained running. Its summary SHA-256 is
`435dbf37ddb8f00ef96514b3eec4cd04b5c0f920340094521500c129753c712a`.
With test-only `PROXY_TIMEOUT_SECONDS=1`, a delayed upstream was canceled after
one second, returned 429, drained all PIG/backend state with zero lifecycle
failures, and immediately reopened after delay removal. Its summary SHA-256 is
`ea381c7d6822e299191cd0acc45bf1d4c5c4a72258747c51164015edfcfe4181`.

All fake PIG containers and the fake server were removed, and the workbench was
disconnected from the temporary serving network. The primary PIG and vLLM
identity, start time, and restart count remained unchanged throughout. This
accepted c21 compatibility, request lifecycle, low/no-flow recovery, real and
simulated long-input behavior, Router pressure visibility, reset, stale,
drift, and timeout gates at that checkpoint. Section 10.8 now accepts the
matched QoS/goodput/Pareto repetitions. Registry publication, Router enablement,
and production traffic remained open. The main PIG remains the accepted
local-only v0.12.12 image; the v0.12.10 rollback container remains stopped and
preserved.

Required c21 workloads include:

- sustained regular Decode and the 12-by-2 small-request case;
- low flow after request, upstream, cancel, and timeout failures;
- same-snapshot bursts and near-KV concurrent arrivals;
- streaming and non-streaming lifecycle drain;
- real regular, weighted, and near-context long-input cases supported by the
  262K context; its automatic maximum admissible input is 261,888 tokens, so
  the 262,144-token exclusive boundary is intentionally unreachable there;
- isolated high-context fake-upstream exclusive, quiescent, 512K, and 650K
  classification and lifecycle cases, without forwarding those requests to
  the real 262K vLLM;
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

### 10.8 c21 matched QoS, completion-goodput, and Pareto acceptance

The final GPU gate ran on 2026-08-14 against the same unchanged c21 vLLM. The
benchmark executable SHA-256 was
`42b68c093d991324d5607adb58c45b932e496f8fa1676d3b00be54fa79081b0a`.
It used streaming chat with returned usage, a nonce at the beginning of every
prompt, 384 maximum output tokens, 500-ms PIG/vLLM samples, 300 seconds of
sustained arrivals per side, and ten complete 30-second windows plus the final
drain window. Candidate and reference traffic never overlapped. Each side
started only after both PIGs had zero reservations and pending Prefills and
vLLM had zero running and waiting requests. vLLM and its prefix cache were not
restarted or cleared between sides; the two 12-user repetitions reversed order
to control time and residual-prefix-cache bias.

The accepted matched results are:

| Run and order | Users | Reference goodput | Candidate goodput | Ratio | Reference per-user Decode TPS | Candidate per-user Decode TPS | Ratio | Minimum full-window ratio |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| r1 reference then candidate | 12 | 1568.958506 | 1569.182177 | 1.000143 | 134.861823 | 135.046784 | 1.001371 | 0.989178 |
| r2 candidate then reference | 12 | 1566.188510 | 1560.672631 | 0.996478 | 134.560732 | 134.183882 | 0.997199 | 0.991405 |
| Pareto r1 candidate then reference | 24 | 2727.542156 | 2719.835690 | 0.997175 | 117.391055 | 117.259691 | 0.998881 | 0.988048 |

Across the two balanced 12-user repetitions, the median candidate/reference
completion-goodput ratio was `0.998310` and the median time-weighted per-user
Decode TPS ratio was `0.999285`. None of the 20 complete windows was below 70%
of its matched reference; the minimum was `0.989178`. The 24-user matched point
also had no low window and remained effectively equal to reference.

Within the candidate, moving from the median 12-user point to 24 users raised
completion-output-token goodput by `73.799%` while retaining `87.107%` of
per-user Decode TPS. This is an accepted QoS/goodput Pareto improvement, not a
claim that PIG makes regular-only vLLM Decode faster. The clean rebuild's
distinct gains remain pre-forward long-input protection, atomic reservation
lifecycle, and proactive Router-visible backpressure without reducing regular
Decode performance.

All `9,235` requests across candidate and reference completed successfully
with zero request failures, unintended 429s, metric-sample errors, waiting,
preemptions, container restarts, OOM kills, or recent vLLM fatal memory/engine
errors. Maximum observed KV was `0.108639`; maximum live PIG reservations was
24. Every side drained reservations, pending Prefills, and pending Prefill
tokens to zero; intake reopened, lifecycle failures remained zero, and Router
backpressure returned to zero.

Raw and aggregate evidence is retained at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-qos-matched-r1.AXfiyX
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-qos-matched-r2.TEAu7g
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-qos-pareto-c24-r1.6gS4AD
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-qos-pareto-aggregate-r1.dn5zqak9
```

Every directory's `SHA256SUMS` verifies. The earlier smoke evidence at
`pig-v01212-qos-smoke-r1.u2wPUX` remains rejected because its nonce was at the
end of the prompt and let the second side inherit the first side's long cached
prefix. It is not used in any acceptance ratio. The corrected 12-by-2 smoke at
`pig-v01212-qos-smoke-r2.o7Vqv7` is retained only as a short protocol smoke;
the 300-second runs above provide the statistical acceptance.

The final three review passes found:

1. **Objective, model, and causality.** Completion tokens came from successful
   stream usage rather than estimates; Decode TPS used first-content-to-terminal
   active time; candidate/reference sides shared one unchanged vLLM and used
   balanced order at the primary point. The result proves no regular Decode
   regression and an accepted 12-to-24-user Pareto movement, while making no
   exact-tokenizer or cache-hit attribution claim.
2. **Safety and lifecycle.** There was no overlap between sides, no stale
   reservation, self-lock, waiting, preemption, restart, OOM, or terminal leak.
   The final live snapshot was idle and open. The candidate image ID remained
   `sha256:3670d0db8d1e472a505d2feb68d4aeff9cb261ebabc4eb13014688524d9b51a6`;
   vLLM StartedAt remained `2026-08-13T16:45:35.283358031Z` with restart count
   zero.
3. **Evidence and release.** The benchmark binary, raw JSONL, summaries,
   comparisons, aggregate, stdout, and success markers are hash-covered. The
   image still has `RepoDigests=[]`, OCI version `0.12.12`, and OCI revision
   `bc9513117c36f1021896e17825289c94945b79e5`. This accepts the exact local
   image for registry upload only; it does not authorize Router enablement,
   production deployment, or production traffic.

## 11. Three-pass design review record

### Pass 1: objective, model, and causality

### 10.9 exact registry publication and digest-canary acceptance

After section 10.8 and commit `eb38fba` were pushed, authenticated pre-push
checks returned explicit `manifest unknown` for both intended references. The
exact accepted builder-local image ID was tagged without rebuilding and
published as:

```text
ghcr.io/phala-network/phala-inference-guard:0.12.12
ghcr.io/phala-network/phala-inference-guard:0.12.12-bc9513117c36
```

Both tags and the immutable digest reference resolve to:

```text
sha256:88fe445972412a71e2f03c6347d4868b5f62d1beb205ffb9611827535dd2e11e
```

Independent manifest inspection and pull of the version tag, revision tag, and
digest reference all resolved to local image ID
`sha256:3670d0db8d1e472a505d2feb68d4aeff9cb261ebabc4eb13014688524d9b51a6`
with OCI version `0.12.12` and revision
`bc9513117c36f1021896e17825289c94945b79e5`.

A fresh temporary container started directly from the digest reference with no
explicit `PREDICTIVE_*` environment. It reported `PIG-v0.12.12`, default
enforce mode, a 500-ms observer, automatic capability, open intake, and zero
reservations or pending Prefills. Health, authenticated models, and
authenticated metrics returned 200. The canary was then removed. The primary
candidate PIG, reference PIG, and vLLM remained at restart count zero; both PIGs
were open and fully drained, and vLLM running, waiting, and preemption remained
zero.

The first publish attempt is retained as a rejected runner/authentication
attempt: all pre-push tag checks were valid and no manifest was created, but an
expired Docker credential returned `unauthorized` while pushing the revision
tag. GHCR authentication was refreshed from the existing authorized GitHub
credential without recording the token, and the same prechecked image then
published successfully. The first digest-canary run itself was healthy but its
runner expected the wrong log key; the corrected r2 canary accepted the same
digest without changing runtime code.

The complete publication evidence, including both rejected harness attempts,
successful push logs, three manifest documents, three pull logs, canary logs
and metrics, final runtime state, secret audit, `SUCCESS`, and verified
`SHA256SUMS`, is retained at:

```text
/var/volatile/dstack/persistent/pig-v0124/tmp/pig-v01212-registry-publish-r1.iGfjhT
```

This completes the exact image registry gate. It does not deploy that digest to
production, enable a Router node, or authorize production traffic.

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
capability-drift invalidation, same-capability runtime-epoch reset recovery,
last-coherent-observation behavior for transient fetch failures, shutdown
ordering, and old-handle fencing. One Controller owns the observation and
reservation transaction; the Observer has no decision copy. No negative
reconciliation credit, silent capability rebase, ID-only handle, or
reporting-owned business state remains in the target design.

### Pass 3: SOLID, efficiency, evidence, and release scope

Completed after Pass 2 revisions. The review rejected interface proliferation
and compatibility wrappers, fixed the final public transaction surface, banned
I/O and repeated folds under the Controller lock, added explicit structural
deletion gates, and kept external metric spelling separate from source naming.
The source, version, image, c21 runtime, registry, and production layers remain
independent evidence gates. The plan contains no permission to restart vLLM or
the CVM, upload an unaccepted image, modify Router, or enable production traffic.

### 2026-08-11 re-review after Slice A red tests

The plan and live dirty tree were reviewed again instead of treating the first
three passes as permanent approval.

1. Model and causality: removed legacy manifest identity from request work and
   made the Controller's immutable capability the only rounding/geometry
   authority. `RequestEstimate` is the estimator boundary; `RequestWork` is an
   internal derivation. The fixed estimator matrix and model-neutral claim
   boundaries remain unchanged.
2. Safety and lifecycle: found and corrected completion-before-poll
   undercounting by adding watermark-bounded residual positive debt. Replaced
   permanent self-lock after same-capability counter reset with an atomic
   runtime-epoch transition; true capability drift still closes. Removed
   guessed reservation expiry and simplified fencing to epoch plus a monotonic
   internal reservation ID.
3. SOLID and efficiency: rejected the proposed double migration through the
   old Manager/Policy, changed Slice C to one atomic real-path cutover and
   deletion, and replaced per-admission map folding with a checked O(1)
   aggregate plus a slow-fold test oracle. The new core may reuse independent
   kernels but may not adapt or extend the old stateful path.

### 2026-08-14 Slice A simplification review

The first Slice A implementation used four lexical windows and outlier
discarding to keep incremental work nearly constant. Re-review rejected it as
unnecessary complexity and as a correctness blind spot: content between the
four windows could evade estimation even though PIG already bounds the request
body at 4 MiB and scans it for JSON structure.

The replacement counts complete raw string values while the same bounded body
is classified. Submission review then found that the old estimator API itself
accepted some malformed JSON even though the HTTP caller happened to validate
first. The domain API now performs standard bounded JSON validation rather than
maintaining a second partial parser. On c21 the final r3 ordinary-build
estimator remained allocation-free: one 4-MiB string measured p50 20.76 ms /
p99 28.71 ms, and the adversarial many-short-string body measured p50 36.73 ms /
p99 46.05 ms. Full body read, preservation, strict parsing, field extraction,
and estimation together measured p50 29.79 ms / p99 40.50 ms, below the accepted
100-ms extreme-input budget.

The registered exact-tokenizer evidence matrix also exposed and fixed schema
structure undercount. It rejected the registered 1.25x candidate and selected
the fixed 1.5x KV-reservation margin; all seven fixtures retained at least 10%
evidence headroom without exceeding 2.25x actual tokens. The frozen Gemma4
oracle fixtures bind request byte length and SHA-256 to the recorded token
counts. That oracle remains target evidence only; the portable contract is
still a fixed model-neutral formula plus startup KV headroom and later GPU
acceptance.

### 2026-08-14 Slice A source acceptance

The final Slice A candidate was tested in `pig-v0124-workbench` on c21 from
base HEAD `d8590670ac773d12da676a959a5b739abe1e9f70`, branch
`codex/pig-v0.11.0-request-aware`, with Go `1.24.13 linux/amd64`. The runner
used `set -euo pipefail`; therefore a failed command on either side of `tee`
could not produce the success sentinel. The candidate passed:

- focused tests for `kvadmission`, `predictive`, and request classification;
- `go test ./... -count=1`;
- focused `go test -race`;
- `go vet ./...` and `go build ./...`;
- the seven-fixture exact-tokenizer oracle and fixed-margin acceptance matrix;
- 4-MiB estimator/classifier native latency and allocation gates;
- five benchmark repetitions; and
- `git diff --check`.

The ordinary-build extreme-input evidence was p50 `20.53 ms` / p99
`29.64 ms` / zero allocations for one 4-MiB string, p50 `37.31 ms` / p99
`38.26 ms` / zero allocations for 4 MiB of many short strings, and full
classifier p50 `28.92 ms` / p99 `42.53 ms`. The r4 log SHA-256 values are:

```text
bench       9d4d58e5124988a81457f84c737fe203627648484f8f4d6655933ccf6dad405c
build       e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
diff-check  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
evidence    9f532055974284f8aba44153ca2b24484b9e69972894cbdaec6fc36e42aa8567
focused     1ac2a7bbee4ab505883d10f412863f5f1312d6bf4cc37a77d5380b50978151cc
full        88e8ac20482f762659f67ed0139e7fe63e139c59a651059c68060a31c057558a
race        fe9d8b393a631ed6763ffbae7c4c541ae6af71af85c618c26ac39e8ed05d9cf7
source      b2c030b5a18efb031404a97ac9a92b16e4d722ede7b3b609438719f154b8b422
vet         e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

This accepts only the Slice A foundation. `RequestEstimate` is not yet
consumed by a new Controller or by the real pre-forward HTTP transaction. The
shared estimator behavior has changed, but there is no new enforce path,
image, deployment, runtime, GPU-goodput, or production evidence.

The exact Slice A source was committed and pushed as
`a86d4c1852c1c30864bcd209b9ab71d17af32852`. The executable Go files are
byte-identical to the r4 candidate; the only post-gate change inside that
commit was this evidence/checklist documentation.

### 2026-08-14 Slice B three-pass implementation review

Slice B starts from pushed HEAD `ae2402e` and adds a new independent
`internal/admission` package. It does not modify or adapt either old Manager,
the old request-aware Policy/Gates, or the production HTTP path.

1. Model and causality: `AdmissionController.Admit` consumes the canonical
   `RequestEstimate`, derives `RequestWork` from its immutable capability, and
   applies Context, KV, and Prefill Gates to the same pre-admit projected
   state. The deterministic suite now names this implementation `candidate`
   and retains `v0.12.10` as a frozen historical comparison. Review found that
   the old suite protected KV/preemption/self-lock but did not directly lock
   the objective, so aggregate raw and QoS-qualified output-token goodput
   non-regression against no-admission, `v0.12.2`, and `v0.12.10` was added.
   Review also corrected protected DecisionRecords so their post-admit KV and
   Prefill fields remain counterfactual forecasts instead of silently
   reverting to the pre-admit values.
2. Safety and lifecycle: behavior-level red tests first demonstrated that a
   policy-only implementation admitted all concurrent near-KV arrivals and
   never accumulated Prefill work. The implemented atomic transaction now owns
   epoch-fenced monotonic handles, a checked O(1) aggregate, sample-watermark
   coverage, completion-before-poll residual debt, same-capability counter
   reset recovery, permanent capability-drift closure, bounded maps, and
   fail-closed sequence overflow. Tests cover an in-flight old sample, covered
   first byte, every terminal shape, duplicate events, reset, drift, stale
   recovery, busy startup, concurrent publication/admission, 32 near-KV
   arrivals, 5,000 deterministic lifecycle transitions, shutdown, and a slow
   reference fold after every state change.
3. SOLID and efficiency: the new core has one mutable owner and concrete pure
   Gates/Projector; it introduces no compatibility interface, forwarding
   wrapper, model asset, cache path, learning state, timer, request mutation,
   or mode branch. Snapshot and protected Admit use the aggregate without
   scanning reservations. On c21, both 256 and 4,096 live reservations measured
   zero allocations; Snapshot p99 was `254 ns` and protected Admit p99 was
   `320-321 ns`, with no 16x live-count scaling. Observation publication and
   shutdown are the intentionally bounded O(n) reconciliation paths.

The deterministic candidate currently reproduces the frozen `v0.12.10`
aggregate behavior: `91,998.25` raw output tokens, `90,092.07` QoS-qualified
tokens, one simulated preemption, and at most `0.4 s` idle with demand. This is
architecture and deterministic evidence, not a claim of GPU improvement or
production readiness. The new Controller is still disconnected from the real
observer and pre-forward HTTP transaction until Slice C.

### 2026-08-14 Slice B source acceptance and release review

The exact executable candidate was tested in `pig-v0124-workbench` on c21 from
base HEAD `ae2402e1d9273b1399c2eed4bdcd65411443abc3`, branch
`codex/pig-v0.11.0-request-aware`, with Go `1.24.13 linux/amd64`. The final r2
runner SHA-256 was
`d9bfa04ebe9f6500f3af754a5ddb291f4f099f5c02d6d1b97a58a33725372b9d`.
It refused to reuse an existing evidence directory and used
`set -euo pipefail`, so a failed command on either side of `tee` could not
produce the success sentinel. It passed:

- focused admission and deterministic-simulation tests;
- `go test ./... -count=1`;
- race tests for admission, simulation, and the unchanged HTTP server;
- `go vet ./...` and `go build ./...`;
- the ordinary-build O(1)/zero-allocation Controller performance gate;
- registered deterministic raw and QoS-qualified goodput acceptance;
- the public simulation command; and
- `git diff --check`.

The third evidence/release review found one acceptance-harness false-positive:
the aggregate formula read the frozen `v0.12.10` baseline, but a missing
per-scenario `v0.12.10` result aggregated to zero and could pass. A focused red
test failed only for that missing comparison. The harness now requires all
four policies and equal arrivals for each scenario; the focused test and the
complete r2 matrix then passed. This changes acceptance integrity, not the
Controller policy or its deterministic result.

The r2 ordinary-build performance evidence was zero allocations for Snapshot
and protected Admit with both 256 and 4,096 live reservations. Snapshot/Admit
p99 was `335 ns`/`439 ns` at 256 and `253 ns`/`320 ns` at 4,096. The 16x live
reservation count did not cause linear hot-path scaling. The deterministic
aggregate remained:

```text
policy        raw output tokens  QoS-qualified tokens  preemptions  max idle with demand
no_admission          79,114.46              70,112.34           57                     0 s
v0.12.2               76,621.10              70,825.16            1                  15.3 s
v0.12.10              91,998.25              90,092.07            1                   0.4 s
candidate             91,998.25              90,092.07            1                   0.4 s
```

Final r2 log SHA-256 values are:

```text
build                  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
diff-check             e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
focused                4f51cc56490b7faa79b1e564e80a641947caa14051b1bbb906710d79e040821c
full                   d019f330a2267122800f70378bca2edfbc97da6a707c302b5b9fe1cb07e049c3
performance            ed2a67ca72db141cfeef39a5a04514beecfd20187e5a6ad2ea537f1c9f80f35a
race                   b35d48b35403353c3dc5d82522934f3aa296eeb99e2d0bc7a1d498a2c5aeed0b
simulation-acceptance  29fb7c5f9ea281fad8f8d478426441020037eb0e484e4552a446b80ee6a4958b
simulation-command     c678b7a2734df0b68a8e22fa5cd9f8ad64323c3c883a2564e83cd4dea1e09075
source                 9e444ba6060ffff72e2c0acf1283f5ac44d101a35f8c825878fbf05b8fefbc02
vet                    e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

The final scope review found zero tracked diff bytes under
`internal/runtime/predictive` and `internal/app/server`; no production HTTP,
Observer, Router, Compose, image, deployment, GPU, or traffic claim is part of
Slice B. The simulation candidate does execute the new Controller transaction
and reservation lifecycle, but that remains deterministic model evidence until
Slice C performs the real HTTP/Observer cutover.

The exact Slice B executable source and acceptance record were committed and
pushed as `caa0138c70282f6876fe1cc4669f48703e177d35`. HEAD and
`origin/codex/pig-v0.11.0-request-aware` matched that SHA with a clean worktree.
The following closure update is documentation-only; it does not change or
inherit new executable evidence beyond the r2 candidate recorded above.

### 2026-08-14 Slice C/D clean-runtime cutover review

Slice C started from pushed HEAD `95561bc9022f6f50a1679c9a8f8fed30ce5fe5e4`
in `pig-v0124-workbench` on c21. The implementation cut the real request path
directly to `AdmissionController`; it did not adapt the deleted Manager, Policy,
Adapter, or shadow coordinator. Three review passes revised the candidate before
acceptance.

1. Model and causality: the bounded HTTP classifier now supplies its complete
   `RequestEstimate` to `AdmissionRuntime.Decide`, which calls
   `AdmissionController.Admit` before any upstream operation. A focused HTTP
   contract holds backend state constant and proves request size changes that
   pre-forward decision. Review found that the ignored estimator-validity bit
   obscured unsupported-input behavior. The path now explicitly turns an
   unsupported estimate into request-scoped `invalid_request` protection; a
   contract proves it reserves nothing, leaves canonical capacity open, and
   cannot lock a following supported request. Shadow and enforce share policy
   and policy-admitted reservation evolution; only the HTTP handling of a
   protected decision differs. The simulator executes the same Controller and
   labels `v0.12.10` only as a frozen historical fixture.
2. Safety and lifecycle: real proxy contracts cover successful first byte and
   EOF, upstream 5xx, transport failure, timeout, client cancellation and
   disconnect, outer-defer duplication, and positive residual debt until a
   covering observation. Every case performs exactly one successful terminal
   mutation. Observer failure leaves the last coherent state to age stale, a
   fresh sample immediately recovers low/no-flow state, and identity/geometry
   drift closes. Review found that the old observer appeared to validate
   `max_model_len` while merely replaying the startup value. c21's actual vLLM
   metrics were read-only checked and expose `process_start_time_seconds`.
   Runtime epoch changes are now detected from that optional metric even when
   monotonic counters increase; counter decrease remains the fallback for older
   metrics. Before an automatically initialized reset sample can reopen intake,
   one bounded `/v1/models` revalidation must confirm model identity and
   `max_model_len`. Failure publishes nothing and ages stale; changed metadata
   permanently closes the old Controller. Explicit profiles remain governed by
   their explicit immutable contract.
3. SOLID, efficiency, and release scope: Controller is the sole mutable
   admission-state owner; Observer performs I/O/publication, Reporter owns only
   bounded reporting state, and Router/status/metrics are pure snapshot
   projections. Production source contains no old `Manager`, `RequestAware`,
   `predictiveShadow`, `RequestCost`, or `VirtualState` execution symbol. The
   unused TPS/TPOT histogram and `chooseBackend` helper were deleted. The
   simulation-only `requestaware` namespace and external
   `pig_predictive_request_aware_*` wire strings remain because renaming them
   adds churn without creating a second production path. The candidate reports
   `PIG-v0.12-dev` / OCI `0.12-dev`, not the historical v0.12.10 release and not
   a newly assigned `0.12.x` release.

The frozen baseline initially had CRLF worktree bytes with SHA-256
`1812c2cf2a0dad1c0562a252ba32844bc0d1116d489a68f22b6702f25a08e9c4`,
while Git's `eol=lf` index normalization produced different release bytes. The
fixture was normalized to the actual tracked LF blob and both worktree and
index now have SHA-256
`cb1a57553e3f709fd3825e01e56bf6f8eb6d6f0f30883cfa8df280f5cd16f462`.
It remains 36/36 scenarios from source commit
`caa0138c70282f6876fe1cc4669f48703e177d35` and source-suite SHA-256
`c678b7a2734df0b68a8e22fa5cd9f8ad64323c3c883a2564e83cd4dea1e09075`.
`git checkout-index` exported the exact candidate without `.git`, included the
fixture, and passed `go test ./...`; therefore the embedded baseline no longer
depends on an ignored workbench file.

### 2026-08-14 Slice C/D source acceptance

The exact staged candidate patch SHA-256 was
`fe68bcc64736506787761024b3aee758b328daa390a1adba9c9aff41b4de5c6c`.
The runner used Go `1.24.13 linux/amd64`, `set -euo pipefail`, per-command test
timeouts, a new evidence directory, and a final success sentinel. It passed:

- focused admission, server, simulation, metrics, predictive-domain, and
  Prometheus-parser tests;
- `go test ./... -count=1` both in the worktree and in an exact clean-index
  export;
- focused race tests for admission, server, simulation, metrics, and the
  Prometheus parser;
- `go vet ./...` and `go build ./...`;
- registered deterministic acceptance, replay, and alternate policy order;
- the public simulation command;
- Controller constant-time/allocation gates at 256 and 4,096 reservations;
- 4-MiB classifier and estimator native latency/allocation gates plus three
  benchmark repetitions; and
- gofmt, staged diff, structural deletion, release-identity, fixture, and
  added-line credential audits.

The outer Codex SSH control call was mistakenly given a 10-second wrapper
timeout, but the already-started remote runner was not killed. It continued to
natural completion. Acceptance was recorded only after its parent process had
exited, `SUCCESS` contained `SLICE_C_ACCEPTANCE_PASS`, every log matched
`SHA256SUMS`, the staged patch hash was unchanged, and no unstaged source drift
existed. This was a control-channel timeout, not a Go test timeout or a manually
reconstructed success result.

Ordinary-build performance evidence was:

```text
Controller 256 reservations    Snapshot p99 280 ns  Admit p99 312 ns  0 allocs
Controller 4096 reservations   Snapshot p99 279 ns  Admit p99 312 ns  0 allocs
4-MiB full classifier          p50 29.49 ms  p99 41.28 ms
4-MiB long-string estimator    p50 20.97 ms  p99 29.26 ms  0 allocs
4-MiB many-string estimator    p50 37.93 ms  p99 49.29 ms  0 allocs
```

The deterministic aggregate remained:

```text
policy              raw output tokens  QoS-qualified tokens  preemptions  max idle with demand
no_admission               79,114.46              70,112.34           57                     0 s
v0.12.2                    76,621.10              70,825.16            1                  15.3 s
v0.12.10 historical        91,998.25              90,092.07            1                   0.4 s
candidate                  91,998.25              90,092.07            1                   0.4 s
```

Evidence log SHA-256 values are:

```text
benchmarks             213658638ffa2902e4fa52a319918dfef4fa139f63e491d249bac7bc773b506c
build                  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
classifier-performance fdc0a58ec1e1df34b99a41ec4dd7da5096611ce59e83c9d9c666d930351c433a
clean-index-full       0fe1570bc3c8102805ec8424445fc1fea302c62a9787796b828c272c8abf9867
controller-performance 497f4a012b9cbbb7904ecaf9c7b800583c8839d125a421597016e660cf78ab2d
estimator-performance  63eb85bc7ce2f76992b50db5bb46a28976771ab115e1d09e946755d737f33fd3
focused                2c5a8a6b7893884c85a2fd94ff5de6b5aaf026dee73fdd615bd89b3464da5b9a
full                   8d5be136027616cd9778a9fe25e610c2e61ae28b7868816a0489bcaaec2827c1
race                   5fbb07c5f4a67e5cb1890db0e2241f9482cf1413a4bc086c703a7f091d320857
simulation-acceptance  6f44c196dee53dd24df5b58a986a9ce1950310de6609c919f0129dade310131d
simulation-command     253f9b13b54b8db7a26f64ebf104e06d7be9d0b3a3dad0698b998367babe1bcc
source                 d4958a7b85a84e215cbef2b5840170ba294649fe3b89504389fa2692beba3c41
structural             afae4fed0f3d00c1ef4f898631e7abf3cfedad1b15addcf247f1989424dc2578
vet                    e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

The exact executable source was committed and pushed as
`c2e58cc66675c9632e536d73aece1641cf0fca71` with message
`refactor: cut over to clean admission runtime`; HEAD and
`origin/codex/pig-v0.11.0-request-aware` matched. This accepts the complete
source cutover and source matrix only. It does not assign a release version,
build or publish an image, replace the running c21 PIG, restart vLLM/CVM,
modify Router, or provide GPU/runtime/production-goodput evidence.

### 2026-08-14 independent source/evidence audit

An independent audit started from clean pushed HEAD
`24654d61a5423b2476a626ff97196ece8d2bbb4e`. The only path changed after the
accepted executable commit `c2e58cc` was this plan. The executable binary patch
SHA-256 was independently reproduced as
`fe68bcc64736506787761024b3aee758b328daa390a1adba9c9aff41b4de5c6c`, exactly
matching the recorded acceptance patch.

The audit directory is:

```text
/tmp/pig-independent-source-audit-r1.hNLPVa
```

The audit independently verified:

- HEAD, branch, remote HEAD, and a clean worktree;
- every file in `/tmp/pig-slice-c-acceptance-r1/SHA256SUMS` with
  `sha256sum -c`, including the original success marker;
- the frozen `v0.12.10` fixture is tracked, present in a clean index export,
  and has identical index/worktree SHA-256
  `cb1a57553e3f709fd3825e01e56bf6f8eb6d6f0f30883cfa8df280f5cd16f462`;
- no deleted production Manager, RequestAware, predictiveShadow, RequestCost,
  VirtualState, or NewManager declaration returned;
- no credential-like added line or unexpected private-key/patch artifact was
  present; and
- an exact `git checkout-index` export passed `go test ./... -count=1`, focused
  Controller/server/simulation `go test -race`, `go vet ./...`, `go build
  ./...`, and `git diff --check` on c21 with Go 1.24.13.

Independent audit log SHA-256 values are:

```text
build             e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
clean-full        1c6d30bf73a57f8f63e5a2eac18cc49a7356ddc90e97accb4e24b8ffa926493f
clean-race        20904ef64bc562b539a38038be4aac7fb3103eea3dd502769c1b026a7c60a68a
evidence-hashes   cc965e34b0457b216443fb043f4b4e26f2c74f6657dce69946e3c87c9c81e2e4
fixture-hash      30dcc73db0d96dbe32ce3d3fa63cc69c074396e7fceef4bc10dd07d053224323
fixture-tracked   324f7904424b6c9292c0f448993aa6f72c72e634037e59f98910ae25c3aafdbb
identity          1688600abae7910cbc406145c2025303f57365418e4205c9330a7b5b208ba133
legacy-symbols    e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
patch             673380429daf8b252c0ca0ff6c33dbb3349f98aa6bacc390e094cd75f513b31f
vet               e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

This closes the independent pre-version source/evidence audit. It does not
convert model-neutral estimator error into tokenizer parity evidence, and it
does not accept c21 runtime lifecycle, QoS/goodput/Pareto behavior, an image,
registry publication, deployment, Router changes, or production traffic.

### 2026-08-14 v0.12.11 identity assignment

The version gate started from clean pushed HEAD
`139fe91f5cbac2a776eb2b489c6948e1ee6eac26`. Authenticated registry `GET`
checks, deliberately not `HEAD`, returned `404` for both candidate tags
`0.12.11` and `v0.12.11`; Git also had no v0.12.11 tag. The release identity was
therefore assigned once as:

```text
runtime  PIG-v0.12.11
OCI      0.12.11
commit   16d1940e2fba0f608357fa8428a4553ddd712ba3
```

The identity commit changes exactly `Dockerfile`, `server_types.go`, and
`release_identity_test.go`. Its binary patch SHA-256 is
`6f6c3c09c2b9a072eac492d957f87c454ce650e87e4908c500d9f53d5673141c`.
The explicit release test locks the assigned runtime identity and the existing
cross-file test locks runtime/OCI equality.

The clean pushed identity gate is:

```text
/tmp/pig-v01211-pushed-identity-r1.7dqenW
```

On exact pushed commit `16d1940`, it passed the focused release tests,
`go test ./... -count=1`, and `go build ./...`. It also reconfirmed that neither
candidate registry tag existed before any image build or upload. Log SHA-256
values are:

```text
build         e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
full          3a21814041c7202b2bf7bb4bdbc7cad685e2b5700931ada240eb92da0bc308ab
identity      64943361393b74cc996ad103b4e7c0c9df775683fd59f962d8a5178333a9a460
registry      9fd02cad39f78678386a09728c87d58637b1d3b8a4b39f1c642e17400dd4c020
release-test  bd6d57a8f88bd34753af08bb8f90f92af586b457ffe9466ee3210f1cad7f3b29
```

This accepts only the unique v0.12.11 source identity. No image has been built,
uploaded, or deployed, and no runtime, GPU, Router, or production gate is
inherited from this result.

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
- [x] re-review the plan against the first Slice A implementation, correct
  lifecycle/reset/efficiency flaws, and eliminate throw-away old-path migration.
- [x] commit and push the new authority plan before production-code changes
  (`5bb0ef2`).
- [x] Slice A candidate passes the c21 r4 source-acceptance matrix.
- [x] Slice A exact source commit `a86d4c1` is pushed and recorded.
- [x] Slice B candidate passes the complete c21 source matrix without modifying
  the old Manager/Policy or production HTTP path.
- [x] the exact Slice B source commit `caa0138` is pushed and recorded.
- [x] Slice C atomic HTTP/observer cutover passes; old Manager, Policy, Adapter,
  shadow naming, request cost, and duplicate observation code is deleted and
  exact commit `c2e58cc` is pushed.
- [x] Slice D complete source acceptance and three code reviews pass on exact
  pushed commit `c2e58cc`.
- [x] assign the initial `PIG-v0.12.11` identity on exact pushed commit `16d1940`,
  build its local-only image, and pass initial primary smoke before the
  long-input estimator failure rejected it.
- [x] rebuild and re-accept the estimator on exact pushed commit 8ddd87b after
  the real repeated-short-lexeme 4x underestimation rejected v0.12.11 and
  forced PIG-only rollback.
- [x] assign the replacement `PIG-v0.12.12` identity on exact pushed commit
  `74b5c76` after proving the Git and registry tags were unoccupied.
- [x] build one exact v0.12.12 local-only image without a registry digest and
  pass isolated startup/capability, request-preservation, and shadow/enforce
  decision/forward-causality gates without changing the primary PIG or vLLM.
- [x] complete c21 PIG-only compatibility, lifecycle, real and fake long-input,
  low/no-flow, Router-pressure visibility, reset, stale, drift, and timeout
  gates while retaining the v0.12.10 rollback asset and leaving vLLM unchanged.
- [x] complete matched c21 QoS, completion-goodput, and Pareto repetitions.
- [x] complete independent pre-version source/evidence audit on exact clean
  pushed HEAD `24654d6`.
- [x] upload only the exact accepted digest and verify all published references.

## 13. Stop rules

Stop and fix the architecture rather than adding a compatibility patch if:

- a decision depends on state not owned by the Controller;
- selection tokens travel outside the canonical request record;
- a new public reserve/no-reserve or external-observation decision variant is
  proposed;
- a stale handle can address a reservation without its Controller epoch and
  monotonic internal ID;
- a forwarded terminal can erase work that no covering observation has ever
  absorbed;
- a recoverable same-capability backend reset requires a PIG process restart;
- Admit or Snapshot scans every live reservation instead of using the checked
  aggregate;
- an old Manager, Policy, or Adapter is modified merely to bridge into the new
  core before its atomic deletion;
- HTTP/log/metrics/Router derive different action, reason, or scope;
- metrics/status influence admission state;
- a model asset, tokenizer RPC, cache lookup, learning loop, TTFT Gate,
  cooldown, or request mutation reappears;
- a test passes only by retaining a private field, legacy alias, or release
  version name; or
- image, deployment, or production claims exceed the exact completed evidence
  layer.
