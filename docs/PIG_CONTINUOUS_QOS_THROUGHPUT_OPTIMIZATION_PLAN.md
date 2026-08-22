# PIG Continuous QoS and Throughput Optimization Plan

Status: active maintenance plan; baseline observation is running. No behavior,
policy, image, Compose, Router, or deployment change has been authorized or made
by this execution.

Last reviewed: 2026-08-22.

This document is the durable execution reference for observing PIG, analyzing
production evidence, optimizing performance, and correcting defects. Versioned
plans remain release evidence. They do not replace this rolling maintenance
plan.

## 1. Objective

Maximize successful completion goodput and useful GPU utilization when real
demand exists, subject to these constraints:

- the long-window, sequence-second-weighted output TPS per active Decode
  sequence remains at or above the configured business reference;
- occasional short TPS dips are acceptable, but persistent or clustered
  degradation is investigated and must not be hidden by a high prompt-token
  rate;
- preemptions, OOMs, backend restarts, proxy failures, and admission lifecycle
  failures remain absent in release acceptance and controlled in longer
  observation;
- request-specific protection remains selective, so a large request that does
  not fit does not unnecessarily close capacity for a smaller request; and
- PIG adds bounded pre-forward cost and never relies on post-response feedback
  to undo a request that should not have entered the upstream.

The primary throughput result is successful completion output tokens per
second. Raw prompt TPS, total token TPS, PIG admission count, Router attempts,
and GPU utilization are supporting evidence, not substitutes for completion
goodput.

The primary QoS result is output tokens per active Decode sequence, weighted by
active sequence-seconds over a sufficiently long window. A simple arithmetic
mean of sampled ratios is not authoritative. Periods with no qualified Decode
work are reported as low flow and are not treated as QoS success or failure.

## 2. Current Accepted Baseline

The starting point is the accepted measured chain below. Re-read all live
identities before an observation or change because this record can become
stale.

```text
PIG                    v0.12.18
source revision        80b7f0581f03fbaa8490c9245c3f55771ea0ec42
CVM                    311bbcdb-e348-4922-b37d-541755b09ff7
Router upstream        use1-19
backend                vLLM on H200
mode                   enforce
metrics poll           500 ms
TPS reference          25 output tokens/s/active Decode sequence
```

The accepted 30-minute window contained 360 complete five-second samples:

```text
mean-active TPS mean / minimum     113.95 / 59.80
completion output throughput       525.96 tokens/s
GPU utilization mean               75.72%
KV utilization mean / maximum      4.22% / 12.72%
prefix-cache hit share             60.00%
classified fit / protect           2,389 / 128
admission lifecycle failures       0
preemptions                        0
complete pre-forward mean          0.4128 ms
```

This is health and release evidence for the exact observed workload. It is not
equal-load peak-throughput evidence. The preceding v0.12.17 window received
17,991 Router attempts while the v0.12.18 window received 2,491, a 7.22-times
offered-load difference.

The live host Compose uses v0.12.18, while the Phala control-plane Compose
snapshot still names v0.12.17. Synchronizing that persistent snapshot is a
separate, explicitly authorized operation. This plan does not authorize it.

A read-only audit also found one vLLM host-memory OOM more than three hours
after an earlier formal 30-minute window. PIG, HAProxy, and ingress did not
restart; PIG closed intake after backend observations became stale and reopened
after vLLM recovered. This delayed failure is why a 30-minute acceptance window
is necessary but not sufficient.

## 3. Scope and Non-Goals

PIG owns single-upstream predictive admission, reservation, lifecycle
reconciliation, and the metrics and Router projection of those decisions.

This plan does not add the following responsibilities:

- routing among model replicas;
- request rewriting, priority injection, customer-tier classification, or
  backend scheduling;
- TTFT admission protection;
- model-specific tokenizer assets or full model-template rendering in the hot
  path;
- a duplicate `input + declared output` backend context validator;
- request-specific prefix-cache lookup inferred from aggregate hit rate; or
- learned Prefill thresholds, KV geometry, model limits, or cache capacity.

Prefill and KV capability values continue to initialize once from coherent
upstream metadata and metrics, then remain frozen for the Controller lifetime.
Observed output TPS and cache effectiveness may inform later predictions, but
feedback affects only future admissions.

The current lexical estimator is intentionally model-neutral. Improve it only
when paired request/response evidence proves material classification bias and a
bounded change improves the decision, not merely because exact tokenization is
possible. The remotely measured 4 MiB extreme-input pre-forward p99 must remain
below 100 ms; normal requests must also retain a low and stable p95/p99 relative
to the accepted baseline.

## 4. Evidence Contract

Every observation artifact records:

- UTC start and end time, sample interval, expected and received samples, and
  collector errors;
- CVM, Router upstream, backend family/version, PIG version/source/image digest,
  container IDs, start times, restart counts, and Compose hash;
- exact PIG non-default policy, including runtime policy revision and source;
- Router enabled set before, during, and after a controlled change;
- Router attempts, processed requests, upstream 429s, other failures, and any
  available completion success counters;
- PIG attempts, admits, protections by reason and scope, enforced rejects,
  reservations, lifecycle failures, Router backpressure, and stale-state time;
- backend generation tokens, successful completions, running, waiting, KV use,
  cache metrics, preemptions, and metric resets;
- GPU utilization and GPU-memory usage;
- request-size, estimated Prefill, output-length, streaming, finish-reason, and
  cache-effectiveness cohorts where the available low-cardinality evidence can
  support them; and
- client-observed output tokens and Decode duration for sampled or aggregated
  traffic when that evidence is available without logging request content; and
- bounded logs for PIG, backend, HAProxy, and ingress, including OOM and restart
  state.

Backend metric names are not interchangeable. The collector maps vLLM and
SGLang native metrics to the semantic fields above, records which source metric
was used, and fails the affected conclusion if the metric is absent, changes
type, resets unexpectedly, or has incompatible semantics. It never silently
uses a vLLM metric as an SGLang metric or the reverse.

### 4.1 Derived results

Compute at least:

```text
completion_goodput
  = successful completed output token delta / valid wall time

mean_active_tps
  = qualified completed output token delta / active Decode sequence-seconds

request_goodput
  = successful completed requests / valid wall time

protection_share
  = enforced predictive protections / predictive attempts

preemption_rate
  = preemption delta / successful completed requests

backpressure_duty_cycle
  = samples with Router backpressure active / valid samples
```

Report numerator, denominator, missing-data coverage, and counter resets beside
every derived value. Do not calculate a rate across a reset. A backend generation
counter that includes failed, aborted, or otherwise unattributed work is reported
as `raw_generation_throughput`, not successful `completion_goodput`. If no
trustworthy success linkage exists, mark completion goodput unavailable instead
of silently substituting the raw counter.

When client-visible Decode duration is available, compare its token rate with
PIG's sequence-second-weighted mean-active TPS. They answer related but different
questions: the backend measure drives the aggregate control envelope, while the
client measure validates the user-visible outcome. Disagreement is a diagnostic
finding, not a reason to choose whichever value looks better.

Cache evidence is reported next to request-size and running/waiting cohorts.
Aggregate prefix-cache hit share may explain Prefill compute, but it does not
prove that any candidate request will hit and it does not reduce its input or KV
reservation.

### 4.2 Comparable cohorts

A performance comparison is valid only when the windows are matched or
stratified by at least:

- offered Router attempts and accepted request rate;
- input-size or capability-derived Prefill class;
- prefix-cache effectiveness;
- running and waiting distributions;
- output-token distribution and finish reason;
- streaming/fanout shape where relevant; and
- backend version, model identity, GPU topology, and non-PIG configuration.

Use standard input-size views (`<64K`, `64K-256K`, `256K-512K`, and `>512K`)
when they are supported by the model context, plus the actual startup capability
boundaries exported by PIG. The standard views aid cross-run analysis; they do
not override automatically initialized capability.

If a matched cohort is unavailable, report only health and correlations. Do not
claim a throughput win, regression, or causal effect from unlike traffic.

## 5. Observation Windows

Use four horizons with different purposes:

| Horizon | Sampling | Minimum samples | Purpose |
| --- | --- | ---: | --- |
| Release acceptance | 30 minutes at 5 seconds | 360 | Immediate behavior, Router visibility, lifecycle, and QoS acceptance |
| Stability | 6 hours at 30 seconds | 720 | Traffic mix, protection quality, preemption clusters, and sustained goodput |
| Delayed failure | 24 hours at 60 seconds | 1,440 | OOM, restart, counter reset, stale recovery, and long-tail drift |
| Steady state | Rolling 6-hour and 24-hour Prometheus views | continuous | Regression detection between releases |

The 30-minute observer must be uninterrupted. A sample gap or collector parse
error is evidence loss, not a healthy zero.

For a deployment, these are nested checkpoints measured from the same Router
restore time, not three sequential waits totaling 30.5 hours. Preserve a common
identity manifest throughout; a process restart, policy update, Compose change,
or material backend change starts a new window.

Do not leave an orphan collector process on a CVM after an interactive task.
Long observation uses Prometheus/Grafana or a deliberately managed collector
with an owner, output path, end time, and cleanup procedure.

## 6. Diagnosis Model

Classify each finding before changing code or policy.

### 6.1 QoS deficit

Evidence includes a sequence-second-weighted TPS result below the configured
reference during meaningful Decode load, especially when accompanied by
growing waiting, preemptions, or a lower completion goodput cohort. An isolated
sample or a short dip is not by itself a defect. A continuous under-reference
period of ten minutes is a diagnosis trigger, while the complete 30-minute and
6-hour weighted results decide acceptance.

### 6.2 Over-protection

Evidence requires offered demand plus protection while the backend has durable
headroom: low waiting, non-saturated GPU/KV, no recent preemption, and fitting
request classes that repeatedly fail to enter. Low GPU utilization without
offered demand is not over-protection. Request-scoped rejection of a large input
while smaller inputs continue is intended behavior, not node closure.

### 6.3 Under-protection

Evidence is a TPS decline coupled with increasing waiting, KV pressure,
preemptions, OOM, or backend instability after admissions. High GPU utilization
alone is not under-protection; useful saturated work with compliant QoS is the
target state.

### 6.4 Estimator error

Evidence requires paired estimates and trustworthy response/backend outcomes,
stratified by request shape. Distinguish safe bounded overestimation from errors
that materially change an admission class. Censored outputs, client aborts,
external context, unsupported endpoints, and cache effects do not qualify as
clean tokenizer-calibration samples.

### 6.5 Implementation or observability defect

Examples are admission lifecycle failure, a reservation or pending-Prefill
gauge that does not drain, hidden enforced protection, incorrect Router scope,
counter rollback without reset handling, stale state that does not recover from
a coherent backend sample, invalid cross-backend metric mapping, proxy-body
mutation, or a protection that is absent from logs and metrics.

### 6.6 Traffic or backend cause

Changes in arrival rate, cache share, request size, output length, model/backend
version, GPU health, or upstream restart are first-class explanations. Establish
these before attributing a change to PIG.

## 7. Decision Gates

### 7.1 Immediate release stop or rollback

- PIG crash loop, panic, request-body corruption, authentication regression, or
  proxy failure attributable to the candidate;
- enforced protection not represented by PIG metrics/logs and the correct
  Router projection;
- a new admission lifecycle-failure counter delta, leaked reservation, or pending
  work that does not drain after the backend and proxy are idle;
- intake remains closed after the backend has produced a coherent fresh recovery
  sample;
- new OOM, backend restart, or repeated preemption causally linked to the
  candidate; or
- loss of the exact fallback, Compose, source, image, Router, or observation
  identity needed to interpret the run.

A single backend preemption outside release acceptance is not automatically a
rollback. Preserve the surrounding cohort and determine whether it is isolated,
repeating, or linked to an admitted request class. Any preemption during the
30-minute release window fails that window. Any backend OOM or restart during a
formal release window also invalidates the window; subsequent causality decides
whether to restore PIG, repair the backend, or repeat the observation without a
behavior change.

### 7.2 Optimization trigger

Create an optimization hypothesis when one of these repeats in a valid cohort:

- the long-window weighted TPS is below the reference;
- successful completion goodput is lower at matched demand without a QoS or
  stability benefit;
- load-scoped protection remains active while offered demand exists and the
  backend repeatedly shows durable headroom;
- waiting, preemption, or instability rises after a specific admitted size or
  Prefill class;
- estimate bias repeatedly moves requests into the wrong admission class; or
- normal or extreme-input pre-forward latency materially regresses.

Do not tune merely to reduce the number of 429s. A lower protection count can
mean either improved capacity use or unsafe admission; the post-admit backend
and completion outcome decide which.

