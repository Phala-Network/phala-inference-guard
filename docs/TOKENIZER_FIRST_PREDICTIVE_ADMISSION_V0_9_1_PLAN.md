# PIG v0.9.1 Tokenizer-First Predictive Admission Plan

Status: implemented release candidate; count-only HTTP prediction, fresh vLLM
reconciliation, attributed semantic-TTFT learning, and deterministic goodput
gates are builder-green; the final clean-builder release matrix remains pending

Supersedes: `PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`

Version target: PIG-v0.9.1

Release-candidate source version: PIG-v0.9.1; it remains unpublished and
undeployed until the exact final-commit gates pass

Control mode: off or shadow only

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
