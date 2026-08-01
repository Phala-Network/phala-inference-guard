# PIG v0.9.2 Tokenizer-First Predictive Admission Plan

Status: builder-validated PIG-v0.9.2 source release candidate. The v0.9.1
source-only candidate was not releasable: its reconciliation and simulation
treated the complete decode horizon as already materialized after a metrics
sample, and its request/profile contracts did not fail closed on every
unsupported shape or identity drift. All v0.9.1 green evidence below is
retained as historical provenance only and is not inherited by v0.9.2.

Supersedes: `PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`

Version target: PIG-v0.9.2

Release-candidate source version: PIG-v0.9.2 reached on the immutable builder
archive recorded in Section 19.18. It remains an unpublished and undeployed
source candidate; no image, registry artifact, or live runtime is implied.

Implemented modes: off, shadow, and enforce; no live mode was enabled

Routing: out of scope

Production deployment: out of scope

Executable validation: remote builder only

## 1. Decision and objective

The cache-aware design is cancelled. PIG will not predict prefix-cache hits,
track cache residency, hash token blocks, maintain prefix references, model
eviction, or grant capacity from expected cache reuse.

The new controller starts from one reliable fact available before forwarding:
the backend-equivalent input token count. It combines that count with the
request's bounded output horizon, the latest upstream state, and PIG-owned
reservations that may not yet appear in upstream metrics.

The question answered before forwarding is:

> If this request is added now and all of its input is conservatively treated
> as uncached, will projected KV use and user-visible generation performance
> remain inside the configured safety and SLO boundaries?

The optimization objective is:

~~~text
maximize completion-token goodput

subject to:
  projected KV upper bound <= protected KV capacity
  projected existing-user TPS lower bound >= target
  projected new-user TPS lower bound >= target
  projected TTFT and TPOT upper bounds <= configured SLOs
  preemption proxy and hard-cap violations do not increase
  no reservation leak, double release, or metrics double count
~~~

Raw admitted request count, prompt TPS, and total TPS are diagnostic metrics;
they are not the objective. A request contributes goodput only when its modeled
single-user TPS, TTFT, TPOT, and hard-cap constraints pass.

## 2. Non-negotiable scope

PIG predicts intake for its one configured local upstream. It does not select
a CVM, route by prefix, move traffic, or emit routing hints.

The implementation changes PIG only. vLLM source, templates, logs, and metrics
may be read as protocol or oracle references, but vLLM is not modified, built,
published, or deployed.

No production CVM is deployed, restarted, or sent benchmark traffic during
this plan. Windows is limited to editing, Git operations, static diff review,
and evidence inspection. Go, Rust, tokenizer, simulation, race, formatting, and
benchmark execution runs on the remote builder.

No Docker image is built or published until the final builder gates pass and a
separate user instruction authorizes that stage.

## 3. Explicit no-cache invariant

Every supported request is costed as cache-cold:

~~~text
cached_input_tokens = 0
uncached_prefill_tokens = exact_input_tokens
cache_discount_tokens = 0
~~~

The production tokenizer hot path returns a token count, not token IDs or block
digests. The production admission path must not construct or call
`CacheMirror`, inspect vLLM prefix-cache metrics, or consult prior request
prefixes.

An identical request has the same input and prefill cost regardless of whether
the same prefix was seen earlier. Prefix reuse may make the real backend faster,
and later observations may improve the QoS calibration for future requests,
but PIG never assumes that reuse before admission and never turns it into KV
headroom.

The current branch contains experimental cache-aware types and tests. They are
not production-reachable. Migration is test-first:

1. add a failing application test proving that changing prefix history cannot
   change request cost, decision, or reservation;
2. introduce a count-only analysis and count-only coordinator transaction;
3. switch the real HTTP adapter to the count-only path;
4. prove off mode constructs no tokenizer or predictor work;
5. remove the obsolete cache-aware application path and its now-unused
   digest/residency code after the count-only path is builder-green.

No compatibility adapter may silently synthesize empty cache state while still
running the old cache state machine. The new path must be structurally
cache-free.

## 4. Request support and exact token count

The first profile supports only the already verified Gemma4 text subset:

- `/v1/completions` with one string prompt;
- `/v1/chat/completions` with verified user, assistant, and initial
  system/developer text messages;
- string content and verified text-part arrays;
- one output horizon from `max_tokens` or `max_completion_tokens`;
- a conservative profile default when the output horizon is omitted;
- `n=1` and no `best_of`, parallel choices, tools, reasoning, multimodal
  content, prompt adapters, cache salt, suffix, or pre-tokenized prompt arrays.

Unsupported or ambiguous syntax returns a typed predictive unknown. In shadow
mode it does not reject or rewrite the real request; existing PIG control stays
authoritative. It receives no tokenizer-based extra headroom.

The renderer and tokenizer are bound to immutable profile identity:

- served model and model revision;
- tokenizer and tokenizer-config SHA-256;
- template SHA-256 and renderer compatibility version;
- backend kind/version/source revision/image digest;
- request-class special-token policy;
- model maximum length and block size;
- predictor version and backend epoch.

Startup fails closed in shadow when a required asset is missing or a hash or
identity differs. The already measured five Gemma4 text oracle cases may be
reused, but the new hot ABI exposes only `exact_input_tokens` and bounded timing
metadata. Token IDs remain builder-only parity evidence.

## 5. Capacity inputs

The simple controller needs one immutable KV capacity in tokens for each exact
backend profile:

~~~text
max_kv_tokens
~~~

For vLLM this value is taken from a pinned startup/configuration artifact for
the exact model, tensor-parallel layout, KV dtype, block size, and memory
configuration. It is not guessed from GPU model name. If a verified vLLM metric
exports total KV capacity, it may be cross-checked; a utilization ratio alone
is not a capacity source.

Runtime observation supplies, when valid:

- KV utilization ratio or used KV tokens;
- running and waiting sequences;
- recent completion TPS or per-user generation TPS;
- TTFT/TPOT samples;
- preemption counter delta;
- sample start/end time, backend epoch, and monotonic poll sequence.

If `max_kv_tokens`, backend epoch, or a required sample is missing, stale, or
identity-mismatched, predictive admission returns unknown and grants no extra
headroom. Hard emergency guards from current PIG remain independent.

The hash-pinned schema-2 profile also owns the observer timing contract:

- metrics poll interval;
- maximum sample age;
- metrics request timeout;
- preemption cooldown.

These values are not inherited from the older dynamic/KV environment settings.
The same manifest therefore produces the same freshness and cooldown behavior
across processes. A vLLM preemption-counter reset invalidates the prior fresh
sample immediately and requires a new stable sample before prediction resumes.

## 6. Per-request prospective cost

For one supported request:

~~~text
input_tokens = exact backend-equivalent tokenizer count
output_upper = validated requested maximum or conservative profile default
context_upper = input_tokens + output_upper
request_kv_upper = round_up(context_upper, block_size)
request_uncached_prefill_upper = input_tokens
request_decode_sequences = 1
~~~

The request is unsupported for predictive fit if `context_upper` exceeds the
model maximum length or if any addition/rounding overflows.

No learned value can reduce `request_kv_upper` or
`request_uncached_prefill_upper`. Learning is restricted to QoS forecast
residuals.

## 7. Event-driven virtual state

Metrics are delayed. PIG therefore maintains only a small reservation ledger,
not a cache model.

Each accepted shadow counterfactual records:

- request ID;
- exact input tokens;
- output upper bound;
- rounded KV upper bound;
- one decode sequence;
- admission sequence and time;
- prefill-complete state;
- terminal cause;
- scheduler prediction identity for later attributed observation.

Immediately before a decision:

~~~text
projected_kv_upper =
    observed_kv_upper
  + reservations_not_proven_absorbed_by_the_sample
  + candidate_request_kv_upper
  + configured_error_margin_tokens

projected_decode_sequences =
    observed_decode_sequences
  + unabsorbed_reserved_decode_sequences
  + 1

projected_uncached_prefill =
    observed_or_reserved_unfinished_prefill
  + candidate_exact_input_tokens
~~~

Sample watermarks classify a reservation as absorbed, unabsorbed, or
ambiguous. Ambiguous state uses the larger safe upper bound. Completion,
cancellation, disconnect, timeout, upstream failure, and local QoS reject
release exactly once. A completion known to PIG can reduce virtual work before
the next metrics poll without claiming a cache hit.

Check, predict, and reserve is one atomic transaction. Concurrent near-capacity
requests cannot both observe the same free capacity.

## 8. Simple QoS predictor

The predictor deliberately has few features:

- exact input-token bucket;
- requested output-horizon bucket;
- post-admit decode-sequence count;
- projected KV ratio bucket;
- unfinished uncached-prefill tokens;
- observed running/waiting bucket;
- backend/profile identity.

The static prior is a conservative service curve fitted from builder
simulation or later authorized isolated-backend evidence:

~~~text
aggregate_completion_tps_lower =
    base_decode_tps_lower
  - prefill_penalty_per_1k_tokens * projected_uncached_prefill / 1000

new_user_tps_lower =
    aggregate_completion_tps_lower / max(1, projected_decode_sequences)

existing_user_tps_lower =
    aggregate_completion_tps_lower / max(1, existing_decode_sequences)

ttft_upper =
    base_ttft_upper
  + ttft_per_uncached_token * projected_uncached_prefill
  + ttft_per_waiting_sequence * projected_waiting

tpot_upper =
    base_tpot_upper
  + tpot_per_existing_decode_sequence * existing_decode_sequences
~~~

All arithmetic is saturating and predictions carry confidence and profile
identity. Static cold-start values must be conservative.

One bounded online residual calibrator may adjust TPS downward/upward and
latency upward/downward only after a minimum number of fresh, sufficiently
attributed samples in the same feature cell. It has:

- explicit minimum and maximum samples;
- lower/upper residual quantiles;
- maximum sample age;
- bounded multipliers;
- backend epoch and predictor version;
- invalid/out-of-distribution fallback to the static prior.

The learner is considered active only when its pre-forward output changes a
forecast and, for a fixed request/current state, can change a decision or
reservation. Aggregate metrics reconcile virtual state but cannot fabricate an
individual request's TPS, TTFT, or TPOT outcome. Requested output length is not
an observed completion outcome.

Feedback calibrates future predictions; it is never the late trigger intended
to protect a request that has already been forwarded.

The integrated HTTP path currently has one per-request outcome that is both
semantically valid and attributable without changing upstream request bytes:
semantic streaming TTFT. It is recorded at first semantic output but submitted
to the learner only if that owned reservation later terminates as a successful
completion. Local reject, cancel, disconnect, timeout, upstream failure, late,
duplicate, and wrong-identity observations cannot train the model.

PIG does not infer actual completion tokens from `max_tokens`, SSE chunk count,
or aggregate backend metrics. Consequently live TPS and TPOT residual cells
remain on the conservative static service curve unless a future request already
carries a reliable, backend-produced, per-request completion-usage outcome.
This is an intentional non-fabrication boundary, not a claim that TPS/TPOT are
unprotected: their static post-admit forecasts are still evaluated before
forwarding.

## 9. Decision order

For each supported request, under one transaction boundary:

1. validate profile, sample freshness, and backend epoch;
2. render and count tokens once;
3. compute cache-cold request cost;
4. merge observed state with unabsorbed PIG reservations;
5. reject the counterfactual if projected KV exceeds the protected budget;
6. predict existing-user TPS, new-user TPS, TTFT, TPOT, and risk bounds;
7. reject the counterfactual if any SLO/risk bound fails;
8. atomically commit the reservation only for `fit`;
9. reconcile semantic and terminal events exactly once;
10. admit attributed outcomes only for the matching owned reservation.

In v0.9.1 shadow, `fit`, `risk`, and `unknown` are observations only. Existing
PIG QoS remains client-visible and authoritative. Shadow must not change status,
headers, body, routing, queue duration, or upstream request bytes.

## 10. Body capture and latency budget

Predictive capture is independent of the smaller output-field classifier. It
has an explicit maximum body size, total in-flight byte budget, concurrency
limit, and deadline. It restores exactly the bytes read to the forwarded body.

Bodies outside the validated synchronous tokenizer envelope return unknown.
The in-process C ABI is retained only if builder measurements prove that its
worst supported call fits the deadline and cannot commit a reservation after
timeout or close. Otherwise the supported envelope is narrowed; this plan does
not add a helper process unless measurements show it is necessary.

