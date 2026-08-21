# PIG v0.12.18 QoS-Constrained Throughput Optimization Plan

Status: active design and execution plan. No v0.12.18 executable identity,
image, deployment, or live acceptance exists yet.

This plan supersedes the v0.12.17 maintenance conclusion only for the new
optimization work described below. It does not invalidate the measured
v0.12.17 release evidence, change the current production deployment, or
authorize a production mutation by itself.

## 1. Objective

Maximize sustained aggregate completion throughput and useful GPU utilization
while keeping the sufficiently long mean per-user TPS roughly at or above the
configured reference, avoiding material queue growth, and keeping preemption
and resource failures acceptably rare. Occasional short TPS excursions below
the reference are acceptable; preserving unused instantaneous margin is not an
objective.

Every admission decision remains predictive and occurs before forwarding.
Backend metrics and completed responses may improve the evidence for a later
decision, but never retroactively justify an in-flight request. PIG remains a
single-upstream admission proxy. It does not route, mutate requests, require a
model-specific tokenizer asset, duplicate backend request-legality validation,
or reintroduce TTFT as a gate.

The intended next version is v0.12.18. Assign the executable version only after
the behavior-bearing source and its focused gates are coherent. A plan commit,
source commit, passing test, local image, published image, Compose candidate,
deployed container, and live acceptance are separate completion layers.

## 2. Current Truth And Evidence Boundary

The source baseline for this plan is clean commit
`c61d7e73cc5ca66b3a406f467f90c6af1e35532b` on branch
`codex/pig-v0.12.17-log-observability`. New work is isolated on
`codex/pig-v0.12.18-throughput-estimator`. The currently published and accepted
v0.12.17 executable source remains
`0091241bc9edc30f0f7ff50010504225d3fa14c8`; later commits are documentation
only. Do not inherit executable evidence after the first v0.12.18 source
change.

The latest read-only production audit covered approximately six hours on CVM
`311bbcdb-e348-4922-b37d-541755b09ff7`, Router name `use1-19`, with vLLM,
`PREDICTIVE_TPS_REFERENCE=25`, a default 500-ms metrics poll, max model length
262,144, KV capacity 1,977,660 tokens, KV block size 64, and KV hard ratio 0.88.
The exact audit boundary must be retained with its artifacts; the following
counts are decision evidence, not a new release acceptance window:

```text
attempts                                      116,685
admitted                                       38,940  33.37%
enforced protection                            77,745  66.63%
tps_reference/load                             46,191  39.59% of attempts
Prefill protection, all scopes                 30,921  26.50% of attempts
input_limit/request                               512   0.44% of attempts
kv_capacity protection                              0
backend preemption                                  0
KV mean / maximum                              10.79% / 41.69%
```

Compact-log suppression and time-boundary reconstruction introduce a small
count mismatch. Router retries can also make attempts much larger than unique
business requests. Therefore protection counts locate possible control-plane
friction but are not themselves lost-throughput counts.

In an unmatched six-hour Grafana comparison, `use1-19` had about 18.1% higher
mean output TPS than legacy `use1-4c`, materially lower waiting/queue/TTFT/E2E,
and zero preemption. The nodes differed in traffic shape, cache hit, backend
revision, KV dtype, and memory settings. This proves neither that every gain
came from PIG nor that v0.12.17 has no effect. Future claims require a matched
or time-aligned workload and completion-goodput evidence.

Current vLLM completion histograms after its last restart show an average
actual Prompt of about 2,598 tokens and an average actual output of about 273
tokens. Approximately 96.61% of outputs are at most 2K tokens and 99.92% are at
most 5K, while the client-declared output limit P95 is about 95K. This mismatch
is the primary evidence for reviewing the TPS surplus lifetime forecast. It is
not permission to replace a declared maximum with the observed mean.

## 3. Non-Negotiable Contracts

1. The objective is QoS-constrained completion goodput, not the lowest reject
   rate, the highest raw admit count, maximum KV occupancy, or a perfect TPS
   floor on every 500-ms sample.
2. Keep the configured TPS reference as a soft sufficiently-long average
   target. Allow bounded short-lived debt only when the post-admit forecast and
   outstanding liabilities make it safe. Waiting or preemption evidence must
   stop new expansion without creating a cooldown after current state recovers.
