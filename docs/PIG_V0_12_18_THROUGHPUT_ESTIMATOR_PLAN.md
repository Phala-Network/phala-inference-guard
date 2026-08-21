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
volume. Response-usage evidence must also accept the protocol-valid aggregate
`usage` attached to non-streaming Chat/Completions responses with `n > 1`; the
number of `choices` is not the completion-token count and must not invalidate
that aggregate.

The response observer is optional evidence on the response forwarding path, so
its resource cost must not become a new QoS bottleneck. For 1 MiB and 4 MiB
non-streaming fixtures, the final remote benchmark must report both known and
unknown Content-Length cases. The implementation must use the bounded known
length for preallocation when available, avoid retaining duplicate copies of
large `choices`/`output` payloads, and keep total allocations within
`1.25 * payload + 256 KiB` for known length and
`2.25 * payload + 256 KiB` for unknown length on the pinned Go 1.24 runner.
Latency must remain below the already accepted 100 ms extreme-input ceiling;
the allocation gate is independent and cannot be waived by a low ns/op result.

### Phase 1: Endpoint-aware one-pass estimator

Replace the duplicated generic scans with one strict, allocation-bounded parser
selected by the normalized supported endpoint:

```text
/v1/chat/completions
  messages/content/name/prior tool_calls/tools and audited fan-out fields

/v1/completions
  prompt/suffix and audited completion fan-out fields

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

The protocol audit is pinned to vLLM
`0a21947d710f5aedb1865038ebef20e141b29c58` and SGLang
`8ff9c2b2276e388f94c88be08c644554fa384b6f` as design references, not as a
claim about every deployed revision. Both expose `n` for Chat and Completions;
SGLang Completions additionally exposes `best_of`. For Completions, charge
`suffix` as input context and derive Decode fan-out as
`prompt_batch * max(n, best_of)` when those fields are valid. Unknown or
conflicting duplicate fan-out values are unsupported rather than silently
under-counted. `parallel_tool_calls` describes allowed output shape and is not
a fixed count of Decode sequences.

Both audited frameworks convert `response_format` or equivalent structured
output configuration into constrained-decoding/sampling state, not Prompt KV.
The endpoint estimator therefore ignores it for Prompt size. If a later backend
revision injects such a value into its rendered Prompt, that revision must add
oracle evidence before the production contract changes.

Responses state references need a hard semantic boundary. A non-empty
`previous_response_id`, `previous_input_messages`, reusable `prompt`,
conversation reference, or another body-external context source can add input
that PIG cannot observe. Classify it with a closed unsupported reason and use
the existing conservative request-scoped fallback; do not estimate only the
visible delta and call context/KV known.

Implement the parser as one bounded syntax walk which returns endpoint-neutral
semantic features to the estimator: relevant raw Prompt bytes, lexical text
tokens, tool-schema bytes, message/template count, typed modality count,
explicit token-array shape, output limit, streaming state, and Decode fan-out.
The estimator may apply bounds and margins to those features but must not scan
the body again. Replace the current whole-body upper floor with a relevant
Prompt-subtree floor so ignored model/user/metadata/sampling/debug payloads
cannot manufacture KV pressure. Keep standalone generic estimator helpers only
if a non-production caller still needs them; the HTTP path must use the
endpoint-aware result.

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

Phase 3 uses one narrow experimental seam rather than a production setting.
`NewAdmissionController` and every production caller retain the complete
declared-lifetime policy byte-for-byte. A separately named simulation-only
constructor may inject a bounded horizon into the same Controller transaction;
the horizon must not be exposed through environment variables, the dynamic
policy API, Compose, or request fields during this phase.

Keep horizon selection and budget accounting as separate responsibilities. The
budget gate continues to derive rolling surplus, the post-admit rate, and the
`base + 1` wave limit. A small horizon policy returns only the forecast duration:

```text
declared-lifetime policy:
  known output   -> declared_tokens / projected_per_sequence_TPS
  unknown output -> ineligible

bounded experimental policy:
  known output   -> min(declared_lifetime, control_horizon)
  unknown output -> control_horizon
