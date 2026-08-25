# PIG v0.12.20 Strict Public Route Policy Plan

Status: complete; source, image, four-node rollout, Router restoration, and final audit passed

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

Read-only control-plane checks on 2026-08-25 found the rollout targets
`use2-19`, `use2-3b`, `use2-4c`, and `use2-5d` running PIG `0.12.19` with
`UPSTREAM=http://vllm:8000`. They are direct PIG-to-vLLM deployments. The user
excluded `use1-4c` and `use2-9b/bb/cb/db` from deployment. `use1-4c` remained
part of the pre-existing Router enabled set and was preserved unchanged;
`use2-9b/bb/cb/db` no longer serve Gemma4 and were neither deployed nor
enabled.

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
   expansion as separate authorized stages. Do not explicitly restart or
   redeploy after an accepted Compose update. The current non-dev control-plane
   Compose operation nevertheless restarts the whole CVM; every target must
   therefore remain disabled until vLLM finishes its complete cold start.

## Release decision

Release complete. Commit
`85bb6e4084b437cfc6e9320f97e58d134176d4b6` is pushed on
`codex/pig-v0.12.20-strict-public-routes`. The exact pushed commit passed the
clean Linux builder matrix, image smoke, and anonymous digest-pull identity
gate. Registry digest
`sha256:fa433bbd8d6fdb4696542f1954944d41649ef4fc6dfe457b4b108f4b6bf22c70`
was deployed serially to `use2-19`, `use2-3b`, `use2-4c`, and `use2-5d`.

Each node passed strict-route, management-route, auth, PIG/backend readiness,
and two-minute natural-traffic gates before Router restoration. The final
read-only fleet audit passed with the exact original enabled set and no OOM,
Xid, panic, or restart-loop markers. The release does not change predictive
QoS behavior or authorize any wider public forwarding surface.

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
  readiness remained separate gates throughout the release.
- The exact pushed commit matrix repeated formatting, focused strict-route
  tests, vet, all-package tests, race tests, binary build, and image smoke.
- The non-dev control-plane Compose update restarted each CVM and vLLM even
  though the only Compose content change was the PIG image. Early runner
  failures based on the invalid no-vLLM-restart assumption were retained; the
  already-applied candidates were validated without redeploying. Later nodes
  explicitly accepted the observed CVM restart and waited for full vLLM
  readiness.
- Every target was disabled and drained independently, validated, restored,
  and observed under natural traffic before the next node was changed.
- Result: pass; source, image, registry, four-node deployment, Router
  restoration, and final fleet audit approved.

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
- Behavior commit:
  `85bb6e4084b437cfc6e9320f97e58d134176d4b6`; remote branch
  `pig-origin/codex/pig-v0.12.20-strict-public-routes` resolved to the same
  commit after rollout.
- Exact-commit builder archive SHA-256:
  `218192656de969497d0d01507b2a5fecf2615f8fd935c99c40565e2450932e15`;
  builder directory:
  `/var/volatile/dstack/persistent/.cache/pig-v01220-strict-route/commit-85bb6e4-21819265`.
- Exact-commit builder gates: formatting, focused strict-route tests,
  `go vet ./...`, `go test -count=1 ./...`,
  `go test -race -count=1 ./...`, binary build, image smoke, and anonymous
  registry digest-pull/OCI identity all passed.
- Image tags:
  `ghcr.io/phala-network/phala-inference-guard:0.12.20` and
  `ghcr.io/phala-network/phala-inference-guard:0.12.20-85bb6e4084b4`.
  Registry digest:
  `sha256:fa433bbd8d6fdb4696542f1954944d41649ef4fc6dfe457b4b108f4b6bf22c70`;
  image ID:
  `sha256:86cdb75419c1649f998f322c5872c7e833255bf1601f96c83202c8c8bad67f43`.

## Production rollout record

Only `use2-19`, `use2-3b`, `use2-4c`, and `use2-5d` were changed. The candidate
Compose for each node replaced exactly one PIG image reference. No Router,
vLLM, SGLang, or vllm-proxy source or configuration was changed.

| Node | Final Compose SHA-256 | Natural processed | Backend accepted/completed | Backend failed/proxy errors | Result |
| --- | --- | ---: | ---: | ---: | --- |
| `use2-19` | `646f28c8c9d7b4dd75de341b89df11a40d11d5f453e06e45737fb9f305e4c650` | 71 | 76 / 74 | 0 / 0 | passed, restored |
| `use2-3b` | `b5bd8f170430f870038f5a7b0ff553f037eabe32ca1c497d33afdb129077bc7c` | 28 | 29 / 29 | 0 / 0 | passed, restored |
| `use2-4c` | `5d7f326c3843e1bfadf33158803605a3be090195df598b5c8d34a6c677ada039` | 41 | 41 / 39 | 0 / 0 | passed, restored |
| `use2-5d` | `9936c7f9e39ad9878ab1e3a36dbd9194d5f9009ccd9858e9e55dbc70f10bb054` | 27 | 30 / 29 | 0 / 0 | passed, restored |

The natural-traffic window was 120 seconds per node. Counter differences can
include requests that were accepted just before the first sample or completed
just after the last sample; release gating used zero increase in backend failed
and proxy-error counters plus a stable vLLM process epoch, not equality among
processed, accepted, and completed deltas.

All four control-plane Compose updates restarted the entire non-dev CVM and
therefore changed the vLLM process epoch. Each node stayed disabled throughout
weights loading, KV allocation, CUDA graph capture, tokenizer/template setup,
and multimodal warmup. No manual CVM restart and no second deployment of an
already-accepted candidate occurred.

Per-node runtime validation proved:

- PIG reports `pig_info{version="PIG-v0.12.20"} 1`.
- Authenticated `GET /v1/models` returns 200 and unauthenticated access returns
  401.
- `/healthz`, `/pig/metrics`, `/v1/metrics`, `/v1/upstream-status`,
  `/admin/v1/predictive-policy`, and `/v1/attestation/report` remain local and
  return their expected production statuses; unauthenticated PIG metrics return
  401.
- `/generate`, `/v1/tokenize`, wrong-method `/v1/models`, trailing-slash chat,
  and an encoded chat alias return the generic OpenAI-shaped 404.
- The blocked-route gate increments `pig_route_not_allowed_total` exactly once
  per request while admission, reservations, scanner, backend accepted/failed/
  completed, proxy-error, and capacity-related live counters remain unchanged.

## Final fleet audit

The read-only audit completed at `2026-08-25T09:21:56Z`; it sent no generation
request. Evidence JSON SHA-256:
`1651f26c580bee3689af9092155a5c9b78f04f08edb3acf6d93adb4476c18ca8`.

- All four CVMs are `running` with `in_progress=false`, exact candidate Compose
  hashes, one exact digest-pinned PIG image occurrence, PIG `v0.12.20`, backend
  metrics available, and authenticated `/v1/models=200`.
- All four Router entries are enabled, selectable, circuit closed,
  `pig_metrics.ok=true`, and `pig_metrics.stale=false`; vLLM waiting was zero on
  every node at capture time.
- The audited PIG and vLLM log tails contained zero CUDA OOM, Xid, panic,
  fatal-error, back-off restart, or restart-loop markers.
- Final enabled set is exactly
  `use1-4c,use2-19,use2-3b,use2-4c,use2-5d`.
- `use1-4c` was preserved without deployment. `use2-9b`, `use2-bb`, `use2-cb`,
  and `use2-db` were neither deployed nor restored because they no longer serve
  Gemma4.
