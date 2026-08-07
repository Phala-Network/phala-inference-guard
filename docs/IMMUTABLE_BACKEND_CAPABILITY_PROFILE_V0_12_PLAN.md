# PIG v0.12.0 Immutable Backend Capability Profile Plan

Status: source implementation and complete remote-builder matrix passed;
exact-commit builder-local image verified; implementation commit `caaa882` and
parameter-boundary review commit `f3d92d3` pushed to
`pig-origin/codex/pig-v0.11.0-request-aware`

Date: 2026-08-07

Baseline branch: `codex/pig-v0.11.0-request-aware`

Baseline commit: `c7abab035135647d0f87c7a8d39be3dec15eb60c`

Target source version: `PIG-v0.12.0`

Execution boundary: PIG source only. Go, race, simulation, benchmark, and image
execution is remote-builder-only. This plan does not authorize a registry push,
Compose change, Router change, CVM deployment, restart, or production request.

## 1. Goal

Maximize admitted completion goodput while preserving the configured QoS
policy. Request size must influence the real pre-forward decision so that PIG
can admit a small request when a larger request would create unsafe KV or
Prefill pressure.

The v0.12.0 change replaces fixed absolute Prefill and KV token thresholds with
one immutable backend capability profile constructed when the single vLLM
upstream first becomes coherent and usable.

The following values may adapt during initialization but must never be learned
from business-request feedback:

- KV capacity and block geometry;
- block-aligned KV soft and hard token limits;
- conservative cold-Prefill service rate;
- regular, weighted/exclusive, quiescent, and aggregate Prefill token bounds.

The profile is immutable for the lifetime of its capability epoch. A 500-ms
metrics poll may change current usage, running, waiting, generation progress,
and preemption state. It must not mutate the profile.

## 2. Correct scope of learning

The user contract does not prohibit every possible learning mechanism. It
prohibits learning Prefill and KV parameters. This version does not introduce a
new learner, but it preserves an explicit boundary for later work.

### 2.1 Four parameter owners

The word "adaptive" is too broad unless the owner and lifetime are explicit.
PIG uses four non-overlapping owners:

| Owner | Examples | Update rule | May business feedback change it? |
| --- | --- | --- | --- |
| backend fact | served-model identity, KV capacity, KV block size, maximum model length | read once per capability epoch | no |
| measured capability | cold-Prefill tokens/s and derived token bounds | bounded initialization measurement, then freeze | no |
| live state | used KV, running, waiting, reservations, generation delta, preemption event, freshness | replace on every coherent 500-ms observation | no; this is state, not learning |
| policy or learner | QoS targets and safety budgets are policy; request/workload response estimates may be learners | policy changes only by configuration; learner changes only from qualified future samples | only the explicitly listed learner outputs |

This prevents three common category errors: treating a counter delta as a
learned parameter, treating a fixed safety ratio as measured model capability,
or allowing a workload learner to rewrite a physical resource limit.

### 2.2 Parameter classification

| Value | Owner and source | Update rule | Learning allowed? | Admission use |
| --- | --- | --- | --- | --- |
| served-model identity | backend fact from coherent vLLM metrics and `/v1/models` cross-check | once per capability epoch | no | identity and invalidation |
| KV capacity tokens | backend fact from `vllm:cache_config_info` | once per capability epoch | no | hard resource geometry |
| KV block size | backend fact from `vllm:cache_config_info` | once per capability epoch | no | reservation and limit alignment |
| maximum model length | backend fact from `/v1/models` | once per capability epoch | no | request/probe validity bound only |
| scheduler sequence ceiling | future trusted backend fact such as explicitly published `max_num_seqs` | once per capability epoch, only when available and validated | no | cap the process fail-safe ceiling; never infer it from traffic |
| scheduler Prefill geometry | future trusted backend facts such as `max_num_batched_tokens`, chunked-Prefill mode, and partial-Prefill concurrency | once per capability epoch, only when available and validated | no | fingerprint the backend, bound probes, and one-sided-cap partial-Prefill concurrency; never derive a request-size threshold from a scheduler-step budget |
| KV target/hard ratios | versioned QoS policy or complete explicit override | process configuration | no | derive safe operating limits |
| KV soft/hard token limits | KV facts plus fixed policy ratios | once per capability epoch | no | immutable soft selection and hard rejection |
| cold-Prefill tokens/s | two bounded isolated initialization probes | once per capability epoch | no | convert request tokens to estimated Prefill work |
| Prefill safety factor and time budgets | versioned QoS policy | process configuration/source | no | derive immutable Prefill classes and aggregate budget |
| four Prefill token bounds | measured capability, fixed time budgets, and fixed safety ceilings | once per capability epoch | no | immutable one-sided request-aware Prefill admission |
| current used KV and local reservations | live metrics plus atomic lifecycle state | every decision/poll | not a parameter | post-admit KV forecast |
| running/waiting/preemption | live metrics | every poll | not a parameter | current pressure and safety state |
| generation TPS delta | live counter delta | every coherent poll | not a learned value | immediate fallback forecast input |
| TPS target and floor | product/SLO policy | explicit configuration | no | objective and degradation boundary |
| poll interval and maximum age | control-plane policy | explicit configuration | no | freshness contract |
| preemption cooldown | event plus fixed duration | event-driven | no | bounded recovery state |
| model-neutral input estimate correction | exact attributed `usage.prompt_tokens` divided by the lexical/raw estimate | future bounded, epoch-scoped learner | yes, but not in v0.12 | soft Prefill and size-selection estimate only; never lower hard KV safety cost |
| expected completion-length distribution | uncensored attributed `usage.completion_tokens`, request class, and requested maximum | future bounded, epoch-scoped learner | yes, but not in v0.12 | soft goodput forecast only; never define the hard KV reservation |
| aggregate Decode capacity by effective concurrency/context bucket | future qualified stable Decode windows | future bounded, epoch-scoped learner | yes, but not in v0.12 | predict total and marginal completion TPS |
| learner confidence/uncertainty | sample coverage, recency, variance, and shift detection | derived with each learner snapshot | yes, but not in v0.12 | select learned estimate or conservative fallback |
| cache-hit estimate | deliberately absent | none | no | v0.12 remains cache-agnostic and charges cold work |
| `GLOBAL_LIMIT` hard ceiling | operator fail-safe policy | explicit configuration | no | final process-wide ceiling |

### 2.3 Why Prefill parameters are initialization-adaptive

KV capacity and maximum model length do not reveal Prefill speed. Scaling the
old `64K/256K/512K/256K` values only by KV capacity would replace one fixed
heuristic with another. vLLM exposes both of the isolated measurements needed
for a bounded startup capability check:

```text
vllm:prompt_tokens_by_source_total{source="local_compute"}
vllm:request_prefill_time_seconds_sum
```

The delta of cold locally computed prompt tokens divided by the Prefill-time
delta measures the backend work directly and excludes PIG JSON parsing, network
round trips, and the one-token Decode tail.

