# PIG v0.12.x Protocol Correction and Live Validation Plan

Status: active execution plan, 2026-08-08

This document is the canonical continuation record for PIG v0.12.x. It is
intentionally self-contained so work can resume after context compression
without inheriting obsolete v0.8-v0.11 behavior or starting v0.13 work.

## 1. Objective

Release and validate the current PIG v0.12.x patch as a single
predictive-admission product. A
request decision happens before an eligible request reaches the upstream,
malformed client input remains a client protocol error, and every real
protection decision is consistent across HTTP, metrics, bounded logs, and
Router capacity. A syntactically valid admitted request reaches the upstream
without PIG changing its application JSON or business headers. No legacy QoS
mode remains compiled into the executable. After all Router-disabled gates
pass, enable only `use1-cb` and observe 30 uninterrupted minutes of actual
traffic.

The product objective remains:

```text
maximize completed throughput subject to bounded QoS degradation,
low preemption incidence, no low-flow self-lock, and correct API behavior
```

The fast request-size estimate is one admission input, not an exact
model-specific tokenizer and not an independent reason to reject all traffic.
PIG does not implement request routing, cache lookup, or a learner in this
release.

`shadow` and `enforce` are rollout phases of the same new predictive
architecture, not old and new product modes. Both use the same read-only
classification, backend observation, policy, and decision telemetry. Shadow
records a counterfactual protection but forwards valid requests and cannot
activate Router backpressure. Enforce alone turns protection into a
pre-forward HTTP response and owns authoritative capacity reservations.

## 2. Authorized scope and hard boundaries

The only live target is:

```text
CVM UUID:   a0f0bfb3-e46f-4b22-814e-24872f251193
CVM name:   gemma4-31b-it-use1-cb
Router key: use1-cb
```

Required boundaries:

1. Stay on v0.12.x; the next executable version is v0.12.1.
2. Do not implement a learner and do not start v0.13.
3. Modify PIG only. vLLM and Router source are read-only protocol references.
4. Never change another Router upstream or route.
5. Keep `use1-cb` disabled until every shadow and enforce gate passes.
6. Run no Go, race, vet, simulation, benchmark, or image-build command on the
   local Windows host. Use the approved remote builder only.
7. Preserve the two unrelated untracked v0.11 documents and exclude them from
   every archive and commit.
8. If GitHub or GHCR write authorization fails, start a GitHub device flow and
   give the user the verification URL and one-time code. Never store the code
   or token in source, plans, logs, or artifacts.
9. Remove legacy behavior instead of hiding it behind mode checks. The shipped
   executable accepts only predictive `shadow` or `enforce`; a proxy-only
   `off`/`disabled` mode is not part of v0.12.1.
10. Preserve only the single-upstream predictive observation path. Do not
    retain or add backend selection, premium/basic traffic policy, request
    mutation, cache inspection, or learned admission as compatibility layers.

## 3. Current source and live truth

Nested repository:

```text
branch: codex/pig-v0.11.0-request-aware
HEAD:   e628f2d27eb3d478e0c6c71e0d448ae0f7ed43f1
v0.12.0 executable source: caaa882
```

The worktree currently contains this plan work, the v0.12 canonical ledger
update, and focused v0.12.1 tests. The unrelated untracked files that must not
be changed or committed are:

```text
docs/MODEL_AGNOSTIC_PRESSURE_BUCKET_ADMISSION_V0_11_PLAN.md
docs/QOS_CONSTRAINED_MAX_GOODPUT_ADMISSION_V0_11_PLAN.md
```

Fresh live read at 2026-08-07T16:33:14Z:

```text
CVM: running, in_progress=false
live Compose SHA-256:
  c1031b021c8e06b5186d6317b6a22a7f6c8607c398d67d018bf5d9ee9e04c56b
Router digest:
  sha256:25c16040ad6695b1676d0e2bc6bd910b7907aded2319c757056d07380ce0f58f
enabled set:
  use1-19,use1-9b
use1-cb upstream/route/running:
  disabled/disabled/0
```

These Router values are evidence only and are never reusable for a mutation.
Every mutation must immediately fresh-read and freeze the full set and digest.

The current live node safely remains on the Router-disabled v0.12.0 enforce
candidate while source correction proceeds. No Router enable is authorized.

## 4. Why v0.12.0 failed promotion

Artifact:

```text
tmp/pig-v0120-live-20260807/protocol-enforce-20260807T162356Z
```

Normal chat, supported sampling, streaming, required tool call, and strict
structured output returned 200. A valid unknown model preserved upstream 404.
Malformed JSON incorrectly returned 429.

Exact delta:

```text
predictive attempts:       7
fit / unknown:             6 / 1
enforced rejects:          1
backend accepts/failures:  6 / 0
proxy errors/preemptions:  0 / 0
terminal reservations, Prefill, running, waiting: all 0
```

The defect chain is executable and source-confirmed:

```text
invalid JSON
  -> classifier cost UnsupportedReason=invalid_json
  -> request-aware cost marked unknown
  -> request-scoped predictive request_reject
  -> generic QoS 429 plus enforced-reject increment
```

This is a protocol classification defect, not evidence that the upstream lacks
capacity. It also pollutes QoS protection metrics. v0.12.0 is therefore closed
as a failed release candidate and must never be Router-enabled.

The same artifact showed
`pig_predictive_admission_prediction_duration_seconds_count=0` after seven
request-aware attempts. The active request-aware adapter does not own or export
the existing histogram, so the zero is a wiring defect rather than zero-cost
prediction.

The focused r4 source audit and remote red run also proved that v0.12.0 is not a
single predictive architecture:

```text
shadow: legacy request-count/tier gate can return 429 before forwarding
shadow: backend priority injection overwrites JSON priority fields
shadow: compatibility cleanup removes tool_calls: []
enforce: compatibility cleanup still removes tool_calls: []
both: legacy X-PIG business-header handling remains in the request path
```

The problem is wider than one conditional in `proxy.go`. Legacy ownership is
still present in config loading and validation, dynamic QoS polling, lane/tier
queues, KV-only shadow admission, priority/body rewriting, status and metrics,
and old approximate/learned admission code that is not constructed by the
v0.12 default factory. Keeping those components dormant would leave false
operator controls and two admission architectures to maintain. v0.12.1 removes
them from the build.

## 5. v0.12.1 behavior contract

### 5.1 Protocol validation precedes admission

For an admitted JSON API path, a known-length body that is read within the
existing bounded classifier limit and is syntactically invalid JSON must:

1. return HTTP 400 with a bounded OpenAI-compatible client-error body;
2. make zero upstream calls;
3. make zero predictive attempts and create no reservation;
4. not increment `pig_predictive_admission_enforced_rejects_total` or any 429
   counter;
5. not activate Router backpressure or reduce Router inspect capacity;
6. increment exactly one bounded-cardinality metric:
   `pig_client_protocol_errors_total{reason="invalid_json"}`.

The response contains no request body fragment, parser offset, model name,
authorization value, or unbounded label.

This is syntax validation only. PIG does not validate the OpenAI request
schema, required fields, message roles/content, tool schemas, sampling values,
or model existence. A syntactically valid JSON scalar, array, or object with
invalid API semantics remains eligible for forwarding so the upstream retains
authority over its 4xx response.

The correction is intentionally narrow. Unknown content length, body over the
classifier bound, classifier saturation, body-read failure, unsupported
content type, unsupported request shape, and valid unknown model are not
silently reclassified as invalid JSON. Their existing behavior must be covered
by regression tests before any separate design change.

Shadow and enforce must return the same 400 for this protocol error. Admission
mode controls capacity protection, not JSON syntax semantics.

### 5.2 Prediction timing measures the active path

The request-aware adapter owns one fixed-bucket duration histogram initialized
with the existing predictive-duration buckets. A non-nil active adapter records
exactly one duration around each `Decide` invocation, including forward,
load-protection, availability-protection, and request-cost-unknown outcomes.

A malformed request rejected by the protocol precheck never invokes `Decide`
and therefore does not increment the prediction histogram. Request lexical
classification remains measured separately by
`pig_predictive_admission_estimator_duration_seconds`; the overall local
decision path remains measured by `pig_decision_duration_seconds`.

The patch must not add labels, per-request logging, a second timing definition,
or an allocation to the constant-time policy path.

### 5.3 New predictive behavior remains authoritative

The patch must preserve:

- immutable startup-adaptive Prefill and absolute KV profile ownership;
- the default 500-ms predictive observation interval and 1500-ms freshness
  bound;
