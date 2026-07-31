# PIG v0.9.1 Predictive Admission Shadow Plan

Status: reviewed implementation and test plan
Version target: PIG-v0.9.1
Control mode: off or shadow only
Routing: explicitly out of scope
Production deployment: explicitly out of scope
Test execution: remote builder only

## 1. Objective

PIG v0.9.1 will predict the effect of a request before forwarding it to the
single local upstream. It will use tokenizer-exact request tokens,
backend-specific prefix-cache state, an event-driven virtual upstream state,
and a calibrated scheduler model to answer:

> If this request is forwarded now, will the projected upstream state remain
> inside the protected KV, workspace, TTFT, TPOT, preemption, and single-user
> completion-TPS boundaries?

The product objective is:

~~~
maximize local upstream completion_tokens_per_second

subject to:
  predicted existing-user TPS lower bound >= configured target
  predicted new-user TPS lower bound >= configured target
  predicted TTFT and TPOT upper bounds <= configured SLOs
  projected KV upper bound <= protected KV budget
  projected workspace risk <= backend-specific budget
  preemption/retraction risk <= configured risk budget
  no increase in OOM, restart, or client-visible incompatibility
~~~

Prompt TPS and total TPS are explanatory metrics. They are not the optimization
target because cache hits and long prompts can inflate them without protecting
user-visible generation speed.

v0.9.1 remains shadow-only. It records what a predictive controller would
have done, but existing PIG QoS remains authoritative and client-visible
behavior is unchanged.

## 2. Scope boundary

PIG v0.9.1 is responsible only for local intake:

~~~
admit now
predict a later locally safe time
or classify the request as unsafe
~~~

It does not:

- select another CVM or backend;
- implement sticky routing, consistent hashing, or prefix affinity;
- write routing hints for an upstream router;
- move traffic between the six target CVMs;
- alter production Compose;
- deploy, restart, or send test traffic to a production CVM;
- enable predictive enforcement.

All development tests, simulations, Go/Rust builds, race tests, and image
builds run only on the remote builder CVM. The Windows checkout is used for
source editing, Git inspection, and artifact review only.

## 3. Current-code baseline

The v0.9.0 request path currently:

1. classifies the OpenAI request;
2. computes a bounded byte-based KV cost;
3. records a KV shadow decision and reservation;
4. enters the existing QoS gate using the dynamic controller's current limit;
5. forwards an admitted request and observes semantic first output,
   completion, cancellation, or failure.

The current KV shadow closes the same-poll token blind window with:

~~~
projected_high =
    observed_active_tokens
  + unabsorbed_shadow_reservations
  + decode_drift_tokens
  + estimated_input_high
  + bounded_new_request_decode_tokens
~~~

This is a useful memory-safety foundation, but it is not a complete
forward-looking scheduler model:

- input tokens are an interval rather than the backend-exact token sequence;
- cache residency and prefix block sharing are not predicted;
- prefill cost is not separated into cached and uncached tokens;
- TPS protection is derived mainly from observations after work reaches the
  backend;
- a stale waiting or TPS sample can keep intake closed after PIG-observed work
  has completed;
- fixed queue waits are not based on a predicted safe time.

## 4. Design principle: feed-forward decision, feedback calibration

The admission decision must use the predicted state after admission:

~~~
virtual_state_now
+ exact request resource cost
+ uncertainty margins
-> predicted state after admission
-> shadow admit / predicted wait / predicted reject
~~~

Backend metrics and actual request outcomes remain necessary, but their role
changes:

- request-time prediction decides what would be safe now;
- PIG-observed events update virtual state immediately;
- Prometheus samples reconcile drift;
- actual token, cache, TTFT, TPOT, and completion results calibrate prediction
  intervals;
- repeated excessive error disables predictive extra headroom.

A feedback sample must never blindly replace newer request-ledger events.

## 5. Architecture

~~~
Incoming request
  -> exact request normalization and chat-template rendering
  -> exact tokenizer
  -> backend block keys and cache-hit interval
  -> request resource-cost interval
  -> virtual scheduler simulation
  -> constraint evaluation
  -> atomic shadow reservation

PIG request events
  -> admitted
  -> semantic first output
  -> completed / cancelled / failed
  -> immediate virtual-state transition and waiter wake-up

Backend samples and response usage
  -> reconcile predicted versus observed state
  -> update error bounds and profile confidence
~~~

The implementation is divided into portable layers:

1. tokenizer manifest and tokenizer interface;
2. backend-specific cache adapter;
3. event-driven virtual state and atomic reservation ledger;
4. backend scheduler profile and simulator;
5. predictive admission domain decision;
6. observability and deterministic replay.

