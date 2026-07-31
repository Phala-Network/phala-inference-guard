# PIG v0.9.1 Predictive Admission Shadow Plan

> Superseded on 2026-07-31 by
> `TOKENIZER_FIRST_PREDICTIVE_ADMISSION_V0_9_1_PLAN.md`. Do not use this
> cache-aware plan as the execution contract unless the user explicitly
> restores cache prediction.

Status: the real adapter transaction, strict Gemma4 text renderer, native C ABI
analysis, active-prefix cache accounting, TPS guard, and five-case pinned
tokenizer parity are builder-green; immutable production bundle construction,
independent predictive body capture, real reconciliation/outcome feedback,
native latency/cancellation qualification, and efficacy simulation remain P0
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

### 3.1 Current implementation reachability audit at `92d1daf`

The 2026-07-31 source audit found that the new predictive components are not
reachable from the PIG HTTP request path:

| Component | Implemented evidence | Actual request-path reachability |
|---|---|---|
| `internal/runtime/predictive.TokenizerRuntime` | Unit-tested manifest, request-class policy, concurrency, reset, and fake-engine encode contract. | No application/server/config call site constructs or calls it. |
| `internal/runtime/predictive.CacheMirror` | Unit-tested active/pending/probable block state and opaque native analysis ingestion. | No application/server call site constructs it or applies its hit interval to a request. |
| `internal/runtime/predictive.Manager` | Unit-tested minimal KV check-and-reserve and sample watermark reconciliation. | Called only by predictive unit tests and the predictive simulator. |
| `runtime/predictive.Scheduler` | Interface exists. | All implementations are test-only hand-written fakes; there is no learned/calibrated runtime scheduler. |
| `internal/simulation/predictive` | Minimal completion-before-poll and cache-hit examples. | It consumes already-constructed `RequestCost`; it does not run rendering, tokenizer, cache analysis, learning, or the HTTP transaction. |
| Production HTTP shadow path | `server.ServeHTTP -> classifyRequest -> shadowKVRequest -> runtime/kvshadow.Manager`. | Uses the v0.9.0 byte interval and sampled backend state, not the new predictive packages. |
| Existing capacity/TTFT learning | `capacity.CleanLearnCap` and `latency.LearnCap` adjust a global dynamic limit from observed TPS, waiting, KV, preemption, and TTFT. | Feedback-only global-cap learning; it does not predict the post-admit effect of the current request. |

This meant the branch at `92d1daf` contained useful contracts and prototypes,
but did not implement predictive admission. Passing those isolated tests did
not prove that learning, tokenizer output, or cache state changed any real or
shadow HTTP decision.

### 3.1.1 Current HTTP boundary at `0974efe`

The current server has a deliberately injected reachability seam:

~~~
ServeHTTP
  -> bounded request classification and isolated predictive body snapshot
  -> predictiveAdmissionShadow.DecideAndReserve
  -> existing authoritative QoS gate
  -> semantic first-output and typed terminal callbacks
~~~

`off` creates no predictive adapter or body snapshot, and the tested shadow
path preserves upstream body and header behavior plus client-visible status,
headers, and body. However, production construction deliberately supplies no
adapter and fails startup if shadow is requested. The server does not yet
construct the strict renderer, native tokenizer, cache mirror, learned
scheduler, or `Coordinator`. Therefore this is application reachability, not
yet an end-to-end predictive controller.

### 3.2 P0 prediction-authenticity gate

Before further tokenizer micro-optimization, the branch must prove one coherent
vertical slice:

~~~
request features and virtual state
  -> versioned scheduler features
  -> static backend prior plus online calibrated residual bounds
  -> post-admit TPS/TTFT/TPOT/KV forecast
  -> atomic shadow decision and reservation
~~~

The gate fails if observations merely update metrics, EWMA values, a later
global limit, or an unused model. With current backend metrics held constant,
tests must show that changing only valid learned state changes at least one of:

- the lower-bound existing-user or all-user TPS forecast;
- TTFT/TPOT upper bounds;
- a reservation's resource/phase horizon;
- the resulting fit/risk decision.

Cold, insufficient, stale, incompatible, or invalid learned state must reduce
confidence and remove predictive extra headroom; it must never create a more
permissive forecast than the validated static prior.

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
process-local keyed rendered-input fingerprint
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
template runtime and compatibility version
declared BOS/EOS/UNK/PAD values and token IDs
immutable request-class-specific add_special_tokens policies
endpoint and tools/schema/reasoning/multimodal capabilities
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

### 6.3.1 vLLM Router tokenizer source evaluation

The vLLM Router tokenizer is a useful source reference, but the implementation
at the evaluated revision is not the PIG production tokenizer dependency or
parity oracle.

The builder evaluation is pinned to:

~~~
repository: https://github.com/vllm-project/router.git
revision: d60711dc72ab8f073e33f9a3d93ee81b97274c26
package: vllm_router_rs 0.1.15
license: Apache-2.0
~~~

Source and builder findings:

| Area | Evaluated behavior | PIG decision |
|---|---|---|
| Encoder core | Uses `tokenizers 0.22.2`, the same core already pinned by the PIG native prototype. | Keep the smaller PIG crate; importing Router does not provide a different tokenizer algorithm. |
| Package boundary | `src/tokenizer` is a module of the monolithic Router crate. There is no tokenizer-only crate or feature gate. An external release consumer locked 489 packages, versus 81 in the PIG native tokenizer lock. | Do not directly depend on the whole Router crate. |
| Runtime use | No Router request-path call site invokes the tokenizer or `apply_chat_template`. | Do not treat Router deployment or cache-aware behavior as production validation of this tokenizer module. |
| Cache-aware policy | The policy deliberately stores raw text characters rather than token IDs. For Chat Completions, the evaluated request extractor returns `session_id` or an empty string. | Do not reuse this as PIG KV admission state. PIG has one upstream and needs exact token blocks, physical allocation, scheduler, and TPS fit rather than backend selection. |
| Asset loading | The HF helper downloads tokenizer-related files from the repository's current revision; the API does not require an immutable revision or expected hashes. Runtime factory calls can download. | PIG assets remain startup-local, revision-pinned, and hash-verified; no request-time download. |
| Special tokens | The wrapper infers token roles by string-pattern search in the vocabulary instead of parsing the effective tokenizer configuration. | Reject heuristic role inference. Load declared values and IDs from the immutable profile and prove them against the backend oracle. |
| Template input | `ChatMessage` contains only `role: String` and `content: String`. The MiniJinja context contains messages, `add_generation_prompt`, BOS, and EOS only. | Use lossless normalized JSON for messages, tools, tool results, reasoning fields, multimodal parts, and profile-approved template kwargs. |
| Template failure | Missing templates silently fall back to `role: content`. The evaluated Gemma4 template fails on `message.get(...)`. | Exact profiles fail closed to `tokenizer_profile_unknown`; never silently substitute a generic template. |
| Encode policy | `encode` and `encode_batch` hard-code `add_special_tokens=false`. | Make special-token behavior an immutable, request-class-specific, golden-tested profile decision. |
| Concurrency and cancellation | Immutable tokenizer values are shareable and synchronous encoding returns `Result`, but there is no profile pool, in-flight cancellation, deadline, or panic/crash containment boundary. | Retain PIG's bounded profile concurrency and keep the in-process versus Unix-socket isolation gate. |
| Tests | The 41 selected tokenizer unit tests pass, but chat-template tests are simplified string assertions and the real tokenizer integration uses TinyLlama. There is no Transformers/vLLM final-token parity suite. | Treat the tests as module tests only, not backend parity evidence. |

The fixed Gemma4 asset probe made the incompatibility concrete:

~~~
model/tokenizer revision:
  RedHatAI/gemma-4-31B-it-FP8-dynamic@5f206f2ff1a06ee5cc9d368127da5c3e80853153
tokenizer.json SHA-256:
  cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f
candidate chat template SHA-256:
  6a1015c47ccfcfa67c3b772385bccee357a4d37c3cda37bd202e9047f391ab82
~~~

The tokenizer configuration declares `<bos>` ID 2 and `<eos>` ID 1. Router's
heuristic instead reports `<s>` ID 203 and `</s>` ID 212. Its MiniJinja
renderer then fails on the candidate Gemma4 template with:

~~~
unknown method: map has no method named get
~~~

Router and PIG do return identical raw IDs for a plain completion fixture when
no template or special-token processing is involved. This proves only
`tokenizer.json`-level raw parity, which is expected because both use the same
Rust library. It does not prove Chat Completions, tools, reasoning,
multimodal, BOS/EOS, or backend cache-block parity.

The direct comparison used the same builder container, tokenizer asset, input
bytes, token counts, warmup, and iteration counts. It excludes Go/FFI and, for
Router, excludes template rendering because the template failed. One matched
rerun measured:

| Core path | Load | Small p50/p95/p99 | 64 KiB, 45,056 tokens p50/p95/p99 | 2 MiB, 1,441,792 tokens p50/p95/p99 |
|---|---:|---:|---:|---:|
| PIG current `Vec<u32>` result | 2.36-2.57 s | 29.3/49.2/59.8 us | 16.1/19.0/28.0 ms | 1.305/1.390/1.452 s |
| Router retained `Encoding` | 2.89 s | 26.2/44.8/55.1 us | 15.5/20.4/47.9 ms | 1.159/1.246/1.264 s |

The matched evidence hashes are:

~~~
Router probe JSONL:
  70d9118a0c2c31bd03bb52dffe17db8c961cf11a0cf612a43b47ab592d10690b
PIG comparison JSON:
  dc99d6bbea7487bcda3e6ee0b2d48494c742b6a980780445254e250c956d872f
plain-completion raw-ID output:
  948b3305619354df6a964b5f90f4e655ba569324970933c819f2917c0a24fdfb
~~~

The overload run reached about 481-484 MiB maximum resident set size on both
paths. This is dominated by the 1.44-million-token encoding and reinforces
that it must not share the normal synchronous prediction lane.

The small differences are not evidence of a faster tokenizer implementation.
The Router wrapper retains the library `Encoding`, while the current PIG
prototype immediately copies all IDs into a new `Vec<u32>`. The useful
optimization to carry forward is therefore:

1. keep the encoding borrowed inside Rust for as long as possible;
2. derive token count and full-block keyed digests in one Rust pass;
3. return full token IDs only for profiles/callers that genuinely require
   them;
4. avoid copying an oversized token vector across FFI merely to hash it again
   in Go;
5. retain a small exact-result LRU keyed by tokenizer manifest plus rendered
   input fingerprint for byte-identical repeated inputs, storing token count
   and block digests rather than prompt text or an unnecessary full-ID copy;
6. never use raw-character prefix similarity as a confirmed KV-cache hit.

The implementation decision is therefore **reference selected, dependency
rejected**. PIG keeps its minimal native core and adds a separately tested
vLLM-compatible template/profile layer. Extracting or copying Router's current
template and special-token code would preserve the very incompatibilities the
profile contract is intended to prevent.

### 6.3.2 Strict profile and native block-analysis implementation

The next tests-first slice was implemented on the remote-builder-only branch.
It does not yet claim chat-template parity or request-path integration.

The strict Go manifest now binds and validates:

- template runtime identity and compatibility version;
- declared BOS/EOS/UNK/PAD text and unsigned 32-bit IDs;
- immutable request-class-specific add/omit special-token policies, which a
  request cannot override;
- completions and chat-completions endpoint capabilities;
- tools, tool choice, response format, JSON schema, reasoning, and multimodal
  feature capabilities with dependency checks;
- exact manifest equality before and after tokenizer warm/reset.

Unsupported request features are rejected by the predictive profile before the
tokenizer engine is called. This is a shadow-predictor failure only; it is not a
new real-traffic rejection path.

The native Rust core now offers a borrowed-Encoding analysis path that returns:

~~~
process-local keyed input fingerprint
exact token count
keyed chained full-block digests
partial-block token count and digest
optional token IDs, disabled on the normal analysis path
~~~

The second implementation review found unkeyed `input_sha256` and
`RenderedInputSHA256` prototype fields. Red commit `f8f25a5` made a keyed
fingerprint mandatory; green commit `f7789a6` replaced them. Each Go tokenizer
runtime owns a random 32-byte key and produces a domain-separated HMAC-SHA256
fingerprint that remains stable across its reset. Native analysis uses the
explicit 32-byte process-local analysis key with a separate keyed BLAKE3 domain
and binds the fingerprint to manifest, backend epoch, and block size. Tests
prove stability with one key, inequality from plain SHA-256, and unlinkability
across independently keyed runtimes/contexts. Fingerprints remain prohibited
from logs, persistence, Prometheus labels, or external APIs.