3. Request-size estimation stays model-independent and bounded on the
   production hot path. Do not add per-model tokenizer assets or tokenize every
   request with a full backend tokenizer. Offline exact tokenizers are oracles,
   not production dependencies.
4. Separate three meanings that are currently entangled:

   ```text
   SelectionInputTokens
     approximate work used for Prefill classification and scheduling policy

   ContextUpperBoundInputTokens
     conservative per-sequence model-context legality bound

   KVReservationInputTokens
     conservative aggregate/per-sequence KV reservation including uncertainty
     and later backend block rounding
   ```

   A KV uncertainty margin must not by itself make a context-legal request fail
   `input_limit`.
5. Cache evidence may reduce only estimated Prefill compute unless the backend
   supplies request-specific, coherent prefix/KV evidence. Aggregate cache hit
   must not reduce hard input KV reservation or downgrade an exclusive or
   quiescent long-input class.
6. vLLM and SGLang retain separate metrics adapters and validation contracts.
   Do not substitute a metric from one backend for the other or add model-family
   branches.
7. Check, decision, and reservation remain atomic. Preserve bounded, idempotent
   reconciliation for pre-forward cancel, forward failure, success, upstream
   error, client disconnect, timeout, panic, backend epoch reset, policy reset,
   shutdown, and observation absorption.
8. Production defaults to enforce. Shadow mode and extensive overrides are
   explicit isolated-test tools only. Production configuration should state
   only required deployment values and intentional policy such as the TPS
   reference.
9. Do not add a global queue-wait increase, a fixed one-second hold, request
   rewriting, Router policy, priority injection, premium/basic tiers, learning
   of Prefill/KV parameters, or a delayed feedback cooldown.
10. Execute Go tests, race, simulations, builds, and benchmarks only in the
    approved remote f563 workbench. Local Windows work is limited to inspection,
    editing, formatting inspection, Git, and evidence review. Do not restart the
    CVM or backend for PIG development; replace only PIG when a later isolated
    image gate explicitly requires it.
11. Commit and push every accepted plan or source revision. Build only from the
    exact pushed commit. Publish an image only after the complete source,
    simulation, race, benchmark, and three-review gates pass. Deployment and
    Router changes require their own later gate and are not authorized by a
    source result.

## 4. Confirmed Design Defects And Opportunities

### 4.1 Request estimator semantic over-counting

The current classifier buffers at most 4 MiB, strictly scans JSON through
`ParseJSONFields`, then scans the same body again through `scanJSONFeatures`.
All JSON string values contribute to the lexical estimate, including `model`,
metadata, routing/debug/custom strings, and control parameters that do not
normally enter the model Prompt.

The generic feature scanner also:

- recognizes `role` and `function` by property-name occurrence rather than by
  endpoint-valid message/tool position;
- recognizes modality markers by string equality without validating a typed
  content part;
- estimates raw JSON escape bytes rather than incrementally decoded text;
- charges schema string values and then charges the raw schema again, which can
  double-count tool-heavy payloads;
- treats `response_format` like a Prompt schema even when the backend uses it
  only as a Decode constraint;
- applies a whole-body high bound that charges non-Prompt metadata;
- only derives Decode fan-out from Prompt batch and `n`, without an audited
  endpoint/backend contract for other candidate parameters such as `best_of`.

These defects are expected to overestimate metadata-, tool-, schema-, and
escape-heavy requests. Unsupported fan-out can instead underestimate resource
demand. Both directions require tests.

### 4.2 Context legality uses a KV-reservation margin

`contextGate` currently consumes
`MaximumSequenceKVReservationInputTokens`. The normal 9/8 and conservative 3/2
KV safety margins can therefore produce `input_limit` for a request whose
estimated Prompt fits the backend context. This violates the distinct meanings
of context legality and KV risk and is a confirmed responsibility defect.

### 4.3 TPS QoS surplus uses the full declared output lifetime

The marginal QoS lease computes:

```text
forecast_seconds = declared_output_limit / projected_per_sequence_TPS
required_surplus = projected_deficit_rate * forecast_seconds
```

Production clients declare output ceilings far above actual completions, so a
single extra sequence can require enough rolling surplus to pay for an
implausibly long complete lifetime. Requests without a recognized output limit
cannot use this path at all. This can defeat the stated long-average objective
even when only one bounded marginal sequence would be exposed.