## 6. Exact tokenizer and template parity

### 6.1 Required output

For every supported request, the tokenizer stage returns:

~~~
model profile
tokenizer manifest
rendered-input fingerprint
exact token count
exact token IDs or backend-equivalent block keys
message/tool/schema/modality classification
max output tokens when present
support/confidence state
tokenization duration
~~~

Token count alone is insufficient for cache prediction. The predictor needs
the same token sequence and block boundaries used by the backend.

### 6.2 Template parity

PIG must reproduce the same final token IDs as the backend after applying the
same effective:

- chat template;
- special tokens;
- model and tokenizer revisions;
- tool/schema serialization;
- reasoning markers;
- BOS/EOS behavior;
- multimodal placeholder policy;
- cache salt and adapter inputs where applicable.

A tokenizer manifest binds the predictor to immutable inputs:

~~~
served model name
model repository and revision
tokenizer repository and revision
tokenizer.json SHA-256
tokenizer_config.json SHA-256
special_tokens_map.json SHA-256
chat-template SHA-256
backend kind and version
block size
multimodal processor profile
predictor profile version
~~~

If a required manifest item is missing or does not match the configured
backend profile, exact prediction is invalid. Shadow records
tokenizer_profile_unknown and falls back to the existing conservative path.

Matching tokenizer files is necessary but not sufficient. A Rust tokenizer
library does not by itself prove parity with Transformers, vLLM, or SGLang
chat-template execution. A profile becomes valid only after golden cases prove
the final rendered token IDs, special-token placement, and block boundaries
are identical to the selected backend oracle.

Supported endpoint/request classes are explicit profile capabilities, for
example:

~~~
/v1/chat/completions
/v1/completions
/v1/responses
tools and tool_choice
response_format and json_schema
reasoning controls
text-only inputs
verified multimodal inputs
~~~

Passing one endpoint or simple chat case never enables a different endpoint or
feature class.

Tokenizer assets are loaded and warmed at process startup. There is no
request-time model download and no hot-path call to the upstream tokenize
endpoint.

### 6.3 Implementation candidates

The lowest-latency candidate is an in-process Rust tokenizer runtime exposed
to Go through a narrow C ABI:

- Hugging Face tokenizers-compatible tokenizer;
- exact template renderer validated against the serving runtime;
- bounded worker pool;
- immutable per-profile tokenizer instances;
- one-pass block-hash generation;
- no HTTP or subprocess round trip.

The builder test matrix also measures a Rust helper over a local Unix socket as
a fault-isolated fallback. The hot-path choice is made from measured p95/p99,
CPU saturation, cancellation, crash-containment, and parity evidence rather
than latency alone. An FFI panic, invalid pointer, or tokenizer failure must
not terminate or corrupt the PIG serving path; if the in-process candidate
cannot meet that gate, the isolated helper is preferred.

A Python or upstream tokenizer is used only as a builder-only golden oracle,
not as the production hot path. Golden fixtures record immutable oracle
version, model/tokenizer/template hashes, request-class input, and final token
IDs without storing production prompts.

Tokenizer assets may come from an immutable profile bundle in the PIG image or
a read-only model-cache mount. The selected delivery method must prove the same
manifest and must not make the first request download assets.

### 6.4 Unsupported requests

The first implementation treats a request as unsupported for exact predictive
fit when it cannot reproduce backend tokenization, including an unverified
multimodal processor, unknown prompt adapter, unknown cache salt semantics, or
unsupported input schema.

Unsupported means unknown, not zero cost. In shadow it remains fail-open for
real traffic while recording the conservative fallback result.

A tokenizer/profile error never consumes a cache discount. If the existing
byte estimator can still produce a conservative KV interval, shadow records
both the predictive-profile failure and that fallback result.

## 7. Cache-aware local state

Tokenizer output is divided according to the backend's real cache unit:

- vLLM: full token-prefix blocks using the reported block size and effective
  cache-key inputs;
- SGLang: token-prefix/radix state with separate active, evictable, and free
  semantics.

PIG does not need to reproduce a backend process's randomized in-memory hash
value. Its mirror identity is a process-local keyed digest of the verified
token-block semantic inputs. A backend-equivalent hash value is used only when
the backend hash algorithm, seed/salt, and all extra keys are explicitly part
of a validated profile. Prediction correctness is judged by prefix-token and
block-boundary parity, not by coincidentally equal opaque hash bytes.

The local cache mirror has four confidence states:

| State | Meaning | Hard-safety use |
|---|---|---|
| definitely_active | A PIG-tracked active request currently references a completed block. | May reduce confirmed new physical allocation. |
| pending_prefill | A tracked request contains the block but its prefill completion is not confirmed. | Count as miss unless backend behavior proves safe reuse. |
| probably_resident | A completed request may have left the block in prefix cache. | Use only in expected-cost prediction or a calibrated lower bound. |
| evicted_or_unknown | No reliable residency evidence exists. | Count as miss. |

The cache mirror is:

- bounded by configured blocks and memory;
- scoped to one backend epoch and one tokenizer manifest;
- cleared or quarantined on backend restart, generation reset, block-size
  change, tokenizer/profile change, or material capacity change;
- reconciled with backend cache query/hit deltas;
- never exported as prompt or block-hash Prometheus labels.

After a PIG restart the completed-prefix mirror starts cold. Pre-existing
backend cache entries are unknown unless a separately validated read-only
backend snapshot/probe provides evidence. Unknown pre-existing entries improve
actual performance if hit, but they are treated as misses in the hard
prediction until learned safely.

PIG stores no raw prompt in cache telemetry. Any in-memory fingerprint uses a
process-local keyed hash, bounded TTL, and no high-cardinality metric label.

### 7.1 Cache-hit interval

For a request:

~~~
cached_tokens_certain
cached_tokens_lower
cached_tokens_expected
cached_tokens_upper
~~~

Hard KV safety uses certain or validated lower-bound cache hits. Expected
prefill and TTFT may use expected hits. A low hit assumption is used for the
TTFT/TPS safety upper bound.

The predictor never subtracts the backend's aggregate cache-hit rate from an
individual request.

Unknown cache state normally means a conservative miss, not a failed
admission. cache_state_unknown is returned only when a decision or predictive
extra fit explicitly depends on a cache discount that cannot meet its
confidence requirement.

### 7.2 vLLM accounting

vLLM prediction separates:

~~~
resident shared prefix blocks
newly allocated prompt blocks
pending prompt blocks
decode-horizon growth blocks
~~~

Conceptually, before backend block rounding:

~~~
physical_increment_high =
    exact_input_tokens
  - certainly_resident_prefix_tokens
  + decode_horizon_high
~~~

The actual implementation rounds to backend block units and includes partial
last-block behavior, copy-on-write behavior, decode growth from the current
partial block, and backend cache-key parity. Its state separates:

~~~
new physical blocks
already active shared blocks
resident but not active blocks
newly pinned/non-evictable blocks
partial blocks requiring private allocation
~~~

### 7.3 SGLang accounting

SGLang prediction tracks:

~~~
active non-evictable tokens
evictable radix-cache tokens
free tokens
new physical allocation
cached tokens becoming pinned/active
~~~

A hit on an evictable prefix may add no physical allocation while increasing
non-evictable active pressure. Both projected physical occupancy and projected
active pressure must pass their budgets.

EAGLE/DeepGEMM workspace risk remains a separate constraint and cannot be
cancelled by cache hits.

## 8. Event-driven virtual upstream state

At time now, virtual state is an interval rather than an unqualified scalar:

~~~
virtual_state_lower/upper(now) =
    assimilated_observed_state(sample_watermark)
  + definitely_unabsorbed_reservations
  + ambiguous_sample-window_events
  + predicted phase transitions
  + reconciliation drift interval
~~~

A metrics HTTP response does not prove the exact instant at which every
backend metric was read. Every poll therefore records:

~~~
poll_started_at
poll_finished_at
PIG event sequence at poll start
PIG event sequence at poll finish
backend generation/profile epoch
~~~

Events before the poll-start watermark may already be reflected in the sample.
Events after the poll-finish watermark are definitely not reflected. Events
inside the scrape window are ambiguous and widen the interval rather than being
blindly added or subtracted.

The controller tracks which reservations were present at both watermarks and
whether their resource growth has already been absorbed by a sample. A
completion releases its PIG-owned unabsorbed reservation immediately, but it
may reduce the observed baseline only when the watermark and ownership model
prove that the same work has not already disappeared from the sample. This
prevents both double-add and double-subtract.

Required state includes:

~~~
backend epoch and predictor profile
sample timestamp and age
active KV, evictable KV, free KV
unabsorbed physical and active-token reservations
prefill sequences and uncached prefill remaining
decode sequences and context-length buckets
decode-horizon reservations
cache block references
generation step profile
speculative acceptance interval
workspace risk
predicted completion/phase-transition intervals
confidence and drift bounds
sample watermarks and event sequence
known-work ownership coverage
unknown/bypass work interval
~~~

### 8.1 Exclusive-ingress assumption