Initial builder gates:

| Operation | Gate |
|---|---:|
| Off-mode added work | zero tokenizer/predictor calls |
| Small supported request tokenize p95 | <= 1 ms |
| 64 KiB supported text tokenize p95 | <= 25 ms |
| Count-only analysis vs retain-ID analysis | no slower at p95 by more than 10% |
| Predict and atomic reserve excluding tokenizer p99 | <= 1 ms |
| Deadline/close race | zero late reservations and zero leaks |

These are engineering gates, not production latency claims.

The completed production-tokenizer benchmark supersedes any interpretation
that long prompts are sub-millisecond. Steady-state count-only p95 was:

| Exact input tokens | Count-only p95 |
|---:|---:|
| 49 | 52.539 us |
| 3,074 | 8.612 ms |
| 24,578 | 132.639 ms |
| 65,538 | 587.303 ms |
| 131,074 | 1.516 s |

Tokenizer load time is excluded from per-request steady state. The deterministic
TTFT ground truth charges these p95 values to exact-token policies, while the
static predictive TTFT curve is configured as a conservative upper envelope.

## 11. Test-first phases

### Phase 0: freeze the simplified contract

- mark the cache-aware plan superseded;
- capture current threshold/KV-only baselines;
- add a red proving prefix history cannot change count-only cost;
- add a red proving no cache object or digest allocation is reachable from the
  production factory.

### Phase 1: count-only tokenizer

- retain strict Chat/Completion rendering and pinned final-ID oracle tests;
- add a count-only Rust ABI and Go wrapper;
- prove request-class special-token parity;
- prove no token IDs, block digests, prompts, or request IDs are retained or
  logged on the hot path;
- measure count-only latency and memory on the builder.

### Phase 2: count-only atomic reservation

- implement cache-cold request cost;
- implement sample-watermarked virtual state;
- atomically check and reserve KV, prefill, context, and decode sequences;
- race-test duplicate IDs, simultaneous fits, terminal events, and sample
  reconciliation;
- prove completion-before-poll can reopen capacity without undercounting.

### Phase 3: TPS/TTFT/TPOT prediction

- implement the conservative static service curve;
- add bounded residual calibration;
- prove learned history changes a pre-forward forecast/decision with current
  metrics held constant;
- prove cold, sparse, stale, invalid, wrong-epoch, and shifted history never
  grants unvalidated headroom.

### Phase 4: real HTTP shadow

- build the immutable profile factory only in the explicit native build;
- make off mode construct nothing;
- use independent bounded body capture;
- call the count-only transaction before existing QoS;
- reconcile local reject, semantic first output, completion, cancel,
  disconnect, timeout, upstream failure, close, and constructor rollback;
- preserve client and upstream behavior exactly.

### Phase 5: deterministic efficacy simulation

Compare identical traces under:

1. current count/dynamic threshold control;
2. current v0.9.0 KV-only shadow;
3. exact-token cache-cold KV prediction;
4. exact-token cache-cold KV plus TPS/TTFT/TPOT prediction.

There is no cache-aware comparator in the target implementation. Repeated
prefixes are intentionally charged as cold in every tokenizer-first policy.

### Phase 6: release decision

- run focused/full Go and Rust tests, both race modes, formatting, static
  checks, latency gates, and deterministic simulations on a clean builder
  checkout;
- audit evidence and the Git diff;
- keep runtime at PIG-v0.9.0 until component and efficacy gates pass, then
  promote the source candidate to PIG-v0.9.1 and rerun the exact final matrix;
  revert the promotion if any final gate fails;
- do not build an image, publish, or deploy without a later explicit user
  instruction.

## 12. Deterministic scenarios

The simulator includes at least:

1. same-poll short burst near KV capacity;
2. mixed short, 64k, and 128k prompts;
3. long prompt plus short decode;
4. short prompt plus long decode;
5. many decode sequences with low current KV;
6. completion before the next metrics poll;
7. stale waiting sample after PIG-known completions;
8. cancellation during prefill and decode;
9. local QoS reject after predictive reservation;
10. timeout and upstream failure;
11. stale or reset backend epoch;
12. tokenizer/template mismatch;
13. unsupported tools or multimodal input;
14. near-capacity concurrent check-and-reserve;
15. repeated identical prefixes, still charged cache-cold;
16. high KV headroom but post-join TPS below target;
17. low KV usage with excessive projected TTFT;
18. calibration error and distribution shift.

## 13. Acceptance criteria

Correctness and safety:

- zero false fits against deterministic hard/SLO ground truth;
- zero extra KV-hard, TPS, TTFT, TPOT, preemption-proxy, underflow, duplicate,
  or reservation-leak violations versus both baselines;
- prefix history changes neither request cost nor admission;
- every fit includes the full cache-cold input and output upper bound;
- high KV headroom cannot bypass TPS/TTFT/TPOT protection;
- stale/unknown state grants no predictive headroom;
- off mode performs zero tokenizer/predictor/capture work;
- shadow changes no client-visible or upstream-visible behavior.

Efficacy on identical deterministic traces:

- at least 5% aggregate completion-goodput improvement over current threshold
  control and v0.9.0 KV-only shadow;
- strict goodput improvement in at least three named scenarios;
- no more than 1% goodput regression in the cache-cold long-prompt suite;
- fewer safe completion-before-poll false denies;
- lower or equal false-deny rate for exact-token short requests;
- no SLO improvement claim based only on admission count or prompt TPS.

Prediction authenticity:

- exact token count changes prospective KV/prefill cost before forwarding;
- the post-admit state, not only the current sample, is evaluated;
- eligible learned residuals change a pre-forward forecast and at least one
  decision/reservation while current metrics remain fixed;
- learning from an aggregate, rejected, mismatched, duplicate, or
  insufficiently attributed outcome is impossible.

Evidence:

- every material red fails for the intended behavioral reason;
- every green records exact commit, commands, builder/container/toolchain,
  exit status, fixture/profile hash, random seed, and SHA-256 of log/status;
- a narrow green is described only as its named scope;
- CPU/simulator results are never reported as real-GPU capacity coverage;
- no vLLM modification, image, deployment, or production test is implied.

If the 5% goodput gate is not reached without cache prediction, the result is a
valid no-go for this simple design. The gate is not weakened and cache-aware
complexity is not silently reintroduced.

The builder-green deterministic candidate at commit `5ba63af` produced:

The fixed model uses 1,000,000 maximum KV tokens, 900,000 protected tokens,
64-token blocks, a 120 completion-token/s base curve, 25 token/s per-user
target, 3 s TTFT SLO, and 35 ms TPOT SLO except where a named scenario
explicitly changes one value. The current-threshold comparator uses its sampled
80% KV intake guard plus a two-request local count cap. The v0.9.0 comparator
uses the repository's real `kvshadow.Manager` and byte-estimate interval; both
exact-token policies use the real `CountCoordinator`. Every policy receives the
same arrivals, terminal causes, poll schedule, and cache-cold ground truth.
This deliberately isolates admission behavior; it is not a claim that these
service-curve constants describe any of the six production CVMs.

| Policy | Admitted | Completed | SLO-compliant | Completion-token goodput | Safety violations | False accepts | Minimum KV headroom |
|---|---:|---:|---:|---:|---:|---:|---:|
| Current threshold | 34 | 28 | 26 | 5,856 | 2 | 2 | 80,000 |
| v0.9.0 KV-only | 65 | 60 | 20 | 3,040 | 72 | 20 | -40,320 |
| Exact-token KV plus QoS | 48 | 44 | 44 | 12,256 | 0 | 0 | 19,840 |

This is a deterministic CPU/service-curve result, not a real-GPU benchmark.
The predictive policy improved completion-token goodput by about 109.3% versus
the current threshold model and 303.2% versus v0.9.0 KV-only in this fixed
suite, with strict improvement in seven named scenarios and no long-prompt
suite regression. The intentionally stressful bursts explain the large margin;
only the pass/fail conclusion against the predeclared 5% gate is used for this
release decision.

The structured report is
`/work/pig-v091-evidence/5ba63af-completion-goodput-report.json` with SHA-256
`088c4f7ab7d6b10266c7f2d6e6300d8ac055b54dd582f820e25bd7e19927e3ce`.

## 14. Deliverables

- this reviewed plan;
- count-only tokenizer ABI and Go wrapper;
- immutable tokenizer/profile manifest;
- cache-free reservation manager and simple QoS predictor;
- real HTTP shadow adapter with bounded capture and lifecycle reconciliation;
- deterministic simulator and baseline comparison report;
- remote-builder red/green, race, performance, and evidence manifest;
- source commits and pushed branch only until later authorization.

## 15. Three-pass review record

### Pass 1: model and objective

Finding: simply removing cache lookup from the old coordinator would still
leave cache-hit intervals, block digests, and cache-dependent request cost in
the transaction, so the implementation could remain cache-aware in disguise.

Revision: Section 3 now requires a structurally count-only production path,
defines every request as cache-cold, and adds a behavioral invariant that
prefix history cannot change cost, decision, or reservation. Sections 6-9 fix
the prospective KV and QoS equations and forbid learning from reducing exact
KV/prefill cost.

Finding: KV-only protection would repeat the original feedback problem for
single-user TPS and latency.

Revision: the objective and decision order jointly protect KV, existing/new
user TPS, TTFT, and TPOT before forwarding. A small residual calibrator remains
only for QoS forecasts and must demonstrably affect a pre-forward result.

### Pass 2: safety and lifecycle

Finding: using only the latest vLLM KV ratio would retain the metrics polling
blind window and could admit several requests against the same free capacity.

Revision: Sections 5-7 require immutable `max_kv_tokens`, sample identity and
watermarks, unabsorbed PIG reservations, conservative ambiguity handling, and
one atomic check/predict/reserve transaction.

Finding: tokenizer exactness alone does not bound body-copy memory, native call
latency, late commit, or terminal leaks.

Revision: Sections 9-11 add independent capture budgets, deadline/close gates,
constructor rollback, typed terminal causes, idempotent release, and off-mode
zero-work evidence.

### Pass 3: evidence and execution

Finding: the old plan's cache scenarios and five-policy comparison would allow
future work to drift back toward cache-aware implementation and would make a
simple design look incomplete for the wrong reason.

Revision: Sections 11-13 remove cache-aware target policies, explicitly charge
repeated prefixes as cold, retain current threshold and KV-only baselines, and
define completion-goodput/SLO acceptance without cache credit.

Finding: a simpler design could be declared successful merely for being safer,
even if it lowers total throughput.

Revision: the plan retains a quantitative 5% aggregate goodput gate, strict
improvement in three scenarios, and a no-go outcome when the simple design
cannot meet it. No deployment or cache reintroduction follows automatically.

Review result: the plan is internally consistent with the simplified request.
It removes cache prediction rather than hiding it, remains prospective rather
than feedback-only, protects single-user TPS/latency as well as KV, and keeps
simulation/builder/no-production boundaries explicit.

## 16. Post-implementation three-pass review

### Execution pass 1: model causality and objective

Finding: the learner changed decisions in direct tests, but the real HTTP
reservation interface originally exposed no outcome method. That made learning
production-unreachable even though the pre-forward predictor itself was live.

Revision: successful streaming requests now carry owned semantic TTFT through
the real reservation and submit it only after successful completion. A fixed
state/fixed request test proves three eligible outcomes change the next
pre-forward decision from static fit to calibrated TTFT risk. Failed requests
with the same semantic observation do not train. TPS/TPOT remain static rather
than accepting fabricated per-request outcomes.

Finding: a safety-only result could still lower useful throughput.

Revision: the independent `internal/simulation/goodput` suite runs four
policies on identical cache-cold traces, counts only SLO-compliant completion
tokens, charges tokenizer p95 to TTFT, and enforces the original 5%/three-
scenario/long-prompt gates. It passed with 12,256 predictive goodput versus
5,856 and 3,040 for the two required baselines.

### Execution pass 2: safety, identity, and lifecycle

Finding: a falling vLLM preemption counter returned without reconciliation but
left the preceding sample authorized as fresh. A restarted/reset backend could
therefore receive predictive headroom from pre-reset state.

Revision: counter reset now clears freshness and old preemption time. Tests
cover capacity mismatch, missing token capacity, stale/future clocks,
preemption cooldown, reset/recovery, in-flight Close cancellation, concurrent
sample watermarks, and exactly-one-upstream URL selection.

