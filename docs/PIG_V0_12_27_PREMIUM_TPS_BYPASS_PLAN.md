# PIG v0.12.27 Premium Unlimited-Pass Plan

Status: active implementation and release plan. Deployment is explicitly out
of scope; this task may push source and publish a registry image only.

## Question And Current Finding

The v0.12.26 source does not treat `X-User-Tier: premium` specially. The only
existing premium-header test verifies that the header and request body are
forwarded unchanged. A premium request therefore receives the same TPS,
waiting, preemption, stale-observation, running-limit, and window-concurrency
decision as any other request. The user contract now explicitly requires
“无限放行”: PIG must not impose an admission decision on this trusted tier.

## Objective

Allow a trusted, authenticated request carrying exactly one
`X-User-Tier: premium` value to take a PIG admission-free fast path:

- keep strict method + exact-path public routing and all local-management
  routing checks;
- keep the existing bearer authentication check;
- forward directly to the configured backend without request-body
  classification or parsing;
- bypass TPS, waiting, preemption, stale-observation, running-limit, and
  window-concurrency gates;
- do not create, mark, or terminate a PIG reservation and do not increment
  admission attempts/reject counters;
- keep basic, missing, duplicate, and unknown tier values on the normal path;
- preserve backend routing, request body, streaming, and header forwarding.

“无限放行” is limited to PIG admission. The backend may still return its own
errors, timeouts, overload responses, or connection failures. The header is a
trusted ingress signal: Router/HAProxy must inject it and remove any
client-supplied duplicate before the request reaches PIG. PIG does not modify
Router or HAProxy in this release.

## Implementation Contract

The HTTP layer owns strict header classification through
`request.IsPremiumTier`. The fast path is evaluated only after local
management routing, public exact-route validation, and bearer authentication,
and before `AdmissionRoutePolicy` classification. It calls the existing
backend proxy directly and therefore cannot touch admission state. The
admission package remains tier-agnostic; no premium flag, TPS subreason, metric
label, global state, timer, queue, learner, model-specific branch, or new
production configuration is introduced.

## Verification And Release

1. Preserve the remote-builder red evidence showing v0.12.26 rejects premium
   under TPS protection while a basic request remains rejected.
2. Implement and test the admission-free fast path, including full running and
   window limits, malformed/large bodies, streaming, backend errors, strict
   header cardinality/casing, and proof that classifier/admission/reservation
   counters remain untouched. Confirm auth, exact routing, and local-management
   interfaces cannot be bypassed by the header.
3. Complete the clean remote-builder matrix: gofmt, focused/full/race tests,
   vet, build, deterministic simulation, benchmark, and production-image
   contract. Review the diff three times for route/auth safety, lifecycle
   isolation, and release evidence.
4. Assign `0.12.27` only after the behavior and full matrix are green. Push the
   exact source commit and annotated tag, publish version and source-revision
   image tags, and verify the registry digest/provenance.
5. Do not deploy, restart, reconfigure, or mutate any CVM, Router, Compose,
   backend, production configuration, or running PIG instance.
