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
polls, and pending-first-byte window concurrency defaults to 32 Decode
sequences. `shadow` is an explicit test-only override.

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
| `HTTP_BODY_READ_TIMEOUT_SECONDS` | `120` | Absolute complete-request read budget, including headers; `1..86400` |
| `HTTP_WRITE_IDLE_TIMEOUT_SECONDS` | `120` | Maximum duration of a pending downstream Write/Flush; `1..86400` |
| `HTTP_IDLE_TIMEOUT_SECONDS` | `60` | Inbound keep-alive idle budget; `1..86400` |
| `PIG_STATUS_LOG_INTERVAL_SECONDS` | `30` | Compact status cadence; `0` disables it |
| `PIG_LOG_LEVEL` | `info` | `debug` adds bounded decision details |
| `UPSTREAM_ERROR_CLASSIFICATION_ENABLED` | `true` | Bounded upstream error classification |

Attestation variables do not alter admission policy.

The request read deadline also covers classification before admission and Go's
automatic drain of an incomplete body on an early local response. It is an
absolute upload limit, not an inactivity timer; completing the upload clears it
and does not limit response/SSE lifetime. The existing header timeout is 30 s.
Write deadlines are active only during writes/flushes and the final server
flush, so a long prefill or silent SSE interval does not consume a write budget.
The existing proxy total timeout still applies, and its cancellation interrupts
an already blocked downstream write. There is no short global `WriteTimeout`.

## Admission policy

| Variable | Default | Constraint and meaning |
| --- | --- | --- |
| `PREDICTIVE_ADMISSION_MODE` | `enforce` | `shadow` or `enforce` |
| `PREDICTIVE_METRICS_URL` | `${UPSTREAM_ORIGIN}/metrics` | One absolute HTTP URL |
| `PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS` | `10000` | `1..300000`; per diagnostic attempt, not an overall startup deadline |
| `PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS` | `500` | `1..60000`, not above startup timeout |
| `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS` | `500` | `1..60000` |
| `PREDICTIVE_MAX_METRICS_AGE_MS` | `3 x poll` | At least one poll, at most `60000` |
| `PREDICTIVE_TPS_REFERENCE` | `0` | Finite Decode tokens/s/active sequence in `[0,1000000]`; `0` disables TPS health |
| `PREDICTIVE_WINDOW_CONCURRENCY` | `32` | Decode sequences pending first byte in `[1,1048576]` |
| `PREDICTIVE_RUNNING_LIMIT` | `0` | Backend running ceiling in `[0,1048576]`; `0` means unknown/disabled |
| `PREDICTIVE_PREFIX_INDEX_TOKENS` | `0` | `[0,1073741824]`; Prompt tokens the prefix mirror holds; `0` sizes it to the backend KV capacity when known |

TPS is a health signal, not a capacity formula. Waiting protection remains
independent of TPS reference. Flex requests are admitted while observed backend
waiting is at or below the mutable `waiting_allowance` (default `3`), subject to
the other guards. A fresh observation above that value protects immediately;
one back at or below it reopens intake. Priority requests bypass waiting,
preemption, the running limit, the window bound and TPS health, because the
backend schedules them ahead of every waiting default and flex request
(below); a warm Prompt passes a default or flex request on a recovered
interval (below).
The tier comes from one `X-User-Tier` value: `priority`, `default` or `flex`;
anything else, including a missing or repeated header, is `default`. Default
and flex are protected alike and differ only in order: default is served first
in the local queue and, with backend tier scheduling, in the backend's. With
TPS health enabled, the gate stays open while warming or
when the latest interval has no reliable Decode denominator. It protects on a
fresh preemption or when both the ready rolling mean and latest qualified mean
are below the reference. One low interval does not close a healthy rolling
window, and one qualified recovered interval reopens a low rolling window
immediately.

**Backend tier scheduling.** When SGLang `/server_info` confirms
`enable_priority_scheduling=true` and `schedule_low_priority_values_first=false`, PIG appends
`"priority": N` as the last member of every admitted JSON body
(`priority` 2, `default` 1, `flex` 0). On any other backend (vLLM reads the
field the other way round, smaller values first) PIG leaves the body alone and
TPS health protects every tier as before; `pig_predictive_backend_tier_scheduling_enabled`
reports which. The backend keeps the last of duplicate
keys, so the value replaces any priority the client sent; the body length is
updated; an unbuffered JSON body uses a constant-memory streaming rewrite and
chunked forwarding, including oversized and scanner-saturated requests. **The
backend must order its waiting queue by that value**: run SGLang with
`--enable-priority-scheduling` (larger values first, the SGLang default), whose
`fcfs` and `lof` schedule policies sort by priority, and with
`--disable-priority-preemption`, since adjacent tiers differ by 1, below
SGLang's preemption threshold, and preempting a running sequence only
recomputes it, and with `--default-priority-value 1` for requests that enter
the backend directly. Non-JSON and unknown-endpoint bodies are left alone;
`pig_predictive_backend_tier_scheduling_requests_total{result="skipped"}` counts
them. A policy that ignores the field (such as `hrrn`) needs a tier-aware
variant. If the backend flags are missing, reversed, disabled or unavailable,
PIG keeps TPS protection for every tier and does not write a priority. With
verified scheduling, priority Prefill runs ahead of every waiting
default and flex request, the rolling Decode mean no longer describes the
service it receives, and TPS health does not protect the priority tier (subreason
`backend_scheduled`). Availability protection (missing, stale or invalid
observations, fail-closed) still applies to every tier. A Prefill chunk the
backend has already started is not interrupted, so a priority request can
still wait for one long cold Prompt in progress.

