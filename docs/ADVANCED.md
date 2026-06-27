# PHALA-INFERENCE-GUARD(1)

## NAME

phala-inference-guard - dynamic QoS proxy for vLLM or SGLang backed model serving.

## SYNOPSIS

```text
phala-inference-guard

Common production configuration:
  UPSTREAM=<url>
  DYNAMIC_METRICS_URL=<url>
  TOKEN=<bearer-token>
```

Common multi-backend forms:

```text
DYNAMIC_METRICS_URLS=<url>,<url>
BACKENDS=name=<upstream>|<metrics-url>,name=<upstream>|<metrics-url>
UPSTREAMS=<url>,<url>
```

## DESCRIPTION

PIG forwards OpenAI-compatible requests to one or more serving backends while
using vLLM or SGLang metrics to decide whether new requests should be accepted,
given a very short recovery window, or rejected. The default dynamic policy
learns a global cap from backend load and generation throughput, then rejects
overloads early so queue time does not dominate provider throughput metrics.
Request body and output-token lanes are labels for observability, not
independent QoS caps. PIG can optionally add SSE comment keep-alives during
low-load green states so long idle gaps do not look like a dead stream. It can
also optionally open a low-load SSE bridge for accepted provider requests with
`Accept: text/event-stream` after the built-in header grace window if upstream
SSE headers have not arrived yet. Both SSE behaviors are disabled by default and
require explicit environment variables.

Non-default options below are intentionally kept out of the README because most
deployments should start with only `UPSTREAM`, `DYNAMIC_METRICS_URL`, and
`TOKEN`.

## REQUEST TIER PRIORITY

`X-User-Tier`
: Request header read by PIG. `premium` identifies direct traffic. `basic`,
  missing, or unknown values identify provider traffic. This is not an
  environment variable.

PIG uses tier priority only for new request QoS. It does not cancel streams that
are already running. Premium requests can use the full current global cap. Basic
requests are limited by the current dynamic premium reservation, and they stop
taking newly freed slots while premium requests are waiting.

The built-in reservation keeps one empty premium slot whenever the current cap
is greater than one, and grows with current premium in-flight work:

```text
cap = 1  -> basic limit = 1
cap = 8, premium inflight = 0 -> premium 0/1, basic limit = 7
cap = 8, premium inflight = 1 -> premium 1/2, basic limit = 6
```

This leaves one open path for premium traffic without wasting a large fixed
share of the learned cap. Set `X-User-Tier` only from trusted HAProxy or gateway
rules and delete any client supplied value before writing it.

PIG also rewrites backend JSON request priority by default. The built-in mapping
uses the same low-number-is-higher convention for vLLM and SGLang deployments
that enable priority scheduling:

```text
premium -> priority -100
basic, missing, or unknown -> priority 0
```

`BACKEND_PRIORITY_MODE=all` is the default. Set
`BACKEND_PRIORITY_MODE=premium_only` only when basic, missing, or unknown request
bodies must be left unchanged.

This backend priority injection is separate from PIG's own tier admission
policy. PIG admission decides whether a new request may enter the backend.
Backend priority helps the runtime scheduler prefer premium work among requests
that have already reached the serving backend or its queue.

## REQUIRED ENVIRONMENT

`UPSTREAM`
: Default: `http://backend:8000`. Upstream URL used when `BACKENDS` or
  `UPSTREAMS` is not provided.

`DYNAMIC_METRICS_URL`
: Required when dynamic QoS is enabled and `DYNAMIC_METRICS_URLS` is not
  set. Points to the backend Prometheus metrics endpoint.

`TOKEN`
: Bearer token required for `/pig/metrics`, `/v1/metrics`, and
  `/v1/attestation/report`. When `TOKEN` is set, PIG also protects OpenAI
  generation routes by default. If empty, metrics and attestation access are
  denied.

## GENERAL OPTIONS

`LISTEN`
: Default: `:8000`. HTTP listen address.

`GLOBAL_LIMIT`
: Default: `512`. Hard upper bound for admitted in-flight requests. This is a
  safety cap; normal tuning should rely on dynamic QoS.

