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

The following list is the authoritative consolidation of the current user
contract. It applies to every v0.12.15 source revision, candidate image, and
production transition. Earlier plans, experiments, or evidence that conflict
with this list are historical only and must not be carried forward after a
context reset:

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
- Backend-source review is part of the metric contract, not permission to patch
  the backend. Pin the exact deployed SGLang source and trace the scheduler
  update/reset paths for KV, cache tiers, running/waiting, decode TPS, and
  retraction before using those signals for QoS. Apply the same source-level
  verification to the supported vLLM metric families and aggregation rules.
  Modify PIG only; SGLang, vLLM, Router, and HAProxy remain read-only protocol
  references for this release.
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
- If GitHub authentication or repository permission blocks a required push,
  start the GitHub device-authorization flow and give the user its verification
  URL/code for approval, then resume the same push after authorization. Do not
  bypass the required push, expose credentials, move executable tests to local
  Windows or the retired builder, or treat an unpushed commit as image input.
- Treat every executable version as a measured iteration. Keep the required
  monitors active throughout isolated candidate testing, image validation,
  promotion, and the post-promotion window; compare time-aligned baseline and
  candidate evidence. A material QoS, correctness, visibility, or goodput
  regression starts a new red-test-to-release iteration rather than an in-place
  production patch.
- Before every production candidate replacement, make the separately verified
  exact `0.8.13` image and configuration the traffic-bearing PIG baseline. Do
  not promote directly from a previous v0.12.x runtime, do not mix v0.12.x
  defaults into `0.8.13`, and do not use an unverified candidate as the service
  fallback. The candidate may replace only PIG after its complete acceptance;
  failure restores that same verified `0.8.13` baseline first.

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

For SGLang, exact source commit
`c4271c3fe1262fc2adbd162c33b25de5255251c5` proves a first-attempt contract for
all four modes. `Req.reset_for_retract` permanently sets `retracted_stain`.
`PrefillAdder._update_prefill_budget` always adds raw input/hit tokens, then
either adds them to the reprocessed counters when `retracted_stain` is true or,
in the mutually exclusive `elif`, splits and adds first-attempt
`device_hit`/`host_hit`/`storage_hit` tokens. `metrics_reporter` subtracts the
reprocessed input/hit totals and forwards the already first-attempt-only tier
counters. The collector's own documentation declares the same exclusion.
Therefore reprocessed/retracted tokens do not enter any effective mode; this is
source-path proof rather than an inference from aggregate `cache_hit_rate`.
The recent hit fraction is:

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

Decode activity alone must not discard useful cache evidence before applying
the existing contended Prefill token budget. A `weighted` request may therefore
enter under Decode contention only when its bounded cache-aware Prefill compute
cost, without changing its class, fits the same contended budget as regular
work. Backend waiting or a current preemption/retraction delta still blocks that
weighted admission, and `exclusive` or `quiescent` work remains blocked while
Decode is active. KV fit and reservation always charge the complete estimated
input. This opens at most the capacity already represented by the atomic
Prefill and cache-credit reservations; it does not add a new threshold or
learned state.

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
- an optional SGLang cache family from a different `dp_rank` falls back cold
  without invalidating the otherwise coherent primary admission sample;
- first sample, missing metrics, low evidence, stale observation, counter reset,
  backend epoch change, and invalid ratios produce zero cache credit;
- a qualified cache observation is never transferable indefinitely across
  unrelated work: its age and finite token budget are both enforced, a
  650K-class hot sample followed by several cold requests cannot over-discount
  aggregate Prefill, and a current preemption/retraction delta suppresses it
  immediately without creating a later cooldown; the final lifetime must be
  justified against the 500 ms cadence rather than inherited as 10 seconds;
- a cache observation can reduce only aggregate Prefill compute cost while KV
  fit, KV reservation, maximum input, and long-input class remain byte-for-byte
  unchanged;
- exclusive and quiescent requests cannot be downgraded by a high recent global
  cache hit rate;
- under Decode contention, a cache-aware weighted request can enter only when
  its predicted Prefill compute fits the atomic contended budget; a cold
  weighted request, waiting/preemption pressure, and every exclusive or
  quiescent request remain protected;
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

Corrective request-shape, TPS, and estimator tests required after the
2026-08-18 review:

- the normalized request estimate explicitly carries Decode sequence
  multiplicity; top-level `n`, `best_of`, string-prompt batches, and token-id
  prompt batches either produce a complete bounded aggregate estimate or an
  explicit request-scoped unsupported result before forwarding;
- per-sequence Context fit remains separate from aggregate Prefill, KV, and TPS
  demand; multiplicity cannot be hidden by treating one HTTP request as one
  scheduler sequence, and overflow fails closed without a reservation;
- one multi-sequence admission atomically reserves all predicted Decode KV and
  sequence demand; cancellation, terminal release, observation reconciliation,
  and runtime reset release it once without leaking or double releasing;
- decision logs, status, and metrics expose the request Decode sequence count
  and agree with TPS post-admit demand for both admitted and protected requests;
- a ready 60-second TPS window remains the primary capacity estimate when the
  latest 500 ms observation is valid; one ordinary low sample cannot overwrite
  long-window headroom, while current waiting or preemption still freezes new
  admission immediately;
- atomic reservations prevent repeated requests in one poll from exceeding the
  long-window sequence limit, and a newly healthy current sample can recover at
  most one bounded step when old long-window evidence remains degraded;
- reference-50 sparse, saturated, low-sample, counter-jitter, recovery, burst,
  and multiplicity simulations preserve long-run mean active TPS and improve or
  retain SLO-compliant completion goodput without low-flow self-lock;
- the model-neutral estimator oracle includes representative byte-BPE,
  SentencePiece, multilingual, emoji/rare-Unicode, escape-heavy, tool-schema,
  chat-template, and multimodal shapes. Lexical Prefill ranking and conservative
  KV/Context safety are evaluated separately; Gemma4-only evidence cannot select
  a universal hard-capacity margin;
- remote multimodal content whose expansion cannot be bounded from the JSON
  body is explicitly represented as low-confidence or unsupported for hard
  admission instead of being reported as a cross-model reliable estimate;
- a 512K/650K hot observation followed in the same and later observations by
  multiple 64K/128K cold requests cannot spend more cache credit than current,
  age-bounded evidence justifies, and the cold transition does not bypass long
  input, aggregate Prefill, KV, or TPS gates;
- valid chunked or HTTP/2 JSON with unknown `Content-Length` uses the existing
  bounded reader and is classified up to the configured maximum; only the
  first byte beyond the maximum changes it to `body_too_large`;
- several concurrent short Decode requests that start and finish between polls
  cannot be represented as one sequence-second when doing so would overstate
  per-user TPS.

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

Earlier exact-source performance evidence is tied to pushed commit
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

These results establish the `d708950` source matrix only. They do not accept an
image or release. Exact SGLang source tracing has now closed the per-tier
reprocessed-hit question, but review exposed an untested cross-DP cache-family
identity case. Test-only commit
`397d234426e881e5006625b970d3d12237bd6f62` proved that the current parser
accepted a DP1 cache family alongside coherent DP0 admission metrics. The
first-attempt/retraction fixture passed in the same run. Fix commit
`49de08600feb5a5c043597527b8602baa7254514` now carries the primary admission
`dp_rank` into the optional cache adapter and falls that cache family cold when
its `dp_rank` differs, without invalidating coherent KV/TPS admission fields.
Its GitHub archive SHA-256 is
`aff29a6691a252fa95e3dae861724b267aa23d35e7ced93c5f7cd30acbf0bbcb` and its
focused contract-green log SHA-256 is
`53d0bb81f242a749fb5bd8323f491a67c92dbfd4bc5069d84bd96150e90b1319`.