Do not replace the declared ceiling with an observed average. The candidate
design is a bounded QoS control horizon:

```text
soft_forecast_horizon = min(declared_lifetime, bounded_control_horizon)
```

The horizon measures how much short-term QoS debt the controller may expose; it
does not claim the request will finish at that time. Retain at most one
outstanding marginal lease, at most `base + 1`, and immediate expansion freeze
on waiting/preemption. The lease remains a lifecycle liability so the same
surplus cannot be spent repeatedly.

This is a simulation candidate, not an approved source change. Its horizon and
behavior must be selected through deterministic scenarios and long-window
acceptance rather than intuition.

The TPS denominator has a second, separate uncertainty. `tpsWindow` divides
generation tokens by the maximum of backend endpoint running-seconds, local
forwarded sequence-seconds, and local response sequence-seconds. Local forwarded
exposure includes Prefill and unabsorbed work, and is structurally not smaller
than local response exposure. Backend running gauges can also include Prefill.
This is a conservative protection against hidden concurrency, but it may count
non-Decode time against a per-user Decode TPS objective. Do not alter this
denominator from code inspection alone. Phase 0 must expose which source wins
the maximum and compare it with response-provided ITL/generation evidence where
available. Keep denominator correction and output-lifetime debt as separate
simulation variables so their effects remain attributable.

### 4.4 Non-streaming and multi-sequence Prefill lifecycle may over-hold debt

PIG transitions a forwarded reservation from Prefill to Decode on the first
upstream response-body byte. For streaming responses this normally approximates
the first generated token. For non-streaming responses, the first body byte may
not be visible until generation is complete. PIG can therefore retain pending
Prefill tokens, Prefill class ownership, sequence liabilities, and input-KV
liabilities throughout Decode.

One HTTP first-byte event also does not prove that all children of Prompt batch
or `n>1` have completed Prefill. The existing conservative residual liability
is safe but can over-protect.

No release behavior changes until fixed-cardinality evidence measures:

- streaming known/true/false/unsupported;
- Prompt batch and Decode fan-out buckets;
- first-byte-to-terminal duration;
- Prefill protection correlated with those request shapes.

Do not use a fixed timer, rewrite requests to streaming, or release hard debt
without backend/request-specific evidence.

### 4.5 Automatic Prefill bounds are fixed fallbacks

`automaticPrefillBounds()` currently aligns fixed 64K/256K/512K and 256K
aggregate thresholds to KV blocks, with limited clipping. It is not derived from
model Prefill rate, GPU type, vLLM `max_num_batched_tokens`, or SGLang
`chunked_prefill_size`/`max_prefill_tokens`.

A later initialization-only improvement may normalize thresholds in scheduler
Prefill chunks when the upstream exposes coherent metadata. Missing metadata
must retain a documented fallback and must not create new production-required
configuration. KV capacity or max context alone must not scale Prefill limits.

### 4.6 Current KV and cache policy are not the primary bottleneck

The audited window had no `kv_capacity` protection and no preemption. Raising
the hard KV ratio or using aggregate cache hit to discount KV cannot be justified
as the next throughput change. Preserve the current bounded recent cache credit
for Prefill compute only. Revisit request-specific cache/KV reuse only if the
backend later exposes coherent prefix evidence and KV becomes a measured gate.

### 4.7 Decode horizon and scanner resource bounds need portability work

The fixed 256-token future Decode horizon is ample for the current 500-ms H200
observation cadence but is not proven for very high per-sequence TPS or a longer
poll/staleness interval. A later safety design should combine a minimum floor
with bounded generation growth across the maximum credible observation gap.

The classifier limits 64 concurrent requests but not aggregate buffered bytes.
At 4 MiB each, peak outstanding request bodies can approach 256 MiB even though
the retained pool is smaller. Add a weighted byte budget after higher-impact
behavior is resolved; unknown-length bodies reserve their maximum weight.

### 4.8 Current observability cannot attribute the next optimization

Compact logs recover approximate aggregate reasons but lose suppressed request
shape distributions. Current metrics do not provide cumulative fixed-cardinality
breakdowns for input size, confidence, Prefill class, streaming/fan-out shape,
or TPS lease grant/deny subreason. `pig_dynamic_global_limit` is a Router
compatibility projection, not an old-style absolute request ceiling; Grafana
must not divide running by it as a cross-version pressure ratio.