Digest identity is bound to a 32-byte process-local key, tokenizer manifest,
backend epoch, and block size. One keyed BLAKE3 stream covers the entire token
prefix; each full-block digest finalizes a clone of the prefix stream. A token
change in one block therefore changes that block and every later digest without
re-initializing a hasher for every block.

The Go cache mirror accepts these opaque analyses without prompt text or token
IDs. It verifies manifest, backend epoch, block size, exact full/partial shape,
and non-empty digests before allowing any cache lookup or cache discount.
Stale or malformed analyses fail closed for predictive cache credit.

The red/green evidence sequence is:

| Slice | Red commit/evidence | Green implementation |
|---|---|---|
| Strict profile and native analysis | `f1288cc`, Go `1`, Rust `101` as expected | `f7867fd`, formatted at `458f8e1` |
| Opaque cache-analysis input | `0e9df17`, Go `1` as expected | `2fe694f`, formatted at `eedc3d9` |
| Matched benchmark harness | HMAC version at `812671f` exceeded the 2 MiB safety gate | keyed BLAKE3 at `af54622`, streamed at `81d52e6` |
| Reservation-to-tokenizer identity | `b196bf6`, Go `1` as expected because the manager and simulator did not yet bind a manifest | `3f2fb90`, exact clean Builder checkout fully green |
| Rendered-input privacy | `f8f25a5`, Go `1` and Rust `101` as expected for the missing keyed fields | `f7789a6`, exact clean Builder checkout fully green; benchmark description fixed at `4e9d3d7` |

Two matched reruns of the streamed `81d52e6` release binary measured:

| Case | Analysis p50 range | Analysis p95 range | Analysis p99 range | Interpretation |
|---|---:|---:|---:|---|
| small, 49 tokens | 32.4-33.8 us | 54.9-55.3 us | 65.5-66.7 us | Tokenizer/block core only; template and FFI are absent, so the 1 ms end-to-end chat gate remains open. |
| 64 KiB, 45,056 tokens, 704 blocks | 17.30-17.32 ms | 19.31-22.60 ms | 21.68-52.02 ms | Both p95 runs pass 25 ms; p99 retains host scheduling outliers. |
| 2 MiB, 1,441,792 tokens, 22,528 blocks | 1.539-1.575 s | 1.638-1.683 s | 1.655-1.726 s | Overload-only; matched analysis/Vec p99 ratio was 1.002-1.035. |

The HMAC implementation's 2 MiB analysis/Vec p50-p99 ratio was approximately
1.14-1.17. Per-block keyed BLAKE3 reduced it, and the single streaming keyed
BLAKE3 design reduced the final 2 MiB ratio to approximately 0.99-1.04. The
64 KiB p95 gate passed in both final reruns. Exact raw evidence remains on the
builder:

~~~
/work/pig-v091-evidence/812671f-analysis-benchmark.json
SHA-256 80792590565139a6fdf381d8bc8c8fa7075872f69f154b3f239abb357b2f94b8

/work/pig-v091-evidence/81d52e6-analysis-benchmark.json
SHA-256 63b8714803eb6e7c43a5969ebc6df57cef523f09db79a57d33fe019a2479c7ac

/work/pig-v091-evidence/81d52e6-analysis-benchmark-rerun2.json
SHA-256 e265467e116ded9fd135f1a30a76497835bd1dcbc6055789f1e58b34e13252d1
~~~

Recomputing the applicable gates directly from those JSON files gives:

| Final run | 64 KiB analysis p95 | 64 KiB gate | 2 MiB analysis p99 | Matched Vec p99 | `max(1.5 s, 1.10 x Vec p99)` | Result |
|---|---:|---:|---:|---:|---:|---|
| `81d52e6-analysis-benchmark.json` | 22.603 ms | 25 ms | 1.726 s | 1.668 s | 1.835 s | pass |
| `81d52e6-analysis-benchmark-rerun2.json` | 19.306 ms | 25 ms | 1.655 s | 1.652 s | 1.817 s | pass |

These passes apply only to the stated core and overload gates. They do not
close the small-chat template/FFI gate or the calibrated synchronous-lane gate.

The `81d52e6` values above are the pre-keyed-fingerprint performance baseline.
After the privacy fix, two matched release-binary reruns at exact benchmark HEAD
`4e9d3d76a8c42c7b144b281a110cfdfdf62e1cd7` measured:

| Case | Keyed analysis p50 range | Keyed analysis p95 range | Keyed analysis p99 range | Interpretation |
|---|---:|---:|---:|---|
| small, 49 tokens | 33.550-34.553 us | 55.164-56.642 us | 66.728-67.356 us | Core only; the template/FFI gate remains open. |
| 64 KiB, 45,056 tokens, 704 blocks | 17.546-17.902 ms | 19.248-24.379 ms | 20.409-28.920 ms | Both p95 runs pass 25 ms. |
| 2 MiB, 1,441,792 tokens, 22,528 blocks | 1.495-1.498 s | 1.540-1.580 s | 1.579-1.630 s | Both matched overload gates pass; analysis/Vec p99 ratio is 0.998-1.019. |

The keyed rerun gate calculations were:

| Run | 64 KiB analysis p95 | 2 MiB analysis p99 | Matched Vec p99 | Allowed ceiling | Result |
|---|---:|---:|---:|---:|---|
| 1 | 19.248 ms | 1.579 s | 1.582 s | 1.740 s | pass |
| 2 | 24.379 ms | 1.630 s | 1.600 s | 1.760 s | pass |

Builder evidence:

~~~
/work/pig-v091-evidence/f8f25a5-keyed-fingerprint-red.log
SHA-256 0718e570015f293ba6b0cd5d21d72543598a55a9f4e545d1c02817d17614a6bb

/work/pig-v091-evidence/f8f25a5-keyed-fingerprint-red.status
SHA-256 4652a73ba397132aeb3c730044eb76393e1a38e5d8aac54064b850adff6ae220

/work/pig-v091-evidence/f7789a6-keyed-fingerprint-green.log
SHA-256 899962d952708b5ef6a57309017750a1290d90aa13fcdf233684d20b44ef2232

/work/pig-v091-evidence/f7789a6-keyed-fingerprint-green.status
SHA-256 4bf65cea25036f3c9f2c2c06ebb8ef398dba1b5ceae0ba43b2d961b188b8370f

/work/pig-v091-evidence/4e9d3d7-keyed-fingerprint-benchmark-run1.json
SHA-256 754ff723fbfbd8e4756f6309562866f92d705a796567075c293b37e554cb4688

/work/pig-v091-evidence/4e9d3d7-keyed-fingerprint-benchmark-run2.json
SHA-256 925d04b530caa8003a83be43a73c27f424093b625a280673552a5a17f68f316c

/work/pig-v091-evidence/4e9d3d7-keyed-fingerprint-benchmark.log
SHA-256 b6275c6a878a27092a68a6a7ac9ad5438c21fc9a0b15e0b5b23c5d4da040b855

/work/pig-v091-evidence/4e9d3d7-keyed-fingerprint-benchmark.status
SHA-256 6b51764e1a9b533825628bf561abd4753d0f35ae2eeead755939b0940912d01c
~~~

The report-level `input_sha256` in these JSON files identifies only fixed,
synthetic benchmark fixtures. It is not returned by runtime block analysis.

The reservation-identity red/green evidence is:

~~~
/work/pig-v091-evidence/b196bf6-manifest-reservation-red.log
SHA-256 8581336748e291bd064d610ef005b7ee5185511a5480a9eb4322ead42b0f83b9

/work/pig-v091-evidence/b196bf6-manifest-reservation-red.status
SHA-256 d0557c125b967784e2f9e06f0023ae528cd8dbb417f1ddc2de507f230e3dd000

/work/pig-v091-evidence/3f2fb90-manifest-reservation-green-v3.log
SHA-256 88bba9783036d9bf53713f0c227ef829caa9ad4994a0c1b84d9b7a51a8080c5b

/work/pig-v091-evidence/3f2fb90-manifest-reservation-green-v3.status
SHA-256 cb022e3c36e41e60bfca031a9efdcf8705b44c6b310a077edb0d1809c55c4359
~~~

The green run checked exact HEAD
`3f2fb905c94bb170f46523b634bc37bbb0bc3488`, `git show --check`, all
tracked Go formatting, focused and full Go tests, focused and full race tests,
Rust formatting, and locked Rust tests. Every recorded status and the final
run exit were zero. The evidence also records the clean Git status, Go 1.24.5,
Rust/Cargo 1.97.0, container ID, immutable image ID, and image name
`ubuntu:24.04`.

These measurements still exclude a real template renderer, C ABI or Unix
socket transfer, and Go request-path integration. They do not prove final
vLLM Chat Completions token parity.

### 6.3.3 Target profile split and production provenance

The six originally named CVMs are not one interchangeable tokenizer/scheduler
population. The read-only 2026-07-31 snapshot divides them into:

| Profile family | CVMs | Backend | Profile rule |
|---|---|---|---|
| Gemma4 31B IT | `bf47b91b-77f9-44ab-a081-284268e205f7`, `6e775a03-c7e2-496b-9c6b-76d17d89ca12`, `a0f0bfb3-e46f-4b22-814e-24872f251193` | vLLM | One Gemma/vLLM profile may be shared only while immutable image/model/template/config/block-size evidence matches. |
| GLM-5.2 | `d4c268f5-b537-4b5e-969f-784432250f7c`, `55f52ee5-813c-4c25-b92a-4d3ca2de39c2`, `6193464a-a31a-4bab-8284-9b64d326a848` | SGLang | Requires an independent SGLang tokenizer/template/cache/scheduler profile; Gemma/vLLM evidence cannot enable it. |

PIG still performs no routing. The split only selects the one configured local
upstream profile and prevents cross-backend learning or cache state.

The three Gemma CVMs used the same read-only production identity:

~~~
image:
  ghcr.io/phala-network/vllm-openai:v0.24.0-cu129-ubuntu2404-phala.6
image digest:
  sha256:66fa87a8eb31b1c9849c907c63a18a6d03c1696a50246ca094c5789b0efd7368
Phala overlay source revision:
  6586e54ee274d75b71bd0b77600a6cc71f57c4bc
official vLLM base source revision:
  ee0da84ab9e04ac7610e28580af62c365e898389
model repository/revision:
  RedHatAI/gemma-4-31B-it-FP8-block@b92691b6de6294798f45df81accf88cbc3e1d901
served model:
  google/gemma-4-31B-it
block size / maximum model length:
  64 / 262144
production CLI template:
  examples/tool_chat_template_gemma4.jinja
production CLI template SHA-256:
  afdbb2abe3667ccde95cc2f86919f05370339399bab5f750950a4390523b8927
tokenizer.json SHA-256:
  cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f
tokenizer_config.json SHA-256:
  e467669cfe172dfb0c4e7de7bfbe7553c42bfa5de95acd71f423f58a434d80de
~~~

The production CLI template is not the model repository's default
`chat_template.jinja` (`6a1015...`). Earlier raw-tokenizer work used the
`FP8-dynamic@5f206f...` candidate and the repository-default template. Its
identical `tokenizer.json` keeps the raw encoder benchmark informative, but it
does not establish production Chat Completions parity.

Read-only vLLM protocol inspection also established endpoint-specific special
token behavior:

- Completions defaults to `add_special_tokens=true`;
- Chat Completions defaults to `add_special_tokens=false` because the chat
  template inserts BOS itself.

Test-only commit `40f579c` exposed the previous invalid single global policy;
green commit `92d1daf` binds immutable policies to request class and also binds
template source, backend source revision, and backend image digest in the
manifest. This improves the contract only. It adds neither a template renderer
nor an HTTP predictive call site.

The SGLang/GLM-5.2 immutable tokenizer, template, radix-cache semantics,
scheduler features, and golden oracles remain pending. Until they exist, those
three profiles return `tokenizer_profile_unknown`/`predictor_profile_unknown`
in predictive shadow and receive no cache or learned extra headroom.

### 6.3.4 Real HTTP adapter contract and native bridge decision

The next P0 is one production-constructible adapter, not another server fake or
an isolated tokenizer benchmark. Its request-time transaction is fixed as:

~~~
bounded private body snapshot
  -> strict endpoint/schema/capability parse
  -> profile-specific canonical renderer
  -> one exact native tokenize-and-block-analysis call
  -> identity and cost-envelope validation
  -> Coordinator.DecideAndReserve
  -> one HTTP-owned reservation handle
  -> semantic/terminal reconciliation and attributed learning
~~~