That measurement has a strict causal limit: an idle cold-Prefill probe can
prove that an upstream is slower than the built-in assumption, but it cannot
prove that a faster Prefill may overlap more work with active Decode streams
without damaging TPS. Initialization therefore adapts Prefill bounds only
downward. No learner may widen or rewrite those bounds. A later Decode response
learner may improve the soft TPS forecast inside the frozen envelope, but it
cannot turn learned business feedback into a larger Prefill class or aggregate
budget.

### 2.4 Why KV ratios are not probed or learned

The upstream reports allocatable KV tokens directly. A startup experiment
cannot discover a better safe fill ratio without filling the cache close to
exhaustion, creating queueing, or causing a preemption. That conflicts with the
QoS objective.

Therefore v0.12.0 adapts the absolute token limits to the upstream capacity and
block size, while treating the target/hard ratios as a conservative product
policy. A later operator may explicitly override those ratios, but business
traffic and preemption feedback may not rewrite them.

### 2.5 What is suitable for later learning

Only workload relationships with attributable labels and a safe fallback are
learning candidates. There are three useful families.

The first is input-size correction. PIG already receives exact
`usage.prompt_tokens` on eligible completed responses. A later learner may
calibrate the model-neutral lexical hint by request class and coarse size
bucket. Its estimate may improve soft Prefill classification and request-size
selection, but the conservative raw upper remains the hard KV cost. This split
prevents an optimistic token learner from weakening the immutable KV envelope.

The second is expected completion length. It can improve a soft goodput
forecast when a response is attributable and not censored by `max_tokens`,
abort, timeout, or truncation. It must not replace a hard output/KV bound.

The third is the Decode response surface:

```text
(effective decode sequences, current context pressure, recent stable TPS)
    -> expected aggregate completion TPS after one more admission
    -> expected per-user TPS after one more admission
```

This response changes by model, hardware, serving engine, speculative decode,
active context, and output workload. It cannot be read from vLLM startup
metadata. The current `aggregate_tps / (effective_sequences + 1)` projection is
only a deterministic fallback assumption; it is not a learned response curve.

All three future learners must be bounded, future-only, epoch-scoped, and able
to fall back independently. None may mutate model identity, KV capacity, block
geometry, KV soft/hard limits, cold-Prefill rate, Prefill token bounds, or the
TPS SLO. v0.12.0 intentionally adds none of them; first it establishes a
correct immutable resource envelope and clean measurement ownership.

### 2.6 Selection rule and learner priority

A value belongs to initialization adaptation only when it is stable for one
backend incarnation, safely observable before PIG opens intake, causally
identifiable without business traffic, and needed to construct the hard
resource envelope. A value belongs to learning only when it is
workload-dependent, has attributable and uncensored labels, cannot be obtained
from startup metadata or bounded probes, and can fall back without weakening a
hard guard. Values that satisfy neither rule are policy or live state.

This produces the following implementation order:

| Priority | Candidate | Decision |
| --- | --- | --- |
| 1 | aggregate and per-user Decode TPS response by effective concurrency and coarse context-pressure bucket | first useful learner after v0.12; directly replaces the constant-throughput projection that can over-protect batching headroom |
| 2 | lexical/raw input estimate correction by request class and coarse size bucket | later optional learner; soft selection and Prefill work estimate only, with conservative raw upper retained for hard KV |
| 3 | uncensored completion-length distribution | later optional learner for occupancy/goodput horizon; never a hard reservation bound |
| none | Prefill thresholds, cold-Prefill rate, KV ratios/limits, TPS SLO, preemption cooldown, freshness windows, global cap | policy or immutable capability; never learned |
| none | current KV, running/waiting, reservations, generation delta, preemption event | current state; observe and replace, never train as a parameter |
| excluded | cache-hit probability or cached-token estimate | outside the current contract |

The scheduler rows are classification rules, not v0.12.0 implementation
claims. The inspected vLLM v0.24.0 metrics contract does not publish those
values, so the current implementation neither guesses them from running counts
nor parses the upstream command line. If a later vLLM version publishes a
stable typed contract, they become initialization facts and capability-epoch
fingerprint inputs. `max_num_seqs` may cap an existing hard ceiling;
`max_num_batched_tokens` must not be misread as a safe long-request or aggregate
Prefill threshold and may only bound calibration work; explicitly published
partial-Prefill concurrency may only tighten PIG concurrency.

Preemption is a sparse safety event rather than a useful first learner target.
v0.12 uses the event and a fixed cooldown as a one-sided brake. A future risk
model would require independently attributable labels and may only tighten
admission; it cannot justify a larger KV or Prefill envelope.

The first future learner must estimate a response surface, not a learned
concurrency threshold:

```text
(effective Decode sequences, coarse active-context pressure)
  -> conservative lower confidence bound for aggregate completion TPS
  -> conservative per-user TPS forecast after one more request
```

Only fresh, epoch-consistent, Decode-dominant windows qualify. A window is
discarded on counter reset, preemption, active or pending Prefill, startup
settling, unstable sequence ownership, insufficient generation progress, or
metrics staleness. Context pressure is a read-only feature normalized by the
immutable profile; it does not train KV capacity, limits, ratios, or
reservations. The learned result may replace the deterministic soft TPS
projection only after minimum bucket coverage and uncertainty gates pass. It
has no authority over hard KV fit, frozen Prefill classes, the TPS SLO, or the
global fail-safe ceiling. The immutable KV and Prefill gates run first; a
learned Decode forecast cannot turn their rejection into an admission.

Input-size correction remains a distinct lower-priority learner. Its target is
the error of PIG's model-neutral request-size estimate against attributable
`usage.prompt_tokens`; it is not a Prefill capability measurement. Even when
mature, it may affect only soft size selection. The conservative raw input
upper remains the hard KV and cold-Prefill safety cost. Completion-length
learning is deferred further because abort, timeout, truncation, and client
`max_tokens` make its labels frequently censored.

Arrival-rate prediction, a learned preemption probability, TTFT learning,
cache-hit prediction, and a directly learned global concurrency cap are
excluded. Arrival and queue counters remain replaceable live state; a sparse
adverse event remains a brake; and the user contract has removed TTFT and cache
from this admission design.

## 3. Current-source findings

The v0.11.4 active path already has no online learner. Its composition root
constructs `RequestAwarePolicy` directly and passes fixed configuration values
for all four Prefill bounds.

The current observer already validates:

- one unambiguous served-model identity;
- vLLM backend kind;
- KV token capacity;
- KV block size;
- running and waiting counts;
- preemption and generation counters.

The current policy calculates soft and hard KV token limits from fixed ratios
on every request. The current Prefill classifier compares the request's
model-neutral token estimate with fixed token thresholds. Request-size and
local reservations already reach the atomic pre-forward decision path, so
v0.12.0 must replace the policy inputs rather than create a disconnected
calibration component.

