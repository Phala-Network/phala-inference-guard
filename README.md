# Phala Inference Guard

Phala Inference Guard (PIG) v0.12.9 is a single-upstream, predictive admission
proxy for OpenAI-compatible vLLM services. It estimates request size before an
upstream call, combines that estimate with one fresh vLLM observation and all
unabsorbed reservations, and decides whether the post-admit state can preserve
service quality.

The objective is QoS-constrained throughput, not a fixed request-count limit.
Small requests can still fit while a larger request is protected under the same
backend pressure.

A known local weighted, exclusive, or quiescent Prefill temporarily protects
new regular requests until Prefill completion or terminal release. Regular
requests remain work-conserving behind other regular Prefills.

When Decode users are active, PIG also bounds the candidate's total Decode
interference before forwarding. It multiplies post-admit pending Prefill tokens
by effective Decode sequences and compares the product with the immutable
regular-Prefill budget. A rejection from this envelope is request-scoped: it
does not close the node to a smaller request that still fits.

Effective Decode sequences start from fresh backend `running` observations.
Only a Prefill-complete local reservation not yet definitely absorbed by an
observation adds an unobserved Decode sequence. Prefill-incomplete reservations
still charge pending Prefill and KV, but are not double-counted as Decode users.

## Request path

```text
bounded read-only JSON scan
  -> model-agnostic lexical input and output-horizon estimate
  -> fresh vLLM KV, running, waiting, generation and preemption snapshot
  -> current reservations and post-admit KV, Prefill and Decode-interference gates
  -> atomic enforce decision and reservation
  -> unchanged request bytes forwarded to the single upstream
  -> Prefill completion and exact-once terminal release
```

PIG does not route between backends, inspect prefix cache contents, learn KV or
Prefill policy, rewrite request bodies, inject priority, classify customer
tiers, or protect TTFT. Feedback is observation and reconciliation data; it
does not create a second post-response admission controller.

## Production configuration

Production Compose should be small. Do not spell out values that equal the
v0.12.9 defaults.

```yaml
services:
  pig:
    image: ghcr.io/phala-network/phala-inference-guard:0.12.9
    environment:
      - UPSTREAM=http://backend:8000
      - TOKEN=${PIG_TOKEN}
      - TLS_CERT_PATH=/etc/pig/tls/tls.crt
      - ATTESTATION_DSTACK_ENDPOINT=${DSTACK_ENDPOINT}
```

`UPSTREAM` is exactly one absolute HTTP URL. PIG derives the observer endpoint
from its origin as `/metrics`. Predictive admission defaults to `enforce`, the
observer polls every 500 ms, and the maximum observation age defaults to
1500 ms. KV capacity, block size, protected KV limit, and Prefill thresholds
are derived once during startup from the upstream capability profile. Automatic
Prefill initialization reads `max_model_len` once from `/v1/models` and combines
it with the metric-reported KV geometry. It never sends a completion or active
performance probe. If metadata is unavailable, PIG uses a bounded 512 Ki-token
geometry fallback and records `metadata_fallback`.
The bounded request scanner uses a 4 MiB internal ceiling so a model-neutral
650K-token text window remains classifiable under the estimator's six-byte
upper ratio. This safety bound is not a production Compose variable.

`TOKEN`, TLS, and attestation settings are infrastructure values and depend on
the deployment. A production manifest may contain a real non-default policy
choice, but it must not copy the full test matrix into Compose.

## Test configuration

Controlled builder tests and Router-disabled experiments may override many
typed values. Shadow testing explicitly sets:

```text
PREDICTIVE_ADMISSION_MODE=shadow
```

Shadow computes and exposes the counterfactual decision but never returns a
predictive 429, creates no reservation, and does not reduce Router capacity.
Enforce testing removes the variable and proves the production default.

Every test artifact must record its complete override set. A test artifact with
policy overrides is not promoted unchanged to production. See
[Advanced configuration](docs/ADVANCED.md).

## HTTP behavior

- Syntactically malformed JSON on an admitted path returns a bounded
  OpenAI-compatible HTTP 400 before prediction and never reaches the upstream.
- An enforce protection returns HTTP 429 before forwarding and is reflected in
  decision metrics, bounded logs, upstream status, and Router compatibility
  metrics from the same predictive snapshot. A request-scoped
  `decode_interference` rejection keeps upstream status green and Router
  backpressure inactive so a fitting smaller request can proceed.
- Valid supported requests are forwarded with their original application body
  bytes and `Content-Length`.
- Valid unsupported JSON shapes and unknown models remain upstream protocol
  concerns; PIG does not rewrite them into a different request.
- A missing, stale, or identity-invalid observation fails closed in enforce.
  An incomplete individual scrape retains the last coherent snapshot until its
  freshness deadline, avoiding a one-scrape self-lock.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `/healthz` | Process liveness |
| `/pig/metrics` | PIG Prometheus metrics |
| `/v1/metrics` | PIG metrics followed by a bounded upstream metrics copy |
| `/v1/upstream-status` | `0` green, `1` selective pressure, `2` closed, `3` unknown |
| `/v1/attestation/report` | Attestation report endpoint |

Metrics and administrative endpoints require the configured bearer token.

## Development gates

Executable Go tests, race checks, simulations, benchmarks, and image builds for
the v0.12.9 release are run on the dedicated c21 Linux workbench. The release
plan records the exact archive hash, commands, logs, image digest, live gates,
and production observation evidence:

- [v0.12.9 QoS-constrained goodput remediation](docs/PIG_V0_12_3_QOS_CONSTRAINED_GOODPUT_REDESIGN_PLAN.md)
- [v0.12.0-v0.12.2 historical audit](docs/PREDICTIVE_ADMISSION_V0_12_1_CORRECTION_AND_LIVE_VALIDATION_PLAN.md)
- [Observability](docs/OBSERVABILITY.md)
- [Internal algorithm flow](docs/PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md)
