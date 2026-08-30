# Phala Inference Guard

Phala Inference Guard (PIG) is a single-upstream admission proxy for
OpenAI-compatible vLLM and SGLang services. Its current development direction
is intentionally narrow: protect a configurable long-run per-user Decode TPS
reference while admitting as much total work as the observed service quality
supports.

PIG makes the decision before forwarding. Backend feedback updates the next
prediction; it is not a second post-response limiter. The controller does not
route between backends and does not use request input size, KV occupancy,
prefix-cache hit rate, Prefill classes, long-input bands, or TTFT as independent
admission gates.

Source, builder tests, published images, deployment, and live observations are
separate evidence layers. This README describes the source contract and does
not identify what any CVM currently runs.

## Request path

```text
canonical method + exact-path public policy
  -> public bearer authentication
  -> bounded request-shape scan for Decode sequence demand
  -> fresh backend identity/running/waiting/generation/preemption observation
  -> rolling and latest qualified TPS health
  -> projected running and same-observation window bounds
  -> one atomic decision and sequence reservation
  -> unchanged request forwarded to the single upstream
  -> first-response and terminal lifecycle reconciliation
```

`PREDICTIVE_TPS_REFERENCE` is a long-run mean output-TPS target per active
Decode sequence. It is not an instantaneous threshold. Occasional low samples
are acceptable and become evidence for later predictions; they do not create a
cooldown, consecutive-clear requirement, sticky recovery timer, or learned low
cap.

Waiting pauses marginal intake even when TPS reference protection is disabled.
One sub-window nonzero sample is treated as transient; a second adjacent fresh
sample confirms waiting, while a first sample at or above the current window
bound protects immediately. The first zero-waiting sample clears waiting
protection immediately; independent guards may still protect.
A fresh preemption pauses intake when TPS health is enabled. Same-snapshot
reservations remain atomic so concurrent arrivals cannot spend the same
apparent headroom. TPS never derives a concurrency ceiling. The default window
bound allows 32 Decode sequences that are still pending first byte. A normal
metrics poll does not release that budget. First byte or terminal lifecycle
releases it immediately; after three polling intervals, a fresh zero-waiting
observation may release a long-TTFT lease. A separate backend running limit is
enforced only when explicitly configured or safely discovered.

## Production configuration

Production Compose should be small and should not repeat source defaults:

```yaml
services:
  pig:
    image: ghcr.io/phala-network/phala-inference-guard:<released-version>
    environment:
      - UPSTREAM=http://backend:8000
      - TOKEN=${PIG_TOKEN}
      - PREDICTIVE_TPS_REFERENCE=${PIG_TPS_REFERENCE}
      - TLS_CERT_PATH=/etc/pig/tls/tls.crt
      - ATTESTATION_DSTACK_ENDPOINT=${DSTACK_ENDPOINT}
```

`UPSTREAM` is one absolute HTTP URL. PIG derives `/metrics` from that origin,
defaults to `enforce`, polls every 500 ms, and defaults observation freshness to
three polls. The pending-first-byte lease is also three polls and is derived,
not configured. Omit `PREDICTIVE_TPS_REFERENCE` or set it to `0` when no
business TPS target exists; waiting protection remains active.

`PREDICTIVE_WINDOW_CONCURRENCY` defaults to `32` and normally stays implicit.
`PREDICTIVE_RUNNING_LIMIT=0` means unknown/disabled. SGLang can initialize its
running limit from a coherent top-level integer `max_running_requests` in
`/server_info`; vLLM does not expose a trusted production maximum, so its limit
remains disabled unless an operator sets it. These are initialized or
administered bounds, not learned values.

For SGLang, auto-discovery runs only when `PREDICTIVE_RUNNING_LIMIT` is absent.
Setting it explicitly to `0` disables discovery and the running-limit gate;
setting a positive value uses that value without probing `/server_info`.

PIG startup requires coherent backend identity, running, waiting, generation,
preemption, and runtime-epoch telemetry. It does not require KV/cache metrics or
model context metadata and does not probe `/v1/models` to construct admission
policy.

These retired settings are ignored and should be removed from Compose:

```text
GLOBAL_LIMIT
DYNAMIC_*
QOS_QUEUE_*
KV_ADMISSION_*
BACKEND_PRIORITY_*
CLASSIFY_OUTPUT_TOKENS
ADAPTIVE_OUTPUT_*
PREDICTIVE_KV_TARGET_RATIO
PREDICTIVE_KV_HARD_RATIO
PREDICTIVE_MAX_MODEL_LEN_TOKENS
PREDICTIVE_PREEMPTION_COOLDOWN_SECONDS
PREDICTIVE_PREFILL_REGULAR_TOKENS
PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS
PREDICTIVE_PREFILL_QUIESCENT_TOKENS
PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS
OUTPUT_TOKEN_FIELD_NAMES
```

## Test configuration

Controlled tests may explicitly set cadence, freshness, metrics URL, TPS
reference, window concurrency, running limit, and:

```text
PREDICTIVE_ADMISSION_MODE=shadow
```

Shadow reports the counterfactual policy result but does not return a TPS 429
or reduce Router-visible capacity. Enforce is the production default. Test
overrides are not copied unchanged into production Compose.

## HTTP behavior

- Forwarded public routes are `POST /v1/chat/completions`,
  `POST /v1/completions`, `POST /v1/responses`, and `GET /v1/models`.
- All public routes use the configured bearer policy. Model discovery does not
  create a sequence reservation.
- Unknown paths, method mismatches, encoded aliases, prefixes, suffixes,
  trailing slashes, repeated slashes, and backend-native routes terminate
  locally with a generic OpenAI-shaped HTTP 404 and no backend call.
- Malformed JSON on a generation path returns a bounded OpenAI-shaped HTTP 400
  before admission and forwarding.
- If bounded request inspection cannot prove fanout because of its byte/depth
  limit, content type, read failure, or scanner saturation, PIG charges one
  explicitly labelled fallback sequence through the normal atomic TPS path.
  Scanner limits do not independently return 429; ambiguous, conflicting, or
  overflowing fanout still receives request-scoped protection.
- An admission protection returns HTTP 429 before forwarding and is reflected in
  structured low-cardinality logs and metrics.
- Supported request bodies and application headers are forwarded unchanged.
- A missing, stale, identity-invalid, or internally inconsistent observation
  fails closed in enforce. One failed scrape retains the last coherent snapshot
  until its freshness deadline.

## Runtime policy API

The authenticated process-local API is:

```text
GET   /admin/v1/predictive-policy
PATCH /admin/v1/predictive-policy
```

`tps_reference`, `window_concurrency`, and `running_limit` are independently
mutable. PATCH includes one or more fields plus `expected_revision` and applies
them atomically with compare-and-swap. Only a TPS-reference change resets the
TPS window. An admin running-limit update becomes source `admin`; zero disables
that gate. Restart restores validated initialization values. Responses do not
expose credentials, endpoint URLs, request content, KV geometry, or model
identity.

## Local endpoints

| Endpoint | Purpose |
| --- | --- |
| `/healthz` | Process liveness |
| `/pig/metrics` | Minimal five-gauge Router capacity contract |
| `/v1/metrics` | Full PIG diagnostics plus a bounded upstream metrics copy |
| `/v1/upstream-status` | Router-facing admission status |
| `/admin/v1/predictive-policy` | Read or atomically update TPS and admission bounds |
| `/v1/attestation/report` | Attestation report |

Metrics, management, and attestation endpoints preserve their route-specific
authentication semantics and are handled locally rather than forwarded by the
public proxy policy.

## Development evidence

Executable Go tests, race checks, simulations, benchmarks, and build checks run
on an approved isolated builder. Correctness, atomicity, lifecycle, protocol,
and build checks are required before an image is considered. Historical
production windows and benchmark comparisons are optimization evidence, not
universal numeric hard gates.

- [Documentation map](docs/README.md)
- [Current TPS health-gate plan](docs/PIG_V0_12_23_TPS_HEALTH_GATE_PLAN.md)
- [Advanced configuration](docs/ADVANCED.md)
- [Observability](docs/OBSERVABILITY.md)
- [Internal algorithm flow](docs/PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md)