The observer still contains compatibility hooks for older learners, but the
v0.11.4 factory does not wire them. Removing unrelated legacy learner code is
not required for this profile change and would unnecessarily expand the risk
surface. The active v0.12.0 factory must continue to prove that no learner can
mutate the capability profile.

The dormant `InputSizeCalibrator` is not evidence that request-aware admission
currently learns. It is wired only by older approximate-admission tests and the
simulation package, not by `newDefaultPredictiveShadow`. Reusing it in a later
version requires a new composition-root test proving that its output affects
only the soft request estimate while the hard KV reservation remains
conservative.

The vLLM v0.24.0 source at commit
`ee0da84ab9e04ac7610e28580af62c365e898389` confirms the initialization
protocol assumed here:

- `/v1/models` returns `max_model_len` for the base served model;
- `vllm:cache_config_info` is emitted from the effective `CacheConfig` after
  GPU block initialization;
- `vllm:prompt_tokens_by_source_total` is labeled by `model_name`, `engine`,
  and `source`;
- `vllm:request_prefill_time_seconds` is labeled by `model_name` and `engine`.

The inspected v0.24.0 metrics surface does not publish a corresponding
`scheduler_config_info` contract for `max_num_seqs` or
`max_num_batched_tokens`. v0.12.0 therefore must not invent or infer those
values. They remain optional future backend facts only if an upstream version
exposes and PIG validates them explicitly.

## 4. Capability epoch

A PIG v0.12.0 process owns exactly one immutable capability epoch, identified
by:

```text
served-model identity hash
KV capacity tokens
KV block size
profile schema/version
```

A normal metrics poll, monotonic generation-counter change, request completion,
low-flow interval, or preemption does not create a new capability profile. A
generation or preemption counter decrease proves that the metrics provenance
belongs to a new backend incarnation. Model identity, KV capacity, and block
size are not a sufficient execution-configuration fingerprint, so v0.12 closes
intake and requires PIG process reconstruction instead of reusing the old
profile.

If served-model identity, KV capacity, or block size changes, the observer
remains fail-closed as it does in v0.11.4. The orchestrator must reconstruct
PIG, at which point initialization builds one new profile. This avoids mutable
policy replacement while requests and reservations are live.

## 5. Immutable profile

The runtime owns one value object with no mutating methods:

```go
type BackendCapabilityProfile struct {
    SchemaVersion                string
    ModelIdentitySHA256          string
    KVCapacityTokens             int64
    KVBlockSize                  int64
    KVSoftLimitTokens            int64
    KVHardLimitTokens            int64
    SafeColdPrefillTokensPerSec  float64
    PrefillRegularTokens         int64
    PrefillExclusiveTokens       int64
    PrefillQuiescentTokens       int64
    PrefillAggregateBudgetTokens int64
    Source                       CapabilityProfileSource
}
```

`Source` is one of:

```text
explicit
startup_calibration
fallback
```

The raw model name and startup prompt are not retained in this object, logs,
metrics labels, or request state.

## 6. Initialization sequence

```text
wait for coherent vLLM metrics
  -> validate model identity, KV capacity, block size, counters
  -> construct block-aligned KV soft/hard limits
  -> if a complete explicit Prefill override exists, validate and freeze it
  -> otherwise check that the upstream is idle enough for isolated probes
  -> attempt two bounded direct cache-cold Prefill probes
  -> validate exact metric deltas and derive conservative service rate
  -> obtain a fresh coherent post-probe startup snapshot
  -> build and validate immutable profile
  -> on any calibration failure, build the conservative fallback profile
  -> construct Manager, policy, observer, and HTTP adapter
  -> open PIG intake
```

Calibration calls the internal vLLM endpoint directly. It must never pass
through PIG admission, Router, or the public ingress, eliminating startup
self-lock.

The proxy listener is not created until `PredictiveFactory` returns, so a
calibration request cannot recursively enter the PIG instance being built.

## 7. KV profile derivation

The default vLLM QoS policy remains:

```text
target ratio = 0.84
hard ratio   = 0.88
```

These are policy constants, not learned estimates. Initialization computes the
actual upstream-specific token limits once:

```text
kv_soft_tokens = block_round_down(kv_capacity_tokens * target_ratio)
kv_hard_tokens = block_round_down(kv_capacity_tokens * hard_ratio)
```

Validation requires:

```text
0 < kv_soft_tokens < kv_hard_tokens < kv_capacity_tokens
kv_soft_tokens % block_size == 0
kv_hard_tokens % block_size == 0
kv_hard_tokens - kv_soft_tokens >= block_size
```

The request policy consumes token limits directly. It must not recompute the
limits from ratios on every request. Existing post-admit accounting remains:

```text
effective_kv = observed_kv + every unabsorbed local reservation
post_admit_kv = effective_kv + conservative request KV reservation
admit only if post_admit_kv <= kv_hard_tokens
```

The soft-to-hard interval remains the selective request-size window. Current
KV usage changes pressure within that immutable interval but cannot move either
boundary.

Complete explicit ratio overrides remain supported for exceptional serving
stacks. Equal-to-default values do not belong in Compose.

## 8. Bounded cold-Prefill calibration

### 8.1 Eligibility

Calibration is attempted only when:

- coherent startup metrics identify exactly one vLLM model;
- KV capacity and block size are valid;
- running and waiting are both zero;
- the preemption counter is valid;
- `/v1/models` returns exactly the served identity and a positive
  `max_model_len`;
- no complete explicit Prefill override is configured.

If the upstream is busy, protected, unauthenticated, incompatible, or missing
the required metrics, PIG immediately uses fallback. It does not wait for a
low-flow window and does not close itself indefinitely.

### 8.2 Progressive probe bounds

Two sequential, non-streaming `/v1/completions` requests use:

```text
max_tokens = 1
temperature = 0
unique leading nonce per probe
no user data
```

The first probe is deliberately small and derived from the upstream geometry:

```text
probe_1_estimated_tokens = block_clamp(
    min(max_model_len / 64, kv_hard_tokens / 64),
    2K,
    8K,
)
```

After the first valid metric delta, the second probe targets at most four
seconds of measured work:

```text
probe_2_estimated_tokens = block_round_down(min(
    64K,
    max_model_len / 4,
    kv_hard_tokens / 8,
    probe_1_rate * 4s,
))
```

The second probe must be at least twice the first and at least 2K estimated
tokens larger. If the backend geometry cannot support two distinct probes,
calibration is ineligible and fallback is used.

The generated ASCII body size is chosen conservatively from the existing
model-neutral estimator. Actual cold tokens always come from the vLLM counter;
the requested estimate is used only to bound the probe. A complete calibration
has a fixed 15-second deadline that is independent of the potentially much
longer metrics-readiness timeout.

