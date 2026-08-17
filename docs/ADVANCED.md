# PIG Advanced Configuration

This document describes the `PIG-v0.12.13` source contract. The assigned source
identity does not by itself imply an accepted registry image or production
deployment. The loader exposes bounded overrides for controlled tests, but
parser capability does not define what belongs in production Compose.

## Production contract

A normal production deployment contains only:

- `UPSTREAM`, exactly one absolute HTTP URL;
- required authentication, TLS, and attestation infrastructure; and
- a target-specific choice that genuinely differs from the accepted image
  default.

Do not explicitly configure predictive mode, metrics URL, polling, freshness,
the KV hard ratio, model length, or Prefill bounds when the accepted defaults
are intended. Production enforce mode is proved with
`PREDICTIVE_ADMISSION_MODE` absent. Shadow is a test-only override:

```text
PREDICTIVE_ADMISSION_MODE=shadow
```

Test and production manifests are separate artifacts. Never promote a test
manifest by changing only its mode. Before deployment, compare the candidate
with a fresh live Compose and audit every explicit `PREDICTIVE_*` value.

## Core infrastructure

| Variable | Source default | Meaning |
| --- | --- | --- |
| `LISTEN` | `:8000` | PIG listen address |
| `UPSTREAM` | `http://backend:8000` | The only upstream base URL |
| `TOKEN` | empty | Bearer token; setting it enables API authentication by default |
| `PIG_PATHS` | three OpenAI generation paths | Paths that use predictive admission |
| `API_AUTH_ENABLED` | true when `TOKEN` is set | Require bearer authentication |
| `API_AUTH_PATHS` | `PIG_PATHS` | Authenticated generation paths |
| `PROXY_TIMEOUT_SECONDS` | `1800` | End-to-end upstream timeout |
| `PIG_STATUS_LOG_INTERVAL_SECONDS` | `5` | Status log interval; `0` disables periodic lines |
| `UPSTREAM_ERROR_CLASSIFICATION_ENABLED` | `true` | Preserve the bounded upstream error classifier |

Attestation infrastructure remains configurable with `ATTESTATION_ENABLED`,
`ATTESTATION_DSTACK_ENDPOINT`, `TLS_CERT_PATH`, `ATTESTATION_GPU_ARCH`, the
NVIDIA evidence command/payload settings, and their timeout. These values do
not alter admission policy.

## Predictive source defaults

| Variable | Source default | Constraint |
| --- | --- | --- |
| `PREDICTIVE_ADMISSION_MODE` | `enforce` | Only `shadow` or `enforce` |
| `PREDICTIVE_METRICS_URL` | derived `${UPSTREAM_ORIGIN}/metrics` | One absolute HTTP URL |
| `PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS` | `10000` | `1..300000` |
| `PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS` | `500` | `1..60000`, not greater than startup timeout |
| `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS` | `500` | `1..60000` |
| `PREDICTIVE_MAX_METRICS_AGE_MS` | `3 x poll`, normally `1500` | At least one poll, at most `60000` |
| `PREDICTIVE_KV_HARD_RATIO` | `0.88` | Strictly between 0 and 1 |
| `PREDICTIVE_TPS_REFERENCE` | `0` (disabled) | Finite output tokens/s/active Decode sequence in `[0, 1000000]` |
| `OUTPUT_TOKEN_FIELD_NAMES` | standard OpenAI output-limit fields | Unique supported JSON field names |

The 1500-ms value is observation freshness, not a post-rejection hold. A
current canonical probe recomputes Router-visible capacity on every inspection;
the last-reject timestamp is telemetry only.

`PREDICTIVE_TPS_REFERENCE` is not an upstream capability override. Set it only
when the deployment has a real long-run per-user Decode TPS requirement, for
example an OpenRouter ranking floor. A positive value enables the fixed
60-second sustained-TPS controller; zero or omission leaves the TPS gate
disabled. The source-owned semantics are four qualified samples, eight
qualified sequence-seconds, five-percent healthy headroom, and at most one
base-plus-one exploration sequence whose fixed-rate counterfactual remains at
least 95 percent of the reference. These are algorithm constants, not extra
production YAML values.

The sequence projection counts backend running and waiting plus only those
local reservations newer than the latest observation watermark. The next
covering poll absorbs that extra contribution. This prevents a concurrent
same-poll burst from bypassing a learned limit while avoiding a permanent
double count after vLLM exposes the request.