`PROXY_TIMEOUT_SECONDS`
: Default: `1800`. Per-request upstream proxy timeout.

`PIG_STATUS_LOG_INTERVAL_SECONDS`
: Default: `5`. Periodic `pig_status` log interval. Set to `0` to disable
  periodic status logging.

## QOS PATHS

`PIG_PATHS`
: Default: `/v1/chat/completions,/v1/completions,/v1/responses`. Only matching
  paths are controlled by QoS. Other paths pass through to the selected
  backend.

`PIG_PATH_SUFFIX_MATCH`
: Default: `false`. When `true`, path matching uses suffix matching. Use this
  only when PIG is mounted behind a reviewed path prefix.

## OPENAI API COMPATIBILITY

`API_AUTH_ENABLED`
: Default: `true` when `TOKEN` is set, otherwise `false`. When enabled, PIG
  requires `Authorization: Bearer $TOKEN` on paths matched by
  `API_AUTH_PATHS`.

`API_AUTH_PATHS`
: Default: same as `PIG_PATHS`. Comma-separated OpenAI generation paths that
  require Bearer auth when `API_AUTH_ENABLED=true`. `/v1/models` is not
  protected by this option.

`OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS`
: Default: `true`. Removes `tool_calls: []` from `messages[]` objects before
  forwarding admitted JSON requests. This preserves compatibility with vLLM
  versions that reject empty assistant tool-call arrays. This cleanup shares the
  same streaming JSON body pass as backend priority injection, so premium and
  basic default rewrites do not add a second body parse.

`OPENAI_COMPAT_BODY_BYTES`
: Default: `33554432` (`32 MiB`). Maximum known `Content-Length` eligible for
  compatibility JSON rewrites. Larger, chunked, unknown-size, or non-JSON
  bodies are skipped.

`OPENAI_COMPAT_FAIL_OPEN`
: Default: `true`. When `true`, JSON scan or rewrite failures skip the
  compatibility rewrite and forward the request. Known-length bodies that fit
  within `BACKEND_PRIORITY_STREAM_BUFFER_BYTES` are safety-buffered, so failures
  restore the original body. Larger bodies keep the streaming path to avoid
  unbounded memory growth. When `false`, such failures are rejected with PIG's
  normal OpenAI-compatible HTTP 429 body.

## ATTESTATION

`ATTESTATION_ENABLED`
: Default: `true`. Enables `/v1/attestation/report` directly in PIG. The
  endpoint is protected by `Authorization: Bearer $TOKEN` and requires
  `/var/run/dstack.sock` at request time.

`ATTESTATION_DSTACK_ENDPOINT`
: Default: empty. Optional dstack endpoint override. When empty, PIG uses
  `DSTACK_SIMULATOR_ENDPOINT` if set, otherwise `/var/run/dstack.sock`.

`TLS_CERT_PATH`
: Default: empty. PEM certificate path used by attestation report version `2`
  to bind the TLS SubjectPublicKeyInfo SHA256 fingerprint into dstack
  `report_data`. Version `2` returns HTTP 400 if this path is unset or
  unreadable.

`ATTESTATION_GPU_ARCH`
: Default: `HOPPER`. GPU architecture label written into the NVIDIA payload
  fallback shape used when local test evidence is allowed.

`ATTESTATION_REQUIRE_NVIDIA_EVIDENCE`
: Default: `false`. When `true`, `/v1/attestation/report` fails unless NVIDIA
  evidence is supplied by `ATTESTATION_NVIDIA_PAYLOAD`,
  `ATTESTATION_NVIDIA_PAYLOAD_FILE`, or `ATTESTATION_NVIDIA_COMMAND`, and the
  normalized `evidence_list` is non-empty.

`ATTESTATION_NVIDIA_PAYLOAD`
: Default: empty. Optional raw NVIDIA payload JSON. `${nonce}` is replaced with
  the attestation request nonce before the response is returned. Payloads with
  `evidence_list`, `evidences`, or a single `evidence` field are normalized to
  the `nvidia_payload` response shape.