```text
GitHub archive sha256 d1f18692e35e34699cc235901e00733d5bd21a407a8ac951c905aebd11215e0f
focused red log sha256 0b253350d19eeb154e919e9f62df2223d3114b203b9ac070a64b192308f4756c
focused red exit 1: cross-DP cache family accepted
harness error sha256 0ae446f63ec8d1e71531efbc73ffb2ab1d7a47f851634c3967d9c399d27e03fc
harness error exit 127: `sh -lc` reset Go image PATH; not red evidence
```

The exact `49de086` f563 core matrix passed focused tests, focused race, serial
and default-parallel full tests, full race, vet, build, and two byte-identical
deterministic simulations:

```text
focused             a17bd28cd2b445f997d22fab66b5f95e433bf232fa963cc66d420127a55e8bd2
focused race        67c187fb4d21b484ccf218da1067e11ba03e60970d1c2f913fe74ab00f6baf81
full serial         5ea517b984449b4ce6f05ebcba019a71e021f45d7e3f33e0a82acd6f8045b807
full parallel       8f79a3d797c8316293e11e7eed0ad6cad6545b2bb869719de558cd994d94b333
full race           032f20439a945875d4ca94aa5e18375f1e8bbd9c49384bb38f11644b970e79f1
vet                 f4e5ca988b9e9fa4fd82883c14bed636e82d12a09e66c12c861c95791707b852
build               e21fcd21234752a48e2170171fd36a6d68922eba13544edbcc63da3e918f4784
simulation A/B      2f29cb429523018c4f68f01fea03179e219b8d0919e32e97140450b6fced30e1
```

The targeted TPS-reference and Controller hot-path/TPS runs, three-repeat
classifier and estimator latency gates, three-repeat 4-MiB benchmarks, complete
artifact audit, and all three release-candidate reviews remain pending. No
v0.12.15 image may be built until the source matrix and reviews are complete.

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
   exact SGLang/vLLM source contracts, Router/admin-facing observability,
   minimal production configuration, environment, source/image publication,
   GitHub authorization recovery, continuous per-version monitoring, and
   rollback. Conflicting historical
   requirements such as exact/model-specific tokenizers, learning, TTFT gating,
   request mutation, PIG routing, or tier/priority injection remain excluded;
2. algorithm-consistency pass completed. Cache credit is limited to recent
   aggregate Prefill compute while complete input still drives KV and long-input
   safety; TPS 50 is a long-window QoS reference rather than an instantaneous
   hard floor; the 500-ms observer has no hidden one-second hold; and Router
   visibility remains scope-aware so a large request-specific rejection does
   not close capacity for a smaller fitting request;
3. operational-order pass completed. Every executable version is committed and
   pushed, tested only on f563, monitored as an isolated candidate, and uploaded
   only after acceptance. Every production update first uses a separately
   verified exact `0.8.13` configuration as the traffic-bearing PIG baseline,
   then replaces only PIG with the accepted digest, preserves SGLang/HAProxy/CVM,
   observes real traffic for at least 30 minutes, and restores `0.8.13` before a
   new iteration if a release-blocking regression appears.

This document audit does not satisfy the three release-candidate review passes
below. Those remain tied to exact executable source and fresh f563 evidence.

### Pass 1: metric model and causality

Status: in progress. SGLang source and the f563 live scrape established the
one-sided KV invariant and selected scheduler-interval decode counter. The live
SGLang scrape also exposes `prefill_effective_tokens_total` as token-level
counters with the required input/device/host/storage modes. vLLM upstream source
established the metric types, per-engine labels, and update sites. Exact SGLang
source now proves that all effective modes exclude reprocessed requests. The
`397d234` red run proved that a cross-DP optional cache family was accepted;
`49de086` fixed the identity binding and passed the core exact-commit f563
matrix. The remaining TPS/performance repeats, complete evidence audit, and
causality review are still pending, so this pass is not complete.

Exact-version source was resolved to SGLang commit
`c4271c3fe1262fc2adbd162c33b25de5255251c5`. Its
`metrics_collector.py` declares and pre-seeds the four effective Prefill counter
modes. `schedule_batch.py` makes `retracted_stain` persistent;
`schedule_policy.py` sends reprocessed input/hit tokens to the subtraction
counters and sends only the mutually exclusive first-attempt branch to the
three tier counters; `metrics_reporter.py` subtracts reprocessed input/hit and
forwards those tier counters. The f563 scrape confirms only TP0 carries non-zero
cumulative values while the other TP ranks expose pre-seeded zero views,
requiring maximum rather than sum aggregation per mode.

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

Earlier pushed source `d7089509e419814572c14f46c14903e649e2993c`
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

This historical `d708950` green matrix is not image acceptance. The newer
`49de086` cross-DP contract fixture is green and its core matrix is recorded in
Section 4, but Pass 1 remains open until the remaining exact-source gates and
evidence review complete.

The 2026-08-17 continuation then reviewed request-path memory bounds and the
causal use of cache/TPS evidence. Three defects were converted to isolated f563
red tests before implementation:

- `8d605e3` proved that the default 64-entry 4-MiB request-body pool could retain
  256 MiB while idle (`feb4477ce36aaa39140afcfcbecee97b3674ddb5223f529664fa637543df150f`);
  `8c29647` bounds idle pooled body capacity to 32 MiB without reducing the 64
  concurrent scanner slots;
- `696b54e` proved that one cache observation could be charged repeatedly to
  same-snapshot admissions (`3053d724f2491f032a27ddb6dab327fd8db7337b465ffdc8ee6d5f77e68f9f90`);
  `ff1c295` reserves complete pending Prefill input and atomically limits cache
  credit to the recent evidence-derived token budget;
- `5531c1f` proved that historical TPS headroom could be spent repeatedly while
  waiting, preemption, or unobserved same-poll demand already existed
  (`d1752529419ac18dcf538020b47ee30f348ddf5203fd749aaa48607abdf17b38`);
  `c6a0fad` freezes current pressure and limits a qualified current poll to one
  reservation-backed expansion wave.

The first exact TPS rerun at `90b44d6` exposed a separate throughput defect in
the reference-25 saturation scenario: four existing 30-TPS Decode sequences
could not admit a fifth sequence even though its projected 24 TPS remained
inside the documented five-percent tolerance. Focused red commit `b947031`
reported `sequenceLimit=4` instead of 5; its f563 log SHA-256 is
`71acab8a8696d0948cdb71900fb9b576c95353ec75ee47e451d86cf211b3a0e0`.
Executable fix `e3ae3d0c1b9b137c782170a9dc5b822aedbf7059` supplies the current
per-running-sequence mean to the existing bounded exploration calculation while
preserving waiting/preemption freezes, unobserved reservations, and the one-wave
limit. Exact-commit f563 targeted evidence is:

```text
TPS focused             26f360303fd54739c234601697269e0858220785ccba51fc84c71ef0351a8900
TPS/cache simulations   c9a74c74e4b71ce5f869791c8598a1b2f8d70b8662f8b116339b0143e36e88f9
```

For reference 25, the corrected candidate admitted two bounded additions,
produced 8907.78 completion tokens versus the unprotected 8901.11 baseline,
held long-run mean active TPS at 25.20, and produced zero preemptions. Reference
20 retained 8906.67 completion tokens and 21.75 mean active TPS. Cache hot,
cold-transition, and mixed scenarios retained their 5-to-7 admission and
SLO-goodput improvements without preemption or TPS-floor time. These targeted
results reject both the over-protective pre-fix behavior and restoration of
same-poll historical bursting. Pass 1 remains in progress until the complete
exact-executable source matrix and artifact audit pass.

The 2026-08-18 review found that `prefillGate.evaluate` rejected every weighted
request as soon as any Decode sequence existed, before its already bounded
cache-aware compute cost could be compared with the contended token budget.
That made recent cache evidence causally ineffective for 64K-256K work on a
continuously busy backend. Focused red commit `553c318` held KV geometry,
running Decode load, and cache evidence constant and failed because both cold
and cache-aware runs admitted only one of two requests. Its f563 log SHA-256 is
`7dea32a2166be8c0d8aa536c449a60abc3b2928982cdf9641b44c82e5b8ce7f4`.

Executable fix `fea7eb3` allows regular and weighted work to reach the same
atomic contended Prefill budget while preserving waiting/preemption freezes and
the exclusive/quiescent ownership rules. Test commits `dc2bf10` and `38466b1`
lock the complete-KV/class invariant and a hot-to-cold workload transition.
Exact archive SHA-256 for `38466b1` is
`93e0a792b7b204916a72f799695ffe4c24ee4dbcd18277a73f328d33daa3fc2f`.
Its f563 targeted log SHA-256 is
`2acd542d052da1532c1e8fe30d73763e31844cdefd752f627644c821f46cf339`.
The cache-aware steady case improved admissions from 1/2 to 2/2 and
SLO-completion tokens from 1441 to 1505 with mean active TPS 29.92 and zero
preemptions. The adversarial hot-to-cold case admitted 4/4 versus the cold
baseline's 3/4, retained mean active TPS 29.41 against reference 25, retained
approximately 97 percent of baseline SLO-completion tokens, and produced zero
preemptions; its one bounded TPS-floor excursion lasted 5.4 seconds over the
180-second run. This is accepted as an occasional transition dip, not a
sustained TPS failure.

### Pass 2: safety and lifecycle

Status: in progress. Controller and HTTP lifecycle review confirmed atomic
reserved/forwarded-prefill/active-decode/residual-debt transitions, pre-forward
cache-credit refund, old-lease isolation, next-sample reconciliation, runtime
epoch reset, terminal-cause coverage, and bounded reservation count. Commit
`c83d3d8` removed the dead `v0.9.0` builder script and its unreferenced legacy
KV-simulation scenarios, then extended `verify-no-legacy-mode.sh` so those
paths cannot return. Exact `c83d3d8` f563 legacy audit passed with log SHA-256
`455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0`.

The cache-credit lease fix at `d7bd22a` also passed its exact targeted focused,
race, simulation, and formatting gates on f563:

```text
cache lease focused       e0071bf51e31ba2a643d17108ab53ca922c004094c238d14f2821784320ce866
cache lease focused race  aedf9286b9f94b3e8ae5abefcc18b888a535b78692f930b328a7b6fbb18cf6c2
cache/TPS simulations     17e988e41df30dff866fd05d7ad953e9bab1eddbcc0d62949a92f99ff17dcc4f
gofmt                     fd6c987ec7e755c1df6bfb551a9259ea12b634b676dc5f8dca3be9ad635e2310
```

The final exact-source full tests, full race, vet, build, legacy audit,
deterministic simulations, performance gates, and bounded-state evidence remain
pending; therefore Pass 2 is not complete.

### Corrective review: request shape and long-window QoS

Status: red design review recorded on 2026-08-18. This section supersedes any
earlier targeted acceptance that conflicts with it. Current source commit
`79ec93c9c09a986691bb62ea0448d666e4f587fd` is not a release candidate.

The review found three release blockers:

1. `RequestEstimate`, reservation overlays, and TPS projection account every
   HTTP request as one Decode sequence. `n` and batched completion prompts can
   therefore under-reserve future KV and post-admit TPS demand until a later
   backend observation arrives.
2. Once the TPS window is ready and the current interval is valid,
   `tpsGate.evaluate` replaces the 60-second rate-derived sequence limit with the
   last 500 ms running/current-rate result. This makes the long window mostly a
   readiness latch and contradicts the long-run reference contract.
3. The runtime lexical estimator is model-independent, but the fixed `9/8` KV
   margin was selected only by Gemma4 exact-token fixtures. That evidence does
   not bound byte-fallback Unicode, other tokenizer/template families, or remote
   multimodal expansion.

The same review recorded four additional gaps: cache credit can remain fully
effective for 20 default polls; automatic Prefill initialization is fixed
64K/256K/512K policy with geometry clipping rather than measured Prefill
capability; valid unknown-length JSON is rejected before the already-bounded
reader; and completion-between-polls TPS accounting falls back to one active
sequence even when several concurrent short requests may have completed.

Execution resumes test-first from these findings. Required order is:

1. add focused red request-shape/multiplicity and TPS long-window tests and run
   them only in the isolated f563 workbench at the exact pushed red commit;
2. implement one normalized request-shape contract through classifier,
   estimator, `RequestWork`, policy, reservation lifecycle, logs, and metrics;
3. restore long-window TPS capacity as the primary limit while retaining atomic
   same-snapshot accounting, immediate waiting/preemption protection, idle
   probing, and bounded current-rate recovery;
4. add cross-tokenizer/low-confidence oracle coverage, cache age/burst tests,
   unknown-length body support, and short-request sequence-time accounting;
5. repeat model/causality and safety/lifecycle reviews, then run the complete
   exact-source f563 matrix. No image build or upload is permitted before all
   corrective tests, simulations, benchmarks, and three reviews pass.

Focused red source `232350e` was pushed and executed only in the isolated f563
workbench from its GitHub archive. The archive SHA-256 was
`a38fc5356d719e49abc9d0413ff0997342ac70a24e03ece83eab63ad13b818ed`;
the pinned Go toolchain resolved to image ID
`sha256:e0cffc405270b9114fac7706d07c373727d1b42b0e47c525b9cd1ab1097779ff`.
The focused command was:

```text
go test ./internal/admission ./internal/app/request ./internal/app/server -run TestV01215 -count=1
```

It exited 1 with log SHA-256
`8432381918744832ef9ab98b4a29b31ba17ede6f0801bfbfbda28ed307c69e4b`.
The failures were the intended behavioral assertions: `n`/`best_of`/prompt
batches all produced one post-admit sequence and one Decode horizon; a ready
long-window limit of six was replaced by the current count of three; and valid
unknown-length JSON returned `unknown_body_length`. All packages compiled and
dependencies resolved, so this is valid red evidence. No service container,
Compose file, backend, route, or production request was changed.

