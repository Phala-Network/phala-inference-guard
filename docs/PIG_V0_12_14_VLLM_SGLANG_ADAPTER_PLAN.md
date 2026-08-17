# PIG v0.12.14 vLLM and SGLang Backend Adapter Plan

Status: active implementation plan. Nothing in this document authorizes using
the unaccepted source or a builder-local image on a production CVM.

## 1. Objective and scope

PIG must maximize QoS-constrained throughput in front of either one vLLM
upstream or one SGLang upstream. Request classification, prediction,
reservation, Context/KV/Prefill/TPS gates, proxying, and lifecycle handling stay
framework-neutral. A backend adapter is responsible only for turning one
coherent framework-specific Prometheus scrape into the common admission
observation.

The active deployment request is:

1. implement and validate SGLang support on development CVM
   `c21b7281-2c25-4453-8a68-f39ec42d03b4`;
2. preserve the accepted vLLM behavior;
3. publish an image only after all acceptance gates pass;
4. then update only PIG on CVM
   `f563dae5-d468-4134-aa57-11ad4588bd32`, with
   `PREDICTIVE_TPS_REFERENCE=50`;
5. do not restart either CVM or rebuild the SGLang model service merely to test
   PIG.

The production update remains a later stage. Its exact YAML must be derived
from a fresh live Compose immediately before mutation.

## 2. Corrective finding

v0.12.13 retained partial SGLang parsing in the Prometheus package, but the
startup probe and observer deliberately accepted only `BackendKind=vllm`.
Several retained SGLang mappings were also not safe for predictive admission:

- `sglang:num_retracted_reqs` is a per-report gauge that is reset after each
  report, not a monotonic preemption counter;
- `sglang:num_paused_reqs` is async-weight-sync state and is not KV-pressure
  preemption;
- `sglang:gen_throughput` is an instantaneous gauge and is not a monotonic
  generation counter;
- `sglang:generation_tokens_total` advances when a request finishes, so it is
  exact completion accounting but too delayed for a 500-ms sustained-TPS
  controller;
- SGLang exposes `sglang:page_size`; leaving KV block size unset makes automatic
  capability initialization fail;
- the existing model-identity parser required vLLM metric families and could
  never establish a SGLang identity;
- exported PIG backend metrics hard-coded `backend_kind="vllm"`.

Therefore this is an adapter correction, not a relaxation of the vLLM-only
observer check.

## 3. Framework-specific metric contracts

### 3.1 vLLM adapter

The accepted v0.12.13 mapping remains the compatibility baseline:

| Common field | vLLM source | Aggregation |
| --- | --- | --- |
| model identity | required unique `model_name` across the admission families | exact unique value |
| KV capacity/block | labels on `vllm:cache_config_info` | one coherent geometry |
| KV used | capacity times `vllm:kv_cache_usage_perc` | maximum usage ratio |
| running | `vllm:num_requests_running` | sum logical engines |
| waiting | `vllm:num_requests_waiting` | sum logical engines |
| generation | `vllm:generation_tokens_total` | sum series |
| preemption | `vllm:num_preemptions_total` | sum series |

All vLLM regression fixtures and observer lifecycle tests must remain green.

### 3.2 SGLang adapter

The initial contract is grounded in the live c21 SGLang scrape and the exact
Python source inside `muse-glimmer-r10-candidate`.

| Common field | SGLang source | Required semantics |
| --- | --- | --- |
| model identity | core admission families' `model_name` | exactly one non-empty value |
| KV capacity | `sglang:max_total_num_tokens` | logical token slots; never multiply TP ranks |
| KV page/block | `sglang:page_size` plus `sglang:num_pages` | positive integers and `num_pages == floor(capacity / page_size)` |
| KV free | `sglang:kv_available_tokens` | free full-pool slots |
| KV evictable | `sglang:kv_evictable_tokens` | radix-cached slots reclaimable for a new request |
| KV active | `sglang:kv_used_tokens` | active locked slots; consistency check only |
| running | `sglang:num_running_reqs{priority=""}` | the empty-priority series is the total; take its maximum across duplicate TP/PP rank views and never combine priority subsets |
| waiting | `sglang:num_queue_reqs{priority=""}` | same total-versus-priority rule as running |
| generation | `sglang:realtime_tokens_total{mode="decode",priority=""}` | monotonic counter; take the maximum across duplicate TP/PP rank views for the one supported logical scheduler |
| preemption | `sglang:num_retracted_requests_total{priority=""}` | monotonic KV-pressure counter; take the maximum across duplicate TP/PP rank views |
| runtime epoch | `process_start_time_seconds` when available | positive value; counter rollback still detects reset |

