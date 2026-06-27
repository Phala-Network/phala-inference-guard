# Phala Inference Guard

Phala Inference Guard (PIG) is a lightweight QoS proxy for Phala model serving
backed by vLLM or SGLang. It sits between HAProxy and the serving backend, reads
backend metrics, and decides whether new model requests should be forwarded,
given a very short recovery window, or rejected.

PIG is designed for production model compose stacks where user-visible
generation speed matters. The default policy targets `25 tok/s/user` and treats
sustained performance below `20 tok/s/user` as degraded.

## Features

- Streams request and response bodies through the proxy path.
- Learns a dynamic global QoS cap from vLLM or SGLang load and generation
  metrics.
- Uses PIG-observed semantic streaming TTFT for TTFT cap learning when stream
  samples exist, with backend TTFT as the startup fallback. TTFT p99 acts as a
  tail-latency guard when first useful output drifts above the default `1s`
  target. Recovery probes upward gradually under representative load instead of
  jumping back to the global cap.
- Applies QoS only to new requests; existing streams continue.
- Gives trusted direct traffic priority over provider traffic through the
  lightweight `X-User-Tier` header. `premium` requests can use the full current
  cap, while `basic` requests leave one currently empty premium slot whenever
  possible and stop taking newly freed slots when premium requests are waiting.
- Rewrites backend JSON request priority from trusted `X-User-Tier` by default:
  `premium` receives `priority=-100`; `basic`, missing, or unknown tiers are
  normalized to `priority=0`.
- Rejects overloaded requests early instead of letting queue time damage
  provider throughput metrics.
- Measures semantic streaming TTFT: the delay until the first useful SSE
  `data:` payload such as `reasoning_content`, `reasoning`, `content`, or a tool
  call delta reaches the client.
- Can add low-load SSE keep-alive comments and early bridge comments for
  streaming responses when explicitly enabled.
- Returns vLLM-compatible HTTP 429 errors while metrics still separate QoS
  pressure from backend-unavailable failures.
- Protects `/pig/metrics` and `/v1/metrics` with
  `Authorization: Bearer $TOKEN`.
- Can protect OpenAI generation routes with the same Bearer token when `TOKEN`
  is set.
- Provides `/v1/attestation/report` directly from PIG so the extra Python proxy
  hop is no longer required.
- PIG images do not include Python or a built-in NVIDIA evidence collector.
- Intentionally does not implement E2EE request/response encryption or the
  upstream `/v1/signature/{chat_id}` route.

## Architecture

```mermaid
flowchart TD
    Request(["User request"]) --> Decision{"PIG QoS"}
    Metrics[/"Backend metrics"/] -.->|poll and learn capacity| Decision
    Decision -->|capacity available| Backend[["Serving backend"]]
    Decision -->|short recovery window| Queue["Queue"]
    Decision -->|full or unavailable| Reject(["Reject"])
    Queue -->|capacity recovers| Backend
    Queue -->|timeout| Reject
```

User requests enter the PIG QoS decision point. Backend metrics feed that
decision as a separate capacity signal, so request traffic and metrics polling
stay on different paths.

The decision has three outcomes. Requests are forwarded to the serving backend
when capacity is available, allowed only a short recovery window when pressure
may clear immediately, or rejected when capacity is full or no backend is usable.
Queued requests move to the backend if capacity recovers quickly; otherwise they
time out and are rejected.

Backend scheduler waiting is treated as a hard new-intake signal. When backend
metrics report any waiting request, PIG stops forwarding newly arriving model
requests directly to the backend, puts them through the short recovery window,
and only forwards them if the next healthy metrics polls show waiting has
cleared.

PIG learns the capacity signal by polling backend metrics and combining recent
load with generation throughput. TTFT learning prefers PIG's own semantic
streaming TTFT, meaning request arrival to the first useful SSE data written
downstream; before such stream samples exist, it falls back to backend TTFT.
The learned cap can move down when TTFT stays high, then only probes upward
after healthy samples arrive while traffic is close enough to the learned cap to
prove the backend can carry more load. That learned state is applied to future
QoS decisions.

## Project Layout

```text
cmd/phala-inference-guard/        executable entrypoint
internal/app/server/           HTTP proxy runtime, lifecycle, and component wiring
internal/app/dynamic/          dynamic metrics polling, learned caps, pressure state
internal/app/gate/             QoS acquire, tier priority, queue counters, wait/notify loop
internal/app/request/          path, body, priority rewrite, and output-token request classification
internal/config/pigconfig/     environment config loading and validation
internal/domain/capacity/      capacity learning and pressure limits
internal/domain/decision/      decision states, reasons, and limit composition
internal/domain/dynamic/       dynamic QoS policy evaluation, clean signal derivation, and stage orchestration
internal/domain/latency/       TTFT and semantic-latency learning policy
internal/domain/request/       request path, body, tier, and token classification rules
internal/domain/qos/           queue wait and prefill grace policy
internal/domain/tier/          premium/basic share and limiter policy
internal/domain/lane/          QoS lane counters and bucketed metrics state
internal/infra/                backend, HTTP, SSE, OpenAI error, env, and Prometheus adapters
internal/runtime/              backend observations, aggregation-friendly telemetry samples, dynamic snapshots, token windows, trackers
internal/observability/        histogram, status-line, and Prometheus metrics rendering
internal/support/              small pure helper packages
Dockerfile                     lean Go production image build
Dockerfile.attestation         GPU-attestation-capable image build
docs/ADVANCED.md               optional runtime knobs
docs/OBSERVABILITY.md          logs and metrics reference
docs/REQUEST_TIER_PRIORITY.md  direct/provider request priority guide
docs/WAITING_POLICY.md         backend waiting and short-wait behavior
```

