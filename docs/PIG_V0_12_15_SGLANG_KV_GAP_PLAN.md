# PIG v0.12.15 Backend Metrics Contract Correction Plan

## 1. Objective

Correct two backend-adapter contract defects discovered after v0.12.14:

- accept the SGLang protected/session-held KV gap while charging the whole
  non-reclaimable gap to admission;
- require vLLM gauges and counters to have the types declared by vLLM itself.

Do not change request estimation, prefill policy, TPS policy, reservations, or
Router compatibility behavior.

The correction must preserve QoS-constrained throughput: count all
non-reclaimable full-pool slots against admission, accept valid
protected/session-held KV gaps, and reject a torn scrape whenever accepting it
could understate non-reclaimable KV.

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

## 5. Production Boundary

The v0.12.14 f563 observation continues as fail-closed evidence while the
correction is developed. Do not hot-patch the production binary. A v0.12.15
production replacement requires the same PIG-only drain, rollback, identity,
readiness, and live-traffic gates used for v0.12.14. Do not restart the CVM,
SGLang, HAProxy, or rebuild the model service.

## 6. Review Record

### Pass 1: metric model and causality

Status: in progress. SGLang source and the f563 live scrape established the
one-sided KV invariant and selected scheduler-interval decode counter. vLLM
upstream source established the metric types, per-engine labels, and update
sites. Focused red/green evidence is pending.

### Pass 2: safety and lifecycle

Status: pending.

### Pass 3: exact evidence and release

Status: pending. No v0.12.15 image has been built or uploaded, and production
still runs the accepted v0.12.14 image.