- model-agnostic lexical request-size estimation;
- atomic check-and-reserve and exact-once terminal release;
- request-size differentiation under the same pressure;
- TPS-aware protection as a soft QoS constraint rather than a throughput stop;
- current shadow/enforce semantics for valid requests;
- Router projection and bounded protection logs for real QoS protections;
- streaming, sampling, tools, structured output, cancellation, timeout, and
  valid unknown-model behavior.

The default mode is `enforce`. Configuration validation accepts only explicit
`shadow` or the default/explicit `enforce`; it rejects `off`, `disabled`, or
any other value. Startup
always constructs exactly one request-aware predictive adapter and exactly one
single-upstream vLLM observer. Shadow never falls back to a legacy queue or
proxy-only path when observation is stale or the adapter is unavailable.
Shadow accounting, if retained for same-poll counterfactual accuracy, is
strictly separate and non-authoritative: it cannot consume enforce capacity,
reject, return 429, or affect Router projection. Enforce reservations remain
atomic and follow the exact lifecycle in Section 5.5.

### 5.4 Predictive request transparency

When predictive admission is `shadow` or `enforce`, PIG is an admission proxy,
not a request rewriter. For every syntactically valid request that is forwarded:

1. the upstream receives the exact application-body bytes read from the client;
2. PIG does not add, remove, normalize, or override JSON `priority`,
   `extra_body.priority`, `tool_calls`, or any other application field;
3. PIG does not add or remove `X-PIG-Lane`, `X-PIG-Tier`,
   `X-PIG-Output-Tokens`, `X-User-Tier`, or another business header;
4. premium/basic tier admission, legacy QoS queueing, backend-priority
   injection, and empty-`tool_calls` compatibility cleanup do not run;
5. bounded classification may replace the in-process body reader only if the
   forwarded byte stream and known `Content-Length` remain identical;
6. standard Go reverse-proxy transport behavior such as hop-by-hop header
   removal and `X-Forwarded-For` is outside the application-transparency
   contract and must not be confused with PIG business mutation.

Malformed syntax is the explicit non-forwarding exception from Section 5.1.
An enforced capacity decision is the explicit admission exception: it returns
429 without forwarding, but still never rewrites the request.

No cache inspection, route selection, premium/basic tier behavior, backend
priority behavior, TTFT protection, request compatibility rewrite, or learner
is added to or executed by the predictive path.

### 5.5 Legacy removal contract

The following executable ownership is removed, not deprecated:

| Retired ownership | Required v0.12.1 result |
| --- | --- |
| request-count lane gate, queue wait/poll, premium reserve, tier classification | packages, server fields, config, metrics, logs, active docs, and behavior tests removed |
| dynamic QoS controller, learned caps, pressure/TTFT/TPS feedback admission | polling/evaluation pipeline and all controls removed; feedback cannot gate a later request outside the predictive observer |
| KV-only shadow manager and its independent decision/reservation lifecycle | mode, manager, metrics, simulations, and config removed |
| backend-priority injection and OpenAI empty-`tool_calls` cleanup | request mutators, body-rewrite buffers/limits, metrics, config, and tests removed |
| `X-PIG-Lane`, `X-PIG-Tier`, `X-PIG-Output-Tokens` mutation | no server code may add, remove, or interpret these client business headers |
| adaptive output/lane thresholds and output-history learner | classification becomes bounded, read-only predictive input extraction only |
| old approximate predictive adapter, learned scheduler/calibrators, and their learner/goodput modes | unreachable alternative algorithm code and tests removed rather than retained beside request-aware admission |
| completion-usage QoS learner, deferred outcomes, and dynamic-dependent SSE keepalive/early-bridge heuristics | removed; normal buffered/streaming proxying remains and the first positive upstream response-body read closes predictive Prefill ownership |
| multi-backend selection/routing controls | active server requires exactly one upstream and never scores or selects between backends |
| predictive `off`/proxy-only mode | no startup or request path exists without the request-aware adapter |

The following are not legacy and must remain:

- bounded model-agnostic lexical request-size and output-token extraction;
- the immutable startup-derived backend capability profile, absolute KV limits,
  Prefill thresholds, and fixed policy ratios;
- one vLLM metrics observer with the 500-ms default interval, freshness,
  preemption cooldown, and epoch invalidation;
- deterministic request-aware TPS/KV/Prefill policy, atomic reservations,
  forward commit, Prefill transition, cancellation, timeout, completion, and
  exact-once terminal reconciliation;
- authenticated PIG and combined backend metrics, bounded predictive logs,
  upstream error handling, health/auth/attestation, and predictive Router
  capacity projection.

Values still needed by the new graph must be renamed and owned by predictive
configuration rather than retained inside a legacy config struct. In
particular, request-size estimator bounds, KV target/hard ratios, preemption
cooldown, metrics URL, and freshness belong to predictive configuration. The
single upstream metrics URL defaults to the upstream `/metrics` endpoint and
may have one predictive override; old `DYNAMIC_*`, `KV_ADMISSION_*`,
`BACKEND_PRIORITY_*`, compatibility-rewrite, tier, lane, queue, and adaptive
output variables are not loaded.

Production configuration and test configuration are deliberately different
surfaces:

1. The production environment is minimal. `UPSTREAM`, authentication and
   infrastructure settings remain; predictive mode defaults to enforce, the
   metrics URL is derived from the single upstream, and no value equal to a
   versioned default is written into Compose.
2. KV capacity, block size, absolute KV limits, and Prefill thresholds are
   derived once at startup. They are not learned and are not copied into the
   production Compose as model-specific constants.
3. The observation interval defaults to 500 ms. Freshness, preemption cooldown,
   scanner concurrency, estimator bounds, and safety ratios use bounded
   versioned defaults derived in one predictive config owner. The scanner body
   ceiling is an internal 4 MiB default, sufficient for a model-neutral 650K
   text-token window at the estimator's six-byte ratio; it is not another
   production environment variable.
4. Product TPS target/floor and a complete all-or-none Prefill emergency
   override may remain supported for controlled experiments, but the production
   artifact omits them when defaults are intended. Partial overrides are
   invalid. A live-test artifact that uses any override records the complete
   override set and is never promoted unchanged as the production artifact.
5. Tests may explicitly vary all policy, timing, capacity, request-size, and
   failure inputs through typed Go test configuration and dependency injection.
   Builder tests and Router-disabled experiments may therefore expose many
   explicit controls without making those controls mandatory production
   configuration. A test knob does not automatically become an environment
   variable or an operator-facing README option.
6. `PREDICTIVE_ADMISSION_MODE=shadow` is the only algorithm variable expected
   in the Router-disabled shadow Compose. The enforce Compose removes it and
   proves the default.
7. The production Compose configuration contract is audited independently of
   parser capability: it contains only `UPSTREAM`, required auth/infrastructure
   values, and genuine non-default deployment choices. It must not spell out
   version defaults merely because the loader accepts an override.
8. Test, shadow, and production manifests are separate generated artifacts.
   Production is regenerated from the fresh live Compose and the immutable
   image digest; it is never obtained by renaming a test artifact or merely
   changing `shadow` to `enforce` while retaining test overrides.
9. Before deployment, extract the effective PIG environment from each candidate
   and record an override manifest. Every `PREDICTIVE_*` entry must include its
   version default, candidate value, and operational reason. An entry equal to
   the default fails the production gate. The shadow candidate may additionally
   contain exactly `PREDICTIVE_ADMISSION_MODE=shadow`; the enforce candidate
   must omit that variable.
10. A target-specific non-default such as a longer startup probe timeout may
    remain when live startup evidence requires it. This is a documented
    deployment exception, not permission to copy cadence, KV, TPS, Prefill, or
    other test policy values into production.

Historical versioned design records may remain as audit evidence, but README,
`ADVANCED.md`, `OBSERVABILITY.md`, example Compose, and any other current-user
documentation must describe only v0.12.1. Retired queue, tier, KV-shadow,
priority, lane, and dynamic-policy metric families disappear rather than remain
exported as zeros.

There is one temporary wire-compatibility exception. Read-only inspection of
the current request-aware Router source at `f3f4866` proves its parser still
requires at least one of the old capacity fields and its request-aware protocol
still consumes:

```text
pig_dynamic_observed_running_raw
pig_dynamic_observed_waiting_raw
pig_dynamic_observed_running
pig_dynamic_observed_waiting
pig_dynamic_global_limit_raw
pig_dynamic_global_limit
```

Removing all six in a PIG-only release would make Router report
`pig_metrics_missing` and continue with an invalid capacity view. v0.12.1
therefore retains only these names as a compatibility projection written
directly from the predictive observer and Router projection. They have no
dynamic controller, learned-cap, tier, queue, or admission-policy owner. The
predictive enforce flag, backpressure fields, and inspect capacity remain the
authoritative protocol. Tests must prove a PIG protection changes these
compatibility values in the same scrape atomically. Removing the six names
requires a separately authorized Router protocol migration and is not hidden
inside this PIG-only patch.