Finding: observer poll/age/timeout/cooldown came from mutable legacy config,
while model, tokenizer, capacity, and QoS came from the immutable manifest.

Revision: schema version 2 pins all four observer timings. Unknown and duplicate
JSON keys, profile/asset hashes, image digest, renderer, special-token policy,
KV alignment/order, horizon, timing, and scheduler failures are fail-closed.
Native construction tests prove legacy timing values cannot override the
manifest.

### Execution pass 3: evidence and claims

Finding: the first aggregate reporter used a negative number as both a valid KV
headroom result and an uninitialized sentinel. A later scenario could overwrite
the real minimum even though the goodput decision itself was unchanged.

Revision: aggregation now tracks initialization separately. The corrected
v0.9.0 KV-only minimum is -40,320 tokens, while predictive QoS remains +19,840.
Tests also require zero predictive false accepts, full cold cost for four
identical prefixes, and the measured 49/3k/24k/65k/131k tokenizer p95 values.

Finding: several otherwise-correct builder runs contained a formatting failure
or test-harness syntax error.

Revision: those runs are explicitly invalid evidence. Valid behavioral reds
and clean greens use later exact commits. Current material green evidence is:

- attributed semantic TTFT: commit `0d98721`, log SHA-256
  `06b916c6ebbc290e398a059679b26eb63046dd0e2e1c00a739b5cc1d492a1f82`;
- deterministic goodput: commit `5ba63af`, log SHA-256
  `d1166beabfd882ff481ae7c42257ea3998b00c734c2b2d55843c92afe6b234f0`;
- observer negative/race suite: commit `30c2d51`, log SHA-256
  `15d0e8d699c08be4b1da0314b5b54c42e2032538c197bc5cb4ebe58e83894aab`;
- schema-2 profile/default/native/race suite: commit `1aee2ff`, log SHA-256
  `0cc8d144c4b19c229b0508e152959852ad4c9938ae8941203414b5cd84af530c`.

Review result: the implementation is prospective and cache-cold, protects QoS
while increasing deterministic completion goodput, and fails closed on state or
identity uncertainty. The source-only PIG-v0.9.1 promotion is included in the
release candidate; remaining work is the exact final-commit full matrix. No
image, registry publication, deployment, restart, or production inference test
is authorized or implied.

## 17. Predictive hot-path efficiency review

This review covers only production-reachable predictive request work. It does
not optimize the disconnected cache experiments or change admission semantics.
The measurements below are remote-builder CPU benchmarks, not GPU-serving
latency or production capacity claims.

### Findings implemented

1. The native count-only tokenizer requested offsets that PIG never consumes.
   The hot count path now uses `Tokenizer::encode_fast`, while the builder-only
   token-ID oracle keeps the full encode path. All five pinned Gemma4 production
   cases retained exact final token IDs and counts. In the production-tokenizer
   exploration, 128k count latency improved by about 11.4% at p50 and 21.6% at
   p95; no measured length regressed at p50.
2. The Gemma4 renderer copied each message content through an intermediate raw
   message representation, copied content again for buffer ownership, grew the
   output incrementally, and allocated a split/builder path even when assistant
   text contained no thinking markers. Direct message-field decoding, capacity
   pre-sizing, removal of the content copy, and the no-marker fast path reduced
   the paired median by about 13.5%, bytes from 2,336,655 to 1,877,650 per
   operation, and allocations from 137 to 131.
3. Duplicate-key validation used `json.Decoder.Token`, which buffered or copied
   large string values even though only object keys matter. A structural scanner
   now runs only after the standard library has validated the JSON and decodes
   only object keys. Semantic-equivalence tests cover escaped solidus,
   backslash, Unicode, surrogate pairs, nested arrays/objects, and structural
   characters inside strings. Against the preceding renderer commit, the
   alternating ten-round builder A/B reduced median time by 23.41%, bytes from
   1,877,645.5 to 1,133,463 per operation, and allocations from 131 to 59; the
   new path was faster in nine of ten paired rounds.
4. String message content was decoded once for validation and again for
   rendering. The parsed text is now retained for rendering and only the raw
   content length is retained for capacity estimation. Against the scanner
   commit, the alternating ten-round A/B reduced median time by another 24.56%,
   bytes from 1,133,463 to 912,165 per operation, and allocations from 59 to 56;
   the new path was faster in all ten rounds.
5. Calibrated prediction copied all samples, filtered them into another slice,
   split each residual target again, and copied each target once more before
   sorting. It now counts fresh target samples under the scheduler lock, copies
   only scalar ratios into exact-capacity local slices, releases the lock, and
   sorts those slices in place. The calibrated prediction benchmark decreased
   from 2,680 B/op and 8 allocations to 128 B/op and 1 allocation; a prior
   alternating ten-round A/B reduced median time by about 67%.

### Findings intentionally retained

- Predictive shadow keeps an independent request-body copy. A regression test
  deliberately lets the shadow mutate its owned bytes and proves that both the
  upstream request and client-visible response remain unchanged. Removing this
  copy would weaken the shadow isolation contract for a relatively small
  `memcpy` saving compared with JSON rendering and exact tokenization.
- `Manager.virtualStateIntervalLocked` scans the active reservation map during
  an atomic decision. With the configured request caps this is a small bounded
  set, while incremental aggregates would complicate assimilation, ambiguous
  samples, prefill completion, terminal reconciliation, and rollback. No
  profile showed this scan as material, so it remains the simpler safety-first
  implementation.
- The native FFI error buffer was not pooled. The measured short count path is
  approximately 520 B/op and 2 allocations, not a per-request 4 KiB heap
  allocation. Adding shared pooling without evidence would introduce
  synchronization and ownership complexity.
- Prediction remains synchronous before forwarding. Moving tokenizer work to
  an asynchronous shadow task would lower apparent request latency but would no
  longer measure or exercise the required pre-forward forecast and reservation
  transaction.

### Stop condition and remaining cost

The final renderer profile at source commit `78de475` measured about 4.05 ms,
912,190 B/op, and 56 allocations on the long-chat CPU fixture. The old
`json.Decoder.Token`/`Decoder.refill` allocation path is absent. Standard
`encoding/json` validation and decoding account for about 92% of CPU;
`uniqueJSONKeyScanner.skipString` accounts for about 5.9%. Allocation space is
now dominated by `json.RawMessage` copies (about 50.6%), the one decoded string
(about 23.5%), and the rendered output buffer (about 25.6%).

A more invasive typed or custom full-request parser could remove another raw
copy, but it would change accepted-field, null, and error-priority behavior for
an end-to-end stage already much smaller than exact tokenization on long
inputs. It is deferred until an integrated profile shows renderer parsing, not
the tokenizer, is the admission latency bottleneck.

Material builder evidence is retained under `/work/pig-v091-evidence`:

- `d93defc-json-key-scanner-focused.log`, SHA-256
  `d5c578d24ef2ddcfbbe861f230aa6fa47e50eba61fe926eb56475b7b51961cb6`;
- `d93defc-json-key-scanner-paired-ab.log`, SHA-256
  `93522225188ad5e50d180e519c94968340ac4bfcaca9e6eed7ba2c30119f8d05`;
- `78de475-renderer-decode-once-focused.log`, SHA-256
  `86b2f3bae131bf942f6f7ad16339e2e721773369f19c27433ddca405d7aaf608`;
- `78de475-renderer-decode-once-paired-ab.log`, SHA-256
  `c112e847f97c30c15214752ae9e527f355303c885b1fb37310bd47c53f58b395`;
- `78de475-gemma4-renderer-profile.log`, SHA-256
  `dcc3e648f22a1f0328b52c456cee262eb618c1fd9c16a19c6cd5aba6437f5945`.

At this document revision the optimized source is focused-green. The next and
final source-only gate is the exact document-commit full matrix: default and
native Go tests and races, locked Rust tests, production renderer/count oracle,
exact final token IDs, deterministic goodput, and the full tokenizer benchmark.

## 18. SOLID, admission-model, and concurrency review

This review is scoped to the PIG-v0.9.1 source-only shadow/evaluation
candidate. It does not build an image, publish a registry artifact, deploy,
restart a CVM, query a production inference endpoint, modify vLLM, or add
routing behavior. All executable red/green work in this section ran on the
remote builder; Windows was used only for editing, read-only inspection, and
Git operations.

The production dependency path after this review is deliberately small:

~~~text
HTTP request
  -> Gemma4 renderer
  -> count-only native tokenizer
  -> cache-cold count cost builder
  -> CountCoordinator
  -> Manager atomic state/predict/evaluate/reserve transaction
  -> narrow lifecycle reservation

vLLM metrics
  -> narrow sample coordinator
  -> Manager sample-window reconciliation
~~~

`predictive_factory.go` is now only the composition root. Manifest schema,
bounded file IO, duplicate/unknown JSON rejection, hashes, asset verification,
and semantic validation live in `predictive_profile.go`. Count-cost validation,
model-length overflow protection, block rounding, and conversion to the domain
cost live in `count_cost.go`. The HTTP lifecycle adapter and vLLM observer own
different narrow coordinator interfaces; neither depends on a concrete
coordinator or on an artificial interface containing every method.

The review intentionally does not introduce repository, service, strategy, or
factory interface layers where Go functions and concrete immutable values are
sufficient. SOLID here means one reason to change, consumer-owned interfaces,
dependency inversion at process boundaries, and substitutable behavior under
tests; it does not mean Java-style abstraction depth.

### Review pass 1: model causality and SOLID boundaries

Finding: the active count-only HTTP path was integrated and prospective, but
3,243 lines across eleven disconnected cache/analyzer history files made the
source model appear cache-aware and kept two incompatible architectures alive.
The domain still exposed cache-hit intervals, projected discounts, tokenizer
manifests, block digests, and cached-prefill fields even though the configured
request path did not consume them.

Revision: the disconnected Go cache mirror/coordinator/tokenizer/analyzer
slice and the Rust block-analysis ABI/tests were deleted. `RequestCost` is now
structurally cache-cold, and `Manager.validRequestCost` requires
`UncachedPrefillUpper == InputTokens`. A regression test proves a partial
prefill discount fails closed without creating a reservation. Repeated
prefixes continue to pay full input, prefill, and rounded KV cost. The v0.9.0
`kvshadow.Manager` remains only as the real KV-only comparator in the
deterministic goodput suite; it is not a production dependency of the new
path.

Finding: the native library still exported block digest and analysis handles,
and retained direct `blake3`/`sha2` dependencies, although production needed
only exact count. The Go counter API also carried a `RequestFeatures` value
that the renderer never populated and both native implementations ignored.

Revision: native ABI 3 contains only open/count/destroy/version. The CLI keeps
`encode` for the independent exact-ID oracle and keeps count/vector
benchmarks, but no production block-analysis entry point. `RequestFeatures`,
four unused Manager inspection/rollback methods, and the unused
`HasPrediction` result field were removed. `blake3` and `sha2` are no longer
direct Cargo dependencies. This is interface segregation and dependency
reduction, not an inference-throughput claim.

Finding: risk constraints were inconsistent with their declared equation.
`workspace_risk_budget == 0` or `preemption_risk_budget == 0` skipped the
comparison, so a zero-tolerance profile could admit a non-zero predicted risk.

Revision: workspace and preemption risk now always enforce
`predicted_upper <= configured_budget`; zero predicted risk still fits a zero
budget. The optional zero-disables convention remains only for KV/TPS/TTFT/TPOT
constraints where it is explicitly part of the existing contract. The
behavioral red failed on both zero-budget cases and the merged green passes.

The authentic prediction chain remains unchanged: exact rendered input tokens
produce full-cold KV/prefill/decode cost; observed state plus unabsorbed
reservations produces the post-admit counterfactual; the learned/static
scheduler predicts existing-user TPS, all-user TPS, TTFT, TPOT, workspace, and
preemption bounds; and `Evaluate` consumes those values before forwarding.
Real successful completion with attributed semantic TTFT trains the residual
calibrator for a future decision. TPS and TPOT remain explicit static profile
predictions because PIG has no sufficiently attributed per-request production
target for them; fabricated targets were not added merely to make learning
look broader.

Pass-1 result: the source and domain now describe one cache-free admission
model, PIG still predicts acceptance for exactly one configured upstream and
does not route, and every retained abstraction has a production, safety-test,
or deterministic-simulation consumer.

### Review pass 2: safety, locking, lifecycle, and failure modes

