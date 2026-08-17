# PIG v0.12.15 Cache-Aware Backend Contract And Release Plan

## 1. Objective

Maximize sustained aggregate throughput subject to bounded QoS degradation. The
request must be admitted or protected before it reaches the backend; backend
feedback only updates the next observation and never rewrites an in-flight
decision.

v0.12.15 must correct two backend-adapter contract defects and one missing
admission-accounting input discovered after v0.12.14:

- accept the SGLang protected/session-held KV gap while charging the whole
  non-reclaimable gap to admission;
- require vLLM gauges and counters to have the types declared by vLLM itself;
- use backend-native token counters to estimate recent cache-aware Prefill
  compute cost without weakening KV fit or long-context safeguards.

Keep the bounded model-neutral lexical token estimator and complete estimated
input tokens as the request-size signal. This is the requested simple, fast
tokenizer-style approximation, not an exact model tokenizer or chat-template
oracle. Do not add a cache lookup, per-model asset, learning algorithm, TTFT
gate, Router behavior, or request mutation. Keep the default backend metrics
interval at 500 ms and default predictive mode at enforce.
`PREDICTIVE_TPS_REFERENCE=50` remains the intended f563 production QoS
reference; isolated tests may explicitly override it.

The correction must preserve QoS-constrained throughput: count all
non-reclaimable full-pool slots against admission, accept valid
protected/session-held KV gaps, and reject a torn scrape whenever accepting it
could understate non-reclaimable KV. It must also avoid treating evictable
prefix-cache KV as occupied hard capacity merely because the cache hit rate is
high.

### 1.1 Non-negotiable execution contract

The following requirements apply to every v0.12.15 source revision, candidate
image, and production transition:

- The optimization objective is maximum sustained SLO-compliant completion
  throughput and GPU utilization, not minimum concurrency or a perfect
  instantaneous TPS floor. Occasional low-TPS observations are acceptable;
  sustained avoidable degradation, excess preemption, or lower long-window
  goodput is not.
- Admission is predictive and occurs before forwarding. Backend metrics,
  completion results, and cache counters may affect only the next decision.
  They must never retroactively justify a forwarded request or create a delayed
  cooldown after current capacity has recovered.
- Request size comes from one bounded, model-independent lexical estimator pass
  over the supported request body. Reuse the existing bounded body parse; do
  not add a second JSON parse, model-specific tokenizer assets, exact tokenizer
  parity, a prefix lookup, or a network call. Unsupported, truncated, or
  oversized bodies use the documented conservative fallback. The complete
  estimated input, not a cache-discounted value, drives context fit, KV
  reservation, and long-input class. The hot path must be benchmarked, and the
  accepted extreme body's p99 must remain below 100 ms on f563.
- Model context, KV capacity/block geometry, KV hard limit, and Prefill class
  thresholds are initialized automatically from one coherent upstream
  capability profile. They are immutable within a backend epoch and are
  re-derived only after explicit epoch/config drift and full revalidation.
  Prefill or KV thresholds must not learn from traffic. v0.12.15 introduces no
  other learning algorithm.
- vLLM and SGLang are independent metric protocols. Each adapter must validate
  its own metric names, `TYPE`, labels, rank/engine aggregation, reset behavior,
  and freshness. A metric from one backend must never be substituted into the
  other backend's contract. No model family receives special behavior.
- Cache awareness must prevent compute over-protection without pretending that
  active cached tokens consume no KV. Evictable cache is excluded from hard KV
  occupancy; a bounded recent hit fraction may reduce only aggregate Prefill
  compute charge. Full input remains reserved for KV and long-context safety.
- Admission is request-aware rather than an all-or-nothing node gate. Under the
  same backend snapshot, a large request may be protected while a smaller
  fitting request is admitted. A request-scoped large-input decision must not
  close the node globally, and this work must not turn `429` into an enlarged
  global queue wait or worse TTFT.
- `PREDICTIVE_TPS_REFERENCE=50` is the intended f563 long-window QoS reference,
  not a per-sample hard floor. TPS protection must reopen immediately when the
  current predictive state fits, tolerate sparse/low-flow samples, and avoid
  self-lock. Metrics polling defaults to 500 ms; no fixed one-second sleep or
  one-second admission hold may erase that cadence.