The compatibility writer takes one current predictive snapshot per scrape. Raw
running/waiting comes from the current vLLM observer, never from the last client
decision; effective running/waiting/global limit comes from Router projection
of that same snapshot. This prevents sparse traffic from leaving stale capacity
latched and prevents mixed-epoch fields in one response. `/v1/upstream-status`
uses the same predictive snapshot semantics.

## 6. SOLID ownership

The active vertical slice is:

```text
request.Classifier
  detects bounded invalid JSON and returns a typed client protocol error

server.ServeHTTP
  maps the typed error to HTTP before admission and forwarding

infra/openai
  serializes the fixed OpenAI-compatible 400 response

observability/metrics classifier input
  exports the fixed invalid_json counter

requestAwarePredictiveAdapter
  owns and exports timing for its own Decide operation

predictiveVLLMObserver
  owns the one backend observation loop and immutable capability epoch

runtime/predictive request-aware manager and policy
  own deterministic post-admit evaluation and atomic reservation lifecycle
```

The predictive policy remains unaware of HTTP syntax, HTTP status codes, and
metrics serialization. The proxy does not inspect stringly
`UnsupportedReason` values. Metrics do not decide behavior. This keeps each
reason to change in one owner and avoids coupling protocol validation to
capacity policy.

The proxy has no dependency on a legacy QoS gate, lane, tier, dynamic
controller, KV-shadow manager, priority injector, or request body mutator.
Request classification owns read-only bounded inspection and exact restoration.
The server composes one classifier, one adapter, one observer, and one upstream
proxy; it must not expose an optional second algorithm. Predictive config does
not import legacy policy types merely to reuse their defaults.

## 7. Test-first remote-builder workflow

### 7.1 Focused red

The first test-only archive was prepared before this standalone plan was
finalized and is retained only as a superseded construction artifact:

```text
tmp/pig-v0121-builder-20260808/pig-v0121-red-r1-source.tar
SHA-256: 046b5458f31f84a6e28fe8c38a7c8df628a5c9bf8e98319d34a6fc8b92efd7b2
base HEAD: e628f2d27eb3d478e0c6c71e0d448ae0f7ed43f1
```

Do not execute r1. The initial post-review r2 archive was created and uploaded
but not executed:

```text
tmp/pig-v0121-builder-20260808/pig-v0121-red-r2-source.tar
SHA-256: 6da8ffd997e2657b9c51b00d01a6208888cd5ae1f0d3a9fffcf5c281e4187510
```

The request-transparency requirement was added after r2, so r2 is also
superseded. The first complete-contract archive r3 was remotely hash-verified
and executed:

```text
tmp/pig-v0121-builder-20260808/pig-v0121-red-r3-source.tar
SHA-256: ae45076910f97f1c482c76f34baf8fec662ef3e06e0de7956324034b43eee48c
builder directory: /work/pig-v0121-red-r3-ae450769
focused exit: 1
```

R3 validly reproduced malformed shadow 200/forward, malformed enforce 429,
missing request-aware prediction timing, and shadow legacy-gate 429. Its two
request-transparency subtests failed during server construction because the
fixture enabled output-token classification without specifying
`OUTPUT_TOKEN_FIELD_NAMES`; those subtests are invalid red evidence. R4 fixed
the fixture, was remotely hash-verified, and reproduced all focused defects:

```text
archive: /work/pig-v0121-red-r4-source.tar
SHA-256: 64d769cf440b4b5fdfc73e300617c5588fb97e062e446e250c9e124be762148c
builder directory: /work/pig-v0121-red-r4-64d769cf
command: go test ./internal/app/server -run V0121 -count=1
exit: 1
log SHA-256: 739b567bb215be23ed3f5b21b68664a02e7a0249099e5af915b36c9e6eb90903
```

R4 proved the two malformed response defects, disconnected request-aware
prediction duration, body/header mutation in both modes, and shadow rejection
by the legacy gate. It remains valid red evidence for those behavior families.

The later full legacy-removal decision adds a new source/config contract, so
create r5 from tracked HEAD plus only the canonical ledger update, this
reviewed plan, the existing focused tests, and new removal tests/audit script.
Record and remotely verify the new SHA-256. Run on the remote builder:

```text
go test ./internal/app/server -run V0121 -count=1
go test ./internal/config/pigconfig -run V0121 -count=1
sh scripts/verify-no-legacy-mode.sh
```

Valid r5 red evidence must preserve the intended r4 behavior failures and must
show that `off`, retired variables, packages, metric writers, request mutators,
and alternative old algorithm sources still exist. A missing dependency,
archive mismatch, compiler error, broken runner, or unrelated package failure
is invalid red evidence.

R5 was hash-verified and produced valid server/config red, but its source audit
depended on `rg`, which is absent from the builder container. That audit is
invalid runner evidence and is superseded by r6; no product result is inferred
from the missing command:

```text
r5 archive SHA-256: 00032f625cc682f5f84d09768b14f286865d2d3c6a489e4fafe7a5bcf6b5e72e
server log SHA-256: 739b567bb215be23ed3f5b21b68664a02e7a0249099e5af915b36c9e6eb90903
config log SHA-256: 86f55902fba1cdabd23fef99ef18ea89c446cbc82d95e839f2da5b1d90b5447a
server/config exits: 1/1
```

R6 added a standard `grep` fallback without changing the product tests. It was
remotely hash-verified and the audit exited 1 solely because the enumerated
legacy paths, symbols, variables, mutators, and metric writers still exist:

```text
r6 archive SHA-256: 975edc576b4cce0bc67a245edffd86b80340675485b618450ee2a64e3fb85a9d
builder directory: /work/pig-v0121-red-r6-975edc57
audit log SHA-256: ab3c88eec0be25a7033784b92501ed43229754e1e631ceba310432885d6cd488
audit exit: 1
```

### 7.2 Green implementation and focused coverage

Implement the ownership in Section 6 and add focused tests for:

1. enforce malformed JSON -> local 400, backend zero;
2. shadow malformed JSON -> the same local 400;
3. fixed client-error body and metric;
4. predictive attempts, reservations, enforced rejects, 429s, and Router
   backpressure remain zero for the malformed request;
5. valid unknown model still reaches the backend and preserves 404;
6. unsupported content type/unknown length/oversized/saturated cases retain
   explicitly selected existing behavior;
7. one prediction-duration observation per active `Decide` result class;
8. concurrent timing observations are race-safe;
9. exported histogram count matches active request-aware attempts, excluding
   pre-prediction client errors;
10. shadow and enforce forward byte-identical JSON containing client
    `priority`, `extra_body.priority`, and `tool_calls: []`;
11. shadow and enforce preserve selected business headers byte-for-byte and do
    not add any `X-PIG-*` header;
12. predictive shadow is observation-only and forwards a would-protect request,
    while enforce protects the same request pre-forward;
13. startup defaults to enforce, accepts only shadow/enforce, requires exactly
    one upstream/observer, and rejects off/disabled mode; only tests and the
    Router-disabled shadow stage explicitly configure shadow;
14. retired environment variables do not populate any config field and no
    retired config field remains in `Config`; test-only policy injection does
    not expand the production environment surface;
15. an executable source audit proves retired directories/files, imports,
    request-mutator symbols, env-name strings, and metric-family writers are
    absent from active source, except for the six explicitly listed Router wire
    names in one predictive compatibility writer;
16. current README/advanced/observability examples contain no retired runtime
    instructions and list predictive replacements for removed metrics;
17. the server starts with no lane, tier, queue gate, dynamic controller,
    KV-shadow manager, priority injector, adaptive output learner, or alternate
    approximate/learned admission adapter;
18. shadow never returns predictive 429 or changes Router projection; enforce
    retains atomic check/reserve/forward/terminal behavior and exact drain.
19. the production scanner ceiling classifies a 650K word-like text fixture,
    preserves its exact bytes and Content-Length, and has a recorded 4 MiB
    classifier/estimator benchmark;
20. current metrics export only lifecycle phases with real owners and do not
    retain `off`, semantic-learning, completion-learning, resource-release, or
    shadow-attribution placeholders as permanent zeroes;
21. response headers alone retain pending-Prefill ownership; the first positive
    body read ends Prefill exactly once, and terminal remains exact once.

### 7.3 Full builder matrix

After focused green, run from one exact archive on the remote builder:

```text
sh scripts/verify-no-legacy-mode.sh
test -z "$(gofmt -l internal cmd)"
go test ./internal/app/request ./internal/infra/openai \
  ./internal/observability/metrics ./internal/app/server -count=1
go test ./... -count=1
go vet ./...
go test -race ./internal/app/request ./internal/infra/openai \
  ./internal/observability/metrics ./internal/app/server -count=1
go test -race ./... -count=1
go build ./...
```

Run the complete request-aware deterministic simulation; retired KV-only,
approximate-learning, and learner-frontier simulations must no longer compile
as product modes. Repeat paired v0.12.0/candidate benchmarks in both orders,
including the HTTP pre-forward benchmark and request-aware policy/manager
benchmarks. Acceptance requires
zero new allocations; no more than 15 percent median regression and no more
than 1 microsecond absolute added time on adapter cases with 48 or 256 active
reservations; no more than 5 percent or 2 microseconds, whichever is larger, on
the end-to-end HTTP pre-forward benchmark; no more than 1 percent loss of
simulated SLO-compliant goodput; and no new self-lock or preemption failure.
Record the raw paired values even when they pass. A threshold failure requires
profiling or a simpler implementation, not a prose waiver.

Record exact archive SHA-256, builder CVM/container/toolchain, command, exit
status, and SHA-256 of material logs. Any executable source change invalidates
earlier green evidence.

## 8. Version, Git, image, and registry gates

Only after behavior is green:

1. set executable version, README examples, Dockerfile OCI metadata, and other
   current-version references to v0.12.1; document the breaking removal of old
   modes, variables, and metric families;
2. audit `git diff --check`, changed path set, staged path set, and exclusion of
   both unrelated v0.11 documents;
3. commit and push exact source on the current branch;
4. archive the exact commit and prove it reproduces the green source;
5. build the image on the remote builder only;
6. verify version log, `/healthz`, entrypoint, configured user, OCI revision and
   version labels, image ID, and extracted binary SHA-256;
7. publish both a human version tag and commit-qualified immutable tag to GHCR;
8. pull by registry digest on the builder and prove the extracted binary hash
   equals the builder-local candidate.

Do not treat a commit, push, local image, tag, or digest as deployment proof.

If push/package authorization is denied, initiate GitHub device authorization
instead of retrying blindly. Give the user `https://github.com/login/device`
and the one-time code, poll only for the bounded device-flow interval, verify
the minimum required source/package scope, and erase transient credentials from
builder tmpfs after publication.

## 9. Router-disabled live validation

Re-read the target CVM and live Compose immediately before candidate
generation. Generate rollback, shadow, and enforce artifacts from those exact
bytes. Change only the PIG immutable digest, selected predictive mode, and the
minimum configuration migration required to remove retired PIG variables;
preserve vLLM, ports, volumes, commands, healthchecks, auth references, and
operational settings. Keep Prefill overrides absent so startup calibration owns
the immutable profile. Record the exact removed variables and prove the
candidate derives the single metrics endpoint correctly before deployment.
The shadow artifact alone sets `PREDICTIVE_ADMISSION_MODE=shadow`; the enforce
artifact removes that override and exercises the default enforce contract.
Neither artifact may spell out algorithm values that equal v0.12.1 defaults.

Produce and hash four configuration views before either deployment: the fresh
live PIG environment, the shadow candidate environment, the enforce candidate
environment, and a normalized before/after diff. The shadow predictive override
set must be exactly `{PREDICTIVE_ADMISSION_MODE=shadow}` plus separately
documented genuine non-default deployment exceptions. The enforce predictive
override set contains only those exceptions. For each exception record why the
default is unsafe or unsuitable on this target; otherwise remove it. Reject the
candidate if any test-only cadence, freshness, KV-ratio, TPS, cooldown, or
Prefill value leaks into either production artifact.

Before every deploy or direct test:

- fresh-read complete Router upstreams/routes and digest;
- map the live Router image/revision to a parser that supports the
  request-aware capacity protocol equivalent to source `f3f4866`, including
  predictive enforce/backpressure plus the six compatibility fields;
- prove `use1-cb` upstream and route disabled and route running zero;
- prove PIG reservations/Prefill and vLLM running/waiting drained;
- stop if another deploy is active or any unrelated Router field drifts.

For both shadow and enforce require:

1. platform operation complete and `in_progress=false`;
2. expected image digest and binary/version evidence;
3. stable PIG/vLLM containers with restart counts and no new fatal/OOM;
4. authenticated `/v1/models`, `/pig/metrics`, and `/v1/metrics` 200;
5. metrics unauthenticated 401;
6. exactly one coherent calibrated or bounded-fallback capability profile;
7. correct KV capacity/block/absolute limits and Prefill thresholds;
8. normal, sampling, stream, tool, strict structured, unknown-model, and
   malformed protocol gates;
9. a direct capture upstream proves body bytes, known Content-Length, client
   priority fields, empty tool-call arrays, and selected business headers are
   unchanged in both shadow and enforce;
10. sparse low-flow, burst, cancellation, disconnect, timeout, and failure
   exact-once release;
11. regular/weighted/exclusive/quiescent and near-262K size gates;
12. same-pressure differentiation: large request protected while a hard-fit
    short request remains admissible;
13. atomic concurrent reservations cannot cross hard KV: derive the smallest
    bounded request shape from the live profile for which multiple earlier
    reservations fit but the next post-admit reservation crosses the hard
    limit; observe the final request rejected pre-forward, cancel admitted
    probes promptly, and prove exact drain without sending a prompt beyond the
    backend context limit;
14. prediction-duration count advances and latency distribution is collected;
15. every real enforced protection matches HTTP 429, predictive reason/counter,
    bounded log, last-reject, and Router projection;
16. malformed 400 increments only the client-protocol metric and never Router
    load protection;
17. metrics and status contain no retired queue/tier/dynamic-policy/KV-shadow/
    priority families; the six Router capacity compatibility names are present,
    coherent with predictive fields in the same scrape, and driven only by the
    predictive observer/projection;
18. final terminal state is intake open, no reservations/Prefill/waiting, no
    clamp, no preemption, and no restart loop.

The complete matrix must be repeated after the v0.12.1 source change. The
earlier v0.12.0 shadow green evidence is diagnostic history, not release proof.

The known deploy transition is not hidden: this vLLM has previously needed
about 350 seconds to start while the configured PIG startup probe is bounded at
300 seconds. Capture container `RestartCount` before and after every deploy. At
most one PIG restart caused solely by the documented pre-readiness probe timeout
may be classified as deployment recovery; it is not a steady-state pass. The
surviving process must publish one coherent profile and remain restart-free
through all readiness gates and tests. A second restart, any post-readiness
restart, or a restart loop fails the deployment.

## 10. Actual-traffic canary and stop rules

Only after all Router-disabled gates pass:

1. fresh-read at least one healthy enabled non-target peer;
2. freeze the exact enabled set and Router digest;
3. enable only `use1-cb` with an exact before/after diff;
4. observe 30 uninterrupted minutes of actual traffic;
5. collect target-separated request share, completion goodput, per-user Decode
   TPS, running/waiting, KV tokens/ratio, Prefill ownership, request-size
   classes, 400/429 reasons, prediction latency, Router projection,
   preemptions, container restarts, and idle-with-demand;
6. compare with old-version peers only after separating traffic share, request
   size, cache state, and workload mix.

Immediately disable only `use1-cb` before expensive evidence collection if any
stop rule occurs:

- preemption, fatal, OOM, EngineCore death, PIG/vLLM restart, or profile epoch
  reset;
- three consecutive 500-ms stale/unavailable/intake-closed observations outside
  a deploy transition;
- HTTP protection without matching predictive metrics/logs/Router projection,
  or Router projection contradicting request-path enforcement;
- malformed client input appears as 429 or activates load protection;
- hard-fit short request is protected while a larger request is admissible in
  the same fresh state;
- idle/drained state remains clamped for three fresh observations;
- waiting persists more than five seconds outside an owned long Prefill;
- per-user Decode TPS stays below the configured floor for two consecutive
  ten-second Decode-dominant windows;
- target exceeds 50 percent Router share over five minutes, another route
  changes, Router digest/set drifts, or evidence collection cannot prove state.

After disable, prove Router running, PIG reservations/Prefill, and backend
running/waiting drain to zero. Never modify another route to keep the canary
alive.

## 11. Evidence layers and completion definition

Report these layers independently:

1. plan reviewed;
2. focused red reproduced;
3. source implemented;
4. focused builder green;
5. full builder matrix/simulations/benchmarks green;
6. source committed and pushed;
7. builder-local image verified;
8. registry digest and pulled-binary provenance verified;
9. shadow Compose deployed and live gates green;
10. enforce Compose deployed and live gates green;
11. Router canary observed for 30 minutes;
12. final Router/CVM state and release conclusion recorded.

