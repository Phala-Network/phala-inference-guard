# PIG Advanced Configuration

This document describes the current TPS-health admission contract. Source,
builder evidence, images, deployment, and live observations are separate
evidence layers.

## Production contract

A normal production deployment sets one upstream, credentials and attestation
infrastructure, and a real business TPS reference when required:

```text
UPSTREAM=http://backend:8000
TOKEN=...
PREDICTIVE_TPS_REFERENCE=25
```

Do not repeat source defaults in production Compose. Admission defaults to
`enforce`, metrics polling defaults to 500 ms, freshness defaults to three
polls, and same-observation window concurrency defaults to 32 Decode sequences.
`shadow` is an explicit test-only override.

The public surface is fixed to `POST /v1/chat/completions`,
`POST /v1/completions`, `POST /v1/responses`, and `GET /v1/models`. Generation
routes use admission; model discovery does not. Unknown or non-canonical paths
and method mismatches return a local generic OpenAI-shaped 404 without a
backend call.

## Core infrastructure

| Variable | Default | Meaning |
| --- | --- | --- |
| `LISTEN` | `:8000` | Listen address |
| `UPSTREAM` | `http://backend:8000` | Exactly one upstream base URL |
| `TOKEN` | empty | Bearer token; setting it enables auth by default |
| `API_AUTH_ENABLED` | true when `TOKEN` is set | Require bearer auth |
| `PROXY_TIMEOUT_SECONDS` | `1800` | End-to-end upstream timeout |
| `PIG_STATUS_LOG_INTERVAL_SECONDS` | `30` | Compact status cadence; `0` disables it |
| `PIG_LOG_LEVEL` | `info` | `debug` adds bounded decision details |
| `UPSTREAM_ERROR_CLASSIFICATION_ENABLED` | `true` | Bounded upstream error classification |

Attestation variables do not alter admission policy.

## Admission policy

| Variable | Default | Constraint and meaning |
| --- | --- | --- |
| `PREDICTIVE_ADMISSION_MODE` | `enforce` | `shadow` or `enforce` |
| `PREDICTIVE_METRICS_URL` | `${UPSTREAM_ORIGIN}/metrics` | One absolute HTTP URL |
| `PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS` | `10000` | `1..300000` |
| `PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS` | `500` | `1..60000`, not above startup timeout |
| `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS` | `500` | `1..60000` |
| `PREDICTIVE_MAX_METRICS_AGE_MS` | `3 x poll` | At least one poll, at most `60000` |
| `PREDICTIVE_TPS_REFERENCE` | `0` | Finite Decode tokens/s/active sequence in `[0,1000000]`; `0` disables TPS health |
| `PREDICTIVE_WINDOW_CONCURRENCY` | `32` | New unobserved Decode sequences per observation window in `[1,1048576]` |
| `PREDICTIVE_RUNNING_LIMIT` | `0` | Backend running ceiling in `[0,1048576]`; `0` means unknown/disabled |

TPS is a health signal, not a capacity formula. The gate stays open while
warming or when the latest interval has no reliable Decode denominator. It
protects on waiting, a fresh preemption, or when both the ready rolling mean and
latest qualified mean are below the reference. One low interval does not close
a healthy rolling window, and one qualified recovered interval reopens a low
rolling window immediately.

The independent admission bounds are:

```text
projected_running = observed_running + observed_waiting
                  + unobserved_sequences + request_decode_sequences

projected_window = unobserved_sequences + request_decode_sequences
```

A known running limit rejects when `projected_running` exceeds it. The window
bound rejects when `projected_window` exceeds its configured value. Both checks
and the complete request-fanout reservation occur under the same controller
lock. PIG returns 429 immediately and does not queue.

vLLM standard metrics expose current running but no trusted configured
`max_num_seqs`; PIG therefore leaves its running limit unknown unless explicitly
configured. SGLang may initialize the limit from a bounded, same-origin
`/server_info` response only when the top-level `max_running_requests` is a
coherent positive integer. Missing, malformed, inconsistent, timed-out, or
out-of-range data leaves the limit unknown. Environment and admin values take
precedence. No value is learned from traffic.

The bounded request scan reads only protocol shape needed for Decode fanout.
Input size, declared output length, token counts, cache, KV, Prefill, and TTFT do
not enter admission. Scanner-unavailable requests use one labelled fallback
sequence through the same atomic policy; malformed or unsafe fanout is
request-scoped protection.

## Runtime policy API

`GET` and `PATCH /admin/v1/predictive-policy` always require the configured
bearer token. GET returns schema `pig.predictive-policy.v1`, revision, source,
update time, mutable values, running-limit source, and non-secret observer
settings.

PATCH supplies `expected_revision` and at least one mutable field:

```json
{
  "expected_revision": 7,
  "tps_reference": 25,
  "window_concurrency": 40,
  "running_limit": 256
}
```

Omitted mutable fields are preserved. The update is atomic; unknown fields,
trailing JSON, bodies over 4096 bytes, stale revisions, and invalid values fail
without a partial change. Only a changed TPS reference clears the TPS window.
Changing either bound does not rewrite TPS evidence. An admin running-limit
change reports source `admin`; setting it to zero disables that gate. Restart
restores the validated initialization policy and revision 1.

## Failure semantics

One failed scrape retains the last coherent observation until its freshness
deadline. Missing, stale, identity-invalid, or corrupt state closes enforce
intake. Counter rollback or backend restart advances the runtime epoch, clears
incompatible TPS evidence and reservations, and fences old handles.

Forward failure, completion, upstream error, timeout, cancellation, disconnect,
panic, reset, and shutdown converge on one terminal lifecycle transition.
Shadow records counterfactual decisions but returns neither predictive 429 nor
Router backpressure.

## Retired settings

Context/KV/cache/Prefill/input-size/TTFT gates, tiers, priority injection,
request rewriting, queue waits, learned capacity, and the old TPS-derived
sequence-limit/QoS-budget controller are removed. Historical variables such as
`GLOBAL_LIMIT`, `DYNAMIC_*`, `QOS_QUEUE_*`, `KV_ADMISSION_*`,
`BACKEND_PRIORITY_*`, `PREDICTIVE_KV_*`, and `PREDICTIVE_PREFILL_*` are ignored
and should be removed from Compose.