Finding: `realPredictiveShadow.mu` covered the complete
`CountCoordinator.DecideAndReserve` call. A slow scheduler therefore blocked
`Close`, snapshot bookkeeping, and reservation lifecycle operations even
though the Manager already owned the required atomic transaction lock.

Revision: the adapter lock now covers only closed-state ordering, attempt
statistics, and its lifecycle map. The Manager lock still covers the full
virtual-state read, scheduler prediction, counterfactual evaluation, event
sequence increment, and reservation insert. If prediction returns after
adapter closure, a fit is never returned to the caller: its reservation is
terminated with `TerminalExpired` and the attempt is recorded unknown. The
behavioral red proved that old `Close` waited more than one second for a
blocked prediction; default/native race greens prove the reduced lock and late
rollback path.

Finding: when the vLLM preemption counter increased, the observer first waited
for Manager reconciliation and only then recorded the cooldown. During that
window the preceding sample remained healthy and could authorize another
request.

Revision: detection now immediately clears sample freshness and records the
new preemption time under the observer lock, then performs reconciliation.
Successful reconciliation restores only sample freshness; it does not cancel
the cooldown. Failed reconciliation remains fail-closed. A blocking fake
coordinator produced a deterministic red while reconciliation was in progress,
and default/native race greens cover the corrected ordering without nesting
the observer and Manager locks.

Safety invariants rechecked in this pass:

- count analysis must match manifest and backend epoch;
- input plus decode horizon must not overflow or exceed model maximum length;
- every admission is charged full cold input and block-rounded KV;
- observed vLLM KV token capacity must equal immutable profile capacity;
- sample windows retain conservative lower/upper ambiguity and unabsorbed
  reservations;
- duplicate admission IDs, invalid predictions, stale/future metrics, counter
  reset, recent preemption, cancellation, close, and terminal replay fail
  closed or remain idempotent as specified;
- only successful completion with owned semantic TTFT can train;
- native counter close/count races retain handle ownership via the existing
  RW lock;
- adapter Close no longer waits for prediction, while Manager admission remains
  one atomic check/predict/reserve transaction.

Pass-2 result: reducing the outer lock improved shutdown/lifecycle
responsiveness without weakening oversubscription protection, and preemption
feedback now closes intake at detection rather than after reconciliation.

### Review pass 3: evidence, reproducibility, and claim limits

The disconnected-architecture baseline at `a6aa07d` failed the no-cache gate
as intended and measured 3,243 legacy lines. Evidence:

- `/work/pig-v091-evidence/a6aa07d-pig-v0.9.1-solid-architecture-red.log`,
  SHA-256 `9a8fbc420f2cc699bd8fe3937a7cec86f0b56707447886397f398ffae1df40a2`;
- matching status SHA-256
  `31487098585a70a8e9609b8d65e5c8fa87a24c42d441c46e79c3a72ced1768c7`.

The adapter lock red is
`/work/pig-v091-evidence/solid-focused-20260731144628.log`, SHA-256
`b27dcd1080e85f63f272294bc8a553b344e106a92a9ae2c57c9a54889de929ba`.
It failed for the intended one-second Close wait, not for formatting or a
broken harness.

The immediate-preemption red is
`/work/pig-v091-evidence/solid-focused-20260731150058.log`, SHA-256
`bccdd19487f7670296e3d63a906d3d16ce2413b779b395ecea13e694a5f47dc9`.
Default, default-race, native, and native-race all failed only because the old
healthy sample remained authorized while reconciliation was blocked.

The zero-risk-budget red is
`/work/pig-v091-evidence/solid-focused-20260731151405.log`, SHA-256
`70cdbdd35a500ab478767e94081df43d641cc9bbe4d06967dd18c1e3fa0b0461`.
Both workspace and preemption cases returned the old incorrect `fit`; all
unrelated focused and race/native gates stayed green.

The merged uncommitted candidate is focused-green in
`/work/pig-v091-evidence/solid-focused-20260731151708.log`, SHA-256
`8a11e6bff3194c54ebe9d2e43533125f62e75bff9e145aa15d9046de3f315eec`;
its status SHA-256 is
`be795f95234e8fe3b09709820f1732e3ba2aad7d89587af365c97a3b5a316295`.
It passed the no-cache source gate, Rust formatting and locked all-target tests,
release native build, focused Go tests, default/native races, and the native
counter tests. Its formatter/lock patch is empty with SHA-256
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

Against the architecture-red baseline, the staged source diff is 836
insertions and 4,905 deletions across 44 files, a net reduction of 4,069 lines.
The release native library changed from 5,942,400 to 5,839,576 bytes
(-102,824, about -1.73%), and the CLI from 6,037,776 to 5,939,416 bytes
(-98,360, about -1.63%). `cargo tree --locked -i blake3` no longer resolves a
package. These measurements support maintainability, dependency, and binary
surface reductions only. They do not prove higher GPU utilization or serving
throughput.

Source commit `60ab3c1bb4c9e01c4a8eff59097e8bc24a659bcd` then passed one
clean-checkout, all-green remote-builder matrix. It reproduced 5,856
completion-token goodput for current thresholds, 3,040 for v0.9.0 KV-only, and
12,256 for the predictive policy. Predictive TPS, TTFT, TPOT, KV-hard,
preemption-proxy, false-accept, and reservation-leak counts were all zero. The
repeated-prefix scenario retained full cold charging and 1,024 predictive
goodput.

The same matrix passed:

- `git show --check`, clean checkout, PIG-v0.9.1 source version, tracked gofmt,
  Python compilation, Rust format, locked release build, and locked all-target
  tests;
- immutable tokenizer/config/template/oracle hashes;
- the no-cache architecture gate, 44-source-file `836/-4,905` report, direct
  dependency report, smaller binary assertions, and exactly four exported
  count-only ABI-3 functions;
- all default Go tests and races and all `pig_native` Go tests and races;
- all five production Gemma4 renderer/count oracle cases, their race run, and
  exact final token IDs with `mismatches=0`;
- deterministic goodput tests, structured goodput validation, and the new
  five-case `count_only + vec_ids + count_to_vec_ratio` benchmark schema with
  no `block_analysis` field.

Material exact-source evidence is:

- full matrix log
  `/work/pig-v091-evidence/60ab3c1-pig-v0.9.1-solid-final-gates.log`,
  SHA-256 `6c1203c5a249db20a3b45237517e92aa1d0bc0956fb1d649c5195157013b388c`;
- status, SHA-256
  `e12b8286a75b90ff487078065b0795c881661476d38b5369b659c8bf06bb8259`;
- structured goodput report, SHA-256
  `088c4f7ab7d6b10266c7f2d6e6300d8ac055b54dd582f820e25bd7e19927e3ce`;
- exact-final-ID report, SHA-256
  `b3adeee6101b5b3a3a87b8c4dacbb3399fa058125202b79cfe86dc53424f4488`;
- count/vector benchmark report, SHA-256
  `df7da99447f6b76e58e6db7ed9db621239b75ff12548316c309b8719333c43db`.

The final all-green benchmark measured count-only p95 of 43.336 us at 49
tokens, 8.982 ms at 3,074, 122.427 ms at 24,578, 485.082 ms at 65,538, and
1.770 s at 131,074. The preceding same-commit run measured approximately 1.410
s at 131,074, while the older recorded baseline was approximately 1.489 s.
That spread is too large to claim a stable 128k latency improvement or
regression from this SOLID refactor. Exact counts, token IDs, schema, and
goodput are release gates; these standalone sequential CPU timings are retained
as raw performance evidence and are not GPU utilization or production serving
measurements.

Pass-3 result: all behavior-specific reds have matching greens, the exact
source commit passed the complete builder matrix, and throughput/goodput claims
remain separated from source deletion, binary size, and noisy standalone
latency. A documentation-only evidence commit may follow; it must prove that
all executable paths are byte-identical to `60ab3c1` before inheriting this
matrix. No image, publication, deployment, production access, or cache
prediction is authorized or implied.

## 19. PIG-v0.9.2 corrective learning goal

This section supersedes the v0.9.1 completion and safety conclusions above.
The old evidence remains an immutable record of what was run, but its
simulation ground truth is not sufficiently faithful to progressive vLLM KV
allocation and cannot qualify v0.9.2.

### 19.1 Approved objective

Prediction happens before a request enters the configured upstream. Feedback
does not attempt to rescue an already-forwarded request; it calibrates future
predictions and may immediately make subsequent admission conservative. The
learner may start imprecise. As fresh, sufficiently covered evidence reduces
uncertainty, PIG may progressively use more capacity while protecting
single-user TPS, TTFT, TPOT, KV safety, and preemption frequency. The optimized
quantity is SLO-compliant completion-token goodput, not raw admits or aggregate
token throughput.

PIG remains tokenizer-first, cache-cold, single-upstream, and routing-free.
No cache residency, prefix hashing, cache discount, vLLM modification, image
publication, deployment, production restart, or production inference is in
scope. Executable validation remains remote-builder-only.

### 19.2 Hard invariants that learning cannot repair

1. Every supported request has backend-equivalent rendering and exact input
   token count under a runtime-bound model/tokenizer/template/backend identity.
2. Unsupported sequence multiplicity (`n`, `best_of`, beam, or parallel
   choices) fails closed until its complete KV/prefill/decode cost is modeled.
3. A reservation retains its unmaterialized decode-horizon KV until terminal
   release. Current vLLM KV allocation is not proof that future output KV is
   already materialized.
4. Work queued inside PIG is not classified as upstream-observed. Forwarding,
   prefill completion, terminal release, metrics reconciliation, and backend
   epoch transitions have explicit lifecycle semantics.
5. Check, post-admit prediction, constraint evaluation, and reserve are one
   atomic transaction. Every terminal path releases exactly once.
6. Unsupported, stale, identity-mismatched, out-of-distribution, or invalid
   inputs receive no learned headroom.

### 19.3 Learning contract

The static prior is a conservative starting envelope, not an accuracy claim.
The learner predicts residuals and uncertainty for future requests. Its
features, targets, sample qualification, bounds, version, staleness, global
memory limit, eviction, and distribution-shift behavior must be explicit.

Trusted targets are semantic TTFT, backend-provided per-request completion
usage and duration when present, and window-level generation-token deltas,
running/waiting state, KV growth, completions, and preemption deltas. Requested
output length, SSE chunk count, output bytes, and re-tokenized output are not
fabricated completion-token outcomes. Until censored failures and distribution
shift are modeled safely, learned TPS may not exceed its conservative prior and
learned latency may not fall below its conservative prior.

Cold or sparse cells use the static prior. Qualified cells require sufficient
fresh samples, feature coverage, bounded recent error, and a confidence level
that passes the admission gate. Preemption, counter reset, backend-epoch drift,
identity drift, stale data, or excessive prediction error invalidates learned
headroom and falls back to the prior. Learning state is globally bounded; it
cannot grow one permanent cell per request shape.

### 19.4 Findings to reproduce before implementation

1. A reservation present across a scrape loses the unmaterialized portion of
   its decode-horizon KV because the current manager absorbs its full cost.
2. A reservation waiting in the local QoS gate can be marked absorbed before
   it reaches vLLM.
3. The renderer accepts `n > 1`/`best_of` while cost construction hard-codes
   one decode sequence and one context horizon.
4. Served model, model revision, tokenizer config, chat template, backend
   image/source revision, and backend epoch are declared but not all consumed
   or verified at runtime.
5. The goodput simulator exposes the complete output upper bound as observed
   KV at admission time instead of growing allocated KV during decode.
6. Successful termination and TTFT outcome submission are separate manager
   transactions, so reconciliation can delete the completed owner between
   them.
7. Successful-only TTFT calibration can lower latency bounds and carries old
   learned cells across preemption or backend reset.
8. Learned feature cells have a per-cell sample cap but no global cell cap or
   eviction policy.

### 19.5 Test-first execution order

1. Add focused unit/race tests for multiplicity, remaining-horizon retention,
   local-queue lifecycle, atomic successful outcome, epoch invalidation, and
   globally bounded learning cells. Preserve builder red evidence.
2. Change deterministic ground truth to progressive KV allocation and add
   arrivals after one or more scrapes while long decodes remain active. Preserve
   a structured red report against v0.9.1.
3. Implement the smallest coherent request, reservation, lifecycle, identity,
   and conservative-learning changes. Do not add cache or routing abstractions.