The objective is complete only when layer 11 passes without a stop rule and the
final state is recorded, or when the user explicitly changes the product scope.
A failed candidate is a completed diagnostic stage, not a successful release.

## 12. Progress ledger

- [x] v0.12.0 shadow diagnostic gates completed
- [x] v0.12.0 enforce protocol defect reproduced with live evidence
- [x] v0.12.0 Router promotion stopped; `use1-cb` remains disabled
- [x] defect and disconnected prediction-duration metric traced in source
- [x] v0.12.1 focused tests written without local Go execution
- [x] three plan reviews completed and corrections recorded
- [x] red archives r1/r2 retained as superseded and not executed
- [x] r3 reproduced three defect families but transparency red was invalid due
  to a configuration fixture error
- [x] corrected red archive r4 created and remotely hash-verified
- [x] r4 focused behavior red reproduced on the remote builder for intended
  reasons
- [x] full legacy ownership/dependency boundary audited
- [x] legacy-removal plan reviewed three times and corrected
- [x] complete-contract r5 behavior/config red and r6 source-audit red
  reproduced remotely for intended reasons
- [x] r19 scanner/metrics focused green established the 4 MiB baseline;
  archive `117e17ff740e6ad0ddc6ba6d6b610f22bbbd9637ae0cadae2f663aae65ee76a5`
- [x] r20 valid red proved JSON field parsing allocated 83 times on the hot
  path; archive `af99c3691fd0947a34609a4da2f40786562d9fccf2d9845bbc3c6324c1b314e4`
- [x] r21 zero-copy structural field scan green; archive
  `bd62a6f974821fc209aaaec8f35b74e9b529e756b6c6f66408839e469d6cc981`
- [x] r22 valid red proved undeclared oversize-body truncation and excessive
  known-length allocation; archive
  `43921bea1bae7c555495338215f379c3a3e1fdb4141cb3ad632a2c2fb52fd88c`
- [x] r23 transparent single-buffer body handling green; archive
  `e1e9c7ea1a3004e93c7b9f5e6641ea17da8a4ff273c4a81b9d50db9e5a43bfb4`
- [x] r24 is invalid green evidence because gofmt and compile failed; corrected
  r24b interface cleanup is green at archive
  `b32bf695780b64cf7bc8ea53dc93ab11d1daa7630546d4e97fab4fbd0a577660`
- [x] r25 is invalid red evidence because its test file was not formatted; r25b
  is the valid one-observer-generation red at archive
  `7ff5fc7053c801e5b0d5b365a570da2b0a95075fcade078a051647b6c3e979e2`
- [x] r26 one-snapshot Router telemetry fix is focused green; archive
  `25198fa4bdd9ac8cec3fe619699ed4d360be5eb6e47808389677aafc1cdcef58`
- [x] r27 is invalid full-matrix evidence: the Windows working-tree archive
  included empty directories for deleted legacy packages, so the unchanged
  strict legacy audit correctly failed; the detached SSH session also ended
  before simulation and benchmarks. No r27 result is inherited.
- [x] r28 exact-file full builder matrix is green; candidate archive
  `20aa1ed13fb254f0b3299260321f195a35d5d984c1e2efc500fb4b6ef8504889`,
  v0.12.0 baseline archive
  `ad9f8b48cf3642dd0b2e58921f9069e492d5fb5df30f47c8d8ba11bde2e5cfbe`
- [x] v0.12.1 source implementation completed
- [x] three source/evidence reviews completed
- [x] full remote-builder matrix, simulations, and benchmarks green
- [x] v0.12.1 exact source committed and pushed at `d953233`
- [x] v0.12.1 immutable image published at digest
  `sha256:28713fc7811100beeba71b46cbcfec71b77b303488367cce9334d1ab15fa2ef8`
- [x] v0.12.1 Router-disabled shadow matrix completed
- [x] v0.12.1 enforce readiness, protocol, lifecycle, size, and contract gates
  completed before r51
- [x] r51 live red stopped v0.12.1 promotion; `use1-cb` remained disabled and
  drained
- [x] v0.12.2 request-reject Router projection correction is builder green
  through the r55/r56 executable-identity evidence chain
- [x] v0.12.2 exact source committed and pushed at `88cbb29`
- [x] v0.12.2 immutable builder/registry image provenance complete at digest
  `sha256:7cafb935d48175045cd355a844a3f94638fdfae16f965e2a9d7dbedeee63c4e4`
- [ ] complete Router-disabled shadow matrix green
- [ ] complete Router-disabled enforce matrix green
- [ ] only `use1-cb` enabled for the canary
- [ ] 30-minute actual-traffic observation passed without a stop rule
- [ ] final Router/CVM state and release conclusion recorded

## 13. Plan review record

### Pass 1: model, protocol, and causality

Completed 2026-08-08. The review confirmed that JSON syntax validity is a
precondition to admission rather than an unknown-capacity prediction, so the
malformed path must bypass `Decide`, reservations, enforced-reject accounting,
and Router projection. It kept the lexical request-size estimate model-agnostic
and separate from exact tokenization, cache lookup, and routing. It also
separated classifier time, predictor time, and whole local-decision time.

The review found one overreach risk: a local malformed-JSON fix could grow into
partial OpenAI schema validation. Section 5.1 now explicitly limits PIG to
bounded JSON syntax validation and preserves upstream authority for every
syntactically valid but semantically invalid request. The focused and live
unknown-model/unsupported-shape gates must prove that boundary.

### Pass 2: lifecycle, safety, and SOLID

Completed 2026-08-08. The review confirmed that the typed client-protocol error
is owned by the classifier, HTTP mapping by the proxy/OpenAI response writer,
fixed-cardinality counting by observability, and prediction timing by the
request-aware adapter. The policy remains free of HTTP and parser concerns.
Malformed input creates no reservation, so it adds no new release path.

Two safety gaps were corrected. The atomic live gate now derives a bounded
near-hard workload from the actual calibrated profile, rejects the crossing
reservation pre-forward, promptly cancels admitted probes, and proves exact
drain. The plan also records the observed 350-second vLLM versus 300-second PIG
startup transition and distinguishes one bounded pre-readiness recovery from
any post-readiness restart or loop. Router mutation remains limited to
`use1-cb`, with fresh set/digest checks and disable-before-evidence stop rules.

### Pass 3: evidence, release, and operational simplicity

Completed 2026-08-08. The review found that red archive r1 predated this
standalone plan even though the draft implied otherwise. Section 7.1 now marks
r1 superseded and requires a post-review r2 archive. It also corrected the
format gate so listed files fail the matrix, and replaced the vague
"no material regression" statement with paired allocation, latency, and
simulation thresholds.

The release layers, exact-source rebuild, pulled-binary comparison, fresh
Compose derivation, Router-disabled full rerun, disable-first stop path, and
30-minute uninterrupted observation remain necessary. The review rejected
schema validation, cache work, routing work, learner work, and a general
unsupported-request redesign as unnecessary for this patch. The plan is ready
for the request-transparency follow-up review.

### Transparency follow-up: three passes completed 2026-08-08

Pass 1, model and causality: source inspection proved that predictive enforce
still ran empty-`tool_calls` compatibility rewriting, while predictive shadow
could also normalize JSON priority and add PIG headers. The plan now makes
request transparency an executable pre-forward contract: classification is
read-only, admission either forwards original application bytes or does not
forward, and malformed syntax is the only new local protocol exception.

Pass 2, safety and SOLID: the proxy must bypass both legacy QoS and the request
`PriorityInjector` in shadow and enforce. This prevents shadow evidence from
being contaminated by the retired tier gate and prevents classification from
sharing mutable-body ownership with compatibility rewriting. Tests cover exact
body bytes, known Content-Length, business headers, client priority fields,
empty tool-call arrays, and an occupied legacy lane. Standard reverse-proxy
transport headers remain explicitly outside the business-transparency
contract.

Pass 3, evidence and simplicity: r2 was uploaded but never executed and no
longer covers the full contract, so it is superseded rather than partially
reused. A new r3 must reproduce all four focused defect families together:
protocol 400, prediction timing, request transparency, and legacy-gate bypass.
The review did not add schema validation, cache inspection, routing, learner,
or model-specific tokenization. The revised plan is ready for r3 remote-builder
red execution.

### Legacy-removal follow-up: three passes completed 2026-08-08

Pass 1, model and causality: the review rejected the narrow idea of merely
bypassing `legacyQoS`. Source inspection showed the old architecture still
owned config, polling, queues, tiers, KV shadow, request rewriting, metrics,
status, simulations, and unreachable learned-admission implementations. The
plan now removes those owners and leaves one request-aware predictive graph.
It also incorporates the latest product decisions: enforce is the default,
shadow is explicit test/Router-disabled observation only, and test configurability
does not imply a large production environment surface.

