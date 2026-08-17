# PIG v0.12.13 Sustained TPS Reference and Branch Cleanup Plan

Status: active design and implementation plan; no v0.12.13 release identity,
image, deployment, or production evidence exists yet.

Authoritative baseline: branch `codex/pig-v0.11.0-request-aware`, commit
`53cb1d5abef55096c2a13dfa0193c257e64bd397`, whose executable v0.12.12 tag is
`bc9513117c36f1021896e17825289c94945b79e5`.

Target source line: v0.12.13. The patch number is not assigned to runtime or
OCI identity until the exact source passes the complete acceptance matrix.

## 1. Goal

Clean the active branch so current source and current documentation describe one
architecture, then add one optional production policy value:

```text
PREDICTIVE_TPS_REFERENCE=<output tokens per second per active Decode sequence>
```

When configured, PIG must use a sustained, qualified Decode observation to
limit future admission before the request reaches vLLM. The objective is to
keep the long-run mean active TPS at or above the reference while retaining as
much QoS-compliant throughput as possible.

The reference is a policy target, not a physical capability, learned model, or
instantaneous reject threshold. KV geometry and Prefill bands remain immutable
startup-derived capability. Feedback may update only the next pre-forward
decision.

## 2. Non-goals

This change does not:

- restore the deleted dynamic QoS, yellow/red TPS thresholds, cooldowns, holds,
  learned global limits, TTFT gates, tier/priority injection, or request rewrite;
- route between backends or inspect prefix-cache contents;
- promise a per-request TPS value from metrics that cannot identify individual
  requests;
- run an active completion, warmup, calibration, or performance probe;
- change vLLM, Router, Compose, a running CVM, or production traffic;
- publish an image before source, race, simulation, QoS, and Pareto gates pass.

## 3. Current-source findings

The v0.12.12 admission transaction is coherent and atomic:

```text
bounded request estimate
  -> current observation plus positive reservation overlay
  -> ContextGate / KVGate / PrefillGate
  -> immutable decision
  -> atomic reservation before forward
  -> first-byte and terminal lifecycle reconciliation
```

`GenerationDelta`, `ObservationInterval`, current running, and previous running
already enter `ProjectedState`. Current metrics calculate aggregate and mean
active TPS proxies, but no gate consumes them. This is deliberate v0.12.12
behavior, not a missing wire.

The old v0.12.6 Decode Envelope charged:

```text
post_admit_prefill_tokens * active_decode_sequences
```

against one fixed Prefill budget. It had no configurable TPS target, no
sustained average, and no measured Decode-capacity envelope. Its later Pareto
evidence rejected it. It must not be resurrected.

## 4. Branch cleanup

### 4.1 Delete completed or superseded plan histories

The following files are not referenced by executable source and describe
retired algorithms or mixed chronological evidence. Git history and immutable
release commits remain their archive:

- `KV_ADMISSION_V0_9_PLAN.md`
- `PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`
- `TOKENIZER_FIRST_PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`
- `MODEL_AGNOSTIC_APPROXIMATE_ADMISSION_V0_10_PLAN.md`
- `PREDICTIVE_MARGINAL_GOODPUT_OPTIMIZER_V0_11_PLAN.md`
- `REQUEST_TIER_PRIORITY.md`
- `WAITING_POLICY.md`
- `IMMUTABLE_BACKEND_CAPABILITY_PROFILE_V0_12_PLAN.md`
- `PREDICTIVE_ADMISSION_V0_12_1_CORRECTION_AND_LIVE_VALIDATION_PLAN.md`
- `PIG_V0_12_3_QOS_CONSTRAINED_GOODPUT_REDESIGN_PLAN.md`
- `PIG_V0_12_CLEAN_ADMISSION_REBUILD_PLAN.md`

The retained documentation surface is:

- `README.md`: product and production contract;
- `ADVANCED.md`: typed configuration and test overrides;
- `OBSERVABILITY.md`: logs, metrics, and Router projection;
- `PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md`: current code path;
- this plan: the only active design/progress ledger.

### 4.2 Delete unreachable executable support

`internal/runtime/semantic/observer.go` has no inbound import and belongs to the
retired semantic-TTFT path. Delete it. Retain backend TTFT metrics as telemetry
because they are part of the generic backend observability surface and do not
affect admission.

### 4.3 Rename version-scoped contract tests

