# Backend Metric Source Contract

This operator contract prevents vLLM and SGLang metrics from being treated as
interchangeable. The authoritative implementation is
`internal/infra/prometheus/sample_vllm.go` and `sample_sglang.go`; the observer
must record the selected family and source names with every artifact.

| Semantic field | vLLM native source | vLLM rule | SGLang native source | SGLang rule |
| --- | --- | --- | --- | --- |
| model identity | `model_name` on the core admission families | one exact value | `model_name` on static and present dynamic admission families | one exact value for one `engine_type="unified"` scheduler and one DP replica |
| KV capacity/block | `vllm:cache_config_info` labels `kv_cache_size_tokens` and `block_size` | gauge; one coherent geometry; do not multiply by engine count | `sglang:max_total_num_tokens`, `page_size`, `num_pages` | gauges; unique values; `num_pages == floor(capacity/page_size)` |
| used KV | `vllm:kv_cache_usage_perc` | gauge; maximum engine ratio times capacity | `capacity - kv_available_tokens - kv_evictable_tokens` | gauges; charges protected/session-held gap; direct `kv_used_tokens` is only a lower-bound consistency check |
| running | `vllm:num_requests_running` | gauge; sum engines | `sglang:num_running_reqs{priority=""}` | gauge; maximum duplicate TP/PP view, never sum priority subsets |
| waiting | `vllm:num_requests_waiting` | gauge; sum engines | `sglang:num_queue_reqs{priority=""}` | same deduplication rule as running |
| realtime Decode tokens | `vllm:generation_tokens_total` | counter; sum engines | `sglang:realtime_tokens_total{mode="decode",priority=""}` | counter; maximum duplicate TP/PP view |
| preemptions | `vllm:num_preemptions_total` | counter; sum engines | `sglang:num_retracted_requests_total{priority=""}` | counter; maximum duplicate TP/PP view; never substitute resettable `num_retracted_reqs` or paused requests |
| aggregate cache queries | `vllm:prefix_cache_queries_total` | counter; sum matching engines | `sglang:prefill_effective_tokens_total{mode="input"}` plus all hit modes | counter; maximum duplicate rank view for the one scheduler |
| aggregate cache hits | `vllm:prefix_cache_hits_total` | counter; sum matching engines | `device_hit + host_hit + storage_hit` from `sglang:prefill_effective_tokens_total` | counter; same scheduler/DP/priority identity as queries |
| runtime epoch | `process_start_time_seconds` | positive gauge when present; counter rollback also detects reset | `process_start_time_seconds` | same |

For both backends, realtime Decode-token counters are raw scheduler work. They
are suitable for the controller's aggregate rate evidence but are not by
themselves successful completion goodput. A completion-goodput conclusion
requires successful-request linkage to output tokens; otherwise the analyzer
must return it as unavailable.

The current `cvm-311bbcdb-live-observe.py` CSV is explicitly vLLM-only. Its
columns retain a `vllm_` prefix and map one-for-one to the vLLM sources above.
An SGLang collector must use a declared SGLang schema or framework-neutral
semantic columns plus an immutable source manifest. It must never populate a
`vllm_` column from an `sglang:*` metric.