The adapter contract is deliberately narrower than the OpenAI forwarding
contract. Unsupported predictive syntax never changes or rejects the real
shadow request; it produces a typed unknown result and receives no cache or
learned headroom. The first Gemma/vLLM profile accepts only the following after
golden parity is present:

- `/v1/completions` with one string `prompt`; prompt arrays, pre-tokenized ID
  arrays, `suffix`, and prompt-adapter/cache-salt extensions remain unknown;
- `/v1/chat/completions` with losslessly parsed message objects and only the
  feature classes explicitly enabled by the immutable profile;
- `max_completion_tokens` or legacy `max_tokens` as the decode horizon, with a
  conflict rejected by the predictor and an omitted value replaced by the
  profile's conservative upper bound;
- `add_generation_prompt=true` for Chat Completions and the immutable
  request-class special-token policy from the manifest; request fields cannot
  override either choice.

The JSON parser preserves every template-relevant value and rejects duplicate
keys, invalid UTF-8, trailing values, lossy numeric coercion, an unknown role,
or a content/tool/reasoning/multimodal shape outside the profile before the
renderer or tokenizer runs. It does not normalize or reserialize the body sent
upstream. Renderer output is a private byte sequence whose identity binds the
original template SHA-256, renderer implementation/compatibility version,
tokenizer manifest, backend image/source/model revisions, block size, and
backend epoch. A generic fallback template is forbidden.

Predictive capture has its own explicit byte ceiling, in-flight byte budget,
and concurrency limit; it does not silently inherit the smaller output-field
classifier limit. The implementation must handle a known-length body, a body
with unknown `Content-Length`, classifier saturation, and an early read error
without losing or changing any byte forwarded upstream. It reads at most the
configured ceiling plus one byte, restores the exact consumed prefix and
unread remainder, and records `body_too_large`, `capture_saturated`,
`unknown_length_over_budget`, or `body_read_failed` rather than treating a
partial body as complete JSON. The configured ceiling must cover the profile's
validated prompt envelope or the uncovered range is reported explicitly in
simulation coverage; a convenient small default cannot be presented as exact
long-context support. Peak retained bytes and concurrent copies are builder
measured and bounded.

The selected first bridge is a narrow in-process Rust C ABI. This choice is
limited to v0.9.1 shadow and is based on the existing builder evidence: the
repository already builds with `CGO_ENABLED=1`; the Rust core is already a
`cdylib`; small exact analysis is about 34 us p50 and below 68 us p99 before
rendering/FFI; and a process or HTTP round trip is unnecessary for the normal
lane. The ABI owns immutable tokenizer/profile handles, accepts caller-owned
input only for the duration of a call, returns a bounded owned analysis object,
never retains a Go pointer, returns no token IDs on the hot path, catches Rust
unwinds, validates every length and pointer, and exposes idempotent destroy.
The Go wrapper serializes neither raw prompts nor block digests into logs,
labels, persistence, or learner cells.

This is not a blanket safety waiver for in-process native code. Remote-builder
ABI fuzz/error/panic tests, sanitizer-capable tests, cancellation/timeout
measurement, and final end-to-end p95/p99 gates remain mandatory. If a native
panic, invalid input, bounded-allocation failure, or injected library failure
can terminate/corrupt PIG or exceed the synchronous budget, the selected
bridge changes to one long-lived Unix-socket helper. A subprocess-per-request
or upstream-tokenize shortcut is forbidden.

In particular, a Go context cannot forcibly interrupt an already-entered C ABI
call. The in-process lane therefore admits only rendered byte/token envelopes
whose measured worst-case runtime fits the configured synchronous budget; the
pre-call size/capability check returns unknown outside that envelope. The
builder must inject deadline expiry before the call and while a worst-case call
is active. If the latter cannot preserve the shadow latency budget, long inputs
move to the cancellable Unix-helper lane or remain explicitly unsupported;
checking `ctx.Err()` only after an unbounded native call is not timeout
containment. No timed-out analysis may commit a late reservation.

Immutable work (parse, render, native analysis) occurs outside the coordinator
lock. Immediately before mutation, `Coordinator.DecideAndReserve` rechecks the
manifest/backend/scheduler identities and atomically evaluates the post-admit
KV/cache/TPS/TTFT/TPOT counterfactual. Exactly one renderer call and one native
analysis call are permitted per uncached request attempt. Only a `fit` result
owns cache references and virtual capacity; unknown/risk/reject paths leave the
coordinator snapshot unchanged.

The adapter must use the same live `Coordinator` for request decisions and
feedback. A semantic event records the reservation's TTFT transition; a typed
terminal event releases that exact request once. Learning accepts only
sufficiently attributed outcomes with the same manifest, backend epoch,
predictor version, feature cell, and temporal window. Backend aggregate
samples may reconcile virtual state but cannot masquerade as per-request
TPS/TPOT outcomes. If exact output-token/TPOT attribution is unavailable, those
targets remain invalid for that sample instead of learning from the requested
decode horizon. Raw body, rendered text, token IDs, request IDs, and cache
digests are absent from persistent learner state.

Construction therefore requires two distinct observation contracts. A
monotonic backend-state source bootstraps and reconciles physical/active KV,
running/waiting work, active context, and backend epoch; staleness or an epoch
change quarantines cache credit and calibrated headroom. A bounded attributed
outcome source may update scheduler residuals for completed reservations. The
latter must carry the coordinator request ownership, exact scheduler identity,
observation time, and validity bits for every target. If no such attributed
source is configured, the adapter remains a real exact-token/cache/static-prior
predictor, but online residual learning is explicitly disabled and cannot be
reported as integrated. The initial HTTP vertical-slice test may inject a
deterministic attributed source; completion of v0.9.1 still requires the
production constructor to receive a real source or to fail closed when learned
mode is requested.

Every attempt records a bounded reason, prediction source, sample count,
confidence, exact-token bucket, certain-cache-hit bucket, projected KV bucket,
and forecast bucket without high-cardinality request data. `fit` returns the
only HTTP-owned reservation. Unknown or risk returns no reservation but retains
an inspectable aggregate decision counter, so shadow false-deny/false-fit and
learning-causality simulations can be audited instead of disappearing as a
generic nil.

Adapter close first stops new analysis, then waits only a bounded interval for
owned calls, releases every committed coordinator request with `expired`,
destroys native/profile handles exactly once, and scrubs temporary buffers.
Constructor failure after any acquired native or observation resource rolls
back in reverse order. Close, timeout, cancellation, local QoS reject, client
disconnect, upstream failure, and completion are race-tested against semantic
first output; none may double-learn, leak cache refs, or commit after close.

The prediction-authenticity red/green test holds current backend metrics,
request bytes, profile, and virtual state constant. It changes only eligible
history ingested through the real adapter/coordinator and must observe a
different pre-forward scheduler estimate plus decision or reservation. A
server-injected hand-written `predictiveAdmissionShadow`, a pre-seeded final
decision, or a learner queried only after forwarding does not satisfy this
gate. Cold, sparse, stale, wrong-epoch, wrong-template, invalid-analysis, and
unattributed histories must never create additional headroom.

The executable evidence order is:

1. Add a production-construction HTTP red that uses the existing compilable
   `newProxyServer` entrypoint with a complete immutable test profile. It must
   fail for the current behavioral reason, `predictive shadow adapter is
   required`, not for an undefined symbol, missing toolchain, missing fixture,
   or malformed config.
2. Add adapter-contract reds with injected deterministic renderer, native
   analyzer, coordinator, clock, and observation source. These exercise the
   real adapter type rather than replacing `predictiveAdmissionShadow`. They
   prove one render/analyze call, typed unknowns before tokenization, exact
   analysis identity, cache preflight, pre-QoS reservation, lifecycle release,
   no state mutation on reject, no raw retention, and the HTTP-level learned
   counterfactual.
3. Implement the smallest production factory and strict text-only
   Completion/Chat vertical slice. Unsupported feature classes stay unknown;
   no generic template or fake analyzer is installed to make startup pass.
4. Add the Rust ABI and Go wrapper red/green. The production call returns only
   count, opaque full-block digests, and partial-block metadata. A separate
   builder-only parity call may retain token IDs; its allocation and latency
   are not reported as hot-path performance.
5. Run immutable Gemma/vLLM oracle fixtures for every enabled class and field,
   then matched C ABI versus long-lived Unix-helper latency/isolation tests.
   Record whether the provisional C ABI decision remains selected.
6. Only then run the full counterfactual simulator and efficacy gates. A green
   unit/HTTP bridge cannot substitute for throughput, false-fit, false-deny,
   TPS, latency, preemption-proxy, and leak comparisons.

The profile fixture manifest records source URL/revision, license/provenance,
original template bytes and SHA-256, tokenizer/config hashes, oracle runtime
and source revision, request JSON, rendered bytes hash, final token-ID hash and
count, block size, and special-token policy. Production startup reads only a
pre-provisioned read-only bundle and verifies every file before construction;
the builder fetch helper is never a production download path. No vLLM file,
image, repository, or deployment is modified to create the oracle.

Every red and green record contains exact PIG commit, clean/dirty status,
commands, exit statuses, builder/container/toolchain identity, relevant fixture
hashes, and SHA-256 of logs/status files. The builder sequence builds the Rust
artifact first, wires the explicit C ABI library/include path for Go, then runs
focused Go, focused race, full Go, full race, `cargo fmt --check`, locked Rust
tests, tracked-format checks, and `git show --check`. Windows performs no Go,
Rust, oracle, benchmark, or simulation execution.

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

### 9.5 Online learned calibration that participates in admission

The learned component is not the existing global concurrency-limit learner.
It is a versioned scheduler calibrator invoked synchronously by
`DecideAndReserve` before the request is forwarded.

The initial implementation is an explainable hierarchical online residual
model, not an unconstrained neural model. For feature vector `x` and outcome
`y`:

~~~
base = static_backend_profile.predict(x)
residual = observed_y / max(base, epsilon)

safe TPS lower bound = base_TPS * lower_quantile(residual_TPS)
safe latency upper bound = base_latency * upper_quantile(residual_latency)
~~~

The static profile remains the conservative cold-start prior. Learned
residuals can correct systematic bias and recover measured safe headroom only
after the coverage gate passes. They cannot bypass hard KV/workspace limits or
the profile's absolute min/max clamps.

The versioned feature record contains at least:

~~~
backend/model/image/config and predictor epoch
decode sequence-count bucket
active context-token bucket and distribution summary
prefill sequence-count bucket
new and already scheduled uncached-prefill tokens
request cached/certain/expected token intervals
KV physical and active occupancy buckets
new request context and decode-horizon buckets
chunked-prefill and scheduler settings
speculative-acceptance bucket when applicable
cache/profile confidence and sample age
~~~

Cache-hit features reduce only the modeled prefill/allocation work they can
actually avoid. They never reduce the new request's decode share or
backend-specific workspace risk merely because the prompt is cached.

The hierarchy backs off from an exact feature cell to coarser cells and then to
the static profile. Each cell records bounded recent residuals or an equivalent
mergeable quantile sketch, sample count, effective sample weight, last update,
prediction errors, and profile version. Eligibility requires:

- a configured minimum effective sample size;
- non-stale samples from the same backend/model/config/predictor epoch;
- finite, positive, range-checked observations;
- sufficient ownership/attribution to match an observation to the predicted
  cohort;
- measured one-sided coverage at or above the configured target;
- no active distribution-shift or error-circuit-breaker condition.

Training observations are outcome-specific. Missing TPOT, usage, cache, or KV
evidence does not become a zero. The first sources are:

- PIG semantic first-output timing for TTFT;
- streaming token cadence or verified response usage for TPOT/request decode;
- backend generation-token deltas divided across an attributed active decode
  cohort for aggregate completion capacity;
- backend KV/token samples reconciled through poll watermarks;
- PIG request lifecycle events for phase duration and release timing;
- backend prefix-cache query/hit deltas only for aggregate calibration, never
  as proof that a particular request hit.

An accepted shadow request produces a prediction record before forwarding and
an outcome record after sufficient observations arrive. A request that the
authoritative existing QoS path rejects has no observed upstream outcome and
must not be trained as if the predictive counterfactual were known. Builder
simulation supplies explicit ground truth for both policies.

Staleness, reset, backend restart, tokenizer/profile mismatch, model/config
change, impossible residual, sustained coverage miss, or excessive drift
quarantines the affected cell and falls back through the hierarchy. Learned
state is bounded in memory and cardinality and never stores prompt text, token
IDs, request bodies, or prompt-derived hashes in telemetry/persistence.

The mandatory causality tests use identical current backend samples and request
costs, vary only the learner state, and prove:

1. a calibrated safe cohort can raise the TPS lower bound and change a shadow
   risk decision to fit inside all hard bounds;
2. adverse latency/TPS residuals lower capacity and change fit to the correct
   risk reason;
3. cold, sparse, stale, shifted, or invalid learning never makes the decision
   more permissive than the static prior;
4. an observation with the wrong profile/epoch or insufficient attribution
   cannot change a forecast;
5. a prediction is stored in the reservation and later reconciled with the
   matching outcome exactly once.

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

### 11.1 One transaction across cache, scheduler, and reservation state

The current `runtime/predictive.Manager` mutex proves atomicity only for its
minimal physical/active-KV reservation map. `CacheMirror.BeginRequest` owns a
different mutex, and the current reservation does not retain the complete
scheduler prediction, block references, phase state, outcome linkage, or
learner version. Calling those components sequentially would permit a cache pin
without a KV reservation, or a reservation without the matching cache state.

The integrated implementation therefore introduces one admission coordinator
as the transaction owner. Exact rendering/tokenization and native block
analysis may run before the coordinator lock because they can be expensive, but
they are immutable proposals rather than state mutations. Under one coordinator
critical section the implementation must:

~~~
sweep expired state and apply queued lifecycle events
revalidate tokenizer manifest, backend epoch, scheduler profile, and learner epoch
derive cache-hit interval without mutating shared state
derive KV/prefill/decode/workspace request cost
predict the post-admit state using the current learned calibrator snapshot
evaluate every hard and QoS constraint
if fit:
  commit cache references plus all resource/phase reservations
  store the exact prediction/features/model version needed for reconciliation
else:
  leave cache, virtual state, learner linkage, and reservation maps unchanged
~~~

No external callback, tokenizer call, network request, log write, or metrics
scrape occurs while holding this lock. Metrics counters are updated from the
committed result after unlock.

If independent internal locks remain for read-only snapshots, the coordinator
must define and test one lock order. Prefer coordinator-owned state or
non-locking helpers called only under the coordinator rather than rollback
between multiple separately committed managers.

HTTP shadow lifecycle is explicit:

- reserve before entering the authoritative existing QoS gate so the
  counterfactual sees simultaneous arrivals;
- release with `local_qos_reject` if the existing gate rejects, without
  treating it as an upstream performance outcome;
- mark semantic first output once and move prefill to decode;
- reconcile streaming/non-streaming completion, client cancellation, upstream
  failure, timeout, and panic/early return exactly once;
- expire abandoned reservations conservatively and count the cause;
- reset/quarantine all incompatible state atomically on epoch changes.

Property and race tests snapshot every owned map/counter before an injected
failure at each transaction stage and require either a complete valid commit or
byte-for-byte equivalent logical state afterward. They also require zero leaked
cache references and reservations after mixed completion/cancellation/reset
stress.

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
- Add an application reachability test so a predictive package cannot be
  called complete while only tests/simulators import it.
- Set the development runtime version to PIG-v0.9.1 only when the off/shadow
  configuration and HTTP integration exist; a plan or isolated package does
  not change the runtime version.
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

This is the next P0 executable slice after the `92d1daf` audit. Write and push a
test-only red commit before implementation. Tests cover:

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
- exact feature/profile/epoch identity and bounded bucketization;
- static conservative cold-start predictions;
- online lower-tail TPS and upper-tail TTFT/TPOT residual calibration;
- hierarchical fallback for sparse cells and staleness/shift quarantine;
- wrong-epoch, unattributed, missing, NaN, infinite, negative, and duplicate
  observations changing no learned decision state;
- identical current metrics and request cost producing different decisions
  only after eligible learned residuals change;
- the scheduler model version and forecast being retained for exactly-once
  outcome reconciliation.

The red test must fail because no concrete learned scheduler/calibrator exists,
not because of a broken builder script or missing toolchain. The first green
slice is domain/runtime plus deterministic simulator only; it makes no HTTP,
template, native bridge, or real-GPU accuracy claim.

### Phase 5: integrated transaction coordinator

Tests build one coordinator with deterministic renderer/tokenizer analysis,
cache mirror, learned scheduler, virtual state, and reservation ledger. They
cover:

- immutable proposal work outside the lock followed by manifest/epoch recheck;
- exact binding among tokenizer manifest, backend epoch, scheduler profile, and
  learner version before any prediction or mutation;
- cache-hit interval affecting uncached prefill and KV projection;
- learned scheduler output affecting the same atomic decision;
- all-or-nothing commit of cache refs, KV, prefill/decode horizon, prediction,
  and learner linkage;
- every committed reservation contributing its decode sequence, active context,
  and uncached prefill to the next same-window prediction, then releasing or
  transitioning those phase resources exactly once;
- failure injection after every proposed mutation;
- completion, local QoS reject, cancellation, upstream failure, reset, expiry,
  and duplicate events;
- concurrent same-prefix and near-capacity admissions under `go test -race`;
- no request body, token IDs, request IDs, or block digests in metrics labels or
  persistent learner state.

### Phase 6: HTTP shadow integration, decisions, and replay

First add off/shadow HTTP integration with injected deterministic components.
Off mode must not construct, warm, or call rendering/tokenizer/cache/scheduler
components. Shadow mode must execute the coordinator before the existing QoS
gate while preserving the existing status, headers, body, routing, and real
queue decision. Only after that reachability gate passes is the native bridge
and production-profile template parity connected.

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
- cache references, phase reservations, scheduler predictions, and learner
  linkage commit or roll back as one logical transaction.
- learned state is never updated from an unobserved rejected counterfactual,
  mismatched epoch, duplicate outcome, or insufficiently attributed sample.
- no isolated predictive package is called integrated until an application
  test proves HTTP reachability in shadow and zero construction/work in off.

### 17.2 Prediction coverage

- tokenizer/template golden outputs match the selected backend profile exactly
  for all supported request classes.
- cache-hit lower bounds meet the configured empirical coverage target.
- KV peak upper bounds meet the configured empirical coverage target.
- existing-user and all-user TPS lower bounds meet the configured empirical
  coverage target.
- TTFT/TPOT upper bounds meet the configured empirical coverage target.
- error-bound breach disables predictive extra headroom.
- with current backend metrics and request cost held constant, eligible learned
  residuals demonstrably change the scheduler interval and at least one
  admission/reservation outcome; otherwise the learning implementation fails.
- cold, sparse, stale, shifted, invalid, or wrong-epoch learned state is never
  more permissive than the conservative static profile.

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

The deterministic builder suite computes completion goodput rather than raw
admission count:

~~~
goodput = completion tokens from requests whose modeled single-user TPS,
          TTFT, TPOT, KV, workspace, and preemption constraints all pass
          / simulated wall time
~~~

Before any real-GPU calibration claim, the full predictive policy must meet all
of these simulation gates against both current count/dynamic control and the
v0.9.0 KV-only shadow on identical event traces:

- zero additional modeled hard-budget, SLO, preemption-proxy, underflow,
  duplicate, or reservation/cache-reference leak events;
- zero false fits when the deterministic oracle says a configured hard or QoS
  constraint would be violated;
- at least 5% aggregate completion-goodput improvement on the declared
  cache/burst/mixed workload suite, plus a strict improvement in at least three
  independently named scenarios rather than one oversized trace dominating;
- no more than 1% completion-goodput regression on the declared cache-cold
  workload suite;
- lower false-deny count than KV-only control on safe cache-hit and
  completion-before-poll opportunities;
- no fit caused by cache credit in the high-cache-hit plus long-decode case when
  the post-join TPS bound fails;
- every result reproducible from a seed, scenario hash, predictor/profile
  version, and exact commit.

These are simulator engineering gates, not claims about the six production
GPUs. A failure triggers model/profile work; it is not fixed by weakening the
trace ground truth or silently changing the comparator.

### 17.4 Performance gates on the remote builder

Initial engineering gates, to be validated and revised from measurements:

| Operation | Gate |
|---|---:|
| Existing off-mode path | zero tokenizer/predictor calls and p95 within max(2%, 5 us) of matched baseline |
| Small supported chat exact tokenize/template p95 | at most 1 ms |
| 64 KiB, 45k-token dense core stress p95 | at most 25 ms before template and FFI |
| Synchronous exact-prediction lane | tokenization/template p95 at most min(25 ms, 5% of calibrated no-PIG TTFT) |
| 2 MiB, 1.44M-token dense safety case p99 | at most max(1.5 s, 1.10 times the matched Vec-ID baseline p99) and never eligible for the synchronous exact-prediction lane |
| Cache mirror lookup p99 | at most 100 us |
| Scheduler prediction p99 | at most 500 us |
| Atomic predict-and-reserve excluding tokenizer p99 | at most 1 ms |

These are acceptance gates, not production claims. The original byte-only
5 ms and 150 ms gates were revised after the fixed fixture showed that 64 KiB
encoded to 45,056 tokens and 2 MiB encoded to 1,441,792 tokens. Byte length
without token density is not a meaningful tokenizer cost gate.

The synchronous-lane budget is enforced by profile eligibility derived from
normalized payload bytes, request feature class, and a calibrated conservative
cost envelope before native work starts; a timeout cannot safely cancel an
arbitrary in-process tokenizer call already consuming CPU. An unexpected
runtime budget overrun disables or narrows that profile bucket and never
creates admission headroom. Inputs outside the calibrated lane remain
`predictive_profile_budget_exceeded` in shadow and use the conservative
fallback result. They are not admitted with a cache discount and are not
rejected solely because the predictive tokenizer budget was exceeded.

The 2 MiB case remains as overload, memory, and failure-containment evidence.
It is not presented as representative production text or as a valid model
context: its 1.44 million tokens exceed the intended Gemma4 serving context.
Its allowed p99 ceiling is the larger of 1.5 seconds and 1.10 times the matched
Vec baseline because the tokenizer-only Vec baseline itself measured above
1.5 seconds on a later loaded builder run. Here, 1.5 seconds is a minimum value
for the overload-test ceiling, not a claim that any result below it represents
a normal request. The relative allowance does not apply to the 64 KiB or normal
synchronous lane gates, and it cannot make a 2 MiB input synchronously eligible.

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

## 19. Original first executable test slice

The original first implementation slice deliberately stopped before a native
tokenizer. Its planned packages were:

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

### 19.1 Execution status after the native-analysis slice

Completed and builder-green:

- predictive domain contracts, virtual-state intervals, atomic reservations,
  cache certainty states, deterministic simulations, and their race tests;
- manifest-bound reservation admission: a missing or stale tokenizer manifest
  fails before scheduler work and cannot create or mutate a reservation;
- strict tokenizer manifest fields and immutable declared special-token
  bindings;
- request-class-specific immutable special-token policies at `92d1daf`:
  Completions add and Chat Completions omit backend tokenizer special tokens;
- request-feature capability and dependency rejection before engine work, plus
  rejection of native token IDs outside the unsigned 32-bit contract;
- domain-separated runtime-local Go and context-keyed native rendered-input
  fingerprints, with unkeyed SHA-256 removed from runtime results;
- native raw tokenizer parity prototype and retained-Encoding source study;
- native no-ID block analysis with chained keyed digests and partial metadata;
- cache mirror ingestion of epoch-validated opaque block analyses;
- two matched performance reruns for the final streaming digest design.
- a concrete static scheduler prior plus bounded exact-cell online residual
  calibration at `b9f42b0`; healthy/adverse learned residuals change the
  pre-admission TPS decision while stale, wrong-epoch, invalid, or unattributed
  outcomes cannot create headroom;
- prediction identity/feature/prior/estimate retention in minimal reservations
  and exactly-once outcome learning for active or completed reservations;
- predictive simulator sample events now call `Manager.ReconcileSample` and
  return the final manager snapshot instead of incrementing a counter only.
- an identity-bound `Coordinator` at `519b219` now owns cache preflight,
  cache-aware vLLM block projection, learned scheduler prediction, phase-rich
  virtual state, and manager/cache commit under one outer lock;
- every committed coordinator reservation contributes physical/active KV,
  decode sequences, active context, and uncached prefill to the next
  same-window prediction; semantic first output removes prefill while
  completion releases all owned phase state;
- scheduler identity and returned prediction identity are checked before
  reservation; no-existing-user TPS is explicitly non-applicable while the
  joining user's all-user TPS constraint remains binding;
- cache preflight is non-mutating, so a scheduler reject does not pin, evict,
  or touch cache state; near-capacity concurrent admissions serialize and only
  one commits.
- coordinator-owned outcome and sample APIs at `a93f87a` now close the internal
  learning loop: eligible adverse outcomes observed through the coordinator
  change a later fixed counterfactual from fit to TPS risk, while wrong-identity
  and duplicate outcomes cannot create learned headroom;