`ATTESTATION_NVIDIA_PAYLOAD_FILE`
: Default: empty. Optional file containing raw NVIDIA payload JSON. Used when
  `ATTESTATION_NVIDIA_PAYLOAD` is empty.

`ATTESTATION_NVIDIA_COMMAND`
: Default: empty. Optional externally supplied command used to collect NVIDIA
  GPU evidence. If a deployment uses this option, mount the collector executable
  and any runtime dependencies it needs into the container explicitly, and give
  the container the GPU access that collector requires, for example
  `runtime: nvidia`, `privileged: true`, and `NVIDIA_VISIBLE_DEVICES=all`.

`ATTESTATION_NVIDIA_COMMAND_ARGS`
: Default: `--nonce,{nonce},--arch,<ATTESTATION_GPU_ARCH>`.
  Comma-separated arguments for `ATTESTATION_NVIDIA_COMMAND`. `{nonce}` is
  replaced with the attestation request nonce.

`ATTESTATION_NVIDIA_COMMAND_TIMEOUT_SECONDS`
: Default: `30`. Timeout for the NVIDIA evidence command.

PIG's attestation surface is `/v1/attestation/report`; it is the only
attestation HTTP endpoint exposed by the service.

If no NVIDIA payload source is configured and
`ATTESTATION_REQUIRE_NVIDIA_EVIDENCE=false`, PIG still returns a syntactically
compatible `nvidia_payload` with an empty `evidence_list`. That mode is useful
for local tests but is not a complete production GPU attestation.

## DYNAMIC QOS

`DYNAMIC_PIG_ENABLED`
: Default: enabled when a dynamic metrics URL is configured. Turns metrics-based
  dynamic QoS on or off.

`DYNAMIC_PIG_ENFORCE`
: Default: same as `DYNAMIC_PIG_ENABLED`. When `false`, PIG still observes
  dynamic state but does not enforce dynamic limits.

`DYNAMIC_TTFT_ENABLED`
: Default: same as `DYNAMIC_SINGLE_USER_TPS_ENABLED`. When `false`, PIG still
  exports observed TTFT metrics but does not use TTFT latency to mark load
  state or reduce the QoS cap.

  When enabled, PIG uses its own semantic streaming TTFT once it has observed
  accepted stream samples; otherwise it falls back to backend TTFT metrics. TTFT
  p95 above the target can learn the cap downward, while TTFT p99 acts as a
  tail-latency guard. Healthy TTFT does not immediately reset the learned cap to
  `GLOBAL_LIMIT`; PIG probes upward in small steps after enough healthy samples
  arrive. It can also recover a stale TTFT cap from healthy low-load samples
  instead of staying yellow forever just because traffic is below a
  representative-load threshold.

`DYNAMIC_METRICS_URLS`
: Default: empty. Comma-separated metrics URLs. For a single upstream that
  already load-balances backend traffic, PIG aggregates running, waiting,
  preemption, generation, and KV usage from these endpoints. Each metrics URL is
  normalized independently before global aggregation, so generation and
  preemption counter resets on one backend do not poison the global learning
  window. If some URLs fail but at least one URL succeeds, PIG excludes the
  failed samples, reports the failed backend count, and continues learning from
  the healthy metrics. If all URLs fail, PIG enters the metrics-unavailable
  failsafe path.

`DYNAMIC_POLL_INTERVAL_MS`
: Default: `1000`. Metrics poll interval.

`DYNAMIC_FAILSAFE_STATE`
: Default: `yellow`. State label used when metrics polling fails. Metrics
  failure sets the effective dynamic global limit to `0`, so new admitted-path
  requests return an OpenAI-compatible HTTP 429 immediately when no backend is
  usable.

`DYNAMIC_GLOBAL_GREEN_LIMIT`
: Default: `GLOBAL_LIMIT`. State cap before QoS, learned-cap, and pressure
  reductions.

`DYNAMIC_GLOBAL_YELLOW_LIMIT`
: Default: `DYNAMIC_GLOBAL_GREEN_LIMIT`.

