# PIG v0.12.5 Advanced Configuration

This document separates production configuration from test controls. The
loader accepts bounded overrides so the policy can be tested, but parser
capability does not define what belongs in production Compose.

## Production contract

A normal production deployment contains only:

- `UPSTREAM`, exactly one absolute HTTP URL;
- required authentication and attestation infrastructure;
- a deployment choice that genuinely differs from the v0.12.5 default.

Do not explicitly configure predictive mode, metrics URL, polling, freshness,
the KV hard ratio, or Prefill boundaries when the defaults are intended.
The enforce artifact must prove default behavior with
`PREDICTIVE_ADMISSION_MODE` absent.

Test and production manifests are separate artifacts. Generate production from
the fresh live Compose and immutable image digest; do not promote a test
manifest by only changing its mode. Before deployment, audit the effective PIG
environment. Every explicit `PREDICTIVE_*` value must differ from the v0.12.5
default and have a target-specific operational reason. Shadow may additionally
set `PREDICTIVE_ADMISSION_MODE=shadow`; enforce must omit it.

## Core infrastructure

| Variable | Default | Meaning |
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

## Version defaults

These values are part of the v0.12.5 behavior and should normally remain absent
from production Compose.

| Variable | Default | Constraint |
| --- | --- | --- |
| `PREDICTIVE_ADMISSION_MODE` | `enforce` | Only `shadow` or `enforce`; retired proxy-only values fail startup |
| `PREDICTIVE_METRICS_URL` | derived `${UPSTREAM_ORIGIN}/metrics` | One absolute HTTP URL |
| `PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS` | `10000` | `1..300000` |
| `PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS` | `500` | Not greater than startup timeout |
| `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS` | `500` | Positive, at most 60000 |
| `PREDICTIVE_MAX_METRICS_AGE_MS` | `3 x poll`, normally `1500` | At least one poll, at most 60000 |
| `PREDICTIVE_KV_HARD_RATIO` | `0.88` | Less than 1 |
| `OUTPUT_TOKEN_FIELD_NAMES` | standard OpenAI output-limit fields | Unique supported JSON field names |

The scanner body ceiling (4 MiB) and scanner concurrency are internal bounded
defaults. The ceiling covers a model-neutral 650K-token text window at the
estimator's six-byte ratio without making body size another production knob.
Unit and integration tests may inject typed values directly without turning
them into production environment variables.

## Startup-derived capability

The startup probe requires coherent vLLM metrics for:

- one served-model identity;
- exact KV capacity in tokens;
- KV block size;
- used KV, running, waiting, generation, and preemption counters.

PIG then performs at most one bounded read-only `/v1/models` request and freezes
one immutable capability profile. It never sends a completion, warmup, or active
performance probe. The automatic profile is derived as:

```text
effective_span = block_align_down(min(max_model_len, kv_hard_limit))
regular        = block_align_down(min(64 Ki,  effective_span / 8))
exclusive      = block_align_down(min(256 Ki, effective_span / 2))
quiescent      = block_align_down(min(512 Ki, effective_span))
aggregate      = exclusive
```

If `/v1/models` metadata is unavailable or inconsistent, `max_model_len` is
replaced by a bounded 512 Ki-token fallback and the reason is exposed as
`metadata_fallback`. Backend running/waiting state does not change this profile.
KV and Prefill parameters are initialized once and are not learned during
service.

Explicit Prefill overrides are available only as a controlled test/deployment
exception:

```text
PREDICTIVE_PREFILL_REGULAR_TOKENS
PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS
PREDICTIVE_PREFILL_QUIESCENT_TOKENS
PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS
```

All four must be omitted, or all four must be positive and satisfy:

```text
regular < exclusive < quiescent
exclusive <= aggregate <= quiescent
```

## Test matrix rules

Builder tests and Router-disabled experiments may explicitly configure cadence,
freshness, the KV hard ratio, Prefill bounds, metrics URL, and mode. Each result
must include the exact override set and archive hash.

Shadow is observation-only. It cannot:

- reject a request;
- reserve capacity;
- publish reduced Router capacity;
- mutate application bytes or business headers.

Enforce owns atomic check-and-reserve. A request either receives one reservation
before forwarding or is protected before the upstream call. Forward failure,
completion, upstream error, timeout, cancellation, disconnect, panic, reset,
and shutdown all have an exact-once terminal path.

## Observer failure semantics

An individual failed or incomplete scrape does not invalidate immutable
capability. PIG retains the last coherent snapshot until
`PREDICTIVE_MAX_METRICS_AGE_MS`; after that, enforce closes intake until a
coherent sample returns.

An explicit served-model, KV-capacity, or block-size change, or a generation or
preemption counter reset, invalidates the capability epoch. The old adapter
cannot reopen; reconstruction is required so old reservations cannot be reused
against a new backend epoch.