Rename `config_v0121_legacy_removal_test.go` to a version-neutral configuration
contract test. Preserve the assertions that retired environment variables and
Config fields cannot silently re-enable old modes. Test names must describe the
current contract rather than historical patch numbers.

### 4.4 Do not rename stable wire labels casually

`request-aware-capability-v3` and existing Prometheus names are externally
visible compatibility labels. Their historical wording is not proof of dead
execution. Retain them unless a separately versioned migration proves all
consumers.

## 5. TPS reference semantics

### 5.1 Configuration

`PREDICTIVE_TPS_REFERENCE` is a finite non-negative float:

- absent or `0`: TPS admission is disabled and v0.12.12 admission behavior is
  preserved;
- positive: sustained TPS admission is enabled;
- negative, NaN, infinity, or a value above `1_000_000`: startup validation
  fails. The upper bound is defensive parsing/overflow hygiene, not a claimed
  model capability.

No second production knob is required. The source-owned controller constants
are:

```text
window duration                    60 seconds
minimum qualified samples          4
minimum qualified sequence-seconds 8
healthy exploration headroom       5 percent
exploration projected TPS floor    95 percent of the reference
maximum exploration                1 sequence above the rate-derived base limit
warming sequence limit             2, or the larger observed raw-running value
```

These constants are algorithm semantics and must be documented and tested. They
are not copied into normal production Compose.

### 5.2 Qualified Decode sample

For two consecutive coherent observations in the same runtime epoch, the
Controller also snapshots the PIG-owned active-Decode reservation count at each
observation boundary:

```text
interval_seconds = current.observed_at - previous.observed_at
active_sequences = max(
  previous.running,
  current.running,
  previous.local_active_decode,
  current.local_active_decode,
)
generated_tokens = current.generation_total - previous.generation_total
```

A sample is qualified only when:

- the runtime epoch did not reset;
- the interval is positive and no larger than the configured maximum metrics
  age;
- `active_sequences > 0`, except that a positive generation delta observed
  entirely between polls uses a conservative minimum of one sequence;
- either `generated_tokens > 0`, or PIG had a known active Decode reservation
  at one of the two observation boundaries.

This distinction is intentional. A zero-generation interval with a known
active Decode reservation is genuine Decode-stall evidence and contributes
zero tokens plus its sequence-seconds. A zero-generation interval with no
known Decode is ignored because vLLM `running` also includes Prefill, so a pure
Prefill interval is not evidence of a user's Decode rate. Prefill interference
remains protected by PrefillGate. A positive generation delta with zero running
at both endpoints represents completion between polls; counting it with one
sequence keeps the aggregate-rate evidence instead of silently losing fast
requests. Counter resets clear the TPS window. Missing, stale, or invalid
observations retain existing fail-closed availability behavior.

### 5.3 Sustained statistics

The Controller owns a fixed-size, one-second-bucket trailing window. Publication
updates it under the same lock as the coherent observation. A qualified sample
is split proportionally across the one-second buckets it overlaps; the split is
bounded by the already validated maximum metrics age and performs no heap
allocation. Publication and request-time snapshots expire buckets against the
same observation/request clock; request snapshots exclude expired buckets
without mutating Controller policy state, so a bucket cannot remain authoritative merely
because no qualified Decode sample arrived. Whole-bucket expiry has less than
one second of bounded retention at the trailing boundary; this is preferable to
request-path allocation or an unbounded sample list and is covered by tests. The
window maintains checked, bounded totals:

```text
qualified_tokens
qualified_active_seconds
qualified_sequence_seconds
qualified_sample_count

aggregate_tps = qualified_tokens / qualified_active_seconds
mean_active_tps = qualified_tokens / qualified_sequence_seconds
```

The tracker becomes ready only after both minimum sample count and minimum
sequence-seconds are satisfied. Buckets older than 60 seconds expire. There is
no unbounded map, request identity, learned parameter, background goroutine, or
request-path allocation.

### 5.4 Sustainable Decode sequence envelope

When ready:

```text
base_sequence_limit = floor(aggregate_tps / tps_reference)
base_sequence_limit = max(base_sequence_limit, 1)

candidate_exploration_tps = aggregate_tps / (base_sequence_limit + 1)

if mean_active_tps >= 1.05 * tps_reference and
   candidate_exploration_tps >= 0.95 * tps_reference:
    sequence_limit = base_sequence_limit + 1
else:
    sequence_limit = base_sequence_limit
```