Immediate virtual-state release is valid only if inference traffic cannot
bypass PIG. The profile records whether exclusive ingress is proven.

If exclusive ingress is unknown:

- completion events still release PIG-owned reservations;
- observed backend running/waiting is not decremented as if all observed work
  were PIG-owned;
- uncertainty and drift margins remain larger;
- predictive extra headroom is disabled when confidence is insufficient.

Even with exclusive ingress, a PIG restart begins with unknown ownership for
backend work that predates the process. Immediate reopening becomes eligible
only after a clean assimilation watermark establishes sufficient known-work
coverage.

### 8.2 Event transitions

- Admission inserts prefill, KV, cache, decode-horizon, and scheduler
  reservations atomically.
- Semantic first useful streaming output transitions a request from prefill to
  decode immediately.
- For non-streaming requests, phase transition remains predicted until a
  direct backend signal or completion reconciles it.
- Completion, cancellation, and failure release remaining reservations and
  wake predictive waiters immediately.
- Expiry bounds abandoned state.
- A new metrics sample reconciles rather than overwrites ledger state.
- Sample-window ambiguity widens state bounds and cannot produce a false-safe
  fit.
- A completion can bring the predicted safe time forward immediately without
  claiming the backend is idle unless ownership coverage and state upper
  bounds also prove that condition.

## 9. Scheduler and TPS predictor

### 9.1 Why a scheduler model is required

The admission cost has two distinct phases:

1. uncached prefill temporarily consumes batch/scheduler compute and may reduce
   existing-user TPS or increase TTFT;
2. the request joins decode and consumes continuing decode capacity and KV.

Cache hits mainly reduce phase 1. A high-hit request with a long output still
has substantial phase-2 cost.

### 9.2 Initial predictor form

The first implementation uses an explainable hybrid:

~~~
backend scheduler simulation
+ versioned latency/throughput lookup tables
+ calibrated quantile error margins
~~~

It does not begin with an opaque general-purpose ML model.

The backend profile contains measured or simulation-calibrated distributions:

~~~
step_time_p50/p95/p99 = f(
  backend and model profile,
  decode batch size,
  active context-token bucket,
  scheduled uncached prefill tokens,
  KV occupancy bucket,
  chunked-prefill settings,
  speculative acceptance bucket
)
~~~

The predictor produces:

~~~
existing_user_tps_lower_during_prefill
new_and_existing_user_tps_lower_after_decode_join
completion_tps_lower/expected/upper
TTFT upper interval
TPOT upper interval
KV peak upper interval
workspace peak/risk interval
preemption/retraction risk interval
earliest predicted safe time
confidence
~~~

### 9.3 Per-user TPS protection

The current aggregate approximation:

~~~
single_user_tps = generation_tps / decode_running
~~~

remains an observation and calibration signal. Enforcement-quality prediction
must protect a lower quantile or conservative weighted decode share, not only
the mean.

The predictor evaluates both:

- existing requests during the new request's prefill window;
- existing plus new request after the new request joins decode.

### 9.4 Receding horizon

PIG does not reserve every requested max-output token for the entire request.
It predicts only to the next reliable re-evaluation horizon:

- the new request's prefill completion;
- a configured number of scheduler iterations or seconds of decode;
- the next request event or reliable backend sample.

Every admission, phase transition, completion, cancellation, sample, cache
epoch change, or prediction-error threshold crossing triggers re-evaluation.

## 10. Predictive decision

For a request r:

~~~
predicted = simulate(virtual_state_now, exact_cost(r), uncertainty)
~~~

The hypothetical predictive decision is fit only when all configured
constraints pass:

~~~
predicted.KVPeakUpper <= KVHardBudget
predicted.ActiveKVUpper <= ActiveKVHardBudget
predicted.ExistingUserTPSLower >= UserTPSTarget
predicted.AllUserTPSLower >= UserTPSTarget
predicted.TTFTUpper <= TTFTSLO
predicted.TPOTUpper <= TPOTSLO
predicted.WorkspaceRiskUpper <= WorkspaceRiskBudget
predicted.PreemptionRiskUpper <= PreemptionRiskBudget
predictor confidence >= MinimumConfidence
~~~

Decision values:

| Decision | Meaning |
|---|---|
| fit | All predictive constraints pass; a shadow reservation is created. |
| kv_over_budget | Projected physical KV exceeds its protected budget. |
| active_kv_over_budget | Projected non-evictable active pressure exceeds its budget. |
| existing_tps_at_risk | New prefill/decode would reduce existing-user TPS below target. |
| new_tps_at_risk | Predicted post-join TPS lower bound is below target. |
| ttft_at_risk | Predicted request TTFT upper bound exceeds its SLO. |
| tpot_at_risk | Predicted TPOT upper bound exceeds its SLO. |
| workspace_at_risk | Backend-specific non-KV workspace risk is excessive. |
| preemption_at_risk | Preemption/retraction risk is excessive. |
| predicted_wait | Unsafe now but a bounded, confident safe time is predicted. |
| stale_state | Observed state is too old for the selected confidence mode. |
| tokenizer_profile_unknown | Tokenizer/template parity is unavailable. |
| cache_state_unknown | Required cache confidence is unavailable. |
| predictor_profile_unknown | No compatible scheduler profile exists. |
| unsupported_request | Exact resource cost cannot be produced safely. |

Failure decisions preserve full projected state, binding constraint, confidence,
and earliest-safe-time evidence.

## 11. Atomic predictive reservation

Predict and reserve occur in one critical section:

~~~
lock
  sweep expired state
  apply queued request events
  reconcile newer backend samples
  predict request effect
  record decision
  if fit, insert all resource reservations
unlock
~~~

Each reservation contains:

~~~
request id
backend and predictor epochs
tokenizer manifest id
exact input tokens and block count
cache-hit interval and block references
uncached-prefill interval
predicted prefill duration
context length
decode-horizon interval
physical/active KV increments
predicted TPS/TTFT/TPOT intervals
workspace and preemption risk
current phase
created, transition, and expiry times
~~~

Duplicate IDs, double release, reset, completion, cancellation, and expiry are
idempotent and cannot underflow virtual state.

## 12. Predicted waiting instead of fixed poll waiting

The predictor may return:

~~~
decision = predicted_wait
earliest_safe_time = now + duration
reason = binding constraint expected to clear
confidence = value
~~~

The request waits only when:

- the predicted time is within the configured client queue budget;
- confidence exceeds the wait threshold;
- the relevant state transition can be observed by PIG;
- waiting does not violate tier fairness.

Waiters wake on request events and new samples, not only a fixed timer. If the
safe time moves beyond the queue budget or confidence collapses, shadow records
the corresponding reject outcome.

v0.9.1 does not actually alter queue behavior; it records hypothetical wait
duration and wake reason.

## 13. Baseline plus predictive extra headroom

The future enforcement shape, not enabled in v0.9.1, is:

~~~
baseline capacity:
  existing validated QoS behavior

predictive extra headroom:
  requests above baseline admitted only when all forward constraints pass
~~~

Low confidence disables only predictive extra headroom. It does not make the
entire production intake depend on the new predictor.

Shadow metrics separately measure:

~~~
baseline admits
predictive extra safe admits
predictive false fits
predictive false denies
predicted GPU idle avoided
predicted completion TPS gained
SLO violations prevented
~~~

## 14. Configuration boundary

v0.9.1 supports:

~~~
PREDICTIVE_ADMISSION_MODE=off
PREDICTIVE_ADMISSION_MODE=shadow
~~~

Any enforce value fails startup validation.

Configuration is grouped and versioned:

- tokenizer/profile manifest;
- cache-mirror limits and confidence policy;
- virtual-state age and drift policy;
- backend scheduler profile;
- TPS/TTFT/TPOT targets;
- KV, active-KV, workspace, and preemption budgets;
- horizon and predicted-wait policy;
- fallback and minimum-confidence policy.

Unsafe or internally inconsistent configurations fail startup rather than
silently selecting permissive defaults.

## 15. Observability

Prometheus exports only bounded-cardinality aggregate metrics:

- mode, profile, manifest-valid, and confidence state;
- tokenizer/template latency histograms and failure reasons;
- cache certain/lower/expected hit-token buckets;
- mirror size, epoch, reset, reconciliation, and eviction counters;
- virtual prefill/decode/KV/workspace state;
- decisions by bounded reason;
- predicted versus actual KV, TTFT, TPOT, completion TPS, and cache-hit errors;
- predictive reservation lifecycle;
- predicted wait duration and wake reason;
- baseline versus predictive-extra counterfactual outcomes;
- predictor disable/fallback reasons.

Prompt text, token IDs, block hashes, request IDs, and unbounded profile values
are not Prometheus labels.

Status logs provide a bounded last-decision summary without prompt-derived
data.

## 16. Test-first implementation phases

### Phase 0: baseline and harness

- Preserve v0.9.0 tests and deterministic scenarios.
- Add predictive packages behind mode off/shadow.
- Prove off mode performs no tokenizer/cache/scheduler work.
- Add a deterministic clock and backend-profile fixtures.

### Phase 1: tokenizer interface and manifest

