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
- [v0.12.18 active optimization plan](PIG_V0_12_18_THROUGHPUT_ESTIMATOR_PLAN.md):
  current estimator, TPS, Prefill-lifecycle, evidence, and release work. The
  behavior candidate and exact-source remote gates are complete; the executable
  version identity, image, deployment, and live acceptance remain separate and
  incomplete.

## Historical release evidence

- [v0.12.13 sustained TPS and cleanup](PIG_V0_12_13_SUSTAINED_TPS_REFERENCE_AND_BRANCH_CLEANUP_PLAN.md)
- [v0.12.14 backend adapters](PIG_V0_12_14_VLLM_SGLANG_ADAPTER_PLAN.md)
- [v0.12.15/v0.12.16/v0.12.17 cache-aware correction and release](PIG_V0_12_15_SGLANG_KV_GAP_PLAN.md)

Historical plans may explain why a contract exists, but the current source,
tests, and the four current documents above are authoritative for present
behavior. A source commit, test result, image, deployment, and live acceptance
remain separate evidence layers.

## Active maintenance

Branch: `codex/pig-v0.12.18-throughput-estimator`
Status: v0.12.17 remains the accepted and published runtime while the separate
v0.12.18 behavior candidate has completed its implementation, three final
reviews, and exact-source remote gates at commit `d2aa6fb`; it has not yet been
assigned an executable version or built as an image. v0.12.17 PIG-only
deployment, Router contract, and 30-minute live-traffic acceptance are complete
on development CVM
`311bbcdb-e348-4922-b37d-541755b09ff7` (`use1-19`). Executable image revision is
`0091241bc9edc30f0f7ff50010504225d3fa14c8`; later documentation-only commits do
not change that image identity.

A later read-only audit found one vLLM host-memory OOM more than three hours
after the formal window. PIG, HAProxy, and ingress did not restart; PIG closed
on stale backend observations and reopened after vLLM recovered. This does not
change the complete formal-window result or PIG executable identity, but the
serving chain must not be described as lifecycle-clean beyond that measured
window. The versioned release plan records the exact boundary and current
recovery state.

Completed v0.12.17 plan and progress:

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
   image, then publish only after image acceptance: complete; published digest
   `e96b3a5a0864f8d8c57f39dbfa289402ecac0a7eb0eee42efaa9a23825e504f8`.
7. Re-read the live host Compose, validate a PIG-only candidate, recreate only
   PIG, and verify runtime/log/metrics behavior without restarting vLLM,
   HAProxy, ingress, or the CVM: complete.
8. Restore the exact pre-change Router enabled set and run an uninterrupted
   1800-second/5-second live observer with logs, metrics, container identities,
   capability geometry, and checksum finalization: complete; 360/360 samples,
   zero collector errors.

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
  workbench. During that pre-identity isolated validation, the online PIG
  restart count and start time were unchanged.
  The identity-bearing snapshot SHA-256
  `7220ac34b602b8febdf9095181f6bee63d06b56ad871effd9ac3c1235f34f77c`
  then passed focused and complete tests, complete race, vet, build,
  deterministic simulation, and the same benchmarks. The exact revision image
  then passed isolated image acceptance before publication. On `use1-19`, the
  final live window measured 34.66 mean-active tokens/s per sequence, 895.92
  completed-output tokens/s, 90.99% mean GPU utilization, and zero preemptions,
  proxy failures, restarts, OOMs, or fatal matches. The complete result and
  evidence hashes are recorded in the v0.12.15/v0.12.16/v0.12.17 versioned
  release plan. This paragraph is documentation-only; executable and Dockerfile
  inputs remain those of revision `0091241`.
