# PIG v0.9.1 Predictive Admission Shadow Plan

Status: reviewed shadow plan; implementation and builder validation in progress
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
immutable add_special_tokens policy
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
| Encode policy | `encode` and `encode_batch` hard-code `add_special_tokens=false`. | Make special-token behavior an immutable, golden-tested profile decision. |
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
- immutable add/omit special-token policy, which a request cannot override;
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
- strict tokenizer manifest fields and immutable special-token policy;
- request-feature capability and dependency rejection before engine work, plus
  rejection of native token IDs outside the unsigned 32-bit contract;
- domain-separated runtime-local Go and context-keyed native rendered-input
  fingerprints, with unkeyed SHA-256 removed from runtime results;
- native raw tokenizer parity prototype and retained-Encoding source study;
- native no-ID block analysis with chained keyed digests and partial metadata;
- cache mirror ingestion of epoch-validated opaque block analyses;
- two matched performance reruns for the final streaming digest design.

Still pending and not claimed:

- a strict lossless JSON chat-template runtime supporting the pinned Gemma4
  template, tools, tool results, reasoning, multimodal placeholders, and
  approved template kwargs;
- final-token parity against a pinned production vLLM oracle for every enabled
  request feature class;
- a Go C ABI or Unix-socket native engine and its cancellation/crash-isolation
  comparison;
- complete request-path population of the phase-rich reservation fields in
  Section 11; the current Builder-green manager reserves the minimal
  `RequestCost` contract only;
- one request-path digest protocol: the legacy Go token-ID/HMAC helper remains
  an internal test helper and must not be mixed with native BLAKE3 opaque block
  analyses in a shared cache-mirror epoch;
- off/shadow HTTP request-path integration and proof that off mode performs
  zero predictive work;
- calibrated scheduler/TPS/TTFT/TPOT profiles from a real upstream;
- Docker smoke, image publication, any CVM deployment, and enforcement.

The active implementation therefore remains an internal builder-tested slice.
It does not change production traffic or current PIG behavior.

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
