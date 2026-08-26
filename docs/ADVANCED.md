# PIG Advanced Configuration

This document describes the current TPS-only configuration contract. Source,
tests, images, deployment, and live observations remain separate evidence
layers; a development branch is not a released image.

## Production contract

A normal production deployment contains only:

- `UPSTREAM`, exactly one absolute HTTP URL;
- required bearer, TLS, and attestation infrastructure; and
- `PREDICTIVE_TPS_REFERENCE` when the deployment has a real long-run Decode
  TPS target.

Do not spell out source defaults in production Compose. Predictive admission is
`enforce` by default, metrics are derived from the upstream origin, and the
observer polls every 500 ms. `shadow` is a test-only override:

```text
PREDICTIVE_ADMISSION_MODE=shadow
```

The public forwarding surface is fixed. PIG accepts only
`POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/responses`, and
`GET /v1/models`. The generation routes use admission; model discovery does
not. Unknown paths and method mismatches terminate locally with a generic
OpenAI-shaped 404 and never reach the backend.

## Core infrastructure

| Variable | Source default | Meaning |
| --- | --- | --- |
| `LISTEN` | `:8000` | PIG listen address |
| `UPSTREAM` | `http://backend:8000` | The only upstream base URL |
| `TOKEN` | empty | Bearer token; setting it enables API authentication by default |
| `API_AUTH_ENABLED` | true when `TOKEN` is set | Require bearer authentication |
| `PROXY_TIMEOUT_SECONDS` | `1800` | End-to-end upstream timeout |
| `PIG_STATUS_LOG_INTERVAL_SECONDS` | `30` | Compact Controller status interval; `0` disables periodic lines |
| `PIG_LOG_LEVEL` | `info` | `debug` adds bounded decision detail |
| `UPSTREAM_ERROR_CLASSIFICATION_ENABLED` | `true` | Preserve bounded upstream error classification |

Attestation infrastructure remains configurable with `ATTESTATION_ENABLED`,
`ATTESTATION_DSTACK_ENDPOINT`, `TLS_CERT_PATH`, `ATTESTATION_GPU_ARCH`, the
NVIDIA evidence command or payload settings, and their timeout. These values do
not alter admission policy.

## TPS controller

| Variable | Source default | Constraint |
| --- | --- | --- |
| `PREDICTIVE_ADMISSION_MODE` | `enforce` | `shadow` or `enforce` |
| `PREDICTIVE_METRICS_URL` | derived `${UPSTREAM_ORIGIN}/metrics` | One absolute HTTP URL |
| `PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS` | `10000` | `1..300000` |
| `PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS` | `500` | `1..60000`, no greater than startup timeout |
| `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS` | `500` | `1..60000` |
| `PREDICTIVE_MAX_METRICS_AGE_MS` | `3 x poll`, normally `1500` | At least one poll, at most `60000` |
| `PREDICTIVE_TPS_REFERENCE` | `0` | Finite output tokens/s/active Decode sequence in `[0, 1000000]` |
| `OUTPUT_TOKEN_FIELD_NAMES` | standard OpenAI output-limit fields | Unique supported JSON field names |

`PREDICTIVE_TPS_REFERENCE` is a long-run service-quality target, not an
instantaneous threshold or learned backend capability. A positive value enables
the rolling TPS controller. Zero leaves the TPS reference disabled. Short low
samples are evidence for later predictions and do not create a cooldown or a
sticky low-capacity state.

The observer publishes one coherent vLLM or SGLang identity with running,
waiting, generation, preemption, and runtime-epoch signals. TPS admission does
not require KV metrics, prefix-cache metrics, Prefill thresholds, model context
metadata, or a `/v1/models` startup probe. Optional backend metrics may remain
visible to operators, but they are not inputs to the TPS decision.

Waiting or a fresh preemption pauses marginal intake for that observation only.
The first fresh observation with both clear can reopen intake. There is no
cooldown, consecutive-clear requirement, sticky recovery timer, learned low
cap, or model-specific threshold.

The controller still owns a same-snapshot atomic sequence reservation. Backend
running/waiting and locally forwarded sequences are reconciled at the next
observation watermark so concurrent requests cannot spend the same apparent
headroom twice and completed work does not remain permanently double-counted.

## Retired settings

The following environment variables are ignored. They cannot restore the old
Context, KV, input-size, cache-aware Prefill, or long-input gates:

```text
PREDICTIVE_KV_HARD_RATIO
PREDICTIVE_MAX_MODEL_LEN_TOKENS
PREDICTIVE_PREFILL_REGULAR_TOKENS
PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS
PREDICTIVE_PREFILL_QUIESCENT_TOKENS
PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS
```

Remove them from production and test Compose files. Invalid values in these
retired variables do not change loading or validation.

## Runtime policy API

The authenticated process-local policy endpoint is:

```text
GET   /admin/v1/predictive-policy
PATCH /admin/v1/predictive-policy
```

It always requires exactly one `Authorization: Bearer <token>` header. GET
returns schema `pig.predictive-policy.v1`, a monotonic revision, source, update
time, the mutable `tps_reference`, and non-secret mode/poll/freshness values. It
does not return credentials, endpoint URLs, request data, model identity, KV
geometry, or Prefill policy.

PATCH uses compare-and-swap:

```json
{
  "expected_revision": 1,
  "tps_reference": 25
}
```

Unknown or missing fields, trailing JSON, bodies over 4096 bytes, stale
revisions, and values outside finite `[0, 1000000]` fail atomically. A changed
reference clears incompatible TPS-window evidence; existing request leases keep
their exact lifecycle. Restart restores the validated environment value and
revision 1.

## Test rules

Builder tests may override cadence, freshness, metrics URL, TPS reference, and
mode. Each result records the exact source and override set. A test manifest is
not promoted unchanged to production.

Shadow computes the counterfactual policy decision but never returns a TPS 429
or reduces Router capacity. Enforce owns the atomic pre-forward decision and
reservation. Forward failure, completion, upstream error, timeout,
cancellation, disconnect, panic, reset, and shutdown must converge on one
terminal lifecycle transition.

Correctness, race, lifecycle, build, and protocol checks remain required source
evidence. Production samples and historical comparisons guide optimization;
one model, traffic window, rejection percentage, or benchmark number is not a
universal hard acceptance threshold.

## Observer failure semantics

One failed scrape retains the last coherent snapshot until
`PREDICTIVE_MAX_METRICS_AGE_MS`. A stale, missing, or identity-invalid snapshot
closes enforce intake until coherent evidence returns. Generation or preemption
counter rollback advances the runtime epoch, clears incompatible TPS evidence,
and reconciles old leases without requiring KV or model metadata.