- Production defaults to `enforce`. `shadow` is allowed only when explicitly
  configured for an isolated test. Production YAML must contain only required
  deployment values and intentional overrides such as the TPS reference; do
  not spell out default Prefill/KV thresholds or other default values.
- v0.12.15 PIG does not route, mutate requests, inject backend priority, or
  preserve the removed premium/basic or legacy QoS modes. It exposes truthful
  current capacity and protection state for Router consumption, but Router
  policy is outside this repository and release. This source boundary does not
  alter the exact legacy behavior of the `0.8.13` fallback once that f563
  configuration has been separately reconstructed and reverified.
- Every enforced protection outcome must be visible and mutually consistent in
  bounded decision logs, periodic status, PIG metrics, upstream status, and
  Router-compatible capacity. Agreement is scope-aware: request-scoped
  protection remains visible but keeps the node open when a canonical smaller
  request fits, while load/availability protection publishes non-open capacity.
  A hidden reject, an open signal during current load/availability protection,
  or a stale lock after recovery is a release-blocking defect.
- Existing Router and admin/frontend consumers must receive truthful, stable
  protection and capacity fields from PIG. PIG does not implement their routing
  or presentation policy. If compatibility requires a change outside this
  repository, record it as a separate release blocker instead of fabricating a
  PIG signal or silently leaving protection invisible.
- Keep backend adapters, observations, policy, reservation lifecycle, HTTP
  projection, and observability behind separate interfaces with single
  responsibilities. Review allocations, parsing, label scans, lock scope, and
  bounded state on the request/500-ms paths; do not trade correctness for
  abstraction count or retain dead legacy code.
- All executable Go tests, race checks, simulations, builds, image work, and
  benchmarks run only in an isolated f563 workbench. Do not use the retired c21
  CVM, the old builder, or local Windows execution. Never restart the f563 CVM,
  SGLang, or HAProxy for PIG work; rebuild or replace only PIG containers.
- Commit and push every source or plan revision. Build a host-local image only
  from the exact pushed commit after source gates pass, and upload a registry
  image only after the complete candidate matrix and three reviews pass.

## 2. Production Finding

Production SGLang source version `0.0.0.dev1+gc4271c3fe` defines:

```text
kv_available_tokens + kv_evictable_tokens + kv_used_tokens
    <= max_total_num_tokens
```

`kv_used_tokens` is active locked KV only. The gap may contain protected or
session-held slots that are neither free, evictable, nor active. The safe common
value remains:

```text
non_reclaimable = capacity - available - evictable
```

v0.12.14 incorrectly required `kv_used_tokens == non_reclaimable`. That equality
works when the gap is zero but fail-closes a valid scrape when protected/session
KV exists. Live production scrapes also proved that the three gauges update
sequentially and can be temporarily torn.

The vLLM adapter already keeps vLLM's aggregation rules separate from SGLang,
but it currently accepts samples without checking Prometheus `TYPE`. Upstream
vLLM commit `5fd7a888386cff800f32de6b5a33d1dd3ca1e397` declares:

```text
num_requests_running       gauge, one series per engine
num_requests_waiting       gauge, one series per engine
kv_cache_usage_perc        gauge, one series per engine
cache_config_info          gauge, one coherent geometry per engine
generation_tokens_total    counter, one series per engine
num_preemptions_total      counter, one series per engine
```

Running, waiting, generation, and preemption remain sums across vLLM engine
series. KV usage remains the maximum per-engine ratio, and cache geometry must
be identical across present engine series. The one coherent per-engine KV pool
is deliberately not multiplied by DP engine count: this is conservative for a
single PIG upstream and preserves per-request fit even when vLLM chooses the
engine. SGLang `priority=""` filters and TP/PP maximum deduplication must never
be applied to these vLLM series.

## 3. Correct Contracts

### 3.1 SGLang

The SGLang adapter must require:

```text
0 <= active_used <= non_reclaimable <= capacity
```

PIG continues to publish `non_reclaimable` as common used KV. Therefore the
protected/session gap is charged to admission rather than treated as capacity.
`active_used > non_reclaimable` remains invalid because it proves the candidate
scrape would understate occupied KV. A torn scrape that only overstates
non-reclaimable KV is conservative and may be accepted.