**Warm and cold Prompts.** PIG keeps an LRU mirror of Prompt prefixes from
completed successful responses with qualified Prompt/cache usage and estimates each request's uncached Prompt tokens from it,
corrected by the cached-token counts the backend reports. At startup PIG reads
the backend's Prefill chunk size (`chunked_prefill_size` in SGLang
`/server_info`, the same probe that discovers the running limit; reported as
`pig_predictive_tps_warm_prompt_tokens`) and, when the backend reports one, TPS
health treats default and flex requests by that estimate while the rolling
window is below the reference; a backend that reports none keeps the plain
reference gate for every tier. A warm Prompt (estimate at most the threshold)
passes on a recovered latest interval (subreason `warm_prompt`): it costs at
most the one-step stall any chunk costs, while turning a continuing
conversation away sends it to another node and brings it back later as a cold
Prefill. A cold Prompt (estimate above the threshold, or none) passes only
while the rolling window mean is at the reference, because a recovered latest
interval is the moment a Prefill chunk ended and says nothing about absorbing
several more chunks (refusal subreason `cold_prompt`), and it is admitted only
while no other default or flex cold Prompt is pending first byte (subreason
`cold_prompt_pending`): the window mean lags the load it admits, so for the
few seconds after it reaches the reference every cold Prompt that arrived
would pass, all Prefill at once, and each one's first byte would wait for all
of them. The backend still Prefills one chunk per step, so admitting cold
Prompts in turn changes their order, not Prefill throughput. When both the
window and the latest interval are below the reference nothing in those tiers
passes. Priority requests are unaffected; waiting, preemption, running and
window protection are unchanged. The estimate can be wrong;
`pig_predictive_warm_prompt_*` reports how many warm admissions the backend
then computed above the threshold.

The mirror stores no prefixes when its backend-derived or explicit capacity
is unavailable or too small for a block. All non-Prompt request fields seed
every prefix hash, so changing tools, model or rendering options cannot reuse
a different context's prefix even when those fields follow the messages.
Sampling changes may conservatively miss. Pending cold Prompts retain their
serialization slot until first byte or termination, independently of the
sequence-capacity reconciliation lease.

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

`unobserved_sequences` is the pending-first-byte lease, not total backend
concurrency. It is released on first byte or any terminal path. A successful
fresh observation may expire a long-TTFT lease after three polling intervals
only when backend waiting is zero. Ordinary polls, failed/invalid/stale
observations, and positive waiting cannot release it. The duration is derived
from polling cadence and is not a separate environment or admin setting.

vLLM standard metrics expose current running but no trusted configured
`max_num_seqs`; PIG therefore leaves its running limit unknown unless explicitly
configured. SGLang may initialize the limit from a bounded, same-origin
`/server_info` response only when the top-level `max_running_requests` is a
coherent positive integer. Missing, malformed, inconsistent, timed-out, or
out-of-range data leaves the limit unknown. Environment and admin values take
precedence. No value is learned from traffic.

When `internal_states` supplies `effective_max_running_requests_per_dp`, the
configured limit is capped by that finalized static request-pool capacity.
Several reported pools use their conservative minimum, not a sum without rank
identity; legacy backends without the field retain the configured limit.
Mixed, invalid or inconsistent pool reports are rejected.

The SGLang probe still reads warm-Prompt and scheduling capabilities with an
explicit `PREDICTIVE_RUNNING_LIMIT`. An explicit startup value of `0` disables
the running gate; a positive value initializes that exact bound. The probe
does not follow redirects or use an environment HTTP proxy.

The bounded request scan reads protocol shape for Decode fanout and fingerprints
for the Prompt-aware TPS estimate. Declared output length, exact KV allocation
and TTFT do not create additional hard capacity gates. Scanner-unavailable requests use one labelled fallback
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
  "running_limit": 256,
  "waiting_allowance": 3
}
```

Omitted mutable fields are preserved. The update is atomic; unknown fields,
trailing JSON, bodies over 4096 bytes, stale revisions, and invalid values fail
without a partial change. Only a changed TPS reference clears the TPS window.
Changing either bound does not rewrite TPS evidence. An admin running-limit
change reports source `admin`; setting it to zero disables that gate. Restart
restores the validated initialization policy and revision 1.
`waiting_allowance` accepts integers in `[0,1048576]`; zero protects flex
requests whenever backend waiting is positive. The startup value is always 3.

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

Context/KV/cache/Prefill/input-size/TTFT gates, tier reservations, header-based
backend priority injection, queue waits, learned capacity, and the old TPS-derived
sequence-limit/QoS-budget controller are removed. Historical variables such as
`GLOBAL_LIMIT`, `DYNAMIC_*`, `QOS_QUEUE_*`, `KV_ADMISSION_*`,
`BACKEND_PRIORITY_*`, `PREDICTIVE_KV_*`, and `PREDICTIVE_PREFILL_*` are ignored
and should be removed from Compose.