```

The control horizon never expires a lease. A granted lease stays attached to
the live reservation even when the request runs longer than the soft horizon;
terminal success, cancel, error, disconnect, or timeout moves it through the
existing residual-debt path, and only a covering observation releases it. Thus
one live/terminal liability prevents a second marginal lease and prevents the
same rolling surplus from being spent twice.

Run a horizon matrix rather than selecting a constant in code first. Compare
the exact current policy with bounded candidates over declared and unknown
outputs, short actual completions under large declared limits, actual outputs
that exceed the soft horizon, burst and replacement waves, low flow, waiting,
preemption, stale observations, epoch reset, recovery, and short-to-long and
long-to-short distribution shifts. Record completion goodput, output-token
goodput, sufficiently-long mean-active TPS, sub-reference exposure, scheduler
queue-wait P95, sustained waiting, preemptions, KV fit, maximum running, budget
grants, maximum outstanding leases, leaks, and controller failures. Candidate
admissions are evidence only when these QoS and safety measures also pass.

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
Phase 0 fixed-cardinality evidence source               complete (six slices green)
Phase 0 complete acceptance gates                       complete (`b5a53f6` executable source)
Phase 1 endpoint-aware estimator                        complete (`87cf1e0` executable source)
Phase 2 cross-tokenizer offline oracle                  complete (`7eefd41` source acceptance)
Phase 3 bounded TPS-debt simulation                     in progress
Phase 4 Prefill lifecycle decision                      pending
Phase 5 portability/resource hardening                  pending
Complete remote source gates                            pending
v0.12.18 executable identity                            not assigned
Published image                                         none
Compose integration / deployment / live acceptance     not started
```

The completed Phase 0 slices expose cumulative admission outcome, protection
reason and scope, estimate confidence, Prefill class, Decode fan-out, Selection
input-token buckets, classifier/streaming shape, TPS decision subreason,
denominator source, per-kind estimator validation, bounded response usage versus
declared output-limit evidence, and Prefill response lifecycle evidence. They do
not change admission, reservation, request bytes, response bytes, Controller
reconciliation, or logging decisions. The compatibility-metric documentation
also supplies replacement PromQL and explicitly rejects the obsolete
`running / pig_dynamic_global_limit` interpretation. Phase 0 now requires its
complete deterministic equivalence, cardinality, remote test/race/static/build,
and hot-path performance gates before any estimator or TPS policy change. The
first complete acceptance run on `727bc3e` exposed two response-observer
defects: valid non-streaming multi-choice usage was classified as malformed,
and a 4 MiB response allocated about 39.5 MiB. Both were corrected test-first;
the final `b5a53f6` executable source subsequently passed the complete Phase 0
functional, race, static, build, simulation-equivalence, HTTP byte-contract,
allocation, and latency matrix recorded below.

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
With Go 1.24.13 image config ID
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

The request-shape metrics-contract red test was committed and pushed as
`c5ebd47833c24edef24a32554701a3367ba6aa04`. Its exact archive SHA-256 was
`a96f0bce43fc4f5ae489bd9ab61644fe9de636114ec11cb0c852fac2f9d3f373`;
the test preserved four upstream forwards plus one malformed-JSON local 400 and
exited 1 only for the missing classifier metric. The red log SHA-256 was
`2c85ce7acaee0ce4fd665aeb66ec99986c42b161b0443b61ba76d8f8f191e6ad`.

The request-shape implementation at `cbf9f89` passed its focused behavior tests
but had a non-empty `gofmt -d`, so it is not green evidence. The mechanical
format correction produced final pushed commit
`b166e96790509fba267e5ebfd44b5dd498793ea9`, exact archive SHA-256
`c533734ec3ee4d9b803e3ed73cfe423b0c94002ac2b22dc3e5fb7f88dc468086`.
On that exact source, `gofmt -d` was empty; focused tests passed with log
SHA-256 `6e3c7fa594267377cf54777131f3f4ac9a83bf22c1647df87913b5c327fd1793`;
focused race passed with log SHA-256
`f8783aeef5f5849a9814443158db3a8e53ca19db9dd702f49f7077c74fe329f1`;
and the complete `internal/domain/request`, `internal/app/request`, and
`internal/app/server` packages passed with log SHA-256
`44f97e7595d01292d7867eb3f3c4db63794167a8d18dba6d70a9c25fff539c5a`.
Top-level streaming evidence remains observational only and preserves true,
false, unspecified, invalid, conflicting-duplicate, and unavailable states
without changing request bytes or admission behavior.