All existing model, engine, DP, priority, metric-type, page-geometry, counter,
freshness, and reservation rules remain unchanged.

The f563 SGLang `0.0.0.dev1+gc4271c3fe` live scrape confirms the selected
families and types. Decode TPS uses the scheduler-interval counter
`sglang:realtime_tokens_total{mode="decode",priority=""}`. It must not use the
completion-time `generation_tokens_total` counter or instantaneous
`gen_throughput` gauge. KV retraction uses
`num_retracted_requests_total{priority=""}` when materialized; the resettable
`num_retracted_reqs` gauge and async-weight-sync `num_paused_reqs` are not
preemption counters.

### 3.2 vLLM

Every materialized vLLM admission family must match its declared type:

```text
cache_config_info, kv_cache_usage_perc,
num_requests_running, num_requests_waiting = gauge

generation_tokens_total, num_preemptions_total = counter
```

Missing or wrong types invalidate the affected common field and make startup or
fresh-observation validation fail closed. Unlike SGLang's documented
multiprocess cold-zero exceptions, vLLM initializes these labeled children; no
missing-TYPE cold-zero exception is allowed.

### 3.3 Cache-aware Prefill cost

Cache awareness has two deliberately separate effects:

```text
KV fit and reservation             = complete estimated input tokens
long-input admission class         = complete estimated input tokens
recent Prefill compute estimate    = complete input * bounded cold fraction
```

A prefix-cache hit avoids part of Prefill compute, but the reused prefix is
locked/non-reclaimable while the request runs and therefore still consumes KV.
Consequently cache hit credit must never reduce per-request KV reservation,
post-admit KV projection, maximum-input validation, or the
regular/exclusive/quiescent request class. It may only reduce the request's
aggregate Prefill contention cost inside the existing class.

PIG does not inspect the exact request prefix and must not present a recent
global hit rate as a per-request fact. The observation is a short-lived,
bounded workload estimate. No cache credit is available on the first sample,
after a counter reset, after backend/model/engine epoch drift, when metric type
or labels are invalid, when the observation is stale, or when the recent token
denominator is below the minimum evidence threshold. These cases fall back to
fully cold Prefill without closing a healthy backend.

The backend contracts remain independent:

| Backend | Source | Aggregation |
| --- | --- | --- |
| SGLang | `sglang:prefill_effective_tokens_total{mode="input|device_hit|host_hit|storage_hit",priority=""}` counters | maximum duplicated TP/PP view per mode; one DP replica |
| vLLM | `vllm:prefix_cache_queries_total` and `vllm:prefix_cache_hits_total` counters | sum independent `engine` series |

For SGLang, source inspection confirms that `input` is exported as logged input
minus reprocessed input. The `device_hit`, `host_hit`, and `storage_hit` modes
are exported from separate tier counters. Their accumulation source still must
be traced to prove whether reprocessed/retracted tier hits are excluded before
publication. Until that proof and a focused fixture exist, SGLang cache credit
is release-blocked and must conservatively fall back to fully cold Prefill. It
is not acceptable to infer exclusion from the aggregate `cache_hit_rate`
calculation. Once this contract is proven, the recent hit fraction is:

```text
hits_delta / (input_delta + hits_delta)
```

For vLLM, the token-level recent hit fraction is:

```text
hits_delta / queries_delta
```

PIG validates `counter` TYPE, finite monotonic values, backend-specific labels,
and `0 <= hits <= total`. The observation component owns only bounded counter
state and produces a conservative cold fraction. Admission owns request
classification, KV reservation, TPS policy, and the final decision. This keeps
the backend adapters, observation policy, and admission policy separately
testable.

A positive preemption/retraction delta in the current observation suppresses
all cache credit for that decision sample. It is evidence that recent cache
reuse did not prevent harmful scheduler pressure, so admission uses fully cold
Prefill cost. A later coherent no-preemption poll may resume a still-fresh
bounded cache observation; this suppression creates no cooldown.