### 7.3 Checkpoint decisions

Use these conclusions instead of one ambiguous `passed` label:

- `provisional`: the complete 30-minute window meets the weighted TPS reference
  under qualified load, has no release-stop event, has exact cross-surface
  protection accounting, and shows no matched-cohort goodput regression;
- `stable`: the six-hour checkpoint preserves those properties and shows no
  repeating preemption, stale-state, reservation, or traffic-class failure;
- `final-observed`: the 24-hour checkpoint adds no OOM, restart, unexplained
  preemption cluster, counter/reset defect, or delayed capacity lock;
- `reverted`: a release-stop condition restored the verified fallback; or
- `inconclusive`: evidence coverage or comparable demand is insufficient. Low
  traffic never becomes an artificial pass or failure.

Before an experiment, state the smallest throughput difference worth acting on
and how run-to-run uncertainty will be estimated. A behavior candidate is a
performance improvement only when it satisfies the primary hypothesis and, in
a matched cohort, either improves successful completion goodput beyond that
uncertainty or preserves goodput while delivering a predeclared QoS/stability
gain. Otherwise retain the existing version.

## 8. Iteration Procedure

Every iteration follows this sequence:

1. Re-read the live Compose, PIG/admin policy, container identities, Router
   enabled set, backend readiness, and current 6-hour/24-hour health.
2. Capture a read-only baseline with the evidence contract in Section 4.
3. Classify the problem using Section 6 and write exactly one falsifiable primary
   hypothesis. Record expected metric movement and disconfirming evidence.
4. Add a failing focused test or deterministic simulation that reproduces the
   claimed defect or performance limit.
5. Make the smallest coherent implementation change. Preserve SOLID ownership:
   estimator parses bounded request evidence, adapters normalize backend
   observations, gates make pure decisions, Controller owns atomic state and
   reservations, lifecycle owns reconciliation, and reporters only expose
   snapshots.
6. Review correctness, algorithmic cost, allocation/locking behavior, lifecycle
   completion, and cross-backend semantics. Do not add an abstraction unless it
   removes a real ownership conflict or meaningful duplication.
7. Run formatting, focused and complete tests, race, vet, build, deterministic
   simulation, and relevant hot-path benchmarks on an approved remote
   workbench. Do not treat local execution as release evidence.
8. Commit and push each accepted plan or source revision. Source publication is
   not image acceptance.
9. Build the exact pushed revision remotely. Validate source/OCI identity,
   runtime defaults, health, authenticated metrics, request compatibility, and
   benchmark regression. Upload the image only after these gates pass.
10. Re-read production state. Preserve the exact live YAML, hashes, container
    identities, enabled Router set, and executable restore procedure.
11. Pull and verify both the candidate image and the pinned v0.8.13 fallback.
    Recreate the complete fallback configuration and prove readiness before
    changing the candidate.
12. Disable only the target Router upstream and wait for Router running work,
    PIG reservations, pending Prefill, backend running, and backend waiting to
    drain to zero. Do not use a fixed sleep as drain proof.
13. Recreate only PIG. Do not restart the backend, HAProxy, ingress, or the CVM
    unless an independently diagnosed fault requires it.
14. Verify authenticated readiness, runtime identity, capability agreement,
    empty lifecycle state, logs/metrics/client/Router consistency, and a normal
    streaming request before restoring exactly the original Router enabled set.
15. Run the 30-minute, 6-hour, and 24-hour observation sequence. Accept, revert,
    or open the next single-hypothesis iteration from recorded evidence.

One behavior-bearing version validates one primary hypothesis. Documentation,
dashboard, or collector-only work does not change the PIG version. The next
behavior-bearing candidate after v0.12.18 is v0.12.19; do not jump to v0.13.x
without a separately approved compatibility boundary.

The runtime policy API may change only `tps_reference`. It is process-local and
resets to the startup configuration when PIG restarts. Use it only for a bounded
experiment that records the complete before/after policy document and revision.
Do not accumulate undocumented production state through repeated API updates.

## 9. Optimization Order

Apply changes in this order so throughput gains remain attributable:

1. Correct missing, contradictory, or backend-incompatible evidence. A control
   loop cannot be optimized from a false metric.
2. Correct lifecycle or atomic reservation defects. Safety state must be exact
   before thresholds are loosened.
3. Correct estimator classification only when paired evidence shows a material
   admission error, starting with the request-size cohorts responsible for the
   largest goodput or QoS loss.
4. Correct request-aware Prefill/KV selectivity when large inputs harm Decode or
   small fitting requests are unnecessarily blocked. Preserve cold KV charging
   even when aggregate cache credit reduces Prefill compute.
5. Adjust TPS prediction only when matched long-window evidence shows persistent
   QoS deficit or systematic underfill. Do not optimize a single 500-ms sample.
6. Reduce hot-path CPU, allocation, and lock cost only after behavior is covered
   by tests and benchmarks.

TPS over-protection must be split into independent causal branches. A
`qos_budget_unobserved` decision is a non-idle marginal-debt decision after the
base and qualified current-rate limits have already been exhausted. An `idle`
decision is made earlier, before the QoS budget forecast runs, and is bounded by
the idle/warming refill limit. A multi-lease debt experiment cannot be credited
with fixing idle refill, and an idle-refill experiment cannot reuse the rolling
surplus argument without a separately valid forecast. Test only one branch in a
behavior-bearing version.

Do not combine estimator, Prefill, KV, TPS, logging, and backend-adapter changes
in one experiment. If a prerequisite defect must be fixed first, close that
iteration and establish a new baseline before testing the original hypothesis.

## 10. Required Reviews

Every behavior candidate receives three explicit reviews.

### Review 1: model and causality

- Does prediction occur before forwarding?
- Does feedback affect only later predictions?
- Is the input estimate bounded, model-neutral, and used by the actual decision?
- Are cache observations used only at the aggregate scope they can prove?
- Is a throughput conclusion supported by comparable cohorts and successful
  completion goodput?

### Review 2: safety, efficiency, and lifecycle

- Is decide-and-reserve atomic for concurrent requests?
- Are success, reject, upstream error, timeout, cancel, disconnect, stale/reset,
  and shutdown paths exact-once and leak-free?
- Do arithmetic, body size, depth, cardinality, and resource bounds remain safe?
- Can low flow, one incomplete scrape, or a metrics reset create a self-lock?
- Are normal and extreme-input latency, allocations, lock contention, and race
  results acceptable?
- Do package responsibilities remain cohesive and dependency direction follow
  the existing domain/application/adapter boundaries?

### Review 3: evidence and release

- Are source, tests, image, registry, Compose, deployment, Router restoration,
  and live observation reported as separate evidence layers?
- Are fallback and drain proofs executable and complete?
- Are vLLM/SGLang metric semantics source-attributed?
- Are the 30-minute, 6-hour, and 24-hour windows complete and comparable?
- Does the result state what remains unproven?

## 11. Iteration Record Template

Append one record per iteration; do not rewrite earlier evidence.

```text
Iteration:
Status: proposed | observing | red test | implementation | remote gates |
        image accepted | live observing | accepted | reverted
Primary hypothesis:
Baseline identity and window:
Matched cohort definition:
Expected improvement:
Disconfirming result:
Source commit:
Remote gate artifact/hash:
Image and digest:
Fallback and drain proof:
30-minute result:
6-hour result:
24-hour result:
Decision and remaining uncertainty:
```

## 12. Initial Execution Backlog

| Priority | Work | State | Completion condition |
| ---: | --- | --- | --- |
| 0 | Re-read live v0.12.18 identities and current health | Observing | Compose, policy, containers, Router set, backend, and 6h/24h evidence captured without service mutation |
| 1 | Create a reusable semantic observer for the four windows | In progress; paired layer and r4 checkpoint lifecycle gate accepted | vLLM/SGLang source mappings, reset handling, completeness checks, cohort output, hashes, and cleanup tested remotely |
| 2 | Establish a traffic-matched v0.12.18 baseline | Observing | At least one valid demand cohort with completion goodput, weighted TPS, cache, size, running/waiting, GPU/KV, protection, and stability evidence |
| 3 | Classify the first material bottleneck | Preclassified; blocked on formal 6h evidence | Select either non-idle marginal debt or idle refill from complete decision-time evidence; do not combine them |
| 4 | Execute one behavior iteration if justified | Blocked on evidence | Red test, minimal implementation, three reviews, remote gates, pushed source, accepted image, safe rollout, and 30m/6h/24h result |
| 5 | Synchronize the control-plane Compose snapshot | Separately authorized | User-approved persistent update matches accepted live Compose and retains a verified restore path |

The correct outcome after observation may be no code or parameter change. If
QoS is compliant, completion goodput tracks demand, protections are selective,
and no reproducible defect or matched-cohort underfill exists, retain v0.12.18
and continue steady-state monitoring rather than manufacturing another version.

## 13. Plan Review Record

The plan itself must pass three reviews before execution:

1. Model and causality: complete. The review removed the assumption that every
   backend generation-token delta is successful goodput, required explicit raw
   versus success-linked naming, and added client-visible TPS as a validation
   surface. Prediction remains pre-forward; cache scope, feedback direction,
   and model-neutral estimation are unchanged.
2. Safety and lifecycle: complete. The review changed cumulative lifecycle
   failures to window deltas, made the remote 4 MiB p99 boundary explicit, and
   invalidated a formal window on any backend OOM or restart before assigning
   causality. Drain, exact fallback, cancellation, stale recovery, low-flow,
   reset, and PIG-only recreation coverage remains explicit.
3. Evidence and release: complete. The review made the 30-minute, 6-hour, and
   24-hour horizons nested identity-stable checkpoints; added provisional,
   stable, final-observed, reverted, and inconclusive outcomes; and required a
   predeclared meaningful difference plus uncertainty before claiming a
   matched-cohort throughput improvement. Source, image, registry, runtime, and
   control-plane persistence remain separate evidence and authorization layers.

Completing these plan reviews changes only this document. It does not start an
observer, modify source behavior, build or publish an image, mutate Compose or
Router, restart a process, or deploy to a CVM.

## 14. Execution Ledger

### 2026-08-22 live identity and delayed-failure audit

Goal: `按持续优化计划执行`.

The first read-only execution pass established this current state at
approximately `2026-08-22T04:55Z`:

```text
CVM status                    running; no control-plane operation in progress
CVM shape                     h200.small; dstack-nvidia-dev-0.5.9
live Compose SHA-256          b5b0a6674ce1cb38105e5958126aab412993b89efa37b26e330966d2fa1c7d4e
control-plane Compose         still names PIG v0.12.17
live PIG image                0.12.18@sha256:7de28db7...f6b20
live PIG source revision      80b7f0581f03fbaa8490c9245c3f55771ea0ec42
PIG started / restarts        2026-08-21T13:40:59Z / 0
PIG policy                    revision 1, startup source, enforce, TPS reference 25
observer cadence/freshness    500 ms / 1500 ms
backend                       vLLM; max_model_len 262144; KV 1977660 tokens; block 64
Router enabled set            use1-19, use1-4c
Router protocol               request_aware_open; metrics fresh; backpressure false
admission lifecycle failures  0 in every owned phase
current reservations/waiting  0 / 0 at the idle audit sample
backend preemptions           0 since the current vLLM start
```

This pass confirmed one delayed backend failure outside the accepted 30-minute
window. Docker recorded `oom` for vLLM at `2026-08-21T18:50:33Z`, followed by
container death and automatic restart approximately ten seconds later. The
current container has `RestartCount=1` and started at
`2026-08-21T18:50:43Z`. PIG, HAProxy, and ingress did not restart. PIG exposed
availability-scoped `observation_stale` protection while the backend was
unavailable and later returned Router capacity to open. The current vLLM
Prometheus epoch reports zero preemptions.

The retained kernel journal did not preserve a corresponding OOM-killer record,
and the restarted cgroup's current `memory.events` counters are zero. Therefore
Docker proves an OOM event, but the available evidence does not yet prove the
allocation source or that PIG admission caused it. Immediately before the
event, vLLM logs showed low KV use and no waiting rather than KV saturation.
Classify this as a backend/traffic incident under investigation, not a PIG
algorithm defect.

At `2026-08-22T04:58:39.588Z`, a managed read-only baseline observer started:

```text
run id                 20260822T045839Z
container              pig-live-observer-20260822T045838Z-379598
duration / interval    86400 seconds / 30 seconds
expected samples       2880
six-hour checkpoint    2026-08-22T10:58:39.588Z
24-hour checkpoint     2026-08-23T04:58:39.588Z
output                  /var/volatile/dstack/persistent/.cache/
                        pig-live-observe-host/20260822T045839Z
```