The TPS-attribution red test was committed and pushed as
`2ff510f739b541ecde5ca62c796c53123b785885`. Its exact archive SHA-256 was
`bd93786e7e282b030e327fc950922e924b656aa5b1f29f8ddc7294445fe9b036`;
the deterministic warming request remained admitted and forwarded once, and the
test exited 1 only for the missing TPS decision metric. The red log SHA-256 was
`d145b78b80dc18aaaab577b04241f2f1f25626dbc9983ae9948ec54ed321cf8b`.

The first TPS implementation commit `6a7b32c` passed focused behavior tests but
had non-empty formatting output, so it is not green evidence. The mechanically
formatted final pushed source is
`30c6939b942c90c35db48068d36824c26e5bc986`, exact archive SHA-256
`8956c3f56c1b9d1c18829b25a52b34a6625a229147fec2cfe11d87cc540152cd`.
On that exact source, `gofmt -d` was empty; focused tests passed with log
SHA-256 `0ce64b38e68daed5ee3bdd3bfc4cde3a9410c674990c45b12ef4651f0a3089a8`;
complete `internal/admission` plus `internal/app/server` race passed with log
SHA-256 `fadaba9b0bb233cde64cd71b1dd58c6b98c6ba8208b830b2bc62a45beefa5c6e`;
and admission, server, observability metrics, and request-aware simulation
packages passed with log SHA-256
`0dbd36345bde41ba40e62abb23360b623c09ffcdc2341938f264e2c591b58c40`.
The slice records TPS gate result/subreason and raw/selected denominator source
seconds while preserving the existing limit, forecast, maximum-denominator,
reservation, and request behavior.

The estimator-validation contract red test was pushed as
`779c71f4a0af7ef8abd303775fe34d109db62a15`. On the approved f563 isolated
workbench, its exact archive SHA-256 was
`382db3b7341a4aa52c90523e0f8ca12d45941944498f9e64650a5158cc781e56`;
the focused test exited 1 only because the three new bounded estimator metrics
were absent. The red log SHA-256 was
`1a393d34fb2fc32c3935bd2f5886ff6b72c6504875a9c4314965904d3a04879e`.

The initial implementation `505dd86` passed focused, focused-race, and complete
server-package tests but had a non-empty formatting diff, so it is not green
evidence. The mechanical correction produced final pushed source
`6a332381842e6eabad1218f76d1f3681df437395`, exact archive SHA-256
`7df4306b39160e9380a8310a6fa38aaf77e26b9065ba9e51f1008bb337070cc9`.
Its runner used pullable `golang:1.24-bookworm` manifest digest
`sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`
(Go 1.24.13, config ID `sha256:e0cffc405270b9114fac7706d07c373727d1b42b0e47c525b9cd1ab1097779ff`).
`gofmt -d` was empty; focused tests passed with log SHA-256
`dcbeec7ef22ce8c19612d8644db9ea8760b64c02b3f3a4d2260633186658ecd9`;
focused race passed with log SHA-256
`84e5aede3fcdd295053a6e6d39ba9bef893839298e63353a5beff6a85ddcd4aa`;
and the complete `internal/app/server` package passed with log SHA-256
`879215c014d580a76ac5ade727e885ed12f72227f9ce99676f88821ce743e585`.
The evidence distinguishes known and unavailable Selection input, per-sequence
Context upper-bound input, and KV-reservation estimates using only closed labels;
it does not alter HTTP behavior or admission policy.

The response-usage contract red test was pushed as
`fdb3548bca23c39cd84ab1f0d5ef10c2f1364936`. Its exact archive SHA-256 was
`c5b6d3c2a599dfd0da16f8ff8a269cafb86fc81adbf08e206f248c68dbd14cc2`;
the test preserved three successful response bodies byte-for-byte, kept one
protected request out of the upstream, and exited 1 only for missing usage
evidence. The red log SHA-256 was
`475034c07d8cfcfeec06a82073d47f71f89fc795372b9a4c59562322023ef5c8`.

The first production wiring at `dd30da2` is not green: its remote run exposed
both formatting drift and two protocol defects. Omitted `stream` was incorrectly
treated as unknown instead of the protocol default `false`, and an intermediate
SSE usage event could invalidate the final usage event. Those defects were
corrected and the completions-only source became green at `d761868`, but the
second protocol review found that default QoS scope also includes
`/v1/responses`. A separate Responses API red test was pushed as
`83ac9792f07f1c57978400d871522e9ffdf06f5c`; archive SHA-256
`bb5a404bc3a41afb45451fc20aece7eb223d11b15ea94a0f3e7ac1e16238f57a`,
test exit 1, and red log SHA-256
`cb5ff48b6847464298ded2e763749876f13373bee0ab3dd868d3443f0ef5d9e2`.

