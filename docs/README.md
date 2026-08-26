# PIG Documentation Map

Use current contract documents for implementation and operations. Versioned
plans are historical design and evidence records; their CVM identities,
deployment state, commands, thresholds, and intermediate conclusions are not
standing instructions.

## Current contract

- [README](../README.md): product boundary, request path, production defaults,
  HTTP behavior, and local endpoints.
- [Advanced configuration](ADVANCED.md): current environment variables,
  production-versus-test boundaries, and runtime policy API.
- [Observability](OBSERVABILITY.md): current metrics, compact logs, debug
  records, Router projection, and audit guidance.
- [Internal algorithm flow](PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md): current
  TPS-only ownership and pre-forward transaction.
- [v0.12.22 TPS-only controller plan](PIG_V0_12_22_TPS_ONLY_CONTROLLER_PLAN.md):
  active source and release evidence for the TPS-only controller. Consult its
  current execution state before treating source, image, or deployment as
  complete.

## Historical plans

- [Continuous QoS and throughput optimization](PIG_CONTINUOUS_QOS_THROUGHPUT_OPTIMIZATION_PLAN.md)
- [v0.12.13 sustained TPS and cleanup](PIG_V0_12_13_SUSTAINED_TPS_REFERENCE_AND_BRANCH_CLEANUP_PLAN.md)
- [v0.12.14 backend adapters](PIG_V0_12_14_VLLM_SGLANG_ADAPTER_PLAN.md)
- [v0.12.15-v0.12.17 cache-aware releases](PIG_V0_12_15_SGLANG_KV_GAP_PLAN.md)
- [v0.12.18 throughput estimator](PIG_V0_12_18_THROUGHPUT_ESTIMATOR_PLAN.md)
- [v0.12.19 backend epoch and KV rebind](PIG_V0_12_19_BACKEND_EPOCH_REBIND_PLAN.md)
- [v0.12.20 strict public route policy](PIG_V0_12_20_STRICT_PUBLIC_ROUTE_POLICY_PLAN.md)
- [v0.12.21 legacy vLLM KV geometry](PIG_V0_12_21_LEGACY_VLLM_KV_GEOMETRY_PLAN.md)
- [superseded v0.12.22 cache/KV plan](PIG_V0_12_22_CACHE_KV_TPS_CONTROLLER_PLAN.md)

Current source, tests, and the active TPS-only plan supersede those historical
plans where they conflict. Keep evidence layers explicit: plan, source,
focused builder tests, complete builder matrix, commit/push, image, Compose,
deployment, readiness, and live observation are different states.