The observer is resource-bounded to one CPU, 512 MiB memory, and 64 PIDs; it
uses the already running vLLM image only as a Python runtime, mounts the live
Compose read-only, and removes its container automatically at completion. Its
first sample reported successful PIG, vLLM, Router, GPU, and container reads
with an empty error log. It does not send inference requests or change PIG,
Router, backend, Compose, or CVM state.

Priority 0 remains `Observing` until the identity-stable six-hour and 24-hour
checkpoints are analyzed. The next parallel task is Priority 1: make the
analysis reset-aware and source-attributed so a backend restart, counter reset,
or incomplete scrape cannot become a false goodput result.

### 2026-08-22 semantic analyzer remote gate and partial baseline

Priority 1 produced a source-only operator analyzer at commit `6db02fe`, pushed
to `pig-origin/codex/pig-v0.12.18-throughput-estimator`. It changes only
`.gitignore` and `tools/observe`; it does not link into the PIG request path,
change admission behavior, build an image, publish a registry tag, change
Compose or Router, or restart a service.

The implementation review found and corrected four evidence defects before
acceptance:

1. counter deltas could bridge an incomplete scrape because analysis first
   discarded incomplete rows;
2. one abnormally short interval could make every normal interval look like a
   sampling gap;
3. a container whose Docker record was readable but whose status was not
   `running` did not invalidate the window; and
4. internal evidence integrity was mislabeled as formal checkpoint completion,
   allowing a healthy partial window to appear eligible before its horizon.

The corrected analyzer now keeps component-specific validity beside each
counter interval, excludes resets and identity transitions, independently
reports evidence integrity, and requires an explicit `release`, `stability`, or
`delayed` horizon with the plan's duration, sample-count, and cadence contract.
It never substitutes the raw backend generation counter for successful
completion goodput. `BACKEND_METRIC_SOURCES.md` records separate vLLM and SGLang
names, types, labels, and aggregation rules; the current CSV remains explicitly
vLLM-only rather than guessing or relabeling SGLang data.

The code is standard-library only. Parsing, time-series calculations, horizon
qualification, CLI/artifact hashing, and backend-source documentation remain
separate responsibilities. Analysis cost is linear in sample count times a
fixed metric set; the maximum planned 24-hour artifact is only 2,880 samples at
the current 30-second cadence. No polling or work was added to PIG.

The source and real partial-window gate ran remotely on CVM
`311bbcdb-e348-4922-b37d-541755b09ff7` inside an isolated, no-network,
read-only-root container using the already present vLLM image as the Python
runtime. Result:

```text
unit tests                         14/14 passed
test runtime                      0.039 seconds
real observer samples             53/53 complete
observed span                     1,560.001 seconds
sampling interval                 30.000 s median; 30.001 s max
evidence integrity                eligible
six-hour formal checkpoint        not eligible
formal qualification reasons      insufficient samples and observed span
critical counter resets/missing   0 / 0
analysis output SHA-256            260080f4602cc3080aa8af57b12c18ce8f34b0b04980e572dbcc7febe6ea736f
input prefix SHA-256               c813e4f3a11d10793ee9ec304940704a37a1af35de165c5256833228353d5859
```

The managed observer remained running and all service identities remained
unchanged. The partial window is health and correlation evidence only:

```text
raw generation throughput          459.44 tokens/s
proxy completed-request rate       1.397/s
controller trailing mean TPS       min 50.98; mean 97.87; p95 133.87
ready/load samples below ref 25     0%
backend waiting / preemptions       0 / 0
KV usage                            mean 3.11%; max 11.85%
GPU utilization                    mean 72.11%; p95 94.4%; max 99%
backend aggregate cache hit share  41.27%
Router backpressure duty           1.89%
enforced/known decision share      4.55%
over-protection screen             0 candidate intervals
mean total pre-forward latency      0.139 ms
```

These numbers do not justify loosening or tightening admission yet. They are
not traffic-matched against `use1-4c`, contain no success-linked output-token
counter, and have not reached the six-hour horizon. Priority 1 remains in
progress because the existing live CSV lacks histogram buckets for p95/p99,
per-reason protection deltas, success-linked completion tokens, and durable
request-shape cohorts, and an SGLang collector has been source-mapped but not
remotely exercised in this iteration. Priority 0 remains `Observing`; the next
formal analysis point is the six-hour checkpoint at
`2026-08-22T10:58:39.588Z`.

### 2026-08-22 paired target/comparator evidence gate

The next Priority 1 slice added a standard-library-only paired snapshot
analyzer under `tools/observe`. It consumes immutable start and end PIG,
backend, and Router snapshots for `use1-19` and `use1-4c`; it is operator
evidence code and is not imported by the PIG binary or request path. No PIG
policy, image, Compose, Router, backend, container, or route was changed.

The first capture at `2026-08-22T05:33:39Z` contained an empty raw
`target_pig_version` because the temporary collector recognized only legacy
`pig_version_info`. The analyzer preserves that raw manifest and separately
derives `PIG-v0.12.18` from current `pig_info`. A corrected read-only capture at
`2026-08-22T06:06:56Z` checks both names and records the current version
directly. The original start manifest and hashes were not overwritten.

Two test-first review cycles were run on the approved CVM. The initial 14-test
red gate failed only at deliberate `NotImplementedError` boundaries. The
second 20-test red gate reproduced five review defects: hierarchical metric
double counting, grouped-bucket monotonicity, inconsistent cross-engine bucket
schemas, an unverified recorded SHA list, and an unobserved PIG restart. The
accepted implementation corrects all five and also omits zero-delta label rows
from the human-facing breakdown while retaining their counts.

The three required reviews produced these constraints:

1. Model and causality: raw generation/prompt work, terminal requests, cache
   share, and latency histograms may be compared descriptively. Successful
   completion token goodput remains unavailable because the vLLM output-token
   sum is not linked to `finished_reason`. Target/comparator ratios are marked
   `descriptive_only` until demand, cache, input, and output cohorts match; no
   causal PIG improvement is inferred.
2. Safety and lifecycle: every counter requires the same complete label set;
   any rollback is a reset. Backend epochs, model labels, target PIG image and
   start identity, Compose, versions, Router config, recorded source hashes,
   and histogram schemas are checked. Router drift invalidates matched-routing
   evidence without discarding an otherwise valid PIG/backend stability
   window. Missing legacy request-aware metrics remain unavailable, not zero.
3. Evidence and release: all execution used the existing vLLM image with no
   network, a read-only root filesystem, all capabilities dropped, and no new
   inference requests. The full observer suite and real snapshot smoke passed;
   this is source/operator evidence only and does not create a PIG version or
   image release.

The accepted source and this execution record were committed as `3dae7ee` and
pushed to `pig-origin/codex/pig-v0.12.18-throughput-estimator`.

Final remote gate:

```text
full unit suite                    34/34 passed
unit runtime                       0.056 seconds
real paired wall time              1,997 seconds
runtime integrity                  eligible
matched Router identity            eligible
required fields                    18/18 available
optional fields                    0 unavailable
backend/PIG counter reset          none
target/comparator preemptions      0 / 0
analysis output bytes              85,303
analysis output SHA-256            5e0afb26f80b23b4b844b06e3722a647333286a00bd535725bca64e4e04c3692
unit log SHA-256                    c10768d073582a80035060649e8929fd6e199042bd53204ebe596d888be6c966
```

The valid 1,997-second interval is still not a traffic-matched or formal
stability result. It observed:

```text
                                  target v0.12.18   comparator v0.8.12
Router upstream attempts          3,420             2,240
raw generation work               428.49 tok/s      256.39 tok/s
raw prompt work                   2,662.22 tok/s    2,082.60 tok/s
non-error terminal requests       3,298             2,175
aggregate cache hit share         38.57%            32.00%
preemptions                       0                 0
PIG backend proxy errors          0                 0
```

Target PIG admitted 3,318 requests and recorded 103 protected or unknown
decisions: 100 `tps_reference/load`, two `prefill_budget/load`, and one
`observation_stale/availability` unknown decision. TPS subreasons included 67
`qos_budget_unobserved`, 29 `waiting`, five `idle`, and one `active_lease`.
These deltas establish visibility and a candidate signal, but the target also
received 1.53 times as many Router attempts and had a 6.57 percentage-point
higher cache-hit share. They do not justify changing `qos_budget_unobserved`
or creating v0.12.19.

At `2026-08-22T06:21:36Z`, the managed 24-hour observer remained running with
166 samples, zero collector-error bytes, unchanged Compose and service
identities, no new PIG/backend restart, and no current OOM flag. The user's
Router 404 fix is outside PIG scope. If that fix changes Router identity during
the six-hour window, the paired routing result will be split or marked
ineligible while the independently valid backend/PIG stability evidence is
retained. Priority 0 and Priority 2 remain `Observing`; no admission behavior
change is authorized by this partial interval.

### 2026-08-22 pre-checkpoint TPS budget architecture correction

A read-only source audit at commit `82cd917` corrected an overbroad initial
interpretation of the `qos_budget_unobserved` signal. The check in
`internal/admission/qos_budget.go` is not a global same-poll admission lock.
The complete order in `tps_gate.go` is:

1. project current demand from raw running/waiting, local Prefill/Decode
   liabilities, and every unobserved sequence;
2. derive the base sequence limit from rolling aggregate TPS divided by the
   configured per-sequence reference;
3. retain any larger, qualified current-rate recovery limit;
4. admit when the post-admit sequence count still fits that non-budget limit;
   and only then
5. consider spending rolling TPS surplus for one marginal sequence beyond the
   non-budget limit.

`UnobservedSequences > 0` and `QoSBudgetLeases > 0` block only Step 5. They do
not block Step 4. The current tests explicitly cover mature rolling capacity
with an unobserved sequence still admitted inside the base limit, while the
QoS-debt simulations cap the marginal surplus path at one lease, require a
covering observation before reuse, clear it on backend epoch reset, brake on
waiting/preemption/staleness, and bound idle-with-demand to one 500-ms poll.
The production forecast already uses a ten-second control horizon rather than
charging every request's complete declared output lifetime.

Therefore, the observed 67 `qos_budget_unobserved` decisions mean that a second
marginal admission beyond the computed base/current-rate capacity was denied
before the first marginal liability was absorbed by backend metrics. They do
not by themselves mean that ordinary fitting capacity was idle or that PIG
closed the node for a complete poll. The earlier hypothesis that this counter
alone demonstrated a one-request-per-poll bottleneck is withdrawn.

A future optimization of this path now requires stronger evidence: repeated
offered fitting demand at the marginal boundary, the first leased admission
being absorbed without a sustained TPS deficit or pressure, continued
backend/GPU headroom, no waiting/preemption/KV/Prefill risk, and a matched
request/cache/output cohort showing lost completion goodput from denying a
second lease. Without all of those conditions, allowing multiple surplus
leases would weaken the existing atomic debt bound rather than prove a useful
throughput improvement. No source behavior, policy, version, image, runtime,
or route was changed by this audit.

### 2026-08-22 Router collection isolation before the six-hour checkpoint

The managed observer recorded four Router-only HTTPS collection failures from
`2026-08-22T06:22:09.625Z` through `2026-08-22T06:23:39.627Z`, each
`SSL: UNEXPECTED_EOF_WHILE_READING`. Later samples returned to `router_ok=1`.
PIG, vLLM, GPU, and container collection remained successful through those
rows. The observer process stayed running, its 30-second cadence had no gap,
and the live Compose, PIG, and vLLM identities remained unchanged. This event
occurred near work on the separately owned Router 404 repair, but the evidence
does not establish causality and PIG must not compensate for it.

At `2026-08-22T06:43:39.589Z`, the partial window contained 211 samples over
6,300.001 seconds. The original all-surface gate correctly remained
ineligible: 207 samples were complete across every surface and its sole
integrity stop reason was `incomplete_samples`. PIG and backend counters had no
reset or missing critical field; no current OOM, identity transition, stopped
container, or new restart was observed. PIG accepted and completed 7,504
requests over the window with zero PIG failed or proxy-error delta, while vLLM
recorded zero preemptions. This is a runtime-health statement, not a formal
six-hour result or a throughput comparison.

A test-first, source-only observer change now adds independent
`component_integrity.runtime_service` and
`component_integrity.matched_routing` results without changing the old strict
field or its checkpoint semantics. `runtime_service` requires PIG, vLLM, GPU,
containers, runtime identity, critical counters, restart/OOM, and cadence
continuity. `matched_routing` inherits those conditions and also requires
Router scrape and counter continuity. Optional continuous
`router_config_digest` evidence is checked when supplied; the current legacy
CSV does not contain that field, so it explicitly reports
`router_identity_status=not_collected` and requires the paired snapshot
identity gate before any matched-traffic claim.

