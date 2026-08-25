# PIG v0.12.20 Strict Public Route Policy Plan

Status: source candidate accepted; source push and release stages in progress

## Objective

Restore the public protocol boundary that existed before PIG became the direct
backend proxy. PIG must forward only an explicit OpenAI-compatible method and
path set. Every other public request must terminate locally before body
scanning, authentication, admission, reservation, capacity projection, or an
upstream call.

This is a PIG-only security and lifecycle fix. It does not change Router,
vLLM, SGLang, vllm-proxy, predictive QoS thresholds, or model behavior.

## Current defect

At source commit `289ef887358f89440af98c46b8b079c4db44dd0e`, `ServeHTTP`
calls `forwardWithoutAdmission` for every request that is not classified by
`PIG_PATHS`. `API_AUTH_PATHS` defaults to the same admitted paths. A direct
PIG-to-backend deployment therefore exposes backend-native routes without PIG
admission and normally without PIG API authentication.

The strict route policy must explicitly block at least:

- `/v1/tokenize`
- `/tokenize`
- `/generate`
- `/v1/generate`
- `/encode`
- `/decode`
- `/detokenize`
- `/pooling`
- `/score`
- `/rerank`

Adding authentication or admission to these routes is not an acceptable fix.

## Policy ownership

Four policies have distinct responsibilities:

1. `PublicRoutePolicy` owns the fixed method plus canonical exact-path public
   forwarding whitelist.
2. `AdmissionRoutePolicy` owns which already-allowed public routes enter the
   predictive admission transaction.
3. `AuthenticationPolicy` owns authentication of every already-allowed public
   route while preserving the existing `API_AUTH_ENABLED` switch.
4. `LocalManagementRoutePolicy` owns every PIG-local endpoint. Local routes
   never reach the backend and retain their existing authentication and method
   semantics in their existing handlers.

`PIG_PATHS`, `API_AUTH_PATHS`, and suffix matching must not authorize or widen
the public forwarding surface. Obsolete route configuration should be removed
rather than retained as a misleading no-op.

## Public contract

Allowed public forwarding routes:

| Method | Exact path | Authentication | Admission |
| --- | --- | --- | --- |
| `POST` | `/v1/chat/completions` | existing public bearer policy | yes |
| `POST` | `/v1/completions` | existing public bearer policy | yes |
| `POST` | `/v1/responses` | existing public bearer policy | yes |
| `GET` | `/v1/models` | existing public bearer policy | no |

All method mismatches and all other paths return a generic OpenAI-shaped HTTP
404 locally. The response must not reveal the requested path or backend.

Query strings do not participate in route identity and are preserved for an
allowed route. The route path itself must be canonical. Trailing slash,
repeated slash, dot segments, encoded path bytes, encoded slash, case changes,
prefixes, suffixes, absolute-form targets, opaque URLs, and fragments fail
closed instead of being cleaned into an allowed route.

## PIG-owned local endpoint inventory

The source audit found the following complete local surface at the baseline
commit:

| Path | Existing method behavior | Existing auth behavior |
| --- | --- | --- |
| `/healthz` | any method returns `200 ok` | none |
| `/pig/metrics` | no method restriction | bearer required |
| `/v1/metrics` | no method restriction | bearer required |
| `/v1/upstream-status` | no method restriction | bearer required |
| `/admin/v1/predictive-policy` | `GET` and `PATCH`; otherwise 405 | bearer required before method handling |
| `/v1/attestation/report` | `GET`; otherwise 405 | bearer required before method handling |

No additional compatibility alias is registered in the current source. Each
listed path must be matched canonically and handled locally. This plan does not
change the handler-specific semantics above.

## Current topology audit

Read-only control-plane checks on 2026-08-25 found the active Gemma4 nodes
`use2-19`, `use2-3b`, `use2-4c`, and `use2-5d` running PIG `0.12.19` with
`UPSTREAM=http://vllm:8000`. They are direct PIG-to-vLLM deployments. The user
has explicitly removed `use1-4c` and `use2-9b/bb/cb/db` from the current
Gemma4 scope.

The two current tracked `production/**/docker-compose.yaml` PIG deployments are
direct PIG-to-SGLang topologies. No current tracked production Compose uses
PIG-to-vllm-proxy. Historical files are not treated as live evidence.

Strict public routing is therefore required independently of backend type.