The final pushed response-evidence source is
`ec89bd89b203dc6696a715c0e600565aba74c770`, exact archive SHA-256
`716a008ef3be7262a4166c5269d98265ed13fa79cc6baccdf6baa52062e16987`.
It selects a closed parser contract for Chat/Completions versus Responses,
supports bounded JSON and SSE usage, preserves upstream bytes, never forces
usage through request mutation, and records unavailable, malformed, and
censored samples separately from valid observations. `gofmt -d` was empty;
focused tests passed with log SHA-256
`feb83ca27262df5b9fca68bd53bb8378d830b61beeaab070eff1f8056d007ec2`;
focused race passed with log SHA-256
`25228b157d67cc0ca85703155ab6ed2d0368920899f16896be3c304eba6be9f2`;
and complete `internal/infra/openai` plus `internal/app/server` packages passed
with log SHA-256
`f219fab3e66aa0e93e11114c74e990bf07d63aae84fd31a4fd7ea79b83f4cfde`.
These metrics are calibration evidence only: successful responses that omit
usage remain `unavailable`, pre-forward rejects and interrupted responses remain
`censored`, and neither class is counted as estimator accuracy.

The Prefill-lifecycle contract red test was pushed as
`d89dfee0c1970d140612f2f8c1702bdafda19323`. Its exact archive SHA-256 was
`72f509e140dfb56966df03dd402a8a25f64989efcf6e5eb6bf3e21da76a69020`;
the HTTP fixture preserved one body-bearing success, one bodyless forwarded
success, one pre-forward protection, and exactly two upstream calls. It exited
1 only for missing lifecycle metrics; red log SHA-256
`99b2076507114114c841ab5e114a712adea26e8ea6dc878033380f34a98799b3`.

The final pushed Prefill-lifecycle source is
`2ab1dfcd269f907339cbcbffb563be4308bee06e`, exact archive SHA-256
`485ef67945691cf6e5b8963544fc52358c9fb1c5e3e75d1ad8cc9fd96f1bf836`.
It measures response-body first-byte-to-terminal duration and separately counts
forwarded terminal-before-first-byte and pre-forward terminal cases across
single, single-Prompt fan-out, Prompt batch, and batch-plus-fan-out shapes.
It does not release or shorten Prefill/KV debt. `gofmt -d` was empty; focused
tests passed with log SHA-256
`19354b3313a2e2682de4e9ec4df52f5fecf37841a6cffca1315381f8899732de`;
focused race passed with log SHA-256
`67d99ba7e7066cc7adc99750605e82d3b0b3cb40322ae54febc0f708f83e9cc9`;
and the complete `internal/app/server` package passed with log SHA-256
`6d8cc13ef14c1d11ee83b62ca85697fa8858fca8aa50334514b335a355259c27`.

### Phase 0 final acceptance and review closure

The Phase 0 complete-matrix run on `727bc3e` was functionally green but was not
accepted: its non-streaming response parser rejected valid aggregate usage for
`n > 1`, and its 4 MiB benchmark allocated about 39.52 MiB/op. The corrective
multi-choice contract was pushed as
`f5efe3e2da319126012df79ac1125cea63672296`; archive SHA-256
`70656f90e2f40aa740ef4e293696bc9b12e051e139252631523b5db36e3435ee`.
On f563 it exited 1 only because the old parser returned `malformed`; red-log
SHA-256 was
`4fe8228c80f5addc647835a403b138625decb41479c37fa14f99dae2df4c9e33`.