The focused remote red gate ran in the existing vLLM image with no network and
a read-only root. Seven new tests failed only because the component result did
not yet exist. The accepted focused result is:

```text
initial red EvidenceGate run       14 tests; 7 expected KeyError failures
initial red log SHA-256            d89f2086e48736508e3a3bf9f96deffa264fbfbbd3e8b2659f4b4b802f263310
review red boundary run            2 tests; 2 expected constructor failures
review red log SHA-256             11c8395ad616bbf30f6786089c455c9c98ac7a8dabd1546ce87203a1427d4bb0
green window analyzer suite        26/26 passed in 0.044 seconds
green log SHA-256                  76939ab468a29a753e264726ec4c84fea5c0a0f9c70f8f98e90e8c2ca31b3643
real partial runtime_service       eligible; 211/211 samples
real partial matched_routing       ineligible; Router samples incomplete
strict all-surface integrity       ineligible; incomplete_samples
initial real analysis SHA-256      1327804b30f0cb0bf8049dfa2d089d57f79118bfb5487d2f3b1fd786542b1c88
full observer unit suite           46/46 passed in 0.064 seconds
full unit log SHA-256              ddec75803920db0919cc2f8eeb82e46511b688560338c4319040758277decb24
full-gate real samples             227; runtime true; routing false
full-gate real SHA-256             96e5174b4860d8149d7407a63bf4211867507624b23084ec8a6407f979139143
paired regression SHA-256          5e0afb26f80b23b4b844b06e3722a647333286a00bd535725bca64e4e04c3692
```

Three review passes preserve these boundaries:

1. Model and causality: a Router scrape failure cannot become evidence of PIG
   or backend failure, but it still invalidates continuous matched-routing
   evidence. Horizon shortages remain checkpoint reasons rather than component
   integrity failures.
2. Safety and lifecycle: PIG/backend scrape loss, GPU loss, restart, OOM,
   non-running containers, identity changes, counter resets, and cadence gaps
   still invalidate runtime service evidence. Router identity or counter
   changes invalidate routing evidence without erasing a valid service window.
   The review also removed the obsolete constructor requirement for two fully
   matched samples, so an all-window Router outage returns an explicit healthy
   or failed runtime-service result instead of aborting analysis.
3. Evidence and release: this is an offline operator analyzer only. It changes
   no PIG admission path, PIG version, image, Compose, Router, backend, route,
   or running process, and it sends no inference traffic. The strict six-hour
   checkpoint remains scheduled for `2026-08-22T10:58:39.588Z`.

### 2026-08-22 formal checkpoint preparation and current QoS screen

At `2026-08-22T07:01:09.589Z`, the still-growing partial window had 246
samples over 7,350.001 seconds. All 246 PIG/backend/GPU/container samples were
runtime-service complete; 242 were complete across Router as well. No new
collector error, counter reset, restart, OOM, preemption, PIG failure, or proxy
error appeared. The exact partial analysis SHA-256 was
`f24d835081902c40b135505049d10cface24dc3f86b5c5b74a79c362e8ed756c`.

The partial QoS and utilization screen reported:

```text
TPS reference                         25
ready-under-load samples              165
trailing mean-active TPS              min 30.94; p05 59.05; mean 97.70
ready-under-load below reference      0%; longest 0 seconds
backend waiting                       p95 0; max 2; mean 0.0124
backend preemptions                    0
PIG accepted / completed delta         8,543 / 8,540
PIG failed / proxy-error delta         0 / 0
raw generation work                   333.86 tokens/s
known decisions / protections          8,919 / 379
protection share                      4.25%
over-protection screen                0 candidate intervals
GPU utilization                       mean 53.44%; p95 93%; max 100%
KV occupancy                          mean 2.36%; p95 8.62%; max 13.28%
backend aggregate cache-hit share     41.32%
mean prediction / pre-forward cost    0.018 ms / 0.153 ms
```

The simultaneous low GPU/KV samples do not prove over-protection because
offered demand is intermittent, the plan's demand-aware screen found no
candidate interval, and successful completion token goodput remains
unavailable. The correct current decision is no behavior change before the
formal horizon and paired endpoint evidence.

The Router `upstream_config_digest` at `2026-08-22T07:05:12Z` remained
`sha256:007d78ec80c8f5704bdfbc8cf9268321f75b639447999e134d166e13ebc80c6d`,
equal to the paired start capture; `use1-19` remained enabled with
`request_aware_open`. This proves only Router configuration identity. The
admin surface exposes neither Router binary version nor process-start epoch,
so no binary-identity stability claim is permitted.

To prevent the continuing 24-hour CSV from changing the six-hour result, the
formal workflow now freezes the first 721 samples after the horizon, analyzes
that immutable copy, captures fixed-boundary compressed logs, Docker events,
kernel OOM/Xid evidence, then captures and analyzes a new paired endpoint.
These scripts were uploaded only to an isolated gate directory and passed
remote `bash -n`, input-path, container-format, and classification-expression
checks at `2026-08-22T07:03:20Z`; the formal output did not yet exist and none
of the time-gated scripts was executed early.

```text
capture stability window SHA-256    eb414b2391e0f988989e69394568d9b73ead40c09bd4699db2ba49ca94e002da
analyze stability window SHA-256    9a60943a8954f6eadcc650a7d5601bce8f61960ea5e467599c68025c0c903daf
capture fixed logs SHA-256          79d464b921bab4674dd3c23c682415133c593977556efa003559fbea24f046a3
capture paired endpoint SHA-256     b81cbeae63ccc8a6d2073c8a11757ac8e380d0f2dbcda35d68b025a91c933de8
analyze paired endpoint SHA-256     362ddef460ec14a9a6dca0a2fad1458b321f0f9c21248471eee6a4f991f288aa
```

The one-time six-hour heartbeat was corrected: it must preserve the four
Router TLS EOF rows and report the strict gate honestly instead of requiring
an empty error log. After this checkpoint it must replace itself with the
one-time 24-hour delayed-checkpoint heartbeat. No PIG/Router/backend runtime or
production configuration was changed during this preparation.

### 2026-08-22 Router counter-reset correction and r4 remote gate

The Router 404/client-response correction is now owned by the Router change
submitted separately by the user. PIG must neither compensate for that Router
defect nor reinterpret it as an admission outcome. This iteration therefore
changes only the operator-side paired-evidence analyzer and its tests.

A read-only paired capture from `20260822T053339Z` to
`20260822T070815Z` covered 5,677 seconds. The Router configuration digest stayed
at
`sha256:007d78ec80c8f5704bdfbc8cf9268321f75b639447999e134d166e13ebc80c6d`,
but all exported per-route counters moved backwards:

```text
                              start      end
use1-19 processed             94,678     1,474
use1-19 upstream_attempts     95,813     1,483
use1-19 upstream_429          10,080        94
use1-4c processed             43,983     1,076
use1-4c upstream_attempts     50,962     1,140
use1-4c upstream_429           4,366         9
```

This proves a Router counter epoch reset even though configuration identity was
stable. The reset aligns with the four Router-only TLS EOF observations. The
admin surface still exposes neither a process-start epoch nor a binary version,
so this evidence does not identify the Router binary before or after the reset.

The previous analyzer correctly marked the individual deltas unavailable, but
incorrectly left `matched_routing_eligible=true`,
`comparison_eligible=true`, and `errors=[]`. The test-first correction now:

- requires both target and comparator routes to exist and remain enabled at
  both paired endpoints;
- requires `processed`, `upstream_attempts`, and `upstream_429` to be exported;
- treats rollback of any exported route counter as a reset;
- reports Router defects separately in `routing_errors`;
- makes matched-routing and comparison evidence ineligible for any Router
  route error while preserving independent PIG/backend runtime integrity.

The exact red/green evidence retained on the current CVM is:

```text
Router-reset red test SHA-256       31a5791837c946a030450cc37194d770da99ebd32784a32d1214c86fac675404
disabled-route red test SHA-256     615111ed9fd0828c4064d24804bba4ce74b99136510c40c020ed5e1113e872a4
focused paired 23/23 SHA-256        c9b15ba06dda032958c43756a45a8adeda07c7e3ceb4239449ed5ba5db82a2e5
corrected real-reset JSON SHA-256   9e3dd646f2dabe71c1dfc0ef3f30df3fc830c88eda215b70df5ca7f5be85cbe2
```

The implementation and README were committed and pushed as `0fb0234` on
`pig-origin/codex/pig-v0.12.18-throughput-estimator`. A new immutable local
archive was copied to the separate remote directory
`/var/volatile/dstack/persistent/pig-observe-tool-r4-20260822`; it did not
overwrite r3.

```text
r4 source archive SHA-256           bf8a9b7e659814a9b8b2b6b7b8765e1056e09279699704ca0c55c90521e5560e
remote full suite                    49/49 passed
full-suite output SHA-256           e676fee28f56f1821421c2859161b2ad2ced36edd856334e997e7afdcc21f33e
current real-window JSON SHA-256     8529cef264c93d917b7af5122073552dcfc9da9812da481e565f133e897a56ac
pre-reset paired JSON SHA-256        ffe6d3bfd2d50e02317f4b3d728a696a910e5921a6d485ffed8f333d57a8f1bc
reset-crossing paired JSON SHA-256   9e3dd646f2dabe71c1dfc0ef3f30df3fc830c88eda215b70df5ca7f5be85cbe2
```

The current continuous window retained `runtime_service=true` while strict
matched routing remained false because the four Router samples were incomplete.
The pre-reset paired endpoint remained valid for descriptive traffic-cohort
ratios, proving that the new rule does not reject an ordinary monotonic route
interval. The reset-crossing endpoint now has:

```text
runtime_integrity_eligible     true
matched_routing_eligible      false
comparison_eligible           false
comparison.status             ineligible
```

The required three review passes produced these conclusions:

1. Model and causality: Router counter continuity is required to describe
   traffic offered to each node. A Router reset is not a PIG runtime failure,
   and backend raw generation work is still not successful completion goodput.
   The corrected separation preserves both facts and provides no basis for an
   admission-policy change.
2. Safety and lifecycle: missing, disabled, endpoint-changed, or reset route
   evidence closes only the matched-routing comparison. PIG/backend counter,
   epoch, Compose, model, and container checks remain independent. Two endpoint
   snapshots cannot detect a disable/re-enable or process restart that returns
   to the same configuration without a visible counter rollback; formal claims
   must therefore also require the continuous Router sample series and split at
   every observed gap, identity change, or reset.
3. Evidence and release: the focused red tests, 49-test remote suite, normal
   pre-reset real window, and reset-crossing real window cover the corrected
   claim. This is an operator analyzer release only. No PIG behavior, image,
   Compose, Router configuration, route state, backend, or running process was
   changed, and no production inference request was sent.

The interim target/comparator rates collected across the reset are descriptive
backend health only and cannot support a new-versus-legacy throughput claim.
There remains no evidence-triggered reason to create `v0.12.19`; the deployed
PIG baseline stays at `v0.12.18`. Before the six-hour checkpoint, the formal
analysis scripts must be repinned from r3 to this tested r4 directory and pass a
new isolated script gate.

The initial repin passed at `2026-08-22T07:28:38Z`, but the second lifecycle
review found that its paired script still started before the known Router reset.
That would correctly return ineligible forever but would discard all stable
post-reset evidence. The first gate directory was not executed as a checkpoint
and is superseded.

The corrected gate passed at `2026-08-22T07:31:18Z`. Its scripts are isolated
under
`/var/volatile/dstack/persistent/.cache/pig-checkpoint-script-gate-0fb0234-r2`,
reference `pig-observe-tool-r4-20260822`, record analyzer source commit
`0fb023433ada8dff636274e4740eb64bbc68c85b`, and start the formal paired segment
at the complete post-reset capture `20260822T070815Z`. A later reset will still
make the new segment ineligible. Remote `bash -n`, live input,
container-identity, r4 real-output, and classification-expression gates passed
while the formal checkpoint output remained absent; no time-gated capture ran
early.

```text
capture stability window SHA-256    b148d9ebb9026a5eecf80c20748a52a0a9cedd6256f61d89477b0ccfbe49413b
analyze stability window SHA-256    5e68841c631e25e317102d7df4dded7256f2c6920fefccc0a1440fb870ff73e5
capture fixed logs SHA-256          79d464b921bab4674dd3c23c682415133c593977556efa003559fbea24f046a3
capture paired endpoint SHA-256     b81cbeae63ccc8a6d2073c8a11757ac8e380d0f2dbcda35d68b025a91c933de8
analyze paired endpoint SHA-256     4f5aa5388db5d2e326865b133f8b411cdf883ba48a13d5d5438c03982f22e38d
```

