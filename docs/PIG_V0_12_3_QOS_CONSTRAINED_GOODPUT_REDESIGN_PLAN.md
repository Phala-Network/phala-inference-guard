# PIG v0.12.7 Evidence-First QoS-Constrained Goodput Remediation Plan

Status: v0.12.6 failed the ordered Pareto gate and remains unpublished; the
exact v0.12.7 source correction and immutable local image passed their complete
dedicated-CVM gates; targeted GPU, Pareto, upload, and production gates remain
open

This is the only execution plan for the active PIG v0.12.7 remediation. Sections
8 through 24 retain the v0.12.3 design, publication, and failed controlled-live
history; section 25 and later supersede that candidate. Section 29 supersedes
every earlier instruction to use the shared builder or the old `use1-cb` CVM as
the active development environment. Older v0.12 plans and superseded candidates
are evidence, not required behavior. Detailed commands, complete logs, retries,
and live observations belong in ignored evidence artifacts; their authoritative
identities are recorded here.

## 1. Goal

Maximize SLO-compliant completed-token goodput while preventing admission-caused
KV exhaustion and preemption, bounding per-user Decode TPS degradation, and
keeping an available upstream work-conserving.

Every authoritative decision happens before forwarding the request. Backend
feedback may update the next observation used by a decision, but it must not be
the only protection and must not create learned parameters, reject cooldowns,
or sticky state.

The candidate stays in the v0.12 release line and is version `0.12.7`.

## 2. Current conclusion

The exact v0.12.3 source and image completed their builder and publication
gates, then failed the Router-disabled weighted-Prefill QoS diagnostic in
section 25 and are not promotable. The v0.12.4 executable source at
`19574b9f9711886c3362c612317d7d64a2167798` completed the source and immutable
image gates and passed the dedicated weighted-Prefill gate. It is nevertheless
not promotable: section 33 and section 34 prove that its default startup
calibration actively sends synthetic Prefill work and derives materially
different policy from cold-JIT versus warm-backend samples. The v0.12.4 Pareto
matrix is cancelled rather than treating that nondeterminism as a benchmark
variable.

The v0.12.5 correction removed synthetic startup completions and the derived
safe-rate state. KV geometry and model metadata are read once; a pure, bounded
geometry function derives Prefill classes without learning or active performance
calibration. Its source and local-image gates passed, but section 38 proves that
the two-gate policy admitted a 49K weighted Prefill while four Decode users were
active and reduced their output rate to only `7.18%--7.80%` of the immediately
preceding baseline in three repetitions. The exact local image is unpublished
and v0.12.5 is rejected before near-KV, stale, Pareto, or production promotion.

The v0.12.6 correction kept the passive v0.12.5 initializer and added a
fixed Decode-interference envelope derived from the immutable `regular` Prefill
geometry. It bounds total pending Prefill work multiplied by the Decode users it
can disturb. It does not learn, probe, persist a measured rate, add a cooldown,
or create a production configuration knob. A request rejected only by this
envelope is request-scoped: its HTTP response, decision log, and metrics expose
the protection, while Router intake remains open for smaller requests. The
ordered matrix in section 43 proved that its Manager incorrectly treated every
Prefill-incomplete reservation as both pending Prefill work and an active Decode
user. It therefore rejected fitting short work before any request was running
upstream and is not promotable.

The active v0.12.7 correction keeps the same immutable geometry, resource gate,
Prefill gate, and Decode envelope. It changes only ownership of the envelope's
active-Decode input: backend-observed `running` remains the conservative
observed upper, and only a Prefill-complete local reservation not yet absorbed
by an observation adds an unobserved Decode sequence. A Prefill-incomplete
reservation continues to charge atomic KV and pending-Prefill demand but cannot
also claim to be an active Decode user. No learner, probe, cooldown, new public
configuration, or request mutation is introduced. Production `use1-cb` remains
Router-disabled until a new exact image passes every source, GPU, and ordered
Pareto gate and a later explicitly authorized canary begins.

The `one regular credit per 500-ms observation` Decode pacer is rejected as the
default design. It limits the rate of growth but not the final Decode
concurrency. It can therefore reject a same-observation burst while still
growing across observations until delayed waiting appears. The credit value is
not derived from vLLM capacity or controlled GPU evidence.

The r17 simulation is valid evidence for Manager/pacer lifecycle wiring, but not
for policy goodput. Its scheduler model currently:

- counts every admitted active request as running;
- separately derives waiting from the same active set;
- reconciles `DecodeSequences` as running plus waiting, double-counting queued
  work; and
- gives Decode service to every ready request even when some are reported as
  waiting.

Consequently, r17 cannot decide whether v0.12.3 or v0.12.2 has better QoS or
goodput. Production policy must not be changed merely to satisfy those numbers.

## 3. Product contract

PIG remains a single-upstream admission proxy. This release does not perform
routing, cache lookup, exact model tokenization, request mutation, tiering,
priority injection, or online learning. TTFT is observed for diagnostics only
and is not an admission SLO.

Keep:

- a bounded model-neutral request-size estimator;
- startup-derived exact vLLM KV capacity and block size;
- initialization-only Prefill/KV capability parameters that remain immutable
  for the upstream identity epoch;
- fresh vLLM running, waiting, KV, generation, and preemption observations;
- atomic decision plus reservation and exact-once lifecycle release;
- a 500-ms default observation interval;
- production-default `enforce`, with `shadow` only when explicitly configured
  for tests; and
- coherent logs, metrics, status, and Router-compatible protection projection.

Production Compose should normally specify only the image, upstream and metrics
endpoints, authentication, and secrets. Default algorithm values must not be
written explicitly. Test deployments may override parameters and `shadow`.

## 4. QoS and objective

The primary metric is:

```text
SLO-compliant completed output tokens / wall-clock second
```

For each controlled workload, freeze the Decode QoS floor before policy
comparison at 85 percent of its matched no-enforcement per-user Decode TPS. The
Phase D `N` reference retains the same PIG proxy and estimator path in explicit
shadow, so only admission enforcement is removed. This allows bounded TPS loss
in exchange for higher aggregate goodput without turning a universal TPS number
into production configuration.

Also report raw completed-token TPS, per-user Decode TPS distribution,
preemptions, waiting, peak KV, completion counts by request-size class,
idle-with-demand time, rejections by reason, and local decision latency.

Controlled GPU comparisons run at least three repetitions in A/B and B/A order.
A candidate is promotable only when it is Pareto-safe against v0.12.2:

- no candidate-caused preemption, restart, or reservation/lifecycle failure;
- short-only median SLO-goodput is at least 98 percent of v0.12.2 and its
  declared lower per-user TPS quantiles remain above the frozen QoS floor;
- aggregate mixed, ordered, and long-Prefill median SLO-goodput is not below
  v0.12.2; and
- at least one material gain is present: 5 percent higher median SLO-goodput,
  fewer preemptions, or fewer QoS-violation seconds without lower goodput.

Report every repetition and order rather than only the best run. Synthetic
coefficients alone cannot satisfy this gate.

## 5. Minimal candidate architecture

The first candidate is deliberately small:

```text
bounded request inspection
  -> immutable request cost
  -> coherent backend observation plus unabsorbed reservations
  -> resource-fit gate
  -> Prefill-interference gate
  -> atomic admit-and-reserve or reject
```

There is no production Decode pacer in the first candidate.

### 5.1 Request cost

Produce independent values:

- `estimated_prefill_tokens`: fast approximate input size used for Prefill
  classification and budgets;
- `reserved_kv_tokens`: the larger of the structural input upper and lexical
  hint, plus bounded requested output horizon, rounded to the upstream KV block
  size.

The estimator must be bounded, model-neutral, and cheap. A deterministic shape
corpus covers ASCII, code, punctuation, CJK, escaped JSON, tool schemas,
repeated text, and supported multimodal shapes. The runtime estimator is not
described as exact, and unsupported or unbounded shapes receive an explicit
conservative request-scoped fallback.

Body-read duration, estimator CPU duration, policy duration, and total
pre-forward duration remain separate metrics.

### 5.2 Resource-fit gate

The unconditional capacity equation is:

```text
observed_kv
+ unabsorbed_reserved_kv
+ candidate_reserved_kv
<= immutable_usable_kv_limit
```

`immutable_usable_kv_limit` is initialized from vLLM KV token capacity and block
size with explicit headroom for observation age and estimator error. Capacity
identity drift, stale/unavailable metrics, overflow, duplicate request ID,
shutdown, or an invalid request cost fails safely with a distinct reason.

Check, decision, and reservation are one Manager transaction. Completion,
error, cancellation, disconnect, timeout, rollback, upstream epoch change, and
shutdown release exactly once.

### 5.3 Prefill-interference gate

Use request size to distinguish simultaneous requests rather than applying one
global request-count decision:

- regular requests use a bounded aggregate regular-Prefill budget;
- weighted requests require remaining aggregate Prefill budget;
- exclusive requests require no other long Prefill;
- quiescent requests require an idle Decode/Prefill state; and
- a pending quiescent Prefill blocks other large candidates but does not create
  a global lock against a hard-fit regular request.

Prefill thresholds and budget are initialized once from trustworthy upstream
capability evidence or a conservative fallback, then remain immutable. They are
not learned from production requests. Observed waiting or a newly increased
preemption counter in the current fresh observation may tighten large-request
admission. The next coherent observation without another increase clears the
preemption signal; regular-request behavior remains explicit, request-size
aware, and independently recoverable.

### 5.4 Decode QoS decision point

Do not infer the marginal TPS of a candidate by dividing current aggregate TPS
by `N+1`; that assumes unchanged aggregate throughput and immediate Decode.
Generation TPS is an outcome metric until a causal predictor exists.

First test the two-gate candidate without a Decode controller. If controlled
Router-disabled GPU evidence shows unacceptable regular-request Decode
degradation, add one immutable admission envelope in a new focused red/green
cycle. The envelope must:

- be derived from a trustworthy vLLM-exposed capacity or a bounded
  Router-disabled initialization experiment, never from an arbitrary per-poll
  credit;
- include unabsorbed local reservations in the pre-forward counterfactual;
- bound total outstanding work or concurrency, not only its growth rate;
- remain fixed for the upstream identity epoch and not learn online; and
- recover on terminal events and fresh observations without a cooldown or a
  new business request.

If no trustworthy capacity source can be obtained, keep the envelope test-only
and do not invent a production value. A failed two-gate GPU experiment is then
evidence that v0.12.3 is not promotable, not permission to tune the simulator.
Any future active performance experiment is test-only and may run only while
Router is disabled. The v0.12.5 production initializer never sends such a
probe, persists its result, or reuses it after an upstream identity change.

## 6. SOLID ownership

- `request.Classifier`: body lifecycle, protocol syntax, and request-cost input;
- `kvadmission.Estimator`: model-neutral size/KV estimate;
- `predictive.ResourceGate`: capacity identity and hard KV fit;
- `predictive.InterferenceGate`: Prefill classification and budget;
- optional future `predictive.DecodeEnvelope`: only the proven immutable Decode
  envelope;
- `predictive.Manager`: atomic observations, reservations, reconciliation, and
  lifecycle;
- server adapter: HTTP translation and gate composition;
- observer: one coherent vLLM snapshot;
- reporting: logs, metrics, status, and Router projection only.

Policy code must not know HTTP statuses, request bodies, Router fields, or
Prometheus formatting. Reporting must expose the authoritative decision instead
of recomputing it. Do not add a generic plugin, learner, cache, or policy
framework without a second real implementation.

## 7. Simulation contract

Simulation proves deterministic safety, lifecycle, and qualitative policy
behavior. It does not prove GPU throughput.

Before comparing policies, repair and test these scheduler invariants:

1. backend states are mutually exclusive: queued, scheduled Prefill, and
   scheduled Decode;
2. `running` and `waiting` never describe the same request;
3. waiting requests consume neither Prefill nor Decode service until scheduled;
4. observed `running + waiting` equals backend-owned unfinished requests;
5. Manager reconciliation receives each sequence once;
6. reservations proven materialized are not charged twice; reservations whose
   attribution is ambiguous remain conservative and are reported as such;
7. actual input, estimated Prefill, and conservative KV reservation remain
   separate values; and
8. no-admission, frozen-v0.12.2, and candidate policies share the identical
   backend engine, arrivals, service order, and throughput model.

Run three baselines:

- no PIG admission;
- a test-only frozen v0.12.2 policy;
- the v0.12.3 production Manager path.

Simulation acceptance requires no oversubscription or reservation leak, exact
terminal behavior, deterministic repeat order, size differentiation, bounded
same-snapshot admissions through reservations, stale/epoch recovery, and no
idle-with-demand self-lock beyond one fresh 500-ms observation. One recoverable
protection is not itself a self-lock; persistent protection after the condition
clears is.

Synthetic goodput and TPS are reported as diagnostics and counterexamples only.
They are not required to improve by a hand-selected percentage.

## 8. Execution phases

Sections 8 through 24 record the completed v0.12.3 cycle. The active v0.12.4
execution order and release boundary are the corrective protocol in section 25;
no historical v0.12.3 completion item satisfies a v0.12.4 gate.

### Phase A: reset evidence and repair simulation

1. Freeze the exact source archive and preserve unrelated dirty files.
2. Add focused red tests for all scheduler invariants in section 7.
3. Repair the shared backend engine without changing production policy.
4. Re-run all three baselines on the approved remote builder.
5. Reclassify r17 results: retain lifecycle passes, discard policy-performance
   conclusions invalidated by the scheduler fix.

### Phase B: simplify the production candidate

1. Remove `DecodePacer` and all credit state from production Manager,
   reservations, logs, metrics, status, and Router projection.
2. Keep ResourceGate, InterferenceGate, atomic reservation, timing separation,
   and request-size reporting.
3. Delete superseded TPS target/floor forecasts, legacy policy modes, and dead
   compatibility fields rather than retaining parallel algorithms.
4. Add focused tests for hard KV fit, class-aware Prefill admission, burst
   reservations, cancellation, stale recovery, epoch changes, reporting, and
   HTTP enforce/shadow behavior.

### Phase C: builder-only release gates

Run executable checks only on the approved builder:

1. formatting and focused tests;
2. after focused green, set source version `0.12.3` and run complete tests, vet,
   race, and all affected focused tests again;
3. deterministic simulations in both policy orders;
4. model-neutral lexical-shape corpus covering ASCII, CJK, punctuation,
   escaped JSON, tools, multimodal markers, and the 650K text window;
5. estimator benchmarks at 1 KiB, 64 KiB, 1 MiB, and 4 MiB;
6. policy/Manager/full pre-forward benchmarks at 0, 48, and 256 reservations;
7. production binary, then a local immutable candidate image followed by that
   image's production-contract and smoke tests; and
8. three reviews: objective/causality, safety/lifecycle/SOLID, and
   efficiency/evidence/operability.

Relative to an exact v0.12.2 builder baseline on the same host and in both
orders, no hot path may add an allocation. Full HTTP pre-forward and Manager
paths with 48 or 256 reservations may regress by no more than 10 percent. For
sub-microsecond component boundaries, the pure Policy must remain below 250 ns
with at most 100 ns absolute increase, and the zero-reservation Manager below
500 ns with at most 100 ns absolute increase. This prevents ratio noise on a
20-100 ns helper from overriding end-to-end efficiency while still bounding the
SOLID decomposition cost. Absolute 0/48/256 full pre-forward and large-body
measurements are reported separately from network and GPU latency.

The bounded 4 MiB pathological many-short-string request may take up to 100 ms
in local estimation. Below that ceiling, correctness and bounded behavior take
priority over further micro-optimization; the normal 4 MiB large-string path
is still reported separately so this exception cannot hide a broad regression.

Any executable source change invalidates inherited executable evidence. After
each coherent code update passes its scoped formatting and focused tests,
commit and push it before the next implementation iteration; never push a known
broken intermediate state. On the final pushed source, first pass the complete
source matrix, then build one local immutable candidate image. Run the
production-image contract and smoke tests against that exact local image. Only
after both source and image acceptance are green may that exact image be
uploaded as the immutable canary. Record the source revision, local image ID,
registry digest, and pull verification separately.

### Phase D: Router-disabled GPU validation

Use only CVM `a0f0bfb3-e46f-4b22-814e-24872f251193` (`use1-cb`). Re-read live
state before every mutation and keep the Router upstream disabled.

1. Deploy the exact Phase C canary digest in explicit `shadow` for protocol,
   readiness, identity, transparency,
   metrics/log coherence, and counterfactual-decision diagnostics.
2. Deploy explicit `enforce` while Router remains disabled.
3. Run matched no-admission, v0.12.2, and v0.12.3 controlled workloads:
   short-only, long-only, mixed, reversed order, same-snapshot burst, near-KV,
   quiescent-plus-short, low flow, cancellation, stale/recovery, and sustained
   regular Decode concurrency.
4. Use nonce-separated cold inputs where practical. Because PIG does not inspect
   cache state, report any remaining cache effect as a confounder.
5. Evaluate the Pareto gate in section 4. If only Decode QoS fails, return to a
   focused immutable-envelope design cycle; otherwise fix the responsible gate
   or stop the release.

Do not publish or enable Router traffic from shadow evidence alone.

### Phase E: image and actual-traffic canary

After Phase D passes:

1. promote the exact Phase C/Phase D image digest as the v0.12.3 release without
   rebuilding it;
2. deploy production-default `enforce` with minimal explicit configuration
   while `use1-cb` remains disabled;
3. validate readiness, identity, request transparency, metrics/log/Router
   protection coherence, lifecycle drain, and absence of restart/preemption;
4. freeze the Router inventory and enable only `use1-cb`;
5. observe 30 uninterrupted minutes of actual traffic; and
6. disable only `use1-cb` on preemption, restart, stale self-lock, unexplained
   protection/429, short-request starvation, QoS breach, or Router drift.

Compare target-separated request mix, SLO-goodput, raw TPS, per-user TPS, KV,
waiting, protections, idle-with-demand, errors, and preemptions. If a material
defect remains, disable and drain the target, return to Phase A or B, and use the
next v0.12 patch. A 30-minute canary is provisional evidence, not general proof.

## 9. Progress ledger

- [x] Re-audited the current plan, architecture, r17 evidence, and goal.
- [x] Rejected the ungrounded one-credit Decode pacer as the default design.
- [x] Identified scheduler-state inconsistencies that invalidate r17 goodput
  conclusions.
- [x] Plan review pass 1: objective, causality, and evidence boundaries.
- [x] Plan review pass 2: safety, lifecycle, and SOLID ownership.
- [x] Plan review pass 3: efficiency, operability, and promotion gates.
- [x] Phase A scheduler-invariant red reproduced on the builder.
- [x] Phase A shared simulation engine repaired and green on the builder.
- [x] Phase B two-gate production candidate complete.
- [x] Historical v0.12.3 Phase C matrix, source push, and immutable image
  publication completed.
- [x] Historical v0.12.3 failed Phase D and was rejected from promotion.
- [x] v0.12.4 known-local weighted-or-larger Prefill focused red and r6 green.
- [x] v0.12.4 review found and reproduced equal-strength Router verdict
  precedence as the focused r8 red.
- [x] v0.12.4 final focused green after the Router verdict correction.
- [x] old shared-builder complete-matrix path cancelled and superseded by
  section 29; this is an execution-environment change, not a failed gate.
- [x] dedicated `h200.small` development CVM created and platform/GPU/Docker/
  SSH/Router-isolation checks recorded.
- [x] current 23-path tracked diff reconstructed exactly on Linux and the
  pending telemetry benchmark plus focused packages passed in the new CVM.
- [x] v0.12.4 complete dedicated-CVM source matrix green on the final executable
  source archive.
- [x] v0.12.4 executable source committed and pushed.
- [x] one local immutable v0.12.4 image built, production-contract and smoke
  accepted, then the same image uploaded and registry-pull identity verified.
- [x] v0.12.4 Router-disabled targeted weighted-Prefill gate passed.
- [x] v0.12.4 warm-backend PIG-only calibration diagnostic completed and proved
  cold-start sample contamination.
- [x] v0.12.4 Pareto matrix cancelled; the candidate is rejected from
  promotion before that gate.
- [x] v0.12.5 no-active-calibration focused red is valid.
- [x] v0.12.5 focused implementation, version, observability, and SOLID green.
- [x] v0.12.5 complete source/race/simulation/benchmark matrix and three reviews
  passed on the dedicated CVM.
- [x] one immutable v0.12.5 local image passed production contract and smoke.
- [x] v0.12.5 PIG-only runtime readiness and positive targeted GPU lifecycle
  checks completed on the dedicated CVM.
- [x] v0.12.5 failed the repeated Decode-QoS gate and was rejected before
  near-KV, stale, Pareto, registry upload, or production promotion.
- [x] v0.12.6 fixed Decode-interference envelope focused red is valid.
- [x] v0.12.6 focused implementation, Router scope, observability, version,
  efficiency, and SOLID green.
