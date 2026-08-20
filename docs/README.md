# PIG Documentation Map

Use the current contract documents for implementation and operations. Versioned
plans are retained only as immutable design and release evidence; their CVM
identities, deployment state, commands, and intermediate conclusions are not
standing instructions.

## Current contract

- [README](../README.md): product boundary, request path, production defaults,
  HTTP behavior, and endpoints.
- [Advanced configuration](ADVANCED.md): accepted environment variables,
  production-versus-test boundaries, and runtime policy API.
- [Observability](OBSERVABILITY.md): metrics, compact logs, debug records,
  Router projection, and cross-surface auditing.
- [Internal algorithm flow](PIG_INTERNAL_COMPONENT_ALGORITHM_FLOW.md): package
  ownership, admission transaction, gates, reservations, and backend adapters.

## Historical release evidence

- [v0.12.13 sustained TPS and cleanup](PIG_V0_12_13_SUSTAINED_TPS_REFERENCE_AND_BRANCH_CLEANUP_PLAN.md)
- [v0.12.14 backend adapters](PIG_V0_12_14_VLLM_SGLANG_ADAPTER_PLAN.md)
- [v0.12.15/v0.12.16 cache-aware correction and release](PIG_V0_12_15_SGLANG_KV_GAP_PLAN.md)

Historical plans may explain why a contract exists, but the current source,
tests, and the four current documents above are authoritative for present
behavior. A source commit, test result, image, deployment, and live acceptance
remain separate evidence layers.

## Active maintenance

Branch: `codex/pig-v0.12.17-log-observability`
Status: pre-identity executable acceptance passed on development CVM
`311bbcdb-e348-4922-b37d-541755b09ff7`; runtime and OCI identities are assigned
`v0.12.17`, and identity-specific source acceptance is complete. No image,
Compose, Router, or traffic change is claimed yet.

Plan and progress:

1. Audit current source, documentation, log emitters, and retained history:
   complete.
2. Separate reporting state from log formatting; add compact stable events,
   per-signature suppression, debug detail, and a 30-second status default:
   implemented.
3. Remove unreachable TTFT histogram aggregation and zero-valued derived TTFT
   metrics: implemented.
4. Run focused config/log/status/metrics tests, complete tests, race, vet, build,
   and any latency regression gates in the approved isolated environment:
   complete before identity assignment.
5. Assign `v0.12.17` and repeat identity-specific source gates: complete.
6. Commit and push the accepted source, build and validate an exact-revision
   image, then publish only after image acceptance: pending.
7. Re-read the live host Compose, validate a PIG-only candidate, recreate only
   PIG, and verify runtime/log/metrics behavior without restarting vLLM,
   HAProxy, ingress, or the CVM: pending.

Static review record:

- Model and causality: no admission Gate, request estimator, reservation,
  lifecycle, backend adapter, or policy source file changed.
- Safety and lifecycle: reporter locking still owns counters and suppression;
  log callbacks remain outside the lock and panic-isolated; the new signature
  map is bounded; status uses one current Controller snapshot.
- Evidence and release: clean source archive SHA-256
  `8f5c011e111a5d46e2dc9d6b0827bea7107d72f73e460435b86c62244cd3409b`
  passed formatting, complete tests, complete race, vet, build, deterministic
  simulation (`acceptance=passed`), and hot-path benchmarks in the isolated
  workbench. Online PIG restart count and start time were unchanged throughout.
  The identity-bearing snapshot SHA-256
  `7220ac34b602b8febdf9095181f6bee63d06b56ad871effd9ac3c1235f34f77c`
  then passed focused and complete tests, complete race, vet, build,
  deterministic simulation, and the same benchmarks. Image acceptance remains
  separate and pending. This paragraph is the only post-gate documentation
  status change; executable and Dockerfile inputs are unchanged.
