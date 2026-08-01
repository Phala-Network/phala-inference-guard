# PIG v0.10 model-agnostic approximate predictive admission

Status: active plan. The v0.9.4 Gemma4-exact deployment path is superseded and must not be deployed.

Last updated: 2026-08-02 (Asia/Shanghai).

## 1. Authoritative objective

PIG predicts before a request enters the upstream. Feedback only improves later
predictions. Per-user TPS is the primary service objective; TTFT, TPOT, KV
capacity, and preemption remain joint constraints. Subject to those constraints,
PIG should progressively increase SLO-compliant total throughput.

Input size is deliberately approximate and model-agnostic:

- no exact model tokenizer or chat-template reproduction;
- no Gemma, Qwen, Llama, GLM, or other family-specific runtime code;
- no upstream tokenizer RPC on the request path;
- estimate cheaply, then calibrate with qualified `usage.prompt_tokens`;
- accept imperfect cold-start estimates and become more accurate with samples;
- keep prediction, learning, and reservation state bounded and race-safe.

PIG does not route and this version does not inspect prefix-cache/cache hits.

## 2. v0.9.4 disposition

v0.9.4 hard-codes a Gemma4 renderer, requires
`renderer_version=gemma4-text-v1`, pins one served model, loads exact tokenizer
assets through a native ABI, and uses a model-specific immutable profile. It is
an exact Gemma4 experiment, not a model-agnostic PIG.

Keep the generic pieces: pre-forward decision, atomic reservation, post-admit
KV/TPS/TTFT/TPOT forecast, lifecycle reconciliation, qualified completion
feedback, bounded residual calibration, telemetry, and shadow/enforce isolation.

The following artifacts exist but are not deployment evidence:

- source commit `453e32c63bf87754c85fb85b214ca09c183c4c64`;
- image `ghcr.io/phala-network/phala-inference-guard:v0.9.4@sha256:62320a00d52ffb5fcc6d30b26b484bbb4eb4bfe13f2480d8cfd06e9c1cccfdef`;
- no v0.9.4 Compose integration or CVM deployment occurred.

Do not overwrite/delete that tag; keep it as an unused historical candidate.

## 3. Reuse the existing fast estimator

`internal/domain/kvadmission.EstimateJSON` already performs a model-agnostic,
bounded JSON scan. It estimates an interval from string bytes, tool/function and
response-schema bytes, message/tool counts, multimodal markers, and the requested
output limit or bounded fallback.

The predictive path must consume this cost directly instead of cloning the body,
rendering a model-specific prompt, and calling the native exact tokenizer. The
classifier already buffers the bounded body and extracts output-token fields; do
not add another body copy. Fuse the current field and feature scans only if remote
builder benchmarks prove the second linear scan material.

## 4. Real admission transaction

```text
bounded request classification
  -> model-agnostic input-size interval
  -> conservative learned size multiplier
  -> backend observation plus every unabsorbed reservation
  -> post-admit KV/TPS/TTFT/TPOT forecast
  -> atomic decision and reservation
  -> local QoS gate and forward commit
  -> response usage/latency observation and terminal release
  -> qualified samples update only future predictions
```

No outcome may retroactively alter the request that produced it.

## 5. Approximate-size contract

Return raw low/high, calibrated input upper, decode upper, request class,
confidence, source (`cold` or `learned`), and sample count.

Rules:

1. O(body bytes), bounded by the classifier limit, with no network/filesystem.
2. No model-family branch and no requirement that `request.model` match a pin.
3. Cold start uses the existing conservative byte/token interval.
4. Qualified `usage.prompt_tokens` trains actual/raw ratios. A bounded rolling
   upper quantile plus safety margin produces future input upper bounds.
5. Learning must handle ratios above 1.0 and may reduce cold overestimation only
   after maturity.
6. Missing, invalid, duplicate, stale, failed, cancelled, or censored outcomes
   do not train.
7. Backend epoch or estimator-version change invalidates incompatible samples.
8. Samples and cardinality are explicitly bounded.
9. Unsupported bodies become `unknown`; enforce never invents precision.
10. Multimodal payload bytes are not exact tokens; retain bounded allowances.
11. Prefix caching does not reduce the candidate's conservative input upper in
    this version. `usage.prompt_tokens` and the estimator both describe the full
    prompt; an unknown cache hit may improve reality but is never assumed by
    admission.