This pass found one critical protocol exception. Router source `f3f4866`
recognizes predictive enforce/backpressure but still requires the six
`pig_dynamic_*` running/waiting/global-limit fields. Deleting all old metric
names in a PIG-only patch would produce `pig_metrics_missing`. The plan now
retains only those six names as a predictive compatibility projection and
removes the old dynamic metric owner. This is wire compatibility, not a second
admission mode.

Pass 2, safety, lifecycle, and SOLID: the review separated current vLLM
observation from last-decision telemetry. One scrape must use one current
predictive snapshot for raw and effective Router capacity so sparse traffic
cannot leave a stale clamp. Enforce alone owns authoritative atomic
reservations; shadow cannot reject, return 429, or alter Router capacity. Shared
manager tombstones, epoch invalidation, pending-Prefill accounting, semantic
Prefill completion, and exact-once terminal release remain even when generic
scheduler/learner types are removed or renamed. The server must also stop
adding `X-PIG-Backend` now that exactly one upstream is required.

This pass removed two unnecessary compatibility surfaces: completion-usage QoS
learning/deferred outcomes and dynamic-dependent SSE keepalive/early-bridge
heuristics. Normal streaming remains. The lifecycle follow-up corrected the
transition definition: response headers do not end Prefill; the first positive
upstream response-body read marks it complete exactly once without semantic
parsing. Startup fails rather than falling back when the single observer or
adapter cannot be constructed.

Pass 3, evidence, release, and operational simplicity: deleting old tests alone
is not evidence, so r5 adds an executable source/config audit plus focused
behavior tests. The audit permits the six Router wire names only in one
predictive compatibility writer and rejects old directories, imports, env
strings, mutators, commands, metrics, and alternate adapter entry points. Full
builder tests, race, request-aware simulation, and paired benchmarks must run
from one exact archive after deletion.

The production Compose gate now forbids spelling out algorithm defaults. Only
the shadow artifact sets `PREDICTIVE_ADMISSION_MODE=shadow`; the enforce
artifact removes it. Before any deploy, evidence must map the live Router image
to a request-aware capacity protocol equivalent to `f3f4866`; otherwise the
candidate remains Router-disabled. These corrections keep the removal
aggressive without breaking the already-required Router feedback path.

### Source implementation follow-up: three passes completed 2026-08-08

Pass 1, model and causality: the bounded lexical request-size estimate is
consumed by the real pre-forward `Evaluate` and atomic reservation path. KV,
TPS, and Prefill checks all evaluate the post-admit state, including live
reservations. Output-limit fields use the maximum valid value independent of
JSON field order, but only a bounded near-term Decode horizon is reserved so an
arbitrarily large client limit cannot create long-lived false pressure. No
cache lookup, learner, model-specific asset, routing decision, or TTFT gate was
reintroduced. Unused request-class scaffolding and decision inputs were
removed so the interfaces expose only values consumed by the policy.

Pass 2, lifecycle and safety: Prefill ownership now survives response headers
and ends on the first positive upstream response-body read. Oversize or failed
classification restores the complete original stream rather than forwarding a
truncated prefix. The Router compatibility projection and its raw observer
fields are derived from one timestamped observer snapshot per scrape; r25b
proved the mixed-generation defect and r26 proved the one-snapshot correction.
Shadow remains non-authoritative, while enforce retains atomic reservation and
exact-once rollback/release behavior. These changes preserve the classifier,
policy, lifecycle, observer, and compatibility-writer ownership boundaries.

Pass 3, evidence, efficiency, and release simplicity: r20/r21 reduced the 4 MiB
JSON field scan from an allocation-heavy decoder to a zero-allocation
structural scan. r22/r23 removed body truncation and reduced full 4 MiB
classification to one body-sized allocation with bounded fixed overhead; r26
measured `7.55 ms/op`, `0 allocs/op` for field scanning and `15.23 ms/op`,
`22 allocs/op` for full classification on the approved builder. r24 and r25
remain explicitly invalid evidence because their format/compile prerequisites
failed; only r24b, r25b, and r26 are inherited.

The configuration review separated loader capability from production
configuration. Tests may explicitly override the complete typed policy, but
production is regenerated and audited as a separate artifact. Default enforce,
derived metrics URL, 500-ms polling, 1500-ms freshness, startup-derived KV and
Prefill values, and other version defaults remain absent from production
Compose. A real target-specific non-default requires a named reason and an
effective-environment diff. This completes the source and three-review layers,
not the full builder, image, deployment, or production layers.

### r28 exact-file full builder matrix: green 2026-08-08

The approved builder was
`89811a9add5b20427ee1fbf4dc22a33984e41959-22.dstack-pha-use1.phala.network`,
container `pig-v01011-builder`, Go `go1.24.13 linux/amd64`, kernel
`Linux 6.9.0-dstack x86_64`. The exact candidate archive SHA-256 was
`20aa1ed13fb254f0b3299260321f195a35d5d984c1e2efc500fb4b6ef8504889`;
the paired executable v0.12.0 baseline at commit `e628f2d` was
`ad9f8b48cf3642dd0b2e58921f9069e492d5fb5df30f47c8d8ba11bde2e5cfbe`.
Remote work and logs are under
`/work/pig-v0121-full-r28-20aa1ed1` and
`/var/volatile/dstack/persistent/pig-builder-work/pig-v0121-full-r28-20aa1ed1`.

Every recorded status was zero: environment/version identity, gofmt, strict
legacy audit, focused tests, full tests, vet, targeted race, full race, build
all, versioned binary, two simulations, byte comparison, simulation
acceptance, both benchmark orders, benchmark contract, and candidate large-body
benchmarks. The independently verified status-file SHA-256 is
`a891fbf4eb62e78b692908ebfe0e8a5b52462c9d9246b8af1261f32c7d5d64a2`;
the evidence manifest revalidated every listed artifact as `OK`. The binary
SHA-256 is
`a9f21944563785d903c54662ccb4607c0471e58d7d39b16e59d0b508296125d0`.

The two simulation reports were byte-identical at SHA-256
`30041adb51d709dfce1b24f06fc029fae27b96d745391f8e52f87eb91a93394d`
and reported `acceptance=passed`. Aggregate completion TPS improved from
`76.4926` to `96.8961`; SLO-compliant completion TPS improved from `62.6674`
to `87.4188`. Preemptions remained `1/1`, waiting remained `5.0/5.0` seconds,
TPS-floor violation fell from `106.1` to `20.7` seconds, and candidate maximum
idle-with-demand was `0.4` seconds.

The two-order median benchmark contract passed with no new allocations. HTTP
pre-forward protection improved from `43118.5 ns/op, 119 allocs/op` to
`13025.0 ns/op, 33 allocs/op`. Manager decision at 48 active reservations
improved from `4545.0` to `3303.5 ns/op`; at 256 it improved from `24090.5` to
`17488.0 ns/op`; both remained zero allocation. Policy medians ranged from
`0.9915x` to `1.0145x` of baseline. The contract-log SHA-256 is
`744a51204cf1a769763884bcecd06908cac432325bc758b1949a6498f7a92694`.

The candidate 4 MiB structural scan was `7.912 ms/op`, zero allocation; full
classification was `14.575 ms/op`, `4,210,086 B/op`, `22 allocs/op`; the 4 MiB
estimator was `0.242 ms/op`, zero allocation. The large-body benchmark log
SHA-256 is
`1e065a7d1a57a7e11b7cd684a74910bd79770db321299907f07aca3f1bdb9df0`.

This plan update is evidence-only and changes no executable or build input. A
final exact-file provenance archive must prove byte identity of every file
except this plan before commit. r28 authorizes commit/image work only after that
proof; it is not image, registry, Compose, deployment, Router, or production
evidence.

## 14. r51 live red and v0.12.2 corrective continuation

### 14.1 Promotion stop: request-specific hard protection was not projected

The Router-disabled enforce differential run
`enforce-differential-r51-d953233` completed from
`2026-08-07T23:02:00.6522877Z` through
`2026-08-07T23:02:57.4274904Z`. The existing 150,048-token request completed
HTTP 200, the quiescent candidate returned pre-forward HTTP 429, and the
same-pressure short request completed HTTP 200. Prediction deltas were exactly
three attempts, two fits, and one risk. The enforced-reject and represented-log
deltas were exactly one. Final reservations, Prefill ownership, backend
running/waiting, Router target running, preemptions, backend errors, and
internal failures were all zero. The Router digest and enabled set were
unchanged and `use1-cb` remained disabled.