The initial implementation must remain intentionally small: one previous
counter snapshot plus one bounded recent observation, no learned state, no
prefix table, no customer cardinality, and no extra upstream request. A longer
window or stronger cache credit may only be introduced after candidate/live
evidence shows that the 500 ms observation cadence is too noisy without causing
low-flow self-lock or workload-transition under-protection.

## 4. Tests And Evidence

Required focused tests:

- equality case remains valid;
- a positive protected/session gap is valid and common used KV includes it;
- active used above derived non-reclaimable KV is invalid;
- TP disagreement, partial dynamic families, wrong types, invalid page geometry,
  and mixed backend signatures remain invalid;
- vLLM accepts the six upstream-declared types and rejects every missing or
  wrong declaration;
- vLLM DP engine series retain sum/maximum/unique-geometry aggregation without
  SGLang label rules;
- SGLang and vLLM accept only their own cache metric TYPE, labels, and
  aggregation contracts;
- SGLang reprocessed/retracted input and every cache tier have source-backed
  counter semantics and focused fixtures; an ambiguous tier counter disables
  cache credit rather than over-crediting Prefill;
- first sample, missing metrics, low evidence, stale observation, counter reset,
  backend epoch change, and invalid ratios produce zero cache credit;
- a qualified cache observation is carried across zero-delta polls for at most
  10 seconds, then expires; a current preemption/retraction delta suppresses it
  immediately without creating a later cooldown;
- a cache observation can reduce only aggregate Prefill compute cost while KV
  fit, KV reservation, maximum input, and long-input class remain byte-for-byte
  unchanged;
- exclusive and quiescent requests cannot be downgraded by a high recent global
  cache hit rate;
- a request-scoped 256K/512K-class protection decision leaves truthful positive
  capacity for a canonical smaller request whenever that smaller request fits;
  no blanket node closure or enlarged global queue is introduced;
- cold traffic following hot traffic does not bypass the long-input safeguards,
  and idle/low-flow intervals do not self-lock admission;
- the 500 ms observer cadence does not introduce an implicit one-second hold,
  and sparse traffic reopens immediately when the current request fits;
- every enforce decision agrees, with request/load/availability scope preserved,
  across request response, logs, periodic status, PIG metrics, upstream status,
  and Router-compatible capacity; shadow decisions remain observable without
  changing intake;
- automatic capability initialization is identical at idle and under load,
  does not learn Prefill/KV thresholds, and revalidates before accepting a new
  backend epoch;
- vLLM fixtures cannot satisfy SGLang fields and SGLang fixtures cannot satisfy
  vLLM fields, including cache, TPS, preemption, and KV families;
- all backend observer and startup fixtures remain green.

Required f563 gates at the exact executable commit:

```text
git diff --check
gofmt check
focused Prometheus and server tests
go test ./...
go test -race ./...
go vet ./...
go build
deterministic request-aware and TPS simulations
hot-path and 4-MiB request benchmarks
```

Run source gates in an isolated temporary workbench on f563. Do not run any Go
test, race, simulation, build, image, or benchmark gate on local Windows, c21,
or the retired builder. Record the exact pushed commit, toolchain image digest,
commands, exit codes, benchmark distributions, and hashes of material logs.
Executable source evidence is invalidated by the next executable source change.

The lexical-estimator benchmark must cover representative small bodies, long
inputs, concurrent requests, and the accepted extreme body. Report parse plus
token estimate latency separately from total PIG overhead; p99 for the accepted
extreme input must be below 100 ms in the f563 test environment. A narrow
microbenchmark is an overhead gate, not a throughput claim.