- coordinator sample reconciliation now shares the event watermark with
  admission, first output, and completion; an overlapping late sample cannot
  reintroduce completed phase state.
- bounded terminal causes at `7e1f289` release manager/cache ownership exactly
  once for success, local QoS reject, cancellation, disconnect, upstream
  failure, timeout, or expiry; invalid causes preserve the reservation, and
  non-success causes permanently block completed-outcome learning.
- `PREDICTIVE_ADMISSION_MODE=off|shadow` loading and validation, injected server
  construction, off-mode zero predictive construction/body retention, isolated
  body snapshotting, pre-QoS reservation, streaming semantic transition, and
  tested typed terminal release are builder-green through `5759488`;
- upstream behavioral parity is builder-green at `0974efe`: prediction-only
  output-token metadata is separate from authoritative classification fields,
  so shadow does not add `X-PIG-Output-Tokens` or change early-SSE policy.
- HTTP adapter failure containment is builder-green at `3b44e0a`: decide,
  semantic, and terminal panics cannot escape into the client path; ephemeral
  raw-body storage is scrubbed after synchronous analysis; proxy deadlines use
  `timeout`; and fallible server dependencies are built before the adapter;
- the adapter now has an idempotent close contract, and Run closes it after a
  successful construction when the listener exits; the exact close lifecycle
  is builder-green at `b0b31fd`.

Still pending and not claimed:

- a strict lossless JSON chat-template runtime supporting the pinned Gemma4
  template, tools, tool results, reasoning, multimodal placeholders, and
  approved template kwargs;
- final-token parity against a pinned production vLLM oracle for every enabled
  request feature class;
- a Go C ABI or Unix-socket native engine and its cancellation/crash-isolation
  comparison;
- hierarchical feature-cell backoff, effective sample weighting, measured
  one-sided coverage, and distribution-shift/error circuit breakers; the first
  green learner deliberately supports bounded exact cells only;
- production-calibrated scheduler priors and residual targets; current values
  in tests/simulation are deterministic fixtures, not GPU evidence;
- automatic expiry sweeping and atomic epoch reset/quarantine; expiry has a
  typed manual terminal path, but no clock-driven sweep exists yet;
- one request-path digest protocol: the legacy Go token-ID/HMAC helper remains
  an internal test helper and must not be mixed with native BLAKE3 opaque block
  analyses in a shared cache-mirror epoch;
- calibrated synchronous profile eligibility, runtime-overrun
  disable/narrowing, bounded failure metrics, and a no-raw-copy implementation
  audit for the real adapter;
- a production adapter connecting strict rendering, native token/block
  analysis, cache preflight, learned scheduling, coordinator reservation, and
  typed reconciliation to the injected HTTP seam;
- runtime-version promotion: the application still reports PIG-v0.9.0 until
  the real adapter and deterministic efficacy gate pass;
- calibrated scheduler/TPS/TTFT/TPOT profiles from a real upstream;
- Docker smoke, image publication, any CVM deployment, and enforcement.

The active implementation therefore remains an internal builder-tested slice.
It does not change production traffic or current PIG behavior.

### 19.2 Executed slice: learned scheduler causality

The red commit added tests only in these planned paths:

~~~
internal/runtime/predictive/calibrator_test.go
internal/runtime/predictive/scheduler_test.go
internal/simulation/predictive/learning_causality_test.go
~~~

The red tests reference a concrete static-prior scheduler, versioned online
residual calibrator, eligible/stale/wrong-epoch observations, prediction model
identity, and a simulator event that applies a real sample window. The intended
red result is a Go compile failure for missing production types/functions, plus
no unrelated package failure. A failing shell, missing `go`, malformed script,
or pre-existing test failure is invalid red evidence.

The first implementation commit added only the coherent domain/runtime and
simulator support needed to make those tests green. It must not add a fake HTTP
claim, a vLLM dependency, or a production deployment. In particular it must
prove with fixed current metrics and request cost that:

~~~
static/cold prior -> risk decision
eligible healthy residual history -> fit decision inside unchanged hard bounds
eligible adverse residual history -> TPS or latency risk decision
stale/wrong-epoch/invalid history -> exact cold-prior decision
~~~

The focused remote-builder red/green commands were:

~~~
go test ./internal/runtime/predictive ./internal/simulation/predictive
go test -race ./internal/runtime/predictive ./internal/simulation/predictive
~~~

The green exact commit then ran `git show --check`, tracked `gofmt` validation,
`go test ./...`, `go test -race ./...`, `cargo fmt --check`, and
`cargo test --locked` in a new clean builder checkout. Evidence records exact
HEAD, clean status, toolchain/container identity, exit codes, and SHA-256. No
local Windows test is substituted for any gate.

Execution and evidence:

| Stage | Exact commit | Result |
|---|---|---|
| Test-only red | `fa40ad62206d0ab8d5af73780393af66b6f0f970` | Focused Go compilation exited 1 for the missing learned scheduler, features, stored prediction, and real sample fields/functions. |
| First implementation candidate | `c1ca96d6f5ca513bceac00a7b4fbb90f4e6049b6` | All focused/full Go, race, and Rust tests passed, but four Go files failed the tracked gofmt gate; this is a failed candidate, not green. |
| Formatted green | `b9f42b0660a781a1e7ac307bfcbc1f1fa688109d` | `git show --check`, tracked gofmt, focused/full Go, focused/full race, Rust fmt, and locked Rust tests all exited 0 in a new clean checkout. |

Valid red evidence:

~~~
/work/pig-v091-evidence/fa40ad6-learned-scheduler-causality-red-v2.log
SHA-256 ff3340069188ad4ceac01397d1fc875ced30e36c43f0ee24c7940e41e80122b4

/work/pig-v091-evidence/fa40ad6-learned-scheduler-causality-red-v2.status
SHA-256 b8ade3038a7b42ba296c9e015a2a993636099125c697c2fb63d165de3063696d
~~~

The earlier non-`v2` red files are invalid and excluded because the script did
not add `/usr/local/go/bin` and `/root/.cargo/bin` to `PATH`, so `go`, `rustc`,
and `cargo` were not found; PowerShell CRLF also corrupted its final shell exit.

Failed-format candidate evidence:

~~~
/work/pig-v091-evidence/c1ca96d-learned-scheduler-causality-green.log
SHA-256 c7dc40ccc5dcdc5cc8f28479c6ecfd52f70e01bd9c2b5fa38c56313ace901fea

/work/pig-v091-evidence/c1ca96d-learned-scheduler-causality-green.status
SHA-256 8fa1600a326dbfbca738445a176f6a762a13e3074bd27c35c807e77e65458666
~~~

Valid green evidence:

~~~
/work/pig-v091-evidence/b9f42b0-learned-scheduler-causality-green.log
SHA-256 425d00edad10bf8645b67d9b7a721a4d9a598d6cab507565ed4f1b7cfe79b7d9

/work/pig-v091-evidence/b9f42b0-learned-scheduler-causality-green.status
SHA-256 37db6ac0d898840cecffe66bedfdce7bbbb762213205223bd775ab5d55efd112
~~~

This closes only the prediction-authenticity gate from Section 3.2. The
`LearnedScheduler` is called synchronously by the internal predictive manager,
but neither is yet reachable from the HTTP server. The first calibrator has
bounded exact feature cells and one-sided residual quantiles; hierarchical
backoff, empirical coverage/shift control, phase-rich concurrent reservations,
cache transaction integration, and real profile calibration remain open.

### 19.3 Executed slice: atomic predictive transaction

The test-only red commit `9537b23e130d8941ca7d3afd09e5cb53a71f43a2`
added `internal/runtime/predictive/coordinator_test.go`. It required one
coordinator to bind the tokenizer/cache epoch and scheduler identity, derive
cache-aware request cost, make cumulative prospective TPS decisions, and
commit or release cache plus phase-rich manager state atomically. The intended
red was a focused Go compile failure for the missing production coordinator
API, not a shell or toolchain failure.

The green commit `519b2197c640dabcee2e6f92069815a8b24b06e5`
implements that transaction. Its order is deliberately:

~~~
validate immutable proposal and exact identities
  -> non-mutating cache/capacity preflight
  -> derive certain-hit vLLM KV plus prefill/decode/context cost
  -> learned scheduler predicts and manager reserves
  -> only on fit, commit the already-preflighted cache references
~~~

This avoids an apparent rollback that would leave an eviction or LRU touch
behind. A same-window fit immediately contributes its phase state to the next
counterfactual. Semantic first output and ordinary completion are coordinated
across manager and cache ownership.

Pass 1 re-review, model causality and objective alignment:

- Finding: cache certainty now changes uncached prefill and physical KV before
  scheduling, committed decode/context/prefill state changes the next request's
  all-user and existing-user TPS forecast, and a third same-window request can
  change from fit to `new_tps_at_risk`. This is prospective admission state,
  not a later learned global-cap adjustment.
- Finding: when there are zero existing decode users, the existing-user TPS
  constraint is explicitly non-applicable; the candidate's all-user TPS still
  binds. This removes the prior false `existing_tps_at_risk` classification
  without creating headroom for a below-target new user.
- Remaining issue: eligible learned residuals are proven causal through
  `Manager.DecideAndReserve`, and the coordinator calls that manager with a
  `LearnedScheduler`, but no single coordinator test trains/reconciles the
  model and demonstrates the changed counterfactual. More importantly, the
  coordinator exposes neither attributed-outcome nor sample reconciliation,
  so its encapsulated learner cannot yet receive real feedback.
- Change: coordinator feedback APIs plus an integrated learned-causality test
  are now the next P0 before HTTP wiring; Phase 5 is not marked complete.

Pass 2 re-review, transaction and lifecycle safety:

- Finding: scheduler rejection occurs after a read-only cache preflight and
  before cache commit, so the rejected transaction leaves manager and cache
  snapshots unchanged. Cache capacity failure also occurs before eviction or
  LRU mutation. The post-fit cache commit is protected by the coordinator's
  exclusive ownership; its latest-reservation rollback is an invariant-only
  fallback, not the normal rejection path.
- Finding: scheduler identity, returned-prediction identity, tokenizer
  manifest, backend epoch, block size, token-analysis shape, confidence, and
  non-negative cost bounds fail before mutation. Saturating additions and
  floor-zero releases avoid integer overflow and underflow. Concurrent
  near-capacity admission, prefill transition, and ordinary completion are
  serialized by one coordinator lock and pass race tests.
- Remaining issue: only first-output and ordinary completion are implemented
  at this boundary. Cancellation, local QoS reject, upstream failure, timeout,
  expiry, epoch reset, sample assimilation, and cache reset still lack one
  coordinator lifecycle contract. A manager/cache presence mismatch currently
  returns `false`, which prevents a false success but does not yet emit the
  typed invariant/quarantine signal required for HTTP operation.
- Change: the next lifecycle slice must add coordinator-owned sample/outcome
  reconciliation first, then typed idempotent terminal causes and reset/error
  handling with failure-injection and race tests. HTTP shadow cannot be wired
  before these ownership exits exist.

Pass 3 re-review, evidence validity and next executable gate:

- Finding: the `9537b23` red is valid: an exact clean checkout exits 1 while
  compiling the focused predictive packages because the planned production
  coordinator types and constructor are absent. The `519b219` green is also
  valid: an exact clean checkout passes `git show --check`, tracked gofmt,
  focused Go, focused race, full Go, full race, Rust fmt, and locked Rust tests.
- Evidence: the builder was Ubuntu 24.04.4 in container `6aff8e9be30d`, with Go
  1.24.5, Rust 1.97.0, and Cargo 1.97.0. No Windows Go/Rust test, Docker image,
  registry publication, CVM deployment, or inference request is part of this
  result.
- Remaining issue: the deterministic tests prove transaction mechanics, not
  hierarchical calibration, empirical forecast coverage, scheduler latency,
  end-to-end tokenizer/template parity, HTTP reachability, simulation goodput,
  or real GPU accuracy. The runtime version therefore remains PIG-v0.9.0.
- Change: the next test-only red extends
  `internal/runtime/predictive/coordinator_test.go` and must fail for missing
  coordinator `ObserveOutcome`, `ReconcileSample`, and event-watermark APIs. It
  will prove that three eligible adverse outcomes observed through the
  coordinator change a later fixed counterfactual from fit to TPS risk, that
  wrong-identity/duplicate outcomes change no learned state, and that a sample
  plus completion cannot double-add, double-subtract, or leak any phase state.

Valid transaction red evidence:

~~~
/work/pig-v091-evidence/9537b23-atomic-predictive-transaction-red.log
SHA-256 48059263677c4e3e9d6b5155dad826f6e77b90d3ab522dca6ae41aee22e06cda

/work/pig-v091-evidence/9537b23-atomic-predictive-transaction-red.status
SHA-256 77cd77913d06e253e8fb03c60f38b71bb90e1a9932fc2a3abc39458bde3f4245
~~~

Valid transaction green evidence:

~~~
/work/pig-v091-evidence/519b219-atomic-predictive-transaction-green.log
SHA-256 1e93ff2c916e22c7460b685fa986d911ef4d29abe23d1807582524dbd51e62c8

/work/pig-v091-evidence/519b219-atomic-predictive-transaction-green.status
SHA-256 b3006b12135432db08c314332a518d6d1eb81c386d6c5d726d92bf8a419752b8
~~~

### 19.4 Executed slice: coordinator feedback causality

The test-only red commit `c41d76e4c9ec656a4334c3f700c831369351fdc7`
required coordinator-owned outcome observation, event watermarks, and sample
reconciliation. Its exact clean checkout passed commit and gofmt checks, then
focused Go compilation exited 1 only because `Coordinator.ObserveOutcome`,
`Coordinator.EventSequence`, and `Coordinator.ReconcileSample` did not exist.

The green commit `a93f87a42d1a2062bb1bf12365725be75c1fd44e`
adds those ownership-preserving APIs under the coordinator lock. The integrated
test repeatedly returns to the same initial virtual state and cold-cache
feature cell, observes three eligible adverse outcomes through the coordinator,
then proves a later otherwise-fixed counterfactual changes from fit to
`new_tps_at_risk`. It also rejects wrong-identity and duplicate outcomes, and
proves that first-output, absorbed sample, completion, and a late overlapping
sample cannot leak or reintroduce phase/cache state.

Pass 1 re-review, learned prediction causality:

- Finding: online residual learning now has a complete internal causal path:
  admitted prediction identity and features are retained, eligible attributed
  outcomes update the matching cell exactly once, and the calibrated lower-tail
  TPS estimate changes a later coordinator decision before any forwarding.
- Remaining issue: the calibrator still has exact-cell eligibility only. It
  lacks hierarchical backoff, effective weights, empirical one-sided coverage,
  and distribution-shift quarantine. The current priors/outcomes remain
  deterministic fixtures rather than GPU-calibrated evidence.
- Change: coordinator feedback is moved to builder-green, but Phase 4 remains
  partial and the plan retains every calibration/coverage gate.

Pass 2 re-review, state and lifecycle safety:

- Finding: outcome and sample operations are serialized with admissions and
  lifecycle events, revalidate scheduler identity, and reuse the manager's
  exactly-once and watermark rules. The late-overlap test demonstrates no
  double-add/subtract after an absorbed phase transition and completion.
- Remaining issue: HTTP terminal causes still need explicit coordinator
  ownership. Local QoS reject, cancellation, disconnect, upstream failure,
  timeout, expiry, and epoch reset cannot be collapsed into an untyped ordinary
  completion because only attributable upstream outcomes may train the model.
- Change: typed terminal release, reset/quarantine, and their idempotent/race
  tests remain the immediate prerequisite to HTTP shadow lifecycle wiring.

Pass 3 re-review, evidence and release boundary:

- Finding: `a93f87a` passed `git show --check`, tracked gofmt, focused Go,
  focused race, full Go, full race, Rust fmt, and locked Rust tests in a fresh
  exact-commit builder checkout. Toolchain and container identity match the
  recorded transaction gate.
- Remaining issue: this evidence contains no HTTP request-path import, off-mode
  zero-work proof, client-visible compatibility test, tokenizer/template bridge,
  deterministic goodput comparison, latency measurement, image, or GPU run.
- Change: runtime version stays PIG-v0.9.0 and deployment stays prohibited. The
  next red must first define typed terminal ownership, then the off/shadow HTTP
  reachability seam; a package-level coordinator green is still not a PIG
  request-path green.

The next test-only terminal red uses one bounded `TerminalCause` contract and
`Coordinator.Terminate(requestID, cause)`. The initial closed set is successful
completion, local QoS reject, client cancellation, client disconnect, upstream
failure, timeout, and expiry. Unknown strings fail without release. `Complete`
remains a compatibility wrapper for successful completion. Every valid cause
releases manager/cache state exactly once under the coordinator lock, but a
local QoS reject and other non-success terminal causes make the completed
reservation permanently ineligible for later upstream outcome learning. Race
tests require simultaneous success/cancellation to produce one release and no
state leak.

Valid coordinator-feedback red evidence:

~~~
/work/pig-v091-evidence/c41d76e-coordinator-feedback-causality-red.log
SHA-256 0a6ddf5057a18bc2f3461488b143c10474c762835037b3e4b8993e00ef89d228

/work/pig-v091-evidence/c41d76e-coordinator-feedback-causality-red.status
SHA-256 507d9c32cbadcd35ff64732756aafe30f8b43e8f6227db0cf0ee9f07186973e6
~~~

Valid coordinator-feedback green evidence:

~~~
/work/pig-v091-evidence/a93f87a-coordinator-feedback-causality-green.log
SHA-256 d5c594fd07a7a73b0b43f2ef4f4bc55ff726bda9e2d236cf366f3d252b669ab8

/work/pig-v091-evidence/a93f87a-coordinator-feedback-causality-green.status
SHA-256 37acca3b422201c678bd98bb72bf29a8a8ee6cdaf0aa1e05cdd024791ce910a8
~~~

### 19.5 Executed slice: typed terminal release

The test-only red commit `d8ae9917f174812c9e34af0a2ad6ee056586172d`
required a bounded `TerminalCause` contract and coordinator termination API.
Its exact clean checkout passed commit and gofmt checks, then focused Go
compilation exited 1 only for the missing type, constants, and method.

The green commit `7e1f2896a35f6c6de22af371332163e1661d1160`
stores the cause in the completed reservation ledger, releases manager/cache
ownership once under the coordinator lock, rejects unknown cause strings before
mutation, and permits post-completion learning only for successful completion.
Simultaneous success and cancellation produce one successful terminal event
and no reservation, cache reference, or phase-state leak under race testing.

Pass 1 re-review, prediction and learning semantics:

- Finding: terminal causes do not grant capacity or alter a forecast; they only
  release already-owned prospective state. Local QoS rejects and other
  non-success endings cannot later be mislabeled as healthy upstream outcomes,
  closing the feedback-contamination issue identified in the prior review.
- Remaining issue: success alone is still not sufficient attribution for a
  future outcome; HTTP integration must additionally check response/result
  eligibility and measurement completeness before calling `ObserveOutcome`.
- Change: typed release is builder-green, while outcome eligibility remains
  explicit and fail-closed at the future HTTP adapter.

Pass 2 re-review, idempotence and failure safety:

- Finding: every bounded cause shares the same manager/cache preflight and lock
  order, invalid causes leave the reservation live, duplicate terminal events
  return false, and success/cancel races release exactly once. Existing
  sample-assimilation and late-sample tests remain green.
- Remaining issue: `expired` is currently an explicit cause, not an automatic
  sweep; epoch reset/quarantine and abandoned-reservation clocks are still
  absent. Manager/cache presence mismatch needs a typed invariant metric before
  production shadow operation.
- Change: automatic expiry and reset remain Phase 5 work, but they no longer
  block the first injected HTTP reachability/off-mode slice because every HTTP
  exit in that slice must terminate explicitly.

Pass 3 re-review, evidence and boundary:

- Finding: the exact `7e1f289` checkout passed commit, tracked gofmt, focused
  and full Go, both race gates, Rust fmt, and locked Rust tests on the recorded
  builder toolchain. No local Go/Rust run was substituted.
- Remaining issue: no server package imports or invokes predictive admission;
  no config mode, off-zero-work proof, body-parity test, version bump, image,
  deployment, or GPU request exists.
- Change: HTTP reachability is now the P0. Runtime version remains PIG-v0.9.0
  until the off/shadow server seam itself is builder-green.

Valid typed-terminal red evidence:

~~~
/work/pig-v091-evidence/d8ae991-predictive-typed-termination-red.log
SHA-256 39307c8acace1f7e7fe0fd0ff52db4e28b012dba5238ec8120857f9fc0a50735

/work/pig-v091-evidence/d8ae991-predictive-typed-termination-red.status
SHA-256 4f4ba1a35f898f6c94b25547bf87d3886b664de610c4cedf3f7f8c09092f15fa
~~~

Valid typed-terminal green evidence:

~~~
/work/pig-v091-evidence/7e1f289-predictive-typed-termination-green.log
SHA-256 354f73351016e37df4f53187a9769118985a2861213043c61f232152282e3c63

/work/pig-v091-evidence/7e1f289-predictive-typed-termination-green.status
SHA-256 4ca05f26f0339c09f62cb355b6a68847b344457f8d9e2cab0448ff3b5a42339d
~~~

The executed test-only HTTP red added
`internal/config/pigconfig/config_predictive_admission_test.go` and
`internal/app/server/predictive_shadow_integration_test.go`. It defines:

- `PREDICTIVE_ADMISSION_MODE=off|shadow`, with enforce rejected;
- dependency-injected shadow construction that is never invoked in off mode
  and fails startup when shadow construction is unavailable or invalid;
- an ephemeral bounded JSON body snapshot only in shadow mode, with the
  original upstream request body and content length preserved byte-for-byte;
- a server-owned shadow reservation started before the authoritative QoS gate,
  released as `local_qos_reject` on local rejection, marked at semantic first
  output for eligible streaming responses, and terminated exactly once on all
  tested exits;
- off versus shadow equality for upstream request bytes and client-visible
  status, headers, and body, even when the fake predictive decision is risk.

The initial HTTP slice uses only an injected deterministic fake. Production
startup with shadow mode remains fail-closed until a real renderer/tokenizer/
coordinator adapter exists; no no-op production shadow is permitted. Therefore
this slice proves call-graph reachability and compatibility, not tokenizer
parity or deployability.

Valid initial HTTP red evidence at `81e9e9d`:

~~~
/work/pig-v091-evidence/81e9e9d-predictive-http-shadow-red.log
SHA-256 f58c7961a2addb4b09ed197782fcd977123f999344d95ed412fc198bbbca203e

/work/pig-v091-evidence/81e9e9d-predictive-http-shadow-red.status
SHA-256 4e2df3f3815468fd82cf6e5b928674e1e20338cade9d9960b87f166179b72595
~~~

The first implementation commit `774ddd0` passed all Go/Rust tests but failed
tracked gofmt and is not green. Formatting commit `5759488` passed the original
full gate, but the post-HTTP review found that its test did not compare the
prediction-only upstream header side effect. The superseding compatibility red
at `d1369e2` is valid:

~~~
/work/pig-v091-evidence/d1369e2-predictive-shadow-header-parity-red.log
SHA-256 bbaa2c8c753bfc5255c9a1aeaa2dd293f4fd436ffcf6291556402d72ba3d1147

/work/pig-v091-evidence/d1369e2-predictive-shadow-header-parity-red.status
SHA-256 0100e714965ac8fcfeecbb86d14e9a22e40123ad732deaca545d964ff051f00c
~~~

It fails at the intended assertion with `off=""` and `shadow="64"`. The exact
`0974efe` correction then passed `git show --check`, tracked gofmt, focused Go,
focused race, full Go, full race, Rust fmt, and locked Rust tests:

~~~
/work/pig-v091-evidence/0974efe-predictive-http-shadow-header-parity-green.log
SHA-256 dca0780985e58da0bffcc6066f30f705b616fbd71333d92d5567b33c26bf9ece

/work/pig-v091-evidence/0974efe-predictive-http-shadow-header-parity-green.status
SHA-256 2e900ec9e512c2062d83acb41137e3354b4d8ef9c3c92a13f38f5bbff66d5528
~~~

The clean builder was Ubuntu 24.04.4 in container `6aff8e9be30d`, with Go
1.24.5, Rust 1.97.0, and Cargo 1.97.0. No local Go/Rust test, image build,
registry publication, CVM mutation, production request, or vLLM modification
is part of this evidence.

The next failure-containment red at `44ded5b` is valid. It fails when an
injected `DecideAndReserve` panic escapes directly through `ServeHTTP`:

~~~
/work/pig-v091-evidence/44ded5b-predictive-shadow-failure-containment-red.log
SHA-256 e4a71b52afaa0b1cedcfb737775e19d561032657efb0a03ecccfe9cd44de2f47