The rate-derived base is a conservative fixed-aggregate-throughput projection,
so a same-snapshot burst may fill unused slots up to that already observed
capacity. The one-sequence increment is the only unproven exploration above
the base. Requiring the fixed-rate counterfactual for that extra sequence to
remain within five percent of the reference prevents one long-lived exploratory
Decode from creating an unbounded below-reference period. The step still
prevents the controller from treating the current concurrency as a permanent
ceiling while bounding both count and predicted TPS overshoot.

Before the window is ready, the gate uses a bounded warming limit:

```text
warming_sequence_limit = max(raw_running, 2)
```

This admits at most two total projected sequences from an idle or one-stream
start, allowing one batching observation without admitting an unlimited
same-snapshot burst. If PIG attaches while more than two upstream sequences are
already running, the warming limit preserves that population but does not add
to it. Once the window is ready, there is no recurring one-to-two exception;
the rate-derived base and bounded five-percent exploration are the only
expansion paths. Existing admitted work is never evicted.

The pre-admit current sequence estimate is:

```text
tracked_pig_sequences = pending_prefill_sequences + local_active_decode
current_sequences = max(raw_running, tracked_pig_sequences)
post_admit_sequences = current_sequences + 1
```

Using `max` avoids known double counting after a PIG reservation becomes visible
in vLLM `running`, while still covering a reservation not yet visible upstream.
Both the tracked sum and the post-admit increment are checked for overflow. PIG
remains a single-upstream admission owner; bypass traffic is outside this
estimate and must be excluded operationally.

### 5.5 Gate behavior

`TPSGate` is evaluated after Context, KV, and Prefill gates:

- disabled: it performs no sequence projection and does not protect;
- enabled but not ready: it admits only while `post_admit_sequences` fits the
  bounded warming limit;
- ready and `current_sequences == 0`: admit exactly one probe sequence;
- ready and `post_admit_sequences <= sequence_limit`: admit;
- otherwise: protect with reason `tps_reference` and load scope when the
  canonical minimum request also fails.

The idle exception is not an unlimited fail-open burst. During warming, atomic
reservations stop the third projected sequence. With a ready window, the first
reservation makes the next same-snapshot request satisfy the retained
rate-derived envelope. After a long enough idle period the 60-second evidence
expires and the gate returns to bounded warming; after only a brief idle period
it may refill capacity already evidenced by the still-current window.

There is no sticky degraded flag, cooldown, last-reject hold, or requirement to
wait for TPS to recover while idle. Capacity reopens as soon as current work
drains enough for the current canonical probe to fit.

## 6. Architecture and SOLID ownership

### 6.1 `tpsWindow`

Owns only bounded observation aggregation, runtime-epoch reset, expiry, and a
read-only `TPSSnapshot`. It does not know requests or admission actions.

### 6.2 `tpsGate`

A pure policy component. It accepts `TPSSnapshot` plus `ProjectedState`, derives
current/post-admit sequences, and returns one immutable gate decision. It does
not mutate the tracker or Controller.

### 6.3 `AdmissionController`

Remains the sole owner of observation publication, runtime epoch, reservations,
atomic policy evaluation, and the TPS window. It updates TPS only from a
qualified subsequent observation and includes the resulting snapshot in the
same pre-forward state used by the policy.

### 6.4 Configuration ownership

Physical KV/context/Prefill facts remain in `Capability`. The configured TPS
reference belongs to a separate admission policy configuration. Do not put a
business QoS target into immutable backend capability identity.

### 6.5 Server and observability

The server only maps typed environment configuration into the admission policy
and reports immutable decisions. It does not duplicate TPS formulas.

## 7. Decision ordering and scope

The candidate decision order is:

```text
ContextGate
  -> KVGate
  -> PrefillGate
  -> TPSGate
  -> admit and reserve
```

This preserves the most specific physical or request-size reason when multiple
constraints fail. The existing same-snapshot canonical minimum probe assigns:

- request scope when the candidate fails but a canonical minimum still fits;
- load scope when TPS pressure also blocks the canonical minimum;
- availability scope only for missing, stale, invalid, drifted, or closed
  controller state.

Router capacity must therefore close during sustained active TPS pressure and
reopen without a hold when the envelope fits again.