4. Run focused default/native tests and races, then the full default/native Go
   matrix, locked Rust tests/build, exact tokenizer oracles, progressive-KV
   goodput comparison, and tokenizer/decision benchmarks on the builder.
5. Complete model, safety, and evidence review passes. Each pass records actual
   findings and revisions before the next pass starts.

### 19.6 Acceptance gates

- zero deterministic false fits, hard-KV violations, TPS/TTFT/TPOT violations,
  preemption-proxy regressions, reservation leaks, and double releases;
- every fit retains full cache-cold input and remaining output upper cost;
- progressive-KV and local-queue scenarios cannot reuse future capacity;
- learned predictions alter pre-forward decisions only when qualification and
  confidence pass, and fall back on cold/stale/shifted/reset state;
- learner memory and lifecycle maps are explicitly bounded;
- completion-token goodput exceeds both the current-threshold and v0.9.0
  KV-only baselines in the corrected independent simulation;
- focused, full, race, native, oracle, format, dependency, and benchmark gates
  pass from one exact clean source commit on the remote builder;
- source version, plan, evidence hashes, branch, and pushed commit agree;
- no image, registry, deployment, CVM mutation, or production inference occurs.

### 19.7 Version and evidence policy

PIG-v0.9.2 identifies this corrected contract. Version changes occur at a
release-contract boundary, not on each implementation commit. Red commits may
use temporary source snapshots without claiming the version. A final candidate
must update the runtime version, rerun the exact-source matrix, and be pushed
only after staged-path and source/object audits. A source commit, builder test,
built image, published image, and deployment remain distinct states.

### 19.8 Corrective red evidence

The first v0.9.2 corrective red source snapshot used archive SHA-256
`9e212f67565f5a50baab030cf32698c74f62b6b0ba1af69be22f49c3e99cdbbf`.
It ran only on the remote builder as candidate
`solid-focused-20260801010619`. The stable material evidence is:

- log `/work/pig-v091-evidence/solid-focused-20260801010619.log`, SHA-256
  `a1bc44e4621c4ae8658891d751137ebb4166466f7fa38b0e8c748ed72b068b96`;
- status `/work/pig-v091-evidence/solid-focused-20260801010619.status`,
  SHA-256
  `00633bb3fb8bd1bb4940588fd159ad74035973c5bedd822f61cf470285726470`;
- empty format/lock patch, SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

Default and default-race failed only because the current Manager reduced a
500-token full reservation to the 100 currently observed tokens and absorbed
a 10,000-token reservation still waiting inside PIG. Default, default-race,
native, and native-race also failed only because Chat `n=2`, Completions
`n=2`, and Completions `best_of=2` were accepted. All unrelated focused tests,
the no-cache source gate, Rust format and locked tests, release native build,
and native counter tests passed. This proves the intended five behavioral
defects against the pre-fix implementation; it does not qualify any fix.

The runner originally invoked `sha256sum` on its own log while `tee` still had
the log open, then appended that output to the same log. Therefore the
in-log value `736db3...` is intentionally rejected as unstable; the stable
post-process value above is authoritative. The v0.9.2 runner must compute
material hashes only after the test process and log writer have exited.

### 19.9 Corrective learner and progressive-KV red evidence

Candidate `v092-go-focused-20260801013807` used an immutable uncommitted source
archive with SHA-256
`0d8c4482975b2eabf2ffe77f916e5e136c311ae04ce43e92c827919481b5e1d8`
and base source HEAD `8ce939e1b35cd684a8e9568365fd9504ef7613be`. It ran on
the remote builder only. Stable material evidence is:

- log `/work/pig-v092-evidence/v092-go-focused-20260801013807.log`, SHA-256
  `0f345113af6394389e0a5e54680f068ea055100598a5ce5f0b2a3b80f2a5c660`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801013807.status`,
  SHA-256
  `c001b9f271319a9ad5503a31788fe7ab72bbd271bd368650a628a4f00ca02d60`;
- empty format patch, SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

Default and race gates failed for only the intended missing behaviors. The
calibrator admitted an unbounded configuration, retained three feature cells
with a configured global maximum of two, retained optimistic TPS and latency
headroom after a fresh censored terminal, rejected a targetless censored safety
sample, and retained all learned cells across explicit invalidation. The real
HTTP lifecycle submitted no censored learner outcome for a forwarded request
that completed without semantic TTFT, failed upstream, timed out, was cancelled,
or disconnected. The vLLM observer did not invalidate learning on a preemption
increment or counter reset. The corrected global-cell test used an existing
decode sequence, so it reached and failed the intended capacity assertion rather
than an unrelated invalid TPS target.

The no-cache architecture gate, domain predictive package, predictive
simulation package, corrected progressive-KV goodput acceptance, and its race
run all passed. In particular, the simulation now distinguishes progressively
materialized ground-truth KV from PIG's full cache-cold reservation upper bound;
the repeated-prefix scenario passed only after proving four separate full
reservations. This red evidence qualifies the missing learner and lifecycle
behaviors, not their implementation.

### 19.10 First corrective focused green evidence

Candidate `v092-go-focused-20260801014643` used immutable archive SHA-256
`6774d82e62ce263843c054ce995b5a1ebdfa2965f40de9ceb0378c11af098134`
from base source HEAD `8ce939e1b35cd684a8e9568365fd9504ef7613be`. It ran
only on the remote builder. Stable evidence is:

- log `/work/pig-v092-evidence/v092-go-focused-20260801014643.log`, SHA-256
  `cf81e334621de9be085268e9fc777a7d62f3758f510240bf8c2ddf197159905b`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801014643.status`,
  SHA-256
  `27e42e632aef5b162a0ddac8c429e1ab673daf6ad97130fb75ce1f9463355e6c`;
- empty format patch, SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

The no-cache architecture gate, focused domain/runtime/simulation/server Go
packages, progressive-KV goodput acceptance, and focused race packages all
passed. This proves the named bounded-cell, deterministic eviction, censored
fallback, invalidation, forwarded-terminal, atomic terminal/outcome, strict
request-cost, lifecycle, and corrected simulation tests on that archive. It is
not yet full-matrix, native, tokenizer-oracle, benchmark, release-version, or
clean-commit evidence.

### 19.11 HTTP prediction-authenticity red evidence

The model review found a release-blocking causality defect after Section
19.10: `PREDICTIVE_ADMISSION_MODE=shadow` computed and reserved before the
legacy QoS path, but a nil decision did not change HTTP behavior. Consequently
the only implemented runtime mode could observe predictions but could not use
them to protect the configured upstream. The corrective mode contract is now:

- `off`: preserve no predictive request body and execute no predictive work;
- `shadow`: predict before forwarding but preserve upstream and client
  behavior for both fits and non-fits;
- `enforce`: require a successful atomic predictive reservation; any risk,
  unknown, unsupported body, panic, or unavailable predictor fails closed with
  HTTP 429 before KV shadowing, QoS acquisition, backend selection, priority
  injection, `MarkForwarded`, or proxy execution.

Candidate `v092-go-focused-20260801015438` is rejected as behavioral evidence.
Its immutable archive SHA-256 was
`f1e4ca1c0daa390512af231199d3bc2140240b0de38b6469ebc399d262c8ff31`, but
an empty predictive mode was accidentally treated as enabled during server
construction. The desired enforce test failed, while unrelated server
fixtures also failed because they were incorrectly required to load a
predictive profile. The mixed log/status hashes are respectively
`0d55b65d11f213485ff5c40d9d700035f6dadad5693507d0c01122197d52f0a4` and
`4ce75ac663cbfad33dfca10e86383f4d52ee2cd37870ea81136944bb3191f548`;
they are retained only as an audit trail and qualify nothing.

After making empty and explicit `off` modes share the same zero-work server
construction behavior, candidate `v092-go-focused-20260801015610` reproduced
only the intended defect in both default and race gates: an enforcing
prediction returned nil, but the HTTP request still received status 200 and
the test backend ran. All other focused domain, runtime, simulation, goodput,
server, no-cache, and format gates passed. Immutable evidence is:

- archive SHA-256
  `4177ff1f314b1805b2e6047c96776d2d271c5fbc0dc058720703a2bb8d895a26`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801015610.log`, SHA-256
  `0ee3b80b9d4f7bfe574cffed8380b4487ffc4f6d1ca7043c54686685c0a2d7a2`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801015610.status`,
  SHA-256
  `7a11bf1e2919d5f82d87df536d5020c9f8abd75763ef871f710ec3853822f060`;
- empty format patch, SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

This red qualifies the missing HTTP enforcement behavior only. It does not
qualify the mode plumbing, failure coverage, metrics, lifecycle behavior, or a
release candidate.

### 19.12 HTTP prediction-authenticity focused green evidence

Candidate `v092-go-focused-20260801020050` implemented the first actual
enforcement path. In `enforce` mode, a missing reservation now increments a
dedicated enforced-reject counter and returns HTTP 429 before the legacy KV,
QoS, backend, priority, forwarding, or proxy path. The same tests prove that a
fit reaches the upstream and marks forwarding and terminal exactly once;
unscannable bodies and predictor panics fail closed; risk in `shadow` mode
preserves upstream/client behavior; and empty or explicit `off` modes construct
and run no predictive adapter.

The no-cache gate, all focused domain/runtime/simulation/goodput/server tests,
and focused runtime/goodput/server race tests passed. Immutable evidence is:

- archive SHA-256
  `d444c3f973941328a8d6c8291780847bc7dba0e477d431a2856f7216284028a5`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801020050.log`, SHA-256
  `c2c6111e9b14ff0505056582cda59d0476564ad41ad5b3cc169d4059477a8e7a`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801020050.status`,
  SHA-256
  `ec2d6cf17469bbe0c73964ff5e30cd29ca44a60f39fda3e8b87882618e348a80`;
- empty format patch, SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

This focused green proves the named pre-forward HTTP behaviors only. It is not
yet predictive-metrics, bounded-retirement, full-matrix, native,
tokenizer-oracle, benchmark, release-version, or clean-commit evidence.

### 19.13 Bounded-retirement and predictive-metrics focused evidence

The terminal hot-path review found that a full 4,096-entry retirement slice
shifted 4,095 structs for every subsequent absorbed completion. The manager now
uses a fixed-capacity FIFO ring. Terminal insertion and oldest-entry eviction
are O(1); reconciliation processes the bounded ring in completion-sequence
order without allocating or copying the full buffer on each terminal. Dropping
an oldest release credit cannot admit extra work: it can only leave observed KV
conservatively over-counted. The snapshot reports current retired entries and
evictions. Wrap, overflow, FIFO order, mixed scrape watermarks, clean scrape,
benchmark, and race coverage were added.

Candidate `v092-go-focused-20260801020354` passed the no-cache gate, all
focused packages, and focused races with this ring. Immutable evidence is:

- archive SHA-256
  `6b713bf92e0a7a13c6a437980c744171c2270700e099d95484d0e420edf44daf`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801020354.log`, SHA-256
  `7c5b931ca40da7bb038ebcbd0405ee36e518d1dad4882dc7797202a214cba68c`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801020354.status`,
  SHA-256
  `0947491628811c6741ff92667c77718ab4e175e6abab780637967b1425b9938b`.

Predictive runtime metrics now expose the bounded operational state needed to
operate `enforce`: mode, attempts, fit/risk/unknown totals, enforced rejects,
last reason/source/sample count, live and retired reservations, retirement
evictions, learner accepted/rejected/invalidated/cell counts, guarded failure
phase, and prediction/render/tokenizer duration histograms. The adapter owns
the phase timers and provides a read-only telemetry snapshot; metrics do not
enter the prediction transaction or add unbounded labels/state.

Candidate `v092-go-focused-20260801020740` extended the focused and race matrix
to `internal/observability/metrics`; all gates passed. Immutable evidence is:

- archive SHA-256
  `43860eece63b3d526c5ce1846145e3dda9f0b44dbc0f52b30e486630288ab5c8`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801020740.log`, SHA-256
  `f641df650b3e0d629aba122ae81bc5e704e16bdf41eddee4d30c15664e981074`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801020740.status`,
  SHA-256
  `ab1514b4d1245a02513e436cb657cf1f078d7b0a1ee38a3f2c14f50f76fcbaa0`;
- both candidates had the empty format-patch SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

### 19.14 Review pass 1: model and causality

Inspected prediction causality, tokenizer/template policy, request features,
targets, cache-cold accounting, SLO constraints, and baseline claims after the
HTTP enforce correction. One material throughput defect remained: a
predictive fit still passed through the legacy dynamic feedback concurrency
limit. A stale/red legacy snapshot could therefore reject a safe prospective
fit after prediction, recreating delayed recovery and low GPU utilization.