- [x] v0.12.6 complete source/race/simulation/benchmark matrix and three
  reviews passed on the dedicated CVM.
- [x] one immutable v0.12.6 local image passed production contract and smoke.
- [x] the exact local v0.12.6 image replaced only PIG on the dedicated CVM and
  passed runtime identity, readiness, authentication, and no-calibration gates.
- [x] v0.12.6 targeted GPU lifecycle, stale/recovery, near-KV, and repeated
  Decode-QoS gates passed on the dedicated CVM.
- [x] v0.12.6 no-enforcement/v0.12.2/v0.12.6 ordered Pareto matrix executed on
  the dedicated CVM and failed the short, required-scenario, and material-gain
  gates.
- [x] v0.12.6 raw results independently recomputed; the analyzer definition,
  continuation ordering, old-baseline calibration, and cache counters do not
  explain away the deterministic short-request over-protection.
- [x] v0.12.7 phase-correct active-Decode red tests fail against v0.12.6 for
  the intended reason.
- [ ] v0.12.7 focused implementation, source matrix, three reviews, and push
  completed on the dedicated CVM.
- [ ] one exact v0.12.7 local image passes runtime, targeted GPU, and complete
  ordered Pareto gates before upload.
- [ ] 30-minute `use1-cb` canary completed without a stop rule.
- [ ] final Router/CVM state and release conclusion recorded.

## 10. Review record

Pass 1, objective/causality, 2026-08-08: removed the assumption that a
one-credit pacer protects Decode QoS. The plan now starts from the smallest
request-aware gates and requires controlled GPU evidence before adding a Decode
envelope. Synthetic goodput is diagnostic, not a promotion result.

Pass 2, safety/lifecycle/SOLID, 2026-08-08: made scheduler states mutually
exclusive, kept admission and reservation atomic, preserved exact terminal and
epoch behavior, and separated request estimation, resource fit, Prefill
interference, state ownership, and reporting. A future Decode component has one
bounded responsibility and is introduced only with causal evidence.

Pass 3, efficiency/evidence/operability, 2026-08-08: removed the arbitrary
per-poll production knob, retained immutable upstream initialization and minimal
production configuration, separated builder/simulation/GPU/canary evidence, and
added explicit stop rules so a failed experiment cannot be made green by tuning
the synthetic model.

## 11. Phase B builder evidence

The focused two-gate candidate passed on the approved builder before the source
version changed:

```text
archive: pig-v0123-two-gate-green-r25-source.tar.gz
SHA-256: 7534807e10d241f2fbbc8093b6ed1dab88befcda91c0015a4ce6961972dea5e0
builder work: pig-v0123-two-gate-green-r25-7534807e

gofmt              0
compile             0
affected_packages   0
simulation_command  0
```

The tests executed the Manager burst, waiting-size differentiation, atomic hard
KV, HTTP regular-burst, and retired-metrics contracts. The candidate admitted
`pre-poll-burst` as `5/0` and bounded the 40-request regular multimodal Prefill
burst at `32/8`; deterministic acceptance passed. This evidence closes Phase B
only. The v0.12.3 version change starts a new executable evidence boundary for
Phase C.

## 12. Phase C r28 red evidence

The first versioned full matrix used candidate archive
`0c2c639cf6ad2099ffd67bc35713a93d66924c919c7415567b05b949d36fe910`.
All Go tests, vet, targeted and full race, builds, version checks, lexical shape
corpus, policy-order simulations, deterministic acceptance, estimator
benchmarks, and 0/48/256 reservation HTTP benchmarks passed. Two gates remained
red:

- the source archive included Git-untracked empty retired directories, so the
  legacy path audit correctly failed the artifact even though no retired file
  or symbol was present; and
- the first benchmark contract applied a relative 10 percent threshold to
  18-181 ns internal components. It found zero added allocations, 5.65 percent
  full HTTP regression, 7.94/6.73 percent Manager regression at 48/256
  reservations, and 45-63 ns absolute Policy/zero-reservation Manager
  decomposition cost.

The next archive is built from the explicit Git file inventory, not a workspace
directory walk. The revised performance gate above retains strict relative
limits at the request and high-reservation layers and explicit absolute limits
for nanosecond-scale component boundaries. r28 is red evidence and is not a
release candidate.

## 13. Phase C r30 green evidence for the superseded candidate

r29 stopped before executable validation because the builder BusyBox `patch`
does not implement `-d`. The runner was corrected to enter the baseline
directory before invoking `patch -p1`; no source or benchmark input changed.

The final Phase C inputs were:

```text
candidate archive: pig-v0123-phase-c-r29-source.tar.gz
candidate SHA-256: a5c410632c8ca7535d0b92662744ee22d5422c2a64fb94664543760b5fa67041
candidate files compared with current worktree: 158
candidate file mismatches before this documentation update: 0

v0.12.2 baseline archive: pig-v0122-baseline-5b1fe5b-source.tar.gz
baseline SHA-256: 96d38a1b9371e7af3fec445f87fcf6f2ecd8becb24c97b377cf934703444d0d9
baseline HTTP overlay SHA-256: 5666ac3dd9b65d7cdb42d3758f0f3d011861830dc5e6801ff89131bfec5a9642
baseline policy patch SHA-256: 1fcf0e0567dc7d80d8ab81e28d929ba1ec12b2df5a163e52d0e7b048f5648ae3
benchmark contract SHA-256: 964fc00ca6eda7626ba6c0167b4eee82df0dcd7447063ab89656c567755fb976

builder work: pig-v0123-phase-c-r30-a5c41063
container: pig-v01011-builder
Go: go1.24.13 linux/amd64
kernel: Linux 6.9.0-dstack x86_64
binary SHA-256: d9b3f8ef89f3b48bff01c2341dd53d3863ed33f4723d634a3216fcea49225814
statuses SHA-256: 91aeb622870dfd5e6d597e5b211c7899cbdc20310bc219fd5fd551b844e72816
logs archive: pig-v0123-phase-c-r30-logs.tar.gz
logs archive SHA-256: c6ceffde164872ab77132764e2d15fab84e85838685113cf81c2e63d5c73c09b
```

All 23 matrix status rows were zero: environment, formatting, legacy audit,
lexical shape corpus, affected packages, full tests, vet, targeted race, full
race, all-package build, versioned binary, policy-order tests, two simulation
runs, byte comparison, simulation acceptance, four ordered benchmark runs,
benchmark contract, absolute HTTP reservation benchmarks, and estimator
benchmarks. The two simulation JSON files were byte-identical with SHA-256
`69f6d6773c7a07537f8a641a2a8e4ca633c4eba468f01d0bfa0179dab35cbb97`.

The same-host, two-order benchmark contract reported zero added allocations on
every measured path:

| Boundary | v0.12.2 | v0.12.3 | Change |
| --- | ---: | ---: | ---: |
| full HTTP pre-forward | 13,378 ns | 13,707.5 ns | +2.46% |
| Manager, 48 reservations | 3,296 ns | 3,572.5 ns | +8.39% |
| Manager, 256 reservations | 17,248 ns | 18,498 ns | +7.25% |
| Manager, zero reservations | 121.45 ns | 180.30 ns | +58.85 ns |
| pure Policy | 30.09-40.71 ns | 83.27-85.88 ns | +45-53 ns |

Candidate absolute full HTTP pre-forward medians were about 13.8 microseconds
at zero reservations, 17.7 microseconds at 48, and 32.6 microseconds at 256,
with 33 allocations in all cases. The model-neutral lexical hint was about
98-103 ns with zero allocations. Estimator medians were about 284 ns at 1 KiB,
1.98 microseconds at 64 KiB, 27.7 microseconds at 1 MiB, and 158 microseconds at
4 MiB, all with zero allocations.

The deterministic simulation passed its safety acceptance but remains only a
diagnostic. Aggregate SLO-goodput was 74.324 tokens/s for no admission, 87.419
for v0.12.2, and 90.949 for v0.12.3. v0.12.3 admitted 206 of 270 arrivals versus
142 for v0.12.2 and had the same simulated preemption count of one, but its
waiting time was 80.6 seconds versus 5.0 and its TPS-floor violation time was
25.3 seconds versus 20.7. Those negative diagnostics prevent any claim that
Phase C proves GPU QoS or production superiority.

Phase C review pass 1, objective and causality: the real HTTP path consumes the
model-neutral size estimate before forwarding, combines it with the fresh
observation and every unabsorbed reservation, and changes the decision and
reservation under an otherwise identical snapshot. TPS observations are
diagnostic only. The simulation's goodput result and worse waiting/TPS
diagnostics are both retained; neither is a promotion gate.

Phase C review pass 2, safety, lifecycle, and SOLID: ResourceGate exclusively
owns stale/identity/overflow and post-admit KV fit; InterferenceGate exclusively
owns request-class Prefill rules; Manager owns the atomic decision and bounded
reservation state. Cancellation, disconnect, completion, failure, timeout,
shutdown, reconciliation, counter reset, and epoch invalidation paths passed
focused, full, and race tests. The estimator remains approximate and
cache-blind, and its bounded Decode horizon is a rolling headroom assumption,
not a prediction of final output length; Phase D must test that assumption on
the real 500-ms observation cadence.

Phase C review pass 3, efficiency, evidence, and operability: all measured hot
paths satisfy the allocation, relative, and absolute limits. Enforced rejects
are represented consistently in decision logs, PIG metrics, status, and the
Router compatibility projection; a fresh open observation can recover without
a business request or a long-lived cooldown. The executable evidence used only
the approved builder. No image, registry, Compose, CVM, Router, or production
claim is inherited from this Phase C result.

The third review then found that `PREDICTIVE_KV_TARGET_RATIO` and
`KVSoftLimitTokens` no longer influenced either production gate after the
v0.12.3 simplification. They survived only in configuration, capability
metadata, logs, metrics, and test fixtures. Keeping them would expose a tuning
control with no admission effect and violate the minimal production and SOLID
contracts. The dead soft-limit chain was therefore removed. This is an
executable source change, so r30 remains valid evidence for its exact archive
but cannot close Phase C for the final candidate. A new explicit-inventory
archive and complete builder matrix are required.

## 14. Phase C r31 red evidence

The first dead-parameter-free archive was intentionally evaluated as a fresh
candidate rather than inheriting r30 executable evidence:

```text
candidate archive: pig-v0123-phase-c-r31-source.tar.gz
candidate SHA-256: 4afc7be9a6bf956bf4130effe40d585ab6adb98561d6262ad1ac6f3f512242c0
candidate files: 158
retired archive entries: 0
runner SHA-256: 3a702212dbca406b018d2899eff8e02a9baf7419731155498d2d03a05de29e96
builder work: pig-v0123-phase-c-r31-4afc7be9
evidence manifest SHA-256: 4d38d72254d99806e85a4b81f9ceab8909835d77a330a59e8df6d0d492f29161
```

The remote process exited and wrote all 23 status rows. Formatting failed only
for one alignment change in `capability_profile_test.go`. Affected-package,
full-test, vet, targeted-race, full-race, and both candidate benchmark steps
failed because two Manager tests still called the retired
`newRequestAwareTestPolicyWithLimits` helper. The baseline benchmarks and all
independent production builds, version checks, lexical checks, simulations,
absolute HTTP benchmarks, and estimator benchmarks passed. The benchmark
contract failure was downstream of the candidate benchmark compile failure.

This is valid red evidence for a mechanical test migration defect, not an
algorithm failure. The two calls must retain only their former hard limits,
the builder-produced formatting diff must be applied, and the resulting source
must receive a new explicit-inventory archive and complete matrix. No r31
executable result closes Phase C.

## 15. Phase C r32 green evidence superseded by lexical review

The dead-soft-limit candidate received a complete green builder matrix:

```text
candidate SHA-256: 74ae3baa2459999cfefa7dc541a393c7686352825dad67aca645477c037c8bca
builder work: pig-v0123-phase-c-r32-74ae3baa
binary SHA-256: adaf4bc875e0cf24d87a2ce45dc5345f3fd9e2799b77c5d6b8e0a5b928b34215
statuses SHA-256: 91aeb622870dfd5e6d597e5b211c7899cbdc20310bc219fd5fd551b844e72816
logs archive SHA-256: ce007148337000811eae57028bf7cbeb1d27b3a255092f51359892ce37c945f6
```

All 23 status rows were zero and the 158-file explicit-inventory archive was
byte-identical to its frozen workspace inventory. The same-host ordered
benchmarks showed zero added allocations, a 6.61 percent full HTTP pre-forward
regression, and 8.44/7.23 percent Manager regressions at 48/256 reservations.

This result does not close Phase C. The following review found that a shared
256-byte lexical sampling budget could be exhausted by early metadata strings.
A late long CJK prompt would then fall back to the ASCII ratio and could be
underestimated below the 512K quiescent boundary. That is an executable QoS
defect, so all r32 executable evidence is superseded for the final candidate.

## 16. Phase C r33 red and r35 focused lexical correction

r33 reproduced the defect intentionally on candidate SHA-256
`6ef9d4304b3c463188fae5d31a4f9bc064a4b5f1c7e649f8dcd78e8f31e17775`.
After four earlier strings consumed the shared sample budget, equal-byte ASCII
and CJK late strings both produced the same `832`-token hint. The test failed
with `late string shape was lost`, proving the intended behavioral red.

The correction removes the request-wide lexical budget and samples at most 64
bytes independently per string. Values of one to three bytes use a constant
one-token fast path. This retains bounded zero-allocation estimation and keeps
the language shape of a late long string without introducing model-specific
tokenizers or learned state.

r35 focused evidence used source SHA-256
`92c9926f83752d2ca5582c4396543559dee62ac64507522329f0bb8b412b20aa`
in builder work `pig-v0123-phase-c-r35-lexical-green-92c9926f`. Formatting,
the regression, the estimator package, direct consumers, and estimator
benchmarks all passed. The normal 4 MiB path was approximately 152.6-153.3
microseconds with zero allocations. The pathological 4 MiB request containing
about one million short strings was approximately 21.5-21.8 ms, versus
21.8-22.1 ms for the old implementation, also with zero allocations.

## 17. Phase C r36b direct 650K CJK gate

The final lexical contract adds early metadata strings followed by 650K CJK
characters and requires the approximate hint to remain at or above the 512K
quiescent boundary. The focused explicit-inventory archive was:

```text
archive: pig-v0123-phase-c-r36-focused-source.tar.gz
SHA-256: ae37e80c8936b632cbd61cd77fb51b9cf742ecb3d9cf491f2b31bc0f0c7fb4b7
files: 158
builder work: pig-v0123-phase-c-r36b-focused-ae37e80c
```

The exact new test passed in 0.01 seconds, the complete `kvadmission` package
passed, and the formatting diff was empty. The first r36 runner did not reach a
test because a login shell omitted the Go toolchain from `PATH`; r36b used the
verified `/usr/local/go/bin` paths. This was runner infrastructure evidence,
not a source red.

The user-approved bound for the pathological bounded request shape is below
100 ms. r35 is comfortably inside that bound, so no further tokenizer
micro-optimization is justified unless the final full matrix shows a material
regression. Phase C remains open until a new archive containing this plan
record and the final executable source passes the complete matrix.

## 18. Phase C r37 complete green superseded by cooldown review

r37 froze 158 explicit files and passed all 23 builder status rows:

```text
candidate SHA-256: 179bfe00a0d65bef60d4c86e283d4aa4119fcf0cfb65471bbe46bb6a48bf2509
runner SHA-256: f81aa2abeae09f05fcbcd7e3ff03858100af2ea1dac7c63c482e9b069f562814
builder work: pig-v0123-phase-c-r37-179bfe00
binary SHA-256: 1f01670b234f463a211cc28924c0439173a565d9fed7f405433a5356a4e25e2b
statuses SHA-256: 91aeb622870dfd5e6d597e5b211c7899cbdc20310bc219fd5fd551b844e72816
logs archive SHA-256: 5df4a78d1fc0f84590c0b90e6a0784ea175023a8090430c27a5e5d3a1e7ef4ce
```

The benchmark contract reported zero new allocations, 5.92 percent full HTTP
pre-forward overhead, and 8.77/6.83 percent Manager overhead at 48/256 live
reservations. The pathological 4 MiB many-short-string request took
21.42-21.58 ms, while the normal 4 MiB path took 155-157 microseconds, all with
zero allocations. The deterministic simulation was byte-stable and retained
the previously documented diagnostic tradeoffs.

The safety review then found that the active code still exposed a default
10-second `PREDICTIVE_PREEMPTION_COOLDOWN_SECONDS`. It continued protecting
large requests across multiple new 500-ms observations even when the
preemption counter stopped increasing. That contradicted the no-reject-cooldown
contract and could underutilize a recovered backend. r37 is therefore complete
evidence for its exact source but cannot close Phase C.

## 19. Phase C r38 red and r41 focused one-snapshot correction

r38 added a behavioral test requiring a preemption protection to clear on the
next fresh observation when the counter does not increase again. Source
SHA-256 `532133164e6d5bea81cdedc19c5dd1919a15367ada46488cadb250605aa6a8f0`
failed for the intended reason with `PreemptionCooldown:true`; the red log
SHA-256 was `d621392993181028887ed520129af42d67ed2db639fe1096660c204c0293c651`.

The correction removes the cooldown environment variable, duration state, and
timer comparison. `PreemptionObserved` is derived solely from a counter
increase in the current coherent sample. It protects only non-regular
candidates during that snapshot and clears on the next coherent sample without
another increase. The retired variable is covered by the legacy configuration
audit.

r39 stopped at builder formatting before tests. r40 passed formatting but
found one unused import left by the configuration removal. r41 source SHA-256
`47825ec4885d40bf969105dbed9b49d19e5597de6a46bf555018950c58c2f5f9`
then passed formatting, the exact observer recovery test, HTTP pre-forward
reason/projection tests, config tests, runtime policy tests, server tests, and
simulation package tests in builder work
`pig-v0123-phase-c-r41-preemption-green-47825ec4`.

Final review pass 1, objective and causality: request classification, immutable
cost construction, coherent snapshot capture, and atomic two-gate reservation
all precede `proxyRequest`. A cost or observation change can alter the decision
under an otherwise fixed request; TPS remains telemetry and cannot alter either
gate.

Final review pass 2, safety, lifecycle, and SOLID: Manager owns one lock for
check-and-reserve, assimilation, reconciliation, and exact-once release. The
HTTP guard linearizes forward and terminal calls; epoch invalidation closes old
ownership. ResourceGate owns hard KV fit, InterferenceGate owns size-aware
Prefill interaction, and the new preemption signal contains no timer or learned
state. The bounded 1.5-second Router reject projection is reporting-only and
retains one inspect slot for load protection; it does not change PIG admission.

Final review pass 3, efficiency, evidence, and operability: the active source
contains no learner, cache-aware admission, request mutation, route selection,
tier/priority injection, TTFT gate, Decode pacer, soft KV target, TPS target, or
preemption cooldown. Production defaults remain enforce and 500-ms observation
with minimal Compose. Two stale references in advanced documentation were
removed. A final complete matrix is still required because the one-snapshot
change is executable.

## 20. Phase C r42 final complete green

r42 contains the reviewed one-snapshot preemption correction and the completed
review record. It froze 158 explicit files, excluded the two unrelated old
plans, and passed every one of the 23 complete-matrix status rows:

```text
candidate archive: pig-v0123-phase-c-r42-source.tar.gz
candidate SHA-256: f8e058397683f4d3ecd474e3c93ceecd15a5cbcffb37c69c6ebb69c1acc18c66
runner SHA-256: 38ebf88ea9e864a3b732f15a1bd9ea151ce39f3aceff8e63f76670f01d439def
builder work: pig-v0123-phase-c-r42-f8e05839
container: pig-v01011-builder
Go: go1.24.13 linux/amd64
kernel: Linux 6.9.0-dstack x86_64
binary SHA-256: c9cab8a932496848e0e628fe049b680fe8058a94c3aab519d3cfd530ebe215c8
statuses SHA-256: 91aeb622870dfd5e6d597e5b211c7899cbdc20310bc219fd5fd551b844e72816
simulation SHA-256: 88a04d2b03243c38242a052d537a2f354e0b05e05cf494e68b25aa455cffcf66
logs archive SHA-256: e5fcb62a68452f87ae4574de50c84a3907ae12578535e41bb8c488dd8e5b5da0
```

Formatting, legacy audit, lexical corpus, affected packages, all tests, vet,
targeted and full race, all-package build, versioned binary, policy-order
tests, two byte-identical simulations, acceptance, four ordered benchmarks,
benchmark contract, absolute HTTP benchmarks, and estimator benchmarks all
returned zero.

The same-host v0.12.2 comparison added no allocation on any measured path.
Full HTTP pre-forward changed from 12,970 to 13,787.5 ns (+6.30 percent),
Manager active-48 from 3,297.5 to 3,576 ns (+8.45 percent), and Manager
active-256 from 17,280 to 18,460 ns (+6.83 percent). The zero-reservation
Manager added 59.25 ns and the pure Policy remained 83.23-86.09 ns, satisfying
the absolute nanosecond limits.