`DYNAMIC_GLOBAL_RED_LIMIT`
: Default: `DYNAMIC_GLOBAL_YELLOW_LIMIT`.

## LOAD STATE THRESHOLDS

`DYNAMIC_KV_YELLOW`
: Default: `0.70`. KV cache usage at or above this value marks yellow pressure.

`DYNAMIC_KV_RED`
: Default: `0.80`. KV cache usage at or above this value marks red pressure.

`DYNAMIC_RUNNING_YELLOW`
: Default: `0`. Optional running-request threshold for yellow. `0` disables this
  trigger.

`DYNAMIC_RUNNING_RED`
: Default: `DYNAMIC_RUNNING_YELLOW`. Optional running-request threshold for red.
  `0` disables this trigger.

`DYNAMIC_WAITING_YELLOW`
: Default: `1`. Waiting-request threshold for yellow.

`DYNAMIC_WAITING_RED`
: Default: `2`. Waiting-request threshold for red.

`DYNAMIC_PREEMPTION_DELTA_RED`
: Default: `1`. Per-poll preemption increase that marks red pressure.

## SINGLE-USER TPS POLICY

`DYNAMIC_SINGLE_USER_TPS_ENABLED`
: Default: same as `DYNAMIC_PIG_ENABLED`. Enables user-visible TPS
  protection.

`DYNAMIC_SINGLE_USER_TPS_YELLOW`
: Default: `25`. Normal target in generation tokens per second per active decode
  request.

`DYNAMIC_SINGLE_USER_TPS_RED`
: Default: `20`. Degradation floor. Sustained observations below this threshold
  can stop new QoS intake until recovery.

`DYNAMIC_SINGLE_USER_TPS_MIN_RUNNING`
: Default: `1`. Minimum decode-running request count before TPS observations are
  considered.

`DYNAMIC_SINGLE_USER_TPS_YELLOW_CONSECUTIVE`
: Default: `2`. Consecutive low-TPS windows required before yellow TPS pressure
  is enforced.

`DYNAMIC_SINGLE_USER_TPS_RED_CONSECUTIVE`
: Default: `3`. Consecutive low-TPS windows required before red TPS pressure is
  enforced.

## PREFILL GRACE

PIG tracks recently admitted requests as prefill-protected for a bounded time so
long-prompt prefill does not look like a true decode-capacity collapse.

`DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MIN_SECONDS`
: Default: `2`. Minimum prefill grace.

`DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MAX_SECONDS`
: Default: `30`. Maximum prefill grace.

`DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_BODY_BYTES_PER_SECOND`
: Default: `65536`. Body-size based grace estimator.

`DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MULTIPLIER`
: Default: `1`. Multiplier applied to estimated prefill grace.

## CAPACITY LEARNING

The learned capacity cap is monotonic only within a local pressure episode. It
can move down after sustained low per-user TPS, high TTFT, waiting pressure, KV
pressure, or preemption, and can move up again through bounded probes when
backend metrics stay healthy. Later policy stages only keep or tighten an
already stricter QoS cap; they should not raise a cap that TTFT or pressure
learning has already reduced.
The clean learner exports its immediate projected cap and bounded explanation
labels through `pig_dynamic_capacity_projected_limit` and
`pig_dynamic_capacity_learning_reason_info`.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_LEARN_ENABLED`
: Default: same as `DYNAMIC_PIG_ENABLED`. Enables learned-cap adjustment.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO`
: Default: `0.42`. Safety ratio applied to the clean capacity estimate:
  `floor(generation_tokens_per_second * ratio / target_single_user_tps)`.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_MAX`
: Default: `0.85`. Upper bound used by backend-routing capacity scoring when a
  later snapshot reports a larger capacity ratio.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_SMOOTHING`
: Default: `0.85`. Smoothing factor for capacity TPS.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_STEP_UP`
: Default: `0.02`. Maximum upward ratio probe step.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_CONSECUTIVE`
: Default: `10`. Consecutive healthy observations needed before normal upward
  probing.

`DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_MULTIPLIER`
: Default: `1.5`. Required margin above target TPS before confident upward
  probing.

## PRESSURE LIMITING

`DYNAMIC_PRESSURE_LIMIT_ENABLED`
: Default: same as `DYNAMIC_PIG_ENABLED`. Enables extra reductions from
  scheduler pressure, waiting requests, preemptions, and high KV usage.

`DYNAMIC_PRESSURE_HEADROOM`
: Default: `1`. Headroom reserved when reducing pressure limits.

`DYNAMIC_PRESSURE_MIN_LIMIT`
: Default: `1`. Minimum pressure-derived cap.

`DYNAMIC_PRESSURE_LEARN_RATIO`
: Default: `0.75`. Ratio used when learning down from observed pressure.

`DYNAMIC_PRESSURE_LEARN_MIN_RUNNING`
: Default: `16`. Minimum running load before severe pressure is treated as
  representative for learning.

## QUEUEING

`PIG_QUEUE_WAIT_SECONDS`
: Default: `0.5`. Maximum configured queue wait. PIG caps the effective wait at
  `0.5s`, reduces it to about `0.25s` under scheduler or waiting pressure, to
  about `0.1s` under severe KV/preemption pressure, and does not queue when no
  backend is usable. When backend metrics report any waiting request, PIG closes
  new-request backend intake and uses this short queue window; if waiting does
  not clear quickly, the request receives an OpenAI-compatible 429. Set to `0` to
  reject immediately when no slot is available.

`PIG_QUEUE_POLL_MS`
: Default: `100`. Waiter notification poll interval. PIG backs this off for the
  fallback poll loop and also wakes waiters when capacity changes.

## SSE KEEP-ALIVE

`SSE_KEEPALIVE_ENABLED`
: Default: `false`. When explicitly set to `true`, PIG wraps successful
  `200 text/event-stream` responses with a low-load keep-alive reader. While
  backend state is green, no backend has failed, there is no observed backend
  waiting, PIG queue depth is zero, and KV cache usage is below `0.70`, PIG
  emits `: keep-alive` comments every `2s` when the upstream stream is idle.

`SSE_EARLY_BRIDGE_ENABLED`
: Default: `false`. When explicitly set to `true`, accepted provider requests
  that advertise `Accept: text/event-stream` can receive an early SSE comment
  after the built-in header grace window if upstream SSE headers have not
  arrived yet and the backend is in a low-load green state.

Normal keep-alives do not send data before the upstream has returned response
headers. Early bridge does, so keep it disabled unless the deployment explicitly
wants provider fetch-latency shaping during long prefill gaps.

Semantic TTFT metrics are always observed for accepted `200 text/event-stream`
responses. PIG scans only the first `64 KiB` of SSE bytes, stops immediately
after the first useful model delta, and never blocks forwarding on metric
parsing. Dynamic TTFT learning prefers this semantic source after stream samples
exist, which is especially important for reasoning streams where the first
useful output may be `reasoning_content` or `reasoning` before normal `content`.

## OPTIONAL REQUEST CLASSIFICATION

These options are disabled by default and are not required for normal production
QoS.

`CLASSIFY_OUTPUT_TOKENS`
: Default: `false`. When enabled, PIG may read bounded JSON request bodies to
  classify output-token intent. When disabled, PIG never parses request bodies
  for output tokens.

`JSON_CLASSIFY_BODY_BYTES`
: Default: `2097152`. Maximum known `Content-Length` eligible for JSON
  classifier reads.

`JSON_CLASSIFY_LIMIT`
: Default: `64`, or `GLOBAL_LIMIT` if `GLOBAL_LIMIT < 64`. Maximum concurrent
  JSON classifier slots.

`OUTPUT_TOKEN_FIELD_NAMES`
: Default: `max_tokens,max_completion_tokens,max_output_tokens`. JSON fields
  used for output-token classification.

`MEDIUM_BODY_BYTES`
: Default: `60000`. Body-size label threshold.

`LONG_BODY_BYTES`
: Default: `100000`. Body-size label threshold.