The allocation correction was pushed as
`ba8909f5215e7a20fae90dcf58491dcc35098dcc`. It uses a bounded upstream
Content-Length only for preallocation, a doubling bounded buffer when length is
unknown, `json.Unmarshal` into narrow envelopes, and a non-retaining
`choices` shape validator. It accepts non-empty multi-choice arrays without
mistaking choice count for usage, while streaming final Chat/Completions usage
still requires an empty `choices` array. Its archive SHA-256 was
`6caa0fc16e1a2ae4b61c3413496f3562dc3e0b324fd361b4fc4d2467b4d1fddc`;
focused, focused-race, complete-package, and benchmark log SHA-256 values were
`6770158b497a435b081dce01dc2350d51a925e3b426c0ca6f754b5145c3e0548`,
`f24fd376d3f00a065c90dbae0580d682202b3696536e813ee84a3d56068de93d`,
`2b3096f04b5edcc1ead2461d198e59aa4bc40aa0bfa78e01546bbdbb43c1f6cf`,
and `43cf5fd74c58de08344a92502e5c69a78df16e3044cbfec35e38d417ff362fab`.

The final benchmark source is
`b5a53f69fdd7a4d969202348a51e04d55d49ef44`, which puts large data in actual
Chat/Completions `choices[].message.content` and Responses
`output[].content[].text` fields rather than an ignored padding field. Its
archive SHA-256 was
`52eda55a4e0112811d99f8a518568be25cf84fdf82f830bde3c7ec7987d656a7`.
Across three one-second samples on the pinned single-CPU Go 1.24.13 runner, the
worst 4 MiB unknown-length result was 8,381,483 B/op and 27.576 ms/op; the worst
known-length result was 4,195,323 B/op and 26.461 ms/op. The corresponding 1
MiB maxima were 2,089,851 B/op and 1,049,404 B/op. Chat and Responses both pass
the plan's independent allocation bounds and the 100 ms extreme-input ceiling.
Focused and benchmark log SHA-256 values were
`4089d818ee04106f5042cb9df1e77a3333f5a3f8b26bcd6718edba509341a577`
and `c826bb3d26cffed63dc781ccec5865f55e6410bde755b03d194da39ee541f7df`.

The complete Phase 0 acceptance directory is
`/var/volatile/dstack/persistent/pig-v01218-workbench/b5a53f6-phase0-final-acceptance`.
The exact v0.12.17 baseline archive SHA-256 was
`9e818de439a75fc5c983fde53f480ff900bec203684be2cf9a19fb1739fe47b9`.
All exits were zero for repository formatting, `go test ./...`,
`go test -race ./...`, `go vet ./...`, `go build ./...`, both deterministic
simulations, byte comparison, current/baseline shared HTTP byte contracts, and
estimator/classifier benchmarks. Material log SHA-256 values were:

```text
go test ./...       f6f462091f8a28b3642892972dbe04b8a90d1acf071d67ce79beeb0d3d48ec27
go test -race ./... b9790d91d26548131537438340db1c86240ffcffb3caa435702102255b872d8f
gofmt/vet/build      e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
HTTP current         6ddfa1a0028f1d34f64a99895efd3b89471dcc45b2a968206a71c869f2dbe924
HTTP baseline        7436abf2bb00a038fd5fe990820de0fd25c0b2c5b69d73409e7e6a8ff6036ee4
estimator benchmark  3db9b507da2ad10e5161509cebb558b41a4db30c4c1f2653a6342aecfabed97f
classifier benchmark dd4c6908d9d4cab69cac001bdbea24cdc21adc17cf0414e491a565d43e990407
```

The candidate and baseline simulation artifacts are byte-identical, both with
SHA-256
`2f29cb429523018c4f68f01fea03179e219b8d0919e32e97140450b6fced30e1`.

Final review pass 1, model and causality: response usage remains optional
calibration evidence and is not consumed by admission. The unchanged
simulation artifact and shared HTTP contracts confirm that Phase 0 does not
change request decisions, reservations, or bytes. Status: complete.

Final review pass 2, safety and lifecycle: retained JSON is capped at 4 MiB;
known-length preallocation is bounded; unknown length has bounded geometric
growth; large `choices` and `output` are not retained a second time. Truncated,
errored, disconnected, and unconsumed responses still remain censored rather
than accuracy samples. Complete race passed and fixed-cardinality metrics did
not change. Status: complete.

Final review pass 3, evidence and release: red failure had the intended protocol
cause; all green evidence names exact commits, archive hashes, runner limits,
and log hashes. This closes Phase 0 source acceptance only. No v0.12.18
executable identity, image, registry artifact, Compose change, deployment,
Router mutation, backend restart, or live-traffic claim exists. Status:
complete.

### Phase 1 final acceptance and review closure