PIG already contains a bounded response usage parser but the production path
does not consume it. Either wire it only as optional, privacy-safe observation
of response-provided usage or remove it as dead code. Do not mutate requests to
force usage output.

## 5. Implementation Sequence

### Phase 0: Evidence without admission behavior change

Add bounded, fixed-cardinality counters/histograms. Keep dimensions separated
to prevent Cartesian cardinality growth:

```text
decisions_total{action,reason,scope}
estimate_outcomes_total{outcome}
estimate_confidence_total{confidence}
estimate_input_tokens{fixed histogram buckets}
estimate_prefill_class_total{prefill_class}
classifier_outcomes_total{reason}
request_streaming_total{state}
request_fanout_total{bucket}
tps_decisions_total{result,subreason}
tps_denominator_selections_total{source}
estimator_validation_total{result,estimate_kind}
output_limit_comparison_total{declared_bucket,actual_bucket}
```

Exact names may follow the existing `pig_predictive_*` namespace, but label
values must be closed enums. Do not log Prompt content, hashes, model IDs,
tenant IDs, request IDs, arbitrary endpoint strings, or numeric values as
labels. Preserve compact logs and expose cumulative reasons even when a log is
suppressed.

Restore request-shape facts needed for evidence, including streaming-known and
streaming, without changing upstream bytes. If a valid response already carries
Prompt/completion usage, compare it to the accepted request estimate in bounded
histograms. Record unavailable/censored samples explicitly; accepted-only
validation must not be presented as rejected-workload accuracy.

Record the raw backend endpoint sequence-seconds, local forwarded
sequence-seconds, local response sequence-seconds, and the selected maximum in
fixed metrics or bounded summaries. These values diagnose whether TPS protection
is caused by real Decode concurrency, Prefill/unabsorbed exposure, or the
declared-output debt test. They do not authorize a denominator change.

Correct current PIG documentation and provide replacement PromQL so Router
compatibility projection is not interpreted as an absolute dynamic limit.
Changing an external Grafana dashboard requires separate authority and is not a
PIG source task. Phase 0 acceptance requires byte-identical admission decisions
for a deterministic baseline corpus and no unbounded metric labels or log
volume.

### Phase 1: Endpoint-aware one-pass estimator

Replace the duplicated generic scans with one strict, allocation-bounded parser
selected by the normalized supported endpoint:

```text
/v1/chat/completions
  messages/content/name/prior tool_calls/tools and audited fan-out fields

/v1/completions
  prompt and audited completion fan-out fields

/v1/responses
  instructions/input/tools and audited response fan-out fields
```

Explicitly ignore non-Prompt controls such as model selection, sampling
parameters, streaming flag, user metadata, and routing/debug fields. Parse
typed multimodal parts rather than marker strings. Incrementally decode JSON
escapes for lexical estimation without allocating complete decoded strings.
Charge tool schema values and structure once. Treat `response_format` according
to verified vLLM and SGLang request semantics; do not automatically charge it as
Prompt/KV.

The endpoint-aware field set is a semantic proxy, not a claim of exact backend
chat-template serialization. Backend revisions can add special tokens, reorder
tool schemas, or serialize fields differently. Represent that residual in the
Context/KV uncertainty bounds, validate it with offline exact-template fixtures
where assets are available, and fall back explicitly when the request shape or
backend contract is not covered.

Produce independent selection, per-sequence context upper bound, and KV
reservation estimates. Apply tokenizer uncertainty only where needed and apply
backend block rounding only in `BuildRequestWork`, not in the lexical parser.

Unsupported or custom shapes must have an explicit fallback and observable
reason. A fallback may be conservative, but it must not claim exact context
legality. PIG continues to leave final protocol/model validation to the backend.

### Phase 2: Offline cross-tokenizer oracle and estimator acceptance

Create offline fixtures for representative tokenizer families without shipping
their assets in PIG:

- Llama/Qwen-style BPE;
- Gemma/SentencePiece;
- Mistral-compatible BPE;
- DeepSeek-family tokenizer behavior;
- byte-fallback-sensitive content.