The candidate was rejected as `hard_protect/kv`, not the harness-expected
`size_protect/prefill_busy`: its estimated Prefill was 287,254 tokens and its
645,120-token reservation did not fit beside the existing request. This is a
valid safety decision, but the same scrape reported
`pig_predictive_router_backpressure_active=0`,
`pig_predictive_router_backpressure_applied=0`, inspect capacity zero as an
inactive value, and unchanged effective compatibility capacity. The HTTP 429,
predictive counter, last-reject, and bounded log therefore did not have the
Router projection required by Sections 5.3 and 9.15.

Source review identified the cause. `recordDecision` stores the enforced
reject, but `PredictiveAdmissionTelemetry` independently evaluates only a
one-block synthetic inspect request against current state. When the current
state can accept a short request while the rejected business request itself
crosses hard KV, that inspect request fits and clears Router backpressure
immediately. The last-reject timestamp is exported but does not participate in
capacity projection. Existing tests cover a hard reject only when the tiny
inspect request also fails, so they did not cover request-specific hard
protection.

This is a product-contract red, not a harness-only failure. PIG v0.12.1 at
`d953233` and image digest
`sha256:28713fc7811100beeba71b46cbcfec71b77b303488367cce9334d1ab15fa2ef8`
must not be Router-enabled or promoted. The next candidate is v0.12.2; the
major/minor architecture and production configuration contract remain v0.12.

### 14.2 Minimal correction contract

The correction must preserve current-snapshot recovery while making every
enforced protection observable to Router:

1. The decision path remains unchanged: current observation plus atomic
   reservations decides each business request before forwarding.
2. A real enforce rejection publishes a bounded Router projection even when a
   one-block inspect request still fits. Shadow and malformed-client 400 paths
   never create this projection.
3. If current-state inspection already requires stronger backpressure, that
   current result wins. Otherwise, a recent load rejection publishes selective
   backpressure with inspect capacity one so Router can continue bounded short
   traffic while PIG retains request-size differentiation. A recent
   availability rejection publishes hard capacity zero.
4. The projection hold is an internal versioned constant of 1500 ms, long
   enough to cross the live Router's 1000-ms metrics poll once, and is not a
   production environment variable. It starts at the pre-forward rejection
   timestamp and cannot be extended by scrapes or successful requests.
5. After the bounded hold, a fresh open snapshot clears the projection without
   requiring another business request. No indefinite sticky clamp, low-flow
   self-lock, learned state, cooldown feedback admission, or second observer is
   introduced.
6. One telemetry scrape still reads one observer generation and atomically
   drives predictive backpressure, the six compatibility fields, upstream
   status, and metrics. Last-decision request fields remain diagnostic and do
   not replace current observer state.
7. Focused tests must prove the exact request-specific hard-KV case, hold
   boundary, immediate fresh recovery after the boundary, no business-state
   mutation from scrapes, shadow non-authority, malformed 400 isolation, and
   race safety.
8. The live differential harness must use a smaller active holder so the
   intended quiescent Prefill-busy branch is tested separately from hard KV.
   A distinct enforce-only atomic-reservation gate must reproduce the r51 hard
   case and require HTTP, counter, bounded log, last-reject, active/applied
   Router projection, compatibility fields, cancellation, and exact drain.

Any executable correction invalidates the v0.12.1 builder, image, shadow, and
enforce green evidence for release promotion. v0.12.2 must repeat the exact
source archive, focused red/green, full builder matrix, immutable image,
Router-disabled shadow/enforce matrix, and 30-minute canary gates before this
objective can complete.

### 14.3 r52-r56 builder evidence

The r51 live summary SHA-256 is
`f1b8adfcb40fb79cfbe0f95aaeacba3fb3a72d35efac3eb42d2e314cecd78e65`.
The focused r52 source archive was
`bf485866b7cc28804a67cae7ae154383d8f37a9cdd75adaa422feddddbb3603d`.
On the approved builder, formatting passed, the exact test failed, and its
failure matched `want recent selective KV protection`. The r52 evidence archive
was independently pulled and hash-verified as
`15c4ab226878541512ad34025f1701353f3f8f346e0216977d5f79cb04f10d2e`.
This is valid red evidence for the request-specific hard-KV projection defect.

The first green attempt r53 used source archive
`7a66fd5c29c154d1fd3a9e8e5f6e27d39d3a1cf95eb7b495ee340b012d826ce4`.
Formatting and the v0.12.2 binary passed, but focused/server/race tests failed
because three existing assertions still required same-instant open and the
static test provider was incorrectly expected to expose a nonzero observer.
Its evidence archive is
`5c04554b4c7996786977c090e5391d819d2f4f2f6ef25046858b1abb0e176354`.
r53 is retained as failed green evidence and authorizes nothing.

The corrected focused r54 source archive was
`3214c1b296e5bc3cc97cf059e74b9d0ecfe8c110da5064ba97869fc69308ff68`.
Formatting, the exact focused set, the complete server package, targeted race,
and the v0.12.2 binary all passed. Its evidence archive is
`75823e8dad12008238c5e8f7c0a245b52f58db8bf96ff44ea14f7bd0f0dec34e`.

r55 ran the full matrix from that source against the exact v0.12.1 `d953233`
baseline archive
`fec86cbca8a640aa8955a5511dd5ab7644d876c48cdfe861e9e2dc1724f564fe`.
Focused/full tests, vet, targeted/full race, all builds, versioned binary, two
byte-identical simulations, simulation acceptance, both benchmark orders,
the benchmark contract, and large-body benchmarks passed. The sole failure was
the strict legacy audit: the new HTTP integration test duplicated the six
`pig_dynamic_*` wire names outside their one compatibility-writer owner. r55 is
therefore not a full green matrix. Its evidence archive is
`b1f3738096dcd28689db7784c011f079c281925cea816ece82813e11f2bb4ba4`.

r56 removed only those test-string duplicates. Its exact source archive was
`94fbbcfe70d66365ca40d70d65c6d8468ef58d53b4be9004ddea634ea1450e6e`.
The remote exact diff contained only
`request_aware_predictive_http_integration_test.go`; complete non-test SHA
manifests were byte-identical. r55's full evidence manifest and status contract
were revalidated, and r56 independently passed formatting, strict legacy
audit, focused/full tests, vet, targeted/full race, all builds, and the
versioned binary. The r55 and r56 `-trimpath -buildvcs=false` binaries were
byte-identical at
`064d9dd65f21e4593843924ffe75b18482cd25bf6db40c7bae31d057ee77ee4a`.
The r56 evidence archive is
`402d5ea0050ace9d9ab62f5e1fd54c64a400da4dded1c94b844b3d511a073597`.
This exact executable-identity chain makes the r55 simulation and benchmark
steps applicable to r56 while correcting the audit-only test ownership defect.

The deterministic simulation remained byte-identical and passed acceptance.
Candidate aggregate completion TPS was `96.8961` versus `76.4926` for the
binary baseline, and SLO-compliant completion TPS was `87.4188` versus
`62.6674`. Preemptions remained `1/1`, waiting remained `5.0/5.0` seconds,
TPS-floor violation was `20.7` versus `106.1` seconds, and candidate maximum
idle-with-demand was `0.4` seconds. These are deterministic model results, not
live GPU throughput claims.

The paired benchmark contract passed without allocation growth. HTTP
pre-forward protection was `13,232 ns/op` versus `13,019 ns/op` (`1.0164x`,
`33/33` allocations). Manager decision was `3,299.5` versus `3,332 ns/op` at
48 active and `17,254` versus `17,270 ns/op` at 256 active, both zero
allocation. Policy ratios ranged from `0.9647x` to `1.0262x`, all zero
allocation. Candidate 4 MiB structural scan was `7.348 ms/op`, zero allocation;
full classification was `14.549 ms/op`, `4,210,092 B/op`, 22 allocations; the
4 MiB estimator was `0.217 ms/op`, zero allocation. The correction adds one
time subtraction and bounded branch only to telemetry projection, not the
pre-forward policy hot path.

### 14.4 Corrective review pass 1: model, protocol, and causality

Completed 2026-08-08. The live red proved that request-specific post-admit KV
cost can reject a large request while a one-block current-state probe still
fits. Replacing that real decision with the probe was the causality error. The
correction leaves business admission unchanged and projects only an already
completed enforce rejection. Load scope becomes selective inspect capacity
one; current stronger capacity zero wins; request-scoped client failures do not
change Router capacity. No tokenizer, cache lookup, model-specific threshold,
learner, routing decision, or feedback admission gate was added.

### 14.5 Corrective review pass 2: lifecycle, safety, and SOLID

