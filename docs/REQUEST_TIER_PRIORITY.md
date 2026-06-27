# Request Tier Priority

PIG can prefer direct traffic over provider traffic by reading a trusted
`X-User-Tier` request header. This feature is intended for deployments where
OpenRouter, Redpill, and direct users share the same model backend but direct
users should receive capacity first when the backend is under pressure.

## Tier Values

PIG recognizes two effective tiers:

| Header value | Meaning | Behavior |
| --- | --- | --- |
| `premium` | Direct traffic | Can use the full current QoS cap. |
| `basic` | Provider traffic | Uses the basic share of the current QoS cap. |
| missing or unknown | Provider traffic | Treated as `basic`. |

The check is intentionally lightweight. PIG only reads the request header and
normalizes it with trim/lowercase string handling. It does not parse request
bodies, tokenize prompts, inspect model names, or look up external user state to
choose a tier.

## Trust Boundary

Do not let public clients choose their own tier. `X-User-Tier` must be set by a
trusted gateway or HAProxy layer that knows which ingress path the request used.

For provider-facing routes, delete any client-supplied value and set `basic`:

```haproxy
http-request del-header X-User-Tier
http-request set-header X-User-Tier basic
```

For direct-only routes, delete any client-supplied value and set `premium`:

```haproxy
http-request del-header X-User-Tier
http-request set-header X-User-Tier premium
```

If a route does not set the header, PIG treats the request as `basic`. That is
the safer default because it prevents accidental premium access.

## QoS Behavior

Tier priority is applied only when new requests enter PIG QoS. PIG does not
cancel, interrupt, or downgrade requests that are already running in the
backend.

The current global QoS cap is still learned from backend metrics and remains the
outer safety limit. Tier priority only decides how that current cap is shared:

```text
current QoS cap
  -> premium may use the full cap
  -> basic may use the cap minus the current premium reservation
  -> basic stops taking newly freed slots while premium is waiting
```

The built-in premium reservation keeps one empty slot available for premium
traffic when the current cap is greater than one. The reservation grows with
current premium usage:

```text
cap = 8, premium inflight = 0 -> premium 0/1, basic limit = 7
cap = 8, premium inflight = 1 -> premium 1/2, basic limit = 6
cap = 8, premium inflight = 2 -> premium 2/3, basic limit = 5
```

The only exception is a cap of `1`, where basic can use that single slot because
a hard reserve would otherwise reject all provider traffic. This reservation is
intentionally small. It gives premium traffic a path into the backend during
provider load while avoiding a large fixed carve-out that can prevent PIG from
observing enough healthy traffic to raise a learned TTFT cap.

## Queue Behavior

PIG has a short recovery queue. Tier priority also applies while requests wait
for capacity:

- A waiting `premium` request prevents new `basic` requests from taking newly
  released slots.
- Waiting `basic` requests do not block `premium` requests from entering when
  capacity is available.
- Queue time remains bounded by the existing PIG queue policy.

This keeps direct users ahead of provider traffic without introducing forced
preemption.

## Backend Priority Injection

PIG rewrites backend JSON request priority for every admitted eligible JSON
request by default. The default mapping uses lower numbers for higher priority:

| Effective tier | Backend field |
| --- | --- |
| `premium` | `"priority": -100` |
| `basic`, missing, or unknown | `"priority": 0` |

The default `BACKEND_PRIORITY_MODE=all` rewrites every admitted eligible JSON
request. PIG writes premium as `-100` and writes basic, missing, or unknown tiers
as `0`, so client-supplied body priority is overwritten from `X-User-Tier`.
This is the default because the OpenAI compatibility cleanup for empty
`messages[].tool_calls` arrays already uses the same streaming body pass. Set
`BACKEND_PRIORITY_MODE=premium_only` only when provider request bodies must be
left unchanged.

`BACKEND_PRIORITY_REWRITE_STRATEGY=field_scan` is the default safe strategy. It
removes top-level client `priority`, rewrites `extra_body.priority` when
present, and injects the trusted top-level priority. The experimental
`append_last` strategy keeps the original fields and appends a duplicate
top-level priority at the end of the object. `append_last` can be fast in some
large-body cases, but it relies on the backend JSON parser using duplicate-key
last-wins semantics and does not rewrite nested `extra_body.priority`.

