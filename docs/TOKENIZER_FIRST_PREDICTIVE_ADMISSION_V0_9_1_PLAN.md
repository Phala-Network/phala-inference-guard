# PIG v0.9.1 Tokenizer-First Predictive Admission Plan

Status: active reviewed plan; existing renderer/tokenizer parity is reusable,
but the cache-free admission path has not been implemented

Supersedes: `PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`

Version target: PIG-v0.9.1

Runtime version before all gates pass: PIG-v0.9.0

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
- keep runtime at PIG-v0.9.0 and stop if any gate fails;
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