Before the window has four qualified samples and eight qualified
sequence-seconds, TPS warming admits at most two total sequences. If PIG starts
while more than two are already running, it preserves that population but does
not add to it. This bounded warming step supplies initial batching evidence
without a special recurring one-to-two exception or an unlimited cold-start
burst. Once the window is ready, only the rate-derived envelope applies.

The window counts positive generation even when a short request begins and
finishes between polls. A zero-generation interval counts as a Decode stall
only when a PIG-owned reservation is known to be in active Decode; a pure
Prefill/idle interval does not fabricate TPS zero. Runtime counter reset clears
the window. Ordinary drain immediately permits a one-sequence probe without a
sticky hold or cooldown.

The scanner body ceiling (4 MiB) and scanner concurrency (64) are internal
bounded defaults. The body ceiling covers a model-neutral 650K-token text
window at the estimator's six-byte upper ratio without making body size another
production knob.

## Startup-derived capability

The startup probe requires one coherent vLLM metric identity and exact values
for KV capacity, KV block size, used KV, running, waiting, generation tokens,
and preemptions. PIG then performs at most one bounded read-only `/v1/models`
request. Automatic initialization succeeds only when the response contains
exactly one model, its ID matches the metric identity, and `max_model_len` is
positive. Missing, ambiguous, or inconsistent metadata fails startup; there is
no geometry fallback.

The immutable automatic profile is:

```text
kv_hard_limit = block_align_down(kv_capacity * 0.88)
aligned_total = block_align_down(min(max_model_len, kv_hard_limit))
maximum_admissible_input = aligned_total - 256 decode-horizon tokens

regular boundary   = block_align_down(64 Ki tokens)
exclusive boundary = block_align_down(256 Ki tokens)
quiescent boundary = block_align_down(512 Ki tokens)
contended budget   = block_align_down(min(64 Ki, maximum_admissible_input))
aggregate budget   = block_align_down(min(256 Ki, maximum_admissible_input))
```

The fixed 64K/256K/512K request bands are portable workload classes, not
learned rates and not fractions of context length. `maximum_admissible_input`
is the hard per-request input ceiling. A shorter model may therefore never
reach a larger class even though the class boundary remains fixed.

PIG sends no completion, warmup, cache query, or performance probe during
initialization. Backend busy/idle state does not change the profile. KV and
Prefill parameters are frozen for the Controller lifetime and are never
learned. Ordinary observation polls do not repeat metadata I/O. When monotonic
counters indicate a vLLM runtime reset, an automatically initialized profile
performs one bounded `/v1/models` revalidation before publishing the reset
sample; failure leaves the old observation to age stale, and a changed
`max_model_len` closes capability availability.

## Explicit capability override

Controlled tests may bypass `/v1/models` only by setting all five values:

```text
PREDICTIVE_MAX_MODEL_LEN_TOKENS
PREDICTIVE_PREFILL_REGULAR_TOKENS
PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS
PREDICTIVE_PREFILL_QUIESCENT_TOKENS
PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS
```

All five must be omitted or all five must be positive. Before block alignment,
they must satisfy:

```text
regular < exclusive < quiescent
regular <= aggregate <= quiescent
```

The explicit model length is still combined with observed KV geometry and the
256-token Decode horizon to derive the hard maximum input. Partial overrides
fail configuration validation. These values are an auditable test/deployment
exception, not a recommended production manifest.

## Test matrix rules

Builder tests and Router-disabled experiments may explicitly configure cadence,
freshness, the KV hard ratio, the complete capability override, metrics URL,
and mode. Each result must include the exact override set and source commit.

Shadow is observation-only. It cannot:

- reject a request;
- reserve capacity;
- publish reduced Router capacity; or
- mutate application bytes or business headers.

Enforce owns atomic check-and-reserve. A request receives one reservation
before forwarding or is protected before the upstream call. Forward failure,
completion, upstream error, timeout, cancellation, disconnect, panic, reset,
and shutdown must converge on an exact-once terminal path.

## Observer failure semantics

An individual failed or incomplete scrape does not invalidate the immutable
capability. PIG retains the last coherent snapshot until
`PREDICTIVE_MAX_METRICS_AGE_MS`; after that, enforce closes intake until a
coherent sample returns.

An explicit served-model, KV-capacity, or block-size change, or a generation or
preemption counter reset, invalidates the capability epoch. The old adapter
cannot reopen; reconstruction is required so reservations from the old epoch
cannot authorize work against a different backend.