12. Each reservation stores the exact raw and calibrated upper used for its
    decision. Later learning never recomputes or shrinks an existing
    reservation.
13. The first implementation has a fixed, compile-time-bounded request-class
    key space. It must not use raw model names, tenant text, prompt content, or
    another user-controlled string as an unbounded map key.
14. Retained reservation/learning state contains numeric features and versioned
    identities only. Raw request bytes and extracted strings are scrubbed after
    the decision and never appear in logs, metrics, samples, or status output.
15. A positive actual/raw ratio outside configured bounds is not silently
    clamped downward. It clears optimistic calibration, records an anomaly, and
    immediately falls back to the usable cold upper. It must not turn the class
    into permanently `unknown`, because rejection would then prevent the new
    feedback required for recovery.
16. Learning maturity is never a prerequisite for a request that is otherwise
    safe under the cold estimate. Zero, sparse, missing, rejected, or expired
    samples return to cold behavior; they never create an implicit admission
    cap, sticky zero, or a learner-dependent lockout.

Use one generic calibrator per PIG/backend epoch and request class for the first
implementation. This supports any model deployed behind a PIG without source or
image changes. Future multi-model isolation, if required, must use a bounded
generic identity key and global fallback, never family-specific code.

## 6. Qualified feedback

Extend response usage parsing to include `prompt_tokens`. A size sample is valid
only when the request was forwarded, exactly one terminal usage object exists,
`prompt_tokens > 0`, estimator/backend identities match, the raw estimate was
retained in bounded reservation state, and the request completed successfully.

TPS/TTFT/TPOT learning keeps the same next-request-only rule. Prefer backend mean
ITL/generation duration and use local semantic timing only as a qualified
fallback. Missing usage is telemetry, not fabricated evidence.

Size samples are kept only in memory. A PIG restart starts cold; no stale model
or traffic distribution is silently restored. Size-learning update occurs only
after the completed reservation has reached a terminal state, so it cannot
change its own KV accounting. A concurrent request may use the new snapshot and
is, by definition, a later prediction.

Shadow mode must also be able to learn from a request that the predictor would
reject but the proxy intentionally forwards for observation. Such a request
uses a bounded, payload-free **shadow-only observation record** containing the
immutable numeric prediction, estimator identity, size estimate, and lifecycle
flags. It is not a resource reservation, never contributes KV or virtual
concurrency, and cannot make an enforce decision appear to fit. Only a
successfully forwarded and completed record with qualified usage/timing may
train later predictions. A later overlapping admission/forward censors the
older record so the final outcome is not attributed to its original feature
state. Enforce-mode rejects create no observation record and cannot train from
work that never reached the backend. Cap exhaustion fails observation closed
without changing shadow client behavior, and shutdown drops every remaining
observation record without pretending to release KV.

## 7. TPS-first prediction

For every candidate:

1. project observed KV + unabsorbed reservations + candidate upper;
2. predict existing users' post-admit TPS;
3. predict the candidate user's TPS;
4. predict TTFT/TPOT upper bounds;
5. risk/reject on KV, preemption cooldown, TPS, TTFT, or TPOT violation;
6. otherwise reserve future demand atomically.

The objective is SLO-compliant goodput, not raw admitted requests. Cold QoS uses
explicit conservative defaults and exposes `source=cold`; learned values become
active only after the minimum qualified sample count. The coordinator continues
admitting cold-safe work until an explicit, current KV, TPS, TTFT, TPOT,
preemption, freshness, or lifecycle constraint binds. A learned state is not by
itself a reason to stop intake.

## 8. Configuration simplification

Remove predictive runtime requirements for served model/revision, tokenizer and
template paths/hashes, model-family renderer version, native tokenizer ABI, and
exact token-ID oracles.

Keep only mode, backend observer, metric freshness/timeouts, preemption cooldown,
protected KV budget, decode bounds, TPS/TTFT/TPOT targets, estimator settings,
and calibrator age/sample/memory bounds. Observe vLLM KV capacity/block size from
metrics instead of duplicating an exact model profile. Capacity/epoch drift must
still reset reservations and learning safely.

The vLLM implementation performs a bounded startup probe before constructing the
coordinator. It derives the protected token budget by aligning the configured
ratio to the observed block size. A later capacity or block-size change makes the
observer unhealthy, clears or expires incompatible reservations and learning,
and produces `unknown` until a new coherent coordinator is created; it never
continues with the old capacity identity.