Cover ASCII prose, source code, JSON, UUIDs, long numbers, repeated whitespace,
Chinese and other non-ASCII scripts, emoji/combining characters, JSON escapes,
base64/high-entropy strings, tool schemas, metadata-heavy bodies, batching,
`n`, audited candidate fan-out, and supported multimodal request shapes.

For supported text requests require:

- no dangerous context/KV underestimate in the declared hard-bound oracle set;
- lower median and tail overestimation than v0.12.17 for metadata/tool/escape
  corpora;
- unchanged exact explicit-token-array accounting;
- production estimator mean below 1 ms on the representative corpus;
- accepted 4-MiB estimator/classifier p99 below 100 ms in the approved remote
  environment;
- bounded allocations and memory under mixed small/large concurrency.

Exact tokenizer parity with a backend chat template is not a production claim.
The oracle measures and bounds estimator error; backend-specific templates and
multimodal expansion remain explicit uncertainty where unavailable.

### Phase 3: TPS bounded-debt simulation

Keep the estimator change separate from TPS behavior. Add deterministic
alternatives for:

1. current complete declared-lifetime forecast;
2. bounded control horizon with a declared limit;
3. bounded control horizon when the output limit is absent;
4. long-running completion beyond the soft horizon;
5. cancellation, error, disconnect, and completion before the next poll;
6. waiting, preemption, observation staleness, backend epoch reset, bursts, low
   flow, recovery, and distribution shifts.

First hold the existing TPS sequence-seconds denominator byte-for-byte constant
and compare only output-lifetime debt. In a separate experiment, and only if
Phase 0 evidence shows material Prefill/unabsorbed denominator inflation, test a
decode-qualified denominator with conservative coverage for unknown and
non-streaming requests. Do not combine both changes into one result.

The candidate may expose at most one outstanding marginal lease and at most one
sequence beyond the base limit. It must not spend the same rolling surplus
twice. The control horizon remains an internal default unless evidence proves a
production override is necessary.

Promotion thresholds versus the exact v0.12.17 simulation baseline:

- sufficiently long qualified mean-active TPS remains roughly at or above the
  configured reference;
- no increase in preemption, unsafe KV fit, reservation leak, double release,
  controller closure, or low-flow self-lock;
- no material increase in queue P95 or sustained waiting;
- completion goodput and output-token goodput improve on the mixed realistic
  workload, not only on an artificial short-output workload;
- occasional sub-reference intervals are allowed and reported separately.

Do not select a horizon solely because it maximizes admissions. If no candidate
beats the current policy under these thresholds, retain the v0.12.17 TPS gate.

### Phase 4: Prefill lifecycle evidence and conditional correction

Use Phase 0 measurements before choosing a change. If non-streaming or
multi-sequence requests materially contribute to Prefill protection, design a
backend-evidence-based release signal and prove all child-sequence liabilities.
If trustworthy evidence is unavailable, keep the conservative lifecycle and
record that the opportunity is blocked by protocol observability rather than
adding a timer.

### Phase 5: Portability and resource hardening

After the primary goodput changes are decided:

- optionally initialize Prefill classes from trustworthy scheduler metadata,
  normalized in Prefill chunks, with a fixed fallback;
- bind future Decode KV coverage to the credible observation gap and bounded
  generation rate while retaining a safe floor;
- add weighted scanner byte concurrency;
- audit vLLM multi-engine/DP and SGLang DP aggregation contracts;
- remove or wire currently unused response-usage code according to the Phase 0
  decision.

Do not bundle these lower-priority changes into the estimator/TPS causal A/B.

## 6. Test-First And Remote Execution Contract

For each behavior-bearing phase:

1. Add a focused failing test that demonstrates the intended semantic defect.
2. Run the red test in the approved remote workbench and record that it fails
   for the intended reason.
3. Implement the smallest coherent vertical slice through the real HTTP
   admission path.
4. Run focused tests, complete Go tests, complete race, vet, build,
   deterministic simulation, and hot-path benchmarks remotely.
5. Re-run property/lifecycle tests for cancel, error, disconnect, timeout,
   backend reset, policy reset, stale metrics, progressive observation
   absorption, and concurrent decisions.
6. Record exact source commit/archive SHA-256, commands, exit codes, remote
   environment, and artifact hashes. Evidence does not survive a behavior
   source change without revalidation.

