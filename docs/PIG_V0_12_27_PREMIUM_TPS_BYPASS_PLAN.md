# PIG v0.12.27 Premium TPS Bypass Plan

Status: active implementation and release plan. Deployment is explicitly out
of scope; this task may push source and publish a registry image only.

## Question And Current Finding

The v0.12.26 source does not treat `X-User-Tier: premium` specially. The only
existing premium-header test verifies that the header and request body are
forwarded unchanged. A premium request therefore receives the same TPS,
waiting, preemption, stale-observation, running-limit, and window-concurrency
decision as any other request.

## Objective

Allow a trusted, authenticated request carrying exactly one
`X-User-Tier: premium` value to bypass PIG's TPS/load protection, while keeping
the safety and accounting gates that prevent backend overload:

- bypass only TPS protection (`waiting`, `preemption`, and below-reference TPS);
- continue to require a fresh valid backend observation;
- continue to enforce atomic `window_concurrency` and configured running limit;
- continue to create and release the normal reservation lifecycle;
- keep basic, missing, duplicate, and unknown tier values on the normal path;
- do not change Router metrics, backend routing, request body, or header
  forwarding semantics.

“Direct pass” therefore means premium is not rejected solely by the TPS gate;
it does not mean unlimited forwarding when the observation is stale or the
explicit safety bounds are full.

## Implementation Contract

The HTTP layer owns strict header classification. The admission layer receives
an internal premium-TPS-bypass demand bit through constructors; it does not
parse HTTP headers. Policy evaluation marks the TPS decision as
`premium_bypass`, then runs the existing bounds and reservation transaction.
No global state, timer, queue, learner, model-specific branch, or new
production configuration is introduced.

## Verification And Release

1. Establish a remote-builder red test proving a premium request is currently
   rejected by TPS protection while a basic request remains rejected.
2. Implement the smallest vertical slice and test HTTP behavior, strict header
   parsing, TPS bypass, safety bounds, stale observations, reservation release,
   shadow/enforce behavior, and metrics/evidence mappings.
3. Complete the existing clean-builder matrix, including legacy audit, gofmt,
   focused/full/race tests, vet, build, deterministic simulation, benchmark,
   and production-image contract.
4. Assign `0.12.27` only after the behavior and full matrix are green. Push the
   exact source commit and annotated tag, publish version and source-revision
   image tags, and verify the registry digest/provenance.
5. Do not deploy, restart, reconfigure, or mutate any CVM, Router, Compose,
   backend, production configuration, or running PIG instance.