The nonce appears in the first cache block, so a prior PIG startup cannot turn
the probe into a prefix-cache hit. Prompt content is generated locally,
bounded, discarded after use, and never logged.

The calibration client follows no redirects and forwards no client
authorization or user-controlled headers. `/v1/models`, metrics, and completion
response bodies have fixed read limits. Every response body is closed on every
success, rejection, timeout, and parse-error path.

### 8.3 Metric-delta validity

For each probe, PIG captures metrics immediately before the request and after
the backend returns idle. The probe is valid only if:

- served-model identity, KV capacity, and block size are unchanged;
- `request_prefill_time_seconds_count` increases by exactly one;
- `request_prefill_time_seconds_sum` has a finite positive delta;
- `prompt_tokens_by_source_total{source="local_compute"}` has a positive delta;
- `prompt_tokens_by_source_total{source="local_cache_hit"}` has zero delta;
- preemptions do not increase;
- running and waiting return to zero;
- all counters are monotonic and exactly representable.

Any ambiguity rejects the entire calibration. PIG never subtracts unrelated
business traffic from a probe and never treats a cache hit as cold capacity.

After any submitted probe, success or failure, PIG must retry a coherent final
metrics snapshot within a bounded cleanup interval. Manager and observer use
this post-probe snapshot, not the pre-probe baseline. This accounts for probe
KV, generation progress, a still-cancelling request, or concurrent work.

If no probe was submitted, an ordinary calibration incompatibility selects
fallback using the original coherent startup snapshot. If a probe was
submitted and PIG cannot re-establish coherent metrics, startup fails exactly
as v0.11.4 fails when its mandatory state source is unavailable. Opening
intake from a known stale pre-probe snapshot is forbidden.

### 8.4 Profile formula

For each valid probe:

```text
probe_rate = delta_local_compute_tokens / delta_prefill_seconds
```

The immutable conservative service rate is:

```text
safe_prefill_tps = floor(min(probe_rate_1, probe_rate_2) * 0.80)
```

The `0.80` factor is a versioned safety policy, not a learned value. It leaves
headroom for startup-vs-production variance and the approximate request-size
signal.

QoS time budgets are also versioned policy:

```text
regular time budget   = 5 seconds
exclusive time budget = 20 seconds
quiescent time budget = 40 seconds
aggregate time budget = 20 seconds
```

Token bounds are calculated, capped by the built-in safety ceilings, and rounded
down to the KV block size:

```text
regular   = block_round_down(min(64K,  safe_prefill_tps * 5s))
exclusive = block_round_down(min(256K, safe_prefill_tps * 20s))
quiescent = block_round_down(min(512K, safe_prefill_tps * 40s))
aggregate = block_round_down(min(256K, safe_prefill_tps * 20s))
```

Validation requires:

```text
0 < regular < exclusive < quiescent
exclusive <= aggregate <= quiescent
all four values are block-aligned
all arithmetic is finite and overflow-safe
```

For a valid calibrated profile, `aggregate == exclusive` by default. The
separate field remains because it has a distinct policy meaning and explicit
overrides may choose a value between exclusive and quiescent. A sufficiently
fast probe can produce a calibrated profile numerically equal to the fallback;
that is expected and prevents an idle-only sample from widening Decode
interference risk.

## 9. Fallback and explicit overrides

Priority is:

```text
complete explicit override
  > successful one-time startup calibration
  > conservative built-in fallback
```

The fallback Prefill profile remains the v0.11.4 behavior:

```text
regular   = 64K
exclusive = 256K
quiescent = 512K
aggregate = 256K
```

Fallback values are block-aligned before use. If a backend has unusual block
geometry that cannot represent the fallback ordering, profile construction
fails closed rather than publishing an invalid policy.

The four existing `PREDICTIVE_PREFILL_*` variables become an all-or-none
override. Setting only part of the profile is a configuration error. Omitting
all four selects automatic startup calibration and allows fallback.

`SafeColdPrefillTokensPerSec` is positive only for a successfully calibrated
profile. Explicit and fallback profiles report it as unavailable rather than
inventing a measured rate.

The vLLM KV ratio variables remain optional policy overrides. Their default
values should not be written explicitly in Compose.

Calibration failure is observable and normally selects fallback. Busy startup,
missing optional calibration counters, `/v1/models` incompatibility, completion
rejection, and a cleanly cancelled timeout must not become a startup self-lock.
The one safety exception is loss of mandatory coherent metrics after PIG has
submitted a probe: without a post-probe state, PIG must not open from the stale
baseline.

## 10. Runtime invariants

1. Every supported request is classified before forwarding.
2. The approximate request-size signal changes a real pre-forward verdict under
   fixed current metrics and fixed reservations.
3. Manager check, decision, and reservation remain one atomic transaction.
4. Observed KV plus unabsorbed reservations is never lower than the known
   post-admit counterfactual state.
5. No metrics poll, completion, cancellation, preemption, or TPS sample can
   mutate the capability profile.
6. Current usage may change a decision without changing profile identity or
   profile values.
7. Calibration runs at most once per PIG capability epoch.
8. Calibration is never triggered by low flow, rejection, preemption, or a
   business request.
9. Calibration failure reaches fallback in bounded time when a coherent final
   upstream snapshot is available.
10. A profile identity mismatch remains fail-closed.
11. Prefill ownership, terminal release, cancellation, and rollback keep the
    existing exactly-once lifecycle semantics.
12. No cache-aware admission, Router selection, TTFT gate, or model-specific
    tokenizer asset is introduced.

## 11. SOLID ownership

The implementation uses narrow collaborators:

- `VLLMCapabilityMetadataProvider`: reads coherent startup metadata only;
- `ColdPrefillCalibrator`: performs the bounded direct probe and returns raw
  measurements only;
- `CapabilityProfileBuilder`: pure validation and deterministic derivation;
- `RequestAwarePolicy`: consumes immutable token limits and evaluates one
  post-admit decision;
- `PredictiveVLLMObserver`: publishes current state but cannot access a profile
  mutator;
- `PredictiveFactory`: composition root and fallback selection only.

The HTTP probe code does not classify requests. The profile builder does not
perform I/O. The observer does not calibrate. The policy does not know whether
its profile came from explicit configuration, calibration, or fallback.

The design must not add a general plugin framework, request-keyed calibration
map, mutable model registry, background calibration loop, or persisted profile
database.

## 12. Observability contract

One bounded startup log and low-cardinality metrics expose:

```text
capability_profile_source
capability_profile_schema
kv_capacity_tokens
kv_block_size
kv_soft_limit_tokens
kv_hard_limit_tokens
safe_prefill_tokens_per_second
prefill_regular_tokens
prefill_exclusive_tokens
prefill_quiescent_tokens
prefill_aggregate_budget_tokens
startup_calibration_attempts_total
startup_calibration_success_total
startup_calibration_fallback_total{reason=<bounded enum>}
```