The normal 4 MiB estimator path was 154.2-158.2 microseconds. The pathological
4 MiB many-short-string path was 21.50-21.75 ms, below the user-approved 100-ms
ceiling. Both remained zero-allocation. Absolute HTTP pre-forward medians were
approximately 13.8 microseconds at zero reservations, 17.9 microseconds at 48,
and 32.9 microseconds at 256, with the unchanged 33 HTTP-path allocations.

The simulation output stayed byte-identical to r37 and is still diagnostic:
v0.12.3 admits more work and reports higher aggregate synthetic SLO-goodput
than v0.12.2, but also reports more waiting and TPS-floor violation time. It
does not prove GPU QoS. Phase C is complete for source and builder evidence;
source commit/push, immutable image publication, and all Router-disabled/live
layers remain separate incomplete gates.

This section is a documentation-only evidence update after r42. Before commit,
every archive file except this plan must compare byte-identical with the current
workspace. The Dockerfile copies only `go.mod`, `go.sum`, `cmd`, and `internal`,
so this evidence record cannot change the tested binary or future image bytes.

## 21. Exact source and builder-local production image

The final executable source was committed and pushed as
`8c0ad8953e23375179a2eb629425a1d0ac078d1d`. The local branch and
`pig-origin/codex/pig-v0.11.0-request-aware` resolved to that same commit. The
two unrelated untracked v0.11 plans remained excluded.

r43 cloned the pushed branch afresh on the approved builder, required the local
and remote heads to equal that exact revision, required a clean worktree and
the frozen 158-file inventory, and built the pinned production Dockerfile with
`SOURCE_REVISION` set to the full commit. Its identities were:

```text
runner SHA-256: 49ae6c54296d28a69d388d9377bc6921e01ba14e2d67aa63eeed18ff70eb05cd
builder work: pig-v0123-image-r43-8c0ad895
source archive SHA-256: 00e6872f69b86fef1cb4df9d6049bc0c4c5f55cf6bc216ff22e068482b90a922
builder-local image: ghcr.io/phala-network/phala-inference-guard:0.12.3-8c0ad89-local
image ID: sha256:5052355789ff387c20c704bbeab93dd2d05b792f1b07337ed153295b4b5884f7
production binary SHA-256: 17f13486a03171e85e5d267600fc6f6d3dbf739870bfcd58d9713153046c0a35
evidence archive SHA-256: 427e5184daae30594e181121f6228be3f5baa593dd79c06e02a143c7898f407d
```

The image passed the native-NVML production contract, `linux/amd64`, OCI
version/revision, root user, distroless entrypoint, NVIDIA environment, default
`enforce`, default 500-ms observer, `PIG-v0.12.3` runtime identity, `/healthz`,
authenticated and unauthenticated metrics, and a real pre-forward transparent
chat proxy smoke. The production CGO binary is intentionally distinct from the
r42 non-CGO test binary; r43 proves its identity at the production-image layer.
The independently downloaded r43 archive recomputed all 22 evidence entries.

## 22. r45 red and r46 immutable registry publication

r45 stopped before Docker login, tagging, or registry mutation because its
runner incorrectly required `read:packages` to appear separately. The official
GitHub CLI token had `write:packages`, which already authorizes package upload
and download. The failure trap removed the isolated CLI configuration; the r45
work contains only the masked scope evidence and created no registry tag.

r46 removed only that redundant scope assertion. It used the checksum-verified
official GitHub CLI v2.97.0 archive with SHA-256
`a2c9b8497e1f85b1ad0dfcb78b5a622e098801b8e461e459e88e1ee12f018112`.
The r46 runner SHA-256 was
`ed86962283412f414723c7d070183bb3bcd083f313129b9a191d9051e9ad45af`.
Authenticated pre-push manifest checks returned explicit `manifest unknown`
for both targets, after which the approved builder published:

```text
ghcr.io/phala-network/phala-inference-guard:0.12.3
ghcr.io/phala-network/phala-inference-guard:0.12.3-8c0ad8953e23
```

Both pushes and subsequent authenticated manifest GETs resolved to:

```text
sha256:e3a0894a5e508013593f612165884d33c459f973ea3d2556ab33c253147127dd
```

r46 pulled the version tag, revision tag, and immutable digest. The registry
image remained `linux/amd64`, image ID
`sha256:5052355789ff387c20c704bbeab93dd2d05b792f1b07337ed153295b4b5884f7`,
OCI version `0.12.3`, revision
`8c0ad8953e23375179a2eb629425a1d0ac078d1d`, user `0`, entrypoint
`/phala-inference-guard`, and `NVIDIA_VISIBLE_DEVICES=all`. Its extracted
binary was byte-identical to r43 at SHA-256
`17f13486a03171e85e5d267600fc6f6d3dbf739870bfcd58d9713153046c0a35`,
and the digest reference passed the production-image contract again.

The r46 evidence archive contains 24 hashed entries and has SHA-256
`20e68add4610591b4cc27be5d7f4cb2603c9ac37a8c5c52264550555c55340dc`.
An independent download recomputed every entry and found no unmasked credential
pattern. Both the isolated Docker config and GitHub CLI config were absent after
publication. The builder remained running.

## 23. Publication evidence review: three passes

Pass 1, identity and causality: the registry image is derived from the exact
pushed source revision, and its image ID, labels, binary bytes, and contract
match the independently validated builder-local image. The documentation-only
commit that follows cannot alter those image bytes or their recorded revision.

Pass 2, safety and provenance: both tags were proven absent before publication,
resolved to one digest after publication, and were independently read through
authenticated manifest GETs. Credentials existed only in isolated temporary
configs, were not archived, and were verified absent after logout and cleanup.
The r45 red had no registry side effect.

Pass 3, operability and boundary: Phase C now proves source push, builder-local
production behavior, registry availability, immutable digest identity, and
pull-path compatibility. It proves no GPU QoS, Compose integration, CVM
readiness, release promotion, or Router safety. Phase D must use exactly the
recorded digest on Router-disabled `use1-cb`; no rebuild or floating-tag
substitution is allowed.

## 24. Phase D controlled-live protocol and preliminary evidence

Phase D uses three explicitly named policies on the same `use1-cb` vLLM
upstream. `N` is the v0.12.3 digest in explicit `shadow`, which retains proxy,
inspection, and measurement overhead but never enforces admission. `A` is the
immutable v0.12.2 digest
`sha256:7cafb935d48175045cd355a844a3f94638fdfae16f965e2a9d7dbedeee63c4e4`
in its production-default `enforce`. `B` is the immutable v0.12.3 digest
`sha256:e3a0894a5e508013593f612165884d33c459f973ea3d2556ab33c253147127dd`
in production-default `enforce`. This makes `N` a no-enforcement baseline, not
a claim that the network proxy has been bypassed.

The three matched A/B repetitions run in `B1,A1,A2,B2,B3,A3` order. Thus the
paired orientations are B/A, A/B, and B/A while consecutive equal-policy runs
avoid an unnecessary cold restart. N runs three nonce-separated repetitions as
the matched no-enforcement QoS reference. Before the measured counters of every
repetition, run the same uncounted nonce-separated 128-token short request and
approximately 16K-token Prefill warmup, then drain. Every deployment freezes the
complete Router enabled set, requires only `use1-cb` to remain disabled and
drained throughout non-mutating workloads, and re-establishes exact image,
Compose, container, readiness, and runtime identity before a repetition.

The fixed workload suite is:

| Workload | Fixed arrivals |
| --- | --- |
| short-only | 8 simultaneous approximately 1K-token prompts, 512 forced Decode tokens each |
| long-only | 2 simultaneous approximately 195K-token cold prompts, 1 output token each |
| mixed | 8 approximately 1K/1K Decode requests, then one approximately 195K Prefill after 1 second |
| reversed-order | the same large Prefill first, then the 8 Decode requests after 15 seconds |
| same-snapshot burst | 16 simultaneous small prompts with 128 forced Decode tokens each |
| near-KV | 3 simultaneous approximately 245K-token prompts with 1 output token each |
| quiescent-plus-short | one 4K-token Decode holder, then one approximately 215K Prefill and one 256-token short Decode after 5 seconds |
| low flow | 4 serial approximately 1K prompts with 64 forced Decode tokens each |
| cancellation | one 4K-token Decode cancelled after 2 seconds, followed by a 128-token recovery request |
| sustained regular Decode | 24 approximately 1K/1K requests through a fixed concurrency of 12 |

Each request has a fixed-length nonce-derived prefix that differs across
policy, repetition, scenario, and request position. Remaining cache influence
is measured from vLLM cached-prompt counters and reported as a confounder. The
runner retains no prompt or response body. It records status, usage tokens,
TTFT for diagnostics only, first-to-last-token Decode duration, per-user Decode
TPS, wall time, and terminal reason. A 250-ms requested monitor samples PIG
running, waiting, KV, reservations, protection, and compatibility projection;
actual sample spacing is retained rather than assumed. vLLM prompt, cached
prompt, generation, error, and preemption counters are captured before and
after every policy repetition.

For a successful Decode request with at least two output tokens, per-user TPS is
`(completion_tokens - 1) / (last_token_at - first_token_at)`. For each workload,
freeze its QoS floor at 85 percent of N's three-repetition p10 per-user Decode
TPS. N is deliberately not called a physical direct-vLLM bypass: it retains the
same PIG proxy and estimator path while removing enforcement, which controls
more confounders for this admission comparison. A successful request at or
above the floor contributes all its completed tokens to SLO-goodput; a
QoS-violating, rejected, cancelled, or failed request contributes zero.
Successful one-token Prefill probes have no Decode floor and contribute their
one completion token. Report both this SLO-goodput and raw completed-token TPS
so admission cannot appear better merely by rejecting hard work.

Stop a repetition on any new preemption, backend error completion, container or
Compose drift, target Router enablement, internal admission failure,
reservation leak, or failure to drain within 180 seconds. Waiting and 429 are
measurements, not automatic failures, unless they violate the section 4 Pareto
gate or persist after demand clears. The live stale/recovery fault remains a
separate incomplete Phase D case: do not manufacture it by stopping vLLM or
changing production networking while throughput repetitions are in progress.
Define and review a reversible Router-disabled injection before executing it.

Evidence health is a separate executable gate. Every scenario requires at
least three healthy monitor samples, one baseline sample before workload start,
one sample during live demand, every required metric field, no healthy-sample
gap above 5 seconds, no more than 25 percent failed scrapes, and observed PIG or
backend activity whenever a request was forwarded. PIG attempt deltas must
equal submitted requests and every enforced rejection must be represented by a
429. The `reversed-order` case is valid only if the large request either reaches
observable PIG/backend activity before the Decode arrivals and remains active
when they start, or completes its pre-forward 429 before those arrivals. A
failed evidence-health check makes the scenario and repetition red even when
all safety counters remain green.

The Router-disabled v0.12.3 preliminary gates completed on 2026-08-09. Explicit
shadow passed readiness, protocol transparency, low-flow/cancellation/burst
lifecycle, five request-size classes, and the counterfactual Decode-plus-large
Prefill differential. Production-default enforce then passed exact runtime
identity, protocol, lifecycle, and all five idle size classes. Under an existing
Decode holder it admitted the same-pressure short request and rejected the
approximately 215K prompt before forwarding as
`size_protect/prefill_busy/prefill/quiescent`, with no preemption or terminal
leak.

The first enforce differential was red only because the eventually consistent
container-log query returned before the already-emitted decision line. A second
run proved the log but missed the bounded Router projection with a serial
metrics scrape. The corrected run sampled `/pig/metrics` independently: all 5
samples were healthy, 2 observed the complete
`active=1/applied=1/inspect=1/global_limit>running` projection from
`02:51:20.806Z` through `02:51:21.976Z`, the one 429 had one represented log
event, and projection returned to zero after drain. Both red attempts remain
evidence of harness timing defects and are not rewritten as green runs.

The first full `B1` workload attempt at `2026-08-09T03:11:27Z` is retained
under `tmp/pig-v0123-use1-cb-live-20260809/phase-d-B1-v0123-20260809T031127Z`
as red exploratory evidence. All ten scenarios drained with zero preemption,
backend error completion, internal admission failure, or container/Router
drift. However, the 1-second `reversed-order` delay was shorter than the large
body's approximately 3.29-second pre-forward result: the eight short requests
reached admission first and the large request was rejected, so the intended
large-Prefill-first workload never occurred. That scenario also recorded one
failed metrics scrape. No result from this attempt contributes to the formal
N/A/B repetitions; the corrected protocol uses the 15-second offset and the
evidence-health gate above before restarting at `B1`.

The first corrected `B1` start at `2026-08-09T03:27:38Z`, retained under
`tmp/pig-v0123-use1-cb-live-20260809/phase-d-B1-v0123-corrected-r1`, stopped
before its first scenario when a read-only `/pig/metrics` drain snapshot hit a
transient TLS `UNEXPECTED_EOF_WHILE_READING`. No measured request was submitted;
the before/after container and Router identities remained exact and logs had no
fatal match. This is runner-infrastructure red evidence, not policy evidence.
The harness now applies bounded retries only to idempotent metrics and Router
GETs; monitored scrapes still retain failures and inference POSTs are never
retried automatically.

The next corrected start at `2026-08-09T03:31:08Z`, retained under
`tmp/pig-v0123-use1-cb-live-20260809/phase-d-B1-v0123-corrected-r2`, stopped
after `short-only` because one failed TLS scrape among four exceeded the
initial 20-percent failure-rate threshold. The remaining three healthy samples
covered baseline and live demand, contained every required field, observed
`running=8`, and had a maximum 3.73-second healthy gap; all eight requests
completed, with no safety failure or identity drift. The discrete 20-percent
threshold therefore rejected usable short-workload evidence. It is corrected
to 25 percent while retaining the independent minimum-sample, baseline,
live-demand, field-completeness, activity, and 5-second blackout gates. This
attempt remains runner-calibration evidence and is not a formal `B1` result.

The next formal `B1` attempt at `2026-08-09T03:35:21Z`, retained under
`tmp/pig-v0123-use1-cb-live-20260809/phase-d-B1-v0123-formal-r3`, produced the
first valid `reversed-order` GPU evidence. The approximately 195K prompt reached
PIG first, was observed running before the Decode arrivals, and remained active
until about 62.03 seconds. All eight later Decode requests were admitted and
completed, but their p10 per-user TPS was only `22.88`, versus `193.96` in the
same repetition's decode-first `mixed` case and `190.16` in `short-only`.
During the large-only interval the monitor observed one reservation, one
pending Prefill, and one backend request while Router protection remained zero;
protection became active only after the eight Decode requests had already been
admitted. This is causal red evidence for the section 5.4 no-Decode-envelope
candidate: the approximately 88-percent cross-order TPS collapse is not an
acceptable interpretation of bounded QoS loss.

That repetition later stopped at `same-snapshot-burst` because two of sixteen
clients ended with transport `URLError` before reaching PIG: only 14 admission
attempts and 14 successful completions were observed. There was no PIG internal
failure, backend error completion, preemption, leak, fatal log, identity drift,
or Router enablement. The whole repetition remains red and does not enter the
formal comparison, while its already-valid `reversed-order` subcase remains
diagnostic evidence. Future client transport errors record their nested reason
type, but ambiguous inference POST failures are not retried.

The current N/A/B matrix is paused because the exact B image has already failed
the section 5.4 Decode-QoS decision point and cannot be promoted regardless of
additional repetitions. Before changing executable source, run bounded
Router-disabled diagnostics with the same approximately 195K Prefill followed
after 15 seconds by 1, 2, and 4 regular Decode requests. Compare each with the
valid 8-request case and clean regular-Decode controls. The purpose is only to
determine whether a simple immutable concurrent-regular envelope can preserve
useful TPS during active long Prefill; it is not online learning and cannot be
silently converted into a model-specific production constant.

Protocol review pass 1, objective and causality: N/A/B share one request and
measurement implementation; request nonces prevent easy prefix reuse; QoS
floors are frozen from N rather than tuned from candidate outcomes; and rejected
work contributes no goodput.

Protocol review pass 2, safety and lifecycle: near-KV is bounded at three
approximately 245K prompts, every scenario begins and ends drained, cancellation
must recover, target Router isolation is checked around each scenario, and the
first preemption or lifecycle failure stops the repetition.

Protocol review pass 3, efficiency and evidence: the suite reuses the published
images, records actual sampling cadence and every repetition, separates raw and
SLO-goodput, treats cache as a measured confounder, and leaves fault injection,
release promotion, Router enablement, and production canary explicitly open.

## 25. Phase D red conclusion and v0.12.4 remediation cycle

The bounded diagnostic completed green as a harness run at
`2026-08-09T03:47:16Z`; evidence is retained under
`tmp/pig-v0123-use1-cb-live-20260809/phase-d-prefill-decode-envelope-diagnostic-r1`.
The clean 1/2/4-request Decode controls produced p10 per-user TPS of
`217.72/231.20/213.30`. With the same approximately 195K cold Prefill already
active, p10 was `23.30/23.43/22.91`, respectively. Cached-prompt deltas,
preemptions, backend error completions, internal admission failures, and leaks
were all zero. Reducing regular concurrency from eight to one therefore did not
restore QoS: the Prefill itself, not Decode fan-out, caused the approximately
89-percent slowdown.

For all three Prefill-first diagnostics, 9-10 healthy samples observed the
known local Prefill reservation and backend activity before the regular Decode
arrivals, while Router protection had zero samples before those arrivals. The
first protection samples appeared only after admission, at approximately
`49.19/44.13/33.30` seconds for the 1/2/4-request cases. Source review explains
the behavior: an approximately 195K request is `weighted`; Manager does not
count weighted reservations as pending long Prefills; regular candidates are
explicitly work-conserving behind every known non-regular Prefill; and Router's
synthetic inspect uses that same regular-candidate rule.

The exact v0.12.3 image is therefore not promotable and the incomplete N/A/B
matrix must not be resumed for it. The remediation stays in the `0.12` release
line and becomes v0.12.4. Its minimal invariant is:

```text
known local pending Prefill class >= weighted
  -> pre-forward reject new regular candidates
  -> Router inspect capacity 0 in the same manager state
  -> reopen immediately on PrefillComplete or terminal release
```

This is not a cooldown, per-poll credit, online learner, model-specific TPS
constant, or permanent global lock. Weighted, exclusive, and quiescent local
reservations count as pending long Prefills. A regular reservation does not.
Observed-but-unattributed pending work keeps its existing conservative handling
in this patch so an ambiguous observer sample cannot create a new low-flow
self-lock. The same pure `InterferenceGate` owns the decision for business
requests and Router inspect; Manager owns classification/lifecycle state; the
adapter only maps the decision to HTTP and compatibility telemetry.

The red/green implementation order is fixed:

1. Add focused red tests proving a regular request is rejected behind a known
   weighted, exclusive, or quiescent Prefill; Router inspect is active with
   zero capacity before another request; and regular-behind-regular remains
   admitted.
2. Add Manager lifecycle tests proving weighted accounting is atomic and the
   gate clears immediately on PrefillComplete, cancellation, error, timeout,
   rollback, and epoch invalidation without waiting for a 500-ms observation.
3. Implement the smallest gate change, bump the source/runtime identity to
   `0.12.4`, then run focused, full, vet, race, simulation, benchmark, image
   contract, and three SOLID/evidence reviews on the approved builder.
4. Publish one immutable v0.12.4 image only after complete builder green and
   deploy it to Router-disabled `use1-cb` with exact source/image provenance.
5. Before any Pareto matrix, require a targeted live case where the long
   Prefill is admitted, a regular request during it receives represented
   pre-forward 429, Router protection is already active before that request,
   Prefill completion clears protection, and the immediate recovery request is
   admitted. Repeat cancellation and low-flow/no-demand checks to exclude
   self-lock.

For subsequent QoS accounting, a Prefill-first workload must not define its own
degraded Decode floor. The 1/2/4/8 regular Decode requests use their matched
clean Decode-only N controls; otherwise an 89-percent Prefill-induced collapse
would be relabeled as acceptable merely because no-enforcement is equally bad.
Rejected, failed, or below-floor requests still contribute zero SLO-goodput,
and raw goodput remains reported separately. Router-disabled direct evidence
can prove local protection and lifecycle but cannot claim cluster-level reroute
goodput; that remains part of the later Router canary.

## 26. v0.12.4 focused behavioral red evidence

The first frozen attempt, r1, is retained as invalid test-infrastructure
evidence. Its policy test omitted the closing brace for the table loop, so
`gofmt`, policy, and Manager failed during package parsing. The three adapter
tests did reach their intended assertions, but the mixed result is not counted
as the release red gate and no executable source was changed from it.

