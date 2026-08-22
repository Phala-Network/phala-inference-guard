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
| 1 | Create a reusable semantic observer for the four windows | In progress; paired layer accepted | vLLM/SGLang source mappings, reset handling, completeness checks, cohort output, hashes, and cleanup tested remotely |
| 2 | Establish a traffic-matched v0.12.18 baseline | Observing | At least one valid demand cohort with completion goodput, weighted TPS, cache, size, running/waiting, GPU/KV, protection, and stability evidence |
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
matched-routing integrity       false; 7 Router samples incomplete
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
but remains explicitly unavailable as successful completion goodput. Seven
incomplete Router samples preserve the strict all-surface stop reason; they are
not deleted, interpolated, or relabeled as PIG failures. No inference request,
container/CVM restart, image action, configuration change, or Router mutation
was performed. The next decision remains the fixed six-hour checkpoint at
`2026-08-22T10:58:39.588Z`.