No model name, URL, prompt, request ID, raw error, or dynamic reason string may
be a metric label. Status/log output must clearly separate immutable profile
values from current KV/Prefill state and the last decision.

Router-facing capacity remains derived from the same current decision and
immutable limits. A protection decision must therefore continue to appear in
PIG logs, PIG metrics, and Router-consumed capacity consistently.

## 13. Test-first implementation plan

All executable tests run on the approved remote builder. The Windows checkout
is limited to editing, source inspection, archive/hash preparation, and Git.

### Phase A: focused red evidence

Add failing tests for:

1. metadata plus capacity/block geometry produces block-aligned KV token limits;
2. two valid isolated probe deltas produce the expected immutable Prefill
   profile;
3. slower upstream measurements produce smaller Prefill bounds;
4. capacity changes produce different KV token limits without changing policy
   ratios;
5. a later metrics poll cannot mutate the profile;
6. a completion or preemption cannot mutate the profile;
7. cache-hit, counter ambiguity, concurrent traffic, timeout, invalid JSON,
   model mismatch, and non-idle startup all select fallback;
8. fallback completes within the calibration deadline and opens intake;
9. a partial Prefill override is rejected and a complete override wins;
10. changing only the profile under fixed state changes a real pre-forward
    long-request verdict;
11. small and large requests receive different decisions at the same current
    pressure;
12. concurrent admission cannot oversubscribe the immutable hard KV limit;
13. cancellation, disconnect, timeout, upstream error, and panic release each
    reservation exactly once;
14. any monotonic counter reset invalidates the capability epoch, closes intake,
    and prevents an old queued reservation from forwarding;
15. capability identity/capacity/block drift fails closed;
16. zero traffic and sparse generation never create a false lock;
17. a busy startup never waits indefinitely for an idle calibration window;
18. no business-response callback can reach a profile mutation API.
19. every submitted probe is followed by a coherent final state before intake
    opens;
20. response sizes, redirects, cleanup, and all HTTP bodies remain bounded.

Red evidence must fail because the v0.11.4 factory still injects fixed
thresholds, not because of a broken fixture, unavailable network, or missing
toolchain.

### Phase B: implementation

1. Add immutable profile/domain types and pure builder tests.
2. Extend Prometheus parsing with the exact calibration counters.
3. Add `/v1/models` metadata validation and bounded direct-probe transport.
4. Make the four Prefill variables optional all-or-none overrides.
5. Build KV token limits and Prefill bounds once in the factory.
6. Pass token limits, not ratios, to `RequestAwarePolicy`.
7. Add source/profile telemetry and startup logging.
8. Keep request-time policy work O(1) with no new allocations required by the
   profile lookup.
9. Update README and source/image version to v0.12.0 only after focused green.

### Phase C: remote-builder gates

Run from an exact source archive or clean exact-commit checkout:

```text
gofmt -d on every changed Go file
go vet ./...
go test focused profile/config/metrics/policy/server packages
go test -race focused profile/policy/server packages
go test ./...
go test -race ./...
go build ./...
request-aware deterministic simulation
KV deterministic simulation
low-flow and false-lock simulation
paired v0.11.4 baseline/candidate policy benchmarks in both orders
allocation contract for the request-time decision path
builder-local production image build
image version, binary, user, healthcheck, entrypoint, and startup contract
```

Record source archive SHA-256, exact commit, builder/container/toolchain
identity, every command and exit status, and hashes of material logs and
reports.

The final matrix runs against the exact pushed v0.12.0 release-candidate
commit. Version, README, Dockerfile, or executable changes after that matrix
invalidate it and require a new exact-commit run.

The CPU builder has no production vLLM model or GPU. Fake-vLLM integration
tests can prove HTTP sequencing, metric-delta validation, timeouts, fallback,
and final-snapshot ownership. They cannot prove the actual model's cold-Prefill
rate or live calibration success. That remains a later disabled-route runtime
gate requiring separate deployment authority.

## 14. Simulation and acceptance gates

The matrix covers at least:

- small and large KV capacities;
- block sizes 16, 32, and 64;
- slow, medium, and fast Prefill service rates;
- short-only, long-only, and mixed arrivals;
- 64K, 256K, 512K, and 650K-equivalent requests where capacity permits;
- burst admission before the next metrics poll;
- sparse/zero generation progress;
- waiting and preemption events;
- completion before poll, cancellation, and timeout;
- counter reset and capability drift;
- calibration success, busy fallback, metric fallback, and timeout fallback.

Mandatory safety gates:

```text
zero post-admit hard-KV oversubscription
zero reservation leak or double release
zero stale/identity fail-open
zero calibration-triggered preemption in the deterministic model
zero startup or low-flow self-lock
zero profile mutations after publication
fast idle Prefill measurements never widen the fixed safety ceilings
```

QoS/goodput promotion gates against v0.11.4:

```text
no increase in preemption count
no decrease in SLO-compliant completion-token goodput in any mandatory scenario
no decrease in total completion-token throughput greater than 2% in any
mandatory scenario
at least one heterogeneous-backend scenario improves total or SLO-compliant
completion-token throughput by 3% or more
request-time policy median and p99 do not regress by more than 10%
request-time policy allocations/op do not increase
```

Startup calibration latency is reported separately from request-time PIG
latency and GPU-serving throughput. Fallback must complete inside its fixed
deadline; it must not be hidden inside the normal per-request latency metric.

## 15. Three required reviews

Before implementation begins, record and apply corrections from:

1. model and causality review: metadata/probe validity, profile formulas,
   request-size causality, QoS objective, and baseline comparison;
2. safety and lifecycle review: atomicity, bounded startup, fallback, epoch
   identity, drift, reservations, races, low-flow behavior, and privacy;
3. evidence and release review: red validity, builder reproducibility,
   simulations, benchmarks, image contract, provenance, and no-deploy boundary.

Repeat the same three reviews after implementation and before versioning.

## 16. Release layers

Completion must be reported separately:

1. design document reviewed;
2. source implemented;
3. focused builder tests green;
4. complete clean-builder matrix green;
5. source committed and pushed;
6. builder-local deployable image verified;
7. registry image published, if separately authorized;
8. Compose integration, if separately authorized;
9. deployed runtime, if separately authorized;
10. live traffic evidence, if separately authorized.

This goal ends at layer 6 unless the user gives new production authority.

## 17. Progress ledger

- [x] current v0.11.4 source path and fixed-threshold behavior audited
- [x] Prefill/KV initialization-vs-learning boundary researched
- [x] review pass 1 completed and corrections applied
- [x] review pass 2 completed and corrections applied
- [x] review pass 3 completed and corrections applied
- [x] parameter-ownership follow-up review completed and corrections applied
- [x] parameter-selection and backend-incarnation follow-up completed
- [x] initialization-capability and future-learner boundary re-audited
- [x] focused remote-builder red evidence recorded
- [x] source implementation completed
- [x] focused remote-builder green evidence recorded
- [x] complete clean-builder matrix recorded
- [x] v0.12.0 executable source versioned and committed as `caaa882`
- [x] implementation commit and parameter-boundary documentation pushed
- [x] builder-local deployable image verified

