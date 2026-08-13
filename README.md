# Phala Inference Guard

Phala Inference Guard (PIG) is a single-upstream predictive-admission proxy for
OpenAI-compatible vLLM services. It estimates request size before an upstream
call, combines that estimate with one Controller-owned coherent vLLM observation
and every live reservation, and decides whether the post-admit state can
preserve service quality.

The objective is QoS-constrained throughput, not a fixed request-count limit.
Small requests can still fit while a larger request is protected under the same
backend pressure.

The Controller first checks availability, then applies independent `ContextGate`,
`KVGate`, and `PrefillGate` decisions to one immutable projected state. The gates
protect observation freshness, model identity, the upstream input ceiling,
post-admit KV, and Prefill interference. `PrefillGate` applies
request-size differentiation: under current contention, fitting regular
requests share a bounded 64K pending-Prefill budget; when open, regular and
weighted requests share a 256K aggregate budget, while exclusive and quiescent
requests require progressively quieter state. A large request-specific reject
does not close the node when the canonical minimum request still fits.

Instantaneous TPS and generation deltas are telemetry, not admission gates. A
fresh preemption selects the contended regime for one coherent sample only; it
does not create a cooldown or delayed capacity lock.

## Request path

```text
bounded read-only JSON scan
  -> model-agnostic lexical input and output-horizon estimate
  -> Controller-owned vLLM KV, running, waiting and preemption observation
  -> positive reservation overlay and post-admit Context/KV/Prefill gates
  -> same-snapshot canonical probe for request versus load scope
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
accepted image defaults.

```yaml
services:
  pig:
    image: ghcr.io/phala-network/phala-inference-guard:<accepted-version>
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
Prefill initialization reads `max_model_len` from `/v1/models` and combines it
with the metric-reported KV geometry. The response must contain exactly one
model matching the metric identity and a positive `max_model_len`; missing,
ambiguous, or inconsistent metadata fails startup. After a monotonic-counter
reset, an automatically initialized profile revalidates the same bounded
metadata before the reset sample can reopen intake. Ordinary 500-ms polls do
not call `/v1/models`. PIG never sends a completion or active performance probe.
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
predictive 429 or reduces Router capacity. It tracks the same lifecycle as
enforce for policy-admitted requests; a policy-protected request is forwarded
without a hypothetical reservation because enforce would not have admitted it.
Enforce testing removes the variable and proves the production default.

Every test artifact must record its complete override set. A test artifact with
policy overrides is not promoted unchanged to production. See
[Advanced configuration](docs/ADVANCED.md).

## HTTP behavior

- Syntactically malformed JSON on an admitted path returns a bounded
  OpenAI-compatible HTTP 400 before prediction and never reaches the upstream.
- An enforce protection returns HTTP 429 before forwarding and is reflected in
  decision metrics and bounded logs. Controller assigns `request`, `load`, or
  `availability` scope by evaluating the candidate and a canonical minimum
  request under one lock and one state snapshot. A request-scoped rejection
  keeps Router capacity open when that canonical request still fits.
- Valid supported requests are forwarded with their original application body
  bytes and `Content-Length`.
- A valid request that PIG cannot estimate safely is protected as an
  `invalid_request` in enforce and forwarded without a hypothetical reservation
  in shadow. This decision is request-scoped: it does not close canonical node
  capacity or lock a later fitting request. PIG never rewrites the body.
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

Executable Go tests, race checks, simulations, benchmarks, and any later image
build are run on the dedicated c21 Linux workbench. The current development
branch has not been assigned another `0.12.x` release identity and has no
accepted image. The current clean-rebuild plan records exact source commits,
commands, evidence, and
the gates that must pass before versioning or image work:

- [Clean admission rebuild](docs/PIG_V0_12_CLEAN_ADMISSION_REBUILD_PLAN.md)
- [Historical QoS-constrained goodput redesign](docs/PIG_V0_12_3_QOS_CONSTRAINED_GOODPUT_REDESIGN_PLAN.md)
- [v0.12.0-v0.12.2 historical audit](docs/PREDICTIVE_ADMISSION_V0_12_1_CORRECTION_AND_LIVE_VALIDATION_PLAN.md)
- [Observability](docs/OBSERVABILITY.md)
- [Internal algorithm flow](docs/PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md)
