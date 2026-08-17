# PIG v0.12.15 Cache-Aware Backend Contract And Release Plan

## 1. Objective

Maximize sustained aggregate throughput subject to bounded QoS degradation. The
request must be admitted or protected before it reaches the backend; backend
feedback only updates the next observation and never rewrites an in-flight
decision.

v0.12.15 must correct two backend-adapter contract defects discovered after
v0.12.14:

- accept the SGLang protected/session-held KV gap while charging the whole
  non-reclaimable gap to admission;
- require vLLM gauges and counters to have the types declared by vLLM itself;
- use backend-native token counters to estimate recent cache-aware Prefill
  compute cost without weakening KV fit or long-context safeguards.

Keep the bounded fast tokenizer and complete estimated input tokens as the
request-size signal. Do not add a cache lookup, per-model asset, learning
algorithm, TTFT gate, Router behavior, or request mutation. Keep the default
backend metrics interval at 500 ms and default predictive mode at enforce.
`PREDICTIVE_TPS_REFERENCE=50` remains the intended f563 production QoS
reference; isolated tests may explicitly override it.

The correction must preserve QoS-constrained throughput: count all
non-reclaimable full-pool slots against admission, accept valid
protected/session-held KV gaps, and reject a torn scrape whenever accepting it
could understate non-reclaimable KV. It must also avoid treating evictable
prefix-cache KV as occupied hard capacity merely because the cache hit rate is
high.

## 2. Production Finding

Production SGLang source version `0.0.0.dev1+gc4271c3fe` defines:

```text
kv_available_tokens + kv_evictable_tokens + kv_used_tokens
    <= max_total_num_tokens
```

`kv_used_tokens` is active locked KV only. The gap may contain protected or
session-held slots that are neither free, evictable, nor active. The safe common
value remains:

```text
non_reclaimable = capacity - available - evictable
```

v0.12.14 incorrectly required `kv_used_tokens == non_reclaimable`. That equality
works when the gap is zero but fail-closes a valid scrape when protected/session
KV exists. Live production scrapes also proved that the three gauges update
sequentially and can be temporarily torn.

The vLLM adapter already keeps vLLM's aggregation rules separate from SGLang,
but it currently accepts samples without checking Prometheus `TYPE`. Upstream
vLLM commit `5fd7a888386cff800f32de6b5a33d1dd3ca1e397` declares:

```text
num_requests_running       gauge, one series per engine
num_requests_waiting       gauge, one series per engine
kv_cache_usage_perc        gauge, one series per engine
cache_config_info          gauge, one coherent geometry per engine
generation_tokens_total    counter, one series per engine
num_preemptions_total      counter, one series per engine
```

Running, waiting, generation, and preemption remain sums across vLLM engine
series. KV usage remains the maximum per-engine ratio, and cache geometry must
be identical across present engine series. The one coherent per-engine KV pool
is deliberately not multiplied by DP engine count: this is conservative for a
single PIG upstream and preserves per-request fit even when vLLM chooses the
engine. SGLang `priority=""` filters and TP/PP maximum deduplication must never
be applied to these vLLM series.

## 3. Correct Contracts

### 3.1 SGLang

The SGLang adapter must require:

```text
0 <= active_used <= non_reclaimable <= capacity
```

PIG continues to publish `non_reclaimable` as common used KV. Therefore the
protected/session gap is charged to admission rather than treated as capacity.
`active_used > non_reclaimable` remains invalid because it proves the candidate
scrape would understate occupied KV. A torn scrape that only overstates
non-reclaimable KV is conservative and may be accepted.

All existing model, engine, DP, priority, metric-type, page-geometry, counter,
freshness, and reservation rules remain unchanged.

The f563 SGLang `0.0.0.dev1+gc4271c3fe` live scrape confirms the selected
families and types. Decode TPS uses the scheduler-interval counter
`sglang:realtime_tokens_total{mode="decode",priority=""}`. It must not use the
completion-time `generation_tokens_total` counter or instantaneous
`gen_throughput` gauge. KV retraction uses
`num_retracted_requests_total{priority=""}` when materialized; the resettable
`num_retracted_reqs` gauge and async-weight-sync `num_paused_reqs` are not
preemption counters.

### 3.2 vLLM

Every materialized vLLM admission family must match its declared type:

```text
cache_config_info, kv_cache_usage_perc,
num_requests_running, num_requests_waiting = gauge

generation_tokens_total, num_preemptions_total = counter
```