### 2026-08-22 post-reset matched segment and TPS protection attribution

An interim read-only endpoint captured at `2026-08-22T07:34:48Z` extended the
post-reset paired segment from `20260822T070815Z` to `20260822T073447Z`, or
1,592 seconds. r4 accepted every required runtime and Router field:

```text
runtime_integrity_eligible       true
matched_routing_eligible        true
comparison_eligible             true
errors / routing_errors         [] / []
target/comparator preemptions   0 / 0
analysis JSON SHA-256           d92901711d886c12f11f817c8d63b6bcaf1aa58684b7f6f530bab0873c4cf136
```

Protection projection was exact in this segment:

```text
Router target attempts                       1,771
PIG predictive decisions                    1,771
PIG risk decisions / enforced protections     188 / 188
Router target upstream_429                    188
PIG accepted / completed                    1,584 / 1,584
backend non-error terminals                 1,583
PIG proxy errors / backend preemptions          0 / 0
```

The one-request accepted/terminal boundary difference is consistent with the
two independently scraped endpoints; it is not silently treated as either an
error or a completed request. More importantly, this segment proves that an
enforced PIG protection is externally visible to Router as upstream 429. It
does not reproduce the historical hidden-protection defect.

The 188 protections separated into 187 `tps_reference/load` protections and one
`prefill_budget/request` protection. Every load-protected input estimate was at
most 4,096 tokens; the request-scoped Prefill protection was in the
`64K-256K` range. TPS decision subreasons were:

```text
admit   base_rate                   1,460
admit   idle                           95
admit   warming                        19
admit   current_rate                    7
admit   qos_budget_granted              3
protect qos_budget_unobserved          107
protect idle                            60
protect warming                         15
protect waiting                          5
```

This evidence rules out a complete low-flow self-lock: PIG continued to admit
both idle and warming requests and completed traffic throughout the segment.
It also identifies a narrower throughput question. Only three requests spent a
bounded QoS budget while 107 were protected because another request had not yet
materialized in backend metrics; another 60 were protected by the bounded idle
refill. This is the intended one-wave safety behavior, but it may be more
conservative than the user's long-average QoS contract for short requests.

A fresh partial continuous-window analysis covered 317 samples and 9,480.001
seconds. Runtime-service integrity remained true, TPS-ready under-load samples
were never below reference, waiting p95 was zero, and preemptions, PIG failures,
and proxy errors remained zero. Protection share was 5.35%, GPU utilization
mean/p95 was 47.87%/93%, KV mean/max was 1.95%/13.28%, and backend aggregate
cache hit share was 40.03%. The strict Router surface remained ineligible only
because the original four Router samples were missing. Its JSON SHA-256 was
`23e5d3d19a4ada5dbb2b60775cb0d4dd846a6b651e8cb6882b572bc7b0c043f2`.

The earlier point-gauge over-protection screen reported zero candidates, but a
counter-delta audit showed why that result must remain only a screen. Across 90
complete intervals containing 580 enforced protections, the sampled endpoint
TPS window was not simultaneously ready and at reference in any interval;
median endpoint-max GPU utilization was 77%, waiting endpoint-max p95 was zero,
and KV endpoint-max p95 was 10.15%. A 30-second endpoint cannot reconstruct the
exact 500-ms admission state or distinguish work that arrived and completed
between polls. The diagnostic JSON SHA-256 was
`64972c8a72ee2ca97f4b57b93f74e63c3651aecdc0412f4cd04553155a329e29`.
Decision-time TPS subreason counters are therefore the authoritative
attribution; endpoint gauges are supporting load context only.

The descriptive target/comparator rates were:

```text
                              target v0.12.18   comparator v0.8.12
raw generation work          103.81 tok/s       88.97 tok/s
raw prompt work              884.43 tok/s     1,335.12 tok/s
non-error terminal rate        0.9943/s          0.8499/s
cache hit share               21.92%            42.32%
```

These are still unlike cohorts: comparator cache share was 20.41 percentage
points higher and its prompt distribution was materially longer. Raw generation
is not success-linked completion goodput. The values therefore prove health and
counter continuity, not a causal v0.12.18 throughput improvement.

The evidence is strong enough to define, but not yet execute, one falsifiable
post-checkpoint hypothesis: if the completed post-reset six-hour segment still
shows `qos_budget_unobserved` and bounded-idle protection dominating short
requests while long-average TPS remains above reference with no waiting,
preemption, restart, or OOM, test a `v0.12.19` bounded multi-lease QoS debt wave.
The candidate must use declared output lifetime and current surplus to bound
additional pre-poll requests; it must not globally weaken Prefill, KV, waiting,
preemption, or request-scoped protection. Acceptance requires higher
SLO-compliant completion goodput or a stronger attributable proxy, no sustained
TPS deficit, and no new lifecycle or stability failure. Until the formal
checkpoint satisfies this trigger, `v0.12.18` remains unchanged.

### 2026-08-22 success-linked completion goodput source slice

The Router response fix remains an external Router task. PIG does not translate
or compensate for Router 404 behavior. The next PIG-only evidence gap was the
lack of a success-linked output-token counter: the response parser already
classified exact Completions and Responses API usage, but retained only outcome
and declared-versus-actual buckets. Raw backend generation work therefore could
not become the primary goodput result.

The test-first red source was committed and pushed as
`a84c0a5359d91f3fe6c02a092fee0db496d4c3a0`. Its exact source archive SHA-256
was `d4ed9d9f8c154a33c80d70014ef77d9c94634a4bb42bc44e4070e93311741cab`.
The first isolated runner attempt did not execute tests because `/tmp` was
mistakenly mounted `noexec`; its `permission denied` logs are runner-failure
evidence only. The corrected no-network r2 run failed for the intended behavior:

```text
red evidence root         /var/volatile/dstack/persistent/pig-v01218-workbench/evidence/
                          a84c0a5359d91f3fe6c02a092fee0db496d4c3a0/successful-goodput-red-r2
openai focused exit       1
server focused exit       1
openai red log SHA-256    0b75fbe7ad9a650384253fb9ebdc3447fbb7c5e021803739b9194a8ff0bbc26c
server red log SHA-256    9a24273f5583b0bf0c10b844bc90ac0df6006f5469b57ce7640905d196c14935
```

Zero completion tokens were incorrectly classified as malformed; the server
had no exact success-token counter; and an observed usage record could outrank
a later timeout, disconnect, proxy failure, or non-2xx terminal in the outcome
classification.

The minimal implementation is pushed source
`5a6ba0f12df99805c6ac0f89f95f6a0f264419ab`, exact source archive SHA-256
`fc4cd3f994037e921dbcd4d756418fab749e88d0ce41c08681380f00aa3a9ade`.
It adds the label-free monotonic
`pig_predictive_successful_completion_tokens_total`, qualifies usage through the
same proxy-success predicate used by the admission terminal lifecycle, treats
zero as valid exact usage, and censors every non-success terminal. It changes
no estimate, admission decision, reservation, Router projection, HTTP response,
configuration, or version identity.

```text
focused evidence root     /var/volatile/dstack/persistent/pig-v01218-workbench/evidence/
                          5a6ba0f12df99805c6ac0f89f95f6a0f264419ab/successful-goodput-focused
full evidence root        /var/volatile/dstack/persistent/pig-v01218-workbench/evidence/
                          5a6ba0f12df99805c6ac0f89f95f6a0f264419ab/successful-goodput-full
```

Focused Completions/Responses, streaming/non-streaming, zero-output,
`finish_reason=length`, exact-once, non-success exclusion, and response-byte
preservation tests passed with race coverage. The same pinned Go 1.24.13 image,
one CPU, 4 GiB memory, 512-pid limit, no network, read-only source, and executable
tmpfs then passed the complete source matrix:

```text
gofmt -d                        PASS; empty
go test -count=1 ./...          PASS
go test -race -count=1 ./...    PASS
go vet ./...                    PASS; empty
go build ./...                  PASS; empty
verify-no-legacy-mode.sh        PASS

full test log SHA-256           c7b1d6cfa501a7be0964848f3580e6579117e82081d115c78e0494310ec6da5c
full race log SHA-256           01927607d92f90d669b5f29c7486b5e230d9b6239c62b6da7ab5727d7b5c9d77
no-legacy log SHA-256           455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0
```

Review 1, model and causality: passed. Exact usage is accepted only after a clean
body terminal and proxy success. Aggregate usage across multiple choices remains
aggregate output goodput. `length` is a valid completion, while malformed,
missing, duplicate, partial, failed, timed-out, and disconnected evidence cannot
increase the counter. The metric observes results and has no path into the
pre-forward decision.

Review 2, safety, efficiency, and lifecycle: passed. Request completion remains
mutex-protected and exact-once; the global token addition occurs under the
existing evidence lock, adds no new lock, label, body copy, parser, or allocation,
and keeps fixed cardinality. Parser memory remains bounded at the existing JSON
and SSE limits. Full race passed. The only theoretical uint64 wrap horizon is
far beyond a process lifetime and does not justify a more complex hot path.

Review 3, evidence and release: passed for pushed source only. Current live
v0.12.18 metrics showed 66,013 `available`, 101 `unavailable`, 6 `malformed`,
and 7,545 `censored` outcomes, or 99.84% usage availability among non-censored
outcomes. Because deployed v0.12.18 does not yet success-qualify an observed
usage record, this is parser-coverage evidence, not a successful-goodput result.
It still shows that the future qualified counter should not be sparse. This live
read sent no inference traffic. No image was built or uploaded, no
Compose/Router/backend was changed, and no process or CVM was restarted. Whether
this source enters a `v0.12.19` candidate remains gated on the fixed six-hour
checkpoint; it does not itself authorize a behavior version or deployment.

### 2026-08-22 3.2-hour partial-window preflight

At `2026-08-22T08:11:09.589Z`, before the fixed six-hour horizon, the r4
analyzer was rerun against the continuing identity-stable observer. This is an
explicit partial diagnostic, not a formal checkpoint and not evidence for a
release or admission change:

```text
analysis root                   /var/volatile/dstack/persistent/.cache/
                                pig-partial-window-20260822T0811Z
analysis JSON SHA-256           cd313e432ff1864cc5321d0b7af8334fda82cc4c8218499b749ae3234a1bf8fe
observed samples                386 total / 381 all-surface complete
observed span                   11,550.001 seconds
formal checkpoint eligible      false
formal qualification reasons   incomplete_samples, insufficient_samples,
                                insufficient_observed_span
runtime-service integrity       true
matched-routing integrity       false; 5 Router-incomplete sample rows;
                                7 missing counter intervals
```

Runtime service evidence remained healthy and attributable:

```text
TPS reference                            25
ready-under-load TPS samples             215
below-reference fraction / longest       0% / 0 seconds
trailing mean-active TPS mean / p05       98.18 / 61.51
waiting p95 / max                        0 / 2
preemptions                              0
PIG failed / proxy errors                0 / 0
PIG/vLLM/HAProxy/ingress restart delta   0 / 0 / 0 / 0
OOM observed                             none
known decisions / enforced protections  12,538 / 697
protection share                         5.56%
Router backpressure duty cycle           1.84%
GPU utilization mean / p95               44.61% / 91%
KV usage mean / max                      1.67% / 13.28%
backend cache-hit share                  40.60%
prediction / pre-forward mean            0.018 / 0.149 ms
```

The window completed 11,845 PIG proxy requests at 1.026 requests/s with zero
PIG failure or proxy-error delta. Raw backend generation work was 254.78 tok/s,
but remains explicitly unavailable as successful completion goodput. Five
Router-incomplete sample rows, which produce seven missing adjacent
counter intervals, preserve the strict all-surface stop reason; they are not
deleted, interpolated, or relabeled as PIG failures. No inference request,
container/CVM restart, image action, configuration change, or Router mutation
was performed. The next decision remains the fixed six-hour checkpoint at
`2026-08-22T10:58:39.588Z`.

### 2026-08-22 final r4 checkpoint lifecycle gate

The earlier `pig-checkpoint-script-gate-0fb0234-r2` remains historical evidence
but is superseded for formal capture. A third review found two additional
evidence-lifecycle defects: the fixed log copy could include rows after the
six-hour cutoff, and the paired capture could publish `latest-end.path` before
its output became an immutable complete capture. The intermediate r3 candidate
fixed the former but retained a narrow kill window that could leave a published
pointer targeting a capture removed by failure cleanup. It was not admitted to
the heartbeat or used for a formal checkpoint.

The final candidate is isolated at:

```text
/var/volatile/dstack/persistent/.cache/pig-checkpoint-script-gate-r4
archive /var/volatile/dstack/persistent/.cache/
        pig-checkpoint-script-gate-r4.tar.gz
archive SHA-256
d0bd6e3e9cef1ed276890cd6f9d31aec682820e06b8190cc8f18297470d52a78
```

It freezes only the header and first 721 observer rows, bounds the growing
observer error stream at the exact cutoff, and bounds Docker logs/events at the
next whole-second upper limit `10:58:40Z`. That diagnostic log tail can include
at most 0.412 seconds after the sample cutoff and is excluded from all
sample-derived checkpoint metrics. The scripts enforce the time gate
independently for both formal and paired capture, validate the paired output
path under its dedicated root, and complete a paired directory before
atomically publishing the pointer. A failure always removes a temporary
pointer; it does not delete an already completed and published capture.

The exact executable script hashes are:

```text
analyze-stability-6h.sh       be263aa5f4d4cb4601a4ddcbf02ef5edd06a7347cbf58e7d4f37de3b0876e1f2
analyze-stability-paired.sh   ec0e7426154796ff14b94df48a86dede9bbb70ebc561f5a84162586d0114b921
capture-paired-end.sh         5d6239ed4421dbc7de724b5964d4d81e22190284a267eedbefa567adf18295cc
capture-stability-6h.sh       5756742ad1a8a7bb81ab0e5a5ca7de35070d4468bf0efe1777c5dcb05b53c22c
capture-stability-logs.sh     ab483bd20b0733262f3f5e3ee49d1e52654b922a13a6a43a4af89dcdc45a773e
```

The remote gate was completed before the formal horizon at approximately
`2026-08-22T08:30Z` without executing a valid checkpoint capture:

```text
superseded lifecycle red log SHA-256
da3e04abe9f253302680111f4f4f33bc1dbe3c29378b9880b726b18397dc98ed
contract runner SHA-256
3d55d437bf2ed9d81982f8ef630c187ed4c14000d8d1073fd52be6872e9da400
contract green output SHA-256
b7f1c6c340dfc786e374a19cfd839f3addd602107e229a9b1bcbdd46ab168cda
early-rejection runner SHA-256
11920450b22e8005f5fd51013e7704e9c923d4a6001e0183432dba433f081942
early-rejection SHA256SUMS SHA-256
34655585cf904d17e21ddef06da16a2c15e019d5d902e6adfc98ea44e25a58ff
failure-cleanup runner SHA-256
81f8063c0c8990f0f815a517aaa2760b34c3719af119a81441c7f6679cf465e8
failure-cleanup SHA256SUMS SHA-256
fb92415076ca627ad4a7c8b4f64cea4586c5b23dfdb49d3e25355c0b3a427fa6
```

The contract test passed against r4. Both captures returned non-zero before the
horizon (`stability=1`, `paired=1`), with no formal output, directory addition,
pointer change, or partial pointer. Under an injected Docker failure at the
cutoff, the formal and paired paths returned non-zero (`stability=1`,
`paired=97`) and again left no formal directory, partial directory, added paired
capture, or changed pointer. A final residual audit reported
`checkpoint_r4_residuals=clean`.

The first contract invocation accidentally used a Windows-to-SSH escape that
made `tr` remove literal `r` characters and therefore failed before testing any
r4 behavior. The accepted rerun used `sed 's/\r$//'`, passed the contract, and
is the hashed green evidence above. This invocation defect is not counted as an
r4 red or product failure.

Current runtime identity was rechecked during the gate: PIG is still
`0.12.18@sha256:7de28db7b46eade3440358479b30c27000f2c7d0d6acacf2fae6c20f0aaf6b20`;
PIG, vLLM, HAProxy, and ingress are running without current OOM, and restart
counts remain `0/1/0/0`, where the vLLM count is the already recorded historical
restart. No service restart, inference request, image action, Compose change,
Router mutation, or admission behavior change occurred. The formal six-hour
capture remains prohibited until `2026-08-22T10:58:39.588Z` and must use this
r4 directory.

After commit `26010f5`, the one-time `pig-6` heartbeat was repinned from the
superseded r2 directory to this r4 directory and now requires the r4 archive and
script hashes before capture. A read-only liveness check at
`2026-08-22T08:37:47Z` found the same observer run still writing samples, the
same live Compose hash, PIG `running`, `OOM=false`, `RestartCount=0`, no PIG
lifecycle event in the preceding three hours, and Router reporting fresh
`request_aware_open` PIG metrics. This liveness check is not a checkpoint or an
admission-performance result.

### 2026-08-22 delayed 24-hour checkpoint preparation

The six-hour horizon was still in the future at `2026-08-22T08:39:39Z`, so no
formal capture was executed. Instead, the already accepted r4 lifecycle was
mechanically specialized for the nested delayed checkpoint at
`2026-08-23T04:58:39.588Z`. The delayed analyzer contract is 24 hours, at least
1,440 samples, and no slower than a 90-second median interval. The running
observer collects every 30 seconds and stops before taking a sample at exactly
86,400 seconds.

The first delayed candidate, retained as
`pig-checkpoint-script-gate-delayed-r1`, assumed exactly 2,880 samples and would
have rejected an otherwise valid 24-hour window if any individual collection
iteration exceeded 30 seconds and caused the observer to skip one slot. That is
an over-strict evidence gate, not a runtime defect. It was never admitted to a
heartbeat or used for a formal capture. One initial r1 contract invocation also
looked for the wrong literal variable name; the corrected contract then passed
and exposed the sample-count design issue during the explicit derivation review.

The accepted preparation candidate is isolated at:

```text
/var/volatile/dstack/persistent/.cache/pig-checkpoint-script-gate-delayed-r2
archive SHA-256
4c11bd6619e371ff21ee6628dcf6142183917d940beac007e0005cd29368b78c
```

It waits for observer metadata `status=complete`, freezes every actual CSV row
instead of downsampling or truncating, and verifies:

- at least 1,440 actual samples;
- metadata sample count equals the immutable CSV row count;
- configured observer interval/duration remain 30/86,400 seconds;
- first sample is within one second of start;
- final sample and observed span satisfy the delayed horizon lower bound but
  remain before 86,400 seconds;
- observer start/end and current live Compose SHA-256 are identical;
- all observer error rows up to the exact cutoff are retained;
- the analyzer runs with `--horizon delayed`;
- the paired comparison still begins at the complete post-reset endpoint
  `20260822T070815Z`.

Retaining all 30-second rows is intentional even though the minimum delayed
contract is expressed at 60 seconds. Downsampling could omit an incomplete
Router scrape and falsely turn strict all-surface evidence green. Denser input
does not weaken the analyzer's minimum duration, sample-count, or maximum
cadence requirements.

The final delayed script hashes are:

```text
analyze-delayed-24h.sh          44205c08ddd3f766f9ff2e9183d27418cc31cb9c55e8bd31463c21203bfe6132
analyze-delayed-paired.sh       320fa4d7af939ab4ad8bf8bff6c48f6d5719efd677d79668efc5559b8c007f73
capture-delayed-24h.sh          4d8f0c4c542763457c31a9b616a72c232251ac39dd8b28aef86ffa9b0024e3ac
capture-delayed-logs.sh         f7d0dc3e6e04f514985ad22293252d3552719e702110004fdc5bcd9d8383b255
capture-delayed-paired-end.sh   3aceb60506df7c923b680b6a5fe47accaa6e6b55289528e7b5302d62c030954e
```

Remote `bash -n`, exact contract, installed SHA verification, r1-to-r2 diff,
and pre-horizon rejection gates passed. The four scripts unrelated to sample
freezing are byte-identical to r1; the only r2 executable difference is the
bounded all-sample capture and its metadata/Compose consistency checks.

```text
installed SHA256SUMS SHA-256
defc2079ef78ca557d19788b77a0dab2efbf379c237e1246c0fb094795592678
contract green output SHA-256
926e80ef499fcde36216739c6a09218b9f8ba724adbc69926a32f20c220427e2
early-rejection SHA256SUMS SHA-256
59ca3dab888843b86bda17972118ba07212d46e86e5dc1f16b15f595d4d3a15f
r1-to-r2 derivation diff SHA-256
0ecf4caa2c9a964aa5ff487a17779ce9d3e7e2e6bd369f621423e4b989895512
```

Both delayed window and paired capture returned non-zero before the horizon,
with no formal directory, partial directory, paired directory addition, or
pointer change. The final residual audit reported
`delayed_checkpoint_r2_evidence=complete residuals=clean`. This preparation
created only isolated operator evidence. It did not modify PIG, Router,
observer behavior, Compose, images, containers, routes, or production traffic.
The 24-hour heartbeat must not be created until the six-hour checkpoint has
finished; when created, it must pin this delayed r2 directory and archive hash.

### 2026-08-22 3.9-hour partial trend and post-reset attribution

At `2026-08-22T08:54:39.589Z`, a second immutable partial copy extended the
same observer run to 14,160.001 seconds. This remains a diagnostic preflight,
not a formal six-hour checkpoint. Its exact artifacts are:

```text
window analysis
/var/volatile/dstack/persistent/.cache/pig-partial-window-20260822T0852Z
window analysis SHA-256
00473cb5f7e4426159c23e546337953fb043a3c33ab070166d0925b37e12f5c9
post-reset paired analysis
/var/volatile/dstack/persistent/.cache/pig-paired-partial/20260822T0852Z
post-reset paired analysis SHA-256
966751ae7d9c8c33401c1796156d688fad2c7edf8372a549dd6061580fbea91c
fixed trend summary
/var/volatile/dstack/persistent/.cache/pig-partial-trend-20260822T0852Z
trend summary SHA-256
6bed1dba19b81050019d140c841cc5bab02001c7ff80d7ba6549c8abf7b10c14
```

The continuous window retained 473/473 runtime-service-complete samples and
468/473 all-surface-complete samples. The same five Router collection failures
from the earlier portion remain visible as incomplete samples; no failure was
removed or imputed. The formal result remains false for
`incomplete_samples`, `insufficient_samples`, and
`insufficient_observed_span`.

The 3.2-hour to 3.9-hour same-run trend was:

```text
                                      3.2h       3.9h       change
TPS p05                               61.51      61.34      -0.17 tok/s
below-reference fraction              0%         0%          0
waiting p95 / max                     0 / 2      0 / 2       unchanged
preemptions / PIG failures / proxy    0/0/0      0/0/0       unchanged
protection share                      5.56%      7.15%      +1.59 pp
Router backpressure duty              1.84%      1.92%      +0.09 pp
GPU mean / p95                        44.61/91   45.14/91   +0.54/0 pp
KV mean / max                         1.67/13.28 1.79/19.00 +0.11/+5.72 pp
backend cache-hit share               40.60%     41.90%     +1.30 pp
raw generation work                   254.78     247.67     -7.10 tok/s
prediction / pre-forward mean, ms     .018/.149  .018/.165  approximately stable
```

Successful completion goodput remains unavailable on live v0.12.18, so the raw
generation change cannot be interpreted as a goodput regression or gain. The
protection-share increase with stable high TPS, zero waiting p95, zero
preemption, and nearly unchanged GPU mean strengthens the bounded
over-protection hypothesis, but intermittent offered demand and unmatched
traffic still prevent a causal throughput claim.

The separate post-reset paired segment covered 6,410 seconds and passed runtime,
Router continuity, and required-field gates. It captured 5,287 target PIG
decisions: 4,650 fit and 637 risk. Final PIG protections and Router target 429s
were both exactly 637, preserving exact attribution and excluding a hidden
protection path.

```text
final protection reason/scope                count
tps_reference/load                            586
prefill_budget/load                            20
prefill_budget/request                         24
input_limit/request                             7

TPS protected-decision subreason             count
qos_budget_unobserved                         446
idle                                           95
warming                                        40
waiting                                         6
qos_budget_active_lease                         3
qos_budget_ineligible                           1
```

`qos_budget_unobserved` plus `idle` therefore accounted for 541/591, or about
91.5%, of TPS-protected decision outcomes. Five of those TPS decisions ended
with a stronger Prefill or request-scoped final reason, explaining why there
were 591 protected TPS decisions but 586 final `tps_reference/load`
protections. This is not hidden Router backpressure.

Request-size evidence supports keeping long-input protection independent:

```text
outcome              total    p50 estimated input    p95
admitted             4,650             934           13,199
load protected         606           3,407           15,160
request protected       31         122,880           >524,288 at p95
```

The target backend recorded zero preemptions and zero PIG proxy errors. Its
request TPOT histogram implied approximately 41.92 tok/s at p95 and 29.26 tok/s
at p99 by inverse TPOT, both above the 25 reference in this segment. These are
request-histogram proxies, not the same statistic as controller trailing
mean-active TPS and not successful completion goodput.

