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
24-hour/60-second contract. `runtime_integrity.integrity_eligible` means only
that collected evidence is internally coherent. The stronger
`checkpoint.formal_checkpoint_eligible` also requires the horizon's duration,
sample count, and cadence; a healthy partial window cannot pass early.

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