### Corrective review: backend request semantics and low-flow exploration

Status: design correction recorded on 2026-08-18 after exact-source review.
This section supersedes the earlier assumption that `best_of` is a scheduler
sequence multiplier. Source after `9e66cc3` remains dirty and unvalidated; no
result in this section is green evidence.

The exact f563 SGLang commit
`c4271c3fe1262fc2adbd162c33b25de5255251c5` establishes the following request
contract:

- completion `best_of` is present in the request schema but
  `OpenAIServingCompletions._build_sampling_params` does not forward it to the
  scheduler; it creates no additional request or Decode sequence;
- `GenerateReqInput` expands a base batch to `batch_size * n` distinct request
  IDs and the scheduler therefore exposes those children as independent
  running/waiting sequences;
- before expanding `n > 1`, `TokenizerManager._handle_batch_request` sends one
  `max_new_tokens=0` request per base prompt and waits for it to complete so the
  common prefix is cached. SGLang Prefill/input KV is therefore charged once
  per base prompt while Decode horizon and TPS demand are charged once per
  child sequence.

The supported vLLM source commit
`5fd7a888386cff800f32de6b5a33d1dd3ca1e397` likewise has no `best_of` field in
its completion request schema. `ParentRequest` expands `n` into child engine
requests with `n=1`, so running/waiting and future Decode demand must count all
children. Unlike SGLang, this source does not prove an explicit pre-cache pass;
the implementation must not invent a shared-Prefill guarantee for vLLM. This
backend difference must be isolated behind a capability or adapter contract if
it becomes material to Prefill accounting; it must not be guessed from model
identity or cache-hit rate.

The corrected normalized request-shape contract is:

```text
base_prompt_count = one for chat/responses, completion prompt batch size otherwise
decode_sequences = base_prompt_count * n
best_of = no admission multiplicity for the supported backend contracts
unique_input_work = aggregate input of the base prompts
future_decode_work = per-sequence bounded Decode horizon * decode_sequences
context_fit = maximum one-base-prompt input + one Decode horizon
```

This requires a separate maximum-per-prompt context estimate. Reusing aggregate
batch input for context fit is conservative but can reject a valid prompt batch
whose individual prompts fit, reducing throughput without protecting the
backend. Aggregate base-prompt input continues to drive Prefill contention and
unique input KV reservation.

The TPS review also found a low-flow self-lock in the current exploration
formula. With one healthy sequence at 60 TPS and a 50 TPS reference, testing a
second sequence as `60 / 2` assumes aggregate throughput cannot grow and blocks
the only observation that could establish higher capacity. The corrected rule
keeps the 60-second capacity as the primary mature limit, but a coherent current
sample with no waiting/preemption and mean active TPS at least 105% of the
reference may open exactly one additional sequence wave. Atomic reservations
consume that wave until the next 500-ms poll. The next observation either keeps
or closes the added capacity; no cooldown or learned rate is introduced.

Before the next executable commit, focused tests must prove:

- `prompt_batch * n` is charged through HTTP decision, KV, TPS, lifecycle,
  logs, and metrics, while `best_of` does not fabricate scheduler demand;
- a batch can pass context fit when every base prompt fits even if aggregate
  input exceeds one model context, while aggregate Prefill/KV work is retained;
- single-sequence marginal block accounting does not regress when multi-sequence
  future KV is added;
- a healthy one-sequence low-flow state can probe a second sequence, the same
  poll cannot spend that probe twice, and waiting/preemption still freezes it;
- after one metrics poll, every active multi-sequence reservation remains fully
  represented by pending/local demand even when the backend has materialized
  only part of the children. Any later attempt at partial observation credit
  requires an explicit union-accounting proof rather than a boolean shortcut.

### Corrective review: per-sequence KV block accounting

Status: new red review recorded on 2026-08-18. Exact pushed source `1deaa5f`
passed the request-shape focused matrix on f563, with archive SHA-256
`b72765fdfd6c9088de983ec51b68cae8820feeb60a68ca1e8f6e764a021168a3`
and focused log SHA-256
`1d33ac577f5dfb1d816a2b5fe095dbaa3bcb2cb5ad85571e9fb207e865c41025`.
That green result closes only the aggregate-versus-maximum Context estimate;
it does not make `1deaa5f` a release candidate.

Static review then found that `BuildRequestWork` rounds aggregate input KV once
for an entire completion prompt batch. Backend KV allocators own blocks per
sequence, so unused tails from different base prompts cannot be pooled. The
same defect exists in future Decode accounting: one marginal Decode tail plus
full blocks for the remaining children is safe for children of one shared base
prompt, but is not an upper bound for several base prompts with different tail
positions. Two 63-token base prompts with a two-token horizon and a 64-token
block each need two new Decode blocks, while the aggregate calculation reserves
only one.

The normalized contract must therefore retain all three counts separately:

```text
base_prompt_count = number of independent prompt sequences before n fan-out
decode_sequences = base_prompt_count * n
input block rounding = per base prompt, never pooled across prompt sequences
future Decode rounding = marginal per independent base prompt plus fan-out work
```

SGLang's exact source proves one pre-cache request per base prompt before `n`
children are expanded. vLLM expands all children and can reuse prefix-cache
blocks scheduled earlier in the same scheduler pass, but the pinned source does
not prove that every long/chunked child shares the complete prompt. PIG must
therefore isolate this difference in a backend execution capability: SGLang may
charge unique input per base prompt; vLLM must conservatively charge child input
until an exact source test or runtime oracle proves a tighter upper bound. This
is a backend protocol property, not a model-specific setting or learned value.

Required red/green evidence:

- two one-token base prompts consume two input blocks, not one aggregate block;
- two base prompts crossing a Decode block boundary reserve both marginal
  blocks;
- a single base prompt preserves the existing marginal-block optimization;
- SGLang `n` fan-out and vLLM conservative fan-out use explicit backend
  execution profiles without model-name checks or production configuration;
- overflow, validation, lifecycle, log, metric, simulation, race, and hot-path
  coverage include `base_prompt_count`.

### Corrective review: block upper bounds and lifecycle coverage

Status: second red design review recorded on 2026-08-18. This section
supersedes the earlier requirements to preserve marginal future-block
accounting and to charge all SGLang input KV exactly once per base prompt. The
implementation after pushed red source `1ceb14c` is dirty and has no executable
evidence.

The review found four additional release blockers:

1. `KVReservationInputTokens` is an approximate upper bound, not the exact
   input position within a KV block. The incremental function
   `ceil((input + decode) / block) - ceil(input / block)` is not monotonic in
   `input`, so evaluating it at the approximate upper bound is not an upper
   bound for every possible real input. Once reconciliation marks input as
   covered, any block slack embedded in the input reservation disappears.
   `FutureKVTokens` must therefore reserve a complete block-rounded Decode
   horizon per Decode sequence until exact input/block-position evidence exists.
2. SGLang's explicit pre-cache pass shares only page-aligned Radix Cache
   prefix pages. Its insert and match paths truncate the cached key to a page
   boundary; every expanded child may recompute and allocate the unaligned
   input tail. SGLang may charge the aligned prefix once per base prompt, but
   child-tail Prefill and KV liabilities must remain represented separately.
