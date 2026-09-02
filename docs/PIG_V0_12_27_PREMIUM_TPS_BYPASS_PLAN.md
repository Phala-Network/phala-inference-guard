# PIG v0.12.27 Premium Unlimited-Pass Plan

Status: withdrawn/superseded. The source and image remain immutable historical
artifacts, but the premium admission-bypass behavior must not be deployed or
used as the current QoS contract. See
`PIG_V0_12_28_PRIORITY_QOS_WINDOW_PLAN.md` for the corrective behavior.

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

## Final Evidence And Decision (2026-09-02)

The accepted executable source is commit
`ad1b8f8ad173eb3a9e4d5481debd6631015f175e`; annotated tag `v0.12.27` points
to that commit on both configured PIG remotes. The source branch was pushed to
`codex/pig-v0.12.27-premium-tps-bypass` on both remotes. The preserved red
evidence from v0.12.26 is under
`/var/volatile/dstack/persistent/.cache/pig-v01227-premium/red-dc1603-r1`
with log SHA-256
`b6f42cb52d3078ae2be77cc4015c39558a277b75d60d1fd4faef5cd0fb606062` and
environment SHA-256
`87c79ba3f3d7c35877b9abeffb86ff6bed5888a629e32e55b2edbfefe0bea9bf`.
It failed for the intended reason: a premium request received HTTP 429 with
`tps_reference`/`waiting` and made zero backend calls.

The clean remote-builder matrix ran on builder app
`ff40ee31b95e89ebb242c223514adc715ac8a301` (Linux amd64, Docker 25.0.3,
Go image `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`,
Go 1.24.13) at evidence directory
`/var/volatile/dstack/persistent/.cache/pig-v01227-premium/green-ad1b8f8-r7`.
The source archive SHA-256 was
`9e428b18d8b222a6b7da46e77ec538780831380ef82f1756fd9334a368f4a3c2`.
All of the following exited zero: legacy audit, gofmt, focused tests,
`go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, and
two byte-identical `pig-tps-controller-sim` runs. Evidence file hashes are:

```text
builder-environment.txt  8ad17f0e0ab1122a99613d2b504cae62b70d487762d7493024ea58658186247b
source-environment.txt   8fac56b7dcf3354d26db130881266c29ca85efd9b820fcb2b95b4f72555d3738
gofmt.diff               e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
legacy.log               455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0
focused.log              3ec86cd6c8e36f146821b02bfbcb80656e5fe9d3f5a386de51e716ba0b9155c6
full.log                 0f9dc138b8618050f22190b83686289f1961eee2ea3163b219dcfc14119fc3e5
race.log                 c678bdd46cacde8b9672c614ecf02a6631596459b1c04f1cec6800f1d55c1d2e
vet.log                  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
build.log                e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
simulation-1.json        25878d06c4f9b241a3e10773813106bd76c0366907b3344adc74a8d2f76ae7a4
image-build.log          41f844dcd861b9d8b51e71bef17465722ce519fa30ca45c0ba07d058582858e3
image-contract.log       0e9dea6382fc86d896deb7f318c8f188d7813d3210d338fc1c358e29e1e55199
```

The builder host had no Docker buildx plugin, so its local image check used
classic Docker only and did not publish from that host. The actual registry
publication was performed by GitHub Actions workflow run
`33590761355` (success, 54 s):
<https://github.com/Phala-Network/phala-inference-guard/actions/runs/33590761355>.
The workflow's production-image contract passed and its BuildKit log recorded
the source/base provenance metadata. GHCR tags
`ghcr.io/phala-network/phala-inference-guard:v0.12.27` and
`ghcr.io/phala-network/phala-inference-guard:v0.12.27-ad1b8f8ad173` both resolve
to the same digest
`sha256:5f4c197c9ad8eb3ac9d61abef3657664e63ef579efc3e569c5655439780423e0`.
A fresh pull on the builder reported image ID
`sha256:08f1497623124e0849695a7aec15f5f6b74f8a3460560466525ece91110302f8`,
OCI version `0.12.27`, and the exact 40-character source revision. The
registry API confirmed both tags are attached to the same package version.

Three final review passes found no required code change:

1. Model/causality: the premium branch is after local-management routing,
   exact public routing, and bearer authentication, and before classifier and
   admission; ordinary tiers still use the unchanged admission path.
2. Safety/lifecycle: premium creates no admission decision or reservation and
   therefore cannot leak or double-release one; local management and backend
   error handling remain authoritative; duplicate/unknown headers are not
   premium; no body is parsed by PIG on the fast path.
3. Evidence/release: source, tag, remote-builder matrix, image contract,
   workflow, registry digest, and scope boundaries are recorded above.

Final scope: source pushed and image published; no CVM deployed or restarted,
no Router/HAProxy/Compose/backend changed, and no production inference request
sent. The release is complete at the published-image layer only.