Current exact-source evidence is tied to pushed commit
`d7089509e419814572c14f46c14903e649e2993c`, GitHub archive SHA-256
`5db6b003c4afc8c4f3fd13274908b4277ced4609e5faf05de8b85374814e7416`, and
`golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
On f563 it passed focused tests/race, serial and default-parallel full tests,
full race, vet, build, deterministic simulations, TPS simulations, Controller
hot-path benchmarks, and 4-MiB benchmarks. The first default-parallel full test
contained one `many_strings` p99 outlier at 312 ms; three isolated repeats were
approximately 29.7/44.2/29.3 ms, and both the serial and default-parallel full
test rerun passed without weakening the 100-ms gate. Classifier 4-MiB p50 was
approximately 15.1-15.5 ms and p99 33.8-49.7 ms across three runs. The
classifier benchmark was approximately 16.3-17.0 ms/op with 17 allocations and
38-42 KiB/op; estimator-only 4-MiB runs remained allocation-free.

These results establish the current source matrix only. They do not accept an
image or release: the SGLang per-tier reprocessed-hit contract above and the
three release reviews remain open. No v0.12.15 image may be built until that
metric blocker is resolved in source/tests, or the SGLang cache-credit feature
is conservatively disabled with corresponding red/green evidence.

Build and smoke one host-local candidate image only after the source matrix
passes. Validate it
against the existing SGLang with an isolated candidate PIG, then validate
shadow, default enforce, protection visibility, drain recovery, no low-flow
lock, runtime/OCI identity, and zero backend restart. Do not restart or rebuild
SGLang, HAProxy, or the CVM. Upload that exact image only after three review
passes accept it.

Candidate monitoring must run continuously for every source/image revision,
not only after the final deployment. Record at least admission outcomes and
reasons, predicted full/cold Prefill tokens, cache observation validity and hit
fraction, KV used/available/evictable plus reservations, running/waiting,
generation TPS, TPS-gate state, retractions/preemptions, upstream errors,
429/503, latency, completion throughput, PIG/backend restart counts, and GPU
utilization when available. Compare time-aligned baseline and candidate windows;
an isolated protection event or TPS dip is acceptable, but sustained QoS loss,
false lock, hidden protection, materially higher preemption, or lower
SLO-compliant goodput is not.

For each executable candidate, compare cache-hot, cache-cold, mixed short/long,
64K/256K/512K-class, decode-heavy, burst, cancellation, stale/reset, and
low-flow recovery windows. Evaluate aggregate completion throughput and
per-user TPS together; prompt throughput or high cache hit rate alone is not a
win. Source inspection of the exact f563 SGLang commit and the supported vLLM
source remains part of the metric contract gate whenever a selected metric or
aggregation rule changes.

After a production update, observe real traffic for at least 30 minutes and keep
the same monitors active. If evidence exposes a material defect or a clear
throughput opportunity, return to red test -> implementation -> isolated
candidate -> review -> image -> PIG-only update. Repeat until no obvious issue
remains; never optimize from a single low-TPS sample or a single cache-hit
snapshot.

## 5. Production Boundary

The v0.12.14 f563 observation continues as fail-closed evidence while the
correction is developed. Do not hot-patch the production binary.

Current state is drifted and must not be treated as a deployable Compose
baseline. The Phala control-plane Compose snapshot names PIG `0.8.13`, but the
actual running PIG container is still `0.12.14` with restart count zero and
startup time `2026-08-17T08:32:40Z`. The control-plane SGLang image is also
older than the running SGLang container. Therefore a control-plane deploy could
replace or restart SGLang and is forbidden for this PIG-only update. Before any
candidate promotion, establish and verify an exact host-side PIG-only
replacement/rollback procedure that preserves the running SGLang, HAProxy, CVM,
networks, volumes, and secrets. Compose text, container runtime, and endpoint
behavior must be checked independently.

Every production version update must use the verified f563 `0.8.13` deployment
as the stable traffic-bearing baseline while the new PIG is being replaced.
Before the first such transition, recover and verify the exact image and
configuration from live/committed deployment history: image digest,
environment, ports, TLS/auth, health checks, protected metrics, and
Router-visible semantics. Do not put v0.12.x-only variables into the old image
or invent an approximate legacy configuration. Drain only the PIG request path,
switch only PIG to the verified `0.8.13` configuration, and validate
authenticated models/chat/stream, metrics authorization, current Router
capacity, normal traffic, and zero backend restart before touching the new
candidate. If that baseline cannot carry traffic correctly, stop the update;
do not use the unverified candidate as the fallback.

The isolated v0.12.15 candidate must use separate name/port/network state and
must not displace the `0.8.13` traffic path. Only after all candidate gates and
three reviews pass may the production PIG service move from `0.8.13` to the
exact accepted v0.12.15 digest. A failed readiness or live-monitoring gate rolls
PIG back to the verified `0.8.13` configuration. Do not restart the CVM,
SGLang, HAProxy, or rebuild the model service.

The transition itself must remain PIG-only and drain-aware. Snapshot the route
and live Compose first, preserve the exact pre-change files and hashes, verify
that no backend/CVM restart occurred, and never equate Compose acceptance or a
running container with request-path readiness.

The mandatory production order is:

1. snapshot live Compose/hash, exact route state, PIG/backend counters, image
   identity, actual container configuration, and rollback artifacts; explicitly
   record any control-plane/runtime drift;
2. verify, drain to, and validate the exact `0.8.13` PIG configuration as the
   active traffic path without restarting SGLang, HAProxy, or the CVM;
3. keep the exact pushed v0.12.15 candidate isolated until all source, image,
   simulation, observability, shadow, enforce, and three-review gates pass;
4. promote only that accepted digest by replacing PIG, verify the complete
   authenticated request path and Router-visible capacity, then restore the
   exact pre-change route state if it was changed for draining;
5. observe real traffic continuously for at least 30 minutes, comparing cache,
   KV, TPS, queue, errors, preemption, per-user TPS, aggregate goodput, GPU use,
   and all restart counters;
6. on any release-blocking regression, restore the exact verified `0.8.13`
   image and configuration first, then begin a new red-test-to-release loop.

Do not build, upload, or debug the next image while production traffic depends
on a failed candidate. Do not modify Router source or policy as part of this
PIG release; route changes are limited to the drain/restore operations required
to make the PIG-only transition safe.

## 6. Review Record

Contract-document audit repeated after requirement consolidation on 2026-08-17:

1. requirement coverage pass completed for objective, prediction timing,
   approximate token estimation, adaptive initialization, request-aware
   long-input policy, cache/TPS semantics, both backend adapters,
   Router/admin-facing observability, minimal production configuration,
   environment, source/image publication, continuous monitoring, and rollback;
2. algorithm-consistency pass completed; Router visibility was corrected to be
   scope-aware so a large request-specific rejection does not close capacity
   for a smaller fitting request;
3. operational-order pass completed; every production update uses a verified
   `0.8.13` traffic baseline before the accepted candidate replaces PIG, with
   PIG-only rollback, explicit control-plane/runtime drift handling, and at
   least 30 minutes of live monitoring.

This document audit does not satisfy the three release-candidate review passes
below. Those remain tied to exact executable source and fresh f563 evidence.

### Pass 1: metric model and causality

Status: in progress. SGLang source and the f563 live scrape established the
one-sided KV invariant and selected scheduler-interval decode counter. The live
SGLang scrape also exposes `prefill_effective_tokens_total` as token-level
counters with the required input/device/host/storage modes. vLLM upstream source
established the metric types, per-engine labels, and update sites. The current
PIG source matrix is green, but SGLang per-tier reprocessed-hit semantics remain
a release blocker, so this review is not complete.

Exact-version source was resolved to SGLang commit
`c4271c3fe1262fc2adbd162c33b25de5255251c5`. Its
`metrics_collector.py` declares and pre-seeds the four effective Prefill counter
modes; `metrics_reporter.py` publishes effective input as logged input minus
reprocessed input. That reporter derives aggregate cache hit rate after
excluding reprocessed input, but directly forwards the three cache-tier
counters; their accumulation path still must prove that reprocessed tier hits
are excluded. The f563 scrape confirms only TP0 carries non-zero cumulative
values while the other TP ranks expose pre-seeded zero views, requiring maximum
rather than sum aggregation per mode.

vLLM source commit `5fd7a888386cff800f32de6b5a33d1dd3ca1e397`
declares `prefix_cache_queries` and `prefix_cache_hits` as per-engine counters
with `model_name` and `engine` labels; Prometheus exports their `_total` names.
The current PIG vLLM cache parser contract matches that declaration and remains
separate from the SGLang rank/priority contract.

Focused red source `b0d4ab2` was pushed and executed only in the f563 isolated
workbench. The expected parser, Controller cache-cost, vLLM TYPE, and SGLang
KV-gap assertions failed for their intended behavioral reasons. Evidence:

```text
/var/volatile/dstack/persistent/pig-v01215-workbench/red-b0d4ab2.log
sha256 c65474696392d9223d0e38aebf92dc743d65b318ef8608560994bb58af5e1f36
exit 1 (expected red)
```

Historical implementation `4f2bcbf` passed focused packages but failed the
existing 4-MiB classifier latency gate:

```text
/var/volatile/dstack/persistent/pig-v01215-workbench/green-4f2bcbf/focused-4f2bcbf.log
sha256 30ab88d1f8bf855d36d247f3b29a4000b915c9254be7bbcdfce6c90cd5270c70
exit 0