Candidate `v092-go-focused-20260801021158` is rejected evidence because the new
test omitted the dynamic metrics URL and failed during config validation. Its
archive/log/status SHA-256 values were respectively
`63ddc133feeee31b1c7344e39dcd8c40aaee771d4ba8730975e693d95928a06f`,
`207cd6383edfc70d571f29ef0f8c07e7ac852e305178d2b79583c9716eccd379`,
and `fe289ade397d85792602fd15bb87137cadbf0d5eecc39513f5d591529c340116`.
After adding an independent unavailable-metrics fixture, candidate
`v092-go-focused-20260801021257` failed only because a predictive fit received
HTTP 429 with zero inference-backend calls. Valid red evidence is:

- archive SHA-256
  `5a1a51fa5d4c154934b65f7e522efe78bf4e1270713edc6a590b65374d82604c`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801021257.log`, SHA-256
  `74b071c83c3dc3583baf075b77c0c83dea9b39f06827cfa43ad2f6b5b26a9ec3`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801021257.status`,
  SHA-256
  `e38d54234e5be314288eaebf41cd6e200f9231fa3805d55649dce8967e1d8715`.

In `enforce`, the legacy gate now uses only the operator-configured static
absolute concurrency limit and tier fairness; dynamic feedback remains
observable but cannot late-veto a predictive fit. `off` and `shadow` preserve
the existing dynamic behavior. A second HTTP test proves that the static
absolute cap still rejects and releases the predictive reservation correctly.
Candidate `v092-go-focused-20260801021353` passed all focused and race gates:

- archive SHA-256
  `78e6eee990ca1a105c72585676a33f4df0e8bea6cddd6107b655f63af46e8c45`;
- log `/work/pig-v092-evidence/v092-go-focused-20260801021353.log`, SHA-256
  `e379195a5ae08bf016524d0e491bbec550ddc8319784450e4ace5fc84f891868`;
- status `/work/pig-v092-evidence/v092-go-focused-20260801021353.status`,
  SHA-256
  `6cd10cc7a7e68bc2f238ee7fcb4a268b62aeb8c04fd1de31b7cff5667c9ef35d`;
- red and green format patches were empty with SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

The pass also rechecked the learning claim. Real HTTP feedback currently
qualifies semantic TTFT and targetless censored safety evidence. TPS/TPOT
residual targets are implemented and deterministically tested, but the HTTP
proxy does not fabricate them from chunk count, bytes, requested output, or
unattributed aggregate metrics. Without a reliable per-request backend usage
and timing signal, those dimensions deliberately remain at the conservative
profile prior. Thus v0.9.2 can mature TTFT cells and become more conservative
after censored outcomes; it does not claim live per-request TPS/TPOT learning.
Static prospective TPS/TPOT bounds still participate in every admission. This
limitation is explicit so later evidence cannot misrepresent synthetic learner
tests as a live target source.

Pass-1 result: prediction now causally controls pre-forward admission, cache is
always cold, supported request shape is single-sequence and identity-bound, and
the corrected progressive-KV goodput comparison remains the applicable
baseline. The safety pass must next close observable backend-epoch drift and
recheck all atomic lifecycle/failure paths.

### 19.15 Review pass 2: safety, lifecycle, and observable identity

This pass followed the real HTTP transaction and lock order rather than only
the public interfaces. It inspected classifier body ownership and concurrency,
atomic check-and-reserve, local queue and static-cap rollback, forward commit,
prefill assimilation, terminal/outcome atomicity, scrape watermarks, retained
future KV, retired-release overflow, learner bounds, counter resets, required
metric identity, stale fetches, panic containment, label cardinality, and
shutdown races. It also rechecked that the test runner performed no local Go,
Rust, native, race, simulation, benchmark, image, deployment, CVM, or
production-inference action.

The manager's prospective check and reservation remain one mutex transaction.
Successful learner observation and terminal release remain one manager mutex
transaction. A request that has not been forwarded, is waiting in PIG, or has
not completed prefill retains the complete cache-cold reservation. After a
complete scrape window absorbs the materialized input, the full rounded future
decode-horizon KV and future context remain additive until terminal. Retired
release overflow still drops only the oldest release credit and therefore can
only conservatively over-count observed state. Manager, scheduler, adapter,
observer, and telemetry lock order showed no reverse lock dependency in the
focused race paths. Raw body copies are cleared after prediction, rendered
content is cleared after counting, and predictive metrics use only fixed mode,
decision, result, and phase labels; no prompt, token ID, request ID, feature
cell, or user-controlled label is exported.

The pass found and corrected six concrete safety or operability defects.

1. A reservation could fit atomically but `MarkForwarded` could return false or
   panic and the HTTP path ignored the result. Candidate
   `v092-go-focused-20260801022756` reproduced only the target behavior in
   default and race: status/backend calls were `200/1`, not `429/0`. Its
   archive SHA-256 is
   `1291a496c56c6a58e5da3a15274c075e24fabbc332e081f62489579dbc70afbe`;
   log/status SHA-256 values are respectively
   `d6a167a0586c36713a096ea1d798cbb0f672fe455c02b3474a6827a512332aa4`
   and
   `f7fde70fe7b6f03dc3b65107ab6e0057f8efc0ffb432eddc0e14d583d8d0c0a8`.
   Forward commit now occurs after local backend/priority checks but before
   accepted counters, active tracking, headers, or proxy execution. `enforce`
   rejects a false or panicking commit before upstream access and releases the
   predictive reservation as a local QoS reject; `shadow` preserves upstream
   and client behavior. Candidate `v092-go-focused-20260801023009` passed the
   focused and race matrix with archive/log/status SHA-256 values
   `ecd34518685c72eb9deb2491c678b062e232241595a3b166b200c4eb79c9dd16`,
   `bf41385507d95080da7f74c4e933c721c9f357d4ffb8d24e0350ccb78e51d79b`,
   and
   `a26e6c85f8aba02928d9fa370356c55513ff513381ccb8876da2611e8a60bac5`.

2. Missing vLLM `running`, `waiting`, or `preemptions` metrics were silently
   converted to zero. Fractional request counts were truncated, and a
   non-finite preemption counter could survive unsigned conversion. Candidate
   `v092-go-focused-20260801023644` failed only the five intended missing or
   malformed cases in default and race. Its archive/log/status SHA-256 values
   are
   `21106431c726eb52a665357bcf90b507a1241e188ae3307d9bf030636dbaf937`,
   `5912011fa170976008b9c33dfe4d425047cd50d3daf3a26d1653cf776cf7ae96`,
   and
   `4174c33b4dbefb3d6bad8377655a7312b40b69e887a85210df4b315e605dab79`.
   Telemetry now carries explicit validity for running, waiting,
   preemptions, and generation. Required counters must be present, finite,
   non-negative, integral, and exactly representable before reconciliation.
   Candidate `v092-go-focused-20260801023824` passed with archive/log/status
   SHA-256 values
   `e80e8c8a0a27867827ef6871fff4af510e61340bb8187e341230241708d8b39b`,
   `7b1449d569a6dda83e73fd1d93e23e5aa5b4a03be72e0e5a945274a87493aa6b`,
   and
   `34a837e99a8cf0beaede3e81f5861a9e50fd1cfb71d7469767bf120d85ab36eb`.

3. Predictive mode required a bounded JSON body but did not itself require a
   positive classifier concurrency limit. With legacy output classification
   disabled, `JSON_CLASSIFY_LIMIT=0` created a nil semaphore and unlimited
   concurrent body copy/parse/tokenizer work. Candidate
   `v092-go-focused-20260801024226` is the valid red after the runner was
   extended to include `internal/config/pigconfig`; both `shadow` and `enforce`
   incorrectly validated. Its archive/log/status SHA-256 values are
   `b364a6ce32d7b62dffeadc4cc7581553b369b27426e73af191c8b0ee37a642e2`,
   `1460d090134ad8332bf37fd74bcdfc0f174ed4adb1aa926c920d576596e29e66`,
   and
   `f578de28cb0f368f35c1a571974d80da83f22f3c6e857985fa1a112ba43f596d`.
   Both predictive modes now require a positive limit independently of the
   legacy classifier. Candidate `v092-go-focused-20260801024320` passed with
   archive/log/status SHA-256 values
   `6685bf40d9b4132110b60cd21225f05f49ac6ed3d11bd64371b19fdf378f4afd`,
   `96e7f9cf9485757d68321acee06e82e40e07d5c1a30ad163102a6ae30c3edc58`,
   and
   `1879d9a4f7545ff01e4b7e5c05cc7f369d8aa029894fa19728cd34f112a58d8d`.
   Candidate `v092-go-focused-20260801024006` used a fixture still guarded by
   the legacy classifier, and candidate `v092-go-focused-20260801024108` used
   the corrected fixture before the runner included the config package. Their
   green results are rejected for this behavior and qualify no fix.

4. A forward-commit panic was reported as `phase="semantic"`, obscuring the
   boundary that failed. Candidate `v092-go-focused-20260801024423` failed only
   the intended phase assertion in default and race. Its archive/log/status
   SHA-256 values are
   `301b135b77296d920ad5a6d3bf6b331c293d52816a8ad66a8e4a0f00b4356199`,
   `e049b79fb954d5d359812659cbc9e41eac00e09e235f0718f0349c241a55692f`,
   and
   `156534d7ad7782076e67de074db855e9f9681fc954142be27a840d80f010fea8`.
   Forward now has its own fixed `phase="forward"` counter; semantic remains
   limited to prefill/semantic-output observation. Candidate
   `v092-go-focused-20260801024555` passed with archive/log/status SHA-256
   values
   `d9e8f829dc99297d8c879f1a78c8531f979ea7f63ba24c5b4f0b49090dfbaa6e`,
   `bff64e7aa5ed891c789db69cedb38a47f3e1d857136027122ddadcf1c507fd7b`,
   and
   `583d81ef78bf3de2b037637208732fee32242c99d6142613dba9cfbbb888be99`.

5. The Prometheus fetcher read through a 4 MiB `LimitReader` but could not tell
   a complete response from a truncated one. A valid prefix followed by omitted
   series could therefore authorize an under-aggregated sample. Candidate
   `v092-go-focused-20260801024722` failed only this oversized-response test in
   default and race. Its archive/log/status SHA-256 values are
   `87e0e716b57ae1407f6f3fd07558996def800be10b532e67243b5a76142565f9`,
   `346f7533a2f048ef1322461401c8e762714459b41e2dd5dfeff6bc17bdc2da54`,
   and
   `1e6bed17356ae2f19232947f750e451655b8e6c663cdad32d35fa9985b0697ef`.
   Fetch now reads at most limit plus one byte and rejects overflow rather than
   parsing a prefix. Candidate `v092-go-focused-20260801024820` passed with
   archive/log/status SHA-256 values
   `687646060d9b2c40cc297a936f1b745f5cd1a3a0262eaace597c3611e4d918a1`,
   `c7b478614e2d71b4c53dab75076c0bf7ee0fc783fcfb02f8fee0d5fce7f0638b`,
   and
   `78f59a81d6d5b73de389cf883c98d6d6a56cc7033c12089c0d773383822a7f10`.

6. Capacity equality alone did not prove the observable runtime model or KV
   block geometry. Read-only inspection of stock vLLM `v0.26.0` source commit
   `568afb3a13806beb53bb2e6bd518269357b237c0` confirmed that the four required
   request/counter metrics carry `model_name`, while `cache_config_info` carries
   `block_size`. Candidate `v092-go-focused-20260801025141` showed that wrong
   block size, a consistently wrong served model, and mixed model labels all
   became healthy. Its archive/log/status SHA-256 values are
   `e7cf9799fd1cd2d565981cda09f59094cab6bd3b610ed369415dd7abb94fe555`,
   `e5a329424d26a333d0c29fd3418eba4b11f1bb8903e811f92c62e50f52bdce3d`,
   and
   `e1037bccdf2efd54c66f99b3f55a79bc290bedd7cc60f0dba2bf68a81262d2fa`.
   The observer now requires every required metric family to carry the same
   non-empty model label matching the immutable profile, and requires the
   observed positive integer block size to equal the profile before any sample
   is reconciled. Candidate `v092-go-focused-20260801025515` passed with
   archive/log/status SHA-256 values
   `2679f2de65c8af9a9a886d95b9986863c9a3f7d8ae00a10d94bbff0cef2fa8e4`,
   `3124c10674ef61e4ce1e968416e7a99f895e49379822666d9e528d2ffd2b7cd9`,
   and
   `85fe7d34416e3c7f7cf68b7d08d54e48077e6566d0bebdcfed8cf06155e8e624`.