The reference `dstacktee/vllm-proxy:v0.2.20` source was re-audited at commit
`4120cd264f4a1b7ad03eb2f79be3c255660e1f65`. Its explicit FastAPI surface is
`POST /v1/chat/completions`, `POST /v1/completions`, `GET /v1/models`,
`GET /v1/metrics`, `GET /v1/attestation/report`,
`GET /v1/signature/{chat_id}`, and the instrumentator-owned
`GET /local-metrics`. It has no catch-all and no Responses route. PIG does not
inherit the proxy-specific signature or local-metrics routes: PIG's own
metrics and attestation handlers remain local, Responses is explicitly part of
the PIG public contract, and every other proxy/backend route stays blocked.

## Test-first acceptance matrix

Red tests must compile against the baseline and fail because an unknown or
mismatched route reaches the old catch-all path.

Required green evidence:

- Each blocked native path returns the same OpenAI-shaped 404 with missing,
  valid, wrong, and duplicate bearer headers.
- Blocked and mismatched requests cause zero backend calls, zero admission
  decisions, zero reservations, zero body reads, and no admission/capacity
  counter changes.
- Encoded, prefixed, suffixed, trailing-slash, repeated-slash, dot-segment,
  case-mismatched, absolute-form, and opaque targets are rejected locally.
- Query strings remain allowed on the four exact public routes.
- Every public route rejects missing, wrong, and duplicate bearer headers when
  public auth is enabled.
- The three generation routes retain body classification, admission,
  reservation lifecycle, streaming, structured output, tool calls, Responses,
  and upstream error behavior.
- `GET /v1/models` forwards only after auth, does not reserve, and preserves
  headers and query.
- Backend unavailability cannot change a blocked request from local 404 into
  429 or 503.
- Every PIG-owned local endpoint remains local and retains its current method
  and auth contract.
- `pig_route_not_allowed_total` increments exactly once per local public-route
  rejection. A structured `route_not_allowed` log uses only fixed low-cardinality
  classes and never logs body, authorization, query, or raw path.

## Implementation sequence

1. Add behavioral red tests and record the intended baseline failures.
2. Add the four route policy components and move route classification to the
   first operation in `ServeHTTP`.
3. Remove public catch-all forwarding and obsolete suffix/path configuration.
4. Add the generic OpenAI 404 and low-cardinality log/metric.
5. Run focused tests, then the full Go suite, race tests, formatting/static
   checks, and build on the approved remote Linux environment.
6. Complete three review passes: policy/SOLID, security/lifecycle, and
   evidence/release. Revise after each finding.
7. Set source version `PIG-v0.12.20`, update operator documentation, audit the
   diff, commit, and push the source branch.
8. Build and publish `0.12.20` only after the exact pushed commit passes the
   clean-builder matrix and image smoke tests.
9. Treat Compose update, canary deployment, readiness, Router restoration, and
   expansion as separate authorized stages. Never restart a CVM merely to
   replace PIG.

## Release decision

The r5 source candidate passed the focused and complete Linux builder matrix
and is approved for source commit and push. No image or runtime is approved by
this plan yet. Registry publication requires exact-pushed-commit builder and
image smoke evidence. Deployment remains a separate canary/drain/readiness
stage.

## Three-pass review

### Pass 1: policy and SOLID

- `PublicRoutePolicy`, `AdmissionRoutePolicy`, `AuthenticationPolicy`, and
  `LocalManagementRoutePolicy` have distinct ownership and are wired before
  request classification.
- The public route set has one exact generation-path definition shared by the
  public and admission policies. No configurable catch-all, suffix match, or
  duplicate authentication path list remains.
- Search over executable source and current operator documentation found no
  remaining `PIG_PATHS`, `API_AUTH_PATHS`, `PathSuffixMatch`, `QoSPaths`,
  `AdmittedPath`, or `forwardWithoutAdmission` reference.
- Result: pass; no behavior revision required.

### Pass 2: security and lifecycle

- The canonical path contract rejects raw/encoded aliases, encoded slash,
  trailing/repeated slash, dot segments, prefix/suffix, case drift,
  absolute-form, opaque, fragment, and request-target mismatch before auth or
  body handling. Query is excluded from route identity and preserved.
- A real `httptest.Server` plus raw TCP request line test proves Go's HTTP
  parser cannot turn encoded, repeated-slash, or absolute-form targets into a
  backend call.