## 9. SOLID boundaries

Use narrow interfaces:

```text
RequestSizeEstimator
InputSizeCalibrator
BackendObserver
QoSPredictor
AdmissionCoordinator
CompletionOutcomeObserver
```

The estimator knows request bytes, not metrics; the size calibrator knows ratios,
not HTTP; the observer knows serving state, not policy; the predictor knows
features/outcomes, not parsing; the coordinator owns atomic lifecycle; the HTTP
adapter only wires events and contains no model logic.

Lock ordering is explicit: the estimator/calibrator produces an immutable
snapshot before the coordinator transaction; the coordinator never calls back
while holding its reservation lock; terminal release completes before the size
learner is updated. `Close` prevents new estimates, expires each reservation at
most once, then closes observers without holding the adapter lifecycle lock.

## 10. Efficiency gates

Run executable benchmarks only on the approved remote builder. Targets:

- no external call, tokenizer asset, Jinja, Rust FFI, or per-request handle;
- no `PredictiveBody` clone; the already buffered classifier body is scanned and
  then scrubbed by its existing owner;
- zero heap allocations inside the estimator scan;
- 1 KiB <= 10 us/op;
- 16 KiB <= 50 us/op;
- 64 KiB <= 200 us/op;
- 1 MiB <= 3 ms/op;
- linear time and memory bounded by the already buffered body.

Live shadow targets on the test CVM:

- typical <=16 KiB predictive p95 <=0.25 ms;
- <=64 KiB predictive p99 <=1 ms;
- no extra upstream tokenizer request and no response-body change.

These are acceptance thresholds, not measured claims.

## 11. Required red/green evidence

Estimator/calibrator:

- arbitrary model identifiers are accepted without family-specific branches;
  their literal JSON bytes may contribute normally to the approximate size;
- chat/completion/responses, tools, structured output, CJK, escaping, multimodal,
  malformed, unknown-length, saturated, and large-body cases are bounded;
- first request is cold; qualified prompt usage changes only the next estimate;
- underestimation raises the next upper bound; mature samples may narrow it;
- invalid/duplicate/censored usage cannot train;
- an out-of-range positive ratio disables optimistic calibration instead of
  being truncated into an unsafe bound, then returns immediately to usable cold
  estimation rather than permanent `unknown`;
- zero samples and indefinitely sparse low-flow samples remain cold-admissible;
  maturity, expiry, missing stream usage, or an anomalous sample cannot produce
  self-lock, false-lock, sticky zero, or poison the next request;
- epoch/version invalidation, concurrency, sample limits, and memory bounds hold.

Admission/lifecycle:

- all unabsorbed reservations enter post-admit state;
- check/decision/reservation is atomic;
- with metrics fixed, changing only learned size or TPS changes pre-forward fit;
- forward/local/backend/cancel/timeout/disconnect/panic/reset/shutdown paths
  release exactly once;
- cancellation, error, timeout, and missing terminal usage leave no phantom
  reservation and do not poison future estimates;
- after stale metrics, backend unavailability, preemption cooldown, or capacity
  reset clears, the first coherent snapshot can admit the next cold-safe request;
- after a TPS-risk rejection, draining running/waiting/reservations to zero lets
  the next cold-safe request enter; no rejection reason is sticky without a
  still-current binding constraint;
- concurrent decide/observe/terminate/invalidate/close has no mutex deadlock,
  lock inversion, livelock, goroutine leak, or starvation-induced false lock;
- a shadow risk decision with a complete numeric cost and valid prediction can
  obtain a bounded, non-accounting observation record, consume qualified real
  completion feedback, and improve only a later prediction; early unknown
  cases without an attributable prediction create no record, while the same
  risk or unknown decision in enforce remains a pre-forward reject;
- shadow-only records do not change virtual KV/concurrency or reservation
  counts, are censored by later overlapping work, are bounded under a held-open
  rejection flood, and converge to zero on every terminal path and shutdown;
- no false accept, false lock, self-lock, leak, double release, or unsafe
  capacity reuse in simulation.

Builder matrix:

- format/diff/vet/build/default tests/races;
- focused estimator/calibrator/HTTP/lifecycle tests and races;
- deterministic goodput and safety comparison with current threshold logic;
- estimator/prediction/lifecycle/completion benchmarks;
- final image shadow smoke without model assets;
- exact source archive and evidence hashes.