3. The first response byte proves only that at least one child produced output.
   It does not prove that every child of a streaming `n > 1` or prompt batch has
   materialized its input. One HTTP-level `MarkFirstByte` must not release all
   Prefill and input-KV liabilities. Backend execution profiles must define
   Prefill fan-out, KV sharing, and first-byte coverage independently.
4. `RequestEstimate.Validate` accepts an aggregate that cannot be distributed
   across `base_prompt_count` sequences under the supplied maximum. The
   block-rounding helper then silently caps the aggregate by
   `maximum_blocks * sequences`, which can return fewer KV tokens than the
   accepted aggregate. Inconsistent aggregate/maximum pairs must fail before
   work construction.

A third review of the red design found a fifth blocker: the current tokenizer
produces conservative approximate token upper bounds, not exact token counts or
an exact KV-block phase. An estimate equal to the SGLang page size therefore
does not prove that the real request is page aligned; the real count may be one
token lower. The initial implementation must reserve the possible extra child
tail page at every approximate boundary. A page-alignment discount is allowed
only if a future estimator supplies explicit, validated exact-block-position
evidence; backend kind or an approximate integer is not such evidence.

The first focused implementation run also exposed two pre-existing stale TPS
test assertions at red commit `e3c96ae`: they failed before the backend-work
implementation. The policy intentionally permits one 5-percent-bounded
exploration step when healthy mean-active TPS has headroom, and it refills an
idle backend up to the rate-derived sequence limit when the just-finished
metrics interval is qualified. Tests that required limits of six instead of
seven in the 20-TPS/15-reference case, or one instead of four immediately
after a healthy four-sequence interval, contradicted those rules and recreated
the low-flow underfill behavior this plan rejects. Their corrected contract is
to assert the computed limit and that same-snapshot reservations cannot exceed
it; this does not weaken the waiting or preemption stop conditions.

The corrected minimal work contract is:

```text
RequestEstimate
  aggregate input and KV upper bounds
  maximum one-base-prompt input and KV upper bounds
  base prompt count and Decode sequence count

BackendExecutionProfile
  Prefill fan-out
  page-aligned input-KV sharing and child-tail liability
  first-byte input-coverage semantics

RequestWork
  Prefill compute upper bound
  input KV that a coherent observation may cover
  unmaterialized input KV that must survive first-byte reconciliation
  complete block-rounded future Decode KV per sequence
```

The first safe implementation must not add runtime learning, model-name checks,
production configuration, or request-specific cache lookup. SGLang uses its
source-proven aligned-prefix sharing plus conservative child-tail liability;
vLLM keeps conservative child input until exact source/runtime evidence proves
a tighter lifecycle. A later optimization may reduce at most one future block
per sequence only if it has exact block-position evidence and a reconciliation
proof.

Required new red/green evidence:

- an approximate input upper bound at a different block phase cannot reduce
  future Decode reservation below one complete per-sequence horizon;
- SGLang prompts with `n > 1` retain every possible child-tail block through
  first-byte and the next metrics publication, including when the approximate
  upper bound happens to equal a page boundary;
- one vLLM child producing the first response byte cannot cover the other
  children's input or Prefill liability;
- aggregate input or KV above `maximum * base_prompt_count` is rejected with
  overflow-safe validation;
- Prefill class and aggregate budget consume explicitly named work dimensions,
  with a focused decision-path test for backend-expanded work;
- invalid/overflow fixtures reach their intended branch rather than failing on
  unrelated zero-valued shape fields.

### Corrective review: request-time evidence and workload shifts

Status: implementation corrections and follow-up design review recorded on
2026-08-18. Pushed source `3fde5351d94d33f8add992ceb13fe26da3d61dbd`
closed the initial two focused defects. Subsequent pushed source through
`1f9a53accd837e040f16a6fe9aab730b59125b22` closes the conservative
multi-prompt shape, between-poll sequence evidence, and known-below-floor
marginal recovery defects described below, but is not a release candidate.

Red source `0ace26725491b9479e919164d9cd3b6295bcceb8` proved that cache
credit remained usable after its one-second lifetime until another metrics
publication, and that a ready healthy TPS window refilled an idle backend with
only one request even though the warming path allowed a bounded two-request
wave. The focused red log SHA-256 was
`506edfa2ee7ef9011b47042b09868d566e7c5bfa6645251a211cec5d83dea66b`.

The correction projects an empty cache observation at request time when its age
is greater than one second while retaining the Controller-owned lease ledger
for cancellation and old-lease isolation. Age exactly equal to one second
remains valid. Mature idle refill is now
`min(rate_derived_sequence_limit, warming_sequence_limit)`; it never raises a
weak rate-derived limit from one to two. Same-snapshot reservations consume the
two-request wave atomically.

The exact f563 green run used the pinned Go 1.24 container and passed the three
focused regressions, the affected admission/server/metrics packages, and the
admission race suite. The log SHA-256 values were:

```text
cache/idle focused  11336c00cf158b2b03f92aa5b1ec2464ed2fe8fbc87b5c958da152dfea17df3e
affected packages   6dac2d48a9d848e3ebf4e33ff0fdb7d3c0e69b8b1304efb6da953bbb097230cf
admission race      6deb44c9611f655bc95f2606cb56c7873f5132a2dc4b4148aa8e7d79d68ebe46
```

Architecture review then found two remaining throughput/QoS questions that
must not be hidden by the focused green result:

1. A single 512K-plus request is isolated while it is in Prefill, but after its
   first response byte it contributes the same one Decode sequence as a short
   request. A mature TPS window learned from short contexts can therefore open
   a large same-snapshot wave before it contains representative long-context
   Decode evidence. Do not add a model-specific token weight, a universal
   `input / 64K` multiplier, or a fixed one-second hold. First add a deterministic
   workload-shift simulation. The candidate control is at most one additional
   admission wave per fresh 500-ms observation while an exclusive/quiescent
   Decode reservation remains active and its workload has not demonstrated
   stable capacity. Waiting or preemption still freezes immediately; small
   requests must reopen without a cooldown when current state fits. Accept this
   candidate only if it improves sustained SLO-compliant goodput over both the
   current burst and a strict freeze across cache-hot, cache-cold, 256K, 512K,
   low-flow, and mixed short/long scenarios.
2. KV geometry is genuinely initialized from the backend, but automatic
   Prefill bounds still select portable fixed 64K/256K/512K values and clip only
   part of the profile by maximum input and block size. KV capacity does not
   measure Prefill compute throughput, so deriving thresholds from a KV ratio
   would create false adaptation. Inspect source-backed immutable scheduler
   metadata for each backend, such as an exposed batched/chunked Prefill token
   budget, before changing this profile. If neither supported backend exposes a
   coherent startup value, retain and label the portable fallback honestly
   rather than claiming performance adaptation; production still omits the
   default values.

Before implementing either candidate, add the still-missing conservative
multi-prompt regression with two non-ASCII prompts. It must prove that aggregate
hard KV retains both prompts, the maximum per-sequence estimate retains only the
largest prompt plus shared structure, and Context admission can pass when every
prompt fits even though aggregate work exceeds one context. This closes an
estimator-distribution proof without adding another request-body scan.

The review found no evidence that merging the two bounded O(n) JSON scans,
replacing the fixed 61-bucket TPS snapshot, or collapsing admission interfaces
would materially improve end-to-end throughput. The accepted 4-MiB p99 remains
below the user-approved 100-ms boundary. Keep those items behind benchmark
evidence instead of expanding v0.12.15.

