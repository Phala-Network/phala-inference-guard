# PIG Observation Analysis

`analyze_window.py` analyzes fixed-interval serving evidence without treating a
counter reset, container restart, or missing scrape as a healthy zero. It is an
operator tool and is not linked into the PIG request path or production image.

```text
python3 tools/observe/analyze_window.py \
  --horizon stability samples.csv summary.json
```

The explicit horizon is mandatory: `release` applies the 30-minute/5-second
contract, `stability` the 6-hour/30-second contract, and `delayed` the
24-hour/60-second contract. The legacy
`runtime_integrity.integrity_eligible` field remains the strict all-surface
integrity gate: any incomplete PIG, backend, Router, GPU, or container sample
keeps it false. The stronger `checkpoint.formal_checkpoint_eligible` also
requires the horizon's duration, sample count, and cadence; a healthy partial
window cannot pass early.

`component_integrity` prevents a Router-only collection failure from being
misreported as a PIG/backend runtime failure without weakening the strict
gate. `runtime_service` requires continuous PIG, backend, GPU, container,
identity, restart/OOM, and critical-counter evidence. `matched_routing`
inherits those requirements and additionally requires complete Router scrapes
and Router counter continuity. The legacy CSV does not collect a Router config
digest, so it reports `router_identity_status=not_collected` and an explicit
note; use the paired snapshot Router identity gate before making a matched
traffic claim. If a future CSV supplies `router_config_digest`, an incomplete
or changing value invalidates `matched_routing` and the strict all-surface
gate, while an otherwise continuous `runtime_service` result remains usable.

The analyzer deliberately reports backend generation tokens as raw generation
throughput. The observer CSV has no success-linked output-token counter, so the
tool leaves successful completion goodput unavailable instead of substituting a
weaker metric.

Counter deltas are accumulated only across adjacent, source-valid samples with
the same component identity. Missing scrapes are never bridged. Reset and
identity-change intervals are excluded and reported. A formal checkpoint is
ineligible when samples are incomplete, the sampling cadence has a material
gap, runtime identity is absent or changes, a container is not running or
restarts, a critical counter resets or is absent, or an OOM flag is observed.

The current CSV format is the vLLM production collector format. Native names
are mapped explicitly: `vllm:generation_tokens_total` is raw generated tokens,
`vllm:num_preemptions_total` is preemption count,
`vllm:num_requests_running`/`waiting` are scheduler gauges,
`vllm:kv_cache_usage_perc` is KV occupancy, and
`vllm:prefix_cache_{queries,hits}_total` provide aggregate cache evidence.
They must not be substituted for SGLang names. A future SGLang collector must
map its native `sglang:*` metrics into a separately declared schema and retain
the source-name manifest; this analyzer does not guess the backend family.
See `BACKEND_METRIC_SOURCES.md` for the exact cross-backend mapping and
aggregation contract.

The over-protection screen requires offered Router demand, load-scoped PIG
backpressure, a ready above-reference TPS window, zero backend waiting, low KV,
and GPU utilization below 40 percent. It is only a screening signal; it does
not prove that a particular protected request would fit.

Run the standard-library tests with:

```text
python3 -m unittest discover -s tools/observe -p '*_tests.py' -v
```

## Paired snapshot analysis

`analyze_paired_snapshots.py` compares immutable start and end captures for the
current target and a comparator over the same UTC interval:

```text
python3 tools/observe/analyze_paired_snapshots.py \
  START_CAPTURE END_CAPTURE OUTPUT.json
```

Each capture directory must contain `manifest.json`, `target-pig.prom`,
`target-backend.prom`, `comparator-combined.prom`, and `router.json`. When a
`SHA256SUMS` file is present, every required source must be listed and must
match. The analyzer also computes its own SHA-256 for every source and writes a
SHA-256 sidecar for the deterministic JSON result.

The manifest is retained verbatim as `raw_manifest`. Derived identity is a
separate field: current PIG exposes `pig_info{version=...}`, while legacy PIG
uses `pig_version_info{version=...}`. An empty historical
`target_pig_version` is therefore corrected in derived provenance without
altering the original evidence. A capture script must check both metric names;
the immutable image tag is only a fallback when neither version metric exists.

Counter deltas require exactly matching metric and label identities and a
stable backend epoch. Any missing series, rollback, PIG/backend identity
change, Compose drift, or recorded-hash mismatch makes the affected evidence
unavailable. Histograms additionally require one bucket schema across all
series, monotonic start/end cumulative buckets, monotonic bucket deltas, and
agreement between `+Inf` and `_count`. Quantiles are interpolated only within
finite buckets; a quantile landing in `+Inf` is reported as lower-bounded.

Target-only predictive tables include protection reason/scope, TPS result and
subreason, denominator source, input-size outcome buckets, streaming shape,
and declared-versus-actual output buckets. Zero-delta label combinations are
counted but omitted from the changed-series listing to keep the artifact
usable for human review. Legacy comparator fields that are not exported are
reported as `unavailable/not_exported`, never as zero.

`runtime_integrity_eligible` covers PIG/backend/Compose evidence.
`matched_routing_eligible` independently requires a stable Router config
identity, enabled target and comparator routes, continuous Router counters,
and available `processed`, `upstream_attempts`, and `upstream_429` evidence.
Any exported Router counter rollback proves a reset and invalidates the matched
traffic interval even when the config digest is unchanged. A Router update can
therefore invalidate a matched traffic claim without discarding an otherwise
valid backend stability observation. Raw
generation and prompt rates, request completion counts, cache share, and
histograms are reported, but `successful_completion_goodput` remains
unavailable because vLLM does not link generated-token sums to
`finished_reason`. Even when the counter evidence is technically comparable,
the target/comparator ratio is marked `descriptive_only` until demand, cache,
input, and output cohorts satisfy the plan's matching contract; it is not a
causal PIG effect estimate.