r3 corrected only that test fixture and froze all 158 Git-tracked files from
base HEAD `d92b0e48ea85f5737a6bdd954c762526ea46b7fd`. The source archive SHA-256 is
`a2a1b0c3c77761a678e02c527f8361423f215472bf0c9fb94de266160304fe2d` and
the runner SHA-256 is
`59e2cbac3b2e84c035a6de1e8db66fb8cd2dcb42958e31850b4b8ca073d6a029`.
Both hashes were recomputed after transfer before execution. The two unrelated
untracked v0.11 plan files were not present in the archive.

The approved builder container `pig-v01011-builder` ran every test with
`nice -n 19` and `GOMAXPROCS=2`. r3 produced:

```text
fmt_status=0
policy_status=1 policy_reason_status=0
manager_status=1 manager_reason_status=0
adapter_status=1 adapter_reason_status=0
http_status=1 http_reason_status=0
```

The failures are causal and specific. Policy admitted regular candidates
behind weighted, exclusive, and quiescent Prefills. Manager reported a 195K
weighted reservation as one pending Prefill but zero pending long Prefills for
every tested lifecycle. Adapter did not project zero Router capacity before
the next request, admitted the regular request, and retained a stale recent
reject after Prefill completion. The vertical HTTP test reached the backend and
returned `200/1`, rather than the required represented pre-forward `429/0`.
The regular-behind-regular control remained part of the policy test and did not
fail.

Material builder log SHA-256 values are:

```text
gofmt  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
policy bca8c3c0cf77afaed1a79d1817b8d764fbc2f3757283e9de7b691a8c466cb67d
manager 46a9f971ee2f9da9fc94ad4a213ff0c137f64b4d9aaad658f1e8db077a941754
adapter 3d9c5796e357e6ff9f5dfce437410552691f415f0ce5e66f3461be809f4d29ec
http c8767afa672e0192e9acd9111096666bf54fd58ec5589e37742e35834ee2de43
```

This closes the focused red gate only. No production source, version identity,
image, registry, Compose, CVM, or Router state changed. The next action is the
minimal Manager, InterferenceGate, and adapter lifecycle implementation, plus
the `0.12.4` identity update; all executable green evidence remains open.

## 27. v0.12.4 focused implementation and builder green

The implementation remains a three-owner vertical slice. Manager classifies
every uncompleted local weighted, exclusive, or quiescent reservation as one
pending long Prefill. InterferenceGate returns
`size_protect/prefill_busy/prefill` only for a regular candidate when that
known-local count is nonzero; regular-behind-regular remains admitted subject
to its existing aggregate budget. The adapter records whether an enforced
reject carries an authoritative Manager event sequence. A later Prefill
completion, terminal release, or epoch rebase changes that sequence and
immediately supersedes the corresponding 1500-ms Router hold. Adapter-local
stale or unavailable rejects have no Manager sequence and retain the bounded
time fallback.

r4 first proved the production slice: runtime and server/HTTP focused tests
were green. It was not accepted because one Go struct required builder `gofmt`
alignment and the deterministic simulation still expected the retired
regular-behind-exclusive behavior. r5 applied those corrections and added an
approximately 195K weighted-Prefill recovery scenario. That new scenario
passed with two admits and one in-Prefill reject; r5 remained corrective
evidence because the older quiescent recovery scenario still expected its
during-Prefill short request to be admitted. Updating that exact contract from
`3/1` to `2/2` preserved the post-Prefill recovery admit and did not relax the
generic acceptance checks.

r6 froze all 158 Git-tracked files from base HEAD
`d92b0e48ea85f5737a6bdd954c762526ea46b7fd`. The source archive SHA-256 is
`7088fb629f1eec32657570824aa5512eddc9a2c228889686dc3555e0bf8a54ee` and
the runner SHA-256 is
`468c80940409a5db2efdc783f60de7643af781f7b49bdc13d4efed82af747976`.
The approved builder recomputed both hashes before execution and produced:

```text
fmt_status=0
runtime_status=0
server_status=0
simulation_status=0
```

Material r6 log SHA-256 values are:

```text
gofmt e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
runtime baa2e635a31428c119e24047925450a6c67cc415fc539ac6ade47131f3c16f49
server 51dde5029f97981924fb57978c002a0653d09ab5aae6d5eff331e4bb26b6cf50
simulation 41c20baf7f8bd8610e2962a72d34aaebcd25ebb374f978ba672f066d978bf108
```

This is focused green only. At the evidence check, an unrelated vLLM NVCC
image build was still active on the same host after approximately 3 hours 51
minutes. It is not terminated or disturbed, and the complete test, vet, race,
ordered benchmark, and image-contract matrix waits for an idle builder so
resource contention cannot be mislabeled as PIG performance evidence. This
documentation append is non-executable; the later complete matrix must freeze
a new archive containing it rather than reusing r6 as final-source evidence.

## 28. v0.12.4 three-pass review and Router-verdict correction

The previously unrelated vLLM build had ended before this review resumed. The
approved host showed no active compiler, Ninja, or NVCC process, so focused PIG
evidence and the later complete matrix may resume without terminating or
disturbing another build.

Review pass 1, model and causality: the approximately 195K request is classified
as weighted from the bounded model-neutral request estimate. Its atomic local
reservation contributes one known long Prefill before forward; the same pure
InterferenceGate blocks a regular business candidate and the Router one-block
inspect candidate. Regular-behind-regular remains admitted within the aggregate
budget. Observed-but-unattributed Prefill does not acquire this new regular
global gate, so an ambiguous sample cannot create a low-flow self-lock. TPS,
cache state, and TTFT remain diagnostics rather than fabricated admission
authority.

Review pass 2, safety and lifecycle: Manager owns check-and-reserve under one
lock and keeps exact-once release for completion, local rejection, client
cancellation or disconnect, upstream failure, timeout, expiration, rollback,
and epoch rebase. Manager event-sequence validity distinguishes authoritative
decisions from adapter-local stale or unavailable failures. PrefillComplete,
terminal release, and rebase supersede an old Manager-mediated Router hold.
Adapter-to-Manager lock ordering is consistent and Manager has no reverse
adapter callback.

This pass found one reporting defect outside the original weighted gate. When a
recent verdict and the current inspect verdict both had capacity zero, the
recent verdict replaced the equally restrictive current verdict. Capacity was
safe, but Router scope, reason, and source could remain stale for 1500 ms. r7
did not reproduce that exact state and passed, so it is not a behavioral red.
r8 constructed recent `availability/metrics_stale/0` followed by current
`load/kv_over_budget/0` and failed for the intended assertion:

```text
source SHA-256: d4e3b60d679dc825ec57a599d02323b4ae230608dbbced046259a58289e30bba
runner SHA-256: 00d8fad9675ba142f28cfe347e228df68c6bd1731d23aa5263f2ec759fac6ec8
tracked files: 158
fmt_status=0
server_status=1
gofmt log SHA-256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
server log SHA-256: c9134144fbbc5c05a47f3430ce05b4cc4661f856c405ce63fb6f93afe43ce11e
```

The minimal correction lets a recent verdict override current state only when
its inspect capacity is strictly lower, not equal. Focused coverage also
requires terminal release and epoch rebase to clear a represented weighted
Prefill reject just as PrefillComplete does.

Review pass 3, SOLID, efficiency, and evidence: Manager, InterferenceGate, and
adapter responsibilities remain separate; no learner, cache lookup, model
special case, Decode pacer, cooldown, or generic policy framework was added.
The executable hot-path delta is a constant-time Manager sequence read plus the
single comparison correction; ordered same-host benchmarks must still measure
it rather than assuming it is free. Runtime, Docker, README, simulation, and
operator-document identity are v0.12.4. The plan header and progress ledger now
separate historical v0.12.3 publication from incomplete v0.12.4 gates. Final
focused green and the new complete builder archive remain open; no commit,
push, release image, deployment, Router enable, or production traffic is
authorized by this review.

r9 attempted the reviewed focused green with source archive
`e41dbd48e4e255a0e0beed40a9e61cb7d0bdcf8745cf10b6f36201e4137d3487`
and runner
`ce324cf7e75bcaf132f44c453f85b6b3bf007746cf9a1f220570f22a0554541b`.
Formatting, runtime, and simulation returned zero, but server test compilation
returned one because the new table helper accepted the concrete adapter
reservation while the decision exposes the existing `predictiveShadowReservation`
interface. The server log SHA-256 is
`550decf9660f4f4e5dd3bf8c67bec1cf12b783bfbbcec861e0a8638f2178a69e`.
This is invalid green evidence and not a production behavior failure. The test
helper now depends on the existing lifecycle interface, preserving the SOLID
boundary; a new archive and complete focused run are required.

r10 froze the corrected interface-based test and the completed review record in
all 158 tracked files. The approved builder recomputed the source and runner
hashes and returned zero for all four focused rows:

```text
source SHA-256: a5eae10fc9f425e4c1cebc3788f9a5d1bb562e82750a595cf9074373eacbf13b
runner SHA-256: ffba7639d948e799495bf1aec46032cb61d2ea2875445557996be47e32286362
fmt_status=0
runtime_status=0
server_status=0
simulation_status=0
gofmt log SHA-256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
runtime log SHA-256: 7163531d0ae730077c2f2100b88c9df3ccf620d5562372a8b5bfa99aa82aa0a4
server log SHA-256: 4c7895cbb4ffc15ed115d79befa683a4c46e50ef3e15586e1dd132b82c5767f9
simulation log SHA-256: a21e7ae45c8014432cc5d4aff94475a4edd8e6364e3750e6bb9978fd0442c8f0
```

This closes the final focused gate only. The documentation append changes the
source archive, so the complete matrix must freeze a new archive containing it.
No full test, vet, race, ordered benchmark, image contract, commit, push,
release image, deployment, or live conclusion is inherited from r10.

## 29. Dedicated h200.small development and GPU environment

This section is the active execution protocol and supersedes every earlier
instruction to resume work on the shared builder. The shared builder and its
`pig-v01011-builder` container are retired from the v0.12.4 workflow. No new
source archive, test, benchmark, image build, or publication step may run there.
The already recorded r10 focused green remains valid evidence for that frozen
archive only; it does not satisfy any complete-matrix gate in this section.

Create one isolated CVM using the non-secret infrastructure shape of reference
CVM `a0f0bfb3-e46f-4b22-814e-24872f251193`, with these deliberate differences:

- instance type `h200.small`;
- OS image exactly `dstack-nvidia-dev-0.5.9`;
- a distinct development name and persistent development workspace;
- no production endpoint, Router registration, or real traffic;
- persistent disk sufficient for source, Go caches, Docker layers, candidate
  images, model assets, and evidence; and
- SSH access through the current authorized public key, with secret values kept
  in sealed environment storage and never copied into this plan or logs.

Local Windows is limited to this plan edit, read-only source/archive inspection,
Git bookkeeping, sanitized CVM control, and transfer of the frozen tracked-source
archive. All formatting, Go/native execution, focused/full tests, vet, race,
simulation, benchmarks, production-binary and image-contract checks, Docker
build/push, and GPU validation run inside the new CVM. Do not fall back to local
Windows or the retired builder when a command fails; diagnose and repair the new
CVM environment itself.

The active order is:

1. Create the CVM and record its CVM ID, instance type, OS, region/node, disk,
   live Compose hash, and Router isolation without recording secret values.
2. Verify platform progress, SSH, persistent workspace, Docker/BuildKit, disk,
   NVIDIA runtime, H200 visibility, clock, network, and a pinned Go 1.24.x
   toolchain or builder container.
3. Clone the repository at base HEAD
   `d92b0e48ea85f5737a6bdd954c762526ea46b7fd`, then overlay a newly frozen
   archive containing only the current Git-tracked PIG source. Preserve the two
   unrelated untracked v0.11 plan files locally and exclude them from the
   archive. Record the archive SHA-256 and exact tracked-file inventory.
4. Compile and run the pending telemetry benchmark first as a validity check,
   then run formatting, focused/full tests, vet, race, deterministic simulation,
   lexical corpus, ordered v0.12.2 comparison benchmarks, the v0.12.4 benchmark
   contracts including the bounded many-short-string 4 MiB case, production
   binary, image contract, and the three recorded reviews. Record commands,
   exit statuses, environment identity, and material log SHA-256 values.
5. Every accepted code update must be committed and pushed before beginning the
   next implementation iteration. An update is accepted only after its scoped
   formatting and focused tests are green; invalid red fixtures and known-broken
   intermediate states are evidence, not push candidates. Record commit,
   branch, remote result, and source archive identity for every push. GitHub
   authentication uses device flow inside this CVM; never copy or reuse a host
   token.
6. Do not upload an image for a focused-only or intermediate code update. After
   the complete source, simulation, race, benchmark, binary, and three-review
   acceptance matrix is green on the exact pushed source, build one local
   immutable `0.12.4` candidate image. Run the production-image contract and
   smoke tests against that exact local image. Upload it only if those image
   gates are also green; never rebuild between local acceptance and upload.
   Record source revision, local image ID, registry digest, pull verification,
   runtime identity, and image-contract evidence.
7. On the same isolated H200 CVM, run the targeted weighted/exclusive/quiescent
   Prefill gate, immediate completion/cancellation/timeout recovery, low-flow,
   no-demand, stale/recovery, same-snapshot burst, near-KV, and Decode QoS
   controls. The admission target is PIG's production-like chain, while the CVM
   itself remains absent from Router.
8. Only after the targeted gate and Pareto evidence pass may a separate,
   explicitly recorded step consider deploying the exact digest to disabled
   production target `use1-cb`. Router enable and 30-minute actual-traffic
   observation remain later production gates; they are not implied by success
   in the dedicated CVM.

The CVM is a long-lived test appliance, not a disposable build job. After its
initial platform creation, ordinary iterations must not use a platform Compose
redeploy or CVM restart. Keep the vLLM container, loaded model, Docker daemon,
and GPU allocation running; rebuild the candidate image and replace only the
PIG service with a service-scoped `docker compose up` or equivalent container
operation. Before and after every PIG-only replacement, record the vLLM image,
container identity/start time, model readiness, and GPU process identity to
prove vLLM was not rebuilt. A full CVM restart is allowed only for a specific
platform, host, GPU-driver, Docker-daemon, or persistent-disk fault that cannot
be repaired at container scope, and the fault evidence must be recorded first.

The dstack root filesystem is intentionally small and read-only. Put source,
toolchains, build caches, Docker data, model assets, Compose files, and evidence
under `/var/volatile/dstack/persistent`. Use a persistent workbench container
for Git, Go, GitHub CLI, and matrix execution. Runtime test Compose is managed
from that persistent workspace on the already-running host so a PIG source
iteration cannot trigger a platform reboot or discard a loaded vLLM model.

Creation, `status=running`, SSH reachability, `nvidia-smi`, a green focused test,
or a locally built image is not completion. The release gate requires the full
source matrix, immutable image provenance, functional PIG/backend compatibility,
GPU lifecycle evidence, metrics/log coherence, no unexplained protection or
low-flow self-lock, and no admission-caused preemption. If GitHub authorization
is the only missing step, pause at the device flow and wait for the user rather
than weakening or bypassing authentication.

## 30. Dedicated-CVM bootstrap and first scoped acceptance

The dedicated environment was created without modifying the reference CVM or
the retired builder:

```text
CVM UUID: c21b7281-2c25-4453-8a68-f39ec42d03b4
name: pig-v0124-h200-dev-use1
resource: h200.small / 24 vCPU / 192 GB RAM / 500 GB ZFS / 1 x H200
node and region: gpu-use1 / US-EAST-1
OS: dstack-nvidia-dev-0.5.9
KMS: phala
platform Compose hash: c8217429be1f86d7c7561a11c2de24feac93ebecc33d3bec139931d3741e918d
Docker Compose hash: b6fd423f2ce3d5b6c6b2570c487bdbaec8d7040e91ff4fabda07da2efec29a9d
pre-launch script hash: 24d363e17b26dabdbf287588c1e1968fd7fdfef10954123b99ff6c6a837c5692
```

Platform reached `running` with no operation in progress. SSH, outbound GitHub,
Docker 25.0.3, Compose 2.26.0, persistent workspace, and the NVIDIA container
runtime passed. Both host and an isolated CUDA container reported `NVIDIA H200`,
`143771 MiB`, and driver `580.95.05`. The persistent filesystem had 480.6 GB
available. The platform Compose contains only an unexposed keepalive container.
A read-only Router inventory at `2026-08-09T07:28:09Z` returned matching config
digests and zero upstream or route matches for the new name, UUID, or app ID.
Thus the CVM is isolated from production traffic. The host Docker CLI lacks the
optional buildx plugin; install it under persistent storage or use a pinned CLI
container before the image gate. This does not affect source tests and is not a
reason to restart the CVM.

The persistent workbench uses the same pinned Go image as the production
Dockerfile:

```text
source image: golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac
local image ID: sha256:e0cffc405270b9114fac7706d07c373727d1b42b0e47c525b9cd1ab1097779ff
Go: go1.24.13 linux/amd64
Git: 2.39.5
GitHub CLI: 2.23.0
```

GitHub device authorization completed inside this workbench and the repository
API reports write access. No token is copied into source, evidence, this plan,
or operator output.

The first direct tracked-file archive is invalid migration evidence. Although
its 158-path inventory and SHA-256
`d8cccd1bcf6272377588f701b379592e443b305be66dda45a29f65c89648f18c`
were valid, Windows checkout line endings made three unchanged files appear
modified on Linux. It is not tested, committed, or reused.

r2 instead exported the normalized full-index binary diff, applied it to a
fresh Linux clone at base `d92b0e48ea85f5737a6bdd954c762526ea46b7fd`, and
regenerated the archive in the CVM:

```text
local diff SHA-256: 51be45bb87cf05b9c153db0d737edd5628dcc7a9274f2af9c6d2e12f700a7d93
remote diff SHA-256: 51be45bb87cf05b9c153db0d737edd5628dcc7a9274f2af9c6d2e12f700a7d93
tracked files: 158
modified paths: 23
r2 archive SHA-256: 096516714db2aa9c91f22f9af6f54e68f573d344001933a555561ba343d112a9
```

The first new-CVM scoped run then returned zero for formatting, the pending
telemetry benchmark, runtime/predictive tests, server tests, and deterministic
simulation package tests. Material log SHA-256 values are:

```text
gofmt e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
telemetry benchmark 25fca1de0280cba0e228a937166395c8da57f7cc94cbf960f6e5975a75ce4f63
runtime 55fa714a703aef3d66761db71b82a7daac5192e04dacc0305fdefc859bc526ae
server 4cfd582c7e34c893f20428a52aee7d15e5f5613789f4df4e3ddd234781f00a0e
simulation ecaef9ba3acdb4c5dd4b20bef9e97baad00fd1d37fc1ac95cb0f62fa5d265aa3
```

The telemetry benchmark measured approximately 580 ns, 11.5 us, and 61.0 us
at 0, 48, and 256 reservations, with zero allocations. This path intentionally
collects `CurrentRequestAwarePending` and `Manager.Snapshot`, both of which
aggregate live reservations for metrics and Router status. It is not the
business-request pre-forward decision path and is called on the observation
cadence. The result is acceptable scoped evidence, not proof of no regression:
the ordered v0.12.2 comparison and additional benchmark contract remain part of
the complete matrix. No image was built or uploaded, and no PIG, vLLM, Router,
reference CVM, or production traffic state changed.

## 31. v0.12.4 final dedicated-CVM source matrix

The first complete-matrix run, r1, froze pushed source commit
`421d2a8841986f4540996fda716c6a2750401d60` as candidate archive SHA-256
`3a7858cc2cbb46219ff149d4a583067639979a4bc2b4e82dccdb8eb2bae59f3c` and
used v0.12.2 baseline SHA-256
`96d38a1b9371e7af3fec445f87fcf6f2ecd8becb24c97b377cf934703444d0d9`.
Source, simulation, race, build, candidate benchmark, and estimator rows passed,
but both ordered baseline benchmark rows and the additional benchmark contract
failed. This was a fixture-validity failure, not an algorithm regression: the
v0.12.2 baseline's own TPS gate rejected the second synthetic reservation with
`request_size_at_pressure`, so it could not construct the required 48- and
256-reservation telemetry states.

The corrected baseline-only overlay disables that unrelated TPS authority while
seeding the telemetry benchmark. Its SHA-256 is
`c7d5a3f84241fee0a842a58cf1543f48cacdef21f676161ba28ccfc74781d185`.
The focused baseline then measured 514.4 ns, 11.177 us, and 57.146 us at 0, 48,
and 256 reservations with zero allocations. r2 reused the same frozen candidate
and baseline archives with the corrected overlay. All 24 r2 status rows passed;
its status SHA-256 is
`5d6cc0da1bb7995f776375324783edc59ab5f8602db8bab7a858429db644e2ad`.
This validates the behavior and benchmark harness for that archive, but it does
not close the final release gate because the subsequent evidence-identity review
found that the simulator still emitted `v0_12_3_aggregate` while executing
`PolicyV0124`.