The legacy comparator also had zero preemptions, but this pair is descriptive
only: target/comparator cache-hit share was 43.7%/52.4%, while prompt-token p99
was approximately 10.0K/41.5K. Raw generation and request rates therefore
cannot support a new-versus-legacy performance claim.

The falsifiable hypothesis for the formal checkpoint is now narrower: if the
completed post-reset segment preserves these QoS/stability properties and
`qos_budget_unobserved` plus bounded-idle protection still dominates short
requests, the next behavior candidate may allow a tightly bounded second QoS
debt lease for that branch only. It must not alter request-scoped input limits,
Prefill budgets, KV protection, waiting/preemption stops, or long-input policy.
No behavior change or v0.12.19 work starts from this partial evidence alone.

A final liveness check at `2026-08-22T08:57:49Z` found the same PIG/backend
container identities, no OOM or restart delta, no recent PIG lifecycle event,
the observer still writing, unchanged live Compose identity, and fresh Router
`request_aware_open` metrics. No production inference request or runtime
mutation was performed for this analysis.

### 2026-08-22 QoS debt and idle architecture pre-review correction

The Router response/uptime repair is owned by the separately submitted Router
change. PIG will not translate a Router 404, synthesize Router success, alter
Router counters, or add a Router-specific admission exception. The current PIG
source review was performed read-only at commit `ff02694` before the formal
six-hour checkpoint. It changes the earlier candidate wording as follows.

The two dominant TPS subreasons do not share one control path:

```text
qos_budget_unobserved
  ready non-idle TPS window
  -> base/current-rate capacity exhausted
  -> marginal QoS-surplus forecast
  -> blocked because a prior marginal sequence is not yet observed

idle
  ready TPS window but RawRunning=0 and GenerationDelta=0
  -> direct bounded refill limit of 2
  -> QoS-surplus forecast is not called
```

Consequently, a second QoS debt lease can affect
`qos_budget_unobserved`, but cannot affect `idle`. The previous combined wording
is superseded. Formal evidence must select one of these hypotheses; they must
not be implemented together in v0.12.19.

Review 1, model and causality:

1. `projectedTPSSequences` already includes raw running/waiting, pending
   Prefill, local Decode, and unobserved sequences. Base/current-rate fitting
   requests remain admissible while another request is unobserved; only the
   marginal rolling-surplus path is closed.
2. `qos_budget_unobserved` therefore measures denied requests beyond computed
   ordinary capacity, not a global one-request-per-poll lock. It is a valid
   optimization target only if the complete formal segment also shows short
   fitting demand, long-average TPS headroom, low waiting/preemption pressure,
   and lost success-linked goodput or the strongest still-available attributable
   proxy.
3. The existing forecast spends surplus measured in output tokens above
   `reference * qualified sequence-seconds`. A second lease must consume only
   remaining surplus. Re-evaluating the same rolling snapshot without a
   committed-debt ledger would double-spend historical headroom.
4. The smallest coherent non-idle candidate is a maximum two-lease wave with an
   aggregate committed-debt value. The first lease retains its current bounded
   deficit cost. A second lease reserves a conservative full marginal
   `reference * forecast_seconds` obligation, rounded upward to a bounded
   integer token debt, and is admitted only when current rolling surplus covers
   all existing commitments plus the new one. Unknown output still uses the
   fixed ten-second control horizon; a known output may shorten its own
   forecast, never another live commitment.
5. The idle branch remains unchanged in that candidate. If selected instead,
   idle refill needs its own bounded probe design and simulation because it has
   no current generation rate. It must not be justified by the non-idle debt
   ledger.

Review 2, atomicity, lifecycle, and efficiency:

1. `AdmissionController.Admit` already performs state projection, forecast,
   decision, cache-credit spend, reservation contribution, and overlay commit
   under one mutex. No second lock, queue, learner, timer, or background worker
   is needed.
2. Current reservation state stores only `qosBudgeted bool`, and the overlay
   stores only `qosBudgetLeases int64`. Changing the guards from `> 0` to a
   count of two is explicitly forbidden: it has no value with which to subtract
   the first commitment from rolling surplus.
3. A candidate must attach a bounded integer debt-token value to each budgeted
   reservation and aggregate it in the existing overlay. The same contribution
   must survive reserved, forwarded-Prefill, active-Decode, and residual-debt
   phases. Existing exact lifecycle rules release it on pre-forward rollback or
   a legitimately removable terminal reservation, retain it across terminal
   cancellation/error/disconnect until a covering poll, and clear it on backend
   epoch reset, fail-close, or shutdown. A reference-changing TPS policy update
   must preserve pre-update lifecycle debt but fence new marginal-debt grants
   until every cross-revision lease is reconciled; an obligation priced at the
   old reference must never authorize a second lease at the new reference. The
   soft ten-second forecast is an accounting horizon, not a lease-expiry timer.
4. Arithmetic must fail closed on negative values, overflow, invalid floating
   inputs before rounding, overlay underflow, or disagreement between the fast
   overlay and a slow reservation reconstruction. The production cap remains
   two leases and one Decode sequence per debt admission; multi-sequence fanout
   remains ineligible.
5. Waiting, preemption, stale observation, Prefill, KV, cache-credit, input
   limit, and long-input behavior remain byte-for-byte outside this slice. The
   added hot-path state is constant-size integer arithmetic under the existing
   lock; no request body, tokenizer, map cardinality, or per-model profile is
   added.

Review 3, red tests, simulation, and release boundary:

1. Red unit tests must first prove that current v0.12.18 protects the second
   same-poll marginal request, then require the candidate to admit it only when
   aggregate remaining surplus is sufficient. Paired cases must cover
   insufficient surplus, mixed known/unknown output horizons, active versus
   unobserved first leases, lease cap, multi-sequence requests, waiting,
   preemption, stale metrics, same-reference and reference-changing policy
   updates, the cross-revision fence, reset, success, pre-forward rollback,
   timeout, cancellation, error, disconnect, and counter overflow.
2. Property and race gates must prove atomic cap enforcement, no double spend,
   no double release, no leaked commitment, exact overlay reconstruction, and
   fail-closed behavior under concurrent Admit/forward/first-byte/terminal/poll
   interleavings.
3. Deterministic simulation must compare the exact v0.12.18 policy with the
   candidate on pre-poll bursts, short/unknown/mixed outputs, long-running
   outputs, sustained waiting, preemption, staleness, backend reset,
   distribution shift, completion-before-poll, and low flow. Acceptance
   requires higher successful request output-token goodput, long-average
   mean-active TPS at or above reference, no preemption/KV violation, no
   material waiting regression, bounded sub-reference exposure, and a maximum
   of two live debt leases. Raw admits alone do not pass.
4. Idle scenarios remain regression gates for the non-idle candidate, not its
   claimed gain. A later idle candidate would need its own red test, causal
   simulation win, and behavior version.
5. No behavior code, v0.12.19 identity, image, registry publication, Compose,
   Router, backend, CVM process, or production traffic changes are authorized
   by this pre-review. Formal six-hour evidence remains the gate that decides
   whether to start the red-test slice or retain v0.12.18.

All three review passes reject the simple `QoSBudgetLeases < 2` patch and the
combined debt-plus-idle hypothesis. They accept only the above test-first,
single-branch candidate as implementation-ready if the formal checkpoint
selects it. The 24-hour checkpoint remains required regardless of the six-hour
decision.

### 2026-08-22 OpenRouter 404 versus PIG 429 pre-checkpoint isolation

A fresh read-only incident pass rejected the hypothesis that PIG's JSON error
shape can explain OpenRouter's `error-404` classification. PIG v0.12.18 does
return an HTTP 429 before forwarding, but its direct envelope is an older
compatibility shape:

```text
HTTP status              429
Content-Type             application/json
error.type               TooManyRequestsError
error.code               numeric 429
Retry-After              absent
```

This direct envelope should eventually be normalized to
`rate_limit_error` / `rate_limit_exceeded` with `Retry-After`, but the live
Router at source revision `991be26b4c65cd6745460266348dcc78866ad84e`
classifies capacity from the upstream HTTP status rather than those JSON
fields. It rebuilds a Router-owned HTTP 429 with the string code and rate-limit
headers after candidate exhaustion. PIG enforced-reject deltas and the
Router's target upstream-429 deltas also matched exactly in repeated natural
traffic samples. The PIG envelope is therefore a protocol-hardening candidate,
not a supported explanation for an HTTP 404.

Refreshing the authenticated OpenRouter endpoint page at approximately
`2026-08-22T09:48Z` produced newer evidence than the previously cached chart:

```text
window                    1 Hour
uptime                    34.03%
total requests            approximately 4.34K
success-200               1,474
error-404                 2,857
error-429                 4
error-400                 5
latest tooltip            2026-08-22 17:48 Asia/Shanghai; error-404=2
```

OpenRouter's own visible explanation states that a fast HTTP 429 is recorded as
`error-429` and allows rerouting, while other 4xx/5xx responses are recorded as
their actual HTTP status. Even if all four 429s counted against uptime, they
could not explain a window dominated by 2,857 404s. The refreshed chart also
means the incident cannot be dismissed solely as a page that stopped at the
old `17:08` bucket. Most of the rolling-hour 404 total can still predate the
Router repair, but at least two current 404s require attribution.

The same current Router epoch remained continuous. At
`2026-08-22T09:46:31Z` its config digest was
`sha256:007d78ec80c8f5704bdfbc8cf9268321f75b639447999e134d166e13ebc80c6d`.
The enabled routes reported:

```text
route      processed   attempts   upstream 429   protocol
use1-19       31,496     45,999         37,161   request_aware_open
use1-4c       21,679     44,234         36,463   legacy
```

Router metrics showed that the dominant failed surface was streaming
`/v1/completions`, not buffered chat:

```text
surface                              requests   receipts   upstream non-2xx
/v1/completions streaming             43,695      7,853              35,672
/v1/chat/completions streaming          9,249      8,284                 932
```

Those cumulative counters expose only a `4xx` status class for generated or
upstream failures. They cannot prove the final client status. In the exact
`09:40-09:48Z` log slice, Router recorded 22 upstream-429 lines and zero
`status=404`, `upstream_status=404`, or `model_not_found` lines. This does not
fully exonerate the Router surface because source review found two 404 paths
that need not emit the existing request-outcome record:

1. `handle_completion` compares the requested model and configured public model
   by case-sensitive exact string equality, then calls `model_not_found` with
   HTTP 404 on mismatch; that helper currently does not log a generated
   request outcome.
2. Axum has no explicit fallback handler around the enumerated inference
   routes, so an unmatched path or HTTP method returns a framework 404 outside
   the completion outcome logger.

The successful Router outcome logs consistently used
`google/gemma-4-31B-it`; no lower-case `google/gemma-4-31b-it` value appeared in
the retained 50,000-line log window. That absence cannot exclude silent
model-mismatch requests because the mismatch path itself lacks logging. No
production probe was sent to manufacture evidence.

Checkpoint liveness remained healthy at `2026-08-22T09:44:29Z`: the original
observer was `running`, its latest sample was `09:44:09.589Z`, it had written
572 samples, and all five real Router-collection error lines remained intact.
PIG/vLLM/HAProxy/ingress were running with OOM flags false and restart counts
`0/1/0/0`; the vLLM restart is the already recorded historical event. The live
Compose hash and all r4 checkpoint archive/script hashes still matched the
frozen plan, and there was no premature formal or partial checkpoint output.

The formal `18:59` continuation must therefore make these distinctions:

1. refresh the OpenRouter one-hour window after the old high-404 buckets have
   aged out and report post-repair minute buckets rather than only the rolling
   total;
2. correlate any new 404 with Router model/path visibility and outer ingress;
   do not infer a PIG admission fault from an unobserved Router response;
3. preserve all real observer collection failures and independently report
   runtime-service, matched-routing, and strict all-surface eligibility;
4. retain v0.12.18 if the formal QoS/throughput evidence does not select the
   reviewed non-idle debt-ledger hypothesis; and
5. consider PIG's direct 429 envelope only as a separately tested protocol
   cleanup. It must not be presented as the fix for OpenRouter `error-404`.

This pass changed no PIG behavior, Router source, image, Compose, route,
container, or production request. The existing one-time `pig-6` heartbeat was
updated only to carry these diagnostic gates into the formal checkpoint.

### 2026-08-22 post-repair 404 persistence and model-identity isolation

A second authenticated OpenRouter refresh at approximately
`2026-08-22T10:06Z` invalidated the earlier working allowance that most of the
rolling-hour 404 total might still predate the Router repair. The explicit
`1 Hour` chart, covering approximately `17:03-17:58 Asia/Shanghai`, reported:

```text
uptime                    17.66%
rounded total requests    3.2K
error-404                 2,620
success-200                 562
error-429                     7
error-400                     6
exact table total         3,195
```

The 404 share was approximately `82.0%`; all 429s together were approximately
`0.22%`. The chart contained 404 bars across most of the window, including the
late part of the window. This is not a PIG 429-envelope effect and is no longer
consistent with merely waiting for pre-repair buckets to age out.

Current-runtime provenance remained exact. The Router attestation named source
commit `991be26b4c65cd6745460266348dcc78866ad84e`, the container was shown as up
for approximately four hours, and the control-plane operation that installed
the current image completed at approximately `14:21 Asia/Shanghai`. The
OpenRouter hour above is therefore wholly post-installation. The live Router
config digest remained
`sha256:007d78ec80c8f5704bdfbc8cf9268321f75b639447999e134d166e13ebc80c6d`.

Source and live-config review identified a stronger primary hypothesis. The
OpenRouter endpoint's canonical displayed model is
`google/gemma-4-31b-it`, while all of these live Router identities use an
upper-case `B`:

```text
middleware.public_model              google/gemma-4-31B-it
all six upstream public-model keys   google/gemma-4-31B-it
all six upstream model values        google/gemma-4-31B-it
successful Router outcome logs       google/gemma-4-31B-it
```

`RouterBackend::handle_completion` currently compares the request's `model`
with `middleware.public_model` using case-sensitive string equality. A mismatch
returns `completion::model_not_found` with HTTP 404 before route selection or
forwarding. `finalize_generated` currently does not record a generated outcome,
and the service request counter is incremented only on the later forwarding
path. A lower-case request cohort can consequently create exactly the observed
shape: OpenRouter sees 404 while Router attempt, upstream-response, and outcome
logs contain no corresponding 404.

This is a strong causal hypothesis, not yet proof that the affected natural
requests actually carried the lower-case value. No synthetic inference request
was sent and the silent branch does not preserve the required request evidence.
Do not globally case-fold arbitrary model identifiers as an assumed fix. A
Router repair should instead provide an explicit public alias-to-canonical
mapping, or map this deployment's lower-case public identifier to the existing
upper-case upstream identifier, while preserving the upstream model value.
It must also record every generated non-2xx response at the outermost request
boundary without logging request bodies or secrets.

The current Cloud log drawer provided a small, virtualized natural-traffic
slice from `09:59:28Z` through `10:05:55Z`. Within the 32 visible timestamped
lines it showed zero `status=404`, zero `upstream_status=404`, and zero
`model_not_found`; it showed 23 `status=499 outcome=ClientClosed` stream
settlements, two upstream-429 observation lines, and one upstream/final 400.
Two of the 499s had zero data events, while many others ended at approximately
292 seconds after producing data. Those client-closed streams are a separate
availability signal that needs correlation with OpenRouter's streaming
timeout behavior; they cannot be relabeled as HTTP 404 without direct evidence.
The dstack-ingress access format exposes timing, byte, and termination fields
but no final HTTP status, so it cannot close this evidence gap.

The formal six-hour checkpoint must now treat the surfaces independently:

1. PIG runtime-service health and the post-reset matched Router comparison may
   still be analyzed if their own identity and counter-continuity gates pass.
2. Strict all-surface eligibility must fail while the OpenRouter endpoint has
   an unexplained approximately 82% 404 share, even if PIG itself is healthy.
3. The formal report must not attribute those 404s to PIG admission or count
   them as PIG QoS protection.
4. A Router-side capture of final status, method, normalized path, request ID,
   and a bounded model identity is needed to prove or reject the model-alias
   hypothesis. Request bodies and bearer values must not be logged.
5. PIG v0.12.18 remains unchanged until the frozen six-hour evidence is
   collected. The external 404 incident is not evidence for relaxing or
   tightening PIG admission.

This isolation pass changed no PIG/Router code, image, Compose, route,
container, CVM, or production request. It used only current attestation,
redacted admin configuration, existing metrics/logs, and the authenticated
OpenRouter dashboard.

### 2026-08-22 user-directed closure of the external 404 investigation

The user subsequently reported that Redpill had fixed the external issue and
explicitly directed this task to stop investigating it. This instruction
supersedes the additional Router/OpenRouter attribution work and the external
404 gating language above for the current PIG objective.

From this point:

1. do not inspect more OpenRouter, Redpill, Router, model-alias, ingress, 499,
   or external uptime evidence for this incident;
2. do not change Router source, configuration, image, route state, or logging
   as part of the PIG task;
3. retain the prior observations only as historical isolation evidence and do
   not treat the unproven model-alias hypothesis as a confirmed root cause;
4. do not use the external incident as evidence to relax or tighten PIG
   admission; and
5. continue the frozen six-hour and 24-hour PIG checkpoints using PIG runtime,
   backend, and already planned matched-routing evidence. The external fix is
   user-reported and intentionally not re-verified by this task.

No runtime, source behavior, image, Compose, route, container, CVM, or
production request changed in recording this scope correction.

### 2026-08-22 pre-checkpoint debt-ledger implementation audit correction

A further read-only review at source HEAD `bc4ae6d` checked the proposed
non-idle debt candidate against the concrete Controller, projection,
reservation, reconciliation, and dynamic TPS-policy paths. It found two
attribution/fencing requirements that the earlier pre-review did not state
precisely enough. The formal six-hour evidence remains the behavior gate; this
section does not select or implement the candidate.

First, the candidate must not relax `UnobservedSequences > 0` merely because
one QoS-budget lease exists. `UnobservedSequences` also includes ordinary
fitting reservations, so that shortcut could admit a second marginal request
while unrelated demand has not yet materialized in backend metrics. A selected
candidate must aggregate the intersection explicitly: unobserved Decode
sequences belonging to QoS-budgeted reservations. A second marginal grant is
eligible only when every currently unobserved sequence is attributable to the
bounded QoS-debt wave; any unrelated unobserved sequence preserves the current
`qos_budget_unobserved` protection. The value must follow the same reserved,
forwarded-Prefill, active-Decode, terminal-debt, covering-poll, reset,
fail-close, and shutdown lifecycle as its owning reservation.

Second, the reference-change fence must use the Controller's existing
`tpsPolicyEpoch`, not the externally visible `policyRevision`. The policy
revision increases even for a same-reference CAS write, while `tpsPolicyEpoch`
increases only when the TPS reference actually changes and already excludes a
sample window that straddles that change. Each debt lease must therefore store
the admission-time TPS policy epoch, and the constant-size aggregate ledger
must retain the common live debt epoch. A new grant is forbidden when a live
ledger epoch differs from the current TPS policy epoch. A same-reference write
must preserve eligibility and evidence; a reference-changing write must reset
the TPS window and fence all old-epoch debt until reconciliation clears it.
Because cross-epoch grants are forbidden, the live aggregate has either zero
leases and epoch zero, or one/two leases sharing one nonzero epoch; add,
subtract, replace, slow reconstruction, overflow, and underflow checks must
enforce that invariant.

The coherent candidate state is consequently still constant-size, but is more
specific than a lease count:

```text
per QoS-budget reservation
  committed_debt_tokens
  tps_policy_epoch
  budget-attributed unobserved Decode sequences

aggregate reservation overlay
  qos_budget_leases             <= 2
  qos_committed_debt_tokens
  qos_budget_policy_epoch       common epoch or zero
  qos_budget_unobserved_sequences
```

The forecast must return the upward-rounded bounded debt value with the grant,
and `AdmissionController.Admit` must commit that value atomically with the
reservation under the existing mutex. The first and second grant must subtract
all aggregate committed debt from the same rolling surplus before spending it.
Decision records, logs, and metrics must expose enough before/after committed
debt and attribution to distinguish an actual second grant, insufficient
remaining surplus, unrelated-unobserved protection, the two-lease cap, and an
old-policy-epoch fence; no silent protection branch is acceptable.

Focused red tests must now include both false-attribution directions: one
same-poll QoS-budget reservation may permit the second bounded debt grant when
the aggregate budget proves it, while one unrelated ordinary unobserved
reservation must continue to block it. Policy tests must separately cover an
equal-reference update, a reference-changing update, a straddling sample,
old-epoch live and residual debt, reconciliation to an empty epoch, and the
first new-epoch grant after fresh TPS evidence matures. Property, race,
simulation, and idle-regression requirements from the prior three review passes
remain unchanged.

This correction rejects an implementation based only on
`QoSBudgetLeases < 2`, any implementation that treats all unobserved demand as
budget-attributed, and any fence based on ordinary policy revision. It changes
no Go source, tests, version, image, runtime configuration, process, route, or
production request.

### 2026-08-22 frozen six-hour behavior-selection matrix

The six-hour checkpoint must not choose a metric after seeing the result. Live
v0.12.18 does not export success-linked completion tokens, so the checkpoint
cannot prove a completion-goodput improvement or regression. The following
matrix is frozen before capture and limits what the available evidence may
authorize.

Keep v0.12.18 and start no behavior slice when any of these holds:

1. `runtime_service` is ineligible because of an incomplete PIG/backend/GPU/
   container sample, runtime identity change, new restart, OOM, non-running
   component, critical counter reset/missing series, or sampling gap;
2. the post-reset paired segment is ineligible because a required target or
   comparator runtime field is unavailable, a backend epoch/model/PIG/Compose
   identity changed, a required counter rolled back, or matched Router identity
   and counter continuity failed;
3. the target's long-window mean-active TPS does not average at or above its
   configured reference under qualified load, or below-reference exposure is
   sustained rather than occasional;
4. target preemptions, PIG proxy failures, OOM/restart evidence, or persistent
   waiting appear; an isolated nonpersistent endpoint-max waiting observation
   is reported but is not by itself a failure;
5. enforced PIG protections do not reconcile with the target Router 429 surface
   apart from an explicitly reported scrape-boundary in-flight difference;
6. protected request-size and reason evidence shows that long-input, Prefill,
   KV, waiting, preemption, or request-scoped protection is the material limit;
   or
7. offered demand, TPS decision subreason, request-size, and stability evidence
   is too incomplete to distinguish non-idle marginal debt from bounded idle
   refill. Low or unlike traffic is `inconclusive`, never a reason to loosen.

Authorize only the non-idle debt-ledger red-test slice, not a release or live
change, when all of the following are true:

1. `runtime_service` is eligible and the complete target window has no new
   preemption, PIG proxy failure, OOM, restart, lifecycle leak, or persistent
   waiting;
2. the post-reset paired runtime and matched-routing comparison is eligible,
   and target PIG risk/enforced-protection deltas reconcile with target Router
   upstream-429 deltas within the recorded scrape-boundary allowance;
3. target mean-active TPS averages at or above the reference under qualified
   load and any below-reference samples are isolated rather than a sustained
   deficit;
4. `qos_budget_unobserved` is the largest TPS-protection subreason and accounts
   for more than half of TPS-protected decisions after keeping stronger final
   Prefill/request protections separate;
5. those load-protected requests remain predominantly ordinary short inputs
   below the 64K regular-Prefill boundary, while long/request-scoped protections
   remain independently enforced; and
6. the result explicitly labels successful completion goodput unavailable and
   treats accepted/completed alignment, non-error terminal rate, raw generation
   work, cache share, prompt shape, GPU, and KV only as hypothesis-supporting
   proxies. None may be renamed as successful goodput or a causal PIG gain.

An authorization under this branch permits focused failing tests and
deterministic simulation for the constant-size debt ledger defined above. It
does not authorize v0.12.19 source behavior, an image, deployment, or a live
traffic claim until the red/green, property/race, simulation, builder, and
release gates pass.

Authorize only a separate idle-refill red-test slice when `idle` is the largest
TPS-protection subreason and exact decision-time evidence proves repeated
offered demand remained idle beyond the existing one-poll 500-ms bound. A
30-second endpoint gauge or idle counter alone cannot prove this condition. If
that evidence is unavailable, retain v0.12.18 rather than combining idle with
the debt candidate.

The continuous six-hour all-surface result is expected to remain strict and may
be ineligible because the five preserved Router-incomplete rows create seven
missing Router counter intervals. That does not erase the rows, invalidate an
otherwise complete `runtime_service` result, or permit those missing intervals
to be bridged. The independent post-reset paired result is the only eligible
source for a matched-routing comparison if all of its own gates pass.

This frozen matrix resolves the earlier backlog wording that requested a valid
completion-goodput baseline even though deployed v0.12.18 cannot export one.
The 24-hour checkpoint remains mandatory whichever six-hour branch is chosen.
