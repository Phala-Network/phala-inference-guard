# PIG Documentation Map

PIG documentation separates the current source contract from historical release
and incident evidence. Current behavior must be derived from source, tests, and
the active plan; older versioned plans are not standing configuration guidance.

## Current source contract

- [`../README.md`](../README.md): public behavior, endpoints, and operator-facing
  configuration summary.
- [`ADVANCED.md`](ADVANCED.md): complete current configuration and failure
  semantics.
- [`OBSERVABILITY.md`](OBSERVABILITY.md): current metrics, logs, Router projection,
  and admin API.
- [`PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md`](PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md):
  current component ownership and request lifecycle.
- [`PIG_V0_12_23_TPS_HEALTH_GATE_PLAN.md`](PIG_V0_12_23_TPS_HEALTH_GATE_PLAN.md):
  active design, test, review, and release plan for the TPS health controller.

## Historical and audit-only plans

The remaining versioned plans, continuous-observation reports, and incident
records preserve design decisions, test provenance, release evidence, and live
observations from earlier source states. They may describe retired KV, Prefill,
input-size, learned-capacity, or TPS-derived sequence-limit behavior. Do not copy
their algorithms, defaults, environment variables, image tags, CVM identities,
or deployment procedures into a current deployment without revalidating them
against current source and the active plan.

In particular, documents for v0.12.13, v0.12.15, v0.12.18, v0.12.22, and the
continuous QoS/throughput campaign are retained as historical evidence rather
than deleted or rewritten as current guidance.