The evidence-identity defect was converted into
`TestSimulationReportUsesCurrentCandidateVersionKey`. The intentional red run in
`/workspace/evidence/pig-v0124-sim-key-red-r1` exited 1 because the current
candidate field was missing; its test-patch SHA-256 is
`5afce2e043e59fb300b702c9fc1b61a48c33ca6af0b54a1e1c67b6e2dc23078e`.
The minimal correction renamed only the JSON field to `v0_12_4_aggregate` and
added the permanent regression test. The focused green run recorded zero for
formatting, test, two simulations, byte comparison, new-key presence, old-key
absence, and acceptance. Its source-patch SHA-256 is
`19561ea1c5bd659b47e0fcac1cd68a567740db1875e3799a08b868dc4fe14de3`,
and the accepted executable source was committed and pushed as
`19574b9f9711886c3362c612317d7d64a2167798` on
`codex/pig-v0.11.0-request-aware`.

r3 is the final complete source matrix. It froze all 158 Git-tracked files from
that exact pushed commit:

```text
candidate archive SHA-256: 44f6dde568bb900343f7f9c64c130503bed12cdf4fb4061854d2e4c2329b933a
baseline archive SHA-256: 96d38a1b9371e7af3fec445f87fcf6f2ecd8becb24c97b377cf934703444d0d9
runner SHA-256: 36a390bb7f822c7995afcba8ba9f1f2ba18c2662bec42e07b2af231ad3484e7b
environment: Go 1.24.13 / linux/amd64 / Linux 6.9.0-dstack
evidence: /workspace/evidence/pig-v0124-dedicated-phase-c-r3-19574b9
```

All 24 status rows returned zero: environment, formatting, legacy audit,
lexical-shape corpus, affected packages, full tests, vet, targeted and full
race, all builds, versioned binary, policy-order simulation tests, two
simulations, byte comparison, simulation acceptance, both B/C/C/B benchmark
orders, primary benchmark contract, reservation-aware HTTP benchmark, estimator
benchmark, and additional benchmark contract. The final binary SHA-256 is
`279442b030175b68d28ddc54935c8258f73d6fb7757de399aa6da82f74745f3d`;
the status file SHA-256 is
`5d6cc0da1bb7995f776375324783edc59ab5f8602db8bab7a858429db644e2ad`.

The two simulation JSON files are byte-identical at SHA-256
`5a6c513d6816238e41a74529be9a41708f4849c9d84d599265878dd8bf5df8f4`.
The final report contains exactly one `v0_12_4_aggregate`, no
`v0_12_3_aggregate`, and `"acceptance": "passed"`. Simulation remains a
deterministic diagnostic and does not substitute for the Router-disabled GPU
gate.

The ordered benchmark contracts passed. Relevant medians are:

```text
HTTP pre-forward: 12.628 us baseline -> 13.441 us candidate, +6.44%, 33 allocations unchanged
Manager active-0: 121.4 ns -> 170.8 ns, +49.4 ns, zero allocations
Manager active-48: 3.296 us -> 3.294 us, zero allocations
Manager active-256: 17.223 us -> 16.972 us, zero allocations
Telemetry active-0: 509.4 ns -> 579.5 ns, +70.1 ns, zero allocations
Telemetry active-48: 10.996 us -> 11.532 us, +4.88%, zero allocations
Telemetry active-256: 57.362 us -> 60.057 us, +4.70%, zero allocations
Estimator 4 MiB normal: 154.633 us, zero allocations
Estimator 4 MiB many strings: 21.411 ms, zero allocations
HTTP pre-forward with 48 reservations: 16.629 us median
HTTP pre-forward with 256 reservations: 31.467 us median
```

Final review pass 1, model and causality: the bounded model-neutral estimator
produces the request-size signal consumed by the actual HTTP decision; the
Manager computes post-admit KV and Prefill state including every live
reservation; the enforce result returns 429 before any upstream call. TPS is
telemetry rather than admission authority, and no exact tokenizer, cache lookup,
learning, TTFT gate, request mutation, or routing behavior was introduced.

Final review pass 2, safety, lifecycle, and SOLID: the Manager lock covers
observation, decision, and reservation as one transaction. Known weighted,
exclusive, and quiescent Prefill reservations block new regular work until
Prefill completion or terminal release. Completion, local reject, cancellation,
disconnect, upstream failure, timeout, expiry, unforwarded rollback, and epoch
rebase have exact-once release and immediate-recovery coverage. Request
classification, resource fit, Prefill interference, lifecycle ownership, and
HTTP/Router reporting remain separate responsibilities.

Final review pass 3, efficiency, evidence, and operability: the request-aware
Manager and telemetry scans are linear only in live reservations and allocate
zero in their measured paths. The full pre-forward path remains tens of
microseconds at 256 reservations; the bounded estimator remains below the
user-accepted 100-ms extreme-input ceiling. Logs, metrics, `/upstream/status`,
and Router-compatible capacity project the same enforce verdict, while Manager
sequence identity prevents a prior reject from masking completion or recovery.
The red/green and r3 artifacts bind behavior, version label, source commit,
archive, environment, and hashes without claiming GPU or production success.

This documentation append is non-executable and does not change the r3-tested
binary. It must be committed and pushed separately for durable audit history,
but the local candidate image must still be built from exact executable commit
`19574b9f9711886c3362c612317d7d64a2167798`. No local candidate image, registry
upload, Compose update, PIG replacement, vLLM load, Router change, or production
traffic action has occurred yet.

## 32. v0.12.4 immutable image acceptance and publication

The image gate ran on the dedicated H200 CVM without restarting the CVM or its
long-lived workbench. The host Docker CLI initially lacked buildx. Image r1
therefore stopped before producing an image with explicit
`BuildKit is enabled but the buildx component is missing or broken`. Its runner
SHA-256 is
`a2ee30df6a4777f56fa63e9a56f1cce01117bddc0c7d4dda853b12df440d27d3`,
and its build-log SHA-256 is
`90bd9f73d5cf9c8b8e23d90cfc91e206f07d802c22f4e74919da012f92931d64`.
This is an environment failure, not image or PIG evidence. The failed run and
control files remain separately archived.

The host then installed the official buildx `v0.36.1` Linux amd64 plugin under
the persistent Docker configuration. The GitHub release API and the official
checksums file independently identify the plugin as SHA-256
`48af8a397ebd60178778bf63611dbcebe5f5e7a9be90eb9147b24b9587455778`;
the checksums file is SHA-256
`abeea7a52865e60e1af4995d2449cdbaca762dc99689a829f15f0fd760766413`.
The plugin reported buildx `v0.36.1`, and the existing Docker driver reported
BuildKit `v0.12.5` with `linux/amd64` support. No CVM restart or platform
Compose operation was used.

Image r2 created a detached clean worktree at exact executable commit
`19574b9f9711886c3362c612317d7d64a2167798`, verified all 158 tracked files,
and reproduced source archive SHA-256
`44f6dde568bb900343f7f9c64c130503bed12cdf4fb4061854d2e4c2329b933a`.
The only candidate image build produced:

```text
local image: ghcr.io/phala-network/phala-inference-guard:0.12.4-19574b9-local
image ID: sha256:63356c2ca3e9168d0224eed8bb4cbf7f601fbb72fce33d609f0b2cc312b668c4
binary SHA-256: 5c61f559a2f6c815200c23e81800b32f2e039504cffde37ab26f12cd784ccd26
build-log SHA-256: a14a563fab9792d3f8fffa8f8b74efed2a7cc33789d421715cd6503636143c45
production-contract log SHA-256: 1a502077f979551c39889cff08c56f4ec30e8bfa901ff223f2ef2c24a86ca285
```

The production-image contract was green, including `linux/amd64`, OCI version
`0.12.4`, exact full revision, root distroless entrypoint, native CGO/NVML, and
`NVIDIA_VISIBLE_DEVICES=all`. r2 then exited 127 before smoke because its test
fixture used a login shell whose PATH omitted `/usr/local/go/bin`. This was a
harness-only failure after the image and contract had completed. The fixture
compiler was changed to the workbench's verified absolute Go 1.24.13 path. The
candidate image was not rebuilt.

r3 continued against the same immutable image ID and reached the protected
metrics state: the 3.5 MiB request returned pre-forward 429, its upstream call
count remained unchanged, Router status was yellow, and protection metrics
were active and applied. r3 stopped only because its runner expected
`pressure_source="kv"`. The source contract and permanent HTTP integration test
instead require `pressure_source="none"` for hard-KV rejection: no pre-existing
Prefill pressure caused this decision, while `reason="kv"` and Router
`reason="kv_over_budget"` carry the actual rejection cause. The r3 runner
SHA-256 is
`3c4070823261fa4e7113401b58d1ebf05fb44f02a704fb2b201a8f95251ed2f5`.
The failure is preserved as invalid runner evidence and caused no source or
image change.

r4 corrected only that runner assertion and completed the local-image gate
against the unchanged image ID. It proved default `enforce`, the default
500-ms observer, initialization-only `fallback/busy_fallback` capability,
H200 runtime through `--gpus all`, health 200, unauthenticated metrics 401,
authenticated metrics 200, and a transparent chat 200. The 3.5 MiB hard-KV
request returned 429 in approximately 20.3 ms without reaching the upstream.
The protected state exposed enforced reject count 1, active/applied Router
backpressure, yellow upstream status, request class `quiescent`, observed
running 1, and effective Router limit 2. After two seconds it exposed inactive
backpressure, green upstream status, and effective Router limit 0. Thus the
same verdict was visible in the HTTP result, logs, metrics, status, and Router
capacity projection, and it cleared without a low-flow self-lock.

r4 contains 31 hashed evidence entries. Its evidence-list SHA-256 is
`14e66b9888badad08259711b23daeb1ccccb1ab1c79f84a02a7b39508a657204`,
and summary SHA-256 is
`172b1e641f4142b66c20628b55cf948274992358e3a9476cb4a3476fee2d1cf3`.
An independent verifier recomputed every entry, independently extracted the
image binary, reran the production-image contract, and confirmed the local
image had no registry digest or registry authentication state. Its verification
set and summary SHA-256 values are
`ab792dda3f967e76cb3c8ff564c0b54c0212240726f967eeb10649f63a5a89d8`
and `13d7782808ae42a1df397a93a4f2622661d1d044df85287f4bb06cff90a21bf7`.

Only after that independent local acceptance did r5 authenticate. Authenticated
pre-push checks returned explicit `manifest unknown` for both intended tags.
r5 tagged the exact accepted image ID without rebuilding and published:

```text
ghcr.io/phala-network/phala-inference-guard:0.12.4
ghcr.io/phala-network/phala-inference-guard:0.12.4-19574b9f9711
```

Both tags and the immutable digest reference resolve to:

```text
sha256:455534e0c84014e083fefced342e8c4728c27c8334ff0e2ed1675d90057be621
```

r5 pulled the version tag, revision tag, and digest reference. All three
resolved to local image ID
`sha256:63356c2ca3e9168d0224eed8bb4cbf7f601fbb72fce33d609f0b2cc312b668c4`
with the exact OCI labels and binary SHA-256
`5c61f559a2f6c815200c23e81800b32f2e039504cffde37ab26f12cd784ccd26`.
The digest reference passed the production-image contract again. r5 contains
22 hashed evidence entries; its evidence-list and summary SHA-256 values are
`cde2494953e7b959d7ed579e41101b600cb7663d9e514982f691ed2abff0fa11`
and `aad6b9d93d4d50137b3fa93ca18e5e30edf0ccd4953eba43d6af7b96b8d6cd5f`.

An independent authenticated registry verifier resolved each reference to the
same digest, repeated all three pulls, extracted the digest binary, reran the
contract, recomputed all 22 publication entries, and found no credential
pattern. It removed its isolated Docker authentication configuration. Its
verification set and summary SHA-256 values are
`8dcee91ffbe0499f837609458d58de78351a8a1e8ff9fcafdc40409e2af3ecc0`
and `14624ed5ddb67c7874f3a9884ae9fdce2ebd781931630ea3fd886cb497ebcb19`.
The workbench restart count remained zero.

Release review pass 1, provenance: the source archive, executable commit,
single local build, image ID, binary, OCI labels, registry digest, and every
pull-back reference form one exact identity chain. r3, r4, and r5 never rebuilt
the r2 image.

Release review pass 2, behavior: the production contract and model-neutral
smoke prove default production configuration, authenticated observability,
transparent forwarding, pre-forward hard-KV rejection, coherent Router
projection, and timed recovery. This is image-level evidence, not a GPU
goodput or Decode-QoS conclusion.

Release review pass 3, security and boundary: each Docker/GHCR authentication
configuration remained isolated and was removed; the workbench's GitHub CLI
Device Flow login remains only for authorized source pushes. Evidence passed
credential scanning. No Compose update, PIG deployment, vLLM load, CVM
restart, Router mutation, `use1-cb` enable, or production inference traffic
occurred. Phase D must use the exact published digest on the Router-isolated
dedicated H200 CVM and still complete the targeted GPU and Pareto gates before
any production canary is considered.

## 33. Dedicated-CVM runtime readiness and targeted weighted gate

All subsequent source, simulation, image, and GPU testing is pinned to CVM
`c21b7281-2c25-4453-8a68-f39ec42d03b4`. Reference CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` is read-only configuration reference
only and must not receive test traffic or mutation. The retired builder remains
out of scope. Ordinary iterations replace only PIG; the loaded vLLM container
must not be rebuilt or restarted.

The dedicated runtime uses loopback-only bindings and is absent from Router.
Its frozen identities are:

```text
PIG source: 19574b9f9711886c3362c612317d7d64a2167798
PIG registry digest: sha256:455534e0c84014e083fefced342e8c4728c27c8334ff0e2ed1675d90057be621
PIG image ID: sha256:63356c2ca3e9168d0224eed8bb4cbf7f601fbb72fce33d609f0b2cc312b668c4
PIG container: 702e8334f83ed6b5c38809eb53b86a9bcfc4300e0c8551aa3c0de5065d7bd5f8
vLLM registry digest: sha256:485ec89ea08e6b4ead55f4721b01c053264d747bde685de04cd7d5b114d219fe
vLLM image ID: sha256:f90fe278def6819e682889f6b7dd41a4ba9a1faa0e65c1bddf602fea9754a5c2
vLLM container: d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552
vLLM StartedAt: 2026-08-09T10:03:59.648050825Z
model: google/gemma-4-31B-it
root checkpoint: RedHatAI/gemma-4-31B-it-FP8-block
max model length: 262144
KV capacity / block size: 862437 / 64
```

The vLLM readiness set is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/vllm-readiness-r1`;
its evidence-list SHA-256 is
`979e8b6a424b4aa2e3f23c90ce727f49adc83ae60f7efed4a1fbeb79412289c2`.
The PIG readiness set is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/pig-v0124-readiness-r1`;
its evidence-list SHA-256 is
`2435af634aa7649951edb16b4ea23cc481216377fbc15f0a4e045be3d5138ca3`.
Together they prove backend health and a functional short completion, PIG
default `enforce`, the 500-ms observer, metrics 401 without authentication and
200 with authentication, zero residual reservation, no low-flow self-lock, no
restart, no OOM, and unchanged vLLM identity.

Initialization discovered KV capacity `862437` and derived hard limit `758912`,
but Prefill calibration returned `source=fallback`, `reason=scale_fallback` and
the fixed `65536/262144/524288` thresholds with aggregate budget `262144`.
Metrics deltas isolated the cause: the first approximately 4K-token startup
probe took 10.6828 seconds, or approximately 387 tokens/s, because it absorbed
one-time long-input JIT/warmup. This made the next scale invalid. It is not a
missing-metrics or online-learning failure. Before promotion, restart PIG only
against the already-warm backend and determine whether calibration succeeds.
Section 34 records that diagnostic and supersedes the provisional retry idea:
no warmup/discard/retry loop may become a production startup benchmark.

Targeted weighted r1 used a valid product stimulus but an invalid runner
assertion. During the known weighted Prefill, PIG correctly projected Router
inspect capacity zero and `/v1/upstream-status=2`, meaning intake closed. The
runner incorrectly expected status `1`; r1 is retained as runner evidence and
is not a product red.

Targeted weighted r2 changed only that runner assertion. The runner SHA-256 is
`eb3f3df11aaaaf9331609e1098d8a212d1ee6598c45b1ce851cdb493c7881cf4`.
It ran from `2026-08-09T10:30:12Z` through `2026-08-09T10:31:24Z` against the
same immutable runtime and exited zero. The 81,920-word, 409,792-byte request
was classified weighted and reached the backend. While its Prefill reservation
was live, the snapshot taken before the short request already exposed Router
backpressure active and inspect capacity zero. The short request then received
pre-forward 429 and the coherent verdict was `action=size_protect`,
`reason=prefill_busy`, `pressure_source=prefill`, and upstream status `2`. When
Prefill completed while Decode remained live, the immediate short request
returned 200. Cancellation then returned reservations and Router backpressure
to zero, and the recovery request returned 200. vLLM preemptions stayed `0`,
both container identities were byte-identical before and after, and the log
window contained no fatal, OOM, or restart evidence.

The r2 evidence is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/targeted-weighted-r2`.
Its evidence-list SHA-256 is
`836602925666aa4375e11a6d7f009691bc1eeb47fe3a4dcaecc00b48dd80cad2`.
This closes only the targeted weighted-Prefill gate. Exclusive/quiescent test
overrides, terminal lifecycle recovery, low-flow and no-demand non-locking,
stale/recovery, same-snapshot burst, near-KV, Decode QoS, and the three-round
A/B plus B/A Pareto matrix remain open. Router and `use1-cb` remain unchanged.

## 34. Warm-backend diagnostic and v0.12.5 no-calibration correction

The isolated warm-backend diagnostic used only
`c21b7281-2c25-4453-8a68-f39ec42d03b4`. It recreated PIG from the same v0.12.4
image with `--no-deps --force-recreate --pull never`; vLLM was not restarted,
rebuilt, or included in the Compose operation. The vLLM container ID, image ID,
StartedAt, restart count, OOM state, and GPU process identity were byte-identical
before and after.

On the already-warm backend, the same v0.12.4 initializer changed from the cold
run's `fallback/scale_fallback` to `startup_calibration/calibrated`. It observed
approximately 7,943 Prefill tokens/s, stored a safety-adjusted value of 6,354,
and derived `31744/127040/254144` with aggregate `127040`. The short static
completion fixture returned 200, final reservations and Router backpressure
were zero, vLLM preemptions remained zero, and both fatal scans were zero. An
earlier inline curl returned 400 because multi-shell quoting destroyed the JSON;
it is explicitly retained as invalid fixture evidence and is not a PIG result.

The diagnostic evidence is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/calibration-warm-restart-r1`.
Its 20-entry evidence-list SHA-256 is
`de9bf55023867c993561dd0254d1c62dc69a41b4dbcd406a157a87d93d8551b3`,
and summary SHA-256 is
`49c7b9befa66445cce595935f4b528f655b481f2a653cac732d6bbc36ad56acc`.

This comparison proves that active startup calibration is not an acceptable
production default. It creates upstream work before PIG listens, consumes up to
a 15-second initialization window, and makes policy depend on whether the first
probe absorbs backend JIT/warmup. Adding more performance probes or retry state
would increase startup load and complexity without making the result a stable
capacity contract. v0.12.5 therefore removes the production completion probes,
the observed/safe Prefill-rate profile field, and its metric. This is not a
change from one learning algorithm to another; no learned Prefill or KV state is
allowed.

The v0.12.5 initialization contract is:

```text
startup vLLM metrics
  -> model identity, KV capacity, block size, idle/busy state
read-only /v1/models metadata when automatic Prefill mode is selected
  -> max model length, with bounded geometry fallback if metadata is unavailable
effective context span
  = block_align_down(min(max model length, KV hard limit))
regular
  = block_align_down(min(64 Ki tokens, effective context span / 8))
exclusive
  = block_align_down(min(256 Ki tokens, effective context span / 2))
quiescent
  = block_align_down(min(512 Ki tokens, effective context span))
aggregate
  = exclusive