Do not publish an image after Phase 0 merely because metrics tests pass. The
first publishable v0.12.18 candidate requires all selected behavior phases,
complete remote gates, three reviews, and an exact pushed source identity.

## 7. Three Mandatory Reviews

### Review 1: Model and causality

Verify endpoint request semantics against current vLLM and SGLang source or
stable protocol documentation. Confirm every new estimate changes only its
intended gate. Hold backend metrics constant and prove estimator/TPS candidate
differences cause the expected pre-forward decision or reservation. Separate
QoS-compliant completion goodput from raw admissions and Router retries.

### Review 2: Safety and lifecycle

Audit atomicity, reservations, cache-credit lease/refund, QoS lease lifetime,
all terminal paths, response-body EOF/close behavior, observation absorption,
epoch reset, bounded maps/buffers/labels, scanner saturation, numeric overflow,
and concurrency races. Prove no request content or unbounded identity enters
logs/metrics.

### Review 3: Evidence and release

Audit red/green validity, full remote reproducibility, benchmark provenance,
simulation baseline equality, acceptance thresholds, Git/source/image identity,
registry pullability, Compose diff, PIG-only replacement, Router state, and the
exact live observation window. Do not promote an unmatched Grafana improvement
or a microbenchmark as end-to-end goodput evidence.

## 8. Release And Live Validation Boundary

When and only when the source candidate passes all prior gates:

1. Assign v0.12.18, rerun identity-bearing gates, commit, and push.
2. Build and validate a host-local exact-revision image in the approved remote
   environment.
3. Publish only after isolated image acceptance and verify the registry digest
   is pullable.
4. Re-read the live Compose and Router enabled set. Preserve the exact current
   runtime and an executable restore path.
5. Return the traffic-bearing PIG to the separately verified v0.8.13 fallback
   before a production candidate replacement, as required by the standing
   production workflow.
6. Recreate only PIG. Do not restart vLLM/SGLang, HAProxy, ingress, or the CVM
   without a separate diagnosed need and authority.
7. Verify authenticated readiness, metrics/log/Router consistency, zero hidden
   protection, drained counters, capability agreement, and container identity.
8. Restore exactly the pre-change Router enabled set and run at least a
   30-minute uninterrupted live observer.
9. Compare a time-aligned or matched baseline and candidate using completed
   request goodput, output-token goodput, long-window mean-active TPS,
   running/waiting, queue, GPU utilization, KV, cache hit, preemption, OOM,
   proxy failures, PIG lifecycle failures, protection reasons, request-size
   buckets, and estimate-versus-actual evidence.
10. If a material correctness, QoS, visibility, lifecycle, or goodput defect is
    observed, restore the exact fallback and begin a new red-test iteration.

No production deployment or Router mutation is part of the current plan-writing
step.

## 9. Explicit Non-Goals

- full model tokenizer execution for every request;
- model-specific assets or model-family policy branches in PIG;
- exact chat-template parity as a prerequisite for simple request-size QoS;
- aggregate cache hit used as current-request KV evidence;
- raising KV hard ratio without a measured KV bottleneck;
- lowering the configured TPS reference to make tests pass;
- a global queue-wait increase or TTFT gate;
- a fixed Prefill-release timer;
- Router changes, routing decisions, backend priority injection, or request
  mutation;
- learning Prefill/KV thresholds or an output-length ML model;
- claiming success from fewer 429s, higher KV use, raw admits, or a single-node
  unmatched Grafana comparison.

## 10. Current Progress And Next Action

```text
Plan and evidence consolidation                         complete
Three plan reviews                                      complete
Plan commit and push                                    complete (`c520a18`)
Phase 0 fixed-cardinality evidence                      in progress (base slice green)
Phase 1 endpoint-aware estimator                        pending
Phase 2 cross-tokenizer offline oracle                  pending
Phase 3 bounded TPS-debt simulation                     pending
Phase 4 Prefill lifecycle decision                      pending
Phase 5 portability/resource hardening                  pending
Complete remote source gates                            pending
v0.12.18 executable identity                            not assigned
Published image                                         none
Compose integration / deployment / live acceptance     not started
```