## 18. Design review record

### 18.1 Pass 1: model and causality, completed 2026-08-07

Reviewed the current v0.11.4 request path, vLLM metric names captured from the
existing `use1-cb` evidence, the profile equations, and the intended
initialization-vs-learning boundary.

Findings and corrections:

1. Fixed 16K/64K probes would often time out on a slower backend and select the
   old fixed fallback, defeating initialization adaptation precisely where it
   is most needed. Section 8.2 now uses a small geometry-bounded first probe and
   chooses the second size from at most four seconds of measured first-probe
   work.
2. KV capacity metadata does not identify a safe model-specific ratio. The
   plan now explicitly adapts absolute block-aligned token limits while keeping
   the ratios as non-learned QoS policy. It does not claim that multiplying by a
   ratio is a measured KV performance model.
3. A request-size estimator correction would change both Prefill and KV cost.
   Section 2 now forbids online estimator learning in v0.12.0 rather than
   creating a hidden route around the immutable profile contract.
4. `max_model_len` is used only to bound probes. It does not scale Prefill
   performance and does not cap approximate request classification, because a
   conservative estimate can exceed the exact backend token count.
5. This pass initially retained only the Decode QoS response curve as a later
   learning candidate. The follow-up review in Section 18.4 supersedes that
   narrow classification without changing the v0.12.0 no-learner scope.

Pass 1 result: corrected design is ready for the safety/lifecycle review. No
source or executable test claim is made.

### 18.2 Pass 2: safety and lifecycle, completed 2026-08-07

Reviewed server construction order, Manager reservation/reconciliation,
counter-reset behavior, active-probe cancellation, HTTP resource ownership,
and low-flow failure semantics.

Findings and corrections:

1. The factory completes before `ListenAndServe`, proving that a direct startup
   probe cannot re-enter the new PIG listener. Section 6 now records this
   source-backed construction invariant.
2. A successful or timed-out probe can change KV use, generation counters, and
   running state. Reusing the pre-probe startup sample would understate current
   load. Sections 6 and 8.3 now require a fresh coherent post-probe sample for
   Manager and observer initialization.
3. A blanket "calibration can never fail startup" rule was unsafe. When PIG has
   submitted work and then loses mandatory metrics, it cannot distinguish a
   completed cancellation from still-running work. Section 9 now limits
   fallback to cases with a coherent final state and preserves fail-closed
   behavior for unknown post-probe state.
4. Prefix-cache blocks created by a unique probe may remain observable briefly.
   They are not subtracted or declared free; the final KV snapshot carries them
   into the initial virtual state.
5. The calibration transport needs its own bounded ownership contract. The
   design now forbids redirects and credential forwarding, bounds all response
   reads, and requires response closure on every path.
6. This pass originally allowed a same-identity counter reset to rebase current
   state without rebuilding the profile. Section 18.6 supersedes that decision
   after finding that the available identity/capacity/block fields cannot prove
   unchanged execution configuration. Counter reset now closes intake until
   process reconstruction.

Pass 2 result: corrected design is ready for evidence/release review. No source
or executable test claim is made.

### 18.3 Pass 3: evidence and release, completed 2026-08-07

Reviewed red/green provenance, builder availability, exact-source rules,
simulation acceptance, image evidence, and the no-deploy boundary.

Current read-only builder preflight:

```text
CVM: cvm_3e2k83KX / app_89811a9add5b20427ee1fbf4dc22a33984e41959
state: running
container: pig-v01011-builder
container running: true
container OOMKilled: false
Go: go1.24.13 linux/amd64
persistent free space: approximately 150 GiB
```

The current `phala.cmd` is `v1.1.20-beta.1+ebd53ae`. Its generated POSIX
`ProxyCommand` still fails under Windows process creation. Direct Windows
OpenSSH using Git OpenSSL at `C:\Progra~1\Git\usr\bin\openssl.exe` restored
read-only access. This is builder-access repair only and changed no container,
source, model, Router, or production state.

Findings and corrections:

1. A generic builder integration test cannot prove a real model's Prefill
   performance. Section 13 now distinguishes fake-vLLM protocol evidence from
   the later disabled-route GPU/runtime gate.
2. Testing pre-version source and then changing version bytes would invalidate
   exact-source evidence. Section 13 now requires the final full matrix against
   the exact pushed v0.12.0 release-candidate commit and requires a rerun after
   any subsequent source change.
3. A builder-local image is not a registry image or deployment. Section 16
   keeps the current goal at layer 6 and leaves publication, Compose, Router,
   CVM, and live traffic unproven.
4. An explicit profile may intentionally equal the built-in fallback. Priority
   wording now says complete explicit override; Compose hygiene, not runtime
   semantics, removes redundant explicit defaults.
5. Explicit and fallback profiles have no measured service rate. The design now
   forbids reporting an invented `safe_prefill_tokens_per_second` for them.

Pass 3 result: the design is approved for remote-builder red tests and source
implementation. No executable, image, registry, Compose, deployment, or live
behavior claim is made.

### 18.4 Parameter ownership follow-up, completed 2026-08-07

Re-audited the active composition root, request-aware hard and soft costs, the
dormant `InputSizeCalibrator`, completion usage attribution, and the upstream
vLLM v0.24.0 source at commit
`ee0da84ab9e04ac7610e28580af62c365e898389`.

Findings and corrections:

1. "Initialization adaptive" previously mixed backend facts, measured
   capability, live state, and policy. Section 2 now assigns each value to one
   of four owners and gives each owner an explicit lifetime.
2. The existing `InputSizeCalibrator` proves that input correction is
   implementable, but it is not wired into the v0.11.4 request-aware factory.
   Section 3 now records that code presence is not active learning.
3. Input token correction is a valid future learner because successful
   responses provide an attributable label. It is safe only if the learned
   estimate controls soft Prefill/selection behavior while the conservative raw
   upper continues to protect hard KV capacity.
4. Completion-length distribution is also learnable for a soft goodput model,
   but aborts, timeouts, and maximum-token termination are censored samples. It
   may never replace the hard output/KV bound.
5. The current `aggregate_tps / (effective_sequences + 1)` calculation is an
   assumption of constant aggregate throughput, not a learned marginal TPS
   response. A later bounded response-curve learner is justified, but remains
   outside v0.12.0.
6. vLLM v0.24.0 confirms the required cache geometry, per-source prompt-token,
   Prefill-time, and `max_model_len` surfaces. It does not expose a validated
   `scheduler_config_info` contract for the desired sequence/batch limits, so
   PIG must not infer them.
7. Cache-hit learning remains intentionally excluded under the current user
   contract.