The required conservative two-prompt regression was implemented and pushed at
`e2eafc9041920a93123650f7217353125ec64b4d`. Two large non-ASCII prompts retain
their aggregate hard KV while Context admission uses the maximum individual
prompt. The focused and admission logs have SHA-256 values
`3e6b08a337c60306c9d885ad7b0b5292e537b6e92f4dde950630e3dd004dbc1e` and
`5e743c871dc137ad6147a8cb0018e20d3af1ebd8b0e74d45fdcfba5af2b555f4`.

Focused red source `0d1b8e0` proved that requests forwarded and completed
between adjacent 500-ms observations could disappear from reliable sequence
evidence. Pushed implementation `8e8ec0a` retains those forwarded liabilities
in the existing reconciliation pass. A further red test at `9dba9c6` proved
that seven sequences producing 150 aggregate TPS could open an eighth even
though the known projection was 18.75 TPS against a 20 TPS reference. Pushed
implementation `1f9a53a` prevents that known-below-95%-floor recovery while
preserving the one-to-two low-flow probe. Exact f563 green log SHA-256 values
for current source are:

```text
TPS focused             906822efc7db01bf00be3953db64fdd041420cb9608ac2980fc595ab3963b9c5
packages/simulations    28a8da05488671cdf3b2a3edb368ea45fd2b30114628106b51b88edc51edbbd0
admission race          047907090496e012634dc95272166f9790164a0ad7e08e2fde9bfefa4f9a6de7
detailed TPS/cache sim  ab839b98bfadb95b843d2773eeaa4f7a64c65b2f89940b3ab8b392bebd80d579
```

The reference-20 saturation result admitted 3/20 arrivals, retained 8906.67
completion tokens and 8892.78 SLO-completion tokens, held 21.75 mean active
TPS, reached seven running sequences, and produced zero preemptions. Reference
25 admitted 2/20, retained 8907.78 completion and SLO-completion tokens, held
25.20 mean active TPS, reached six running sequences, and produced zero
preemptions. These focused results do not close the following architecture
review.

### Corrective review: long-run QoS budget and exact lifecycle exposure

Status: design correction recorded on 2026-08-18 and revised after the exact
source review at `291066dd65a4f217ef5cb4e0e5fae52eb8baa304`. Exact lifecycle
exposure is implemented and package/race validated at the source recorded
below. The first `QoSBudgetForecast` implementation reached
`f2139d7ce124bbe250f8e439a3dc64c911e20517`, but the corrected shared
simulation clock at `2bc6345330047c6cfc58d917f49d2dbb6567b57a` invalidated its
simulation acceptance and exposed a request-lifetime budget defect. The
request-bounded correction and remaining stages are pending.
This section
supersedes any earlier acceptance that treats a per-wave 95% TPS projection,
a forwarded-request count, or HTTP first body byte alone as proof of the
long-run QoS objective.

The current implementation still has five release blockers:

1. `rateDerivedSequenceLimit` reduces the 60-second evidence to
   `aggregate_tps / reference`, and marginal recovery requires the next wave to
   meet a fixed 95% floor immediately. It cannot spend previously accumulated
   QoS surplus, so it protects a near-instantaneous threshold rather than the
   requested long-run average. The healthy `mix-80-20` simulation confirms the
   cost: completion falls from 1603.42 to 1488.65 (7.2%) and SLO-completion
   falls from 1525.64 to 1449.76 (5.0%) despite 23.59 mean active TPS, zero
   waiting, and zero preemptions. Do not relax the existing assertion to hide
   this loss.
2. The between-poll correction sums every forwarded Decode liability and then
   charges that count for the complete observation interval. Sequential short
   requests are therefore represented as if they were concurrent for 500 ms.
   This can greatly overstate sequence-seconds, understate per-sequence TPS,
   and mature the window on false exposure. Retaining requests as evidence was
   necessary, but count-times-interval is not the final model.
3. A 256K/512K/650K input class is retained only while Prefill liability is
   positive. After the first-byte transition, TPS admission sees the same one
   Decode sequence as a short-context request. The simulator also makes Decode
   throughput depend only on concurrency, so current long-input green results
   do not cover context-dependent Decode cost or short-to-long workload shift.
4. HTTP first body byte approximates Prefill completion for streaming requests
   but occurs only after complete generation for normal non-streaming
   responses. Current simulation invokes `MarkFirstByte` at idealized Prefill
   completion and therefore misses potentially long exclusive/quiescent
   over-protection in the non-streaming lifecycle.
5. The lexical estimate and fixed 9/8 or 3/2 margins have not established a
   cross-tokenizer hard-KV upper bound. A rough estimate is appropriate for
   Prefill ranking, but the same value cannot be called a model-independent KV
   safety bound until byte-BPE, SentencePiece, byte-fallback, multilingual,
   escape-heavy, tool-template, and unsupported multimodal cases pass the
   required oracle matrix.

The corrective architecture is deliberately limited to existing request and
backend evidence; it adds no runtime learning, model-name checks, tokenizer
assets, request rewrite, cache lookup, TTFT gate, fixed one-second hold, or new
production tuning parameter:

- `SequenceExposureLedger` owns two lifecycle-time integrals. Forwarded
  liability exposure covers every Decode sequence from forward commit through
  terminal release and is the conservative local sequence-seconds upper bound.
  Response-active exposure covers only the sequences evidenced after the first
  response body byte and qualifies a known Decode stall. The TPS window merges
  these local bounds with backend endpoint evidence using a documented union
  rule; it never sums overlapping evidence or substitutes response-active time
  for non-streaming Decode. Hot lifecycle operations and sample reads must be
  O(1), deterministic under an injected monotonic clock, reset with the backend
  epoch, and reject stale, duplicate, reversed, or overflowing transitions
  without leaking exposure.
- `QoSBudgetForecast` derives rolling surplus as
  `qualified_sequence_tokens - reference * qualified_sequence_seconds`. A
  request-time forecast may spend only a bounded part of positive surplus on
  at most one new wave per fresh 500-ms observation. The predicted next-window
  deficit must use current or conservative window aggregate throughput and the
  atomic post-admit sequence count. Waiting, preemption, stale/invalid evidence,
  negative surplus, or an unrepresentative workload shift cannot spend credit.
- `WorkloadShiftGuard` retains only a coarse request-size risk class through
  Decode. It may constrain expansion while a newly introduced exclusive or
  quiescent workload lacks a fresh representative observation, but it must
  reopen one bounded wave immediately when current evidence fits. It must not
  convert input tokens into a universal static sequence multiplier.
- Request estimation exposes separate Prefill-ranking and hard-KV-safety
  dimensions. The fast lexical value remains the Prefill signal; low-confidence
  hard capacity uses a conservative body-derived bound or request-scoped
  protection. Cache credit continues to reduce only Prefill compute and never
  hard KV.

Required test-first execution order:

1. Add a focused TPS-window red test with many sequential short requests in one
   500-ms interval. It must fail because current sequence-seconds exceed the
   timestamp-derived exposure, not because readiness or counters are absent.