Tests are written before implementation for:

- manifest equality/mismatch;
- startup warm and profile validity;
- tools, response schema, special tokens, and chat-template variants;
- tokenizer reset/profile epoch;
- unknown multimodal and adapter inputs;
- bounded concurrency and cancellation;
- no upstream tokenize call;
- golden token IDs/counts against builder-only vLLM/SGLang-compatible oracles.

The initial Go tests use a deterministic fake tokenizer. The native tokenizer
integration follows only after the domain contract and failure behavior pass.

### Phase 2: cache mirror

Tests cover:

- vLLM full-block prefix matches and partial last blocks;
- chained block-key differences after one token changes;
- active, pending, probable, and unknown states;
- concurrent shared prefixes;
- conservative handling of pending blocks;
- LRU/radix eviction and capacity pressure;
- restart, block-size, manifest, and capacity epochs;
- aggregate metric reconciliation without per-request false certainty;
- SGLang active/evictable/pinning accounting;
- bounded memory and no high-cardinality metric labels.

### Phase 3: virtual state and reservation

Tests cover:

- same-window concurrent admissions;
- immediate completion/cancellation release before the next poll;
- semantic first-output prefill-to-decode transition;
- conservative non-streaming phase prediction;
- exclusive versus bypass-unknown ingress;
- sample reconciliation without overwriting newer events;
- duplicate IDs, double release, expiry, reset, and race safety;
- waiter wake-up on relevant events.

### Phase 4: scheduler and TPS predictor

Tests cover:

- uncached long prefill reducing existing-user TPS;
- cached long prefix reducing predicted prefill interference;
- high cache hit with long decode still failing TPS protection;
- existing-user and post-join TPS constraints;
- chunked prefill and decode coexistence;
- context-length buckets;
- speculative acceptance lower bounds;
- vLLM and SGLang profile separation;
- EAGLE/DeepGEMM workspace constraint;
- low-confidence profile fallback;
- receding-horizon updates.

### Phase 5: integrated decisions and replay

Counterfactual policies:

1. current count/dynamic control;
2. v0.9.0 KV-only shadow;
3. exact-token KV shadow;
4. exact-token cache-aware KV shadow;
5. full predictive KV/cache/TPS shadow.

Required integrated scenarios:

1. same-poll short burst;
2. mixed short and 64k/128k prompts;
3. cache-cold long prefill;
4. active shared-prefix hit;
5. probable cache hit followed by eviction;
6. high cache hit plus long decode;
7. cache hit collapse before the next metrics poll;
8. upstream work completes before the next poll;
9. predicted safe time earlier than the next poll;
10. stale waiting sample with known PIG completions;
11. non-exclusive ingress uncertainty;
12. vLLM block/profile reset;
13. SGLang radix pinning;
14. SGLang EAGLE workspace risk;
15. tokenizer/template mismatch;
16. unsupported multimodal request;
17. prediction error disables extra headroom;
18. concurrent predict-and-reserve race stress.

## 17. Acceptance criteria

### 17.1 Product and safety

- off mode is behaviorally and measurably equivalent to v0.9.0 off.
- shadow mode changes no status, headers, body, routing, real queue duration,
  or current QoS outcome.
- enforce configuration fails startup.
- no predictive fit violates any configured upper/lower constraint.
- low-confidence cache state never creates a false certain hit.
- a stale sample cannot erase newer virtual events.
- all reservation lifecycle operations are race-safe and idempotent.
- cache/profile/backend resets invalidate incompatible state.

### 17.2 Prediction coverage

- tokenizer/template golden outputs match the selected backend profile exactly
  for all supported request classes.
- cache-hit lower bounds meet the configured empirical coverage target.
- KV peak upper bounds meet the configured empirical coverage target.
- existing-user and all-user TPS lower bounds meet the configured empirical
  coverage target.
- TTFT/TPOT upper bounds meet the configured empirical coverage target.
- error-bound breach disables predictive extra headroom.

Coverage targets are selected from builder/simulator evidence before any
enforcement plan. v0.9.1 does not invent an unmeasured probability guarantee.

Before GPU-serving evidence exists, the executable gates are:

- deterministic scenarios: 100% of fit decisions satisfy every modeled hard
  constraint and all declared ground-truth upper/lower intervals;
- race/concurrency scenarios: zero duplicate reservation, underflow, leak, or
  false fit;
- tokenizer golden fixtures: exact token-ID equality, not approximate token
  count equality;
- randomized/fuzz domain tests: invariants hold for every generated case;
- empirical real-backend coverage: explicitly pending and never inferred from
  CPU-only or simulator results.