## 8. Shadow and enforce

Enforce rejects and reserves exactly as the current atomic contract requires.

Shadow computes the same TPS counterfactual and reports it, but a protected
request is forwarded without a hypothetical reservation and cannot reduce
Router capacity. The production default remains enforce; shadow is explicit
test configuration only.

## 9. Observability

Add bounded, low-cardinality fields to metrics and status/log decisions:

```text
pig_predictive_tps_reference
pig_predictive_tps_window_ready
pig_predictive_tps_window_qualified_samples
pig_predictive_tps_window_qualified_sequence_seconds
pig_predictive_tps_window_aggregate
pig_predictive_tps_window_mean_active
pig_predictive_tps_sequence_limit
pig_predictive_tps_current_sequences
pig_predictive_tps_post_admit_sequences
```

Decision reason `tps_reference` must appear consistently in rejection counters,
bounded decision logs, status, `/pig/metrics`, `/v1/metrics`, and Router capacity
projection. Existing instantaneous TPS proxy metrics remain diagnostic and must
not be relabelled as the sustained window.

Periodic status must print the current canonical capacity action/reason
separately from the last request decision. An observation-driven transition to
TPS protection must be visible even before another request arrives.

Metrics must expose disabled, warming, healthy, and pressure states without a
model, request, user, token, or prompt label.

## 10. Test-first implementation

### 10.1 Focused red tests

Against exact v0.12.12 executable behavior, add tests proving the absence of:

1. typed TPS-reference configuration and validation;
2. bounded qualified-window aggregation;
3. runtime-reset clearing;
4. a decision change when only the TPS reference changes;
5. atomic enforcement of the two-sequence warming limit and the rate-derived
   limit plus at most one exploration slot under a same-snapshot burst;
6. immediate idle one-probe recovery and bounded cold-start behavior;
7. TPS reason/log/metrics/Router projection consistency.

Red evidence is valid only when it fails for the missing behavior, not for a
fixture, compile, environment, or unrelated gate error.

### 10.2 Focused green tests

Required unit/property cases:

- disabled reference is byte-for-behavior compatible with v0.12.12 policy;
- finite positive configuration loads; invalid values fail startup;
- weighted mean uses sequence-seconds, not a mean of sample means;
- pure-Prefill zero-generation intervals do not create TPS debt, while a
  PIG-tracked active Decode stall does;
- missed/stale intervals are not qualified;
- one-second buckets expire and remain numerically bounded;
- reset clears the window and warming state;
- warming admits at most two total projected sequences, preserves a larger
  already-running population without adding to it, and cannot fail open for an
  unlimited same-snapshot burst;
- healthy state never admits above the rate-derived base plus one exploration
  slot in one observation state;
- below-reference state stops expansion but never evicts existing work;
- a ready one-stream state has no recurring special bootstrap below the bounded
  fixed-rate projection;
- ready idle permits one request and the atomic reservation enforces the current
  envelope;
- Context/KV/Prefill reasons retain precedence;
- canonical scope and Router capacity agree;
- streaming, non-streaming, cancellation, timeout, upstream error, duplicate
  lifecycle events, reset, and shutdown leak no reservations.

### 10.3 Complete source gates

Run only on the approved Linux c21 environment:

```text
gofmt inspection
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/phala-inference-guard
go test focused TPS/config/controller/server/metrics packages
go test deterministic simulation packages
hot-path and observer benchmarks
```

No executable Go test on the local Windows checkout is release evidence.

## 11. Deterministic simulation

Add explicit reference-enabled candidates while retaining the reference-disabled
v0.12.12 candidate as a behavioral baseline.

Required scenarios include:

- bounded one-to-two warming followed by rate-derived multi-stream step-up;
- aggregate throughput saturation where mean active TPS crosses the reference;
- same-poll burst before new metrics materialize;
- mixed small Prefill and long Decode horizons;
- long Prefill interfering with existing Decode;
- zero-flow, low-flow, and pure-Prefill startup;
- transient and sustained waiting;
- preemption and runtime counter reset;
- stale scrape and recovery;
- cancellation and completion between polls;
- near-KV capacity and 64K/256K/512K request classes.

For reference-enabled scenarios, report:

```text
raw completion-token goodput
QoS-qualified completion-token goodput
qualified mean active TPS
time below reference
admitted/protected requests
maximum active sequences
KV peak
preemptions
idle-with-demand
```