Missing or wrong types invalidate the affected common field and make startup or
fresh-observation validation fail closed. Unlike SGLang's documented
multiprocess cold-zero exceptions, vLLM initializes these labeled children; no
missing-TYPE cold-zero exception is allowed.

### 3.3 Cache-aware Prefill cost

Cache awareness has two deliberately separate effects:

```text
KV fit and reservation             = complete estimated input tokens
long-input admission class         = complete estimated input tokens
recent Prefill compute estimate    = complete input * bounded cold fraction
```

A prefix-cache hit avoids part of Prefill compute, but the reused prefix is
locked/non-reclaimable while the request runs and therefore still consumes KV.
Consequently cache hit credit must never reduce per-request KV reservation,
post-admit KV projection, maximum-input validation, or the
regular/exclusive/quiescent request class. It may only reduce the request's
aggregate Prefill contention cost inside the existing class.

PIG does not inspect the exact request prefix and must not present a recent
global hit rate as a per-request fact. The observation is a short-lived,
bounded workload estimate. No cache credit is available on the first sample,
after a counter reset, after backend/model/engine epoch drift, when metric type
or labels are invalid, when the observation is stale, or when the recent token
denominator is below the minimum evidence threshold. These cases fall back to
fully cold Prefill without closing a healthy backend.

The backend contracts remain independent:

| Backend | Source | Aggregation |
| --- | --- | --- |
| SGLang | `sglang:prefill_effective_tokens_total{mode="input|device_hit|host_hit|storage_hit",priority=""}` counters | maximum duplicated TP/PP view per mode; one DP replica |
| vLLM | `vllm:prefix_cache_queries_total` and `vllm:prefix_cache_hits_total` counters | sum independent `engine` series |

For SGLang, `input` is the uncached Prefill-compute contribution and the hit
modes are cached token contributions; retraction re-counts are excluded by the
backend. The recent hit fraction is:

```text
hits_delta / (input_delta + hits_delta)
```

For vLLM, the token-level recent hit fraction is:

```text
hits_delta / queries_delta
```

PIG validates `counter` TYPE, finite monotonic values, backend-specific labels,
and `0 <= hits <= total`. The observation component owns only bounded counter
state and produces a conservative cold fraction. Admission owns request
classification, KV reservation, TPS policy, and the final decision. This keeps
the backend adapters, observation policy, and admission policy separately
testable.

The initial implementation must remain intentionally small: one previous
counter snapshot plus one bounded recent observation, no learned state, no
prefix table, no customer cardinality, and no extra upstream request. A longer
window or stronger cache credit may only be introduced after candidate/live
evidence shows that the 500 ms observation cadence is too noisy without causing
low-flow self-lock or workload-transition under-protection.

## 4. Tests And Evidence

Required focused tests:

- equality case remains valid;
- a positive protected/session gap is valid and common used KV includes it;
- active used above derived non-reclaimable KV is invalid;
- TP disagreement, partial dynamic families, wrong types, invalid page geometry,
  and mixed backend signatures remain invalid;
- vLLM accepts the six upstream-declared types and rejects every missing or
  wrong declaration;
- vLLM DP engine series retain sum/maximum/unique-geometry aggregation without
  SGLang label rules;
- SGLang and vLLM accept only their own cache metric TYPE, labels, and
  aggregation contracts;
- first sample, missing metrics, low evidence, stale observation, counter reset,
  backend epoch change, and invalid ratios produce zero cache credit;
- a cache observation can reduce only aggregate Prefill compute cost while KV
  fit, KV reservation, maximum input, and long-input class remain byte-for-byte
  unchanged;
- exclusive and quiescent requests cannot be downgraded by a high recent global
  cache hit rate;
- cold traffic following hot traffic does not bypass the long-input safeguards,
  and idle/low-flow intervals do not self-lock admission;
- all backend observer and startup fixtures remain green.

Required f563 gates at the exact executable commit:

```text
git diff --check
gofmt check
focused Prometheus and server tests
go test ./...
go test -race ./...
go vet ./...
go build
deterministic request-aware and TPS simulations
hot-path and 4-MiB request benchmarks
```

Run source gates in an isolated temporary workbench on f563. Build and smoke one
host-local candidate image only after the source matrix passes. Validate it
against the existing SGLang with an isolated candidate PIG, then validate
shadow, default enforce, protection visibility, drain recovery, no low-flow
lock, runtime/OCI identity, and zero backend restart. Do not restart or rebuild
SGLang, HAProxy, or the CVM. Upload that exact image only after three review
passes accept it.