Exact-tokenizer parity is explicitly not a release gate for this design.

Deterministic acceptance thresholds:

- KV-hard, preemption-proxy, TPS, TTFT, and TPOT violations: zero;
- false accepts, false locks, self-locks, sticky-zero episodes, reservation
  leaks, and double releases: zero;
- every safe low-flow, recovery, and post-drain workload keeps making progress
  until a named current constraint binds;
- mature approximate predictor aggregate SLO-compliant goodput is not below the
  current threshold baseline and improves at least one mixed pressure workload;
- compare completed tokens and requests that satisfy per-user TPS, TTFT, TPOT,
  KV, and preemption objectives; rejected, failed, or SLO-violating work does not
  count as throughput improvement;
- any percentage improvement is reported from raw simulation output and never
  presented as live GPU throughput.

## 12. Three review passes

1. Model and causality: no family dependency; feedback changes only later
   predictions; TPS-first logic is prospective, not retrospective throttling.
2. Safety and lifecycle: uncertainty, bounds, atomicity, every terminal path,
   resets, races, and memory limits.
3. Evidence and release: red/green validity, builder reproducibility, latency,
   simulation, image provenance, Compose diff, rollback, live shadow proof, and
   a bounded real-traffic canary with an explicit iteration decision.

Revise this document after each pass. Do not inherit v0.9.4 executable evidence
after source changes except for unchanged components with explicit identity and
coverage proof.

## 13. Release/live boundary