`VERY_LONG_BODY_BYTES`
: Default: `524288`. Body-size label threshold.

`MEDIUM_OUTPUT_TOKENS`
: Default: `1024`. Output-token label threshold.

`LONG_OUTPUT_TOKENS`
: Default: `4096`. Output-token label threshold.

`VERY_LONG_OUTPUT_TOKENS`
: Default: `8192`. Output-token label threshold.

Chunked or unknown-size requests are labelled `unknown_body` and are not parsed
by the JSON classifier. If the classifier is busy or parsing fails, PIG skips
output-token classification and lets the global dynamic cap decide QoS intake.

## BACKEND PRIORITY INJECTION

These options control PIG's backend request-body rewrite. They are enabled by
default so premium backend priority comes from the trusted `X-User-Tier` header,
not a client-supplied JSON `priority` value.

`BACKEND_PRIORITY_INJECTION_ENABLED`
: Default: `true`. Enables JSON request priority injection for admitted QoS
  paths. Set to `false` to disable trusted backend priority injection. OpenAI
  compatibility cleanup can still rewrite eligible JSON bodies when
  `OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS=true`.

`BACKEND_PRIORITY_MODE`
: Default: `all`. Supported values are `all` and `premium_only`. In `all` mode,
  PIG rewrites both premium and basic JSON requests, which neutralizes spoofed
  client body priorities by setting basic, missing, or unknown tiers to
  `BACKEND_PRIORITY_BASIC_VALUE`. In `premium_only` mode, PIG only injects
  trusted backend priority for premium requests; OpenAI compatibility cleanup may
  still scan eligible basic JSON bodies when enabled.

`BACKEND_PRIORITY_REWRITE_STRATEGY`
: Default: `field_scan`. Supported values are `field_scan` and `append_last`.
  `field_scan` removes top-level client `priority`, rewrites
  `extra_body.priority` when present, and injects the trusted top-level
  priority. `append_last` preserves the original body and appends a duplicate
  top-level priority before the final `}`. It can be fast in some large-body
  cases, but it depends on the backend JSON parser treating duplicate object
  keys as last-wins, and it does not rewrite nested `extra_body.priority`.
  The default streaming body path also removes empty `messages[].tool_calls`
  arrays when OpenAI compatibility cleanup is enabled. Both streaming strategies
  precompute the trusted priority field fragment and use pooled buffers;
  non-JSON content types are skipped without lowercasing or
  copying the full header value.

`BACKEND_PRIORITY_FIELD`
: Default: `priority`. JSON field name to write.

`BACKEND_PRIORITY_PREMIUM_VALUE`
: Default: `-100`. Priority value written for `X-User-Tier: premium`.

`BACKEND_PRIORITY_BASIC_VALUE`
: Default: `0`. Priority value written for `basic`, missing, or unknown tiers.

`BACKEND_PRIORITY_BODY_BYTES`
: Default: `33554432` (`32 MiB`). Maximum known `Content-Length` eligible for
  rewrite. This default is intended to cover long-context premium requests,
  including roughly 1M-token text prompts in typical OpenAI-compatible JSON
  bodies. Larger, chunked, or unknown-size bodies are forwarded unchanged by
  default for compatibility. Set `BACKEND_PRIORITY_FAIL_OPEN=false` when strict
  backend priority normalization is more important than accepting unusual
  request shapes.

`BACKEND_PRIORITY_BUFFER_BYTES`
: Default: `0`. Maximum known `Content-Length` that PIG rewrites fully in
  memory before forwarding. Buffered rewrites preserve an accurate
  `Content-Length`, but they also remove client-to-backend streaming overlap
  and can increase end-to-end latency for large prompts. Keep the default `0`
  for the optimized streaming rewrite path. Set a positive value only when a
  deployment has measured that full-body buffering helps its workload.
  Streaming rewrites remove the original `Content-Length`, so the backend
  receives chunked transfer encoding. In fail-open mode, PIG still
  safety-buffers known-length bodies that fit within
  `BACKEND_PRIORITY_STREAM_BUFFER_BYTES` so malformed or otherwise unrewritable
  small requests are forwarded with their original body.