## 12. Acceptance criteria

Safety failures are absolute:

- no KV hard-limit violation, OOM proxy, new preemption, reservation leak,
  double release, epoch reuse, overflow, stale authorization, or self-lock;
- no protection after all active/pending work drains that prevents the first
  canonical request from entering;
- no same-snapshot admission above the computed TPS sequence limit.

QoS and throughput gates:

- in stable reference-enabled Decode windows with enough offered load, median
  qualified mean active TPS is at least the configured reference;
- brief individual low samples are permitted and are not standalone failures;
- sustained time below reference must improve versus v0.12.12 in targeted
  saturation scenarios;
- QoS-qualified output-token goodput must not regress against v0.12.12;
- among candidates satisfying safety and TPS gates, select the highest median
  raw goodput;
- maximum idle-with-demand remains at most one coherent observation interval;
- reference disabled preserves the accepted v0.12.12 simulation outputs and
  hot-path behavior within measured noise.

Run ordered baseline/candidate and candidate/baseline repetitions. One synthetic
simulation or microbenchmark cannot establish live OpenRouter ranking.

## 13. Production configuration contract

Normal production YAML remains small. A deployment that wants the new policy
adds one genuine value:

```yaml
environment:
  UPSTREAM: http://vllm:8000
  TOKEN: ${TOKEN}
  PREDICTIVE_TPS_REFERENCE: ${PIG_TPS_REFERENCE}
```

Do not copy window, sample, headroom, Prefill, KV, poll, freshness, or mode
defaults into production Compose. A reference value is deployment-specific and
must be chosen from the provider contract plus controlled evidence; it cannot
be auto-derived from `/v1/models` or KV metrics.

## 14. Three-pass review record

### Pass 1: model and causality

Finding: treating every `running > 0 && generation_delta == 0` interval as TPS
zero confuses pure Prefill/TTFT with Decode rate and can self-lock a cold node.

Correction: positive-generation intervals qualify, and zero-generation
intervals qualify only with PIG-owned active-Decode evidence. Pure Prefill
remains handled by the causal Prefill gate. The plan uses sequence-second
weighting and one-step exploration instead of an instantaneous TPS threshold.

Finding: vLLM `num_requests_running` does not identify Prefill and Decode phases,
so it cannot be presented as exact per-request Decode ownership during mixed
workloads.

Correction: treat running as a conservative scheduler-sequence proxy. It may
suppress exploration while Prefill overlaps Decode, but it cannot increase the
rate-derived base because that base uses aggregate generated tokens per wall
second. Zero-generation samples still require PIG-owned active-Decode evidence,
and live validation must compare this proxy with request-level streamed output
timing before claiming an OpenRouter TPS result.

Finding: dividing the current aggregate rate by `current+1` without exploration
would permanently underestimate batching capacity from a one-stream start.

Correction: a healthy window with five percent headroom gets at most one
exploration slot per coherent state, and only when the fixed-rate TPS projected
for that extra slot remains at least 95 percent of the reference; reservations
make this atomic for bursts.

Finding: headroom on the current mean alone does not bound the duration or size
of the degradation caused by a long-lived exploratory stream. At aggregate
150 TPS and a 20 TPS reference, blindly moving from seven to eight sequences
predicts 18.75 TPS for the entire lifetime of that stream.

Correction: gate exploration on both current headroom and the base-plus-one
counterfactual floor. The 95 percent floor makes the tolerated excursion
explicit, fixed, and tested instead of treating every one-sequence overshoot as
equally harmless.

Finding: leaving TPS disabled until the window is ready admits an unlimited
same-snapshot cold-start burst, while a recurring one-to-two exception can put
two long-lived streams below the reference with no way for admission control to
revoke either stream.

Correction: warming admits at most two total projected sequences (or preserves
the larger observed `raw_running` population without adding to it). This gives
one bounded batching observation. Once ready, the recurring one-to-two
exception is removed and only the rate-derived envelope remains.

### Pass 2: safety, lifecycle, and SOLID

Finding: adding raw running and all PIG active reservations double-counts work
after vLLM observes a reservation.

Correction: use the maximum of raw running and PIG pending-plus-active sequences.
The physical KV overlay remains unchanged and conservative.