When real shadow data is later authorized, each interval target must define
sample size, workload strata, confidence method, and acceptable miss rate.

### 17.3 Efficacy

Against the same deterministic or replayed workload:

- predictive shadow records zero additional hard safety violations;
- it predicts earlier safe reopening than poll-only control when PIG-observed
  work completes;
- it admits more independently safe short/cache-hit work than KV-only control;
- the primary gain is completion TPS, not only prompt or total TPS;
- predicted single-user TPS protection is no worse than the current baseline;
- cache-miss and unsupported traffic is not starved by cache-hit traffic.

### 17.4 Performance gates on the remote builder

Initial engineering gates, to be validated and revised from measurements:

| Operation | Gate |
|---|---:|
| Existing off-mode path | zero tokenizer/predictor calls and p95 within max(2%, 5 us) of matched baseline |
| Small supported chat exact tokenize/template p95 | at most 1 ms |
| 64 KiB exact tokenize/template p95 | at most 5 ms |
| 2 MiB exact tokenize/template p99 | at most 150 ms |
| Cache mirror lookup p99 | at most 100 us |
| Scheduler prediction p99 | at most 500 us |
| Atomic predict-and-reserve excluding tokenizer p99 | at most 1 ms |

These are acceptance gates, not claims about results already measured.

Performance comparisons use the same builder host/container, exact commit,
warmup count, sample count, CPU-affinity policy when available, and input
fixtures. Raw durations and quantile code are retained. A one-off wall-clock
run is not sufficient evidence for an off-mode regression claim.

## 18. Remote-builder-only validation

Builder:

~~~
CVM: 4f167f6e-4c50-415f-99f2-94b65652beba
preferred container: pig-ubuntu-builder
~~~

Validation advances in small gates:

~~~
gofmt and git diff --check
focused tokenizer/manifest tests
focused cache mirror tests
focused virtual-state/reservation tests
focused scheduler/predictor tests
deterministic integrated simulations
go test ./...
go test -race ./...
native tokenizer parity tests
performance gates
Docker build and off/shadow/enforce-startup smoke
~~~

No Go, Rust, Python tokenizer, vLLM, SGLang, Docker build, or simulator test is
run on the local Windows checkout.

Builder results record:

- exact commit;
- clean checkout path;
- toolchain versions;
- command;
- exit code;
- focused and full test counts;
- race result;
- tokenizer/profile fixtures and immutable hashes;
- latency quantiles;
- image ID if an image is built.

A builder-local image is not a registry image and neither is a deployment.

The builder tests only an exact pushed commit in a new clean checkout. It does
not test an uncommitted Windows working tree or a mutable shared checkout.
Every result begins with:

~~~
git rev-parse HEAD
git status --porcelain
go version
rustc/cargo version when applicable
container/image identity
~~~

Tokenizer oracle assets are pinned by repository/revision and recorded file
hashes. Authentication presence may be checked, but credentials and environment
values are never printed.

## 19. First executable test slice

The first implementation slice deliberately stops before a native tokenizer.
Planned packages are:

~~~
internal/domain/predictive
internal/runtime/predictive
internal/simulation/predictive
~~~

The red/green order is:

1. add table-driven tests that reference the planned domain/runtime contract;
2. push the test-only commit and run the focused builder command, recording the
   expected compile/test failure;
3. define tokenizer manifest, request token result, cache-hit interval,
   scheduler input/output, predictive decision, and reservation domain types;
4. add a deterministic fake tokenizer;
5. add the minimum runtime implementation needed to make the focused tests
   pass without adding native tokenizer claims;
6. run table-driven tests for manifest mismatch, cache certainty, exact-token
   KV projection, existing-TPS protection, completion-before-next-poll release,
   and atomic concurrent reservation;
7. extend the simulator with at least one stale-feedback-idle scenario and one
   cache-hit-prefill scenario;
8. run focused, full Go, and race gates on the remote builder;
9. use the resulting contract to add the Rust tokenizer parity prototype.

This slice validates the predictive architecture without pretending that a
fake tokenizer proves production token parity.

The initial focused builder commands are:

~~~
go test ./internal/domain/predictive ./internal/runtime/predictive
go test -race ./internal/runtime/predictive
go test ./internal/simulation/predictive
~~~

Package names and commands may be revised only in the plan before the test-only
commit is created.

## 20. Version, Git, and release boundary

- v0.9.0 remains immutable and is not retagged.
- Work continues on codex/pig-v0.9.1-predictive-shadow.
- Plan, tests, implementation, native tokenizer integration, and release
  evidence are separate reviewable commits.
