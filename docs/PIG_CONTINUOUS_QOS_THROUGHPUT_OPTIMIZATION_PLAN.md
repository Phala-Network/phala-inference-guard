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
| 1 | Create a reusable semantic observer for the four windows | In progress | vLLM/SGLang source mappings, reset handling, completeness checks, cohort output, hashes, and cleanup tested remotely |
| 2 | Establish a traffic-matched v0.12.18 baseline | Pending | At least one valid demand cohort with completion goodput, weighted TPS, cache, size, running/waiting, GPU/KV, protection, and stability evidence |
| 3 | Classify the first material bottleneck | Pending | Exactly one falsifiable hypothesis selected from Section 6, or an explicit no-change result |
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