Finding: a sticky below-reference latch cannot recover after traffic drains
because no new generation sample exists.

Correction: no latch or cooldown exists. Current zero work admits one atomic
probe regardless of retained historical pressure. Runtime reset clears the
window; ordinary idle does not fabricate a healthy sample.

Finding: TPS observation, gate, and server reporting could become three owners.

Correction: one Controller-owned window produces one immutable snapshot, one
pure TPSGate decides, and server code only maps configuration and reports the
decision.

Finding: ignoring every zero-generation interval avoids pure-Prefill false
positives but also hides a real stalled Decode stream that PIG already tracks.

Correction: qualify a zero-token interval only when a PIG reservation was in
active Decode at an observation boundary. Positive-generation requests that
start and finish between polls retain aggregate evidence with a one-sequence
minimum. This is bounded observation, not a learned model.

Finding: expiring the trailing window only when a qualified sample arrives can
leave old high-TPS evidence resident during idle or pure-Prefill periods.

Correction: expire fixed buckets on every observation publication and exclude
expired buckets from the read-only request-time snapshot, using the same clock
already checked for observation freshness. Runtime reset clears all buckets
before accepting the new epoch.

Finding: a current metrics-driven TPS protection can occur before another HTTP
decision, while the old periodic status line printed only the last request's
action and reason.

Correction: keep last-decision telemetry, but also print the live canonical
capacity action/reason in every periodic status line. Router backpressure
metrics already use that canonical decision and remain the authoritative
machine-readable signal.

### Pass 3: evidence and release boundary

Finding: the old plans mix superseded green, rejected candidates, deployment
history, and active instructions, making current authority ambiguous.

Correction: delete them from the active tree and retain history in Git. This
plan is the sole active ledger.

Finding: passing source tests does not prove registry, Compose, live TPS, or
OpenRouter rank.

Correction: keep source, image, deployment, traffic, and production evidence as
separate layers. Push explicitly labelled WIP source updates as required, but
assign release identity and upload an image only after the full c21 acceptance
matrix. Router and production changes require a later explicit step.

## 15. Progress ledger

- [x] exact remote v0.12.12 branch HEAD and executable tag identified
- [x] local dirty/stale checkout isolated and preserved
- [x] obsolete document and dead-code inventory completed
- [x] historical v0.12.6 Decode Envelope reviewed and rejected for reuse
- [x] TPS semantics and three design reviews recorded
- [x] obsolete documents deleted and current README links repaired
- [x] unreachable semantic observer deleted
- [x] version-neutral config contract test rename completed
- [x] focused precedence test reproduced red on c21 round 2 and green after the
  request-scope correction on round 3
- [x] TPS configuration/window/gate vertical slice drafted in source; c21 green
  evidence remains pending
- [x] log, metrics, status, and Router projection consistency drafted in source;
  c21 green evidence remains pending
- [x] deterministic reference-enabled scenarios and acceptance gates drafted in
  source; c21 execution remains pending
- [ ] focused/full/race/vet/build/benchmark matrix green on exact source
- [ ] three implementation reviews completed and corrections applied; the first
  static pass removed unsafe recurring low-flow bootstrap and added bounded
  warming
- [x] c21 platform state re-queried read-only on 2026-08-15; it is `stopped`
- [x] c21 platform state re-queried on 2026-08-17 after the owner started it;
  it is `running`, `in_progress=false`, and no CVM restart was performed by this
  source change
- [x] c21 executable gates resumed; round 1 stopped correctly at the formatting
  gate before focused tests
- [x] WIP source commit `1da2644` pushed to
  `pig-origin/codex/pig-v0.12.13-tps-reference`; it is explicitly unverified and
  is not a release identity
- [ ] version identity assigned only after all prior source gates pass
- [ ] local image, registry upload, Compose, deployment, Router, and live traffic
  remain unperformed

## 16. c21 execution record

### 2026-08-17 round 1: exact WIP source formatting gate

Environment and provenance:

- CVM: `c21b7281-2c25-4453-8a68-f39ec42d03b4`,
  `dstack-nvidia-dev-0.5.9`, `h200.small`;
- workbench: `golang:1.24-bookworm`, Go `1.24.13`, Git `2.39.5`, Linux
  `6.9.0-dstack x86_64`;
- isolated c21 repository:
  `/workspace/src/phala-inference-guard-v01213-tps`;