The first Phase 0 slice now exposes cumulative outcome, protection reason and
scope, estimate confidence, Prefill class, Decode fan-out, and Selection input
token buckets. It does not change admission, reservation, proxy, response, or
logging decisions. The next Phase 0 slice must add classifier/streaming shape,
TPS decision subreason, denominator-source, and response-usage evidence before
any estimator or TPS policy change.

## 11. Plan Review Record

### Pass 1: model and causality

The initial draft correctly identified the estimator, TPS lifetime, and
Prefill lifecycle as separate causal changes, but its metrics example combined
outcome, confidence, size, and Prefill class into one unnecessary label product.
The plan now uses orthogonal counters/histograms. It also explicitly limits the
endpoint-aware estimator to a semantic proxy: exact backend chat-template
serialization remains offline oracle evidence or bounded uncertainty, not a
production exactness claim.

Pass 1 status: complete after revision.

### Pass 2: safety and lifecycle

The review traced request classification, atomic decision/reservation,
forwarding, response first byte, outer terminal defer, Controller reconciliation,
and TPS sequence exposure. It found that the TPS window selects the maximum of
backend running, local forwarded, and local response sequence-seconds. Because
forwarded exposure includes Prefill/unabsorbed time and dominates response
exposure, output-lifetime forecast is not the only possible source of TPS
over-protection. The plan now requires source-attributed denominator evidence
and prevents combining a denominator change with the bounded-debt experiment.

The existing outer terminal path still covers success, upstream error,
disconnect, timeout, and proxy failure even when the response-body callback
already terminates success. No timer-based Prefill release or early hard-debt
release was added to the plan.

Pass 2 status: complete after revision.

### Pass 3: evidence and release

The review verified that the plan never treats its six-hour audit, the
unmatched new/legacy comparison, a future microbenchmark, or a source test as a
release. It retains exact separation among plan, behavior source, remote gates,
image, registry, Compose, deployment, Router restoration, and live acceptance.
The new work now has an isolated v0.12.18 branch while preserving the exact
v0.12.17 source and image identity.

The initial draft also said to correct Grafana queries in Phase 0, which could
be read as authority to mutate an external dashboard. It now limits the PIG
scope to truthful metrics, documentation, and replacement PromQL; an external
dashboard change remains separately authorized. No plan step authorizes a
production deploy, backend/CVM restart, Router mutation, or image publication
before its explicit later gate.

Pass 3 status: complete after revision.

## 12. Execution Evidence

The first metrics-contract red test was committed and pushed as
`c390c647fca2e7bc1f3c4153a34de7c79635ed2e`. On the approved f563 isolated
workbench, the exact GitHub archive SHA-256 was
`0d442a1e40d2b6510659df9a962e7736e859fb827796e7b81c20f58dfb8838e7`.
With Go 1.24.13 image
`sha256:e0cffc405270b9114fac7706d07c373727d1b42b0e47c525b9cd1ab1097779ff`,
the focused test exited 1 for the intended missing bounded-metric assertion.
The red log SHA-256 was
`efb353574803e0ab22187153a88cbafe72fb72a396e40a5d1f0969f4c363aeb6`.
There was no compile, dependency, fixture, or harness failure. The official Go
image's configured PATH is lost under this guest's `sh -lc` profile; all valid
gates therefore invoke `/usr/local/go/bin/go` or `/usr/local/go/bin/gofmt`
explicitly. The earlier exit-127 runner attempt is not red evidence.

The base evidence implementation was committed and pushed as
`c29d4746120a8d303237640e00d2761abf4e4d69`; its exact archive SHA-256 was
`35600e13d88d64141e5272cd7b5a9a64820fa49ce31b8d148463cab41766cdea`.
Remote `gofmt -d` returned zero bytes. The three focused tests passed with log
SHA-256 `8b07d96504d921fb2683668bd5b4c5948705f2dda1f87f658d3f84f4449912a4`;
their focused race run passed with log SHA-256
`16cb051d62aeb27110035bc2f0e474a6c3d1de286cea32f21f9e6ff1c4f44b56`;
and the complete `internal/app/server` package passed with log SHA-256
`b5aaed2477a405509b84938d98b753f460a6c4db83e5a5e3e89e5ca4678f755b`.
This is focused source evidence only: no executable identity, image, registry,
Compose, deployment, Router, or live-traffic claim exists yet.