2. Prove the lifecycle evidence contract before adding a QoS budget. Cover
   sequential and concurrent short requests, forward without first byte,
   first byte followed by success/error/cancel/disconnect/timeout, duplicate or
   reversed transitions, failed polls, runtime reset, streaming and
   non-streaming responses, and a sample window opened before intervening
   lifecycle events. Pure Prefill must not become a TTFT gate; non-streaming
   first body byte must not be labeled Prefill completion or exact Decode
   start. The published union must neither lose between-poll completions nor
   double count backend and local exposure.
3. Add a focused QoS-budget red test that holds metrics and reservations
   constant, varies positive/negative rolling surplus, and proves the
   pre-forward decision changes. Same-snapshot requests must consume the
   bounded wave atomically; waiting and preemption must still freeze it.
4. Extend deterministic simulation with bursty healthy mixed traffic and
   require reference-enabled SLO goodput to retain at least 98% of the
   reference-disabled candidate, long-run mean active TPS at or above the
   reference, no additional preemptions, and no more than one observation of
   idle-with-demand. Saturated reference-20/25 results must not regress.
5. Add 256K/512K/650K cache-hot and cache-cold short-to-long Decode shifts whose
   throughput explicitly depends on context class. Compare the current burst,
   strict freeze, and bounded-shift candidates; accept a control only when it
   improves sustained SLO goodput without additional preemption or low-flow
   self-lock.
6. Complete the cross-tokenizer estimator oracle before treating the lexical
   margin as hard-capacity evidence. Keep measured, conservative, and
   unsupported shapes distinct; do not increase every request to a worst-case
   estimate merely to make the matrix green.

Do not implement all four components speculatively. Prove each red behavior on
f563, implement the smallest complete vertical slice through HTTP decision and
reservation lifecycle, rerun focused race and simulations, then record its
exact source, archive, environment, command, exit status, and log SHA-256 here.

The first focused red source is
`291066dd65a4f217ef5cb4e0e5fae52eb8baa304`. Its GitHub archive SHA-256 is
`8b8462e8de8edd5641b8026ea107049e8d0fa028c7bc6ca5bf0ab3d7d422a6be`.
The isolated f563 Go 1.24 run failed only the intended behavior: 100 sequential
short liabilities producing 50 tokens over 500 ms were charged as 50
sequence-seconds instead of the supplied 0.5 sequence-seconds, reducing
mean-active TPS from the expected 100 to 1. The focused red log SHA-256 is
`57b0ea62dde592db50b98ada586e47977dfe4e1475cae391c26be0221493c113`.
This proves the count-times-interval defect only; it does not prove a ledger,
the local/backend union, non-streaming handling, or long-run QoS behavior.

Expanded red source `ccf00a733319f92b2ba1a94803ab5c388f6bc819`
defines the local/backend exposure union before implementation. Its GitHub
archive SHA-256 is
`3a13eb4c5df82294735431d0fb1def6527b75c34fc2834a748dc7fa2589c4c21`.
The isolated f563 command was:

```text
go test ./internal/admission -run '^TestV01215TPSWindow' -count=1 -v
```

It exited 1 with log SHA-256
`87fc93c46cc0ed7af42b976f840619fbb11c54295e691b5e83acd96c7f146ec3`.
The intended failures separately prove that the current window ignores exact
short Decode exposure, loses non-streaming forwarded exposure, discards a
known zero-token Decode stall, and truncates local exposure above backend
endpoint evidence. The pure-Prefill test passed, proving the red contract does
not charge forwarded exposure as TPS debt without output or positive
response-active Decode evidence. No production process, Compose file, backend,
route, image, or request was changed.

The first implementation at `58b721082bbf2199df9fa889afcde6fbb4356474`
passed the focused exposure behaviors but failed the formatting gate. After the
format-only correction, exact source `19a1deec20ee7c9ab0a7f8f570c48f8d3059f755`
exposed a deeper deterministic property failure in both the package and race
suites: subtracting two cumulative `float64` watermarks inverted two equal
19-ms exposure deltas by approximately `1.1e-16`, permanently closing the
Controller as unavailable. This was a real long-running correctness defect,
not a stale test assertion.

Pushed source `53aaec4bf490b88c7f1a227d5b33f7990996a52b` replaces cumulative
floating-point seconds with an O(1), overflow-checked unsigned 128-bit
sequence-nanosecond accumulator. Watermarks are now subtracted exactly and
converted to floating-point seconds only after the interval delta is known.
The deterministic 5000-step lifecycle property uses the injected Controller
clock and retains diagnostic exposure/reconciliation state on failure. The
exact GitHub archive SHA-256 is
`b282ce094be3f330afa3de0d9ddc92b20c7b79cebd85eecf70888cba9e7e4075`.
The isolated f563 Go 1.24 gates produced:

```text
gofmt -l                    empty, e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
exposure/property focused   exit 0, 0cbd6d1769803254dcfdc1080df0087c0c8011d68489ac342781b5554fc4e794
admission package           exit 0, 1af1a5d914e467ef29f29dc9bf7fe5870c55cec1189356ec5e3cb890ec84e035
admission race              exit 0, 230b1cb60040567f35b75b3c0ddec59b56749357c35cdcfaf90a1534ebf49cdd
```

This closes only lifecycle exposure accounting. It is not evidence for the
long-run QoS budget, workload-shift guard, estimator oracle, complete source
matrix, image, deployment, or production readiness. No production process,
Compose file, backend, route, image, or inference traffic was changed.

### Corrective review: request-bounded QoS surplus

Status: candidate implementation pushed at exact source `bc609c7`; exact f563
focused, package, race, and simulation evidence is still pending. This section
supersedes the first `QoSBudgetForecast` acceptance at `f2139d7`.

Static review found that the first budget formula is dimensionally incomplete.
It divides accumulated rolling surplus by historical active seconds and treats
the result as additional sustainable aggregate throughput, but admission is
not reversible at the next 500-ms observation. A long request admitted by one
positive snapshot can remain active after the historical surplus expires. The
same surplus can also be offered again once that request is covered by the next
backend sample, because `UnobservedSequences` is an observation lease rather
than a request-lifetime QoS lease.

The shared-clock correction at pushed source
`2bc6345330047c6cfc58d917f49d2dbb6567b57a` proves the practical failure. Its
GitHub archive SHA-256 is
`2c1b01dcfccd7a17b1ac2230355ffdb400c80d2fd165d859861af4f2eddb0f0a`.
The exact f563 Go 1.24 saturation test exited 1 with log SHA-256
`b68f724777acb82377cc56e10cf3bcd40b291ed809372f748aa7d3ee5568e809`:
the reference-20 candidate admitted a fourth foreground request, reached eight
total running sequences, and held only 19.36 mean active TPS for the 60-second
run. Reference 25 remained green at six sequences and 25.20 mean active TPS;
neither case preempted. An earlier evidence command used `bash -lc`, reset the
Go image `PATH`, and exited 127 with log SHA-256
`401c099e62bea82f14e1776140d4c4f93011200d3ff094963c1752bac26d19da`;
that harness error is not behavioral red evidence.

The smallest correct replacement keeps the strict long-window rate-derived
baseline and adds no runtime learning or production parameter:

- request estimation carries the explicit maximum output-token limit and a
  separate validity bit. The existing `DecodeHorizonTokens` remains the
  bounded rolling KV horizon and must not be mistaken for the complete request
  lifetime. Missing output limits are not fabricated from the 256-token KV
  horizon;
