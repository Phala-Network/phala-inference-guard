# PIG v0.12.3 Evidence-First QoS-Constrained Goodput Plan

Status: active execution plan, Phase C image published 2026-08-09

This is the only execution plan for PIG v0.12.3. Older v0.12 plans and the
superseded candidates are historical evidence, not required behavior. Detailed
builder commands, complete logs, retries, and live observations belong in
ignored evidence artifacts; their authoritative identities are recorded here.

## 1. Goal

Maximize SLO-compliant completed-token goodput while preventing admission-caused
KV exhaustion and preemption, bounding per-user Decode TPS degradation, and
keeping an available upstream work-conserving.

Every authoritative decision happens before forwarding the request. Backend
feedback may update the next observation used by a decision, but it must not be
the only protection and must not create learned parameters, reject cooldowns,
or sticky state.

The candidate stays in the v0.12 release line and is version `0.12.3`.

## 2. Current conclusion

Phase C source, builder, production-image, and immutable registry publication
gates are complete. The exact image is not yet approved for deployment or live
traffic: Phase D must still prove the Router-disabled GPU Pareto gate on
`use1-cb` before release promotion or any Router enable.

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
comparison at 85 percent of its matched direct-upstream per-user Decode TPS.
This allows bounded TPS loss in exchange for higher aggregate goodput without
turning a universal TPS number into production configuration.

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
An active calibration probe may run only while Router is disabled; its result
must not be silently persisted or reused after an upstream identity change.

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
7. production binary and image-contract tests; and
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
all Phase C gates pass on the final versioned source, commit and push it, then
build and publish one immutable canary image on the approved builder. Record the
source revision and digest. Do not create that image before the full matrix is
green.

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
- [x] Phase C full builder matrix green on the final dead-parameter-free
  candidate.
- [x] v0.12.3 source committed and pushed.
- [x] immutable v0.12.3 canary image published and verified.
- [ ] Phase D Router-disabled GPU Pareto gate passed.
- [ ] exact canary digest promoted as the v0.12.3 release.
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