Candidate monitoring must run continuously for every source/image revision,
not only after the final deployment. Record at least admission outcomes and
reasons, predicted full/cold Prefill tokens, cache observation validity and hit
fraction, KV used/available/evictable plus reservations, running/waiting,
generation TPS, TPS-gate state, retractions/preemptions, upstream errors,
429/503, latency, completion throughput, PIG/backend restart counts, and GPU
utilization when available. Compare time-aligned baseline and candidate windows;
an isolated protection event or TPS dip is acceptable, but sustained QoS loss,
false lock, hidden protection, materially higher preemption, or lower
SLO-compliant goodput is not.

After a production update, observe real traffic for at least 30 minutes and keep
the same monitors active. If evidence exposes a material defect or a clear
throughput opportunity, return to red test -> implementation -> isolated
candidate -> review -> image -> PIG-only update. Repeat until no obvious issue
remains; never optimize from a single low-TPS sample or a single cache-hit
snapshot.

## 5. Production Boundary

The v0.12.14 f563 observation continues as fail-closed evidence while the
correction is developed. Do not hot-patch the production binary.

Before replacing production PIG, switch the live PIG service to the exact
previously proven f563 `0.8.13` image and configuration so normal traffic is not
dependent on the candidate update. First recover and verify that baseline from
the live/committed deployment history: exact image digest, environment, ports,
auth/metrics behavior, health checks, and Router-visible semantics. Do not put
v0.12.x-only variables into the old image or invent an approximate legacy
configuration. Validate authenticated models/chat/stream and protected metrics,
then keep it as the immediate rollback target.

The isolated v0.12.15 candidate must use separate name/port/network state and
must not displace the `0.8.13` traffic path. Only after all candidate gates and
three reviews pass may the production PIG service move from `0.8.13` to the
exact accepted v0.12.15 digest. A failed readiness or live-monitoring gate rolls
PIG back to the verified `0.8.13` configuration. Do not restart the CVM,
SGLang, HAProxy, or rebuild the model service.

The transition itself must remain PIG-only and drain-aware. Snapshot the route
and live Compose first, preserve the exact pre-change files and hashes, verify
that no backend/CVM restart occurred, and never equate Compose acceptance or a
running container with request-path readiness.

## 6. Review Record

### Pass 1: metric model and causality

Status: in progress. SGLang source and the f563 live scrape established the
one-sided KV invariant and selected scheduler-interval decode counter. The live
SGLang scrape also exposes `prefill_effective_tokens_total` as token-level
counters with the required input/device/host/storage modes. vLLM upstream source
established the metric types, per-engine labels, and update sites. Cache metric
source/update semantics and focused red/green evidence are pending.

Exact-version source was resolved to SGLang commit
`c4271c3fe1262fc2adbd162c33b25de5255251c5`. Its
`metrics_collector.py` declares and pre-seeds the four effective Prefill counter
modes; `metrics_reporter.py` publishes effective input as logged input minus
reprocessed input and derives cache hit rate after excluding retraction
re-counts. The f563 scrape confirms only TP0 carries non-zero cumulative values
while the other TP ranks expose pre-seeded zero views, requiring maximum rather
than sum aggregation per mode.

Focused red source `b0d4ab2` was pushed and executed only in the f563 isolated
workbench. The expected parser, Controller cache-cost, vLLM TYPE, and SGLang
KV-gap assertions failed for their intended behavioral reasons. Evidence:

```text
/var/volatile/dstack/persistent/pig-v01215-workbench/red-b0d4ab2.log
sha256 c65474696392d9223d0e38aebf92dc743d65b318ef8608560994bb58af5e1f36
exit 1 (expected red)
```

### Pass 2: safety and lifecycle

Status: pending.

### Pass 3: exact evidence and release

Status: pending. No v0.12.15 image has been built or uploaded, production still
runs the accepted v0.12.14 image, and the exact f563 `0.8.13` rollback
configuration has not yet been reconstructed and revalidated.

Repository deployment evidence identifies the former f563 baseline image as
`ghcr.io/phala-network/phala-inference-guard:v0.8.13@sha256:aec805d6e7bbfd82375199d7950ecfbf6148e501c64822dcb46102a9e24a2ea4`
with the DeepSeek-v4 SGLang backend, 500 ms dynamic polling, global limit 15,
zero queue wait, dynamic TPS red/yellow thresholds, and TTFT disabled. This is
historical reconstruction only; live deployment-history and endpoint
revalidation are still required before it can become the update fallback.