Backend priority injection is bounded by
`BACKEND_PRIORITY_BODY_BYTES=33554432` (`32 MiB`) and
`BACKEND_PRIORITY_REWRITE_LIMIT=64` by default. The body limit is sized to cover
long-context premium requests, including roughly 1M-token text prompts in
typical OpenAI-compatible JSON bodies. `BACKEND_PRIORITY_BUFFER_BYTES` defaults
to `0`, so the normal `field_scan` path stays streaming and preserves
client-to-backend transfer overlap. Set `BACKEND_PRIORITY_BUFFER_BYTES` to a
positive value only after measuring that full-body buffering helps a specific
workload; full buffering preserves `Content-Length` but can increase large-body
end-to-end latency because PIG must read and rewrite the body before sending it
upstream. `BACKEND_PRIORITY_STREAM_BUFFER_BYTES` defaults to `2097152` (`2 MiB`)
for the streaming scanner; this was the best measured safe setting across
31 MiB chat `messages`, OpenAI `extra_body.priority`, and SGLang `/generate`
JSON body tests on the builder benchmark. Known request bodies smaller than the
stream buffer maximum use 4 KiB-or-larger power-of-two buffer buckets, which
keeps pooled buffer sizes bounded while avoiding the 2 MiB buffer on every
small request. The streaming path removes the
original `Content-Length`, so the backend receives chunked transfer encoding.
In the default fail-open mode, PIG forwards requests skipped by size, content
type, unknown length, or rewrite-slot pressure unchanged. Priority rewrite
assumes admitted OpenAI-compatible request bodies are valid JSON objects;
malformed JSON can be forwarded unchanged in buffered fail-open paths or fail
while the streaming body is being forwarded. Set
`BACKEND_PRIORITY_FAIL_OPEN=false` only for strict deployments that prefer
rejecting synchronously unrewritable requests over possibly forwarding a
client-supplied priority value.

The backend runtime must have compatible priority scheduling enabled for this
field to affect scheduling. PIG's tier admission still works independently even
if the backend ignores request priority.

For vLLM, use priority scheduling with the backend's `priority` request field;
vLLM schedules lower numeric priority first, so PIG's default `premium=-100`
and `basic=0` mapping matches the backend ordering.

For SGLang, enable request priority with `--enable-priority-scheduling` and
`--schedule-policy priority`. Because SGLang's default priority order schedules
higher numeric values first, also set `--schedule-low-priority-values-first`
when PIG uses the default `premium=-100` and `basic=0` mapping.

## Failure Behavior

PIG-generated rejects still return an OpenAI-compatible HTTP 429 body:

```json
{"error":{"message":"Too many requests","type":"TooManyRequestsError","param":null,"code":429}}
```

Internal reasons such as `tier_basic_limit` or `tier_priority` are not exposed
to clients. They are visible only through protected metrics and status logs.

## Observability

The following metrics show how tier priority is behaving:

| Metric | Meaning |
| --- | --- |
| `pig_tier_inflight{tier="basic"}` | Currently admitted basic requests. |
| `pig_tier_inflight{tier="premium"}` | Currently admitted premium requests. |
| `pig_tier_waiting{tier="basic"}` | Basic requests in the PIG short-wait queue. |
| `pig_tier_waiting{tier="premium"}` | Premium requests in the PIG short-wait queue. |
| `pig_tier_requests_total{tier="...",decision="accepted"}` | Accepted requests by tier. |
| `pig_tier_requests_total{tier="...",decision="rejected"}` | Rejected requests by tier. |
| `pig_tier_basic_limit` | Current maximum basic in-flight requests. |
| `pig_tier_premium_reserved_capacity` | Current capacity outside the basic share. |
| `pig_backend_priority_rewrite_total` | JSON requests where PIG wrote backend priority. |
| `pig_backend_priority_skipped_total` | Requests skipped by mode, body shape, size, content type, or rewrite-slot pressure. |
| `pig_backend_priority_failed_total` | Requests where body read or JSON rewrite failed. |
| `pig_backend_priority_stream_buffer_bytes` | Streaming scanner buffer size for backend priority rewrite. |

The periodic `pig_status` log also includes compact tier state:

```text
tier_basic=<inflight>/<basic_limit> tier_premium=<inflight>/<reserved>
```

## Operational Checks

After enabling the header rules, check:

- Provider routes set `X-User-Tier: basic`.
- Direct routes set `X-User-Tier: premium`.
- Public clients cannot pass through a spoofed `X-User-Tier: premium`.
- `pig_tier_inflight` and `pig_tier_requests_total` show traffic in the expected
  tiers.
- During pressure, `pig_tier_basic_limit` is the current QoS cap minus the
  dynamic premium reservation. With cap `8`, it should be `7` at `premium 0/1`,
  `6` at `premium 1/2`, and so on.
- `pig_tier_premium_reserved_capacity` reports the current dynamic premium
  reservation, not a fixed capacity share.
- `pig_backend_priority_rewrite_total` grows for admitted premium plus basic JSON
  requests by default. In `BACKEND_PRIORITY_MODE=premium_only`, it grows only for
  admitted premium JSON requests.
- `pig_backend_priority_failed_total` stays near `0`.

If all traffic shows as `basic`, the trusted gateway is probably not setting the
direct route header. If provider traffic shows as `premium`, the gateway is not
overwriting client-supplied tier values and should be fixed before rollout.

## Limits

Tier priority improves QoS entry order on a shared backend, but it is not hard
resource isolation. Existing provider requests continue running after they have
entered the backend. If direct traffic must be isolated from provider traffic
even when the backend is already full of long provider requests, use separate
backend pools or separate CVMs in addition to PIG tier priority.