The package split follows a simple rule: `app` wires requests and lifecycle,
`domain` decides limits, `runtime` stores observed state and stable telemetry
sample types, `infra` adapts outside systems such as Prometheus input and HTTP responses, and `observability`
formats logs and exported metrics. Request classification and QoS acquisition
are separate app components so the HTTP proxy runtime stays focused on request
orchestration instead of policy details.

The hot-path implementation keeps those boundaries but avoids avoidable work in
common production cases: telemetry aggregation has empty and single-backend fast
paths, request body compatibility cleanup and priority injection share one
streaming scanner with pooled buffers by default, and final limit composition
evaluates an explicit ordered component list.

## Minimal Configuration

PIG needs a serving upstream, a backend metrics endpoint, and a token for its
protected metrics endpoints.

```text
UPSTREAM=http://vllm:8000
DYNAMIC_METRICS_URL=http://vllm:8000/metrics
TOKEN=<bearer token for PIG-protected routes>
```

With those variables, dynamic QoS is enabled and enforced. The same token also
protects `/v1/metrics`, `/v1/attestation/report`, and, by default when `TOKEN`
is set, the OpenAI generation routes controlled by PIG. By default, PIG
controls:

```text
/v1/chat/completions
/v1/completions
/v1/responses
```

## Production Compose Integration

Build an image:

```sh
docker build -t phala-inference-guard:my_tag .
```

For a deployment that replaces a Python proxy and must serve attestation reports
directly from PIG, build the attestation-capable image:

```sh
docker build -f Dockerfile.attestation -t phala-inference-guard:my_tag .
```

Older production model compose files routed traffic through an extra Python proxy:

```text
dstack-ingress -> haproxy -> legacy-python-proxy -> vLLM
```

Replace that extra proxy hop with PIG:

```text
dstack-ingress -> haproxy -> phala-inference-guard -> vLLM
```

Keep the existing `dstack-ingress` target as `http://haproxy:80`.

### Add The PIG Service

Add this service next to the serving backend:

```yaml
services:
  phala-inference-guard:
    image: phala-inference-guard:my_tag
    container_name: phala-inference-guard
    restart: always
    runtime: nvidia
    privileged: true
    depends_on:
      - vllm
    environment:
      - NVIDIA_VISIBLE_DEVICES=all
      - TOKEN=${TOKEN}
      - BACKENDS=a=http://vllm:8000|http://vllm:8000/metrics
      - PROXY_TIMEOUT_SECONDS=1800
      - TLS_CERT_PATH=/evidences/cert-example.pem
      - ATTESTATION_NVIDIA_PAYLOAD_FILE=/evidences/nvidia-payload.json
      - ATTESTATION_REQUIRE_NVIDIA_EVIDENCE=true
    volumes:
      - /var/run/dstack.sock:/var/run/dstack.sock
      - /var/volatile/dstack/evidences:/evidences:ro
```

Mount `/var/run/dstack.sock` when `/v1/attestation/report` is enabled. Mount the
custom-domain certificate and set `TLS_CERT_PATH` when attestation version `2`
must bind the TLS SPKI fingerprint into `report_data`. When PIG replaces a
Python attestation proxy, give the PIG container the same GPU access that proxy
used to have: `runtime: nvidia`, `privileged: true`, and
`NVIDIA_VISIBLE_DEVICES=all` if an external collector needs GPU access. PIG no
longer ships CUDA runtime libraries, Python, NVIDIA SDK packages, or a built-in
NVIDIA collector in any image. Real GPU evidence must be supplied with
`ATTESTATION_NVIDIA_PAYLOAD`, `ATTESTATION_NVIDIA_PAYLOAD_FILE`, or an
explicitly mounted external `ATTESTATION_NVIDIA_COMMAND` together with whatever
runtime dependencies that command needs. Enable
`ATTESTATION_REQUIRE_NVIDIA_EVIDENCE=true` in production so an empty local
fallback cannot be served by mistake. With that setting enabled, PIG also
rejects configured payloads whose normalized `evidence_list` is empty.

### Point HAProxy At PIG

Change `haproxy.depends_on` from the legacy proxy service to `phala-inference-guard`:

```yaml
services:
  haproxy:
    depends_on:
      - phala-inference-guard
```

Replace the model backend server line:

```haproxy
server model legacy-python-proxy:8000 check maxconn 60
```