/work/pig-v091-evidence/44ded5b-predictive-shadow-failure-containment-red.status
SHA-256 8588a47de5fca222085d2649d714517fe0f64165d7f22edfefb4817e97b8367c
~~~

The first candidate then exposed a hanging test cleanup assumption: the proxy
deadline returned, but the test backend handler required an explicit release
before `httptest.Server.Close`. That diagnostic is not green evidence. After
the test lifecycle correction, exact commit `3b44e0a` passes the complete gate:

~~~
/work/pig-v091-evidence/3b44e0a-predictive-shadow-failure-containment-green.log
SHA-256 7b0f686bfe87ab81bbd27c2239917130622165be3b00c928b752009e0232f3b2

/work/pig-v091-evidence/3b44e0a-predictive-shadow-failure-containment-green.status
SHA-256 8bb293ac2abd84acff40e6ba54afa7bc443871fbd644a9a4184d15a02931984c
~~~

The separate close-lifecycle red at `0d9a4a8` is valid and fails to compile only
because `proxyServer.Close` is absent:

~~~
/work/pig-v091-evidence/0d9a4a8-predictive-shadow-close-red.log
SHA-256 686d32a440dd1a237096dae9a0c31296f4d8112d685feded674f51a8db5f4f1a

/work/pig-v091-evidence/0d9a4a8-predictive-shadow-close-red.status
SHA-256 9b18a708bc1de10a80df671d2b3768ea8fd0bb5fe5fb2a1938c1f61bfe44eaaf
~~~

Candidate `a8f1b6d` is explicitly non-green because its Run defer referenced
`srv` before declaration. Corrected exact commit `b0b31fd` passes every gate:

~~~
/work/pig-v091-evidence/b0b31fd-predictive-shadow-close-green.log
SHA-256 b4b1552313f98a1e97034dd52039e67059c8f948d8f406a929efd40e5754e8bc

/work/pig-v091-evidence/b0b31fd-predictive-shadow-close-green.status
SHA-256 8c858843061679cf333907b1a976f06ea058f8b7928d7d313a8bcc464b8b7638
~~~

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

Three independent reviews were required before the first implementation and
are repeated after every material slice or discovery that changes the
architecture, evidence boundary, or next execution order:

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

### Post-implementation three-pass re-review

The document and implementation were reviewed again after the strict-profile
and native block-analysis slice.

Pass 1, architecture and forward-control boundary:

- Issue: a native block-analysis API and an opaque cache-mirror API could be
  misread as an integrated request path even though no Go native bridge or HTTP
  off/shadow wiring exists. A second issue was that `RequestCost` carried a
  tokenizer manifest ID, but the atomic reservation manager did not bind an
  expected ID, so a stale cost could cross the reservation boundary.
- Change: Section 19.1 now separates builder-green internal components from
  pending bridge, request-path, off-mode, scheduler, and GPU evidence. No
  production behavior claim is made. Red commit `b196bf6` defined the missing
  invariant; green commit `3f2fb90` binds the manager and simulator to one
  manifest and rejects mismatch before scheduler or state mutation.

Pass 2, tokenizer/cache correctness and privacy:

- Issues: the first keyed HMAC implementation initialized a hasher per block;
  the native key accepted a different length boundary from the Go mirror; and
  opaque analyzed blocks needed explicit manifest/epoch/shape validation.
  Review also found that Go accepted token IDs above the native unsigned 32-bit
  contract and that normalized feature flags could express `tool_choice`
  without tools or JSON schema without response format. Finally, the
  prototype's unkeyed rendered-input SHA-256 fields contradicted the keyed
  fingerprint privacy requirement, and the legacy Go token-ID/HMAC helper was
  not identity-compatible with native BLAKE3 analyses.
- Changes: both boundaries now require a 32-byte process-local key; one keyed
  BLAKE3 prefix stream creates chained 32-byte digests; and the Go mirror
  rejects mismatched manifest, backend epoch, block size, counts, empty full
  digests, or inconsistent partial metadata before cache credit. Token IDs are
  range-checked and inconsistent feature dependencies fail before engine work.
  Red commit `f8f25a5` and green commit `f7789a6` replaced the unkeyed fields
  with domain-separated keyed fingerprints; two exact-HEAD matched benchmark
  reruns passed the unchanged core/overload gates. The future request path is
  still required to use one opaque native digest protocol rather than mixing
  the legacy helper.

Pass 3, quantitative evidence and release boundary:

- Issues: the original 2 MiB absolute p99 gate failed when the matched Vec-ID
  baseline itself exceeded 1.5 seconds; a single run also contained large
  small/64-KiB scheduling outliers. The small-core measurements were also
  worded too close to the still-unmet 1 ms template-plus-FFI gate, and the first
  manifest-reservation green record omitted the toolchain/container identity
  required by Section 18.
- Changes: two final exact-commit reruns and raw SHA-256 evidence are recorded;
  the overload-only gate now combines the original 1.5-second floor with a
  1.10-times matched-baseline bound; the independent 64 KiB p95 and synchronous
  lane gates remain unchanged; and 2 MiB remains permanently ineligible for
  synchronous prediction. The small-core result is now explicitly separated
  from the end-to-end chat gate. The manifest-reservation green was also rerun
  in a clean exact-commit Builder checkout with toolchain/container identity,
  full Go, race, and locked Rust gates; no image was built or published and no
  CVM was deployed.

### 2026-07-31 prediction-authenticity three-pass re-review

This cycle was triggered by the question whether the originally intended
learning algorithm actually predicts admission. It inspected the current
`92d1daf` call graph, production and test implementations of `Scheduler`, the
HTTP request path, tokenizer/cache/manager construction sites, current
simulation events, configuration, runtime version, and the full plan.

Pass 1, model causality and objective alignment:

- Issues: the plan correctly described a future feed-forward model, but its
  execution narrative could make isolated tokenizer/cache/manager components
  look like predictive admission progress without proving reachability. Every
  concrete scheduler was test-only. Existing capacity and TTFT learners adjust
  a global limit after observations rather than forecasting this request's
  post-admit effect. No acceptance test required learned state to change a
  decision with current metrics held constant.
- Changes: Sections 3.1 and 3.2 now record the exact call-graph gap and make
  prediction causality the P0. Section 9.5 defines an explainable online
  residual calibrator, features, targets, observation eligibility, hierarchical
  fallback, staleness/shift behavior, and mandatory decision-causality tests.
  Section 16 moves this learned scheduler slice ahead of further tokenizer
  micro-optimization.

Pass 2, transaction safety and lifecycle correctness:

- Issues: the existing manager's mutex protects only minimal physical/active KV
  numbers. Cache references use a different manager/lock, and reservations do
  not retain phase resources, scheduler prediction, learner version, or outcome
  linkage. Sequentially wiring these components could leak a cache pin or a KV
  reservation on partial failure. Local-QoS rejects could also be mislabeled as
  upstream training outcomes.
- Changes: Section 11.1 introduces one coordinator-owned all-or-nothing
  transaction, immutable work outside the lock with epoch revalidation, no
  external work under the lock, exact lifecycle reasons, failure-injection
  properties, and zero-leak race gates. Section 16 adds a separate integrated
  coordinator phase before HTTP wiring.

Pass 3, evidence validity and executable next step:

- Issues: the predictive simulator's `EventSample` was a counter only and never
  reconciled a sample into manager state. Prior green tests could all pass while
  no application package imported the predictive runtime. The next learned
  slice lacked named red-test files, intended failure reason, fixed builder
  commands, and quantitative goodput comparators. The target six-CVM set was
  also at risk of being treated as one Gemma/vLLM profile despite three of the
  nodes being GLM-5.2/SGLang.
- Changes: Sections 6.3.3 and 19.1 record the backend/profile split, immutable
  Gemma production provenance, request-class special-token result, the no-op
  sample event, missing predictive config, and PIG-v0.9.0 runtime truth. Section
  19.2 fixes the next red/green file set and builder gates. Sections 16 and 17
  add application-reachability/off-zero-work tests, a real sample event,
  prediction-causality gates, all-or-nothing state assertions, deterministic
  completion-goodput comparators, zero false fits, and cache-cold non-regression.

Result at the time of this review cycle: the document explicitly said that the
learning algorithm was not implemented or wired at `92d1daf`, and selected the
test-only learned-scheduler causality commit in Section 19.2 as the next action.
The later execution record in Section 19.2 supersedes that implementation
status without changing the HTTP/deployment/GPU-evidence boundary.

### 2026-07-31 post-learned-scheduler three-pass re-review

This cycle inspected `b9f42b0`, its exact red/failed-format/green builder
evidence, the manager/scheduler lock and call order, virtual-state arithmetic,
model identity, outcome lifecycle, simulator integration, and remaining plan
claims.

Pass 1, learned prediction causality:

- Finding: the original defect is fixed at the internal manager boundary.
  `DecideAndReserve` now calls a concrete `LearnedScheduler`; exact-cell static,
  healthy, adverse, sparse, stale, wrong-epoch, invalid, and unattributed cases
  have behavior tests. The manager stores the prediction, and a matching
  outcome updates the calibrator exactly once.
- Remaining issue: this is still one-request/internal-manager causality. There
  is no HTTP reachability, real profile calibration, hierarchical backoff,
  measured coverage, or distribution-shift circuit breaker. The exact-cell
  calibrator alone is not the full Phase 4 model.
- Change: Sections 19.1 and 19.2 now move the concrete learned slice into the
  builder-green list, record its exact limitations, and retain the remaining
  calibration/coverage work rather than marking Phase 4 complete.

Pass 2, virtual-state and identity safety:

- Issues: `Manager` binds `RequestCost.ManifestID`, but it does not bind the
  scheduler's `ModelIdentity` to an expected backend/predictor profile. A wrong
  but internally valid scheduler can therefore produce a forecast. Also,
  `addState`/`subtractState` still update only physical/active KV; committed
  reservations do not add decode sequences, active context tokens, or uncached
  prefill to the next same-window scheduler prediction. This means atomic KV
  reservation is proven but concurrent prospective TPS/TTFT state is not.
- Change: Phase 5 now requires exact cross-profile identity binding and makes
  cumulative phase-resource contribution/release an explicit red-test gate for
  the transaction coordinator.

Pass 3, evidence and completion boundary:

- Findings: the valid `fa40ad6` red fails for the intended missing production
  symbols; the first `c1ca96d` candidate is correctly retained as failed because
  gofmt was nonzero despite all tests passing; the exact `b9f42b0` clean checkout
  passes every recorded Go, race, and Rust gate. Hashes and invalid-red reasons
  are recorded in Section 19.2.
- Remaining issue: these tests use deterministic static priors/outcomes and do
  not measure simulation goodput, false fits/denies, scheduler latency, real
  tokenizer/cache transaction behavior, or GPU coverage. No Docker image was
  built.
- Change: the status now says learned causality is builder-green while the
  integrated transaction remains P0. No HTTP, image, version-runtime,
  production, or real-GPU claim was added.

### 2026-07-31 post-HTTP-shadow three-pass re-review

This cycle inspects the first real server call graph at `5759488`, the
compatibility defect and correction through `d1369e2`/`0974efe`, the exact
builder evidence, and the still-missing production adapter. Each pass is
written before the next pass starts.

Pass 1, prediction causality and request compatibility:

- Finding: `PREDICTIVE_ADMISSION_MODE=shadow` now creates an injected
  reservation after bounded JSON classification and before the existing QoS
  gate. Streaming semantic first output and tested terminal exits reach the
  reservation; `off` neither constructs the adapter nor retains a predictive
  body. This closes only the HTTP reachability seam. The injected fake is not a
  renderer, tokenizer, cache mirror, coordinator, or learned scheduler adapter.
- Issue found: `5759488` reused prediction-only parsed output tokens in the
  authoritative `Classification.OutputTokens` fields. With output-lane
  classification otherwise disabled, shadow could still add
  `X-PIG-Output-Tokens` upstream and could influence early-SSE policy. This
  violated the zero-behavior-change shadow contract even though the lane itself
  remained unchanged.
- Test and change: test-only `d1369e2` holds body, response, and current metrics
  constant and fails only because the upstream header is `off=""` versus
  `shadow="64"`. `0974efe` separates predictive output-token metadata from the
  fields consumed by existing forwarding and queue policy. The exact commit
  passes focused/full Go, both race gates, tracked gofmt, Rust fmt, and locked
  Rust tests on the remote builder.
- Remaining boundary: no production factory exists, so real shadow startup
  intentionally fails closed. Real tokenizer/cache/learned prediction still
  cannot affect an HTTP forecast, decision, or reservation at this point; the
  prediction-authenticity goal is not complete.

Pass 2, lifecycle, failure containment, and privacy:

- Finding: the server-owned guard serializes semantic transition and terminal
  release, suppresses duplicates, and prevents a semantic transition after a
  terminal event. A reservation starts before local QoS admission; a local QoS
  reject releases it as `local_qos_reject`; successful tested responses release
  as `completed`; client disconnect and non-success results use non-learning
  causes. The request body snapshot has separate backing storage from the body
  forwarded upstream and is absent for off, unknown-length, oversized,
  saturated, and read-failure paths.
- Issues found: injected `DecideAndReserve`, `MarkPrefillComplete`, and
  `Terminate` calls are synchronous and not panic-contained. A future slow or
  panicking adapter could therefore add unbounded latency or alter the client
  response even in shadow. Timeout versus generic upstream failure is not yet
  classified precisely. The factory runs before later server construction can
  fail and has no `Close` rollback contract, which would leak a future adapter
  that owns workers or native resources. Finally, the interface cannot prevent
  an adapter from retaining raw body bytes; privacy currently depends on the
  injected implementation.
- Changes to the executable order: a real adapter cannot be installed next.
  First add deterministic failure-injection tests for decide/semantic/terminal
  panic containment, a calibrated synchronous cost budget with a
  profile-disable circuit breaker, explicit timeout classification, and an
  idempotent adapter close/constructor-rollback contract. The adapter must
  convert the ephemeral body to opaque analysis and drop all raw references
  before returning. Shadow failure must produce a bounded observation and fall
  back to the unchanged current QoS path; it must never create capacity
  headroom or change a client-visible result.
- Remaining lifecycle work: automatic expiry, atomic epoch reset/quarantine,
  invariant metrics, and fully attributed HTTP outcomes remain required before
  production shadow. The current explicit defer is sufficient only for the
  tested injected reachability slice.

Pass 3, evidence validity, document consistency, and next gate:

- Issues found: the top-level status and Section 19.1 still described HTTP mode
  loading and reachability as absent after those gates became green. The first
  `774ddd0` builder run could also be misread as green because every executable
  test passed even though tracked gofmt failed. Finally, the original HTTP test
  compared upstream body but not prediction-only upstream headers, allowing the
  Pass 1 defect through.
- Changes: Sections 3.1.1, 19.1, and this execution record now distinguish the
  historical `92d1daf` audit, injected HTTP reachability, and the missing real
  adapter. `774ddd0` is explicitly non-green. The valid `81e9e9d` HTTP red,
  `d1369e2` compatibility red, and final `0974efe` green have exact paths,
  hashes, commit identities, environment, and gate scope recorded. The new
  header comparison closes the evidence gap without claiming routing,
  tokenizer parity, learned HTTP causality, simulation efficacy, or GPU
  accuracy.
- Release boundary: the branch is pushed, but runtime remains PIG-v0.9.0. No
  v0.9.1 tag, image, registry artifact, or deployment is authorized or created.
  The six production CVMs remain historical inputs only.
- Next executable gate: preserve a valid production-construction HTTP red that
  fails because the real adapter is absent, then implement the Section 6.3.4
  renderer/native-analysis/cache/coordinator vertical slice. Failure
  containment and close lifecycle are already builder-green through `3b44e0a`
  and `b0b31fd`; they remain regression gates rather than the next missing
  feature. Deterministic efficacy comparison follows only after real-adapter
  parity, identity, learning-causality, latency, and lifecycle gates pass.

### 2026-07-31 real-adapter-contract three-pass re-review

This cycle was triggered by the observation that an internally causal learner
still has no effect on real HTTP prediction while the production adapter is
absent. It reviewed the new Section 6.3.4 after each change rather than treating
the earlier HTTP seam as completion.

Pass 1, model causality and feedback:

- Finding: exact tokenizer/cache/coordinator wiring alone would make static
  prospective prediction real, but would not prove that online learning is
  live. The existing HTTP callback carries lifecycle causes, not an attributed
  actual-output TPS/TPOT outcome.
- Change: construction now separates monotonic backend-state reconciliation
  from per-reservation attributed outcomes. Aggregate metrics cannot become a
  fabricated request outcome; missing target attribution leaves validity false.
  Learned mode cannot be reported integrated without a real attributed source.
- Gate: identical request/current metrics/profile/state with only eligible
  history changed must alter a pre-forward estimate and decision/reservation
  through the real adapter. Server fakes and post-forward learner queries fail.

Pass 2, body coverage, native timeout, and lifecycle safety:

- Finding: the current predictive body snapshot is coupled to
  `JSON_CLASSIFY_BODY_BYTES`, known `Content-Length`, and classifier
  concurrency. Oversized, chunked/unknown-length, saturated, and partial-read
  cases receive no body, which disproportionately excludes long prompts where
  KV/cache prediction matters. A context also cannot hard-cancel an active C
  ABI call.
- Change: predictive capture receives an explicit ceiling, byte budget, and
  reasons without changing forwarded bytes. Coverage must include the declared
  prompt envelope. In-process work is limited to measured synchronous
  envelopes; missed active-call cancellation moves that lane to a long-lived
  Unix helper or leaves it explicitly unknown. Close/constructor rollback and
  late-commit prohibitions are explicit.
- Gate: memory, deadline-before-call, deadline-during-call, close races,
  oversized/unknown-length capture, and no-late-reservation tests precede any
  claim that the C ABI is selected safely.

Pass 3, red/green validity and reproducibility:

- Finding: a test that references a not-yet-created constructor would be only a
  compile failure, while a server fake would retest the already-green seam. Hot
  analysis also intentionally omits token IDs, so it cannot by itself provide
  exact oracle evidence.
- Change: the first red uses the existing production `newProxyServer` entrypoint
  and fails for the current missing-adapter behavior. Adapter-contract reds use
  the real adapter type with injected components. A separate builder-only
  retain-ID path supplies parity; hot-path and parity latency remain distinct.
  Fixture provenance, native-first builder order, clean commit identity, status
  files, hashes, full/race/Rust/format gates, and the no-local-execution boundary
  are recorded.
- Result: the document now defines the real adapter's body-to-reservation and
  outcome-to-future-prediction contracts. No implementation, tokenizer parity,
  simulation gain, image, v0.9.1 runtime promotion, vLLM change, or deployment
  is claimed by this review.

### 2026-07-31 post-native-parity three-pass re-review

This review covers the implementation and builder evidence from `a433bb1`
through `f034231`. It does not replace the remaining production-construction,
feedback, capture, latency, or efficacy gates. Each pass changed the plan or an
executable test before the next pass was recorded.

Pass 1, prospective model causality and cache/TPS interaction:

- Finding: the real adapter now renders and analyzes before calling the same
  `Coordinator.DecideAndReserve` that owns cache and virtual-state
  reservations. The earlier HTTP learning-causality test holds current state
  fixed and changes only three eligible attributed outcomes; the cold result is
  `static/fit`, while the trained result is
  `calibrated/new_tps_at_risk`. This proves that learned output can affect a
  pre-forward decision through the real adapter, but the outcomes are still
  deterministic test injections rather than production telemetry.
- Finding: the first native end-to-end candidate `874d52a` was rejected on its
  second identical request even though the first prefix was active. The direct
  reason was `new_tps_at_risk`: two decode sequences project 50 completion TPS
  per user against an 80 TPS target. The failure was correct model behavior,
  not a tokenizer/cache failure.
- Change: `fd49675` separates the two invariants. At a 40 TPS target, the active
  full-block prefix reduces the second request's uncached prefill to its partial
  block while the exact renderer/native/coordinator reservation succeeds. At
  the original 80 TPS target, the same cache reuse is still rejected before
  forwarding and does not mutate a second reservation. Cache credit therefore
  reduces prospective prefill/KV work but never overrides decode TPS.
- Remaining P0: construct this chain from one immutable Gemma profile in the
  actual `newProxyServer` path, feed it monotonic backend reconciliation and
  owned attributed outcomes, and keep learned mode fail-closed when that
  outcome contract is unavailable. Test-injected outcomes are not production
  learning.

Pass 2, identity, native lifecycle, and body coverage:

- Finding: `f034231` loads the pinned 31.2 MiB tokenizer, verifies its hash,
  renders all five supported request forms, and calls the Go C ABI wrapper with
  Chat `add_special_tokens=false`, Completion
  `add_special_tokens=true`, block size 64, fixed manifest, and fixed backend
  epoch. Exact token counts and block shapes match. The production hot path
  returns no token IDs; a separate builder-only CLI retains IDs for parity.
- Finding: the tagged word-level transaction test proves one executable
  renderer/native/cache/TPS vertical path but is not production Gemma parity.
  Conversely, the five-case Gemma oracle test proves production asset parity
  but does not by itself prove the production factory. Both tests are required
  and neither is described as the other.
- Remaining issues: `realPredictiveShadow.Close` releases reservations but the
  future factory must also own and destroy the analyzer and any observation
  workers in reverse order on constructor failure. The C call still cannot be
  forcibly cancelled after entry; deadline-during-call, worst-case latency,
  memory, panic/isolation, and close-race qualification remain mandatory.
  Predictive body capture is still coupled to the classifier path, so long,
  chunked, saturated, and partially read bodies do not yet have the separate
  bounded coverage required by Section 6.3.4.
- Change: the next implementation gate is explicitly the immutable bundle and
  fail-closed factory plus resource ownership, followed by independent body
  capture. No no-op shadow adapter may be installed to make startup pass.

Pass 3, red/green validity and oracle provenance:

- Full renderer regression at `6334aeb9145229b07a1720255aabd1ff83636601`
  passed focused/default/tagged Go, both race modes, Rust release/locked tests,
  tracked gofmt, Rust fmt, and `git show --check`. Evidence:
  `/work/pig-v091-evidence/6334aeb-gemma4-renderer-full-green.log`, SHA-256
  `374e878817cd9a09f949b91609e9f2c24d19c14c968a7909c2f440c94adda227`;
  status SHA-256
  `88d051111a824dfd3e88874b2343b93fc80805c0cf235d042664bc6f90761014`.
- The behavioral native transaction red at
  `874d52a78fe0689043f91bb92d72766230425ed2` failed specifically on the TPS
  expectation above. Evidence log SHA-256
  `6d7dc8e69831451f197b9ef94ae06b1da38ea4863ff706f480532d254a828919`;
  status SHA-256
  `0f4a6ea528a8f0facfc15ea4e6ce4e6bf5ec45b2172d14e2568b47f366272cb6`.
  The corrected `fd49675fd89bef0521343d9dbcabf69b3e3646b0` focused/tagged/race
  green log SHA-256 is
  `7a100ab066062a3248ce47e04ec1f061b0a1f82f8e7ea189ee1ddb35d4d664a7`;
  status SHA-256 is
  `62e66aa19d78d5a165b69c16a31613dfd97e4d01074616c138b41cdfbdd1c445`.
- `c63184252a36c0eb6a5dae57cb41d433a4b9b898` produced zero parity
  mismatches but is not green because tracked gofmt failed. The valid
  `f034231bd79b5715c5414beeade2a583cb70345a` rerun passed asset hashes,
  tracked Go/Rust formatting, Rust release, renderer+C ABI focused/race tests,
  exact final-ID comparison, and the full tagged server suite. Evidence log
  SHA-256 is
  `383ee3684b227f7e19371d44b161dd0d4f751ae98f04d2be3a745817d467f17c`;
  status SHA-256 is
  `37fd70a7c230b9ab6a3bf01bc97e9e2bdcd94e37e5165631f7041d599cea9492`;
  final-ID report SHA-256 is
  `4fe90e58efdbe81d319c04c215ad2495009a97d46b397ecb7e34bb67d1ff6047`.
- The verified assets are tokenizer
  `cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f`,
  tokenizer config
  `e467669cfe172dfb0c4e7de7bfbe7553c42bfa5de95acd71f423f58a434d80de`,
  LF-normalized template
  `afdbb2abe3667ccde95cc2f86919f05370339399bab5f750950a4390523b8927`,
  and oracle
  `0161539eae267099adcda3d04b240b800e12a292d96a6bea9192865a71b0955a`.
  All five final token-ID arrays match exactly with zero mismatches.
- Limitation retained: the oracle used Transformers 4.57.1, tokenizers 0.22.2,
  Jinja2 3.1.6, pinned model assets, and the LF-normalized production template.
  It was not generated by executing the exact production vLLM image. vLLM
  remained a read-only reference; no vLLM source, image, or deployment was
  modified.
- Release boundary: runtime remains PIG-v0.9.0. No PIG image was built or
  published, no production CVM was contacted with inference traffic, and no
  deployment was performed. Simulation efficacy and real-GPU coverage remain
  unproven.