Every valid or rejected candidate above had an empty builder format patch with
SHA-256
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
The focused green candidates prove only their named default/race scopes.

Stock vLLM metrics do not expose the container image digest, backend source
revision, or model checkpoint revision as a stable runtime identity protocol.
Those values remain mandatory immutable manifest and deployment-configuration
pins; this source review does not misstate them as dynamically observed. PIG
now dynamically verifies every identity component that the pinned stock
metrics expose and fails closed on missing or drifting capacity, block size,
served model, required counters, generation epoch, or preemption epoch. The
final release remains simulation/builder-only: no image was built or published,
no CVM or router was changed, and no production inference endpoint was called.

Pass-2 result: the prospective HTTP commit, bounded classifier, reservation
lifecycle, reconciliation, telemetry completeness, counter epochs, observable
runtime identity, privacy/cardinality, and race boundaries are safety-green in
the expanded focused matrix. The evidence pass must next audit which candidate
proves which claim, rerun corrected simulation acceptance, and reject any
full-release statement until one exact versioned source archive passes the
complete default/native/Rust/oracle/benchmark matrix.

### 19.16 Review pass 3: evidence validity and corrected acceptance

> Superseded for release qualification by Section 19.17. The full-matrix
> tokenizer benchmark below exposed a runtime TTFT-causality omission after
> this pass. The evidence and rejected-runner audit remain valid history, but
> its `45024` candidate aggregate is not the final v0.9.2 result.

This pass audited the causal scope of every review candidate, regenerated the
goodput report from the current uncommitted source archive, independently
recomputed its acceptance metrics rather than inheriting v0.9.1 constants,
reran the progressive-KV ground-truth test, and measured the two bounded hot
paths most directly affected by the safety review. It also rechecked the
completion claims against the builder-only and no-deployment boundary.

The earlier v0.9.1 aggregate values `5856`, `3040`, and `12256` are invalid for
this source and are not release expectations. Candidate
`v092-evidence-20260801030256` generated the current report and passed the Go
acceptance suite, progressive-KV test, no-cache architecture check, formatting
identity check, and both benchmarks, but its independent validator incorrectly
assumed a 16-token simulation block. The simulator uses a 64-token block, so
the runner incorrectly compared the observed repeated-prefix peak `13568`
against `13376`. This is rejected evidence: it identifies a validator fixture
error and proves no failed product behavior. Its source archive SHA-256 was
`a781e4295b78afbda4829281f84297b31748d6e6793c0e917a19911e112ce3fd`;
the final remote log/status/report/benchmark/manifest SHA-256 values were
respectively
`58192acee7c7007de3a97338a8e9cc04a775cc4a6d5ae62f7efc342a437c32f6`,
`4c1705355d462a0a1c39bbe3e4e43f57e44bc04324265d8aa542a57ae52aa82c`,
`672ee58b2bd22497c2e825979491b1dae8d37ef145900ada4270840d24df83c6`,
`8fa76f3ded269c6ae7844d704a1a40550cdcad9f72b98ba97d8fc59373e6563c`,
and
`9b06a0a83438564efd8cc2424b4ecd2282e4721b9cc490fae4335931a7d7eb72`.
No validation JSON was produced, which is another explicit reason it cannot be
used as green evidence.

After deriving the cache-cold expected value from the same 64-token simulation
block, candidate `v092-evidence-20260801030353` passed every evidence-review
gate on the same source archive. The independently recomputed aggregates were:

| Policy | SLO completion-token goodput | SLO-compliant completions | Safety violations | False accepts | Reservation leaks |
|---|---:|---:|---:|---:|---:|
| current threshold | 38624 | 28 | 2 | 2 | 0 |
| v0.9.0 KV-only | 35808 | 22 | 70 | 20 | 0 |
| exact-token KV-only | 35296 | 20 | 74 | 20 | 0 |
| tokenizer-first predictive QoS | 45024 | 46 | 0 | 0 | 0 |

Predictive goodput improved by `16.570008285004143%` over current threshold
and `25.737265415549597%` over v0.9.0 KV-only. Seven scenarios strictly beat
both baselines, exceeding the three-scenario gate. The cache-cold long-prompt
aggregate was `2880` against the per-scenario best-baseline aggregate `832`, a
`246.15384615384616%` improvement. The repeated-prefix scenario reserved
exactly four complete rounded cache-cold request costs: peak reserved KV was
`13568` tokens and no prefix-residency or cache discount was modeled.

The test and validator intentionally distinguish two different quantities.
Simulation ground truth grows materialized KV with actual decode progress:
49 input tokens round to 64 at admission, 549 materialized context tokens
round to 576 after 500 generated tokens, and 1049 materialized context tokens
round to 1088 at completion. Runtime admission does not receive that future
knowledge; it reserves the complete cache-cold future upper bound prospectively
until safe reconciliation/terminal release. Progressive simulator truth is
therefore not a runtime discount and does not weaken the reservation invariant.

On the builder's `linux/amd64` Go 1.24.5 environment, five scheduler samples
measured `1391-2087 ns/op`, `128 B/op`, and `1 alloc/op` for calibrated TTFT
prediction. The capacity-full retired-ring push measured `12.66-13.75 ns/op`,
`0 B/op`, and `0 allocs/op`. These are bounded microbenchmark evidence, not an
end-to-end latency or production-capacity claim.

Immutable green evidence for `v092-evidence-20260801030353` is:

- source archive SHA-256
  `a781e4295b78afbda4829281f84297b31748d6e6793c0e917a19911e112ce3fd`;
- final log `/work/pig-v092-evidence/v092-evidence-20260801030353.log`,
  SHA-256
  `de03107ec3d0697c984e8be721ed61578953428c88cf8f05d78c0b0c0ecd5848`;
- status SHA-256
  `0d092239809a8319cfbeb6605913bf056dfc9f230ee6834ca5e3966c3b641c67`;
- empty formatting patch SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- goodput JSON SHA-256
  `672ee58b2bd22497c2e825979491b1dae8d37ef145900ada4270840d24df83c6`;
- independent validation JSON SHA-256
  `a2c02f64c212d3a515a7d9b1dd150adb0bac1acfe871a234b7249b1cb9b5a41c`;
- benchmark report SHA-256
  `2a8e703297530916dc1237490788aec658db0fa56d49339105514c0bc9ee3a31`;
- source manifest SHA-256
  `9b06a0a83438564efd8cc2424b4ecd2282e4721b9cc490fae4335931a7d7eb72`.

Evidence-scope audit remains strict. Candidates
`v092-go-focused-20260801024006` and
`v092-go-focused-20260801024108` do not prove predictive classifier
concurrency because the first used a fixture still rejected by the legacy
classifier and the second ran before the config package entered the runner.
`v092-go-focused-20260801024226` is the valid behavioral red and
`v092-go-focused-20260801024320` its corresponding focused green. All focused
candidates prove only their named default/race packages; none proves native
CGO/FFI, Rust, immutable tokenizer/template oracles, the full repository,
release version identity, an image, registry publication, or a deployment.

The learning claim is unchanged by simulation. The live HTTP path supplies a
qualified semantic TTFT target and targetless censored safety evidence.
TPS/TPOT residual-target logic is tested synthetically, but live HTTP has no
reliable per-request backend timing/usage source for those targets, so runtime
TPS/TPOT remain conservative static priors. Feedback trains only later
predictions; it does not late-veto an already forwarded request. No test or
report above is described as live TPS/TPOT learning.

Pass-3 result: the three plan-review passes are complete, the current corrected
simulation clears the predeclared goodput and safety gates, the cache-cold and
progressive-KV meanings are unambiguous, and rejected evidence is separated
from valid red/green evidence. The source is ready for the version bump and one
exact `PIG-v0.9.2` source-archive release matrix. Until that complete matrix
passes, this remains a source candidate only. No image was built or published,
no CVM/router/upstream was modified, and no production inference endpoint was
called.

### 19.17 Post-pass correction: accrued local admission latency is causal TTFT

The first complete-matrix attempt, `v092-full-20260801031101`, is rejected
runner evidence. Default Go, full default race, Rust, goodput, and benchmark
gates passed, but the runner looked for the production oracle in the new
evidence directory instead of its pinned read-only location, let `run_cargo`
change the caller's current directory before relative native-surface checks,
and assumed the builder had the `file` utility. Two audit functions also
continued after intermediate command failures and could return a false green.
No native/oracle/release conclusion is taken from that run. The source archive
SHA-256 was
`909d9424b90853b054fa0399dbf025251b32a1db48b46e97a3414068f3068583`;
the final log/status SHA-256 values were
`3128cddda1905e609e332f4a895a1bff4e079f83601fb2013c5322d95ea3a736`
and
`7b782ce7b5cf308a813f6cd8751c3b209689bc5c51267e3549866d61eb05e666`.

After the runner used subshells, fail-fast audits, the verified oracle path,
and no unavailable utility, `v092-full-20260801031835` passed the whole matrix
on that same pre-correction archive. Its final log/status/source-manifest
SHA-256 values were respectively
`fdfd5d740f591688287bc84e5bf61274c5271801da4b9c71140d576ecb0c2add`,
`388ebb73a36c08071c2543a99ec3bd00d02961268f684c0b2f010c7dda3dbee9`,
and
`9b42788451e544ec3785503231e00ea01b081efbd4503466f40570757425d513`.
That green is nevertheless superseded rather than promoted: its count-only
benchmark showed a 64k-token p95 of approximately `614.877 ms`, above the
simulation fixture's `587.303 ms`, and a 3k-token p95 of approximately
`8.870 ms`, above `8.612 ms`.

Tracing the real timing path found a release-blocking causal omission. The HTTP
semantic TTFT outcome is measured from `requestStart`, so it includes request
classification, rendering, tokenization, and backend latency. The cold-start
runtime prior used only backend base TTFT plus uncached-prefill cost. The
simulator added tokenizer p95 to ground truth but did not pass that known local
cost into the prospective controller. Consequently feedback might eventually
inflate a residual, but the first request and a shifted input length could be
admitted using an understated TTFT forecast. This violated the rule that
feedback may improve the next prediction but may not substitute for the
current pre-forward prediction.

Valid behavioral red `v092-go-focused-20260801032858` added only an HTTP
`enforce` test. A 25 ms tokenizer with a 10 ms backend prior and 20 ms TTFT SLO
was classified as `fit` and reached the backend instead of producing
`ttft_at_risk` before forwarding. The default failure was the intended
behavioral red. Its race phase later failed to link because `/work` had only
20 MiB free, so that phase is rejected environmental evidence and does not
qualify a second red. Archive/log/status/empty-format-patch SHA-256 values were
respectively
`c3aff413a450c7397878f0e6212a2fe3db4bf9bdab918e627d0d79f743d5b293`,
`a5b40ff4c97e38d57b865ed6c7f68cbb046cf6fea719e3350e43a564e51a28b0`,
`776cf04150268d007dd8eda3b9ff128dd8776ff42bc85a73d8cb4cfca028d159`,
and
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
Only the two explicitly resolved full-matrix worktrees, their before-copies,
input archives, and scripts were removed after absolute-path and size checks;
all `/work/pig-v092-evidence` artifacts were preserved. Available builder
space increased from 20 MiB to 801 MiB.

The corrected transaction now carries
`AccruedLocalAdmissionLatency` from the same HTTP `requestStart` used by
semantic TTFT through the count proposal, count cost, domain request cost, and
scheduler features. At the decision boundary this value is already observed,
not guessed: it covers classification, rendering, and tokenization elapsed so
far. The static TTFT upper bound adds it saturatingly to backend base plus
uncached-prefill TTFT before constraints are evaluated. The stored prediction
therefore also gives the learner an end-to-end prior with the same timing
origin as the later semantic outcome. Negative local latency fails closed.
Direct adapter callers without an HTTP start timestamp conservatively use the
adapter prediction start. No new cache, route, backend-feedback veto, or
unbounded state was introduced.