`sglang:num_retracted_requests_total` may have no sample before the first
retraction. In the live SGLang Python multiprocess registry, an unmaterialized
labeled metric emits neither a sample nor HELP/TYPE, even though the current
source has constructed the counter. Therefore an otherwise coherent modern
SGLang admission schema may treat complete absence as cold zero. Once any
counter sample materializes, it must carry counter type and the unified total
label. A non-zero legacy `num_retracted_reqs` gauge without the monotonic
counter invalidates that scrape; the resettable gauge and `num_paused_reqs`
must never be fabricated into a cumulative preemption value.

`sglang:realtime_tokens_total{mode="decode"}` follows the same cold-start rule:
SGLang creates its labeled sample only on the first non-zero increment and the
multiprocess exporter may omit HELP/TYPE before that point. Complete absence is
exact cold zero. Prefill-only children may materialize the counter family while
the absent decode child remains exact zero. Once any decode child exists, a
counter-typed `mode="decode",priority=""` total is mandatory. The model
identity comes from the static admission geometry rather than requiring prior
decode traffic.

SGLang also does not materialize the labeled running, waiting, or absolute KV
gauges until its first scheduler report (idle reporting may take 30 seconds),
while PIG's default startup probe is 10 seconds. If and only if the static
model/KV geometry is coherent, an all-missing dynamic family is interpreted as
the multiprocess cold state: running/waiting/used/evictable are zero and
available equals capacity. A declaration with the wrong type, a materialized
sample without the expected type, a missing empty-priority total, or a partial
dynamic family remains invalid. This avoids low-flow startup self-lock without
turning a partially materialized scrape into a mixed observation.

The common SGLang used-KV value is:

```text
capacity - available - evictable
```

This counts every non-reclaimable full-pool slot while excluding radix cache
that SGLang can evict for a newly admitted request. The inspected SGLang source
assigns `kv_used_tokens` from this same expression, so the direct gauge must
equal the derived value. A mismatch means the scrape is incoherent and fails
closed; it is not treated as an active-only approximation.

The first SGLang adapter supports exactly one logical `engine_type="unified"`
scheduler. Duplicate TP/PP rank series are rank-level views of that scheduler
and are deduplicated rather than summed. Multiple distinct `dp_rank` values are
rejected: summing their KV would violate the per-replica fit invariant, while
taking only one would understate aggregate running and generation state. DP and
PD require an explicit per-replica capability/admission design in a later
version rather than an inferred aggregation rule.

`sglang:token_usage`, `full_token_usage`, `swa_token_usage`, and
`mamba_usage` remain diagnostics. They are rounded, may refer to multiple pool
types, and are not used to manufacture absolute capacity. Hybrid-pool support
must not reinterpret Mamba state slots as text-token KV capacity. A schema that
cannot provide the required full-pool invariants fails closed instead of using
a guessed conversion.

`sglang:generation_tokens_total` remains useful as completed-request
accounting, but does not drive the 500-ms TPS window. Live c21 evidence showed
it equals the decode realtime counter after all requests complete; a live
in-flight test must prove that the realtime decode counter advances before
completion.

## 4. Adapter architecture

The Prometheus layer will expose one framework-neutral parse entry point and
two isolated adapters:

```text
bounded scrape fetch
  -> unambiguous framework signature detection
  -> vLLM adapter OR SGLang adapter
  -> common telemetry.Sample
  -> framework-neutral startup validation
  -> immutable backend kind + model/KV capability
  -> framework-neutral observer and Controller publication
```

Rules:

- a scrape with complete signatures for both frameworks is ambiguous and
  unusable;
- a partial signature is unusable, not silently combined with another
  framework;
- startup freezes backend kind, model fingerprint, KV capacity, and block/page
  size for the capability epoch;
- later framework, model, capacity, or block drift closes the epoch;
- automatic `/v1/models` metadata initialization is shared because both live
  vLLM and SGLang return exactly one ID and positive `max_model_len`;
- exported PIG logs and backend metrics report the selected backend kind;
- no new production environment variable selects the framework.

The observer indexes only fields consumed by current admission. Historical
vLLM prompt-source and prefill-duration counters and SGLang diagnostic
throughput/completion/paused metrics are not parsed: they have no consumer in
prediction, reservation, or admission. The legacy SGLang retraction gauge is
indexed only to invalidate a non-zero scrape when the real monotonic counter is
unavailable. TTFT histograms are likewise not reparsed by either adapter because
TTFT is not a current QoS gate and the common observer does not consume them.
This version does not imply a hidden TTFT or Prefill-learning path.