```

If model metadata is unavailable, effective context span is
`min(512 Ki tokens, KV hard limit)` and the profile reason records
`metadata_fallback`; PIG must not send a completion to recover metadata. An
invalid span or non-strict block-aligned ordering fails initialization rather
than inventing a rate. A complete four-value explicit Prefill override remains
available for controlled tests; partial overrides remain invalid. KV hard limit
continues to derive once from reported KV capacity, block size, and the fixed
hard ratio. Backend running or waiting work does not change the derived profile:
metadata reads are passive, so a PIG-only restart during existing work must not
silently switch policy to a busy fallback. None of these values update after
startup.

For representative model lengths, the deterministic automatic result is:

```text
32 Ki context  -> 4 Ki / 16 Ki / 32 Ki, aggregate 16 Ki
256 Ki context -> 32 Ki / 128 Ki / 256 Ki, aggregate 128 Ki
650 Ki context -> 64 Ki / 256 Ki / 512 Ki, aggregate 256 Ki
```

The SOLID ownership boundary is explicit. The vLLM startup reader owns metrics
and model identity. A metadata reader owns the bounded `/v1/models` contract. A
pure capability-profile constructor owns geometry and validation. The Manager
consumes the immutable profile and remains the sole owner of atomic admission
and reservation lifecycle. Observability reports the derived source, reason,
KV geometry, and Prefill thresholds but no synthetic performance rate.

Plan review pass 1, model and causality: the measured cold-versus-warm
divergence invalidates active startup performance sampling. The replacement
uses only model length and KV geometry that causally bound feasible request
size; it does not claim to predict backend throughput.

Plan review pass 2, safety and lifecycle: block alignment, strict ordering,
hard-KV capping, metadata fallback, complete explicit overrides, and busy-state
invariance are explicit. No startup request creates a reservation, cache state,
GPU work, cancellation path, or hidden lifecycle owner.

Plan review pass 3, efficiency and evidence: automatic initialization adds at
most one bounded read-only metadata request and no new production option. The
hot admission path remains unchanged. v0.12.5 must produce new red/green,
source, race, simulation, benchmark, image, and GPU evidence; no prior binary or
image result is promoted across the executable change.

The required red/green order is:

1. Add a red server test proving automatic initialization performs one bounded
   metadata read and zero completion calls even when backend work exists, plus
   table tests for 32 Ki, 256 Ki, 650 Ki, KV-limited, metadata-fallback,
   explicit, and invalid geometry cases.
2. Add red observability and lexical tests proving the safe-rate profile field,
   metric, log key, and calibration-only completion path are absent; bump every
   executable and evidence identity to `0.12.5`.
3. Implement the smallest metadata-plus-geometry path and delete unreachable
   calibration code. Run formatting and affected packages first; accept and
   push only a coherent focused green source update.
4. Freeze that pushed source and rerun the complete source, vet, race,
   simulation, ordered benchmark, production binary, lexical, and three-review
   matrix. No v0.12.4 executable evidence carries across the source change.
5. Only after complete source green, build one local immutable v0.12.5 image,
   run production-contract and model-neutral smoke, then replace only PIG on the
   dedicated CVM. Record unchanged vLLM identity before and after.
6. Repeat weighted/exclusive/quiescent, lifecycle, low-flow/no-demand,
   stale/recovery, burst, near-KV, Decode QoS, and three-round A/B plus B/A
   Pareto gates. Upload the image only after all source, image, and GPU gates
   accept the exact same image ID. Router and production remain unchanged.

## 35. v0.12.5 no-calibration focused implementation

The test-first red was run only in the `pig-v0124-workbench` container on
`c21b7281-2c25-4453-8a68-f39ec42d03b4`, against base HEAD
`6d5fc0bef14c2dc5c927c2c3a00dce3ce6624a2e`. The capability-contract,
server-initialization, and observability tests each exited `1` for the intended
reason: observed/safe rate fields remained, automatic startup still submitted
completion work or changed under busy state, and the safe-rate metric remained
in production output. The red evidence is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/v0125-no-calibration-red-r1`;
its `SHA256SUMS` file hash is
`c65d0455e7eccc3a621f68a49e45219377f603bfb59c49a50999e39219545e2d`.

The implementation replaced `predictive_capability_calibrator.go` with a
metadata-only initializer and changed the pure capability constructor to schema
`request-aware-capability-v2`. Automatic profiles now have source `automatic`
and reason `metadata` or `metadata_fallback`; explicit profiles retain source
`explicit` and reason `explicit_override`. The initializer performs at most one
bounded `/v1/models` request and contains no completion, warmup, retry, cache,
or performance-probe path. The safe-rate field, production log key, and metric
were deleted. Runtime, Docker, README, observability, algorithm, and simulation
identities are `0.12.5`.

The first affected-package run found an incorrect KV-limited test expectation:
`264000 / 2` must be aligned down to `131968` at a 64-token block size. The
subsequent server-wide run exposed two old-calibration assumptions. First,
`CapabilityMetricsOK` represented optional performance-calibration counters and
must not gate metadata-plus-KV initialization. Second, quiescent-at-or-below-hard
is an automatic-profile invariant, not a blanket restriction on controlled
explicit test profiles; the hard resource gate remains authoritative. Both
ownership errors were corrected rather than hidden by narrower tests.

The final focused r3 gate ran `gofmt`, `git diff --check`, a production-source
retired-symbol scan, and these five packages with `-count=1`:

```text
./internal/runtime/predictive
./internal/app/server
./internal/observability/metrics
./internal/simulation/requestaware
./cmd/pig-request-aware-sim
```

All five exited `0`. The evidence is under
`/var/volatile/dstack/persistent/pig-v0124/evidence/v0125-focused-green-r3`.
The pre-commit tracked patch SHA-256 is
`793237fd174af89bee91d32497212f6aad0157c5a4520da9c217ce6364de655c`,
and the evidence `SHA256SUMS` file hash is
`d63266e9c66e025cd128ae01376a6651e4345410bb2642214482812bd75533e3`.
The 25-path coherent source was committed as
`26e5369` (`fix:v0.12.5-no-calibration-capability`) and pushed to
`origin/codex/pig-v0.11.0-request-aware`.

Focused review pass 1, model and causality: request size policy remains on the
pre-forward path; model length and KV geometry bound request classes without
claiming to measure throughput. Busy and idle startup derive byte-identical
profiles, and automatic/explicit/fallback calls are counted explicitly.

Focused review pass 2, safety and lifecycle: automatic bounds are block-aligned,
strictly ordered, and capped by hard KV. Metadata failure is bounded and passive.
No startup request creates GPU work, reservation, cancellation, or reconciliation
state, and no runtime PIG or vLLM container was restarted or replaced.

Focused review pass 3, SOLID and evidence: the metadata reader owns one HTTP
contract, the pure profile constructor owns geometry, the factory owns assembly,
and Manager ownership is unchanged. Focused green does not satisfy the complete
source, race, benchmark, image, GPU, Pareto, registry, or production gates; those
remain pending in the exact order above.

## 36. v0.12.5 complete source matrix and three-pass review

The complete Phase C source matrix ran only in `pig-v0124-workbench` on
`c21b7281-2c25-4453-8a68-f39ec42d03b4`. The authoritative repository was clean,
and both HEAD and its upstream were
`0668c3876dd64ba1c0862eb92ad36a2489d96cad`. The exact pushed source archive was
`/workspace/incoming/pig-v0125-0668c38-source.tar.gz`, SHA-256
`5fdd99d16c60741513ff1637ec21e1a7db6fb1333b28b8a728cb51230a1b7da0`.
The runner used Go `1.24.13` on Linux `6.9.0-dstack` in workbench container
`2c14ed1bca84` and wrote evidence to
`/workspace/evidence/pig-v0125-dedicated-phase-c-r1-0668c38`.

All 26 registered steps exited `0`: environment and version identity, formatting,
legacy and active-calibration audits, lexical corpus, affected and full tests,
vet, targeted and full race, full build, versioned binary, policy-order tests,
two deterministic simulations plus byte comparison, ordered baseline/candidate
benchmarks in both orders, benchmark contract, reservation HTTP benchmark,
estimator benchmark, and the additional benchmark contract. A fresh
`sha256sum -c` verified every file in the evidence manifest. Material hashes are:

```text
binary                 97a0ce94ab046c2408b30483b17731d17a69b04b0d755e471620043a042f4d9e
statuses.tsv            7772aedb8a74f69107ac6882992bceea2d5b49e995cd49328f61a002c410ab0d
evidence-sha256         c718da5a3ee14e9750fff896d8bc1ddb70c44c22fd168ddad157bd251cbb9456
simulation-1.json       6077516956d4b01d093bf7523745547fe7243f202ca3a6165e37c8f5a7bcdcb1
simulation-2.json       6077516956d4b01d093bf7523745547fe7243f202ca3a6165e37c8f5a7bcdcb1
```

Ordered benchmark medians remained within the section 8 contract. Full HTTP
pre-forward cost changed from `12.895 us` to `13.520 us` (`+4.85%`) with
allocations unchanged at `33`. Manager active-48 changed from `3319 ns` to
`3317.5 ns`; active-256 changed from `17364.5 ns` to `17020 ns`. Candidate pure
Policy cases were `83-88 ns`, zero-allocation, below the `250 ns` absolute cap;
their roughly `2.0-2.74x` ratio to the simpler v0.12.2 helper is not an HTTP
latency regression. Candidate telemetry was approximately `607 ns`, `12.077 us`,
and `62.815 us` at 0, 48, and 256 reservations, with zero allocations.

The model-neutral estimator remained zero-allocation. Representative medians
were approximately `279 ns` at 1 KiB, `1.975 us` at 64 KiB, `27.817 us` at
1 MiB, `158.476 us` for a normal 4 MiB body, and `22.005 ms` for the pathological
4 MiB many-short-string body. The last value is below the explicitly accepted
`100 ms` extreme-input ceiling and does not relax the normal-body result.

Deterministic aggregate results must be read in both directions:

```text
metric                         v0.12.2       v0.12.5
admitted                            145            205
completed                           123            134
completion tokens/s               94.12          98.23
SLO completion tokens/s           84.94          88.12
preemptions                            1              1
TPS-floor violation seconds       20.70          25.00
waiting seconds                     5.00          80.60
maximum idle with demand            0.40           0.40
peak KV tokens                    798446         798446
```

Thus the deterministic model shows higher raw and SLO-compliant goodput with no
preemption, KV, or low-flow self-lock regression, but materially worse waiting
and a `4.3 s` increase in synthetic TPS-floor violation. The simulation contract
intentionally treats those two values as diagnostics because its synthetic
coefficients cannot establish GPU QoS. This result closes the source simulation
gate but is not Pareto acceptance; the Router-disabled Decode-QoS and ordered GPU
matrix must reject the candidate if the same degradation appears without the
required goodput tradeoff.

Complete review pass 1, objective and causality: the real HTTP integration proves
that the approximate request-size signal changes the enforce decision before an
upstream call. Automatic capability initialization performs at most one passive,
bounded `/v1/models` read, and a same-state profile test proves geometry can
change a pre-forward class decision. It does not send completions or claim that
context length predicts Prefill rate. A read-only check against the unchanged
dedicated vLLM returned the single served identity and `max_model_len=262144`,
matching the initializer contract.

Complete review pass 2, safety, lifecycle, and SOLID: the pure profile constructor
owns alignment and invariants; the metadata reader owns bounded I/O and fallback;
ResourceGate owns post-admit KV fit; InterferenceGate owns request-class policy;
Manager owns the atomic decision/reservation transaction; and the adapter owns
HTTP projection and lifecycle. Redirects, environment proxies, authorization
forwarding, oversized bodies, partial overrides, invalid geometry, busy startup,
metadata failure, duplicate IDs, stale observations, cancellation, epoch reset,
and concurrent lifecycle paths are covered. Both race matrices passed. No
learner, active calibration, cache inspection, routing, TTFT gate, request
mutation, or tier/priority injection was reintroduced.

Complete review pass 3, efficiency, evidence, and operability: the archive,
binary, statuses, deterministic outputs, and evidence manifest are independently
hashed; ordered performance contracts pass with no added hot-path allocation;
and the pathological estimator remains below the user-approved ceiling. vLLM
and the existing v0.12.4 PIG runtime retained their prior container IDs,
StartedAt values, zero restart count, and non-OOM state throughout the matrix and
review. The next commit changes only this plan, so executable evidence remains
attached to `0668c38`; its diff must be audited as documentation-only before the
local image build. Image construction, image contract, smoke, PIG replacement,
GPU/Pareto validation, registry upload, Router integration, and production proof
remain explicitly open.

## 37. v0.12.5 immutable local image acceptance

After section 36 was committed and pushed as documentation-only commit
`045591cde89f554c4daef81ce4ddb14b737a7b1d`, the authoritative repository was
clean and byte-identical to its upstream. The image runner proved that the only
delta from executable commit `0668c38` was this plan, created a detached clean
worktree at `045591c`, and archived all 158 tracked files as SHA-256
`77b0d3034f82bb5509c01a9622d5c8c13d13cb78b04859a69643b9c6327ac819`.
It used the previously verified buildx `v0.36.1` through a new Docker config
with no registry authentication and built exactly once with no pull or push.

The accepted local image is:

```text
tag:       ghcr.io/phala-network/phala-inference-guard:0.12.5-045591c-local
image ID:  sha256:8096b132425648f609f2257436ed58e9d2cdb738b55ef7ed0c0f7081d5f9abdf
binary:    72919a08859e0ed8c5e6f3ad1738c405dbec4209e53e0c00d532a906fa136bfc
platform:  linux/amd64
OCI:       version=0.12.5, revision=045591cde89f554c4daef81ce4ddb14b737a7b1d
runtime:   root distroless entrypoint, native CGO/NVML, NVIDIA_VISIBLE_DEVICES=all
registry:  RepoDigests=[], auth absent, upload not attempted
```

The production-image contract passed twice against that same image ID: once in
the primary run and once in independent verification. Default production smoke
used no predictive override. It proved `enforce`, the 500-ms observer,
`request-aware-capability-v2`, source `automatic`, reason `metadata`, and derived
Prefill bounds `65536/262144/524288/262144`. The fixture exposed
`max_model_len=1000000`; startup inference-call count remained zero, proving the
metadata read did not become a completion probe. The retired safe-rate metric
and calibration log vocabulary were absent.

Health returned 200, unauthenticated metrics 401, authenticated metrics 200,
and transparent chat 200. A 3.5 MiB model-neutral request returned pre-forward
429 in `21.045 ms`; the upstream inference-call count remained one from the
prior chat. The HTTP response, enforced-reject counter, request-aware action and
reason, load-scoped Router backpressure, inspect capacity, upstream status, and
decision log all reported the same hard-KV protection. The smoke container ran
with the GPU device request and no fatal, panic, OOM, or retired-calibration
pattern.

Primary evidence is under
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0125-image-r1-045591c`.
All 42 evidence rows and all four independent-verification rows passed fresh
`sha256sum -c`. Material hashes are:

```text
runner                         351c81474d007445d314e3c3246e9166022597cb283c7e68e5e3906bb81fc88c
build.log                      33696fbb2efad7d537dbbe37f6ce559a8bf53760260908f3152e8098902d72c0
production-image-contract     067b57cc9d558e3afe2ff845e884b43aed8a6ba2d4d3cdc7dd9f98d3f907ca0d
evidence-sha256               e0ce1481489162c9178348c8907d47c5a0d884cfec008d8aed50f1a9469f27d4
independent sha256sums         13e657688801d26464ccee7739208a44ed5696361c7bdcbd1928e7f4e562c8c5
summary                        f21f1daa6d813a871ee62e9db0a61374c51b9d947865299dd4b20a8fcc66c149
```

The primary runner's functional recovery assertion passed, but its displayed
`0.000000 s` used BusyBox `date` with an unsupported nanosecond formatter and is
not latency evidence. A no-rebuild continuation therefore repeated the same
hard-KV and Router-recovery path against the exact image ID. Startup inference
calls again remained zero, the 3.5 MiB request returned pre-forward 429 in
`20.605 ms`, protected status was 1, and status returned to 0 on poll 15 of a
100-ms loop, establishing a `1.5 s` polling upper bound consistent with the
fixed projection hold. Metrics were active before and inactive after recovery;
the request never reached the fixture.

The continuation evidence is under
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0125-image-r2-recovery-045591c`.
Its runner, evidence manifest, manifest-check, and summary SHA-256 values are
`317f582f9734538dad300404b4236ff611e901215df128fc6b4f021d6ffdbda6`,
`6d4009cd843c684318caa1321cccce1b65dc237066c1f276dadf79e6d87061db`,
`4049f4a198ae5cd1938de63a81bd849f37e4b87a3c0c04cf2520947e5278b548`,
and `41aac707214f69a06222e5b5bba3762bc1f4569cbe6aa7690b882949dd7f384b`.

The smoke and audit left no temporary container or fixture process. The existing
vLLM, v0.12.4 PIG, and workbench retained their exact container and image IDs,
StartedAt values, zero restart count, and non-OOM state. This closes only the
local image gate. The image remains deliberately unpublished and undeployed;
the next step may replace only PIG on the dedicated CVM, then must complete the
targeted GPU and ordered Pareto matrices before any upload.

## 38. v0.12.5 dedicated runtime rejection and v0.12.6 Decode envelope

The exact local image from section 37 was deployed only to
`c21b7281-2c25-4453-8a68-f39ec42d03b4`. The deployment runner verified the
original Compose SHA-256
`408480f4b66f6785a05500f81eb0737059c72b3522e7b80d541278d8ce1aa0b2`,
created exact rollback and candidate copies, proved that the only candidate
change was `pig.image`, fixed the original Compose project and working directory,
and ran `up -d --no-deps --force-recreate --pull never pig`. It neither named nor
recreated vLLM. The runner SHA-256 is
`671ca8455732584aa94336a3bb83ba4149a520aeafdd3f4b08d3baed2352edd6`.

The vLLM identity remained exactly
`d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552`,
StartedAt `2026-08-09T10:03:59.648050825Z`, restart count zero, and non-OOM.
The running PIG changed to container
`f71a089b16fc2ef030d94fbd2f930eb13af0ba3599f0a256b2f70c759d9215de`
on image ID
`sha256:8096b132425648f609f2257436ed58e9d2cdb738b55ef7ed0c0f7081d5f9abdf`.
Health, model identity, transparent chat, metrics authentication `401/200`,
default `enforce`, 500-ms observation, zero Router protection, and fatal scans
all passed. The live automatic profile was `request-aware-capability-v2`,
source/reason `automatic/metadata`, hard KV `758912`, and Prefill bounds
`32768/131072/262144/131072`. The 38-file evidence manifest SHA-256 is
`d48427e729636edf3ab987a3d96b0d6609d4158c7f1c12bf4a0cc70d6ea1fc89`
under
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0125-runtime-r1-045591c`.

Positive targeted checks used nonce-separated requests through the real PIG and
vLLM chain:

- weighted Prefill protection, cancellation, post-Prefill progress, Router
  recovery, and zero preemption passed;
- a 12-request same-snapshot 16K burst admitted seven and rejected five in at
  most `20.36 ms`, with peak pending Prefill `130053`, no preemption, and exact
  terminal release;
- the 120K fixture reached the exclusive class; a second exclusive request was
  rejected as `prefill_concurrency` in `4.01 ms`, a regular request was protected
  during Prefill, and both immediate post-Prefill and post-cancel progress passed;
- a 270K quiescent candidate was rejected pre-forward during Decode in
  `10.52 ms` with zero vLLM prompt-token delta, while a regular request progressed;
  a successful quiescent GPU request cannot exist on this target because its
  automatic quiescent threshold equals `max_model_len=262144`; and
- twenty low-flow requests all returned 200 with Router protection continuously
  zero; an upstream 404 released its reservation and the next request returned
  200 without a business-request-dependent unlock.

The mechanical summaries above passed, but they did not assert the product
Decode floor. The dedicated Decode-QoS diagnostic then held four streaming
Decode users active and admitted one 49K weighted Prefill. Three repetitions
produced:

```text
run   baseline per-user event TPS   Prefill TPS   ratio    recovery TPS
r1                         43.570         3.350   7.69%          42.250
r2                         44.572         3.201   7.18%          43.250
r3                         44.898         3.502   7.80%          43.574
```

The independent vLLM generation counter agreed: baseline aggregate Decode was
`866--891 tokens/s`, fell to `67--71 tokens/s` during Prefill, and recovered to
`838--865 tokens/s`. Preemptions remained zero. Summary SHA-256 values for the
three repetitions are
`ca1ca35d261bf532e9a4a5b642dc0ed6621e270784589859c52f87d3a7d2090f`,
`fa267fd96f969dab5e7d6f5acc68ed3d7cfde8c33f527f3cda2174a3ddd9d467`,
and `fd1d8ded14c13a8dbad389d92b58bec44e673efd4cf4935c54d7cc1dd3621dab`.
The candidate therefore fails Decode QoS; recovery after the damage does not
make a pre-forward policy protective.

A bounded Router-disabled impact curve confirmed that merely lowering the
weighted boundary is insufficient. With the same four Decode users, sequential
4K, 8K, 16K, and 32K Prefill fixtures left respectively `26.72%`, `19.84%`,
`13.28%`, and `9.45%` of baseline output rate during their Prefill intervals.
Their intervals were `1.44 s` for four 4K requests, `2.15 s` for three 8K,
`3.04 s` for two 16K, and `3.59 s` for one 32K. This is diagnostic evidence for
a bounded exposure rule, not a production startup calibration and not a learned
rate. The exact fixture script SHA-256 is
`ed833365463d18e24dc8d558443256e0f6d3bda0b06c0dc5504032cdbdab357e`.

The v0.12.5 near-KV, stale/recovery, and Pareto matrices are cancelled because
the executable candidate is already rejected and the correction changes its
pre-forward decisions. The local image remains unpublished. Production Router,
`use1-cb`, and actual traffic were not touched.

The active v0.12.6 envelope is deterministic:

```text
active_decode_sequences = reconciled effective Decode sequences
post_admit_prefill_tokens = pending Prefill tokens plus the candidate
decode_interference_budget = immutable profile regular Prefill tokens