/var/volatile/dstack/persistent/pig-v01215-workbench/green-4f2bcbf/full-test-4f2bcbf.log
sha256 2b7e62fe8bc545ff5530bcdbe12adc5bc4c70fc0ca3fbaf1abf194016fd24810
exit 1: body_bytes=4194300 p50=34.280061ms p99=107.131729ms
```

Current pushed source `d7089509e419814572c14f46c14903e649e2993c`
supersedes that performance failure without weakening the gate. It also adds
the required cache no-delta, expiry, runtime-epoch, and current-sample
preemption-suppression coverage. Its exact f563 matrix passed with these
material evidence hashes:

```text
focused             674ff6a57975c6f5a6365c1cc623c7090087deffaef8b044473d1ab05eb93543
focused race        92d55c65b3f62cefde8f87e79855c020d5962ba63334ad5352ab060094bbe150
full serial         819b56e0d17408b5befeeaae86cc009392583bcafd24015355f8e1a7d3e561da
full parallel retry f07f4f71697ed03645f26bd21a67fb1e30a011a7f2bb14f18602393b659408de
full race           08cad4dcf33db0bd7577b5f9387c0d3149c0cf5ad00af7acc2cf521316e28dd4
vet                 a6d5a1141a212e9ee49cbf33cba28de14f6e826718cebd0a48a7292765985608
build               dca46ce5dcda3aaac3e979fefb1d9ef4f8add96f5be7c376b8d2fdeb810c88d7
classifier latency  30fad4bdc327299cf78bfe0210413b4a8fb53511774b645b9f8650da516c551c
4-MiB benchmark     565581a0b21010aa262ba3bd6f653376b0283ae0e3a1523841349b13be71d948
hot path            58ef284226f213963c64dd4f868548c336da44141f74c7b1190b57f21bee93e5
TPS simulations     c5d926c3c4dd6a4bc128b4709312f171e9e47208072b20a77b0c280e47180a99
deterministic JSON  2f29cb429523018c4f68f01fea03179e219b8d0919e32e97140450b6fced30e1
```

This green matrix is not image acceptance. Pass 1 remains open until the
SGLang tier-counter contract is proven or cache credit is disabled for SGLang
with focused tests.

### Pass 2: safety and lifecycle

Status: pending. This pass must cover reservation/release/cancel/reset races,
low-flow and transition recovery, cache observation bounds, current-capacity
observability, request-path efficiency, removal of dead legacy behavior, and
SOLID ownership boundaries.

### Pass 3: exact evidence and release

Status: pending. No v0.12.15 image has been built or uploaded, production still
runs the accepted v0.12.14 image, and the exact f563 `0.8.13` rollback
configuration has not yet been reconstructed and revalidated. The control-plane
Compose currently names `0.8.13` while the runtime still runs `0.12.14`; this is
configuration drift, not evidence that the fallback is active.

Repository deployment evidence identifies the former f563 baseline image as
`ghcr.io/phala-network/phala-inference-guard:v0.8.13@sha256:aec805d6e7bbfd82375199d7950ecfbf6148e501c64822dcb46102a9e24a2ea4`
with the DeepSeek-v4 SGLang backend, 500 ms dynamic polling, global limit 15,
zero queue wait, dynamic TPS red/yellow thresholds, and TTFT disabled. This is
historical reconstruction only. The current control-plane snapshot instead
contains `GLOBAL_LIMIT=20`, so neither value may be guessed into production.
Live deployment history, exact container configuration, PIG-only replacement,
authenticated endpoint behavior, metrics, and Router-visible capacity must be
revalidated before one exact configuration can become the traffic-bearing
update fallback.