This keeps framework detection and parsing open for extension while the
Controller remains closed to framework-specific changes.

## 5. Test-first matrix

### 5.1 Parser red/green tests

- exact live-shaped SGLang idle fixture;
- running/waiting total plus priority subsets, proving max rather than sum;
- mixed streaming/completion counters do not affect the realtime decode source;
- unmaterialized multiprocess cold decode/retraction counters do not self-lock
  startup even when HELP/TYPE is absent;
- registered cold dynamic gauges initialize an idle backend before SGLang's
  first 30-second report, while a partial family remains unusable;
- registered-but-zero retraction counter;
- non-zero retraction counter and reset;
- a non-zero legacy retraction gauge and paused gauges cannot fabricate a
  preemption counter;
- priority child series without `priority=""` totals are rejected;
- materialized gauges/counters with the wrong Prometheus type are rejected;
- KV identity with coherent free, evictable, and used accounting;
- invalid page geometry, inconsistent absolute KV gauges,
  negative/NaN/fractional values, duplicate model identities, mixed frameworks,
  multiple DP replicas, non-unified engines, missing required metrics, wrong
  counter types, and counter overflow;
- unchanged vLLM fixtures and mixed-engine aggregation.

### 5.2 Startup and observer tests

- vLLM and SGLang both initialize the same common Controller contract;
- backend kind is frozen and drift is rejected;
- model, capacity, page/block, and runtime reset paths revalidate metadata;
- one incomplete scrape retains the last coherent observation until freshness
  expiry;
- SGLang retraction delta selects contention for one coherent sample without a
  sticky cooldown;
- PIG backend metrics expose the real framework kind.

### 5.3 c21 live integration

Use the already-running `muse-glimmer-r10-candidate` on host port 30000. Do not
restart it.

1. establish live `/v1/models` and `/metrics` schema evidence;
2. build PIG from the exact candidate source in `pig-v0124-workbench`;
3. start an isolated PIG test container only, first with shadow and then default
   enforce, pointing at the existing SGLang;
4. prove startup-derived model identity, context length, capacity, page size,
   backend kind, fresh observations, health, metrics authentication, and proxy
   behavior;
5. run a long streaming request while polling raw SGLang and PIG metrics to
   prove the selected generation counter advances before completion;
6. use `PREDICTIVE_TPS_REFERENCE=50` and controlled concurrency to exercise TPS
   warming and the mature envelope without requiring every 500-ms sample to be
   above 50;
7. stop and remove only the PIG test containers and temporary network/artifacts.

### 5.4 Complete source gates

On c21 only:

```text
gofmt check
focused parser/startup/observer/metrics tests
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/phala-inference-guard
deterministic simulations
hot-path benchmarks and allocation checks
```

No Windows executable test result is release evidence.

## 6. Three-pass review gates

### Pass 1: model and causality

Verify every selected metric against live HELP/TYPE/sample output and the exact
SGLang source that updates it. Hold observations constant while changing a
parsed field to prove it changes the pre-forward decision or reservation.

### Pass 2: safety and lifecycle

Review framework ambiguity, TP/DP aggregation, scrape incompleteness, counter
reset, model/capability drift, reservations, cancellations, exact-once release,
staleness, and bounded parsing. A missing SGLang metric must fail closed without
closing a healthy vLLM adapter.

### Pass 3: evidence and release

Re-run the complete clean c21 matrix at the exact executable commit, then build
and smoke the image from that commit. Only an accepted image may be pushed.
Registry digest, runtime/OCI identity, and source revision must match.

## 7. Production update boundary

Before touching `f563...`:

- re-read and hash its host-side running YAML;
- verify its SGLang scrape satisfies the accepted SGLang contract;
- record current PIG image/container state and Router state;
- prepare the exact PIG-only diff and rollback image;
- set only `UPSTREAM=http://sglang:8000`, infrastructure values, and
  `PREDICTIVE_TPS_REFERENCE=50`;
- remove v0.8.x `MODEL_NAME`, `BACKENDS`, `GLOBAL_LIMIT`,
  `PIG_QUEUE_WAIT_SECONDS`, and every `DYNAMIC_*` variable;
- do not restart the CVM or SGLang; replace/recreate only PIG after the user
  requested production stage is reached;
- validate health, authenticated status/metrics, backend kind, capability,
  representative request, restart count, Router visibility, and rollback
  readiness.

The live host YAML and control-plane Compose must not be allowed to drift
silently. If a PIG-only replacement cannot also preserve the desired
configuration durably, stop and report that operational boundary before
mutation.