The accepted Phase 1 executable source is
`87cf1e0cba4d9abe8b55821d994de2af1902517c`; archive SHA-256 is
`fcbcd5ba74e5b12a906fe3c52c2b836f386a529961b9bfae5c9bf775514610b7`.
It was tested on f563 in the pinned Go 1.24.13 image with one CPU, 4 GiB memory,
512 PIDs, `GOMAXPROCS=1`, read-only source, and shared module/build caches.

The endpoint-aware production HTTP path now performs one bounded syntax walk
for Chat Completions, Completions, and Responses. It returns independent
aggregate Selection, maximum-sequence Context, and KV-reservation features;
keeps endpoint-local output controls and audited fan-out semantics; estimates
decoded JSON strings without complete decoded allocations; excludes ignored
controls and typed multimodal transport payloads; and closes visible versus
body-external Responses context explicitly. Request and response bytes remain
unchanged.

The escaped output-limit allocation test was introduced in `078c562` and
failed because the previous `strconv.Unquote` plus `strconv.Atoi` path allocated
twice. `dd62837` replaced it with incremental ASCII JSON-escape and checked
decimal parsing. The final focused allocation/strictness run passed with log
SHA-256
`b720c897bb93323fb6bf5f48f1c097a33bff7cd019a2877aa4ee6f85580ee310`.

Review pass 1 found three additional Responses semantic gaps after the first
green matrix: `input_file.file_data` inflated Prompt/KV with base64 transport
bytes; nested `item_reference` was incorrectly treated as visible context; and
whitespace-formatted empty `previous_input_messages` incorrectly forced the
external-context fallback. The first two red tests were committed as `08c0168`;
archive SHA-256 was
`aefc440ff7382f4087d825b3618d198659bd1f6b21c65a15cc2f083ef909b60e`
and red-log SHA-256 was
`fb2acc5c58c158cc72f5d3156f1ca94a96c6696988303ea5eabb6763e81610a9`.
The isolated empty-context red test was committed as `690e927`; archive
SHA-256 was
`02e14c552ff8e89d24bbd13d77e09bc53e5edbd42336fee7a26a71ce974f36e5`
and red-log SHA-256 was
`6e1d7b465e907e66206726899ca6d06271cb28cf1a0286f314ccf5233fb3d6bb`.
`87cf1e0` fixed all three without broadening dynamic labels. Focused green and
five-package green log SHA-256 values were
`34ed2e7ae2e8a39c820393b1f58bc798113ecb068c8be186a1aacaee6016bc01`
and
`047476b7ebea836f2137378d74171186c54cb006da0b3ad1f7b6432732ea1020`.

Review pass 1, model and causality: endpoint-specific Prompt fields, decoded
text, tools, modality parts, Completion batching/suffix/`max(n,best_of)`, Chat
`n`, Responses single-sequence behavior, output-limit locality, aggregate versus
maximum sequence estimates, and tighter batch reservation rounding were traced
from the HTTP classifier through `RequestEstimate`. The three defects above
were corrected and rerun. Status: complete.

Review pass 2, safety and lifecycle: the parser retains the 128-depth and 4 MiB
bounds, performs no complete decoded-string allocation, preserves the request
body and Content-Length, uses fixed unsupported-reason labels, propagates hidden
context only inside request classification, and does not change reservation
release or Controller lifecycle. The final five-package race log SHA-256 was
`67af9861807ed36e6002062a69dec0af60957f121fa18efed0ffd55f4e198834`.
Status: complete.

Review pass 3, evidence and release: all new behavioral corrections have
intended red causes, exact green source/archive provenance, and final-HEAD
performance and full-repository evidence. The final 4 MiB plain and escaped
classifier p50/p99 results were 24.901/26.879 ms and 25.003/26.026 ms; latency
log SHA-256 was
`e827ed788e3b1da14764eca80cd985b240df7d176fb4c5b1a7877afe5705dc6f`.
The five benchmark samples were 24.982--28.331 ms/op, 88,194--97,153 B/op,
and 17 allocations/op; benchmark log SHA-256 was
`a96c7b9fdba4dabfd0e711e2adde43d135b0a1297a923a8a7ff939b344499890`.
Status: complete.

The final complete repository gates all exited zero. Material log SHA-256
values were:

```text
gofmt -d .            e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
go test ./...          56135ac06bd85845079f2c667da94e736365f2c2bc778e04ac8dbe5d483e526c
go test -race ./...    c3edf074e7cf05e5907ae39993da9e2c9e43aafcb493b0ba7edc59bc321f2993
go vet ./...           e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
go build ./...         e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

This closes Phase 1 source acceptance only. Phase 2 evidence is recorded below;
bounded TPS-debt simulation, Prefill lifecycle decision, portability hardening,
executable identity, image, registry publication, Compose change, deployment,
Router mutation, and live-traffic acceptance remain unverified or not started.

### Phase 2 final acceptance and review closure

Phase 2 replaces the obsolete single-Gemma exact-token test with a pinned,
offline cross-tokenizer oracle. The production PIG binary does not load the
manifest, tokenizer assets, model revisions, or any model-specific vocabulary.
The production estimator remains a model-neutral bounded syntax and lexical
walk; exact tokenizers are test-only measurement instruments.

The final oracle generator source is `8700326`. Its source archive SHA-256 was
`853818b6a26b04182bafe1fb5bb8343870387edf697c94a2daf63c505e5ba1e2`.
The accepted manifest SHA-256 was
`1375bb0f1d803cbfdd12f34e6a1c03d7c875ae14af7606d84effd51cc86f8dea`.
It used Python 3.12.3, Transformers 5.15.0, tokenizers 0.22.2, and the isolated
image
`ghcr.io/phala-network/vllm-openai@sha256:a4b6403697a8d39da60c0ba4e326a71cd6fd1c7c94637330c8be6067fa435d0c`.
The pinned oracle families were Qwen BPE, Mistral BPE, DeepSeek, and
Gemma/SentencePiece. Gemma is one of four offline test families and is not a
production algorithm parameter.

Two earlier generator results were explicitly rejected. The first omitted
`docker run -i`, so Python read EOF from stdin and produced an empty, invalid
success. The second treated a Transformers 5.15 `BatchEncoding` mapping length
as token count. `ab0a368` normalized Mapping, sequence, and shaped outputs and
added a large-text sanity check. Only the manifest hash above is accepted.

The hard-bound test was committed red as `031a2cf`. Its archive SHA-256 was
`5bd5d86b29a27ee91803b1e7d7a4408fd61c555133a59eb771e8f251f6215e75`
and red-log SHA-256 was
`2a089f8a9ac34e46f0337d7ac1718ad7a366fb3ca1080dc827c4dab833a0dc32`.
It failed for Mistral repeated spaces, Mistral and DeepSeek mixed whitespace,
and a short metadata-heavy Chat request whose template overhead exceeded the
old estimate. The focused portable-whitespace test was committed red as
`64c4a13`; archive SHA-256 was
`186447c3e511603805c1f96ff85d427b8f9ffd014e31a7fc89c3fe99583b75ea`
and red-log SHA-256 was
`6bed44d9b5ba791db04121b97c2a5c18e5cb22422728e987b47e653d854ad37f`.
It failed only because pure spaces still used one token per 32 bytes.

`f1fbb7b` added model-neutral portable whitespace bounds, a fixed internal Chat
template base, and a denser tool-schema Selection estimate while preserving an
independent conservative KV upper bound. `622739b` updated the superseded
one-token-per-32-spaces unit contract. `e60a168` added a pinned v0.12.17
comparison gate, representative-corpus allocation and concurrency gates, and a
hot-path benchmark. `bb6d8bb` removed the obsolete single-Gemma oracle. The
final `7eefd41` short-circuits whitespace-shape checks after the first ordinary
character, removing unnecessary work from the common prose path without
weakening pure-whitespace bounds.

The v0.12.17 executable baseline was recomputed from exact commit
`0091241bc9edc30f0f7ff50010504225d3fa14c8` in the same remote runner. The
baseline archive SHA-256 was
`091ca81b140d44912ec1259371344e46aae826667bf8d4bd80eb2c1564994537`;
the formatted measurement harness SHA-256 was
`cd50fbbe5ee15afb1501e078d839087ad8c4d79ec5905215cdbf7951e7715995`;
and the baseline log SHA-256 was
`24585b715728e30ce9828079345d7de09f035aef030ce6e9d3e4b11e9b92edcf`.
For escaped CJK, metadata-heavy, and tool-schema fixtures, v0.12.17 KV
reservations were 26,175, 18,446, and 13,310 tokens. The final candidate values
were 7,826, 40, and 9,479 tokens while remaining above every declared hard KV
oracle. Across the twelve fixture/family comparisons, KV overestimate median
and p95 fell from 18,436/21,047 tokens to 2,677/3,527 tokens.

The final Phase 2 executable source is
`7eefd416081dc4140b5a41567acbed5ca86470eb`. Its source archive SHA-256 was
`3c3630ed15050798fcddd915d16974d8ebb2c0af552f3b2cce973564065a04e0`.
All final executable gates ran on f563 in
`golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`
with Go 1.24.13, one CPU, 4 GiB memory, 512 PIDs, `GOMAXPROCS=1`, read-only
source, and shared module/build caches.

The representative 14-fixture production estimator, including endpoint parse
and semantic estimation but excluding prebuilt test-body construction, ran five
samples at 79.507--80.332 microseconds per request with zero bytes and zero
allocations per operation. The 64-worker mixed small/large corpus passed both
native and race builds. Acceptance, acceptance-race, and benchmark log SHA-256
values were:

```text
acceptance            8d690f883b8cd345f641f485c5fead8074a94ff3b3721c7de8592b3f7853e97c
acceptance race       dfb742e59dca0a69e5be999ec24a019074200cc21150863c849c02fb2dc0da4e
representative bench  f8ee7d3526baf37ac0f9c8a8cc7790a6957432c1f947a3c3bf0a16ed6bd8cb59
```

The final 4 MiB plain and escaped classifier p50/p99 results were
24.740/27.015 ms and 28.376/29.848 ms. The common plain-body benchmark ran five
samples at 24.643--24.899 ms/op, 88,195--101,305 B/op, and 17 allocations/op.
All extreme estimator fixtures remained below 100 ms p99 with zero estimator
allocations. Estimator, classifier-latency, and classifier-benchmark log
SHA-256 values were:

```text
estimator maximum body  17af50fda3dc49a74730480647c4fda02bc5b0aa534e0987edcc2863dee8b994
classifier latency      165ce2bd758cd1015c59576603298da041c47ca316d6d4d9a712d545ba40ed8c
classifier benchmark    4f8955bd4ee0dabb5674cb351b6269fe51d6fd01d29127a2300b0bec431c61a7
```

Review pass 1, model and causality: Chat Completions, Completions, and Responses
are parsed before forwarding; their endpoint-aware cost becomes
`RequestEstimate`, is passed by `proxy.go` to the admission runtime, and is
consumed by the Controller's atomic `Admit` decision and reservation. The
offline oracle is embedded only by a `_test.go` file. No tokenizer asset,
tokenizer revision, or model-specific rule enters the production binary. The
new estimates therefore change the pre-forward counterfactual rather than
creating disconnected telemetry. Status: complete.

Review pass 2, safety and lifecycle: the hard KV reservation remained
independent from Selection and exceeded all declared oracle counts; explicit
token arrays remained exact at aggregate/maximum 384/256; pure spaces use at
least one token per 16 bytes; decoded mixed whitespace uses one token per byte;
ordinary prose retained its prior tight range. Checked arithmetic, request-body
preservation, bounded parser depth/body size, unsupported-shape fallbacks,
zero estimator allocations, 64-worker concurrency, and race gates all passed.
This phase changes request-local estimation only and does not alter reservation
release, cancellation, timeout, backend-epoch, or shutdown lifecycle. Final
five-package and five-package-race log SHA-256 values were:

```text
five packages       349150d25f09247f4198996466fee49019d25a6c6fbc3262f015b33d32a7c302
five packages race  bf408ae3a62185ed3c3a0b663195a75dae089db5247329faa229c88490b92159
```

Review pass 3, evidence and release: intended red causes, invalid oracle runs,
the exact v0.12.17 baseline, final source/archive identity, performance,
allocation, race, and complete repository gates were reviewed separately. The
final complete repository gates all exited zero:

```text
gofmt                  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
go test ./...           d2410cb9c24f1efc743b3e06813594d26e82d1421e3b6a99740f948d5c18c2be
go test -race ./...     a3d97e6598bf8b7200492f39b8aa0c28148d5f3267e92c42042efa3fa5335274
go vet ./...            e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
go build ./...          e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

Status: Phase 2 source acceptance complete. This does not assign the
`PIG-v0.12.18` executable identity and does not authorize an image build,
registry publication, Compose change, deployment, Router mutation, or live
traffic. The next implementation phase is Phase 3 bounded TPS-debt simulation.