Use the next breaking experimental version, provisionally `v0.10.0`. Do not
deploy before all builder gates pass. Then use only CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193`, first in shadow mode, and its exact
rollback Compose. Do not change Router source, vLLM, or another CVM. A later
Router write is limited to enabling or disabling this exact `use1-cb` upstream
for the authorized real-traffic canary.

The user explicitly authorized an actual deployment test on that CVM when the
v0.10 implementation reaches the live gate. This authorization does not waive
the builder gates, shadow-first boundary, pre-mutation drift/idle checks, or
exact rollback requirement, and it does not authorize deploying v0.9.4.

All Go/Rust/native/race/simulation/benchmark and image execution remains
remote-builder-only. Re-discover and verify the builder before use; do not treat
the builder identity in older evidence as permanent. Source inspection, editing,
archive creation, Git review, commit, and push may occur from the Windows
checkout. Source/version push remains authorized, but publication happens only
after the exact committed archive passes the full matrix and final-image gates.

The v0.10 Compose candidate is generated from a freshly queried live Compose and
may change only:

- PIG image to an immutable v0.10 tag+digest;
- model-neutral predictive environment/policy values;
- no tokenizer/template assets service, volume, or mount.

It must not change vLLM, HAProxy, ingress, model downloader, GPU arguments,
Router source/configuration, or another service. Immediately before deploy,
re-query platform status/progress, live Compose hash/content, route state,
protected metrics, running/waiting/queue, preemptions, and readiness. The target
route remains disabled and idle throughout deployment and shadow validation.

Use one Compose-only deploy without `.env` for the existing centralized-KMS CVM.
Rollback to the byte-exact predeploy Compose on any of these gates:

- platform operation failure or unexpected Compose drift;
- PIG/backend crash, restart loop, fatal/OOM/Xid, or readiness failure;
- observer/capacity identity remains unknown after the bounded startup window;
- authenticated models/metrics or protocol request failure;
- shadow changes a client admission/response result;
- prediction latency exceeds the recorded acceptance threshold materially;
- learning cannot mature from qualified low-concurrency samples;
- reservations do not return to zero after idle;
- preemption count increases during the controlled low-pressure validation.

Live evidence includes normal, streaming with usage, tool, structured output,
CJK, low-concurrency maturity, prediction latency, size calibration, TPS
outcomes, zero reservations, KV, preemption delta, and final readiness. Shadow
must not alter client admission.

Only after the exact image has passed the full builder matrix, deterministic
simulation, image smoke, deployed shadow gates, and the source review finds no
remaining release blocker may the candidate be called temporarily deployable.
The route must still remain disabled while the same immutable image is switched
from `shadow` to `enforce`. Because that restart starts learning cold, repeat
cold-progress, controlled sparse/low-concurrency maturity, prediction-latency,
protocol/readiness, TPS-risk rejection, post-drain recovery, preemption-delta,
and zero-reservation/zero-shadow-record gates in enforce mode. Do not generate
artificial pressure against the live backend; use only bounded controlled
validation. A failed enforce gate returns to the disabled-route repair loop and
does not authorize Router enablement.

Only after both deployed shadow and deployed enforce gates pass:

1. Snapshot the Router's exact upstream configuration and enabled/disabled set.
2. Recheck that runtime mode is `enforce`, target readiness and metrics are
   coherent, reservations and shadow-only records are zero after idle, and
   there is no unexpected Compose drift.
3. Enable only the `use1-cb` upstream. Do not alter weights, policies, bearer
   values, timeouts, another upstream, or Router source.
4. Observe a continuous 30-minute real-traffic canary. The interval starts only
   after Router state confirms enabled and the target receives a real routed
   request; an enabled target that receives no real requests is inconclusive.
5. Capture timestamped Router health/distribution, PIG and backend metrics at a
   bounded regular cadence, incremental PIG/backend/container logs, serial or
   platform health, and start/end configuration hashes. Retain numeric request
   outcomes only; never retain prompts, bodies, bearer values, or user content.
6. Analyze SLO-compliant goodput, per-request/user TPS, TTFT, TPOT, KV occupancy,
   running/waiting/queue, admission/rejection reasons, cold/learned source and
   sample maturity, prediction overhead/errors, reservation convergence,
   preemption delta, HTTP/protocol errors, crashes, restarts, OOM/Xid, and route
   distribution. Raw admits or GPU utilization alone are not success.

The live canary must also look explicitly for false locking: real demand plus an
idle/available backend and repeated admission rejection without a named current
constraint; admission remaining zero after metrics freshness, cooldown, drain,
or learner invalidation recovers; sparse traffic unable to pass through cold;
or reservations that remain after terminal outcomes. Any such event is a
release blocker even if aggregate error rate looks small.

Rollback or optimization is required for a crash/readiness/protocol regression,
an unexplained QoS guard violation, a new unsafe preemption pattern, a leaked or
double-released reservation, false/self lock, sticky zero, materially excessive
prediction latency, or lower SLO-compliant goodput under comparable traffic. On
such a finding, disable only `use1-cb`, let its work drain, preserve evidence,
and choose the byte-exact previous Compose when safety/readiness requires it.
Turn the finding into a red test, revise source and this plan, then rerun the
complete focused/full/race/simulation/benchmark/image/shadow/30-minute-canary
sequence. Repeat until a complete canary has no obvious remaining problem.

A clean 30-minute interval supports only the bounded conclusion "temporarily no
obvious issue under observed traffic"; it does not prove all production load
regimes. On success, retain the v0.10 Compose and `use1-cb` enabled, verify the
final Compose/image/runtime identity, `/v1/models`, protected and unauthorized
metrics behavior, zero idle reservations, preemption delta, no new fatal logs,
and the final Router set equals the baseline plus `use1-cb`. Scan every saved
artifact and Git diff for secrets before completion.

## 14. Current state

- v0.9.4 deployment stopped before Compose mutation;
- target remains on original PIG v0.8.12;
- v0.10 plan active;
- model-neutral JSON cost classification, bounded input-size calibration,
  `prompt_tokens` feedback parsing, generic upper-bound reservation, and the
  approximate HTTP adapter are committed on
  `5e2283d3cecb0a0a83af0e41e818f4841891d323`;
- the default factory now uses a bounded vLLM startup probe, observed model/KV
  identity, block-aligned protected capacity, model-neutral scheduler,
  calibrator, coordinator and observer; capacity or block-size drift permanently
  quarantines the old coordinator instead of reusing an incompatible epoch;
- focused HTTP evidence proves pre-forward TPS rejection, learned TPS expansion,
  post-drain recovery, stale-metrics recovery, preemption-cooldown recovery,
  capacity-drift quarantine, and exactly-once release for completion, local
  rejection, cancel, disconnect, upstream failure, timeout and expiry;
- focused size evidence proves cold progress, bounded maturity, next-request-only
  feedback, anomaly invalidation to usable cold behavior, and no training from
  missing prompt usage;
- legacy Gemma4 render/profile/native tokenizer/FFI/assets and their runtime
  configuration have been removed; the binary reports `PIG-v0.10.0` and the
  Dockerfile is a CGO-disabled Go-only image with OCI version `0.10.0`;
- telemetry red/green evidence proved the predictive estimator histogram is
  recorded independently of legacy KV shadow and now covers only the bounded
  classification/estimate phase, not downstream admission work;
- valid builder red archive
  `93b034581d7ec1a622f676cabdd6a3d395ab481230a35bf3657afebec4a509c9`
  proved the real shadow-only learning defect: a predicted-risk request was
  forwarded in shadow but returned no non-accounting observation record. The
  red log SHA-256 is
  `3ba40488f8d4e73030acdd05f1bde8d57c26369b154cd955b80c90511792492c`;
- the bounded shadow-only observation implementation now keeps observation and
  resource accounting separate, learns only from qualified forwarded terminal
  outcomes, censors overlapping QoS outcomes, caps held-open observations,
  creates no record for enforce rejects, and clears observations without a fake
  KV release on shutdown;
- the final committed-archive deterministic simulation recorded aggregate
  SLO-compliant completion-token goodput `39520` for the current-threshold
  baseline, `36704` for v0.9.0 KV-only, and `43232` for v0.10 predictive. The
  v0.10 policy recorded zero TPS/TTFT/TPOT/KV/preemption-proxy violations, zero
  false accepts, and zero reservation leaks; the current-threshold baseline had
  two TTFT violations and two false accepts, while the KV-only baseline had 32
  TPS, four TTFT, 32 TPOT, one KV-hard, and one preemption-proxy violation.
  These are deterministic simulation results, not live GPU throughput claims;
- intermediate exact archive
  `d97a62751ff4bd030b0f8ea359cc5ffbd6839e8b9bf7a9480263e00f35bc1627`
  passed the focused packages, `go vet ./...`, `go test ./... -count=1`, the
  targeted race matrix, `go build ./...`, and the deterministic simulation on
  the remote builder. Focused and full/race log SHA-256 values are
  `28aa75f9516b9ec697f394b9d7c4bd1ea96e3d0b0581c56bae419471a686b1dd`
  and
  `7dded9f0194fb11a20195851302c5a3731371efe098e206b947f77bb70938362`;
- the startup semantic-error race fixture now uses a race-safe bounded retry
  budget and still requires the semantic model-identity error rather than
  accepting a generic timeout;
- exact red archive
  `70fa85c6bdcd595ae2598c4ed9941605df71b5da6f73d950c99239adac1f055a`
  proved that predictive sample/cell/time environment values had defaults but
  no hard operational upper bounds. The focused configuration tests failed for
  the intended behavior, not formatting or runner setup; red log SHA-256 is
  `254cfbb19fd83921d8637b7ff1654bed4b4c203d1a4739831574986c00e83f0a`.
  The preceding r26 attempt is explicitly invalid evidence because gofmt stopped
  it before the behavioral test;
- the implementation now rejects raw or programmatic predictive configuration
  beyond five minutes for startup probing, one minute per metrics request, 256
  samples per class/cell, 256 scheduler cells, 4096 shadow observations, or 24
  hours of sample age. Bounds are checked before duration conversion, preventing
  overflow as well as accidental memory expansion;
- exact executable-source archive
  `ad68217caa4dc1dd23383ec619fb189676d78fa54c65371233b7c329f93c0293`
  passed remote-builder gofmt, focused tests and deterministic simulation; its
  focused log SHA-256 is
  `4d2264252bdac4ec0c5d770368cf4aaade534df38b8639559494ffe5e85c7853`;
- the same archive passed `go vet ./...`, `go test ./... -count=1`, targeted
  race, `go build ./...`, full `go test -race ./... -count=1`, and the complete
  benchmark matrix with status `0`. Complete log SHA-256 is
  `66d7daff57271afa956e84a61d53be5f2dcc4fa3c7430c0c91b04e3481e4f5b6`;
- five-run estimator maxima were approximately 0.232 microseconds at 1 KiB,
  0.763 microseconds at 16 KiB, 2.387 microseconds at 64 KiB, and 34.174
  microseconds at 1 MiB, all with 0 B/op and 0 allocs/op. Optional 2 MiB
  characterization was at most 68.517 microseconds with 0 B/op and 0 allocs/op;
- learned scheduler prediction was approximately 1.705--2.959 microseconds with
  256 B/op and 2 allocs/op; bounded retired-queue push was approximately
  12.80--13.86 nanoseconds with zero allocation; the full adapter lifecycle was
  approximately 5.750--6.280 microseconds with 832 B/op and 3 allocs/op; and
  streaming completion-usage parsing was approximately 28.006--42.409
  microseconds with 2009 B/op and 34 allocs/op. These are remote-builder CPU
  microbenchmarks, not service-chain latency or GPU-throughput evidence;
- exact clean commit `5e2283d3cecb0a0a83af0e41e818f4841891d323` was exported as
  `pig-v010-commit-5e2283d-20260802-r29.tar.gz` with archive SHA-256
  `77c46ec1ac9bd27f09f63b9f0cec55ed14cf105777eab03624b53a9e92057553`;
- that committed archive passed the remote gofmt gate, focused packages,
  verbose deterministic simulation, `go vet ./...`, `go test ./... -count=1`,
  targeted race, `go build ./...`, full `go test -race ./... -count=1`, and the
  complete benchmark matrix. The focused and complete log SHA-256 values are
  `324dc1ca293067df8e893862c2d2923644f3bc10759dd0345df819a43f15d403`
  and `f2146b2d821acc666bdfb0c98fc5b397759c6fe71e065329605dcfd5e19ee8b4`;
- final r29 estimator maxima across the recorded runs were approximately 0.264
  microseconds at 1 KiB, 0.757 microseconds at 16 KiB, 2.550 microseconds at 64
  KiB, 35.896 microseconds at 1 MiB, and 63.020 microseconds at 2 MiB, all with
  zero allocation. The full admission lifecycle was at most approximately
  3.865 microseconds. These remain builder CPU microbenchmarks, not service-chain
  latency or GPU-throughput evidence;
- plan-only commit `0bddb236a24d22dafb8f82b93e5c904ce5a5b735` changed only this
  document. Git object IDs for `Dockerfile`, `go.mod`, `go.sum`, `cmd/`, and
  `internal/` are identical to `5e2283d...`, so the r29 executable evidence is
  inherited only for those byte-identical build inputs;
- exact HEAD archive `pig-v010-head-0bddb23-20260802-r30.tar.gz` has SHA-256
  `8f39814eb7f962ba4b398a68fefbef0341339c10b6dd7115bcaa7bc8560ab259`.
  A no-cache remote build produced image ID
  `sha256:c970981da59b28249ee18575a25420132d98eda267cac36177e3003dce21387d`
  with size `29147183`, OCI version `0.10.0`, `CGO_ENABLED=0`, and no final
  filesystem path matching model/tokenizer/native-asset signatures;
- the final image gate passed off/shadow/enforce startup, `/healthz`, protected
  and unauthorized `/pig/metrics` and `/v1/metrics`, authenticated `/v1/models`,
  non-streaming and streaming OpenAI protocol smoke, shadow-risk forwarding,
  enforce pre-forward 429, and zero terminal reservation/shadow-observation
  gauges. Its status is `0`; image-gate log SHA-256 is
  `21f100fd7f1b45c38a6c8638a1afc41cd495513cc68418edcab67d7851c4b4e4`;
- image evidence archive SHA-256 is
  `84c48a137bdcc6b02d0003b5f2c9832c4a277bff0e4f5c40b12445da132c94f7`.
  The first smoke attempt is invalid product evidence because the harness gave
  its curl container a non-writable evidence mount; rerunning the corrected
  harness against the already successful no-cache build closed that harness
  defect without changing the image;
- `ghcr.io/phala-network/phala-inference-guard:v0.10.0` is published at
  immutable digest
  `sha256:f1aa7d198fcaaae2c0e8ca15c8288d99b450eb2d9cddc85ae43a1ada685c7ede`.
  Push log SHA-256 is
  `9cf04ea9ea1b8ba9634bff390acfa4785e3fa093caa9814bf11b900902635ff4`;
  a digest pull returned the same image ID, and an independent anonymous
  registry API check returned HTTP 200 with the same digest;
- source branch `codex/pig-v0.10.0-model-agnostic` and annotated Git tag
  `v0.10.0` are pushed to `pig-origin`; the tag points to exact image-source
  plan commit `0bddb23...`;
- builder registry credentials were relayed only through process stdin from an
  already approved authenticated builder, then removed from the build builder.
  The aborted device authorization, temporary credential state, CLI download,
  and incomplete transfer file were deleted; no credential value is retained
  in evidence or this plan;
- Compose shadow and disabled-route enforce deployment, Router enablement, and
  the first-real-request-started 30-minute canary all remain pending. No v0.10
  Compose, CVM deployment, Router mutation, or live serving evidence exists.

## 15. Recorded plan reviews

### Pass 1: model and causality — completed 2026-08-01, repeated 2026-08-02

Corrections made:

- replaced the invalid claim that different model-name bytes must yield an
  identical estimate with the real requirement: no model-family branch;
- made cache-cold/full-prompt estimation explicit so prefix-cache uncertainty
  cannot cause optimistic admission;
- required every reservation to retain the estimate actually used, preventing
  later feedback from rewriting the current request;
- added startup capacity/block-size discovery and fail-unknown behavior on
  runtime identity drift.

Pass 1 result: the plan now matches the user's approximate, model-agnostic,
pre-forward and next-request-only learning contract.

### Pass 2: safety, efficiency, and SOLID — completed 2026-08-01, repeated 2026-08-02

Corrections made:

- prohibited raw prompt/model strings in retained state, metrics, and logs;
- fixed learner cardinality to bounded request classes rather than untrusted
  model-name keys;
- required out-of-range ratios to invalidate optimistic learning instead of
  unsafe downward clamping;
- required invalidation to recover immediately through usable cold prediction,
  and made zero/sparse/expired evidence explicitly incapable of self-locking;
- required release-before-learning, explicit lock ordering, idempotent close,
  and restart-cold behavior;
- prohibited the current full `PredictiveBody` clone and made ownership/scrub
  behavior explicit;
- added hard configuration maxima before duration conversion so trusted
  operator input cannot bypass the memory/time bounds through misconfiguration
  or integer overflow;
- measured estimator, scheduler, retired-queue, full lifecycle, and completion
  paths before deciding that additional hot-path abstraction or allocation
  optimization would add complexity without a release-relevant latency gain.

Pass 2 result: the design now has bounded privacy, memory, lifecycle, and hot-path
contracts.

### Pass 3: evidence and release — completed 2026-08-01, repeated 2026-08-02

Corrections made:

- added zero-violation/leak/false-accept simulation gates and honest separation
  of simulated goodput from live throughput;
- added zero false-lock/self-lock/sticky-zero gates, explicit post-drain and
  post-freshness recovery, and SLO-compliant progress under sparse low flow;
- made executable and image validation remote-builder-only and required fresh
  builder discovery plus exact committed-archive identity;
- reduced the future Compose diff to image plus model-neutral policy only, with
  no tokenizer assets service or volume;
- added live drift, idle, auth, protocol, latency, learning, reservation,
  preemption, and log rollback triggers;
- kept Router write-free throughout deployment/shadow, then limited the
  user-authorized live change to enabling only `use1-cb` for a measured
  30-minute real-traffic canary;
- defined evidence-driven disable/drain/fix/full-retest iteration and exact
  final CVM/Router/secret verification.

Pass 3 result at that review point: the document became the authoritative
execution plan. Implementation evidence subsequently exposed the following
defects, so release approval is reopened rather than inherited:

- **Model/causality correction:** keep learned KV upper accounting separate from
  stable raw request-complexity feature identity; censor an earlier reservation
  when a later admitted request can interfere with its final QoS outcome; add a
  qualified shadow-only outcome path for predictor rejects.
- **Safety/efficiency/SOLID correction:** bound shadow-only records separately
  from accounting reservations, retain no payload/model family data, preserve
  lock ordering and idempotent close, and prove the held-open rejection flood
  cannot leak memory or create false resource pressure.
- **Evidence/release correction:** make the startup semantic-error test
  race-budget-safe without weakening bounded retries; require an exact new
  archive after every source/test change; and require disabled-route enforce
  cold/recovery gates before Router enablement and the real 30-minute canary.

The 2026-08-02 repeat closed the source-level shadow-observation, race-fixture,
resource-bound, full-race, simulation, benchmark, and committed-archive findings
on exact commit `5e2283d...` and archive r29. The plan-only evidence update after
r29 does not alter an executable or Docker build input and must be verified as
such before inheriting those results.

The image identity, smoke, publication, and source/tag provenance findings are
now closed by r30. The remaining release findings are deployed shadow,
disabled-route enforce cold/recovery gates, and the real-traffic canary. The plan
remains authoritative, but the candidate is not approved for Router traffic
until every preceding live gate passes.