`BACKEND_PRIORITY_STREAM_BUFFER_BYTES`
: Default: `2097152` (`2 MiB`). Maximum internal buffer size for the streaming
  `field_scan` and `append_last` scanners. PIG uses a smaller buffer for known
  request bodies smaller than this value, rounded into 4 KiB-or-larger
  power-of-two buckets to keep buffer pools bounded. The default was selected
  from the builder benchmark as the best measured safe setting across 31 MiB
  chat `messages`, OpenAI `extra_body.priority`, and SGLang `/generate` JSON
  bodies. Larger values can help specific body shapes but use more memory per
  active rewrite; smaller values reduced throughput for large bodies in the
  same test. The streaming path is allocation-conscious but still parses JSON
  structure enough to avoid trusting client-supplied top-level or `extra_body`
  priority.

`BACKEND_PRIORITY_REWRITE_LIMIT`
: Default: `64`, or `GLOBAL_LIMIT` if `GLOBAL_LIMIT < 64`. Maximum concurrent
  priority rewrite slots.

`BACKEND_PRIORITY_FAIL_OPEN`
: Default: `true`. When `true`, PIG forwards requests that cannot be rewritten
  because the body is too large, unknown-size, non-JSON, or the rewrite slots
  are busy. When `false`, those requests are rejected with PIG's normal
  OpenAI-compatible HTTP 429 body. Known-length malformed JSON bodies at or
  below `BACKEND_PRIORITY_STREAM_BUFFER_BYTES` are restored to their original
  bytes before forwarding. Larger malformed bodies can still fail while the
  streaming body is being forwarded; set `false` for strict deployments that
  prefer rejecting synchronously unrewritable requests over possibly forwarding
  a client-supplied priority value.

## OPTIONAL ADAPTIVE OUTPUT CLASSIFICATION

These options only apply when output-token classification is enabled.

`ADAPTIVE_OUTPUT_CLASSIFICATION`
: Default: `false`. Enables output threshold adjustment from observed output
  token samples.

`ADAPTIVE_OUTPUT_WINDOW`
: Default: `512`. Sample window size.

`ADAPTIVE_OUTPUT_MIN_SAMPLES`
: Default: `32`. Minimum samples before adaptive thresholds apply.

`ADAPTIVE_OUTPUT_MEDIUM_QUANTILE`
: Default: `0.50`. Quantile used for the medium output threshold.

`ADAPTIVE_OUTPUT_LONG_QUANTILE`
: Default: `0.90`. Quantile used for the long output threshold.

`ADAPTIVE_OUTPUT_VERY_QUANTILE`
: Default: `0.99`. Quantile used for the very-long output threshold.

`ADAPTIVE_OUTPUT_GREEN_RELAX`
: Default: `1.0`. Relax factor in green state.

`ADAPTIVE_OUTPUT_YELLOW_RELAX`
: Default: `0.5`. Relax factor in yellow state.

`ADAPTIVE_OUTPUT_RED_RELAX`
: Default: `0.0`. Relax factor in red state.

## BACKEND ROUTING

`BACKENDS`
: Default: empty. Comma-separated backend specs in the form
  `name=<upstream>|<metrics-url>`. Enables per-backend metrics polling and
  routing.

`UPSTREAMS`
: Default: empty. Comma-separated upstream URLs. When paired with
  `DYNAMIC_METRICS_URLS`, each upstream is associated with the metrics URL at
  the same position.

With backend routing enabled, PIG skips failed or stale backend metrics when
choosing a backend. If all backend metrics fail, PIG marks the service
unavailable for new admitted-path requests.

## OBSERVABILITY

Logs and metrics are documented in `OBSERVABILITY.md`.

## EXIT STATUS

PIG fails at startup when configuration is invalid: malformed URLs, empty
QoS paths, duplicate paths, invalid booleans, invalid integers, or
non-increasing threshold values.

## SEE ALSO

README.md for the basic operating guide.