- Source and version may be pushed because the user explicitly authorized
  code/version pushes for this PIG work.
- No v0.9.1 tag is created until the full documented release gate passes.
- No image is published until its exact commit passes the builder gate.
- No production Compose or CVM is modified without a new explicit deployment
  authorization.

## 21. Enforcement gate for a later version

Predictive enforcement is considered only after:

- tokenizer/template/block-key parity is demonstrated for every supported
  request class;
- representative cache-hit and cache-eviction prediction errors are measured;
- scheduler/TPS/TTFT/TPOT intervals meet documented coverage;
- shadow replay shows improved completion TPS without additional safety or SLO
  violations;
- exclusive-ingress assumptions are verified or uncertainty is safely handled;
- backend-version/profile drift is fail-closed for predictive extra headroom;
- a canary, instant-off, rollback, and bounded-blast-radius plan exists;
- the user explicitly authorizes deployment and enforcement.

## 22. Evidence boundary

This plan distinguishes:

~~~
documented design
implemented Go contracts
native tokenizer parity
deterministic simulation
remote builder validation
builder-local image
published source/tag/image
production shadow deployment
production enforcement
~~~

Completion of an earlier item never proves a later item.

The six production CVMs provide historical capacity and risk evidence only.
They are not test or deployment targets for v0.9.1 under this plan.

CPU tokenizer parity, Go domain tests, deterministic simulation, and builder
performance tests do not prove real GPU scheduler/TPS accuracy. That evidence
remains pending until a separately authorized isolated GPU shadow test exists.

## 23. Review record

Three independent reviews are required before implementation begins:

1. architecture and forward-control correctness;
2. tokenizer/cache/backend semantics and safety;
3. test executability, quantitative acceptance, and release/deployment
   boundary.

Each review records identified issues and the document changes made. A review
with no issue records the checks performed rather than silently passing.

### Review 1: architecture and forward-control correctness

Issue found:

- The initial virtual-state formula treated metrics and completion events as
  scalar additions/subtractions. A scrape can overlap PIG events, so this
  could double-add or double-subtract work and incorrectly predict an idle
  upstream.

Changes made:

- Replaced scalar virtual state with lower/upper intervals.
- Added poll start/finish watermarks and PIG event sequence boundaries.
- Added explicit assimilation state for reservations.
- Added known-work ownership coverage and unknown/bypass work intervals.
- Restricted observed-baseline decrements to cases where watermark and
  ownership evidence prove they are safe.
- Made scrape-window ambiguity widen bounds rather than create fit.

### Review 2: tokenizer, cache, and backend semantics

Issues found:

- Identical tokenizer files do not prove identical chat-template execution or
  final token IDs.
- Reproducing an opaque backend process hash was described too strongly;
  randomized hashes and unmodelled extra keys can differ even for the same
  token prefix.
- Treating unknown cache state as a decision failure would recreate
  unnecessary under-utilization; unknown can normally be a conservative miss.
- Block rounding, partial-block copy-on-write, cold PIG restart, and explicit
  endpoint capability boundaries needed stronger definitions.
- The lowest-latency in-process FFI candidate lacked an explicit
  crash-containment comparison.

Changes made:

- Defined backend-oracle final token-ID and block-boundary parity as the
  tokenizer gate.
- Added endpoint/request-class capability profiles and immutable golden
  fixtures.
- Changed the mirror to verified token-block semantic identity with an
  internal keyed digest.
- Made unknown/pre-existing cache a miss unless a validated lower bound exists.
- Added cold-start, block-rounding, copy-on-write, pinning, and partial-block
  accounting.
- Added an in-process versus Unix-socket Rust runtime benchmark and
  fault-isolation gate.

### Review 3: test executability, acceptance, and release boundary

Issues found:

- Off-mode regression and prediction coverage were not defined tightly enough
  to produce repeatable pass/fail evidence.
- The first test slice lacked concrete package paths and an auditable
  tests-first red-to-green sequence.
- Builder validation did not explicitly require a clean checkout of an exact
  pushed commit.
- Tokenizer oracle assets needed immutable revision/hash evidence.
- CPU/simulator success could be misread as proof of real GPU TPS accuracy.

Changes made:

- Added deterministic 100% hard-invariant gates and separated future empirical
  backend coverage.
- Added a matched off-mode benchmark protocol and quantitative initial gate.
- Fixed initial package paths, focused commands, and test-only red followed by
  minimum-implementation green order.
- Required clean exact-commit builder checkouts and toolchain/image identity.
- Required pinned tokenizer oracle assets with recorded hashes.
- Explicitly marked real GPU scheduler/TPS accuracy as pending separate
  authorization.