- exact source: `ca33096b806ff0b3dced580d000e09e2a5e3715f`, with upstream resolving to
  the same commit;
- evidence directory:
  `/workspace/evidence/pig-v01213-tps-focused-r1-ca33096`.

The ordered bootstrap runner cloned the exact branch, verified clean source and
HEAD, recorded metadata, then ran `gofmt -l` before any executable focused test.
It exited `20` because five files were not formatted:

```text
internal/admission/controller.go
internal/admission/projector.go
internal/admission/tps_gate.go
internal/admission/types.go
internal/app/server/admission_projection_contract_test.go
```

No focused test ran in this round, so it supplies formatting-failure evidence,
not Linux test evidence. c21 `gofmt` produced a mechanical 23-line alignment
change in each direction; the audit patch SHA-256 is
`ca2ddd712a0a87132d04d546b5f28ce25292b3ed63949fe23115b5d1a0cecdba`.
The same change was applied to the isolated local branch with no semantic code
change. It must be committed, pushed, pulled onto c21, and all gates restarted
from formatting before this round can advance.

### 2026-08-17 round 2: focused test exposed request/load scope coupling

The formatting correction and round 1 record were committed and pushed as
`eef3c051aa7393fbb524631d8b355c43ad001a3e`. A fresh c21 clone verified that
exact clean HEAD, passed `gofmt -l` and `git diff --check`, then ran all five
focused package commands. Results:

```text
go test ./internal/admission                    FAIL
go test ./internal/config/pigconfig             PASS
go test ./internal/app/server                   PASS
go test ./internal/observability/metrics        PASS
go test ./internal/simulation/requestaware      PASS
```

The admission failure was
`TestPolicyPreservesPhysicalGateReasonPrecedenceOverTPS`. The returned reason
remained the correct request-intrinsic `input_limit`, but its scope became
`load` because `admissionPolicy.evaluate` re-evaluated the minimum request
through every later gate; the unrelated TPS gate then protected that minimum.
This coupled an individual invalid/oversized request to current node capacity
and could make request telemetry imply a global closure.

Correction: return `request` scope immediately for `input_limit` and
`invalid_request`. Other candidate-dependent gates retain the existing minimum
request comparison, while current node TPS capacity remains independently
represented by the Controller's canonical minimum projection. The gate order,
physical safety decisions, TPS decision, and reservation path are unchanged.
The correction and clearer test failure message are pending commit and c21
round 3.

Round 2 evidence is under
`/workspace/evidence/pig-v01213-tps-focused-r2-eef3c05`. Relevant SHA-256:

```text
focused-admission.log  6bb3769a34b826fafb993c869937ac1703cc854cfed798fbb24cb688f8620fa1
focused-config.log     f311a7ffcbd3392c2f65d0c08c752471e4a0afedae41607177f0278a6b295d8a
focused-server.log     c53ca36ba2b0cb181e1eea66e83082d91a5134bb5a0725029623de9b3604a339
focused-metrics.log    7f94a28d4460d5e6e95d8c672fd27aacef8074b8e7873b786500ae120fdcfa78
focused-simulation.log a715423f087cb15864ec36449c030164bf0c6d7e5022f44045c369e2b0ebee12
```

### 2026-08-17 round 3: focused matrix green

The request-scope correction and round 2 record were committed and pushed as
`2bbc29767a3645bc734a126e6f875cf29d027f82`. A third fresh c21 clone verified
that exact clean HEAD and upstream, then passed the ordered formatting and
focused gates:

```text
gofmt -l over tracked Go files                    PASS (empty)
git diff --check                                  PASS
go test ./internal/admission                      PASS (0.054s)
go test ./internal/config/pigconfig               PASS (0.003s)
go test ./internal/app/server                     PASS (1.448s)
go test ./internal/observability/metrics          PASS (0.003s)
go test ./internal/simulation/requestaware        PASS (0.194s)
```

This closes the focused Linux gate only. Full tests, race, vet, build,
deterministic named simulations, measured hot-path latency, implementation
reviews, and release identity remain pending. Evidence is under
`/workspace/evidence/pig-v01213-tps-focused-r3-2bbc297`; SHA-256:

```text
focused-admission.log  b86a56c85dcc483e603afde314b560b760baf1eff639be709615797b591d1fa0
focused-config.log     f311a7ffcbd3392c2f65d0c08c752471e4a0afedae41607177f0278a6b295d8a
focused-server.log     c53ca36ba2b0cb181e1eea66e83082d91a5134bb5a0725029623de9b3604a339
focused-metrics.log    7f94a28d4460d5e6e95d8c672fd27aacef8074b8e7873b786500ae120fdcfa78
focused-simulation.log 5c9ac3656da68c9c57fcad6bb0b03429c3371d9a849c2131e56e28358cd59351
metadata.log           6ef7a1e0bef2a8a85a7f53d16eaa96201d6ebd7f5737589e81eabdf6e1e7caa1
```

### 2026-08-17 round 4: complete source matrix green

The round 3 record was a documentation-only commit, producing exact clean HEAD
`8e3be675bcdb65fe80ff9ffdee3bfe449cb2a37f`. A fresh c21 clone then passed:

```text
go test -count=1 ./...                                      PASS
go test -race -count=1 ./...                                PASS
go vet ./...                                                PASS
go build -o <evidence>/phala-inference-guard \
  ./cmd/phala-inference-guard                                PASS
go test ./internal/simulation/requestaware \
  -run TestTPSReference -count=1 -v                          PASS
go test ./internal/admission \
  -run 'TestController.*(HotPath|TPS)' -count=1 -v           PASS
```

Measured non-race hot path on c21:

```text
reservations=256   snapshot p99=447ns admit p99=449ns allocations=0/0
reservations=4096  snapshot p99=393ns admit p99=380ns allocations=0/0
TPS enabled        snapshot p99=596ns admit p99=601ns allocations=0/0
```

These are Controller microbenchmarks, not end-to-end serving latency. They prove
the admission hot path is bounded and well below the 100-microsecond source
gate; they do not prove production TPS or OpenRouter rank. Evidence directory:
`/workspace/evidence/pig-v01213-tps-full-r4-8e3be67`. SHA-256:

```text
full-tests.log          d2b82236e47d686d4bf113d8d81d6c9479c0e4f8dfdfebb5ccfa27f5ba8ba1ad
race-tests.log          ce1df6935e6625949e4470b583fc44adfdbeab82391c7cfa275a3f636a04cab8
vet.log                 28659d473fa851406fff918f1c50a39cc858c8ce4bc3b3f3553cb253450a8564
build.log               e72be7dbb4cb6c7da1a0fab91c2f9dade6738e620ab884172197fc24b7805a5e
tps-simulations.log     f51e0b8f37e3a336d39baf9d42701c6f275f929d33a3109819452832c73746a1
hot-path.log            320f5fe996cdbd5bbc8b6505f8cf83ec4527072df0f967dcbc2d1573c7a68329
phala-inference-guard   bf8b1b48870718b08881fab35bc4ff6e28cd0775334ed0e35e7a4ec4fc31400f
```

### 2026-08-17 implementation review pass 1: model, causality, and evidence quality

Production-path result: the positive reference is consumed before forwarding;
the Controller lock owns observation update, window snapshot, post-admit gate,
and reservation; raw vLLM running is used only as a conservative sequence proxy.
No disconnected learner, retrospective TPS reject, or unbounded state was found.

Review correction 1: the saturation test compared raw completion tokens but did
not independently assert QoS-qualified goodput, and its verbose output did not
show baseline/candidate metrics. Add an explicit non-regression assertion for
`SLOCompletionTokens` and emit both metric records.

Review correction 2: simulation self-lock accounting only started an
idle-with-demand interval for request/size protection. TPS is a load protection,
so the check could miss the exact low-flow self-lock it is meant to detect.
Count every hard-fitting rejection when the simulated node has neither
background nor active work.

Review correction 3: positive-reference simulations covered saturation and
bounded warming but not the representative pressure matrix listed in this
plan. Add one table-driven safety run over mixed traffic, same-poll bursts,
transient/sustained waiting, near-KV load, preemption, stale recovery,
cancellation, completion-before-poll, and representative regular, weighted,
exclusive, 512K, and 650K Prefill classes. These are test/evidence corrections;
the production algorithm is unchanged unless c21 exposes a failure.

Because these corrections change executable test code and simulation
accounting after round 4, round 4 is retained as prior green evidence but is not
the final release gate. Commit and push the corrections, reproduce them on c21,
then rerun the applicable complete matrix.
