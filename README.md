# Phala Inference Guard

Phala Inference Guard (PIG) is an admission proxy for OpenAI-compatible
**vLLM and SGLang** services. It forwards requests to one backend and protects
service quality using backend telemetry, a configurable Decode TPS target,
and atomic running/window bounds. Requests that do not fit receive HTTP 429.

PIG does not route between backends or queue requests. It preserves supported
request bodies and application headers. It is a separate component from
[Phala Inference Governor](https://github.com/Phala-Network/phala-inference-governor).

## Quick start

You need Docker and a running vLLM or SGLang backend with coherent `/metrics`
telemetry accessible from the container. This Bash example exposes PIG on
localhost; replace the upstream URL with your backend's reachable address.

```bash
export TOKEN='replace-with-a-strong-token'
export UPSTREAM='http://your-backend:8000'
docker run --rm --name pig -p 127.0.0.1:8000:8000 \
  -e TOKEN -e UPSTREAM \
  ghcr.io/phala-network/phala-inference-guard:v0.12.32@sha256:d91391a1904bea09d2a75a296613541d09f6910c9cb454de9791bc539e2cf107
```

From another terminal with the same `TOKEN`, check readiness and model discovery:

```bash
curl --fail http://127.0.0.1:8000/readyz
curl --fail -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8000/v1/models
```

The HTTP listener opens only after backend initialization succeeds. While the
model loads, PIG retries unavailable or incoherent metrics without exiting;
connection refusal during this wait is expected. After readiness, `/healthz`
is a liveness check, not proof of current backend admission capacity.

This example uses the published **v0.12.32** image. `main` is the integration
branch and may contain later changes. See [source tags](https://github.com/Phala-Network/phala-inference-guard/tags)
and [release guidance](docs/RELEASING.md) for version selection.

## Admission behavior

- `PREDICTIVE_TPS_REFERENCE` sets a long-run mean output tokens/s target per
  active Decode sequence. Its default `0` disables TPS health protection.
- Basic requests allow up to three observed waiting sequences by default;
  the admin API can change `waiting_allowance`. Premium requests retain their
  waiting bypass. Waiting protection remains active with TPS protection off.
- `PREDICTIVE_WINDOW_CONCURRENCY` defaults to 32 sequences pending first byte.
  A separate running limit applies when configured or safely discovered.
- Missing, stale or inconsistent backend observations fail closed in enforce
  mode. `shadow` is available for controlled policy evaluation.

These are telemetry-based admission controls, not a throughput guarantee.
Standard releases support a single upstream; historical DP/PD image variants
have their own topology requirements and are not interchangeable defaults.

PIG accepts both Prometheus counter declarations and
OpenMetrics counter-family declarations with `_total` samples, as emitted by
the vLLM 0.31 Rust frontend. No counter values or process timestamps are synthesized.
`process_start_time_seconds` is optional while absent: counter resets still
identify backend restarts, but a restart without an observed counter decrease
cannot be distinguished. Once a positive process start time has been observed,
its disappearance invalidates that observation instead of silently dropping
epoch tracking.

## HTTP interface

| Route | Purpose |
| --- | --- |
| `POST /v1/chat/completions`, `/v1/completions`, `/v1/responses` | Authenticated generation through admission |
| `GET /v1/models` | Authenticated model discovery |
| `GET /healthz` | Local liveness after initialization |
| `GET /readyz` | Unauthenticated backend observation readiness |
| `GET /pig/metrics` | Minimal Router capacity metrics |
| `GET /v1/metrics`, `/v1/upstream-status` | Diagnostics and admission status |
| `GET/PATCH /admin/v1/predictive-policy` | Authenticated, revision-checked policy updates |
| `GET /v1/attestation/report` | Attestation with separately configured infrastructure |

Unknown or non-canonical public routes return local HTTP 404. The admin API
updates `tps_reference`, `window_concurrency`, `running_limit`, and
`waiting_allowance` atomically using `expected_revision`; restart restores
startup values. See [configuration and API details](docs/ADVANCED.md) and
[observability](docs/OBSERVABILITY.md) for authentication and failure semantics.

Use `/readyz` for Router worker eligibility and container readiness, and
`/healthz` for PIG liveness and diagnostic management backends. Readiness returns
200 only while the admission controller has a valid, fresh backend observation
and a direct backend `GET /health` returns exactly 200 within one second;
missing, expired or unavailable observations and failed health checks return 503. Failed metric polls stop
refreshing the observation, so failure detection is bounded by its configured
maximum age plus health-check scheduling. Busy or TPS-protected backends remain
ready and retain normal admission protection. Health checks run only on readiness
requests, reuse the backend transport, do not follow redirects or forward caller
headers, and add no inference or synthetic process epoch.

## Development and documentation

Use Go 1.24 or later. Production images use the pinned Linux/amd64 CGO toolchain
in the [Dockerfile](Dockerfile); GPU attestation additionally needs the NVIDIA
runtime and its native libraries.

```bash
go test ./... -p=1 -parallel=1
go vet ./...
go build ./cmd/phala-inference-guard
```

See [contributing](CONTRIBUTING.md) for race checks and change guidelines,
[the documentation map](docs/README.md) for current references, and
[historical evidence](docs/HISTORY.md) for prior release investigations.

## License

[GNU General Public License v3.0](LICENSE).