if active_decode_sequences == 0:
    no Decode-interference restriction
else:
    decode_interference_charge =
        post_admit_prefill_tokens * active_decode_sequences
    admit only when decode_interference_charge <= decode_interference_budget
```

Multiplication is checked for overflow. Equality admits. The Manager's atomic
state includes every unabsorbed local reservation before this calculation, so a
same-snapshot burst cannot bypass the total-work bound. Zero Decode remains
work-conserving and the existing Prefill-vs-Prefill and hard-KV gates remain
authoritative. No wall-clock Prefill rate, probe, learned multiplier, cooldown,
retry credit, cache state, tier, priority, or new production environment option
is introduced.

`DecodeEnvelope` owns only the product above. `InterferenceGate` owns only
Prefill-vs-Prefill serialization and budget; it no longer independently checks
Decode activity for quiescent requests. `ResourceGate` retains first precedence,
then Prefill interference, then Decode interference. The policy reports reason
`decode_interference`, pressure source `decode`, and charge/budget ratio through
the existing pressure field. Existing metrics already expose the immutable
regular budget, effective Decode sequences, post-admit pending Prefill tokens,
reason, source, action, and enforced-reject count; the decision log exposes the
same values.

A Decode-envelope rejection is request-scoped. It returns pre-forward 429 and
increments rejection telemetry, but does not activate node-wide Router
backpressure: the same backend may still fit a smaller request. Pending-Prefill,
hard-KV, preemption, stale, and unavailable conditions retain their existing
load/availability Router scope. Focused HTTP tests must prove both halves:
the oversized request never reaches upstream and is visible in log/metrics,
while `/v1/upstream-status` remains zero and a fitting request progresses.

The v0.12.6 execution order is:

1. Commit and push this evidence/design update without executable changes.
2. Add focused red tests for envelope boundaries, overflow, atomic reservations,
   quiescent ownership, HTTP enforcement, metrics/log projection, request-scoped
   Router behavior, low-flow recovery, and no active calibration.
3. Implement the smallest vertical slice, set every runtime/image identity to
   `0.12.6`, run formatting and affected packages, then commit and push coherent
   focused green.
4. On that exact pushed source, rerun full tests, vet, race, deterministic
   simulation, ordered benchmarks, lexical corpus, production build, and the
   three reviews. No v0.12.5 executable result closes a v0.12.6 gate.
5. Build one new local immutable image without push; pass image contract and
   smoke, then replace only PIG on the dedicated CVM while preserving exact vLLM
   identity.
6. Rerun weighted/exclusive/quiescent, burst, terminal, low-flow/no-demand,
   stale/recovery, near-KV, and Decode-QoS. With four Decode users, a fitting 4K
   request must preserve progress; requests above the fixed envelope must be
   pre-forward request-scoped rejects, leave Router open for a fitting request,
   and keep the attempted-request Decode window above the frozen floor because
   no Prefill reaches vLLM.
7. Only after targeted green, run at least three no-enforcement/v0.12.2/v0.12.6
   repetitions in A/B and B/A order. The section 4 goodput and QoS thresholds
   remain unchanged; the envelope is rejected if it protects Decode by
   materially reducing SLO-goodput or creating long-request starvation.
8. Upload only the exact image ID that passes every source, image, GPU, and
   Pareto gate. Production remains unchanged until a later authorized canary.

Review pass 1, model and causality: the repeated GPU result proves a causal hole
in the current pre-forward decision: `EffectiveSequences` was observed but did
not constrain weighted/exclusive arrivals. The new formula makes request size,
all pending Prefill reservations, and affected Decode users change the current
decision before upstream work. It does not claim context geometry predicts a
Prefill rate. The Pareto gate remains mandatory because the proposed regular
budget may be too strict under sustained Decode demand.

Review pass 2, safety and lifecycle: checked addition already constructs
post-admit Prefill work; checked multiplication prevents overflow; Manager lock
scope makes the decision and reservation atomic; every existing terminal,
cancellation, disconnect, timeout, rollback, stale, and epoch path retains one
owner. Zero Decode bypasses only the new envelope, not hard KV, freshness, or
Prefill interference. A request-scoped reject creates no reservation, cooldown,
Router hold, or future-request unlock dependency.

Review pass 3, SOLID, efficiency, and operability: a separate pure envelope
prevents Decode policy from leaking back into Prefill classification. It adds
constant-time integer work and no map, allocation, learner, sampler, goroutine,
or production knob. Existing observability fields carry the full formula and a
bounded reason enum. Focused and full HTTP/Manager benchmarks must show no added
allocation and remain within section 8 limits. The plan does not promote the
formula from design evidence; red/green, GPU, and Pareto results may still reject
or revise it.

## 39. v0.12.6 red, implementation, and complete source evidence

The focused red ran only in pig-v0124-workbench on
c21b7281-2c25-4453-8a68-f39ec42d03b4 against the pre-implementation tree.
Runtime, server, and metrics packages each exited 1 for the intended behavior:
the over-boundary and same-snapshot requests were admitted, quiescent ownership
remained in the old Prefill gate, the adapter did not produce the request-scoped
mapping, the HTTP request reached upstream, and the metrics normalizer rejected
decode_interference. The final red fixture hashes are recorded in
/workspace/evidence/v0126-decode-envelope-red-r2; its SHA256SUMS file SHA-256
is 487cf4a1757f28516a53403fc5320efa16742663f831ac761be4a1e024d3606c.

The coherent implementation is pushed commit
9167c2a146ea8762b024e468f568ca5feec5b226. Its immutable source archive is
/workspace/incoming/pig-v0126-9167c2a-source.tar.gz, SHA-256
ceda8975ad520e49a8ffdb7e4dce026ddf1f6ebbb2f20d70feac2a6e8e1936e0.
Focused runtime, server, and metrics tests passed before the affected-package
matrix. The implementation and test diff contains the pure Decode envelope,
gate precedence, Manager reservation integration, HTTP/Router projection,
bounded observability labels, deterministic simulation identity, and version
0.12.6; it adds no active calibration or production configuration.

The first complete runner correctly passed every product test, race,
simulation, build, and benchmark gate but reported
decode_envelope_contract=1 because its new lexical assertion required exactly
one space after a Go struct-field colon. gofmt inserted alignment spaces. This
is retained as invalid runner evidence under
/workspace/evidence/pig-v0126-dedicated-phase-c-r1-9167c2a; no status was
edited or waived. The corrected runner uses a POSIX whitespace expression, has
SHA-256
97f94b04bd52e33d7db1bfd2e179cc7edc336730742c2ccbe9c58d606ba2e23a,
and passed sh -n plus a standalone traced contract diagnostic before rerun.

The authoritative complete source matrix is
/workspace/evidence/pig-v0126-dedicated-phase-c-r2-9167c2a. It ran in
container 2c14ed1bca84 with Go 1.24.13 on Linux 6.9.0-dstack. All 27 status
rows are zero: environment and version identity, formatting, legacy and
active-calibration audits, Decode-envelope wiring, lexical corpus, affected and
full tests, vet, targeted and full race, all-package and versioned production
builds, policy-order tests, two byte-identical simulations, simulation
acceptance, four B/C/C/B ordered benchmark runs, both benchmark contracts,
reservation-aware HTTP, and the estimator matrix. overall=0. Every listed
artifact passes sha256sum -c; the evidence-sha256 file itself has SHA-256
7f3a72bfd68fafd74bd88e1071986ff582b88c88db19e610623f06dc9545eb4d.

The ordered benchmark contract retained allocation counts and passed every
section 8 limit. Candidate full HTTP pre-forward was 13,426.5 ns/op versus
13,122.0, ratio 1.023205, with 33 allocations for both. The zero-reservation
Manager was 193.8 ns/op, a 71.8 ns absolute increase, with zero allocations;
48 and 256 reservation ratios were 0.998041 and 0.987431. The worst pure-Policy
candidate median was 109.3 ns/op, below 250 ns and less than 100 ns above
baseline, with zero allocations. The normal 4 MiB estimator was 156,699 ns/op;
the bounded many-string pathological case was 22.019430 ms, below the
user-approved 100 ms ceiling, both with zero allocations.

The deterministic aggregate remains diagnostic only:

    metric                         v0.12.2       v0.12.6
    SLO completion tok/s             84.944        105.339
    raw completion tok/s             94.120        106.306
    preemptions                           1              1
    TPS-floor violation seconds       20.7            4.7
    waiting seconds                     5.0           80.0

The adverse large-only counterexample is retained: v0.12.2 produced
91.898 SLO-compliant tokens/s while v0.12.6 produced 30.000. Consequently the
source matrix does not establish Pareto safety or deployment readiness.
Controlled targeted GPU and ordered A/B plus B/A evidence remain mandatory.

Complete review pass 1, model and causality: fixed-state boundary, overflow,
zero-Decode, quiescent, same-snapshot, and real HTTP tests prove that candidate
size, pending Prefill work, and effective Decode users causally change the
pre-forward decision. The initializer remains passive and the policy does not
claim to measure or calibrate a Prefill rate.

Complete review pass 2, safety and lifecycle: Resource, Prefill-interference,
and Decode-envelope precedence is explicit. Checked arithmetic, the Manager
lock, exact-once Prefill release, terminal/cancel/disconnect/timeout/epoch
paths, shadow side-effect freedom, request-scoped rejection, fitting-request
recovery, full race, and no-active-calibration audits all passed on the exact
pushed archive.

Complete review pass 3, efficiency, evidence, and operability: the pure
envelope has no allocation or mutable owner, the full HTTP and reservation
paths satisfy their contracts, logs and metrics expose the enforced decision
while Router remains open, and source plus evidence identities are independently
hashed. The running PIG remained
f71a089b16fc2ef030d94fbd2f930eb13af0ba3599f0a256b2f70c759d9215de
and vLLM remained
d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552;
both had restart count zero and OOMKilled=false. No image was built, uploaded,
or deployed, and production Router plus use1-cb remained untouched.

## 40. v0.12.6 immutable local image acceptance

After section 39 was committed and pushed as documentation-only commit
8dec49f6a87bf83b807f565226baa74ec109d1d4, the authoritative worktree was
clean and byte-identical to upstream. The image runner created a detached clean
worktree at that revision and proved that its only delta from tested executable
commit 9167c2a146ea8762b024e468f568ca5feec5b226 was this plan document.
The 162-file archive has SHA-256
8b69fb92a5f945de63bf8c6aa0f5d4ea4b60b702fe4a9d70e38b5714d9437808.
The isolated Docker configuration contained no registry authentication. The
runner used the verified buildx installation and performed exactly one local
build with pull disabled and no push.

The accepted local image is:

    tag:       ghcr.io/phala-network/phala-inference-guard:0.12.6-8dec49f-local
    image ID:  sha256:7d8c34f580b5b4d3358b5b89b0a4b99ab1a196fd1fd7c948bba734730a729f3c
    binary:    fce70fd67b68efc2a264cf038643a5964e2e56ce1e044b91aa831bb5904b35d6
    platform:  linux/amd64
    OCI:       version=0.12.6, revision=8dec49f6a87bf83b807f565226baa74ec109d1d4
    runtime:   root distroless entrypoint, native CGO/NVML, NVIDIA_VISIBLE_DEVICES=all
    registry:  RepoDigests=[], auth absent, upload not attempted

The primary r1 runner SHA-256 is
37a26ab43f566329d3322e84fcbf6e6de9d8aaf502c45b87e970708711219879.
Its build log and first production-image contract passed with SHA-256 values
d3e16f9a423cb56da455dad43ac152a119b2e7a8daa548d51020c5ed5b4d5273 and
d1f30f6e625214b8a9d849bd79c619e8a51cbc8d318cc4cfa4de302c8024a4c2.
The first Decode smoke also returned pre-forward 429 in 1.284 ms, did not
increase upstream calls, kept Router status zero, and exported
decode_interference with request scope. r1 nevertheless exited 1 because the
runner incorrectly expected the live post-admit pending-Prefill gauge to retain
the rejected request. The correct live value was zero because no reservation
was created; the last-decision counterfactual gauge retained 98,316 tokens.
The r1 failure and original runner are preserved rather than edited or waived.

The no-rebuild r2 verifier pinned the exact image ID and corrected that metric
ownership assertion. It reran the production-image contract twice, extracted
the same binary, and repeated the complete default smoke with no predictive
override. Startup inference calls remained zero; mode was enforce; observer
cadence was 500 ms; capability was automatic/metadata with Prefill bounds
65,536/262,144/524,288/262,144; the retired safe-rate metric and calibration
log vocabulary were absent.

With the fixture reporting one Decode user, a request estimated at 98,316
Prefill tokens returned pre-forward 429 in 1.208 ms. It created no reservation,
did not reach upstream, exported reason decode_interference and pressure source
decode, and kept Router green. A fitting request immediately after it returned
200 and reached upstream. The 3.5 MiB hard-KV request then returned pre-forward
429 in 19.598 ms, activated load-scoped Router protection, and did not reach
upstream. Router returned to green on poll 15 of a 100-ms loop, a 1.5-second
polling upper bound. Health was 200, unauthenticated metrics 401, authenticated
metrics 200, the smoke used the GPU device request, and fatal/OOM scans were
clean.

Authoritative acceptance evidence is under
/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-image-r2-verification-8dec49f.
All 43 evidence rows pass sha256sum -c. Material SHA-256 values are:

    r2 runner                       85e07314650337c0b47632e1199f28fb259c79561399b6c8c34dd90cf2a117a6
    evidence-sha256                 a95e42d6d5e1e6fccf2324b68d957bda0eed62ad07ed7a25c314203011986f95
    evidence-sha256-check           81ea590827491aea6b907074fc4b4cc168bd44b899d7b1565af38382ebe95b62
    summary                         4e8affca1309dab9aa7820c63677323de676a70e0ca4ecd2e831772892f06f8e
    production contract, each       d1f30f6e625214b8a9d849bd79c619e8a51cbc8d318cc4cfa4de302c8024a4c2

Both smoke containers and fixture processes were removed. The existing PIG
remained f71a089b16fc2ef030d94fbd2f930eb13af0ba3599f0a256b2f70c759d9215de
and vLLM remained
d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552;
both retained their StartedAt values, restart count zero, and OOMKilled=false.
This closes only the local-image gate. The image remains unpublished and
undeployed. The next step may replace only PIG on the dedicated CVM while
preserving exact vLLM identity, then must complete targeted GPU and ordered
Pareto validation before any registry upload.

## 41. v0.12.6 dedicated runtime deployment acceptance

The first PIG-only deployment runner correctly created exact candidate and
rollback Compose copies, changed only `pig.image`, started the accepted v0.12.6
image, and preserved the vLLM identity and inference counters. It then failed
its own authentication assertion because it queried transparent upstream path
`/metrics` instead of protected PIG path `/pig/metrics`. The 200 response and
Python metrics body were therefore expected vLLM proxy behavior, not an
authentication regression. The runner automatically restored v0.12.5 and did
not name or restart vLLM. This r1 attempt is invalid as deployment acceptance;
its directory and runner are preserved unchanged at
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-runtime-r1-8dec49f`
and runner SHA-256
`91075fc8ada5bdf2b57ac5dd4037cda5474321580705cd2b1347241000d80a6d`.

The corrected r2 runner started from the exact r1 rollback Compose, generated a
fresh evidence directory, and again proved that its only Compose delta was the
PIG image. It used the fixed project name and working directory and ran
`up -d --no-deps --force-recreate --pull never pig`; vLLM was never an
operation target. It replaced PIG with exact local image ID
`sha256:7d8c34f580b5b4d3358b5b89b0a4b99ab1a196fd1fd7c948bba734730a729f3c`.
The resulting PIG container is
`b30bc5316755dec0dbd8847ffa23633dc75739fe87da3ef1cc0c3844b71087ab`,
StartedAt `2026-08-09T16:35:46.387891422Z`, restart count zero, and non-OOM.

The exact vLLM container remained
`d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552`
on image ID
`sha256:f90fe278def6819e682889f6b7dd41a4ba9a1faa0e65c1bddf602fea9754a5c2`,
StartedAt `2026-08-09T10:03:59.648050825Z`, restart count zero, and non-OOM.
The GPU process remained PID 65953 `VLLM::EngineCore`. Prompt-token,
generation-token, and completion counters were byte-identical before PIG
replacement and after startup, proving zero startup inference calls.

Runtime contract checks passed with no predictive override:

- health became ready on poll two of the one-second loop;
- local `/pig/metrics` and combined `/v1/metrics` returned 401 without the
  token and 200 with it, and the combined endpoint contained both PIG and vLLM
  metrics;
- mode was `enforce`, observer cadence was 500 ms, model discovery returned
  200, the authenticated one-token chat returned 200, and Router status was 0;
- the immutable automatic/metadata profile retained KV capacity 862,437,
  block size 64, hard limit 758,912, and Prefill bounds
  32,768/131,072/262,144/131,072; and
- startup logs and metrics contained no retired safe-rate or active-calibration
  state, while PIG/vLLM fatal and OOM scans were empty.

The accepted r2 evidence is under
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-runtime-r2-8dec49f`.
All 50 manifest rows pass `sha256sum -c`. Material SHA-256 values are:

    r2 runner        6c5f6f192fe7ce7e623f3b44c43f657b8035c7be9fef0a5c99dcf411ad1355f7
    SHA256SUMS       8590fb843ba9097df8fe31fca0f31c178ac774c17e48ed08ab4772f67352b810
    summary          65ccf2aceb462883c024668d8342fe7fb3f6bbce7ad7ba745daa462b7c29e53f

The image still has no registry digest and was not uploaded. Production Router
and `use1-cb` remain untouched. This closes only dedicated runtime deployment
and readiness. Weighted/exclusive/quiescent, burst, terminal, low-flow and
no-demand, stale/recovery, near-KV, repeated four-Decode-user QoS, and ordered
Pareto gates remain open.

## 42. v0.12.6 targeted GPU acceptance

All targeted tests in this section ran only on dedicated CVM
`c21b7281-2c25-4453-8a68-f39ec42d03b4` against the exact unpublished image from
section 40. PIG remained container
`b30bc5316755dec0dbd8847ffa23633dc75739fe87da3ef1cc0c3844b71087ab`
on image ID
`sha256:7d8c34f580b5b4d3358b5b89b0a4b99ab1a196fd1fd7c948bba734730a729f3c`.
vLLM remained container
`d45de8d3e572acb66e72469906f4a495238758cea4204d0a873b3ab51744c552`
on image ID
`sha256:f90fe278def6819e682889f6b7dd41a4ba9a1faa0e65c1bddf602fea9754a5c2`.
Both retained their section 41 StartedAt values, restart count zero, running
state, and `OOMKilled=false`; GPU process PID 65953 remained
`VLLM::EngineCore`.

The first targeted group is
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-targeted-phase1-r3-8dec49f`.
All six raw JSON results report `overall=true`: twenty-request low flow without
false locking, same-observation burst accounting, upstream 404 terminal release,
weighted zero-Decode progress, exclusive Prefill cancellation, and weighted
plus quiescent Decode-envelope rejection. The weighted attempt was estimated at
70,297 Prefill tokens and rejected in 1.502 ms; the quiescent attempt was
estimated at 271,665 and rejected in 3.880 ms. Both were pre-forward,
request-scoped, left Router green, allowed the fitting 4K request, and ended
without preemption or reservation. All 34 manifest rows pass `sha256sum -c`;
the `SHA256SUMS` SHA-256 is
`257dc43c7b73183946e6d36dde1a300e25a67f5579371d81b1cf5a783414681e`.
The earlier r1 and r2 control directories remain unchanged as invalid-runner
evidence.

The stale/recovery group is
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-targeted-stale-r2-8dec49f`.
After the test paused only the vLLM container, Router became
availability-protected after 11 polls at 100 ms. The stale request returned
pre-forward 429 in 0.642 ms and did not change the vLLM prompt-token counter.
After unpause, Router returned to green after 15 polls at 100 ms; the recovery
request returned 200 in 34.065 ms. PIG and vLLM identities were unchanged,
vLLM was not left paused, and final preemption and reservation counts were zero.
All 39 manifest rows pass;
the `SHA256SUMS` SHA-256 is
`aa96e7c3590392e958c93c5d1155a86892493f33033c0ce4618fad9255d8a38b`.
The r1 expectation failure remains preserved and is not acceptance evidence.

The near-KV group is
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-targeted-nearkv-r1-8dec49f`.
Three concurrent 245K-word requests produced exactly one 200 and two
pre-forward 429 responses in 18.492 and 10.342 ms. The prompt-token delta was
245,052, proving only one request reached vLLM. Peak KV was 25.148 percent,
peak running was one, waiting remained zero, and there was no preemption,
reservation leak, restart, or Router residue. All 24 manifest rows pass; the
`SHA256SUMS` SHA-256 is
`47593ecadcf1666a13f37af02ded131dee00e8618ea90a7817b3487700a3a0e5`.