with:

```haproxy
server model phala-inference-guard:8000 check maxconn 512
```

Set HAProxy `maxconn` high enough that it does not become the primary limiter;
PIG should make the QoS decision. The example uses `512` as a conservative
starting point.

Also review the backend runtime concurrency limits, such as vLLM
`--max-num-seqs` or the equivalent SGLang setting. They should usually be raised
high enough that PIG, rather than the backend runtime, makes the first QoS
decision.

Route `/v1/attestation/report` to PIG. Remove any HAProxy rule that points
`/v1/signature/{chat_id}` to a legacy proxy; PIG intentionally returns 404 for
that route because request/response signature lookup is not part of the direct
vLLM path.

### Mark Request Tier

PIG reads only the `X-User-Tier` request header for tier priority. It treats
`premium` as direct traffic and treats `basic`, missing, or unknown values as
provider traffic. The check is intentionally lightweight: PIG does not parse the
request body for tier selection.

PIG also injects backend JSON request priority after a request passes QoS
admission. The mapping uses lower number means higher backend priority:

```text
X-User-Tier: premium -> "priority": -100
X-User-Tier: basic   -> "priority": 0
missing or unknown   -> "priority": 0
```

`BACKEND_PRIORITY_MODE=all` is the default because OpenAI compatibility cleanup
already scans eligible admitted JSON request bodies. Keeping priority
normalization in the same streaming pass neutralizes client-supplied body
priority without adding another body read. Set `BACKEND_PRIORITY_MODE=premium_only`
only when basic/provider request bodies must be left unchanged. Backend priority
requires the runtime to have compatible priority scheduling enabled; otherwise
the field is simply forwarded as part of the OpenAI-compatible JSON request.

Set this header only at a trusted gateway or HAProxy layer. Remove any client
supplied value before setting the tier:

```haproxy
http-request del-header X-User-Tier
http-request set-header X-User-Tier basic
```

For direct-only routes, set:

```haproxy
http-request del-header X-User-Tier
http-request set-header X-User-Tier premium
```

Do not let public clients choose this header directly.

### Route PIG Metrics

If HAProxy exposes protected metrics routes, add a PIG metrics route:

```haproxy
frontend http_frontend
    acl is_pig_metrics path /pig/metrics
    acl is_authorized hdr(Authorization) -m str "Bearer ${TOKEN}"
    http-request deny if is_pig_metrics !is_authorized
    use_backend pig_metrics_backend if is_pig_metrics

backend pig_metrics_backend
    mode http
    server pig phala-inference-guard:8000 check maxconn 32
```

PIG also serves `/v1/metrics` as the combined serving-chain metrics endpoint. It returns PIG local
metrics followed by backend metrics fetched from the configured backend metrics
URLs. If backend metrics cannot be fetched, the endpoint still returns HTTP 200
with a Prometheus comment describing the failure.

## Failure Semantics

PIG protects provider-facing performance by keeping queue waits short,
returning vLLM-compatible 429 errors when QoS capacity is full or no backend is
usable. This favors fast successful requests and early overload signals over
slow queued successes.

PIG-generated failure bodies use the vLLM/OpenAI error shape:

```json
{"error":{"message":"Too many requests","type":"TooManyRequestsError","param":null,"code":429}}
```

The internal PIG reason is not exposed in the HTTP response. It stays in the
protected QoS metrics and process status logs.

SSE comment injection is disabled by default. When explicitly enabled, PIG may
add keep-alive comments to `200 text/event-stream` responses while backend load
is green and idle. With a separate explicit switch, accepted provider requests
with `Accept: text/event-stream` can also receive an early SSE comment after a
short built-in grace window when upstream headers have not arrived yet. That can
lower provider fetch latency during long prefill gaps without inspecting or
buffering non-streaming request bodies.

For accepted `200 text/event-stream` responses, PIG also records semantic TTFT:
the time from PIG request arrival until the first useful SSE `data:` payload is
written downstream. It ignores headers, empty data, `[DONE]`, SSE comments, and
PIG keep-alive comments, so this metric is closer to what an OpenAI-compatible
streaming client actually sees than backend `time_to_first_token_seconds` or
proxy first-byte latency. When dynamic TTFT protection is enabled, PIG uses this
semantic metric as the preferred TTFT learning source after it has observed
stream samples.

Fast upstream errors still keep their original HTTP status. If an upstream
connection fails after PIG has already opened an early SSE bridge, PIG records
that path in protected metrics instead of exposing internal reasons to the
client.

## Documentation

- [ADVANCED.md](docs/ADVANCED.md): optional environment variables and tuning knobs.
- [PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md](docs/PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md):
  flowcharts for PIG internal request admission, dynamic QoS learning, and final
  cap composition.
- [OBSERVABILITY.md](docs/OBSERVABILITY.md): logs, metrics, and production checks.
- [REQUEST_TIER_PRIORITY.md](docs/REQUEST_TIER_PRIORITY.md): `X-User-Tier`
  priority behavior for direct and provider traffic.

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
