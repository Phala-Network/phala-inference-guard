# PIG v0.12.26 Waiting Confirmation Plan

Status: active implementation and release plan. Deployment is explicitly out
of scope.

Baseline is the clean, pushed v0.12.25 release branch at
`d32726887ce467aabcd95f012c726326736e70b9`. The production observation on
2026-08-30 showed that one 500 ms SGLang waiting sample immediately closed
Router capacity even while the 60 second mean-active TPS remained above its
reference. The smallest observed case was raw `running=1`, raw `waiting=1`,
mean-active TPS `122.87`, reference `50`, and effective Router capacity `1/1`.

## Objective

Keep waiting protection simple while removing single-sample red-state and 429
flapping:

1. the first nonzero waiting observation below `window_concurrency` remains
   open;
2. a second consecutive nonzero waiting observation protects;
3. waiting at or above `window_concurrency` protects immediately;
4. the first zero-waiting observation reopens immediately;
5. `window_concurrency`, running-limit, TPS-window, preemption, lifecycle, and
   five-line Router metric semantics otherwise remain unchanged.

The first-sample grace is bounded by the existing atomic
`window_concurrency` reservation gate. This change adds no learner, queue,
cooldown, timer, goroutine, debt ledger, production knob, or model-specific
threshold. Admission remains O(1).

## Implementation Contract

- Preserve the previous raw waiting value from the immediately preceding
  accepted backend observation in the immutable projected state.
- Reset previous waiting to zero across controller initialization and backend
  runtime reset.
- Confirm waiting when current and previous raw waiting are both nonzero.
- Escalate a first sample immediately when current raw waiting is at least the
  current `window_concurrency` bound.
- Do not use an historical request rejection or a hold lease to decide current
  waiting protection.
- Keep the existing OpenAI-shaped 429, structured reason, full diagnostics, and
  fixed five-line `/pig/metrics` contract.

## Verification And Release

1. Prove the existing implementation red on the remote Linux builder: first
   waiting observation must be admitted, while confirmed and window-sized
   waiting must protect.
2. Implement the projected-state and gate correction with focused unit,
   controller, policy, metrics, simulation, and race coverage.
3. Push every accepted source commit. On the approved remote builder run the
   legacy audit, formatting check, focused tests, full tests, race, vet, build,
   deterministic simulations, benchmarks, and production image contract.
4. Assign version `0.12.26` only after behavior is green, then repeat the exact
   complete builder matrix on the versioned commit.
5. Publish immutable source and version tags only after the complete builder
   matrix passes. Verify both registry tags resolve to one digest and verify
   OCI version/revision labels and runtime `pig_info` from a fresh pull.
6. Do not deploy, restart, reconfigure, or otherwise mutate any CVM, Router,
   backend, or running PIG instance in this task.