- a positive-surplus marginal wave is considered only after the strict
  baseline is full, for exactly one additional Decode sequence, with no
  waiting, preemption, stale interval, unobserved sequence, or live QoS-budget
  lease;
- with conservative aggregate rate `A = min(current_interval_tps,
  window_aggregate_tps)`, post-admit count `N`, reference `R`, and an explicit
  per-sequence output limit `O`, the required lifetime budget is
  `max(0, R*N-A) * O/(A/N)`. The request fits the marginal wave only when this
  finite requirement does not exceed positive rolling surplus. A zero-output
  request has zero Decode deficit; an unknown output lifetime cannot spend
  surplus;
- an admission that actually uses surplus owns one atomic QoS-budget lease.
  The lease survives reservation, forward, first byte, backend coverage,
  cancellation, disconnect, timeout, and residual-debt reconciliation, and is
  released only when the reservation lifecycle is terminal and covered. Strict
  baseline admissions remain available while a marginal lease exists, so the
  lease cannot create low-flow self-lock;
- the decision record, logs, status, and metrics expose whether the accepted
  request used the marginal QoS budget and how many leases remain. Arithmetic,
  multiplicity, reset, overflow, stale-handle, and double-release paths remain
  fail closed and bounded;
- Prefill/context/KV gates remain independent and run on their existing
  request-aware inputs. This correction does not claim to solve the separate
  context-dependent Decode shift or non-streaming first-byte questions below.

Execution remains test-first: retain the shared-clock saturation failure; add
focused short-bounded, long-bounded, and unknown-output forecasts; prove that a
covered live lease cannot spend the same surplus again; prove terminal and
reset release; then rerun admission/race and the complete request-aware TPS
matrix. Do not weaken the reference-20 assertion or update simulation golden
values merely to accept 19.36 TPS.

### Corrective extension: authenticated runtime policy API

Status: test-first source candidate implemented on 2026-08-18; exact pushed
source and complete f563 evidence are pending. This extension is part of
v0.12.15 and does not authorize an image, deployment, Router change, backend
restart, or production request.

External adjustment must operate on admission policy, not expose every startup
environment variable as writable state. The v1 mutability boundary is:

| Effective field | GET visibility | PATCH | Reason |
| --- | --- | --- | --- |
| TPS reference | yes | yes | operator-owned long-window QoS policy |
| admission mode | yes | no | enforce remains the production default; shadow is an explicit startup test override |
| observation poll/freshness | yes | no | changing these requires coordinated observer/ticker replacement |
| KV capacity, block geometry, hard limit | yes | no | immutable initialized backend capability within one runtime epoch |
| Prefill bounds and aggregate budget | yes | no | immutable initialization-time capability, not a learned or runtime-tuned policy |
| scanner/estimator limits | no secret data | no | request-path construction and memory/concurrency ownership are static |
| upstream/metrics URLs, TLS, bearer token | no | no | infrastructure or secret state must never be returned or hot-mutated |

The first API is `GET` and `PATCH /admin/v1/predictive-policy`. It always uses
the existing single-value Bearer-token constant-time authentication, regardless
of generation-path auth settings. Responses are bounded JSON with
`Cache-Control: no-store`; they never contain a bearer token, upstream URL,
metrics URL, request content, or model identity. GET returns schema version,
monotonic revision, startup-versus-runtime source, update time, the mutable TPS
reference, and the non-secret effective read-only policy/capability values.

PATCH accepts exactly one bounded JSON object containing `expected_revision`
and `tps_reference`. Unknown, missing, trailing, non-finite, negative, or
out-of-range values fail without changing live state. A stale revision returns
HTTP 409. The full candidate is validated before one Controller-locked commit;
concurrent writers using the same revision cannot both succeed. Every accepted
write advances the revision, while an equal-value write avoids resetting the
TPS evidence window.

When the reference changes, the Controller atomically installs the new value
and empties all accumulated TPS-window samples before the next pre-forward
decision. A metrics sample whose I/O window began before the policy revision
may update backend state but cannot refill the new TPS window. Existing KV,
Prefill, Decode, residual-debt, and request-bounded QoS reservations retain
their handles and normal release lifecycle; old QoS-budget leases remain a
conservative marginal-budget lock until terminal reconciliation. They are not
dropped, reinterpreted as new surplus, or allowed to leak. Disabling TPS with
zero affects only future TPS decisions and does not mutate backend capability.

Runtime API changes are intentionally process-local. A restart restores the
validated environment baseline and revision 1. Persistence, multiple-node
coordination, Router policy, and capability epoch reconstruction are outside
this API and must not be implied by a successful PATCH.

Required test-first evidence:

- auth failure, duplicate Authorization, method handling, media type, body
  bound, unknown/missing/trailing JSON, finite/range validation, and no secret
  fields in GET;
- stale-revision conflict, two concurrent same-revision writers with exactly
  one success, failed-update atomicity, monotonic revisions, and equal-value
  update behavior;
- reference change reaches the next real pre-forward Controller decision,
  clears historical TPS evidence, and excludes an in-flight pre-update metrics
  window;
- live reserved, forwarded-Prefill, active-Decode, cancellation, timeout,
  residual-debt, backend reset, stale handle, and close paths remain bounded
  and release exactly once across a policy update;
- fixed-cardinality logs and metrics expose revision, current reference,
  accepted/invalid/conflict/failure update counts, and last update time;
- focused Controller/API tests, admission and server packages, request-aware
  simulations, and admission/server race tests pass from one exact f563 source
  archive before this source can enter the complete v0.12.15 matrix.

The exact red-test source is pushed commit `7ef4b12`. Its GitHub archive
SHA-256 is `ae92080da1bf1347969beec22a259a14e5178bf6bcba6283c5f93c3b18cf626c`.
The fixed Go 1.24 focused run failed for the intended missing Controller/API
symbols with exit 1 and log SHA-256
`9835a0ecf7edf310144ac8591623e3c8f5bb1b82d6cc6aa0c449b433e4faeae3`.

The first pre-commit candidate run then exposed one return-contract defect:
invalid Controller updates preserved internal state but returned a zero policy
snapshot. Admission/API tests otherwise reached the intended routes. That run
exited 1 with log SHA-256
`8a10a324310212316f5830658c3894e7955986aac0980fde952a11ab92de088d`.
The corrected pre-commit focused run passed both packages with exit 0 and log
SHA-256 `901af26f05f0b71be456510198b645e72d1595852be9bab31e4eb32b92f894e6`.
These local-diff runs validate development causality only; they are not exact
commit evidence and cannot satisfy the remaining package/race/simulation gates.
After adding the policy-service panic boundary, the final pre-commit focused
run again passed both packages, left `gofmt -l` empty, and produced log
SHA-256 `fea0a997652a7fcb725f72006c86b3a0bcc275ad4e84b36680d38709060ffbc2`.

Lifecycle review also corrected a stale test expectation in the unvalidated
request-bounded QoS candidate: a terminal forwarded request remains residual
debt, including its QoS-budget lease, until a sample covers the terminal event.
Dropping that lease immediately would permit premature surplus reuse. The
updated test now requires the residual lease before the covering sample and
exact release afterward.

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