The deterministic simulator passes its pinned tokenizer cost through the same
proposal/scheduler path. Based on the maximum of retained and current builder
evidence, the 3k and 65k p95 fixtures were rounded upward to `9 ms` and
`650 ms`; the already-more-conservative 49, 24k, and 131k values remain
`52.539 us`, `132.639 ms`, and `1.516 s`. This is an acceptance fixture, not a
runtime lookup: runtime charges the current request's actually accrued local
duration.

Focused green `v092-go-focused-20260801033241` proved the initial vertical
fix in default and race. Archive/log/status SHA-256 values were
`32733a5aa96c6d9b5ac90f82ff9ffd882fc8343ec027be697dc31496ad384161`,
`e964c6d34aef8fab4b35389a760c155338ca463e9c2ba9f18fc82005d66a26e6`,
and
`140837b8149339762e32875110db618d1b93d49bb8df8663c99ec18b733f4633`.
After extending the timing origin to HTTP `requestStart` and adding the
negative-duration contract, `v092-go-focused-20260801033526` again passed the
expanded default/race packages. Its archive/log/status SHA-256 values were
`2f764b9429678e7cbdc89109e5e791fed3737827f41f0c89bc46b402a50d39dd`,
`befb12f6d466c09721b94e9cad3b5b1ff448b36a9fa47ae5fd0aa548371f2585`,
and
`12ceedc2ce7042026bd41b702d3c150f3c7955b51131827d981f615913bd94a8`.
Both format patches were empty with the standard empty SHA-256.

Corrected evidence candidate `v092-evidence-20260801033347` passed all 19
scenarios and independent validation. The safer prediction admitted 49 and
completed 45 requests rather than 50/46, because one long-prompt completion
whose full local-plus-backend TTFT was unsafe is now denied prospectively.
Predictive completion-token goodput is `44992`, with zero TPS, TTFT, TPOT, KV,
preemption-proxy, false-accept, or reservation-leak violations. It remains
`16.48715824357912%` above current threshold and
`25.647899910634496%` above v0.9.0 KV-only. Seven scenarios still strictly beat
both baselines; cache-cold long-prompt goodput is `2848` against best-baseline
`832`, and repeated-prefix peak reservation remains the complete cold cost
`13568`.

Immutable corrected evidence is:

- source archive SHA-256
  `7bc3129b4a0b0279922696b2be0efd820154de254f18f8a63056216b7093c24f`;
- final log SHA-256
  `68e633aafa192c5d94b477d4dbaee02f90cd3ea94849a6f6f6e5f91192faf00b`;
- status SHA-256
  `b8bcc1588c8373e6bbec04e4dcff79a219df771603a569c9fa1f6e6cd23cd839`;
- empty format patch SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- goodput JSON SHA-256
  `24d984169544cb16d952ff50c010cc86570fca1ca4a9d3e80b681adb0f1346cc`;
- independent validation JSON SHA-256
  `0bdfb6199de43c6385a82b83901af06e9ff29d04e3a74cea69bc7f14c628256b`;
- benchmark report SHA-256
  `95cc32b7b2b95cb1de72f9da5bef2af324257aca580bb3813deead040a224684`;
- source manifest SHA-256
  `f75dfe2320a4f2ab1a875f78f5454098f20e8dce3b6921c0c94c5000476d4c49`.

This correction improves prediction authenticity rather than maximizing raw
admissions: a small goodput reduction is accepted because the removed
completion was outside the TTFT SLO. The complete release matrix must now be
rerun on one archive containing this implementation and evidence record; all
pre-correction full-matrix results remain superseded.

### 19.18 Final PIG-v0.9.2 exact-archive release matrix

Candidate `v092-full-20260801033723` passed the complete fail-fast matrix on
one immutable source archive. The archive SHA-256 is
`0b7776959100768247341820bd4c9a9502bb0ee5746318a582de5102bd9d2ad2`.
The archive contains runtime version `PIG-v0.9.2`, the accrued-local-TTFT
correction, the corrected simulation fixtures, all tests, and this plan through
Section 19.17. The builder recorded base Git HEAD
`8ce939e1b35cd684a8e9568365fd9504ef7613be` only as ancestry; the tested object
is the archive including uncommitted changes, not that older commit.

The environment was `linux/amd64` on host `6aff8e9be30d`, Go 1.24.5,
Rust 1.97.0, Cargo 1.97.0, Python 3.12.3, and Git 2.43.0. Every named gate
returned zero:

1. archive path/content audit, source manifest/audit, empty gofmt patch, and
   staged-equivalent whitespace check;
2. exact source version, profile schema 3, bounded `calibrator_maximum_cells`,
   Python compilation, immutable asset hashes, no-cache architecture scan, and
   Go/Cargo dependency reports;
3. full default `go vet`, build, all tests, and all race tests;
4. Rust `fmt --check`, locked all-target tests, and locked release build;
5. native surface, explicit renderer/count oracle and race oracle, exact final
   token IDs, full native vet/build/all tests/all race tests;
6. verbose deterministic goodput suite, generated report, independent exact
   validation, five count-only/vector benchmarks and parity validation;
7. renderer, learned-scheduler, retired-ring, and native short-count
   benchmarks.

The immutable tokenizer, tokenizer config, chat template, and five-case oracle
SHA-256 values remained:

- tokenizer
  `cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f`;
- tokenizer config
  `e467669cfe172dfb0c4e7de7bfbe7553c42bfa5de95acd71f423f58a434d80de`;
- chat template
  `afdbb2abe3667ccde95cc2f86919f05370339399bab5f750950a4390523b8927`;
- oracle
  `0161539eae267099adcda3d04b240b800e12a292d96a6bea9192865a71b0955a`.

All five exact final-ID comparisons had zero mismatches. Their count/compact-ID
SHA-256 values were:

| Case | Tokens | Special tokens | Compact final-ID SHA-256 |
|---|---:|---:|---|
| chat_user_text | 14 | false | `87a6d60b92d2c909d9b0ce959324882a7e860a0c9bd82f6e8642ec83f7be4f50` |
| chat_system_user_text | 21 | false | `1ea7f3fdd2b875c27b5d90598e89ead6e5dcfcf3f5bd8fd2ef3ef781dcc4462f` |
| chat_multi_turn_text | 30 | false | `95837b3ee681d3adf745cde3758f0ad525fee5539afb0cea0989f33a9deaa5ba` |
| chat_text_parts | 21 | false | `075651d11a2603dd0dd4552bbfeed4f176c5e18ef7658d0d6d023d45b67900ee` |
| completion_text | 2 | true | `b6f384a779426948b488011ae2f419d58247e86e87d3199425dd99b30f6971b3` |

The release native library and CLI were 5,839,576 and 5,939,416 bytes. The
shared library exported exactly four intended symbols:
`pig_tokenizer_abi_version`, `pig_tokenizer_count`,
`pig_tokenizer_count_destroy`, and `pig_tokenizer_count_open`. Go and Rust both
pinned ABI version 3; no analysis/open/cache surface returned.

The corrected deterministic aggregates remained:

| Policy | Completion-token goodput | SLO completions | Safety violations | False accepts | Leaks |
|---|---:|---:|---:|---:|---:|
| current threshold | 38624 | 28 | 2 | 2 | 0 |
| v0.9.0 KV-only | 35808 | 22 | 70 | 20 | 0 |
| exact-token KV-only | 35296 | 20 | 74 | 20 | 0 |
| tokenizer-first predictive QoS | 44992 | 45 | 0 | 0 | 0 |

Predictive goodput remained 16.49% above current threshold and 25.65% above
v0.9.0 KV-only, with seven strict dual-baseline improvements. Long-prompt
goodput remained 2848 versus best-baseline 832; the full repeated-prefix
cache-cold reservation remained 13568; progressive materialized ground truth
and full-future runtime reservation remained separate and unchanged.

The final count-only benchmark measured p95 approximately `40.230 us`,
`8.156 ms`, `111.840 ms`, `509.945 ms`, and `1.427 s` at the five pinned token
counts. Each count matched vector-ID token count and no block/cache analysis
schema existed. These values sit below the conservative simulation fixtures;
runtime still charges actually accrued local latency rather than consulting the
benchmark. Five renderer samples were `8.501-14.764 ms/op`, approximately
912.3 KiB/op, and 59 allocations/op for the fixed long-chat fixture. Learned
scheduler prediction was `1.198-2.032 us/op`, 128 B/op, one allocation/op;
full-capacity retired-ring push was `11.31-12.05 ns/op`, zero bytes and zero
allocations; native short count was `5.986-6.913 us/op`, 520 B/op, two
allocations. These are builder microbenchmarks, not production latency or
capacity claims.

Immutable final evidence SHA-256 values are:

- final log
  `ecd9e46dd767425f432e9b320f886da36fdbe00d6fed055ddd19f4ac7141f043`;
- status
  `ca1dee7dcd7efdecf2aa25dde6d90b9c1dc4119bf552f4499c15e6487d3efb02`;
- empty format patch
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- archive listing
  `d8fdbacbeb4cce44fc8cc202fd52526ba54428136e2ee653309b4c4d4dadabd0`;
- source manifest
  `4d7143a6c89e22ea086980609b4e25c73cba3c972d40f6356a86ff120295fb2c`;
- source audit
  `404b23d92c1d4025addcee59d8740b57ca45c576194359ca04cc4259b5a0871a`;
- Go dependencies
  `470f047f3ee7b7e5f8b18f74cbf29826993c3606f6746f4d5c403103c9c52bc4`;
- Cargo dependencies
  `721ca8ea3b16fcc6656ece8a8d6c13cda2a2874ab36a8dc4e8ce91f4f07874f3`;
- goodput JSON
  `24d984169544cb16d952ff50c010cc86570fca1ca4a9d3e80b681adb0f1346cc`;
- goodput validation JSON
  `47435da4edc0dfe7f888765fe5d06fc495780e7de5a16c4ca831980d7e621d43`;
- count benchmark
  `43951d4342cb3032052a3eb7adf712fa737d7568e93340aabecb30494e83f4a9`;
- renderer benchmark
  `144c5ba56133803b919a23c5174cbe3f4daf510b71438768d7df01a27d30e322`;
- runtime benchmark
  `1c2bcc9bd4d763fe2788ecfc0f4b5e1a611f45983b016523fe2a26db6589eee0`;
- exact final-token-ID report
  `f2a49a39d2f625bd825f939bf0da804d10f64a56fb363e94736a658d3ae72431`;
- native surface
  `693fc16b6327425a90e3d2cca2e26e5b25f7e4bd007442e25f7047232dd582e8`.

This establishes a builder-validated source release candidate only. No Docker
image was built, no image or version was published to a registry, no CVM,
router, or upstream configuration was changed, and no production inference
request was sent. A final document-only archive must prove that writing this
section changed no executable source object before commit and push.

### 19.19 Document-only executable identity proof

Candidate `v092-doc-proof-20260801034618` passed the document-only builder
proof. Its final archive SHA-256 was
`853b41d35c2a87dcfeffd877d84bd0a5b1c72c301d396c34f3b9c709f19631df`;
it was compared against tested full archive
`0b7776959100768247341820bd4c9a9502bb0ee5746318a582de5102bd9d2ad2`.
After excluding no path except the plan itself, every remaining source-manifest
line was byte-identical. The complete manifest diff contained exactly the old
and new SHA-256 lines for
`docs/TOKENIZER_FIRST_PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`: the document changed
from
`d55242ef6816a1a22254e773b142b86d37c3543657c26a2d4ddb6975a867099a`
to
`fa861cef14facb70be27ad3feee32d453e0881ff43b8a979826410e613c763a6`.

Archive audit, executable-manifest identity, final-document evidence, runtime
version, tracked gofmt, Rust fmt, staged-equivalent whitespace, and no-cache
architecture gates all returned zero. Final log/status/manifest/diff SHA-256
values were respectively
`2a950cd41b66be45fef869061105b4f9fbc9c99e267be08a56c9b7aa9c584aa3`,
`e61803ea07ed6b407cf4d48423afe0f8204c99d17d7c30b9cd2db38cdfcab9b0`,
`cc8e174b213c42ab4e1b42550166ec7c572099b4500c6b7d5229fb4fcce2f959`,
and
`c8b93daba3e511966d0940fce09f1095c382a8c8189cf340c6ea239e7dbf6b75`.

Recording this proof changes the plan once more but no executable source. One
final unrecorded manifest comparison must therefore run after this section;
its result is the commit/push handoff evidence rather than another recursive
document edit.