The final repeated Decode-QoS group is
`/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-targeted-decode-qos-r1-8dec49f`.
Before execution, the evidence path did not exist; runner and harness SHA-256
values were respectively
`905e055deada2d9432827445540ddb4bbec296317156d04fd8d19d737905deb7`
and
`915f48a3e95d81d94d88c9a75ab868f6ab468daccbefe06547cbba39de979add`.
The runner used boolean `overall=1` in its summary, while each raw JSON reports
`overall=true` and the independent process `exit-code.txt` is zero.

Each repetition held four successful streaming Decode users active, admitted a
4K fitting request, and proved all four users emitted tokens during that request.
It then froze a per-user floor at 85 percent of a stable protected baseline and
attempted 8K, 16K, 32K, and 49K requests in separate two-second windows:

```text
run  initial median TPS  protected median TPS  4K wall ms  reject wall ms, 8K/16K/32K/49K  minimum window TPS
r1               42.925                42.319      380.491  0.978/1.089/1.379/1.715                    41.981
r2               43.798                42.986      384.630  0.986/0.977/1.160/1.544                    42.980
r3               43.674                42.986      377.266  0.918/0.969/1.275/1.443                    42.980
```

All twelve oversized attempts returned 429 below 100 ms with estimated Prefill
tokens 12,121/23,449/48,409/70,297. Every attempt reported
`decode_interference`, `pressure_source=decode`, and request scope; each exact
before/after prompt counter was equal, so none reached vLLM. Every two-second
per-user Decode window remained above its repetition's frozen floor. PIG
enforced-reject counters increased by exactly twelve, while Router activation
count remained 14 and final Router active/applied/inspect-capacity values were
zero. A fitting request after the attempts returned 200.

Independent post-run inspection confirmed zero PIG reservations, zero vLLM
running/waiting/KV gauges, zero preemptions, identical PIG/vLLM/GPU identities,
and empty fatal/error scans. The workbench had only its original `bridge`
network both before and after; the current runtime network contains only PIG and
vLLM. All 31 manifest rows pass `sha256sum -c`; the `SHA256SUMS` SHA-256 is
`0ffc4ae527f33dc8be78bd38b13df9ae5da40863cd675ecad5b49bcab07c7e4b`.

These results close the targeted GPU gate, including low-flow non-locking,
request lifecycle, stale recovery, hard capacity, and repeated Decode QoS. They
do not establish Pareto safety: the section 39 adverse large-only simulation
counterexample remains binding. The next executable gate is the ordered
no-enforcement/v0.12.2/v0.12.6 GPU matrix from sections 4 and 38. The image
remains unpublished, production Router and `use1-cb` remain untouched, and no
production traffic has been sent.

## 43. v0.12.6 ordered Pareto rejection and v0.12.7 phase correction

The ordered matrix ran only on dedicated CVM
`c21b7281-2c25-4453-8a68-f39ec42d03b4` with unchanged vLLM and GPU identities.
The original run completed `N1,N2,N3,B1,A1,A2,B2,B3`; its A3 control assertion
failed after the old v0.12.2 policy was ready but before any measured A3 request.
The trap restored v0.12.6. The continuation verified the complete original
manifest, copied the eight raw files by hash, recorded the unmeasured switch,
ran A3 with the same warmup and ten scenarios, restored v0.12.6, disconnected
the workbench from the runtime network, and analyzed all nine repetitions.

The immutable evidence directories and manifest identities are:

```text
original eight repetitions plus failed A3 control
  /var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-pareto-r1-f2c97aa
  SHA256SUMS SHA-256 fd6740a2f10b8b1f0ed7fbb95393036eb172b30f5b6bfb7e041d5e0c713f6f88

A3 continuation plus complete nine-run analysis
  /var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-pareto-a3-continuation-r2-f2c97aa
  SHA256SUMS SHA-256 f7c42b7f4537e6b4bd96f188203f86ff7851b4e65038777eb7b627a40d263ffa

independent raw-JSON recomputation
  /var/volatile/dstack/persistent/pig-v0124/runs/pig-v0126-pareto-independent-audit-r4-f2c97aa
  SHA256SUMS SHA-256 943b880b0d2de36e8daaad0a59f4b1df06ca7d7bac74a609b2fcc9b5e16d54a1
```

Audit r1 and r2 stopped on audit-fixture contract assertions and remain
preserved as invalid audit-run evidence. Audit r3 deliberately used linear p10
interpolation and still returned the same red conclusion. Audit r4 used the
matrix's declared nearest-rank p10 definition, independently recomputed request
TPS and wall time from raw records, and matched the original analyzer with zero
QoS-floor and aggregate-median delta. Both valid statistical conventions reject
v0.12.6.

All nine repetitions and all ninety scenarios passed evidence-health and safety
checks. There was no preemption, restart, OOM, backend error, reservation leak,
undrained terminal state, or nonzero final Router projection. Every scenario's
cached-prompt-token delta was zero, so cache reuse did not cause the policy
difference. Median metrics were:

```text
metric                                      v0.12.2 A       v0.12.6 B       B/A
short-only SLO-goodput                       1177.993          901.767     0.7655
mixed SLO-goodput                            1368.807          970.607     0.7091
reversed-order SLO-goodput                      0.017624         0.017645  1.0012
long-only SLO-goodput                           0.017609         0.017635  1.0014
all-scenario aggregate SLO-goodput            177.950           81.982     0.4607
QoS-violation Decode seconds                    0.408            3.499
preemptions                                      0                0
```

The B short-request success TPS quantiles remained above the frozen floor, but
that does not rescue rejected work. Every B short-only repetition admitted only
five of eight requests and rejected three. Every B sustained-regular repetition
admitted only five of twenty-four and rejected nineteen. Every B mixed
repetition admitted five short requests, rejected three short requests, and
correctly rejected the interfering long request. A admitted all corresponding
short requests. B's median total prompt-token delta was therefore only 662,138
versus A's 689,309; this is rejection, not cache efficiency. The checks for
short SLO-goodput, required scenarios, and material gain all fail.

The extra unmeasured switch before A3 and the old baseline's startup calibration
are real baseline confounders, but they cannot explain the candidate failure.
B1, B2, and B3 each produced the same five-success short and sustained outcome
before A3 existed, and B also loses against A1/A2 alone. The red result is a
product-policy failure, not an analyzer-definition or continuation-order bug.

The causal B1 decision was recorded while backend `running=0`, `waiting=0`:

```text
selection_input_tokens=1298
pending_prefill_sequences=5
pending_prefill_tokens=6490
post_admit_pending_prefill_tokens=7788
effective_sequences=5
regular Decode-interference budget=32768
pressure=(7788 * 5) / 32768=1.188354
reason=decode_interference
```

`Manager.requestAwareStateLocked` adds `DecodeSequencesUpper=1` for every local
reservation before forwarding, while also retaining that reservation as pending
Prefill work. `DecideRequestAware` then derives `EffectiveSequences` from this
virtual total. The Decode envelope therefore treats five Prefill-incomplete
requests as five active Decode users even though no request is running upstream.
This double phase ownership caused the sixth and later small requests to be
rejected.

The v0.12.7 correction is intentionally narrow and keeps SOLID ownership:

1. Manager produces one internal request-aware state summary during its existing
   reservation scan. It continues to own lifecycle and assimilation state.
2. Pending-Prefill tokens and classes include every Prefill-incomplete
   reservation exactly as before.
3. The Decode envelope's active sequence count starts from the fresh
   backend-observed `running` count, not virtual future Decode demand.
4. A Prefill-complete local reservation that is not definitely absorbed by the
   observation adds its Decode sequence upper bound. An absorbed reservation is
   already represented by observed running and is not added again.
5. A Prefill-incomplete reservation never adds an active Decode sequence. It
   still charges KV, future KV, Prefill concurrency, and aggregate Prefill
   budget atomically.
6. Ambiguous assimilation uses the conservative upper and counts the completed
   local Decode. Saturating arithmetic preserves overflow safety.
7. `DecodeEnvelope` remains a pure policy component and receives only the
   corrected scalar. No public parameter, calibration, learning, cooldown,
   Router-wide rejection, or request mutation is added.
8. The Manager scan remains O(live reservations), allocation-free, and under the
   existing decision lock; no second scan or unbounded state is introduced.

Before implementation, focused red tests must prove against v0.12.6 that:

- an idle same-snapshot regular burst does not create active Decode users from
  its own Prefill-incomplete reservations;
- pending Prefill tokens still sum atomically when real Decode users exist;
- a Prefill-complete unobserved local reservation protects its Decode user;
- an absorbed local Decode is not double-counted with observed running;
- weighted/exclusive/quiescent Prefill concurrency, hard KV, cancellation,
  terminal release, stale state, and same-request duplication are unchanged;
- corrected decision telemetry exposes the phase-correct
  `effective_sequences`; and
- the release version and all user-visible surfaces consistently report
  `0.12.7`.

After focused green, run formatting, affected packages, full Go tests, race,
static analysis, deterministic simulations, benchmarks, source-surface scans,
and the three review passes on the dedicated CVM. Build one new local image only
from the exact clean pushed source. Re-run targeted short-only, mixed,
sustained-regular, Decode-QoS, near-KV, low-flow, cancellation, and stale/recovery
GPU gates before executing a completely new nine-repetition ordered matrix. Do
not reuse the prior N/A measurements for promotion. Upload only the exact image
whose new matrix passes every section 4 condition. Until then, v0.12.6 and
v0.12.7 remain unpublished and production remains unchanged.

The phase-correct test-first cycle ran only in `pig-v0124-workbench`. Red r1
stopped before tests because the login-shell PATH omitted the installed Go tool
directory; it is retained as invalid runner evidence. Red r2 used absolute Go
tool paths and exited 1 for all three intended reasons: the idle 1,298-token
burst invented active Decode users, the third 4K Prefill behind four real Decode
users saw six rather than four effective sequences, and a second idle 99K
Prefill was rejected by the Decode envelope instead of reaching the aggregate
Prefill gate. Its evidence is
`/workspace/runs/pig-v0127-phase-red-r2-d2950ba`; the `SHA256SUMS` SHA-256 is
`e670bf31f0422cdcffb463554021e5b16a06e64c12f2b681e8aa3427960bad14`.

The focused implementation replaced the Manager helper's positional return
tuple with one internal state summary. The existing locked reservation scan now
separately reports virtual resource state, pending Prefill ownership, and
Prefill-complete local Decode sequences not definitely absorbed by a sample.
`EffectiveSequences` is the saturating sum of fresh observed `running` and only
that unobserved/ambiguous completed-Decode upper. No pure gate, public config,
HTTP mapping, metric schema, or lifecycle transition changed.

Focused r1 passed the three corrected causal tests; its `SHA256SUMS` SHA-256 is
`71256257a9a11aa737ba334bcff979d54e7338a0bce59e9c6b19819165d6022d`.
The complete runtime-predictive package then passed; its manifest SHA-256 is
`d5d728c66a8481e8a37090207b59d2e2b8e593b5bfc896ff7ade412292a590e8`.
Additional regression coverage proves an unobserved Prefill-complete Decode is
counted, an absorbed Decode is not double-counted with observed running, and an
ambiguous sample retains the conservative upper. Runtime, server, metrics,
simulation, and simulation-command affected packages all passed in 6.1 seconds;
that evidence manifest SHA-256 is
`dd398d25c108275aedcbd05b0a8114aa7d726e4708681d229a4d6baacd04c632`.
These are focused source results only. Commit/push, full/race/vet/build/
benchmark matrices, exact source archive, image, GPU, and Pareto gates remain
open.

## 44. v0.12.7 complete source acceptance and three review passes

The executable correction was committed as
`188e3b8de775460318e981b3bee8c84dcaf331a7` and the simulation report identity
follow-up as `396fc049ac4936c83aacf8ce321ad0f1bed32797`. Both were pushed to
`origin/codex/pig-v0.11.0-request-aware`. At source acceptance, the authoritative
workbench repository was clean and both `HEAD` and its upstream resolved to
`396fc049ac4936c83aacf8ce321ad0f1bed32797`.

The exact 162-file candidate input used by the matrix was reconstructed from:

```text
/workspace/incoming/pig-v0127-396fc04-source.tar.gz
SHA-256 d3392e6ad932f1f61f859504420cf8edb50829c19057d13d2eb82d6ee2d8d627
```

The complete matrix ran only inside `pig-v0124-workbench` on dedicated CVM
`c21b7281-2c25-4453-8a68-f39ec42d03b4`, using Go `1.24.13`, Linux
`6.9.0-dstack`, and container `2c14ed1bca84`. The runner SHA-256 was
`7ec57ca24d5b3d6c2537aaf106aa640a8d13fb3bcffb46398426b0c28c577c4f`.
The immutable evidence is:

```text
/workspace/evidence/pig-v0127-dedicated-phase-c-r1-396fc04
evidence-sha256 SHA-256
  2a74cb1f99d0251f9c151b4412bedec4fd0d9c6d759cd760524aa99173ac85ca
input-sha256 SHA-256
  7c7081f9916ffd8a6674d69da6ed60a0eda454ef07fa8ebc9d9525584f5a5343
```

An independent `sha256sum -c evidence-sha256` passed for every recorded file,
including the environment, exact inputs, status table, source inventory,
production binary, both simulations, every test/race/build log, and every
benchmark. All 28 matrix steps exited zero: environment, formatting, retired
mode and no-active-calibration audits, Decode-envelope and phase-correct
contracts, version identity, lexical corpus, affected and full tests, vet,
targeted and full race, all-package build, production binary, policy-order
tests, two simulations plus byte comparison and acceptance, B/C/C/B ordered
benchmarks, benchmark contracts, reservation-aware HTTP benchmarks, and
estimator benchmarks.

The two deterministic simulation reports were byte-identical and the suite was
policy-order independent. Against v0.12.2, aggregate v0.12.7 simulation metrics
were:

```text
metric                              v0.12.2       v0.12.7
SLO-compliant output tokens/s         84.9444       105.4187
raw completed output tokens/s         94.1203       106.4062
TPS-floor violation seconds           20.7            4.8
preemptions                            1              1
maximum idle with demand seconds       0.4            0.4
hard-fit idle rejects                  1              1
```

This is deterministic-model evidence only. In particular, individual simulated
large-only and low-flow-first-large outcomes remain conservative and cannot be
used to claim GPU Pareto safety. The section 4 controlled GPU gate remains
authoritative.

Review pass 1, model and causality: the HTTP adapter still supplies a fresh
backend observation to the Manager before every decision. The Manager now
computes the Decode-envelope scalar as observed `running` plus only
Prefill-complete local Decode reservations not definitely absorbed by that
observation. Focused causal tests prove that eight same-snapshot 1,298-token
Prefills create zero active Decode users, four real observed Decode users remain
four while pending Prefill accumulates, an unobserved completed Prefill adds one
Decode user, an absorbed one is not double-counted, and an ambiguous one retains
the conservative upper. Request size, post-admit pending Prefill, resource fit,
and the immutable profile still change the pre-forward decision. No tokenizer
exactness, cache hit, learned rate, or synthetic Prefill claim is introduced.

Review pass 2, lifecycle, safety, and SOLID: Manager remains the only owner of
the locked decision/reservation/reconciliation transaction and produces one
internal state summary during its existing O(live reservations) scan.
Prefill-incomplete reservations continue to charge physical and active KV,
future KV, pending Prefill count/tokens, class concurrency, and aggregate
Prefill budget. Completion, absorption, ambiguity, terminal release,
cancellation, rollback, duplicate IDs, stale observations, epoch rebase,
saturating arithmetic, and concurrent lifecycle paths retain their existing
owners and passed focused plus full race coverage. `DecodeEnvelope` remains a
pure policy; no public configuration, learner, calibration, probe, cooldown,
request mutation, cache inspection, routing, tiering, or priority state was
added.

Review pass 3, efficiency, evidence, and release: the ordered benchmark contract
passed in both directions. Median pre-forward HTTP cost was `13,447 ns` versus
v0.12.2's `12,750 ns` (`1.0547x`) with the same 33 allocations. Manager decision
cost was `200.65 ns` at zero reservations, `3,385.5 ns` at 48, and `17,418.5 ns`
at 256, with zero allocations; the 48- and 256-reservation ratios were `1.0216x`
and `1.0044x`. The pure policy reached at most `109.15 ns` and remained
allocation-free. Telemetry at 256 reservations was `1.0101x` baseline with zero
allocations. Estimation medians were `0.283 us` for 1 KiB, `1.980 us` for 64 KiB,
`28.688 us` for 1 MiB, `157.583 us` for 4 MiB, and `21.872 ms` for the adversarial
4 MiB many-string shape, all with zero allocations and below the accepted
100-ms extreme-input ceiling.

The source layer is accepted. No image has been built or uploaded from
`396fc04`; no PIG runtime has been replaced with v0.12.7; vLLM, the CVM,
production Router, and `use1-cb` remain unchanged. The next gate is one local
immutable image built from the exact clean pushed source, followed by image
contract checks and PIG-only replacement on the dedicated CVM. Only after the
targeted GPU suite passes may a completely new nine-repetition ordered Pareto
matrix begin.

## 45. v0.12.7 immutable local image acceptance

The section 44 documentation-only source-acceptance update was committed and
pushed as `92e20b2e8211b179eac578fda2442b6ecfb4f0ea`. Before construction, the
image runner proved that `HEAD` and its upstream both resolved to that clean
commit and that its only delta from matrix-tested executable commit `396fc04`
was this plan. It created one detached source tree, archived all 162 tracked
files, and built exactly one local image without pull or push. The accepted
identity is:

```text
tag
  ghcr.io/phala-network/phala-inference-guard:0.12.7-92e20b2-local
image ID
  sha256:5fc2613c11748c62059b849c56c042456b26c561fade9a8f289af925283abd7e
binary SHA-256
  c4be7f5a9c9133845acc532970245b29dad3801107ecec5304aa0e53b0924ea7
source archive SHA-256
  fb317b119beaf99bd6e74890de1daa5a14008fa2bc2a8e4799cf0febf340f921
platform and OCI
  linux/amd64, version=0.12.7,
  revision=92e20b2e8211b179eac578fda2442b6ecfb4f0ea
registry
  RepoDigests=[], isolated auth absent, upload not attempted
```

The image runner SHA-256 is
`e547d27771515fae217395b7a4bc9eb3d5aab098045f0dca9a56796c05ade0cc`.
Its immutable evidence is:

```text
/var/volatile/dstack/persistent/pig-v0124/runs/pig-v0127-image-r1-92e20b2
evidence-sha256 SHA-256
  daffe500c141e2ceec3b8afd0152804c3b007b2838e6619d39989d186f33bf59
independent-verification/sha256sums SHA-256
  00a5157930592ff2b8e1d966922491cc7e788187e15f82ded93c152f54bd92eb
```

Independent `sha256sum -c` verification passed all 60 primary files and all
four independent-verification files. The production-image contract passed
twice against the same image ID and extracted binary hash. The image is root
distroless with `/phala-inference-guard` entrypoint, native CGO/NVML support,
and all-GPU visibility.

Default-config smoke used no predictive algorithm overrides. It proved
`enforce`, the 500-ms observer, automatic metadata initialization, derived
`65536/262144/524288/262144` Prefill bounds, zero startup inference calls, and
absence of the retired safe-rate metric and calibration vocabulary. Health was
200; unauthenticated metrics were 401; authenticated metrics and transparent
chat were 200.

A 384-KiB request was rejected pre-forward by request-scoped Decode protection
in `1.258 ms`; it did not reach the upstream, did not activate Router
backpressure, exposed the same reason in HTTP, decision metrics, last-reject
metrics, and logs, and a subsequent fitting request returned 200. A 3.5-MiB
request was rejected pre-forward by hard KV in `19.344 ms`, activated the
load-scoped Router projection, and recovered green within 15 100-ms polls, an
observed upper bound of 1.5 seconds.

The smoke container used host networking only for its isolated fixture. The
workbench remained connected only to `bridge`; the existing runtime network
retained exactly the running PIG and vLLM. Their IDs, images, StartedAt values,
restart counts, and OOM states were byte-identical before and after. The image
is accepted locally but remains unpublished and has not replaced the running
PIG. The next step is PIG-only replacement on the dedicated CVM, followed by
runtime identity/authentication/observability checks and the complete targeted
GPU suite. vLLM and the CVM must not be restarted.