Completed 2026-08-08. The hold starts from the immutable pre-forward reject
timestamp, cannot be extended by scrapes or successful requests, excludes
shadow and request-scoped client errors, and expires at exactly 1500 ms without
a new business request. Repeated real rejects may refresh it. Current-state
hard protection remains stronger than the recent selective projection. Tests
cover load/availability/request scopes, future and expired timestamps, the
boundary, shadow non-authority, malformed 400 isolation, current hard capacity
zero, compatibility arithmetic, lifecycle state immutability, and concurrent
telemetry/admission under race. The pure recent-reject helper lives with Router
projection; policy, manager, observer, HTTP transaction, and compatibility
writer ownership remain separate.

### 14.6 Corrective review pass 3: evidence, efficiency, and release

Completed 2026-08-08. r52 is valid red, r53 is failed green, r54 is focused
green, r55 is full executable evidence with an audit red, and r56 proves the
audit correction is test-only while re-running every source/race/build gate.
The duplicate compatibility names were removed rather than weakening the
strict ownership audit. Binary identity, full race, deterministic simulation,
paired benchmarks, large-body cost, and evidence archives are recorded above.
The 1500-ms hold is an internal versioned constant chosen to cross the current
Router's 1000-ms poll once; it is not a production environment variable and it
retains inspect capacity one to limit throughput loss. This review authorizes a
final plan-only provenance archive, commit, image build, and Router-disabled
rerun. It does not authorize Router enable or inherit any v0.12.1 live green
gate for v0.12.2.

r57 then froze source archive
`be8868915ce321f280310769e46ffcc603d4dd77c41d564d0094cc48c8f37b66`.
The remote exact diff against r56 contained only this plan document; the r56
evidence manifest revalidated, formatting, strict legacy audit, full tests, all
builds, and the v0.12.2 versioned binary passed. The r57 binary remained
byte-identical at
`064d9dd65f21e4593843924ffe75b18482cd25bf6db40c7bae31d057ee77ee4a`.
The independently pulled r57 evidence archive SHA-256 is
`2a190c56a94feb753f91d9993a4d385f4e7d2c0693b37885f610060b1aec4d20`.
Because recording r57 changes only this plan, the pre-commit r58 archive must
again prove a one-file plan-only diff and the same binary; no executable test,
simulation, race, or benchmark evidence may otherwise be inherited.

### 14.7 Exact commit and builder-local image provenance

r58 completed that final pre-commit gate from source archive
`375ee1510ec3b8110d8cca48c69e86cc58702f30547fd61c05bcd607cb0c74ee`.
Its summary is `overall=0`, records `inherited_r57=true`, and revalidated the
plan-only transition without replacing any r55/r56 executable evidence. The
exact v0.12.2 source was then committed and pushed as
`88cbb29d9666f77d670c03132866cba38b0c016a`. The commit archive
`pig-v0122-88cbb29.tar.gz` has SHA-256
`9eb69e62d1891f8fb410d438b61a5641062ab006a54e3f78f29a9f2943a47747`.
The two unrelated untracked v0.11 plan documents remained excluded.

r59 stopped before build because its host/container provenance path was
incorrect. r59b proved the only Windows-archive difference from r58 was CRLF
versus LF in `LICENSE`; it did not build. r59c normalized that comparison and
built the production image on the approved builder from the exact commit
archive. It passed the production image contract, OCI version/revision,
entrypoint, user, linux/amd64, NVML environment and native collector checks,
default-enforce startup, health, and authenticated/unauthenticated metrics.
r59c then stopped while constructing the hard-KV request because BusyBox
`head` lacks GNU `-c`; it has no hard-KV conclusion or final summary and is not
represented as a complete smoke run.

r60 continued against the unchanged r59c image and replaced the unsupported
fixture command. It passed with:

- builder-local image
  `ghcr.io/phala-network/phala-inference-guard:0.12.2-88cbb29-local`;
- image ID
  `sha256:4582a5224f0202f3d1d9e7384c01eec036afb308877abe7c55784a23ff769014`;
- production binary SHA-256
  `3b16e83d385d723d573831c1d9e81f7ef3556a21590c853022c5cb1fbb311bbc`;
- OCI revision `88cbb29d9666f77d670c03132866cba38b0c016a` and
  version `0.12.2`;
- default mode `enforce`, health 200, authenticated metrics 200,
  unauthenticated metrics 401;
- a pre-forward hard-KV 429 with enforced-reject accounting, active/applied
  selective Router projection, inspect capacity one, coherent compatibility
  capacity, bounded log attribution, and automatic open recovery after the
  1500-ms hold.

The production image binary is intentionally distinct from the non-CGO
`-trimpath -buildvcs=false` builder-test binary recorded by r57. Its identity is
proven by the production Docker build, native NVML contract, builder-local
extraction, and registry pull below; it is not inferred from the test binary.

### 14.8 Immutable registry publication and independent evidence pull

r61 used the workstation's existing Git credential only after authenticated
absence checks returned `manifest unknown` for both target tags. Its push was
denied because that token lacked package-write scope; no manifest was created
and the builder logged out. r61b used a bounded hand-written device flow, but
GHCR still returned a token-scope mismatch; the exact missing server-side
permission was not independently enumerated. It was likewise denied before
manifest creation and logged out. These failed runs authorize nothing.

r61c used the official GitHub CLI device flow. The returned token was verified
to contain `gist`, `read:org`, `repo`, and `write:packages`, was passed to the
builder only through SSH standard input, and was removed from both the builder
Docker config and the isolated local CLI config after publication. r61c again
proved both tags absent with explicit `manifest unknown`, then published:

- `ghcr.io/phala-network/phala-inference-guard:0.12.2`;
- `ghcr.io/phala-network/phala-inference-guard:0.12.2-88cbb29d9666`.

Both pushes produced registry digest
`sha256:7cafb935d48175045cd355a844a3f94638fdfae16f965e2a9d7dbedeee63c4e4`.
The builder pulled that digest and revalidated image ID, linux/amd64, user
`0`, entrypoint `/phala-inference-guard`, OCI version/revision, NVML
environment, and the extracted binary SHA-256. The source, builder-local, and
registry-extracted production binaries all equal
`3b16e83d385d723d573831c1d9e81f7ef3556a21590c853022c5cb1fbb311bbc`.

r62b revalidated the unchanged r59c evidence against the manifest frozen at
r60 startup, revalidated the complete r60 and r61c evidence manifests, and
confirmed builder GHCR credentials absent. Its full builder archive SHA-256 is
`ba3c3013372b3bc13a2f983b9cadaf2286f63c4b4e0e1e2c192a55995f01d9af`.
Because TLS-over-SSH made redundant binary transfer impractical, r62d produced
a light evidence archive excluding only the already-hashed PIG binary copies
and two smoke-fixture executables. Its independently pulled SHA-256 is
`01419772d09f6ebb9d65927bdb5e4b674cba3f9e14e4179377a6d96274302fa8`.
Local verification recomputed 71 included manifest entries, accepted exactly
four excluded PIG binary entries only at the expected production binary hash,
proved the current and frozen r59c manifests byte-identical at
`a570bc69e776bfd8d24a08c99f574c5c8a8a805323c84aaf66c6a7bdebe0280a`,
and found no credential token pattern.

This completes source-push, builder-local image, registry publication, and
digest-pull provenance layers only. It does not inherit v0.12.1 live evidence,
deploy either v0.12.2 mode, authorize Router enable, or satisfy the remaining
Router-disabled shadow/enforce and 30-minute canary gates.

### 14.9 Publication evidence review: three passes completed 2026-08-08

Pass 1, model and causality: publication introduced no executable source
change. r60 exercised the production image's real default-enforce HTTP path and
proved that a hard-KV pre-forward rejection causes matching request accounting,
Router projection, compatibility capacity, and bounded recovery. This narrow
smoke supports image contract and request-aware projection only; it does not
replace the required live size, lifecycle, differentiation, or atomic gates.

Pass 2, safety and lifecycle: each failed authentication run rechecked tag
absence and stopped before manifest creation. The successful run rechecked
absence again, published both tags, pulled by digest, and logged out. Builder
Docker credentials and the isolated local CLI credential record are absent.
r59c partial evidence is inherited only through the byte-identical manifest
frozen by r60; r60 and r61c manifests independently revalidated.

Pass 3, evidence and release: the remote source ref resolves to `88cbb29`; both
current registry tags resolve to the recorded digest and linux/amd64 platform;
the builder-local and registry-extracted identities match; and the pulled light
archive passes its own hash, path-exclusion, manifest, and secret-pattern
checks. The review leaves deployment, both Router-disabled mode matrices,
Router enable, 30-minute observation, and final state deliberately incomplete.