Follow-up result: v0.12.0 still implements only the immutable initialization
profile. The design now leaves clean, safety-separated extension points for
later learners instead of incorrectly claiming that Decode TPS is the only
learnable quantity.

### 18.5 One-sided Prefill calibration correction, completed 2026-08-07

The first implementation converted a fast idle Prefill rate directly into
larger regular/exclusive/quiescent/aggregate bounds. Deterministic simulation
showed why this attribution was invalid: a 20K token/s idle Prefill sample made
the 512K busy request non-quiescent and preserved a 21.5-second TPS-floor
violation. The isolated probe measured cold work rate, not marginal Decode
interference.

The profile formula was corrected to cap calibrated values at
64K/256K/512K/256K. Slower capability can still tighten the profile. Faster
capability does not widen it. A separately qualified Decode response learner
may later improve only the soft TPS forecast inside these fixed limits. The
production policy also removed its ratio compatibility branch; ratios now
belong only to profile construction and the request hot path consumes one
immutable absolute-limit contract.

### 18.6 Parameter selection and backend-incarnation follow-up, completed 2026-08-07

Re-audited every request-aware input by identifiability, lifetime, feedback
quality, and authority over hard admission. Also compared the capability
profile lifetime with the observer's monotonic-counter reset path.

Findings and corrections:

1. Initialization adaptation and learning are not competing ways to tune the
   same number. Section 2.6 now gives mutually exclusive eligibility rules.
2. The first useful future learner is the Decode throughput response curve. It
   directly addresses the current constant-aggregate-throughput assumption.
   Input correction and completion horizon are lower-priority soft models.
3. Prefill/KV values remain immutable even if a future learner is enabled. A
   Decode learner may change a soft TPS forecast, never the initialized profile.
4. The policy constructor previously filled missing Prefill values itself. That
   hid a broken capability-builder connection and gave two modules ownership of
   defaults. The constructor now requires a complete initialized profile.
5. A same-model counter reset was incorrectly treated as proof that calibrated
   performance remained valid. The metric contract exposes no stable scheduler
   or execution-config fingerprint. v0.12 now invalidates the capability epoch,
   closes intake, and requires process reconstruction before recalibration.
6. Preemption and low/zero generation remain live evidence, not trainable
   parameters. This prevents sparse adverse events or idle windows from
   poisoning a capacity model.
7. The adapter previously accepted a policy and a separately published profile
   without proving they represented the same envelope. Profile validation and
   policy/profile matching now make the decision, startup log, and metrics use
   one KV/Prefill contract.

Follow-up result: the v0.12 implementation boundary is still no online learner.
It now defines a narrow next learner that targets over-protection without
granting feedback any authority over Prefill or KV safety.

### 18.7 Initialization and learner boundary re-audit, completed 2026-08-07

Reclassified every current and plausible upstream input using four tests:
stability for one backend incarnation, safe identifiability before intake,
availability of an attributable uncensored label, and authority over hard
admission.

Findings:

1. Current initialization adaptation is complete for the facts vLLM actually
   exposes: served identity, KV capacity, block size, maximum model length, and
   an isolated cold-Prefill rate. Derived KV and Prefill absolute limits remain
   immutable and block-aligned.
2. `max_num_seqs`, `max_num_batched_tokens`, and chunked/partial-Prefill
   settings are conceptually initialization facts, not learner targets. They
   cannot enter v0.12 because vLLM v0.24.0 exposes no validated metrics contract
   for them. A future typed contract may only cap or tighten admission.
3. TPS target/floor, KV ratios, Prefill time budgets and ceilings, poll timing,
   cooldown, and `GLOBAL_LIMIT` are product or safety policy. Neither startup
   traffic nor business feedback has authority to choose them.
4. Used KV, reservations, running/waiting, generation delta, Prefill ownership,
   and preemption are current state. Retaining history does not make them
   trainable parameters.
5. The first justified learner is a bounded Decode response surface with a
   conservative confidence bound. It learns workload-dependent batching and
   context response, not a concurrency limit, KV setting, or Prefill setting.
6. Input-estimator correction is valid only as a soft measurement correction;
   expected completion length is lower priority because usable labels are more
   often censored. Cache, TTFT, arrival-rate, and preemption-risk learners stay
   excluded.

Result: no executable-source correction is required. The v0.12.0 implementation
already freezes every Prefill/KV field and wires no learner. This pass narrows
the future learner contract and prevents unavailable scheduler metadata from
being guessed.

## 19. Focused red evidence

### 19.1 Remote-builder red r2, completed 2026-08-07

The source archive combined baseline `c7abab035135647d0f87c7a8d39be3dec15eb60c`
with only this plan and the two v0.12.0 behavioral test files. It excluded the
two unrelated untracked v0.11 design documents.

```text
local archive:
  ../tmp/pig-v0120-builder-20260807/pig-v0120-red-r2-source.tar
SHA-256:
  edaebc0672475de6b308bd4042701ee289505c8d6ed87c12fda193b4465a700f
builder directory:
  /work/pig-v0120-red-r2-edaebc06
builder/container:
  cvm_3e2k83KX / pig-v01011-builder / go1.24.13 linux/amd64
```

The builder independently reproduced the archive SHA-256. The focused command
was:

```text
go test ./internal/config/pigconfig ./internal/app/server -run V012 -count=1
```

It exited 1 for the intended behavioral reasons:

- omitted Prefill settings still loaded fixed
  `65536/262144/524288/262144` rather than selecting automatic profile
  construction;
- a partial Prefill override was still accepted;
- the factory made zero `/v1/models` and zero `/v1/completions` calls rather
  than one metadata call and two bounded calibration probes.

The busy-upstream fallback test passed, so the red did not rely on turning a
non-idle startup into a wait or self-lock. An initial r1 attempt was rejected as
invalid evidence because its factory test reached across a package boundary to
an unexported policy field and failed compilation. Red r2 replaced that with
public `Evaluate` behavior. Builder `gofmt -d` found one alignment-only diff in
the new server test; it was corrected locally before implementation and is not
counted as a behavioral red.

This evidence proves only that the named v0.12.0 behaviors are absent from the
baseline. It is not implementation, green, image, registry, Compose,
deployment, or live evidence.

## 20. Remote-builder green and image evidence

### 20.1 Exact candidate and baseline provenance, completed 2026-08-07

The final executable candidate was archived after the parameter-selection,
counter-reset, policy/profile consistency, and direct-transport corrections.
The archive excluded `.git`, `tmp`, and the two unrelated untracked v0.11 plan
documents.