- Blocked requests prove body reads, admission decisions, live reservations,
  scanner capacity, 429s, backend availability, backend inflight/accepted/
  failed/completed/proxy-error counters, and admission attempt/reservation
  metrics remain zero. Only `pig_route_not_allowed_total` increments once.
- All six baseline PIG-local paths retain their local handler, method, and auth
  semantics; encoded local aliases fail closed.
- Result: pass; no behavior revision required.

### Pass 3: evidence and release

- Red evidence was a behavioral failure on the exact baseline rather than a
  compile or fixture failure.
- Green r5 used a new exact archive and evidence directory. The first focused
  runner failed before formatting/tests because its login shell could not find
  `gofmt`; the failure was retained and the corrected runner used absolute Go
  tool paths without changing source.
- Focused route tests, formatting, vet, all packages, race, and binary build
  passed. The frozen estimator performance test also passed in the complete
  matrix; no threshold or estimator source changed.
- Source, pushed commit, image, registry, Compose, deployment, and live
  readiness remain separately gated.
- Result: pass; source commit/push approved, image/deployment not yet approved.

## Evidence log

- Baseline branch: `codex/pig-v0.12.19-backend-epoch-rebind`
- Baseline source: `289ef887358f89440af98c46b8b079c4db44dd0e`
- Baseline remote: identical to local before edits
- Baseline worktree: clean before edits
- Baseline production PIG image: `0.12.19` digest
  `sha256:deae693e8b0a030ac7a93ee4a2b2cef6c46efa7d8ade8b9a03bc259e3e42e1d2`
- Red source archive SHA-256:
  `bd497eaba6f8def0eeea1cec8c03f33c85fee448ff365344ac524f567b5d13a6`
- Red focused test: expected exit `1`; log SHA-256
  `2a4baa5f7614786717e78f46c3b06b775161098cb9d29b7b371f55dea2e21c79`
- Red cause: every explicitly blocked backend-native path, method mismatch,
  canonical bypass, and unauthenticated models request reached the old
  catch-all; backend unavailability changed `/generate` into HTTP 429.
- Green r4 source archive SHA-256:
  `0190cb6ecf51ad8dfda38f9f6d6ee292312f22e41b00d708cf1da01fd1ee2d8a`
- Green r4 format diff: empty; route/config/request/openai packages passed.
  The combined focused command was not accepted because the unchanged 4 MiB
  estimator performance test measured p99 `115.18 ms`; its isolated rerun
  measured `100.92 ms`, above the frozen `100 ms` gate. Baseline pairing and a
  final stable-window full matrix remain required.
- Green r5 exact source archive SHA-256:
  `90dcffed176dcf7033dbf60b4714d59b9b141731cac862f5efcbb594af5fbce1`.
  Builder evidence directory:
  `/var/volatile/dstack/persistent/.cache/pig-v01220-strict-route/green-r5-90dcffed`.
- Builder Go image ID: `sha256:e0cffc405270b9114fac7706d07c373727d1b42b0e47c525b9cd1ab1097779ff`;
  repo digest:
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
- The first r5 focused runner stopped before tests because `gofmt` was absent
  from its login-shell `PATH`; retained failure log SHA-256:
  `ee4916a67519170410a5aac7abccc8435854fdad28d990c73737d6df4ecb37d1`.
- Corrected r5 formatting and strict-route focused tests passed; log SHA-256:
  `d079fcdec88de2692199accf31bcb095790b29b0ef6ce33684474f7a60a0a8e3`.
- Complete r5 matrix passed. Log SHA-256 values: vet
  `39104f880de0ffdd58aa15a00d98079e713b227957166da382d33a1a457ae61e`,
  all packages
  `50827a0cfb7a36ac80818f31a9e245f2a355f1f1a39facf4c5ed6fa043332803`,
  race
  `ef4ea14cfdd44f5dd11c12327efa53eecdb220bafa9e39910c00d93ecd4afeb2`,
  and build
  `39104f880de0ffdd58aa15a00d98079e713b227957166da382d33a1a457ae61e`.
  Matrix marker SHA-256:
  `0657406b866c50cb4bbf25f39a32d8db72074a4e681d81fdf0a8a6c25330aa22`.
- Commit, pushed source, image, and deployment evidence: pending
