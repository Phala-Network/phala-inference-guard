# SGLang PD Decode compatibility review

Base: `ef0035cb60ef05568c2d2663d3881843ec25d16d` (v0.12.29).
Derivative: `v0.12.29-sglang-pd-r1`.
Task: DeepSeek-V4.1-Flash .105, single Decode TP group behind a PD gateway.

## Pass 1 - Model and causality

The baseline rejects `engine_type="decode"`. Focused remote red tests reproduced
invalid model identity at both parser and startup boundaries. The new parser
admits only coherent unified or Decode roles, deduplicates TP copies, and adds
the two disjoint Decode waiting stages to scheduler waiting. The same parsed
sample is used by startup and the live observation path. No new TPS algorithm,
request-token predictor, KV safety gate, Prefill policy or TTFT guarantee is
introduced. Forwarding still targets the gateway, while observation targets D.

## Pass 2 - Safety and lifecycle

The role-specific Decode observation identity prevents reusing controller
history after a same-model unified/Decode switch. Mixed roles/models/DP ranks,
negative/fractional/nonfinite queues, priority-only totals and malformed labels
are rejected. The first green pass exposed duplicate-label overwrite in the
existing label parser; it was corrected rather than dropping the failing test.
Queue sums are overflow-checked. Cache-hit accounting stays unknown on Decode.
The reservation, first-byte, cancellation and stale-metrics algorithms are
unchanged and retain their existing lifecycle/race regression coverage.

## Pass 3 - Evidence and release boundaries

Source tests run in task-owned Linux containers capped at 2 CPU and 2 GiB,
without GPUs. Evidence retains baseline red, first-green failure and corrected
focused/full/race/static results, bound to source archive hashes. Executable
bytes in the clean release archive must match the successful test tree.

Publication requires two fresh no-cache BuildKit workers, byte-identical runtime
config/layers/platform manifest, installed-binary PD startup/queue/stale/role
drift/auth/nonstream/SSE tests, immutable OCI annotations, SPDX and max provenance,
referrer inspection and anonymous registry read-back. These later gates are
recorded in task release artifacts, not asserted passed by this source document.
No model-load benchmark is performed by the release runner. Public .105 remains
in maintenance until actual PIG and HTTPS functional acceptance.