```text
candidate archive:
  ../tmp/pig-v0120-builder-20260807/pig-v0120-r9-source.tar
candidate SHA-256:
  b146aaa8e923f2f4167762bd5bd32680dbcf3082428e7aa1f4da213496e902a0
candidate builder directory:
  /work/pig-v0120-r9-b146aaa8

baseline commit:
  c7abab035135647d0f87c7a8d39be3dec15eb60c
baseline archive:
  ../tmp/pig-v0120-builder-20260807/pig-v0114-baseline-c7abab0.tar
baseline SHA-256:
  cd8c3efc8243422b5e89c6a0d4b98141318ded28a589d4aaf02eb63fcc05581b
baseline builder directory:
  /work/pig-v0114-baseline-c7abab0

CVM / app:
  cvm_3e2k83KX / app_89811a9add5b20427ee1fbf4dc22a33984e41959
container / toolchain:
  pig-v01011-builder / go1.24.13 linux/amd64
```

Both remote archive hashes matched the local hashes before extraction.

### 20.2 Format, test, race, vet, and build matrix, completed 2026-08-07

All commands ran inside `pig-v01011-builder` from the exact candidate directory:

```text
gofmt -l internal cmd
  exit 0, no output

go test ./internal/runtime/predictive ./internal/infra/prometheus \
  ./internal/observability/metrics ./internal/app/server \
  ./internal/simulation/requestaware ./internal/config/pigconfig -count=1
  exit 0

go test ./... -count=1
  exit 0

go vet ./...
  exit 0, no output

go test -race ./internal/runtime/predictive ./internal/infra/prometheus \
  ./internal/observability/metrics ./internal/app/server \
  ./internal/simulation/requestaware ./internal/config/pigconfig -count=1
  exit 0

go test -race ./... -count=1
  exit 0

go build ./cmd/phala-inference-guard ./cmd/pig-request-aware-sim
  exit 0

go build ./...
  exit 0
```

Material log SHA-256 values:

```text
focused.log       12ce4d651ecd073bdb701672cf181dcc5954d012a2af6195db4ca072a6f0c6f6
full-test.log     3eca7cf3decb62373e4a5fc2f2ee5b355aac0255db503d9e7239a690c26f0a24
focused-race.log  f9e2c72cdc434f32488a54ba2bfbe0ac94553a8b50df89a40e945b08eebfe1d6
full-race.log     b095a003ce96f734f3f6acf7ab8c831e74875d45018a131ced84c96f973d5854
vet.log           e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
build-all.log     e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

### 20.3 Deterministic simulations, completed 2026-08-07

The executable request-aware suite included short/long mixes, a 500-ms
pre-poll burst, low-flow first arrival, waiting, stale recovery, preemption,
cancellation, and 512K/650K Prefill cases. The executable returned
`"acceptance": "passed"`.

```text
metric                              c7abab0 binary baseline   v0.12 candidate
SLO completion tokens/s             62.6674                   87.4188
total completion tokens/s            76.4926                   96.8961
TPS-floor violation seconds         106.1                      20.7
preemptions                           1                          1
maximum idle with demand seconds      0                          0.4
hard-fit idle rejects                 0                          1
```

The candidate improved SLO-compliant token goodput by approximately 39.5% in
this deterministic model while materially reducing TPS-floor violation time.
The isolated hard-fit idle reject and 0.4-second maximum idle interval remain
inside the suite's acceptance bounds and are not a production-performance
claim.

```text
request-aware-simulation.json SHA-256:
  30041adb51d709dfce1b24f06fc029fae27b96d745391f8e52f87eb91a93394d
kv-simulation.json SHA-256:
  61f1a9d8694b27f3675e30497fc7a235eb2feda4c396e795b1e680e5bef2692d
```

### 20.4 Paired hot-path benchmarks, completed 2026-08-07

The builder ran baseline then candidate, followed by candidate then baseline:

```text
go test ./internal/runtime/predictive -run ^$ \
  -bench BenchmarkRequestAware -benchmem -benchtime=200ms -count=3
```

Every baseline and candidate result was `0 B/op, 0 allocs/op`. Representative
medians across both orders were:

```text
case                         c7abab0 baseline     v0.12 candidate
policy open                  about 42 ns/op       about 38 ns/op
policy hard-KV               about 34 ns/op       about 30 ns/op
manager active-0             about 150 ns/op      about 137 ns/op
manager active-48            about 4.57 us/op     about 4.60 us/op
manager active-256           about 24.08 us/op    about 24.08 us/op
```

The absolute-profile hot path removes per-request ratio multiplication and is
faster on the constant-time policy branches. Reservation scans remain O(n) and
were effectively unchanged at 48 and 256 active entries; no allocation or
material CPU regression was observed.

```text
baseline order-1 log:  a68c8cce66b4977cede08dd892f909f746f8e1623644180df1961bcb8d03242b
candidate order-1 log: eae70ae17c47fe23df41f41d2304c7fae1adc56225381586efd51a450afc7dad
candidate order-2 log: 4d3d3ac5746436ca05d751d8b32a1c63d5b5aa8dd261eb2e5711e0a9ba7c45e8
baseline order-2 log:  f694168102f58f47b9ef4594b56a3bd1ca000cbbf9c5d61b5eef5d30d13460c6
```

### 20.5 Builder-local image, completed 2026-08-07

The exact r9 candidate built the following local-only image:

```text
tag:
  pig-v0.12.0-r9-b146aaa8:latest
image ID:
  sha256:49b2588abca4596909b552ec803c996ef7011fb212669d123d487944a25fefab
size:
  29315887 bytes
OCI version:
  0.12.0
entrypoint:
  /phala-inference-guard
configured user:
  0
binary SHA-256:
  7e3adec2558f17aa36d07b84bbec0fb01738d253f9da8ac40ead2b7166d6c743
```

A temporary builder-local container started successfully, logged
`phala-inference-guard PIG-v0.12.0`, and returned HTTP 200 with body `ok` from
`/healthz`. The temporary smoke and extraction containers were removed after
evidence capture; the image remains on the builder.

The clean final commit archive (`caaa882`) was then built independently as
`pig-v0.12.0-caaa882:latest`. A fresh 2026-08-07 inspect confirmed the same image
ID, proving that the exact commit and the smoke-verified r9 candidate produced
identical image content:

```text
exact-commit tag: pig-v0.12.0-caaa882:latest
image ID:        sha256:49b2588abca4596909b552ec803c996ef7011fb212669d123d487944a25fefab
OCI version:     0.12.0
entrypoint:      /phala-inference-guard
configured user: 0
created:         2026-08-07T09:37:30.074180902Z
```

```text
image-build.log SHA-256:         f98fa00a8210a88d330973611474e6aa1812090c86d363dd3b1b50bb2b659cb5
image-smoke.log SHA-256:         ca5d8ac6068b199f35fe35c7cf3d6d674ffa8b307ab01c587a31dd24ddfcf35b
image-smoke-inspect.json SHA-256: 3a505a92d5ad4352b8c63ad518997a699c8bdca46c5e0cc9cd01deb3828728c0
```

Completion layer is now source implementation, complete remote-builder matrix,
and builder-local image. Registry publication, Compose integration, Router
mutation, CVM deployment, GPU calibration, and live traffic remain deliberately
unperformed and unproven.
