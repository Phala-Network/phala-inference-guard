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
15. A positive actual/raw ratio outside configured bounds is never silently
    clamped into training. A ratio above the maximum proves dangerous
    underestimation: clear the affected class, record a safety invalidation, and
    immediately fall back to the usable cold upper. A ratio below the minimum
    proves only safe overestimation: reject that sample, retain earlier mature
    samples, and do not record a safety invalidation. Neither side may turn the
    class permanently `unknown`, because rejection would then prevent the new
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
- a high out-of-range positive ratio disables optimistic calibration instead
  of being truncated into an unsafe bound, then returns immediately to usable
  cold estimation rather than permanent `unknown`; a safe low out-of-range
  ratio rejects only that sample and preserves already mature learning;
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
- authenticated `/v1/attestation/report` is not HTTP 200 with non-empty
  NVIDIA evidence when Router attestation is required;
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
   after Router state confirms enabled, Router `processed` advances,
   `pig_ok=true`/`stale=false`, authenticated `/v1/attestation/report` is HTTP
   200, and either PIG predictive-attempts or a vLLM inference
   request/completion counter advances from its pre-enable baseline. Router
   `processed` alone is not proof that inference reached PIG because attestation
   verification can advance Router-side accounting before forwarding. An
   enabled target without PIG/vLLM counter movement is inconclusive and, when
   `processed` continues advancing, is an immediate chain blocker rather than a
   reason to start the timer.
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
- the original byte-exact PIG v0.8.12 rollback Compose is retained with
  SHA-256
  `30ebb4df57185dd988f0be7830bb1dce58283be937298052116b9464f1de031d`;
- the only authorized target is currently running the published immutable
  v0.10.0 image in `shadow` mode. Its freshly queried live Docker Compose
  SHA-256 is
  `1a5052afea8fe83b8b182eabe0b6f5f558fd6e03dfc0981ea67639cca434c620`,
  platform state is `running` with no operation in progress, and `use1-cb`
  remains Router-disabled;
- v0.10 plan remains active, but v0.10.0 is no longer eligible to advance to
  disabled-route enforce or Router traffic because live shadow exposed the two
  release blockers recorded below. The corrective release is provisionally
  v0.10.1;
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
- v0.10.0 shadow Compose deployment and direct live protocol validation are
  complete on the sole authorized CVM. Authenticated `/v1/models`,
  `/pig/metrics`, and `/v1/metrics` returned 200; unauthenticated metrics
  returned 401. Normal chat, streaming with terminal usage, tool call,
  structured output, and CJK requests all returned valid protocol results.
  The backend reported model identity `google/gemma-4-31B-it`, KV capacity
  `862437` tokens, and block size `64`; target ratio `0.84` therefore protects
  a block-aligned budget of `724416` KV tokens. After idle, reservations,
  shadow observations, backend running/waiting, KV use, and preemptions were
  all zero;
- the initial five-request Windows harness attempt is invalid protocol evidence
  because in-memory JSON passed through PowerShell to curl lost quoting and
  returned 400. The corrected UTF-8 file plus `--data-binary` harness passed
  all five protocol cases. This was a harness defect, not a PIG regression;
- live shadow blocked release because the prediction and estimator histograms
  reuse generic duration buckets whose smallest upper bound is 100 ms. Five
  valid predictions had aggregate duration `0.000198` seconds (about 39.6
  microseconds mean), but the histogram cannot prove the required 0.25 ms p95
  or 1 ms p99 gates. v0.10.1 must give every histogram instance immutable
  validated bounds and use predictive-specific 10 us through 100 ms buckets
  that include exact 0.25 ms and 1 ms bounds, while preserving the generic
  buckets for TTFT and total-duration telemetry;
- live shadow also exposed a learner-liveness defect: two input-size samples
  were accepted, three safe low-ratio samples were rejected, three
  invalidations occurred, only one sample remained, and every one of five
  estimates stayed cold. A ratio below `MinimumMultiplier` means the cold
  whole-body estimate was safely conservative; it must reject only that sample
  and preserve mature samples without incrementing safety invalidations. A
  ratio above `MaximumMultiplier` remains a dangerous underestimation and must
  clear the class, increment invalidations, and recover immediately through
  cold estimation;
- v0.10.1 red evidence must first prove both current defects on a freshly
  discovered remote builder: sub-ms predictive buckets are missing, and a safe
  low-ratio sample destroys mature learning. After the smallest coherent fixes,
  rerun gofmt, focused/full tests, vet, build, targeted/full race,
  deterministic simulation, the complete benchmark matrix, no-cache image
  build, off/shadow/enforce image smoke, registry publication and digest-pull
  verification. No executable test is run on local Windows;
- regenerate both rollback and shadow candidates from freshly queried live
  state. For the v0.10.1 candidate, set the bounded startup dependency probe to
  `300000` ms so a normal approximately five-minute vLLM load does not restart
  PIG every ten seconds. This changes dependency-wait churn only and must not
  hide a restart after the backend is ready;
- only after v0.10.1 repeats direct shadow protocol, sparse/cold progress,
  mixed-request maturity, cold-to-learned transition, sub-ms p95/p99,
  no-false-lock/sticky-zero recovery, zero terminal state and zero preemption
  gates may the same digest enter Router-disabled enforce. Only after that full
  enforce gate passes may `use1-cb` be enabled and the first-real-request-started
  30-minute canary begin. Any finding restarts the complete repair loop; no
  Router mutation or canary has occurred yet.
- the builder was freshly rediscovered as running CVM
  `4f167f6e-4c50-415f-99f2-94b65652beba`, app
  `ff40ee31b95e89ebb242c223514adc715ac8a301`, with the
  `pig-ubuntu-builder` container and `/usr/local/go/bin/go`. Red archive r1
  SHA-256
  `7c38b603890313ed51b6d70442751dd81799a73e6616ea1b7d9203e06706035e`
  failed both focused tests for the intended behaviors; its reproducible red
  log SHA-256 is
  `17989abc0ccad2c960ce74fed126d82b3efbcfc80b998368ad21795d3f8e17cf`;
- green archive r2 SHA-256
  `dcfc438593331177e3957d1e8c3d05e4527f88b085e9e2cd85dd5a9b60f400e9`
  passed the four focused packages once, but a recorded repeat exposed a real
  pre-existing startup-probe diagnostic flake: an observed model-identity
  validation error could be overwritten by a later fetch timeout. This repeat
  is not counted as green. The fix retains the last semantic validation error
  and last fetch error separately and reports both at the bounded deadline;
- reviewed green archive r3 SHA-256
  `7ffd4a03b44bde38edf1f59f27b2af5dcb98943e5d5a7a8993b535c5c7ef4be3`
  is gofmt-clean and passed the four focused packages, 30 serial repetitions of
  the former flaky semantic-error test, and 10 race repetitions of that test
  plus the deterministic semantic-then-fetch-timeout test. The complete builder
  matrix remains pending and must run from a new exact archive containing this
  evidence update.
- full-matrix archive r4 SHA-256
  `3151db83c94817269d64fba571177dd88d75b8fe44daedac81d203681a1b284b`
  was gofmt-clean but correctly stopped at `go vet`: the fallible custom
  histogram constructor returned a local struct containing `atomic.Uint64`,
  which copied a `noCopy` value. r4 is failed evidence, not an inherited green
  run. The public fallible constructor now returns a pointer; the legacy static
  constructors build an unused literal directly, so no initialized atomic
  state is copied;
- corrective archive r5 SHA-256
  `7dbe687818cacf24b308344cfee3f4fddc041713fc4a21b44b1aba51ffd450a9`
  is gofmt-clean and passed the four focused packages plus `go vet ./...`.
  This focused result closes the atomic-copy defect but does not replace the
  complete matrix, which starts again from the next exact archive.
- exact full-matrix archive r6 SHA-256
  `56f04fde4cf8d74c127bb2281499c98b953ae84789fdc90cc94951f3f97e92f4`
  passed remote Go 1.24.5 gofmt, `go vet ./...`, `go test ./... -count=1`,
  the targeted race packages, `go build ./...`, full
  `go test -race ./... -count=1`, the verbose deterministic goodput gate, and
  the complete five-run benchmark matrix. Every gate recorded status zero;
  complete log SHA-256 is
  `604c76c8a36024a60b9d448ba3254d382d287dfd4793424d3b5d544d9b35e6d7`;
- r6 deterministic aggregate results remain `39520` current-threshold,
  `36704` v0.9.0 KV-only, and `43232` v0.10.1 predictive SLO-compliant
  completion-token goodput. Predictive recorded zero TPS, TTFT, TPOT, KV-hard,
  and preemption-proxy violations, zero false accepts, and zero reservation
  leaks. These are deterministic simulation results, not live GPU throughput;
- r6 estimator maxima across five runs were approximately 0.249 microseconds
  at 1 KiB, 0.692 microseconds at 16 KiB, 2.582 microseconds at 64 KiB, 35.775
  microseconds at 1 MiB, and 78.327 microseconds at 2 MiB, all at 0 B/op and
  zero allocations. Learned scheduler prediction was at most approximately
  3.578 microseconds at 256 B/op and 2 allocations; the full predictive adapter
  lifecycle was at most approximately 6.091 microseconds at 832 B/op and 3
  allocations. Histogram instance-bound construction is startup-only and does
  not enter these per-request allocations;
- the local evidence copies are retained under the ignored live-evidence
  directory. The r4 vet-red log SHA-256 is
  `1900aed03e7e85c96a700443687cf5acf5adc602db886075948ec523011cd97e`.
  No executable test was run on Windows;
- the reviewed v0.10.1 executable changes were committed and pushed as
  `01f07d71d85c9165aeb54f81ef917263973495e7`. The exact committed archive has
  SHA-256
  `4205f6a7e232b6a079c3f3abed4c43f835d35105bf0bf89f05c01e26a923f993`;
- three final-image harness attempts are retained as invalid final evidence,
  not product failures. r7 malformed a shell continuation before the build
  started; r7b completed the no-cache build but passed the builder-container
  `/work` path to the host Docker daemon as an evidence mount; r7c passed image
  identity, off mode, sub-millisecond buckets and most shadow checks, but
  asserted a currently learned estimate immediately after the sample that only
  reached maturity. That assertion violated the next-request-only learning
  contract. r7d adds the required subsequent request and separates the large
  predicted-risk request from the small fit requests;
- the corrected r7d gate built exact archive `4205f6a...` without cache and
  passed with `IMAGE_GATE_OK`. Builder-local image
  `pig-v0101-candidate:r7-01f07d7` has image ID
  `sha256:749ffb6fc3b9093b8f2c952dc22baef87b38c984a75c022afae90b32a4b130b8`,
  size `29151279`, OCI version `0.10.1`, `CGO_ENABLED=0`, and no final
  filesystem model/tokenizer/native-asset path. It passed off, shadow and
  enforce startup, health, authenticated models/metrics, unauthorized metrics
  401, non-stream and stream-with-usage protocol, shadow risk forwarding,
  enforce pre-forward 429, and zero terminal reservation/shadow-observation
  gates;
- r7d shadow learning recorded five accepted samples, one safely rejected
  low-ratio sample, five stored samples, zero invalidations, four cold
  estimates and three learned estimates. After the low-ratio sample, the next
  request remained learned. Prediction duration was at or below 0.25 ms for
  6/7 observations and at or below 1 ms for 7/7; estimator duration was at or
  below 0.25 ms for 4/7 and at or below 1 ms for 7/7. These are builder image
  smoke observations, not live service latency;
- the r7d image-gate log and evidence archive were copied from the freshly
  verified builder container into the ignored live-evidence directory and
  independently rehashed on Windows. Their SHA-256 values are respectively
  `8b33c8fe8d0ab9d13be226aea76e1c6a3b03716f469d618183151f7b883948dd`
  and
  `722fd6e60cc8b3a5a1af427b011777002cd26a97de6926b6b573c20e802b781a`;
- publish v0.10.1 only through the repository's existing tag-triggered
  `.github/workflows/publish-image.yml`, which grants the job-scoped
  `packages: write` permission. Neither current builder retains GHCR publish
  authentication, and the local GitHub credential does not have package-write
  scope; do not create, copy or persist a long-lived PAT. After publication,
  resolve the immutable registry digest, pull by digest on the builder and
  repeat the registry-image identity/off/shadow/enforce/sub-ms/low-ratio
  learning smoke before any CVM redeployment;
- the image-evidence plan update was committed and pushed as plan-only commit
  `2e4063da0d7356b09226372bd7adf55d258b7660`. Git objects for `Dockerfile`,
  `go.mod`, `go.sum`, `cmd/`, and `internal/` are byte-identical to executable
  commit `01f07d7...`. Annotated tag `v0.10.1` points to `2e4063d...`; official
  `Publish Image` workflow run `30717843162` completed successfully;
- GHCR resolves `v0.10.1` to immutable digest
  `sha256:3aca2bb90bc75fe7be9ab4fbb02202aa678855461eabd3bd768c0e682a5a8f83`.
  A fresh builder digest pull produced image ID
  `sha256:47f03bf3b517297b5c29c0c9569eaf46328bc9c59e969f6296223cfe8bddb717`,
  size `29151279`, OCI version `0.10.1`, `CGO_ENABLED=0`, and no prohibited
  model/tokenizer/native-asset path. Registry-image off/shadow/enforce,
  protocol/auth, shadow-risk forwarding, enforce pre-forward 429, low-ratio
  learning preservation, and terminal-zero gates all passed. Learning counts
  were accepted `5`, rejected `1`, stored `5`, invalidations `0`, cold `4`, and
  learned `3`; all 7 prediction and estimator observations were at or below
  1 ms. Prediction was 7/7 and estimator 6/7 at or below 0.25 ms. This small
  smoke characterizes the buckets but does not replace the larger live p95/p99
  gate. The registry evidence archive SHA-256 is
  `0735abcab4d948e04cbdf74e3b61aaaaa6f16d13388bc249605930feabfe968e`;
- fresh read-only live preflight at `2026-08-01T20:59:18.1568963Z` found the
  authorized CVM `running`, `in_progress=false`, and still on v0.10.0 shadow
  Compose SHA-256
  `1a5052afea8fe83b8b182eabe0b6f5f558fd6e03dfc0981ea67639cca434c620`.
  Router config digest was
  `sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`;
  only `use1-4c,use1-9b` were enabled, `use1-cb` remained disabled with
  route-running zero and processed baseline `234715`. Router reported the
  disabled target metrics stale/not-ok, while direct authenticated models and
  both metrics endpoints returned 200 and unauthenticated metrics returned
  401. Direct PIG/backend state was intake-open with zero running, waiting, KV,
  preemptions, reservations and shadow observations. Re-query all of these
  values immediately before mutation rather than assuming this snapshot holds;
- byte-exact rollback SHA-256 is the current Compose hash above. The v0.10.1
  shadow candidate changes only the PIG tag+digest and startup-probe timeout
  `10000 -> 300000` and has SHA-256
  `6e304b5803a92af3598209f380f93be177bebb30aa946c38a063221d0e590f07`.
  The enforce candidate changes only `shadow -> enforce` from that candidate
  and has SHA-256
  `041aa8aeff89ae5a255ec6c982e5994fcf89315c53fa803109364c9b7658f4c5`;
- the sole authorized CVM was deployed once with the immutable v0.10.1 image in
  Router-disabled `shadow` mode. The deploy CLI did not exit cleanly after the
  platform response, so no second deploy was started. The platform reported
  `running`, `in_progress=false`, and ready after approximately 257 seconds;
  authenticated `/v1/models` recovered to HTTP 200 at
  `2026-08-01T21:13:04.9837737Z`;
- the live Compose SHA-256 is
  `6e304b5803a92af3598209f380f93be177bebb30aa946c38a063221d0e590f07`.
  The running PIG container uses
  `ghcr.io/phala-network/phala-inference-guard:v0.10.1@sha256:3aca2bb90bc75fe7be9ab4fbb02202aa678855461eabd3bd768c0e682a5a8f83`,
  image ID
  `sha256:47f03bf3b517297b5c29c0c9569eaf46328bc9c59e969f6296223cfe8bddb717`;
  vLLM, PIG, HAProxy, and dstack-ingress are running. The enforce candidate
  remains SHA-256
  `041aa8aeff89ae5a255ec6c982e5994fcf89315c53fa803109364c9b7658f4c5`
  and differs from the live shadow Compose only by `shadow -> enforce`;
- all five direct protocol gates passed: normal chat, streaming with terminal
  usage, tool call, structured output, and CJK. Only status, usage, and protocol
  shape were retained; response bodies were discarded. Authenticated models,
  PIG metrics, and vLLM metrics returned 200; both unauthenticated metrics paths
  returned 401;
- twenty strictly serial, single-concurrency, low-output requests all returned
  200. One additional qualified sample reached input-size maturity, the next
  request changed from cold to learned, and a safe low-side sample was rejected
  without invalidation or deletion of mature learning. This closed the sparse
  low-flow false-lock, sticky-zero, and recovery gates without adding a
  preemption;
- streaming-with-terminal-usage learning produced reliable local TPS outcomes.
  After three qualified samples the scheduler source changed from `static` to
  `calibrated`; a corrected three-request repeat ended with calibrated sample
  count `9`, three additional scheduler accepts and local TPS outcomes, no
  invalidation, no risk/unknown decision, and zero terminal reservations and
  shadow observations. The earlier harness-only expectation that the source
  string would be `learned` is invalid terminology evidence, not a product
  failure;
- the final cumulative shadow metrics contain 34 attempts and 34 fit decisions,
  zero risk/unknown/enforced rejection, 30 accepted and four safely rejected
  input-size samples, zero input-size invalidations, 30 stored samples, six
  cold and 28 learned estimates, and a last learned estimate. Scheduler
  learning contains ten accepted outcomes, ten local TPS outcomes, zero
  scheduler invalidations, source `calibrated`, and nine samples for the last
  decision. All 34 prediction observations and all 34 estimator observations
  were at or below 0.25 ms, so both p95 and p99 are at most 0.25 ms and the
  1 ms p99 gate also passes;
- the final read-only drift audit at `2026-08-01T21:35:02.9662467Z` reconfirmed
  CVM `running`, `in_progress=false`, the same live Compose and image identity,
  Router digest
  `sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`,
  enabled set `use1-4c,use1-9b`, `use1-cb` disabled with route running zero and
  processed count `234715`, direct endpoint/auth gates, predictive intake open,
  and zero reservations, shadow observations, backend running/waiting, KV use,
  and preemptions. The disabled target's Router-side PIG state remains
  stale/not-collected as expected while direct protected metrics are healthy;
- direct post-readiness PIG, vLLM, and HAProxy log audits found zero 5xx,
  panic/fatal, OOM/Xid/engine-death, connection failure, or reservation/shadow
  lifecycle error. The only post-readiness HAProxy `<NOSRV>` lines are the six
  intentional local 401 responses for unauthenticated metrics. Current log
  SHA-256 values are respectively
  `1fd0da49a3ac9188f3563396624871db1500768a6b530fb6e9029c0251b395ef`,
  `bc8be03112cb56e189c3a2970fcd0d8e7d075ffc94eb20db41efaeacc8bc1691`,
  and `9e563bdba61d0b8745fecff29d22c2f9ba48855e14927fda6d3bf351ddadb326`.
  The final audit's live-token and generic-secret scans are clean;
- v0.10.1 therefore passed the Router-disabled shadow gate. A fresh enforce
  predeploy audit at `2026-08-01T21:41:24.7551479Z` reconfirmed the exact shadow
  Compose, disabled Router target, unchanged enabled set/digest, idle/open
  backend, zero reservations and observations, and zero preemptions. The
  enforce candidate was proven byte-for-byte equal to the live Compose after
  exactly one `PREDICTIVE_ADMISSION_MODE=shadow -> enforce` replacement;
- one Compose-only enforce deploy without `.env` completed successfully in
  approximately 255 seconds. The platform operation finished before service
  readiness: models and PIG metrics remained startup 503 until both became 200
  at `2026-08-01T21:52:32.9916929Z`. vLLM loading exceeded the bounded 300
  second dependency probe once, so PIG recorded one pre-readiness probe timeout
  and then started as `PIG-v0.10.1` with `predictive_admission=enforce` at
  `2026-08-01T21:52:24.287197393Z`. There was no repeated deploy and no
  post-readiness restart;
- the enforce cold baseline was exact: attempts, fit/risk/unknown/rejects,
  scheduler and input-size samples were zero; mode was enforce, intake was
  open, KV capacity was `862437`, and reservations, observations,
  running/waiting, KV use, and preemptions were zero. A 124-byte cold request
  returned 200 and reached vLLM exactly once. A `1600124`-byte JSON request with
  a short prompt, eight-token output horizon, and only trailing whitespace was
  rejected pre-forward with 429 and `kv_over_budget`; vLLM success and prompt
  token counters did not change. The immediately following 124-byte request
  returned 200. Intake stayed open and all terminal/failure counters stayed
  zero, proving cold progress, pre-forward enforcement, no sticky zero, and no
  reservation leak without GPU pressure;
- the first no-pause protocol harness retained a real bounded TPS-protection
  observation: normal, stream-with-usage, tool and structured requests returned
  200, while the immediately following small CJK request returned 429 with
  `existing_tps_at_risk` from the static predictor. At that decision the prior
  completion was still present in the 100 ms observer window, so the cold
  counterfactual represented two decode sequences at ten TPS each. This was a
  named current TPS constraint, not KV pressure or sticky intake closure. Once
  terminal idle was explicitly observed, a complete five-case repeat returned
  200 for every case. The next 100 one-second-spaced requests produced no new
  risk or enforced rejection. Preserve the original 429 evidence and monitor
  real canary `existing_tps_at_risk` rejections against simultaneous Router/PIG
  running state; repeated rejection while the observed backend is idle remains
  a canary blocker;
- the first low-flow latency assertion was also retained as invalid evidence,
  not hidden: it used only 20 observations for a p99 claim and used cumulative
  histogram counts polluted by the deliberate 1.6 MB risk probe. The harness
  now uses before/after histogram deltas for the exercised normal-size interval
  and at least 100 samples for p99. Its corrected 100-request run returned
  100/100 HTTP 200, added no risk/unknown/enforced rejection or preemption,
  kept intake open and terminal state zero, retained learned input-size state,
  rejected a safe low-side sample without invalidation, and respected the
  per-cell sample bound of 64. Prediction was 99/100 at or below both 0.25 ms
  and 1 ms; estimator/classification was 100/100 at or below both thresholds.
  This passes the declared p95/p99 gates while keeping the intentionally large
  rejection probe as separate body-ingress evidence;
- three additional streaming-with-terminal-usage requests all returned 200
  with 33 completion tokens. The first prediction remained static and produced
  the third reliable local TPS sample; the second and third used calibrated
  fit predictions. Scheduler accepts and local TPS outcomes each increased by
  three, final source was `calibrated` with four samples for the last decision,
  and risk/unknown/invalidation/preemption and terminal state did not change;
- final enforce metrics contain 136 attempts, 134 fit decisions, the one
  deliberate KV risk and one bounded immediate TPS risk, zero unknowns, 134
  successful vLLM completions, five accepted local TPS outcomes, zero scheduler
  rejection/invalidation, 124 accepted and ten safely rejected input-size
  samples, zero input-size invalidation, a bounded 64 stored samples, 13 cold
  and 123 learned estimates, and a last calibrated learned fit. Predictive
  intake is open; every admission failure phase, reservation, shadow
  observation, running/waiting, KV use, and preemption is zero;
- the final read-only audit at `2026-08-01T22:10:29.3207221Z` reconfirmed CVM
  `running`, `in_progress=false`, live enforce Compose SHA-256
  `041aa8aeff89ae5a255ec6c982e5994fcf89315c53fa803109364c9b7658f4c5`,
  immutable PIG image/image ID, all four serving containers running,
  authenticated models/PIG/vLLM metrics 200, unauthorized metrics 401, Router
  digest unchanged, enabled set still `use1-4c,use1-9b`, and `use1-cb` disabled
  at running zero and processed `234715`. After the readiness cutoff, PIG,
  vLLM, and HAProxy logs contained zero 5xx, panic/fatal, OOM/Xid/engine death,
  connection failure, or lifecycle error; HAProxy recorded only the two
  intentional enforce 429 responses. Current log SHA-256 values are
  `517ca4ef533b90d1ef2e32d3fb5e69109fa6384411c264762e1208d8e7b0adfb`,
  `b6850ba296e80c0f940d51cdbfa30e600d9433002b0cdc631fc54759d8cceb2c`,
  and `e02b0175686708dfe8b35bf268fb1c0668d247dbcbe729a91351ce28305f8cd4`.
  The 76-file enforce evidence scan found no live token, literal bearer, or
  private key;
- v0.10.1 has therefore passed both Router-disabled deployment phases and is
  temporarily eligible only for the authorized `use1-cb` Router canary. Router
  enablement, the first real routed request, and a newly timed continuous
  30-minute canary remain. `use1-cb` is still disabled and no real Router
  traffic has reached this enforce instance.

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
- required high-side out-of-range ratios to invalidate optimistic learning
  instead of unsafe downward clamping, while safe low-side ratios reject only
  the current sample and preserve mature state;
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
now closed by r30. The deployed v0.10.1 shadow findings are also closed by the
live gates recorded below. The remaining release findings are disabled-route
enforce cold/recovery gates and the real-traffic canary. The plan remains
authoritative, but the candidate is not approved for Router traffic until every
preceding live gate passes.

### Pass 1 live correction — repeated 2026-08-02 after v0.10.0 shadow

The model and causality review found that a low actual/raw ratio is evidence of
a safe conservative estimate, not evidence that existing learned state became
unsafe. The plan now separates low-side sample rejection from high-side safety
invalidation. This preserves next-request-only learning and lets qualified
mixed low-flow traffic mature without making the current request less safe.

### Pass 2 live correction — repeated 2026-08-02 after v0.10.0 shadow

The safety, efficiency, and SOLID review found that histogram bounds were global
rather than instance-owned and could not express the stated live latency SLO.
The corrective design gives each instance copied, validated, strictly
increasing bounds; predictive timing receives a narrow sub-ms distribution,
while unrelated service-latency histograms retain their existing bounds. The
startup probe Compose correction reduces known dependency-startup churn without
weakening post-readiness failure detection.

Source review also found no per-request allocation added by the histogram
change: bounds and counter slices are allocated once at construction, while
`Observe` remains one atomic count/sum update plus a fixed cumulative bucket
loop. The calibrator change retains the existing mutex and bounded class map,
adds no state, and has no new lock ordering.

The subsequent vet pass corrected one interface detail before release: a
fallible constructor for a type containing atomic counters must return a
pointer, preventing accidental copying after initialization. Static internal
factories retain value fields for compatibility but construct them directly
before first use. This preserves the existing storage layout without weakening
`go vet`'s `noCopy` contract.

### Pass 3 live correction — repeated 2026-08-02 after v0.10.0 shadow

The evidence and release review revoked the inherited v0.10.0 live approval:
means derived from histogram sums are characterization only and cannot prove
p95/p99. Both findings require focused remote-builder red evidence, a new exact
source archive and version, the complete builder/image matrix, fresh shadow and
disabled-route enforce gates, and then a newly started 30-minute Router canary.
The target remains safely Router-disabled during repair.

The evidence pass then reproduced an existing startup error-ordering flake: a
late transport timeout overwrote an earlier coherent semantic validation error.
The corrected probe retains the two error classes separately, and a
deterministic fixture now forces semantic error followed by timeout. Archive r3
passed 30 non-race and 10 race repetitions; this closes the focused fixture
finding but does not substitute for the complete matrix.

The next complete evidence pass caught the atomic-copy issue at vet before any
later gate and restarted from a corrected exact archive. Archive r6 then passed
every declared builder gate and retained the existing deterministic safety and
goodput results. The reviewed executable candidate was committed and pushed as
`01f07d7...`; corrected image harness r7d then passed the complete builder-local
image gate against its exact committed archive. The preceding r7/r7b/r7c
results remain explicitly non-final harness evidence and are not silently
promoted to product green. The plan-only commit/object-identity proof, official
tag workflow publication, immutable-digest pull and registry-image smoke are
now complete. Fresh live preflight retained the Router-disabled and idle target,
captured the current byte-exact rollback, and proved the shadow candidate has
only the two authorized changes. The subsequent v0.10.1 shadow deployment and
complete direct gates passed as recorded in current state. The same immutable
image was eligible only for Router-disabled enforce at that review point and
was not then approved for Router traffic.

### Pass 1 v0.10.1 shadow review — repeated 2026-08-02

The model and causality review checked the live request path rather than only
the presence of estimator and learner metrics. Direct requests produced 34
pre-forward predictions, input-size learning changed only subsequent requests
from cold to learned, and streaming terminal usage matured the TPS scheduler
from static to calibrated after the documented minimum sample count. A safe
low-side outcome did not rewrite the completed request or erase mature state.
The live evidence therefore matches the pre-forward/current-reservation and
next-request-only feedback contract. This review does not infer Router behavior
from direct traffic and authorizes only the next disabled-route phase.

### Pass 2 v0.10.1 shadow review — repeated 2026-08-02

The safety, efficiency, and SOLID review checked sparse progress, low-side
sample handling, bounded learned state, terminal convergence, and the separate
estimator/scheduler responsibilities. Twenty low-flow requests passed without
self-lock; all lifecycle/failure counters remained zero; reservations and
shadow observations returned to zero; no preemption occurred; and all 34
prediction and estimator observations fell in the 0.25 ms bucket. The plan-only
evidence update changes no executable boundary. No new abstraction or hot-path
optimization is justified before enforce because the measured live overhead is
already below the declared gates and no safety finding remains in shadow.

### Pass 3 v0.10.1 shadow review — repeated 2026-08-02

The evidence and release review separated startup transients from readiness:
the dependency probe and HAProxy 503s occurred while vLLM was loading, then
models and protected metrics became ready and every post-readiness 5xx/fatal/
OOM/Xid/lifecycle-error count remained zero. It also re-queried Compose,
container image IDs, Router membership, route counters, auth behavior, terminal
metrics, and logs instead of inheriting the deployment snapshot. Secret scans
are clean. The only valid promotion is from disabled-route shadow to
disabled-route enforce using the one-field candidate diff. `use1-cb` must not
be enabled until enforce repeats its cold-first, sparse recovery, protocol, TPS,
latency, pre-forward rejection, zero-terminal-state, and no-preemption gates.

### Pass 1 v0.10.1 enforce review — repeated 2026-08-02

The model and causality review proved the enforce decision path with upstream
counters, not merely PIG response codes. A cold small request advanced both PIG
and vLLM exactly once; the controlled KV-risk request advanced the PIG risk and
enforced-reject counters but left vLLM requests and prompt tokens unchanged;
the next small request advanced both again. Current feedback did not rewrite
any prior decision. Streaming terminal usage then changed only subsequent
predictions from static to calibrated. This closes the pre-forward and
next-request-only causal gates before Router traffic.

### Pass 2 v0.10.1 enforce review — repeated 2026-08-02

The safety, efficiency, and SOLID review preserved two initially failing
harness observations instead of promoting them. The immediate CJK 429 had the
named `existing_tps_at_risk` constraint during a bounded observer overlap; an
idle-aware full protocol repeat and 100 sparse requests proved recovery and no
repeated low-flow lock. The first p99 assertion mixed cumulative large-body
history with 20 normal requests, which cannot support an empirical p99 claim.
Interval-delta histograms over 100 requests passed the configured p95/p99
thresholds. These corrections changed only ignored evidence harnesses, not PIG
source or the deployed image. Learned state remained bounded, low-side samples
did not invalidate mature state, and every lifecycle/failure/preemption counter
remained safe. The observer-overlap 429 remains an explicit canary efficiency
signal rather than being erased.

### Pass 3 v0.10.1 enforce review — repeated 2026-08-02

The evidence and release review separated deploy completion, model loading,
the one bounded pre-readiness PIG probe timeout, PIG/backend readiness, and
post-readiness health. It re-queried the live Compose, image IDs, endpoints,
Router membership/digest, cold and final metrics, container logs, and secrets.
The target stayed disabled throughout enforce and no source, image, Router,
weight, policy, bearer, timeout, vLLM, or other-upstream change occurred. The
candidate is now eligible for exactly one next mutation: snapshot the Router
again and enable only `use1-cb`. The 30-minute timer must still wait for the
first processed real Router request, and any repeated idle TPS rejection,
self-lock, SLO regression, preemption, leak, fatal error, or lower comparable
goodput restarts the full disable/drain/repair/test loop.

### v0.10.1 Router canary correction — 2026-08-02

Fresh preflight kept the target platform running and idle, Compose SHA-256
`041aa8aeff89ae5a255ec6c982e5994fcf89315c53fa803109364c9b7658f4c5`,
PIG `v0.10.1` in enforce, and the Router baseline digest
`1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`
with only `use1-4c,use1-9b` enabled. The authorized mutation enabled only
`use1-cb` at `2026-08-01T22:25:18.3374184Z`; the resulting Router digest was
`7869dbc9822ec36b0d661bfa9eedcfa6799d9b00d54a97d40c9ebe1db53b5202`
and the audit found no other upstream field change.

The initial observer incorrectly treated Router `processed` moving from
`234715` to `234915` with `pig_ok=true` and `stale=false` as proof of real
inference. It was not: PIG predictive attempts stayed `136`, predictive risks
stayed `2`, enforced rejects stayed `2`, vLLM successful completions stayed
`134`, and vLLM running/waiting/KV/preemptions stayed `0/0/0/0`. HAProxy
recorded repeated attestation-backend HTTP 500 responses. An authenticated
direct request to `/v1/attestation/report` returned HTTP 500 with
`native NVIDIA collector requires linux with cgo and NVML`. The production
Dockerfile had built with `CGO_ENABLED=0`, selecting the non-cgo collector stub,
so Router attestation stopped real requests before PIG predictive admission.
Consequently this approximately three-minute observation is invalid and must
not be described as a 30-minute canary.

The observer was stopped and only `use1-cb.enabled` was returned to `false` at
`2026-08-01T22:28:55.3011186Z`. Route running drained to zero and the exact
baseline Router digest and enabled set were restored. The target stayed running
with no platform operation in progress; authenticated models/PIG/vLLM metrics
were HTTP 200 and unauthenticated metrics remained HTTP 401. No other upstream,
Router policy, bearer, timeout, CVM, or vLLM was changed.

This finding changes both pre-enable readiness and canary causality. Every new
candidate must prove authenticated attestation HTTP 200 with non-empty NVIDIA
evidence before Router enablement. The timer must use the conjunction in step 4
above; persistent Router accounting without PIG/vLLM inference counter movement
is a blocker that triggers disable/drain, not a successful traffic start.

### v0.10.2 attestation repair candidate — 2026-08-02

Commit `28a9b339b05a88d0d872adbcb7d0b1e32c32553d` contains the v0.10.2
candidate: production `CGO_ENABLED=1`, a dynamic distroless Debian runtime,
`NVIDIA_VISIBLE_DEVICES=all`, a production-image contract that rejects the
non-cgo stub and requires the native NVML collector path, matching OCI label
`0.10.2`, and runtime identity `PIG-v0.10.2`. The fix uses the existing
attestation adapter and does not change predictive admission, add a tokenizer
asset, introduce model-specific behavior, or modify Router/vLLM source.

The exact committed archive SHA-256 was
`741fca891f497201aaae106d684d8e012d6abccb2c0b94eb0b0987a9f3f32f4b`.
On the verified remote builder, the full Go/race/12-scenario/simulation/build
matrix and production-image contract both exited zero. Recorded performance was
`estimator_64kib_p95=3.072us`, `estimator_2mib_p99=180.92us`, and
`shadow_decision_p99=9.808us`. The first combined r3 runner reported smoke
status 2 only because it referenced absent
`/work/v0102-run-local-image-smoke-r3.sh`; its log contains no PIG execution and
is invalid harness evidence. After correcting the ignored runner to the
asserted work-directory path, the same source, archive, and already-built image
passed the full off/shadow/enforce smoke. Its evidence archive SHA-256 is
`8a485b9d4e66190e8173832d081fb79b50c63b00b07006232ad707c81e592daf`.

The smoke verified runtime/label version agreement, `CGO_ENABLED=1`, absence of
model/tokenizer/native assets, off pass-through, shadow response invariance,
bounded learning including a low-ratio rejected sample, prediction/estimator
metrics, enforce pre-forward HTTP 429, authenticated/unauthenticated metrics,
synthetic-backend isolation, and terminal reservations/shadow observations at
zero. This is builder-local evidence only. The branch/tag is not yet published,
the registry image/digest has not yet been validated, v0.10.2 is not deployed,
attestation has not yet been proved on the GPU CVM, and Router remains disabled.

### Pass 1 v0.10.2 repair review — completed 2026-08-02

The model/causality review ties the candidate to the observed forwarding
blocker: Linux+cgo selects the already-tested native NVML collector, while the
image contract rejects the exact non-cgo stub that caused HTTP 500. Runtime
identity and OCI label now agree. Predictive decisions, estimator/learner
features, QoS objectives, and feedback causality are unchanged, so the repair
does not claim a new throughput result.

### Pass 2 v0.10.2 repair review — completed 2026-08-02

The safety/efficiency/SOLID review found the repair confined to the production
build, attestation adapter selection, release identity, and image gate. The
dynamic runtime supports the cgo executable, NVML remains runtime-loaded, and
the existing NVIDIA device request is retained. Full race/tests/simulations and
the image behavior smoke passed; reservations, shadow observations, and the
synthetic backend converged exactly as required. No model-specific tokenization,
cache-aware admission, Router function, or new hot-path work was introduced.

### Pass 3 v0.10.2 repair review — completed 2026-08-02

The evidence/release review separated the valid full matrix and image contract,
the invalid path-only smoke attempt, and the valid focused smoke rerun. Logs,
status files, source/archive identities, and the smoke evidence hash are
retained. Publication must still build from the reviewed tag, pull the registry
artifact by immutable digest, repeat contract and off/shadow/enforce smoke on
that pulled image, and then execute fresh Router-disabled shadow/enforce live
gates. Only authenticated GPU attestation plus real PIG/vLLM counter movement
can authorize a newly started 30-minute `use1-cb` canary. Any finding repeats
the disable/drain/red-test/full-builder/registry/shadow/enforce/canary loop.

### v0.10.2 real-traffic canary correction — 2026-08-02

This section supersedes the earlier v0.10.2 release-eligibility statement.
Publication, immutable registry verification, Router-disabled shadow, and
Router-disabled enforce gates subsequently passed. The deployed enforce
Compose SHA-256 was
`add08f14c6dc726eba8dbcd72c265e4119b7a5b1229f98e44252f3e929352069`;
the registry image was
`ghcr.io/phala-network/phala-inference-guard:v0.10.2@sha256:32c1d9c7fa1a3a4217f5873725b03030f7118ff959bcae3c8ff817ad6e85f5da`
with image ID
`sha256:010e488c6ae601d6d428f51110e8a46fc8f1930ad791364410f0bfdddda863d1`.

Fresh preflight at `2026-08-02T00:29:19.8078326Z` proved the target CVM,
PIG, and vLLM running; predictive mode `enforce`; authenticated models,
PIG metrics, vLLM metrics, and attestation HTTP 200; non-empty NVIDIA
attestation evidence; unauthenticated metrics HTTP 401; intake open; and zero
reservations, shadow observations, backend running/waiting/KV, and
preemptions. The Router baseline digest was
`sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`,
the enabled set was exactly `use1-4c,use1-9b`, and `use1-cb` was disabled and
drained. The retained preflight artifact is
`tmp/pig-v010-use1-cb-live-20260802/v0102-canary-preflight-r3-20260802T002919Z`.

The authorized mutation enabled only `use1-cb.enabled` at
`2026-08-02T00:31:52.7993403Z`. The Router digest became
`sha256:7869dbc9822ec36b0d661bfa9eedcfa6799d9b00d54a97d40c9ebe1db53b5202`
and the field-level audit found no other upstream change. The canary timer
started at `2026-08-02T00:32:03.5849807Z` only after both PIG attempts moved
`132 -> 136` and vLLM successful completions moved `131 -> 132`, proving real
inference passed Router attestation and reached PIG and vLLM.

The supervisor stopped the canary after `1188.59` seconds, approximately
19 minutes 49 seconds, at `2026-08-02T00:51:52.1751458Z`. It therefore did not
complete the required 30-minute interval and must not be reported as a passing
canary. Across 33 samples, Router processed moved `234915 -> 236614`, PIG
attempts `132 -> 1824`, PIG risk decisions `1 -> 1512`, unknown decisions
`0 -> 3`, enforced rejects `1 -> 1520`, and vLLM successful completions
`131 -> 298`. vLLM preemptions and error completions both stayed zero and
predictive lifecycle failures stayed zero. The direct blocker was
`idle_reservation_leak_two_samples`.

Learning itself was active rather than inert. Decisions progressed from
`static/existing_tps_at_risk` to `calibrated/ttft_at_risk`; global scheduler
samples reached 64, multiple learning cells matured, calibrated decisions used
up to approximately 28 samples, and observed vLLM running rose from one to two.
Maximum observed KV utilization was approximately `0.0936198202`, waiting
stayed zero, generation tokens continued increasing, and observed single-user
TPS reached approximately 329. These are bounded positive causality signals;
they do not override the canary blocker or establish a throughput improvement.

The final two samples instead proved a temporary false/self lock:

```text
Router use1-cb running = 0
vLLM running = 0
vLLM waiting = 0
vLLM KV usage = 0
PIG predictive reservations = 1
vLLM successful completions no longer advance
PIG attempts and existing_tps_at_risk rejections continue advancing
```

HAProxy then recorded a request begun at `2026-08-02T00:50:47.247Z` and
completed at `2026-08-02T00:52:06.390546390Z` with timings
`743/0/0/59/78399`, HTTP 200, 2701 response bytes, and termination state
`CD--`. vLLM had already returned to running/waiting/KV `0/0/0` near
`00:50:50Z`, but PIG retained one resource reservation until the slow or
disconnected downstream data phase ended roughly 78.4 seconds later. The
reservation then returned to zero. This is not a permanent map leak; it is a
resource-lifecycle error that binds GPU/KV/TPS accounting to downstream
response completion after upstream inference has already terminated.

The supervisor disabled only `use1-cb.enabled` at
`2026-08-02T00:51:52.2618800Z`. The exact Router baseline digest and enabled
set `use1-4c,use1-9b` were restored, the target drained, and the post-disable
audit proved PIG/vLLM healthy with reservations, running, waiting, KV, and
preemptions all zero. The retained canary and causal audit artifacts are:

- `tmp/pig-v010-use1-cb-live-20260802/v0102-real-canary-20260802T003152Z`;
- `tmp/pig-v010-use1-cb-live-20260802/v0102-post-canary-blocker-20260802T005217Z`.

Consequently v0.10.2 is no longer eligible for Router traffic. Keep its
deployed Compose only as the disabled-route rollback baseline; do not enable
`use1-cb` again for this version.

### v0.10.3 slow-downstream lifecycle repair plan — active 2026-08-02

The next candidate version is v0.10.3. The repair must preserve the admission
prediction and QoS constraints while separating two lifecycles:

```text
resource lifecycle
  valid upstream inference terminal signal
  -> idempotently release GPU/KV/TPS accounting reservation

learning and downstream lifecycle
  -> retain only bounded numeric prediction/outcome state
  -> wait for the final handler result
  -> learn once only from a qualified successful outcome
  -> censor or drop cancel/disconnect/timeout/error outcomes
```

An upstream terminal signal must be grounded in the actual response protocol,
such as a fully consumed non-stream response or an explicit terminal SSE
marker. It must not be inferred from a slow client, current low KV alone, a
stale scrape, or handler elapsed time. Releasing resource accounting must not
fabricate terminal usage, train the learner, reopen a stale/failed backend, or
create unsafe headroom.

Required focused red/green evidence:

1. A first request reaches an upstream that emits a valid terminal response,
   while its downstream writer blocks before the HTTP handler can return.
2. On the current v0.10.2 behavior, its resource reservation remains active
   and an otherwise safe second request is rejected pre-forward with
   `existing_tps_at_risk`.
3. After the repair, the upstream terminal signal releases the first resource
   reservation before the downstream writer unblocks, and the safe second
   request reaches the upstream.
4. Unblocking, disconnecting, cancelling, timing out, erroring, closing, or
   panicking after early resource release cannot double-release, resurrect, or
   leak the reservation. Late completion cannot reserve resources again.
5. Scheduler and input-size learning run at most once and only for a real,
   structurally valid, uncensored successful outcome. Missing/duplicate usage,
   failed downstream completion, epoch invalidation, and observation eviction
   do not gain learned headroom.
6. Deferred outcome state is numeric-only, has a strict count bound and cleanup
   behavior, exposes enough telemetry to detect accumulation/drops, and never
   contributes KV, decode sequence, or TPS resource accounting.
7. Focused concurrency and race tests cover terminal-signal/handler-return,
   cancel, close, and observer-reconciliation interleavings. The final manager
   state and deferred observation state both converge to zero.

After the focused test is red for the intended v0.10.2 reason and green for the
repair, repeat the complete remote-builder focused/full/race/simulation/
benchmark/image-contract matrix. Do not run executable Go, race, simulation,
benchmark, or image gates on Windows. Build and publish v0.10.3 only from the
reviewed commit/tag, then repeat registry smoke, Router-disabled shadow, and
Router-disabled enforce on CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193`.

Only after fresh disabled-route gates prove attestation, protocol compatibility,
prediction overhead, learning, cold/recovery progress, no low-flow false lock,
zero terminal resource reservations, zero deferred observations after idle,
and no preemption/error/lifecycle regression may the supervisor enable exactly
`use1-cb.enabled`. A new continuous 30-minute interval starts only at the first
proved real PIG/vLLM inference. Any obvious problem again triggers automatic
single-field disable, drain, evidence capture, repair, and the entire sequence
above. A full clean interval permits only the bounded conclusion "temporarily
no obvious problem" and leaves `use1-cb` enabled for continued observation.

### v0.10.3 focused implementation and review evidence — active 2026-08-02

The original behavior-specific red was reproduced on the v0.10.2 lifecycle,
not on a broken harness. Its source archive was
`tmp/pig-v0103-slow-downstream-red-r1.tar.gz`, SHA-256
`e18f1b618567c2c44c1faf5cc257c5ec676b3c393ff8692cb36022a15ccfa185`.
The remote builder exited `1`; its log SHA-256 was
`b6ddb2ae10166677e2183d65ce714da993497b4cf1928ee85f58a881e5e1be95`.
The focused failure was the intended invariant:

```text
upstream terminal retained resource reservation behind slow downstream
Reservations:1 ReservedPhysicalKV:88 DecodeSequences:1
```

The first implementation green, r4, used archive SHA-256
`91b744e1d48f71822d328ad81095ba335e53445aae4517a77e77047e03ae792b`.
It passed the initial streaming and non-stream slow-downstream tests on
`go1.24.13 linux/amd64`; log SHA-256 was
`86ca1cab28d0a40a02bc863949b7bb52e543cc643da6a1d2b9bebcb6087a3763`.
That evidence is retained but superseded by the additional review and source
changes below.

Pass 2, safety, lifecycle, efficiency, and SOLID, was repeated against the
actual HTTP path. It found and corrected these issues:

1. Resource release and the interference bit are now read and applied in one
   Manager lock transaction. A concurrent new admission therefore either
   precedes release and censors the old outcome, or follows a completed release;
   the adapter cannot invent a race-dependent clean sample.
2. A valid explicit SSE `[DONE]` or complete non-stream EOF releases Manager
   GPU/KV/TPS accounting before a slow downstream write. Completion usage is
   retained only as bounded scalar state; scheduler and input-size feedback are
   still committed only by a qualified final handler outcome.
3. Semantic TTFT is timestamped when semantic bytes are read from the upstream,
   then committed only after the corresponding downstream write succeeds. This
   prevents slow client writes from being learned as model/GPU TTFT while still
   censoring write failures.
4. Streaming observation does not allocate a lookahead buffer. Non-stream EOF
   detection uses a fixed 32 KiB lookahead, matching the proxy copy size. Its
   incremental live-buffer bound is `32 KiB * admitted non-stream handlers`, or
   at most about 16 MiB at the default `GLOBAL_LIMIT=512`; raising that hard cap
   raises this bound proportionally. The existing response-copy buffer is a
   separate pre-existing bound.
5. Deferred learning state has an internal fixed default cap of 256 and is not
   exposed as a new production tuning knob. It retains no request body or token
   IDs and does not contribute resource accounting. Capacity overflow drops the
   learning opportunity, not resource release. The dropped handler-local scalar
   state is additionally bounded by the existing global in-flight cap.
6. `Close` now prevents new learning and waits for any already registered
   unreserved outcome to finish before returning. It clears retained deferred
   outcomes, censors them, and cannot race with a late learning side effect
   after shutdown completion.
7. Explicit tests cover `[DONE]`, EOF-only SSE, duplicate/malformed usage,
   `UnexpectedEOF`, truncated Content-Length, downstream write error, close
   before learning, close during registered learning, deferred-capacity drop,
   release/terminal races, concurrent new admission, and prefill absorbed versus
   unabsorbed reconciliation. All terminal paths converge Manager reservations
   and active deferred outcomes to zero.
8. The guarded reservation's unused mirrored `resourcesReleased` state was
   removed. Resource ownership, one release attempt, one terminal attempt, and
   panic isolation remain narrow consumer-owned interfaces; no model assets,
   tokenizer specialization, vLLM source, or Router source were added.

An intermediate r5 correctly failed after the review added stronger non-stream
EOF tests: its wrapper read from the source and then the lookahead reader,
consuming the body twice. That was an implementation red rather than a builder
failure. Its archive SHA-256 was
`7a412b85def1ce2b5707b7f0e9698720397321e5a46de39f9f45c6fff0ecd9ad`
and log SHA-256 was
`cdc7c80d4dbf96f4ffdfff216d12d20b6b3d289ab6856c28a074665197063514`.
The read paths were made mutually exclusive. r6 then passed but was superseded
when the close/learning barrier was added.

Focused r8 superseded r7 after the close multi-caller result, two-stage semantic
TTFT commit, and prefill-before-release ordering were added. Its exact archive
SHA-256 was
`5f5751372c10af22bd3a0ca4be4e0f2523a778a654b95330337e7f6a796b87b5`;
focused log SHA-256 was
`c754a0b6385aac7aaaf50499c2545655c71ba23f044800113f62a9bc2700d912`.
The first complete r8 clean-builder matrix passed vet, all tests, full race,
build, 12 deterministic scenarios, performance simulation, all repository
benchmarks, a v0.10.2 same-builder comparison, and the pre-version production
image contract. Its full log SHA-256 was
`a981aaab56746fd6a0ee0ef2a5ad56c8e23392bfe82987d163d62c7d950bfbfd`;
all four status files contained `0`.

Pass 3 nevertheless found an avoidable response-path efficiency regression in
r8. Every non-stream completion observer allocated the 32 KiB EOF lookahead,
even when no terminal callback needed it. In the same-builder comparison, the
2 KiB median changed from about `20.7 us/op`, `9360 B/op`, 32 allocations in
v0.10.2 to about `124.6 us/op`, `42248 B/op`, 34 allocations in r8. The 64 KiB
median changed from about `1.28 ms/op`, `375959 B/op`, 38 allocations to about
`2.76 ms/op`, `408846 B/op`, 40 allocations. Absolute time was small relative
to inference, but the extra allocation was unnecessary and r8 was superseded.

The corrected common non-stream path now uses the upstream HTTP
`Content-Length` when present. Exact length releases before the last body bytes
are returned to the downstream without allocating lookahead. Unknown length
retains the bounded 32 KiB lookahead. Short EOF, overrun, `UnexpectedEOF`, and
HTTP truncated non-stream responses do not release early or train. Legacy
completion observers without a terminal callback no longer allocate lookahead.
This is a model-neutral HTTP protocol optimization and adds no tokenizer,
model, cache, vLLM, or Router dependency.

The current pre-version focused green is r10. Its exact uncommitted tracked
source archive is
`tmp/pig-v0103-slow-downstream-focused-r10.tar.gz`, SHA-256
`568fe44df34b5f106dd7f6b6e254013abab533676383d312569124bb5840f031`.
The isolated builder used `golang:1.24-bookworm`, reported
`go1.24.13 linux/amd64`, found zero unformatted Go files, and exited `0` for
focused unit/integration tests, targeted races, and benchmarks. The saved log
is
`tmp/pig-v010-use1-cb-live-20260802/v0103-slow-downstream-focused-r10/focused.log`,
SHA-256
`3dc04ead1c6237c01d570da8c439b16d8a0044874611bd1ce41af04e949e5537`;
the status SHA-256 is
`9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa`.

The exact r10 complete matrix also passed. Evidence is under
`tmp/pig-v010-use1-cb-live-20260802/v0103-full-r10/`; the downloaded log archive
SHA-256 is
`ed2644ac033afd07029ead05e032a35482e920f0e0e5aa14165a79024080d458`.
The input manifest binds candidate r10, v0.10.2 baseline, and the comparison
harness and has SHA-256
`ffe15bd8214690b9ad27e465a822e110aa99cb599eef5188dd9d342683f8522e`.
Full-matrix, comparison, image-contract, and overall statuses are all `0`;
their material log SHA-256 values are respectively
`bbff228e070eded8bdcd050715b60b5ce091886bc33fccd4fb6c7d4c13385e8d`,
`2f6a87ef9904678659f08ee31c9781d3618c6eb8d3740439779782ceed4b1a88`,
and
`6cfe7eb64109b029355a6c2fa62ed7b6995859d9c83d1a3e4343b2393729efe9`.
All 12 deterministic scenarios retained zero candidate hard violations; short
burst and mixed short/long fit remained 60.00% and 33.33% above the comparison
control. Performance characterization was estimator 64 KiB p95 `6.846 us`,
2 MiB p99 `348.778 us`, and shadow decision p99 `5.355 us`, all far below the
plan thresholds but not production latency evidence.

The known-length focused path removed about 32 KiB/request versus unknown-length
lookahead: 2 KiB was `9424 B/op` versus about `42288 B/op`, and 64 KiB was
`376023 B/op` versus about `408886 B/op`. A reverse-order comparison was added
because builder CPU timing was noisy. Candidate-first versus baseline-second
medians were about `37.8 vs 30.9 us` for the 3.36 KiB streaming observer,
`72.2 vs 31.7 us` for 2 KiB non-stream, and `1.65 vs 1.23 ms` for 64 KiB
non-stream; allocation counts were equal and candidate legacy-observer bytes
increased only 40 B. Those are post-upstream response-parsing costs, not the
pre-forward predictor and not evidence of serving-throughput improvement. The
residual absolute cost is accepted for live measurement rather than adding a
more complex parser before a real signal exists. Reverse comparison log hashes
are
`92e06f1c8d49eaef43c0fb908c3b9af6169eccb0eb83214ddd1f1cc3caabedb2`
and
`832c4d79fc3a4b1d87fe6b4bff6bb9ed04deba8932cff62af2a44c69463b1ece`.

Pass 3 is complete for the pre-version r10 executable source: no remaining
source, safety, lifecycle, simulation, allocation-bound, or image-structure
blocker is known. Version identity and documentation are now being changed to
v0.10.3; the exact versioned archive must repeat focused and complete matrices
with `EXPECTED_VERSION=v0.10.3` before commit, push, tag, image publication, or
deployment. `use1-cb` remains disabled, v0.10.2 remains the disabled-route
rollback baseline only, and no new Compose deployment or 30-minute canary has
occurred.

### v0.10.3 versioned release evidence — completed source gate 2026-08-02

The exact versioned r11 tracked-source archive is
`tmp/pig-v0103-versioned-r11.tar.gz`, SHA-256
`e691ca51e2d845b9766a04f45268d9df2f2ed4d1216cbaa00e3ca925f0b8a445`.
It contains runtime identity `PIG-v0.10.3`, Docker OCI label `0.10.3`, and the
v0.10.3 README/Advanced/Observability contract. The remote focused matrix used
the same archive, exited `0`, and found zero unformatted Go files. Focused log
SHA-256 is
`a925b32e328896cce0c77a5a7aa7648d5800139092e6cb12b624bb5505dfbcef`.

The final versioned complete clean-builder matrix also used that exact archive.
The input manifest SHA-256 is
`0056bc4840dcf31fa015e5dca05bc0ba6a673361c9a34498fb775147c3136472`;
full-matrix, comparison, image-contract, and overall status files all contain
`0`. Full log, candidate benchmark, v0.10.3 comparison, and image-contract log
SHA-256 values are respectively
`a52895d224bb4118ff96f7a1fbbdce998ae835869ce651775df5bfa4d65326b1`,
`f96b12754cd58af96683710cdb37a7bb7c5fe94d00951308641806a877339609`,
`de73df5cec57b20f7e9cd5613a5c0ec57d88b5cd27bbb7aeeff00aecca96af98`,
and
`9209462c0c59870f9ed9835bfe237d717daf109b1044cf0da60ff68cd0396443`.
The downloaded combined evidence archive is
`tmp/pig-v010-use1-cb-live-20260802/v0103-r11-evidence.tar.gz`, SHA-256
`a8080f7a724c7cec8e2dd5a27040e2fff84b9ed000706767f4e678231baaaf10`;
its internal `SHA256SUMS` was rechecked locally.

All 12 deterministic scenarios again have zero candidate hard violations and
the same 60.00% short-burst and 33.33% mixed-workload fit improvement over the
comparison control. Final performance characterization was estimator 64 KiB
p95 `3.241 us`, estimator 2 MiB p99 `185.226 us`, and shadow decision p99
`13.345 us`. The versioned builder-local production image contract explicitly
used `EXPECTED_VERSION=v0.10.3`, observed Docker label `0.10.3`, and reported
`PIG_PRODUCTION_IMAGE_CONTRACT_OK`. The builder-local tag was deleted after the
gate and is not a registry image.

This evidence section itself is the only post-r11 archive edit. It changes no
Go source, Dockerfile, workflow, tool, configuration, or runtime documentation
contract and is excluded from the executable/image-input identity comparison.
The source gate is complete and permits commit, branch push, and v0.10.3 tag
push. It does not yet prove a published registry image, registry attestation,
Compose integration, Router-disabled deployment, live readiness, Router enable,
or a complete 30-minute real-traffic interval. Those gates remain mandatory in
that order, and `use1-cb` remains disabled until they pass.


### v0.10.3 registry and Router-disabled shadow evidence — completed 2026-08-02

The release workflow run
`https://github.com/Phala-Network/phala-inference-guard/actions/runs/30730840750`
completed successfully for commit
`584d36bfd1052b2a99fd5629175cb5b2ac70eb3c` and annotated tag
`v0.10.3`. The resulting immutable registry image is
`ghcr.io/phala-network/phala-inference-guard@sha256:0b36cffff01a600cb843806fb273474c22a584c2809b539155b8f040b8893594`;
its image ID is
`sha256:fd99d00d7c44aca01e65b69a762072e134734ce6dca2192200dbe2ad66b3e50e`
and its OCI version label is `0.10.3`.

The remote builder pulled that exact digest and repeated the production image
contract plus off/shadow/enforce, authentication, protocol, pre-forward reject,
low-flow recovery, streaming terminal, input-size learning, and terminal-state
gates. It reported `REGISTRY_IMAGE_GATE_OK`. The complete remote evidence
archive SHA-256 is
`05ca5f598bbee2e96809a8a62d81c7bbcd9c22422f24cebf5e388ccfb694c071`.
The local secret-scanned slim archive is
`tmp/pig-v010-use1-cb-live-20260802/v0103-registry-r1-slim.tar.gz`,
SHA-256
`77131a3cd5208d9f9927299f9f3f4954b151e0b3d4542139dd130c4cf2562d10`.
This closed the registry-image gate but did not authorize Router traffic by
itself.

A fresh live preflight at `2026-08-02T03:53:51Z` and a second immediate
pre-mutation drift check at `03:56:56Z` both found CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` running with
`in_progress=false`, byte-exact Compose SHA-256
`add08f14c6dc726eba8dbcd72c265e4119b7a5b1229f98e44252f3e929352069`,
Router digest
`sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`,
enabled set exactly `use1-4c,use1-9b`, and `use1-cb` disabled with route
running `0`. Protected models, metrics, and attestation were HTTP 200; the
backend and predictive resource state were idle; preemptions were zero.

The shadow candidate was generated from that byte-exact Compose and changed
only:

1. the PIG image from the v0.10.2 digest to the immutable v0.10.3 digest; and
2. `PREDICTIVE_ADMISSION_MODE=enforce` to `shadow`.

Its SHA-256 is
`150d536e469612a2b12b80949ec99540ff4aef0dd73c465e833f5b52a6b86798`.
The compose-only update supplied no `.env`. The local outer command reached
its 240-second wrapper limit, but the single original deploy process continued
and completed at 254 seconds with CLI exit `0`; no second deploy was issued.
A fresh platform query proved `running`, `in_progress=false`, and the live
Docker Compose hash exactly equal to the candidate. PIG ran the expected
registry image ID and vLLM retained its exact configured digest.

The CVM reboot made vLLM reload the model. Direct `/v1/models` remained 503
while loading and became 200 at `2026-08-02T04:08:30Z`. vLLM reported
`Application startup complete`, 89.1 GiB available KV cache, and a physical
KV capacity of 862,437 tokens. The first PIG startup probe reached its existing
300-second timeout shortly before vLLM became ready and the restart policy made
one new startup attempt; the second attempt started `PIG-v0.10.3` in shadow
mode and then remained stable. This was a single cold-start sequencing event
while the route was disabled, not a runtime restart loop. It is retained as an
operational observation and must be rechecked after the enforce restart; the
authorized two-field candidate was not broadened to alter timeouts.

The ready shadow baseline was exact: runtime identity `PIG-v0.10.3`, mode
`shadow`, intake open, and all attempts, learner samples, reservations,
shadow observations, deferred outcomes, lifecycle failures, backend
running/waiting/KV, and preemptions initially zero. Authenticated models,
`/pig/metrics`, `/v1/metrics`, and NVIDIA attestation were HTTP 200;
unauthenticated metrics were HTTP 401. Router remained unchanged and
`use1-cb` remained disabled throughout.

The first low-flow runner completed 23 successful requests and proved the real
cold progression: three cold input-size estimates followed by 20 learned
estimates, 22 accepted size samples, the intended one bounded low-ratio reject,
23 resource releases, 23 deferred terminations, zero active state, zero drop,
zero censor, zero resource-release failure, and zero preemption. Its only
failure was a harness-only expectation that every successful HTTP response must
increment deferred `qualified`. Source inspection proved that metric counts
only qualified scheduler TPS/TTFT outcomes; input-size-only learning is tracked
separately. The corrected gate therefore keeps exact release/termination
accounting and independently requires input-size maturity.

The corrected 20-request repeat passed. All requests were HTTP 200; intake
remained open; no risk, unknown, enforce reject, invalidation, preemption,
reservation, shadow observation, or deferred leak occurred. Prediction latency
was 20/20 at or below 0.25 ms; estimator latency was 19/20 at or below 0.25 ms
and 20/20 at or below 1 ms. It reported `false_lock=false` and
`sticky_zero=false`.

The first streaming TPS runner supplied exactly three cold qualified outcomes:
scheduler accepted, local TPS, and deferred qualified each advanced from zero
to three, while every terminal state returned to zero. Its old order-dependent
harness expected the third request itself to have used three previous samples.
The corrected order-independent gate permits up to four cold requests. The next
request was HTTP 200 with explicit terminal usage and `[DONE]`, entered with
`source=calibrated`, `samples=3`, and a fit decision, then advanced all
three qualified counters to four. No preemption, lifecycle failure, or active
state remained.

Normal chat, streaming with usage, tool call, strict structured output, and CJK
protocol gates all passed HTTP 200 without retaining response bodies. A
truncated JSON request returned HTTP 400 without creating predictive state. A
bounded one-second streaming client cancellation terminated without premature
resource release or learning, converged all state to zero, and was followed
immediately by a safe HTTP 200 request. The final shadow totals were 54 fit
predictions, zero risk/unknown/enforced reject, 53 vLLM successes, 53
released/terminated deferred lifecycles, five scheduler-qualified deferred
outcomes, and zero active reservation/deferred/shadow state, failures, drops,
censors, backend running/waiting/KV, or preemptions.

Pass 1 rechecked image/runtime/Compose/Router identity and the resource versus
learning lifecycle: no identity drift, early-release leak, double release,
false lock, or unauthorized Router write was found. Pass 2 rechecked learner
causality and efficiency: cold input-size and TPS learning matured, the next
prediction used learned/calibrated state, low-ratio and cancelled outcomes did
not grant unsafe headroom, and live prediction/estimator histograms remained
within the plan limits. Pass 3 rechecked operations, protocol, and safety:
current-boot PIG, vLLM, and serial fatal/OOM/Xid/engine-death scans were zero;
readiness, attestation, authentication, protocol, idle convergence, and secret
scans passed. The two harness corrections changed only live evidence logic and
did not modify the released product.

The Router-disabled shadow gate is complete. It authorizes only a fresh
predeploy drift/idle audit followed by switching the same immutable image from
`shadow` to `enforce`. That candidate must differ from the live Compose by
exactly that one mode field. Because the restart makes learner state cold,
enforce must repeat cold progress, low-flow recovery, calibrated TPS, protocol,
client-cancel recovery, prediction latency, zero-terminal-state, no-preemption,
attestation, and log gates before `use1-cb` can be enabled.


### v0.10.3 Router-disabled enforce evidence — completed 2026-08-02

A fresh promote audit at `2026-08-02T04:26:34Z` found the exact shadow Compose
and immutable image, platform `running/in_progress=false`, Router digest and
enabled set unchanged, `use1-cb` disabled and idle, and all active predictive
and backend resource state at zero. The enforce candidate was generated from
that fresh live Compose and changed exactly one field:
`PREDICTIVE_ADMISSION_MODE=shadow` to `enforce`. The candidate SHA-256 is
`2f81a07a71df7ac3a0291c0b9948b41bae0f9960489aeef4b4d3266ce6f2bf35`.
Reverse replacement reproduced the byte-exact live shadow Compose.

The compose-only update again supplied no `.env`, completed in 254.2 seconds,
and exited `0`. The live Docker Compose hash then exactly matched the enforce
candidate; PIG image digest and ID were unchanged. The route remained disabled.
The reboot again made vLLM reload the model. Models became HTTP 200 at
`2026-08-02T04:38:01Z`. As in shadow, the first PIG process reached the
existing 300-second startup probe timeout just before vLLM readiness; the
restart-policy attempt started at `04:37:51Z`, observed a green backend, and
remained stable. No later restart, runtime crash, or readiness loss occurred.

The enforce ready baseline was genuinely cold and uncontaminated: mode enforce,
intake open, and attempts, decisions, enforced rejects, input-size and scheduler
samples, reservations, shadow observations, deferred outcomes, failures, vLLM
request/token counters, backend running/waiting/KV, and preemptions all zero.
Protected readiness, PIG metrics, combined metrics, and NVIDIA attestation were
HTTP 200; unauthenticated metrics were HTTP 401. Router remained unchanged.

The cold-first causality gate passed:

- a 124-byte safe request was cold-fit, returned HTTP 200, and increased vLLM
  success and prompt tokens exactly once;
- a bounded 1,600,124-byte request returned HTTP 429 with
  `reason=kv_over_budget`, increased risk and enforced reject exactly once,
  and did not change vLLM success or prompt tokens, proving pre-forward reject;
- the immediately following safe request returned HTTP 200; and
- the two completed requests produced exactly two releases and two deferred
  terminations, with zero active state, failure, drop, censor, or preemption.

The enforce low-flow gate then passed 23/23 HTTP 200 requests. Input-size
prediction matured from cold to learned; final learned estimates were 20 and 22
samples were stored. The intentional risk and enforced-reject counters remained
unchanged at one. The 23 real completions produced exactly 23 additional
releases and terminations. Prediction latency was 23/23 at or below 0.25 ms;
estimator latency was 22/23 at or below 0.25 ms and 23/23 at or below 1 ms.
There was no false lock, sticky zero, failure, invalidation, or preemption.

The enforce TPS learner started with zero scheduler samples. Four bounded
streaming-with-usage requests were sufficient: the first three supplied samples
one through three; the fourth request was predicted before forwarding with
`source=calibrated`, `samples=3`, and fit. Local TPS, scheduler accepted, and
deferred qualified each increased exactly four, while all active state returned
to zero. Normal chat, streaming usage, tool call, strict structured output, and
CJK all passed HTTP 200 without retaining response bodies.

The first reused adverse harness expected a truncated JSON request to return the
shadow-mode backend HTTP 400. Enforce correctly returned HTTP 429 instead,
because an untrusted request size is not guessed. The harness was parameterized
by mode and rerun: enforce malformed input returned 429 without predictive
resource state, a one-second streaming client cancellation converged to zero
without premature release or learning, and the next safe request returned HTTP
200. This was a harness-only mode expectation, not a released-product change.

The final enforce snapshot at `2026-08-02T04:46:11Z` had 37 numeric attempts:
36 fit and the one intentional KV risk. The three enforced rejects were the KV
risk and two malformed-input harness invocations. vLLM had 35 successes; the
one other fit was the intentionally cancelled stream. Deferred release and
termination were both 35, scheduler-qualified deferred outcomes were five, and
reservations, shadow observations, active deferred outcomes, drops, censors,
all lifecycle failure phases, backend running/waiting/KV, vLLM error
completions, and preemptions were zero. Compose, image, runtime, attestation,
authentication, and Router-disabled identity remained exact.

Pass 1 rechecked enforce causality and safety: the intentional risk was rejected
before vLLM, cold and post-risk safe requests entered, cancelled work did not
learn, and no false/self/sticky lock or resource leak occurred. Pass 2 rechecked
learning and efficiency: input-size and TPS predictors matured from cold, the
next TPS prediction used calibrated state, and live latency histograms stayed
within limits without new reject pressure. Pass 3 rechecked protocol and
operations: all supported protocol gates passed; current-boot PIG, vLLM, and
serial fatal/OOM/Xid/engine-death scans were zero; no preemption or unexpected
configuration drift occurred. The startup-timeout observation is unchanged and
bounded to disabled-route cold boot.

The Router-disabled enforce gate is complete. It authorizes only a fresh
Router/CVM/Compose/metrics/attestation preflight followed by changing exactly
`use1-cb.enabled=false` to `true`. Weight, policy, bearer configuration,
timeouts, every other upstream, Router source, PIG Compose, and vLLM remain
immutable. The 30-minute timer must not start until Router confirms enabled and
healthy and a real PIG attempt or vLLM inference counter advances from the
pre-enable baseline. Any obvious finding requires immediate single-field
disable, drain, evidence preservation, and the full repair/revalidation loop.


### v0.10.3 real-traffic canary — stopped on revised requirements 2026-08-02

A fresh preflight at `2026-08-02T04:55:00Z` re-proved the exact enforce
Compose SHA-256
`2f81a07a71df7ac3a0291c0b9948b41bae0f9960489aeef4b4d3266ce6f2bf35`,
the immutable v0.10.3 image and ID, platform `running/in_progress=false`,
Router digest
`sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`,
enabled set exactly `use1-4c,use1-9b`, disabled and drained `use1-cb`, all
authenticated readiness/metrics/attestation gates, and zero active predictive,
backend, failure, or preemption state.

At `04:55:49Z`, the Router PATCH changed only `use1-cb.enabled=false` to
`true`. Full normalized before/after comparison passed. The Router config
digest became
`sha256:7869dbc9822ec36b0d661bfa9eedcfa6799d9b00d54a97d40c9ebe1db53b5202`.
The real-traffic timer began only at `04:56:10Z`, after `pig_ok=true`,
`stale=false`, protected readiness and NVIDIA attestation were healthy, Router
processed advanced, and PIG/vLLM inference counters advanced from the baseline.

The canary was intentionally stopped before 30 minutes after the product
requirements changed: TTFT must no longer reject requests, and protection must
be visible to the existing Router capacity contract. The last complete observer
sample was 882.5 seconds after the timer start; the single-field disable was
issued at `05:11:08Z`. The post-disable audit at `05:12:52Z` proved the original
Router digest and enabled set restored, `use1-cb` disabled with route running
zero, unchanged Compose/image/runtime identity, all protected endpoints ready,
and reservation, deferred, backend running/waiting/KV, failures, drops, vLLM
errors, and preemptions all zero.

This partial interval is diagnostic evidence, not a passed 30-minute gate. From
the pre-enable baseline to the drained post-disable snapshot, Router processed
advanced by 892, PIG made 888 new predictions, 226 were fit and 662 were risk,
and enforced rejects increased by 665. vLLM success and predictive
release/termination each increased by 218; scheduler-qualified outcomes
increased by 115, censored outcomes by 25, and drop/failure/preemption remained
zero. The one-off excess enforced rejects over risk were non-risk harness
history already present in the process counters; no unknown prediction occurred
during this canary.

Safety and lifecycle behavior were sound under the observed traffic: the route
remained healthy, no counter reset or identity drift occurred, no false/sticky
lock or v0.10.2-style reservation leak recurred, accepted traffic completed, and
preemption remained zero. The canary nevertheless found an obvious throughput
and observability defect. Repeated samples had one backend decode, waiting zero,
KV commonly below 10%, and observed single-user TPS around 194-209, while tens
of subsequent requests were rejected with `ttft_at_risk`. At approximately 7.4
minutes, the incremental vLLM histogram had 98 TTFT observations, average TTFT
about 1.35 seconds, p95 in the `<=2.5s` bucket, and a severe p99 long tail; TPOT
averaged about 7.46 ms/token (about 134 TPS), with p95 in the `<=25ms/token`
bucket. This shows both real TTFT long-tail pressure and substantial TPS
headroom, but the revised contract explicitly makes TTFT observational rather
than an admission constraint.

The second defect is a contract disconnect. PIG exposed increasing
`pig_predictive_admission_decisions_total{decision="risk"}` and
`pig_predictive_admission_enforced_rejects_total`, but the Router reads only
`pig_dynamic_observed_running`, `pig_dynamic_observed_waiting`,
`pig_dynamic_global_limit`, `pig_tier_basic_limit`, and tier inflight. During
predictive protection those Router-consumed values continued to advertise
`global_limit=50`, large basic capacity, and no waiting, so Router correctly
continued selecting the node and caused avoidable PIG 429 responses. Periodic
status logs also omitted predictive decision/protection state. Predictive-only
counters were therefore insufficient even though their numeric values were
correct.


### v0.10.4 revised contract and repair plan — active

v0.10.4 supersedes the v0.10.3 canary candidate. The implementation remains
model-agnostic and tokenizer-approximate; it does not add cache awareness,
model-family assets, Router source changes, or routing logic to PIG.

The admission contract is now:

1. TTFT measurement and learned TTFT estimates remain available for diagnosis
   and offline comparison, but TTFT is not a pre-forward reject condition.
   The predictive admission `Constraints` type has no TTFT SLO and the decision
   reason set has no `ttft_at_risk`, so the decision path cannot return it.
   Deterministic/live gates must prove requests differing only by TTFT forecast
   receive the same admission result.
   `DYNAMIC_TTFT_ENABLED` defaults to `false`, and predictive `shadow/enforce`
   configuration must reject an attempt to enable the legacy dynamic TTFT
   limiter. The canary must expose `pig_dynamic_ttft_enabled 0`.
   Goodput simulation continues to count and report TTFT violations, but those
   observations are excluded from protected-QoS safety, false-accept, and
   completion-token-goodput gates. TPS, TPOT, KV, workspace, preemption, and
   lifecycle safety remain gating dimensions.
2. TPS remains first priority. Existing-user TPS, new-user TPS, and TPOT
   protection remain predictive and pre-forward. KV capacity, workspace, and
   preemption risks also remain pre-forward protections.
3. A request-specific failure must not globally suppress the node. Unknown or
   malformed input, duplicate IDs, and a standalone request whose own KV size
   exceeds the hard budget remain local rejects and do not create Router
   backpressure.
4. A load-dependent protection reason may create bounded Router backpressure:
   `existing_tps_at_risk`, `tpot_at_risk`, workspace/preemption risk, and
   `new_tps_at_risk` only when existing virtual load is present. The KV reasons
   `kv_over_budget` and `active_kv_over_budget` are load-dependent only when the
   rejected request's own validated KV cost fits the corresponding empty-node
   hard budget; this preserves predictive KV capacity protection without
   globally suppressing the node for a standalone oversized request. The hold
   is derived from the metrics poll interval and is a fixed, bounded episode.
   Protection signals inside the episode update latest diagnostic state and an
   extension counter but never move expiry. The first rejected request after
   expiry is a bounded probe and may start a new fixed episode; continuous
   traffic therefore cannot create a sliding-TTL lock.
5. Router backpressure is applied only while real load exists. Effective load
   is the maximum of the dynamic controller's observed running and the
   predictive manager's virtual upper decode sequences. The latter is the same
   reconciled backend-plus-unabsorbed-reservation state family used by the
   rejected decision; it is not the count of reservations alone and does not
   retain the rejected request as synthetic load.
   While a bounded protection is active and effective load is positive, the
   exported Router-consumed `pig_dynamic_global_limit` is clamped to that load,
   making fullness at least 100%. When load reaches zero or the hold expires,
   the unclamped limit is exported immediately. This is the low-flow/self-lock
   escape hatch. Router defines a non-positive limit as zero fullness, so the
   effective Router limit uses the positive effective-running count as a 100%
   fullness sentinel when the raw dynamic limit is zero. The separately named
   raw limit and PIG-local `pig_dynamic_admission_limit` remain zero; the
   sentinel therefore blocks Router selection without reopening local admission.
   For the authorized canary, Router `metrics_poll_ms` must be re-verified as
   `1000` before enable, so the minimum two-second hold spans at least one full
   Router scrape opportunity even when activation begins immediately after a
   scrape. A different Router polling interval requires an explicit compatibility
   gate; PIG does not silently assume or modify Router configuration.
6. Metrics must expose both raw and effective values. At minimum they include
   predictive backpressure active/applied, reason/source, expiry, activation
   and extension counts, raw dynamic running/limit, effective Router running
   and limit, plus existing decision/reason counters. Existing Router fields
   carry the effective values; explicitly named raw metrics preserve diagnostic
   truth.
7. Logs must make protection visible without prompt, body, bearer, API token,
   or user content. A bounded structured activation record is emitted on the
   decision path and includes mode, reason, source, samples, virtual active
   load, activation/expiry, and hold duration. It intentionally does not claim
   the final Router limit because the dynamic backend snapshot is owned by the
   metrics/status boundary, not the admission adapter. The periodic status
   line records the actual raw/effective running and limit projection together
   with predictive attempts/fit/risk/unknown/reject, last reason/source,
   reservations/deferred state, and Router backpressure active/applied state.
   Because a fixed protection episode can be shorter than the periodic status
   interval, the first metrics projection that actually applies an episode also
   emits one `router_capacity_applied` record with its activation number and
   raw/effective capacity. Concurrent or repeated scrapes cannot emit that
   record more than once per activation. Repeated identical rejects do not
   produce unbounded per-request log spam.

The v0.10.4 red tests must fail on v0.10.3 and cover:

- an adverse TTFT forecast no longer rejects while the same TPS/KV forecast
  still does;
- a load-dependent TPS/TPOT protection activation immediately changes both
  explicit predictive metrics and the existing Router-consumed effective
  capacity fields;
- protection activation is represented in a structured log and periodic status
  line, and the first applied metrics projection emits one capacity-applied
  record, all without request content;
- repeated rejects inside an episode update diagnostics without extending the
  fixed expiry or causing an activation/log storm;
- a single oversized or malformed request at idle does not create global
  backpressure;
- a request that fits the empty-node KV budget but crosses it only because of
  existing load does create bounded Router backpressure;
- idle residual/prefix-cache KV with zero active sequences does not create
  global backpressure;
- load returning to zero removes the effective clamp even before hold expiry;
- hold expiry removes the clamp while load remains, permitting a bounded probe
  and relearning rather than a sticky lock;
- a raw zero global limit plus active predictive protection still produces
  Router-visible 100% fullness while raw and local admission limits remain zero;
- concurrent decisions, metrics reads, status logging, resource release,
  cancellation, and close are race-safe; and
- off/shadow modes never alter Router-consumed capacity.

After focused tests, the complete builder-only gate remains mandatory: format,
unit/integration, `go vet`, race, deterministic simulation, benchmark and
comparison, image contract, immutable registry verification, disabled shadow,
disabled enforce, protocol/attestation/lifecycle/low-flow gates, and then a new
full 30-minute real-traffic canary whose timer starts only on proved inference.
The partial v0.10.3 interval cannot be combined with the future v0.10.4 interval.
Any obvious issue repeats the same disable/drain/fix/full-revalidation loop.

#### v0.10.4 review and repair evidence — WIP, not a release

The first full r3 builder matrix reached the deterministic goodput gate and
failed there rather than being accepted as partial green evidence. The initial
aggregate was current threshold `39840`, v0.9 KV-only `37536`, and predictive
`42528`, with zero protected safety failures. Payload-free admission tracing
identified a false deny in `low_kv_excessive_ttft`: a ground-safe request was
rejected as `existing_tps_at_risk` because mature minority-shape residual
evidence had been erased by a dominant high-frequency shape. The simulator had
also trained only fitted requests even though the production shadow contract
forwards predicted-risk requests and lets a bounded, non-interfered terminal
outcome train only a later prediction. Finally, the default `0.50` learned
latency floor imposed an unrelated approximate four-decode TPOT ceiling.

The WIP repairs therefore:

- separate bounded global fallback retention from the per-cell sample cap,
  preserve a minority cell's minimum mature evidence before trimming a
  dominant cell, and hard-bound the global store at `1024` samples;
- skip the global fallback scan when the local cell is already mature for the
  protected TPS and TPOT dimensions, so observation-only TTFT cannot trigger a
  scan by itself; keep indexed global cell counts for bounded eviction, and use
  one compatible-sample grouping pass instead of repeated per-dimension and
  per-decode-level scans;
- model a bounded shadow prefix in deterministic simulation without adding
  shadow-only risk requests to reservation or KV accounting, censor outcomes
  whose prediction did not include later work, and train only qualified future
  predictions;
- lower the learned latency minimum multiplier to `0.10` while retaining the
  minimum-sample, upper-quantile, maximum-multiplier, identity, freshness, and
  censoring gates; and
- keep TTFT violations in diagnostic output while excluding them from
  protected safety, false accept/deny, and completion-token-goodput decisions.

Focused builder diagnostics through `diag4` passed the default config test,
dominant/minority fallback regression, the complete predictive runtime/config/
goodput packages, and the goodput acceptance gate. The exact `diag4` result was
current threshold `39840`, v0.9 KV-only `37536`, predictive `44064`, with zero
protected safety violations, zero false accepts, zero reservation leaks,
thirteen false denies, and four TTFT-only diagnostics. In
`low_kv_excessive_ttft`, all four requests were admitted as `fit`, were ground
safe for TPS/TPOT/KV, and remained ground-unsafe only for observational TTFT.
That evidence predates the final `1024` hard bound, indexed eviction, one-pass
fallback selection, and the Router visibility correction below, so it cannot
serve as release evidence for the current source.

The first Router-backpressure WIP used
`max(dynamic observed running, live reservation count)` as the effective load.
A correctness review found that this could still reproduce the reported
failure: the predictive coordinator can atomically reject with
`ExistingDecodeSequences > 0` while the separately polled dynamic snapshot is
still zero and the existing reservation has already been absorbed. The
activation log and `active=1` metric would then exist, but `applied=0` would
leave Router-consumed capacity unchanged. The repaired projection uses
`max(dynamic observed running, predictive manager virtual upper decode
sequences)` only during an active enforce episode. That virtual value is the
same model-agnostic state family used by the reject, combines the predictive
observer's reconciled backend state with unabsorbed reservations, and returns
to zero on reconciliation rather than latching the rejected request. A new
integration regression requires a reject with dynamic running `0`, reservation
count `0`, and predictive virtual decode `1` to publish in one scrape:

```text
pig_predictive_router_backpressure_active 1
pig_predictive_router_backpressure_applied 1
pig_predictive_router_backpressure_predictive_running 1
pig_dynamic_observed_running_raw 0
pig_dynamic_observed_running 1
pig_dynamic_global_limit_raw 50
pig_dynamic_global_limit 1
pig_dynamic_admission_limit 50
```

Expired/inactive episodes normalize current reason/source/sample state to
`none/unknown/0`, while keeping bounded cumulative counters and last-episode
timestamps. This prevents stale protection labels from being read as a current
block. The next valid load-dependent rejected probe may start a new fixed
episode; expiry is never extended in place.

A second observability review found that the immediate activation log did not
contain the actual Router capacity projection, while the periodic status
interval can be longer than a two-to-five-second protection episode. The
metrics boundary now claims at most one payload-free
`router_capacity_applied` log event per activation using an atomic activation
watermark. It records the same raw/effective running and limit values written
to that metrics response without extending the episode or logging every
scrape. This is evidence that PIG exported the blocking contract; the canary
must separately prove that Router scraped it and reported at least 100%
fullness before treating the control loop as closed.

The efficiency review also removed TTFT-only global fallback scans. Local TTFT
learning remains intact, and a compatible TTFT fallback is still collected
opportunistically whenever immature TPS or TPOT requires the bounded global
scan. Once both protected dimensions are locally mature, observational TTFT
cannot keep the prediction path scanning up to `1024` global samples. A focused
regression distinguishes the two cases.

The first two attempts to run the final matrix were invalid harness evidence,
not product failures or green results. r4 called `find` as though `gofmt` were a
path, and r5 used a login shell that removed the Go image's
`/usr/local/go/bin` from `PATH`; neither reached candidate Go tests. The first
r6 harness fixed the shell and exit-code handling, but review before execution
found that its static TTFT gate would reject the diagnostic simulator's legal
`TTFTSLO` field. The source then changed for capacity-applied logging and
TTFT-only scan removal, so r6 was not executed as candidate evidence. r7 limits
the `TTFTSLO` absence check to real admission/config/server/runtime packages and
keeps diagnostic simulation outside that symbol gate.

The exact r7 candidate archive SHA-256 is
`b53fe6305f5083ad27b12b2630e8f7dc209cb93281205b89266f2bdd46a0678e`.
The v0.10.3 baseline archive remains
`1dfdb640b424535adc768d6d83e3c0eb2e644ac0a6f44f0c2b9c1b359fb78985`.
The r7 runner SHA-256 is
`355c60867187da819c529db8302dfb48158911addd42f61b1bcbe596d20c90aa`,
and the uploaded bundle SHA-256 is
`08dc2d830032394a2ce5af422b0433ed2e21f434e33e9117cf51d63c0876dc86`.
The downloaded evidence archive is
`tmp/pig-v0104-use1-cb-20260802/builder-r7/pig-v0104-r7-evidence.tar.gz`,
SHA-256
`da688ea9f4c46240221b28c79e26c32470baca93487c9fd97e3a4cc60ba82de0`.
All 43 files covered by the inner `SHA256SUMS` were reverified after download;
all 21 status files are zero, and `overall.status=0`.

The three v0.10.3 red proofs each exited exactly `1` for the intended reason:

- adverse TTFT returned `ttft_at_risk` instead of `fit`;
- predictive mode accepted the legacy dynamic TTFT limiter; and
- Router capacity projection source and effective metrics did not exist.

The r7 candidate then passed focused TTFT-observation, Router activation,
dynamic-poll-lag, capacity-applied log, fixed-expiry, idle/self-lock escape,
oversized-request isolation, inactive-state, raw/effective metrics, bounded
learning, minority-retention, one-pass fallback, TTFT-only scan avoidance, and
goodput gates. It also passed targeted race, `go vet ./...`, `go test ./...`,
`go build ./...`, full `go test -race ./...`, deterministic KV simulation,
predictive goodput simulation, candidate and v0.10.3 baseline benchmarks, the
dedicated default/hard-bound fallback benchmark, production image build,
production image contract, image inspect, and gate-container cleanup using
Go `1.24.13` on Linux amd64.

The independently recomputed goodput aggregate is:

| policy | completion-token goodput | TPS violations | TPOT violations | KV hard | preemption proxy | false accepts | false denies | leaks | TTFT diagnostics |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| current threshold | 39840 | 0 | 0 | 0 | 0 | 0 | 37 | 0 | 2 |
| v0.9 KV-only | 37536 | 32 | 32 | 1 | 1 | 16 | 3 | 0 | 4 |
| exact-token KV-only | 37024 | 37 | 32 | 0 | 0 | 15 | 3 | 0 | 5 |
| model-agnostic predictive | 44064 | 0 | 0 | 0 | 0 | 0 | 13 | 0 | 4 |

TTFT remains diagnostic in that table and is excluded from protected safety,
false-accept/deny, and completion-token-goodput decisions. The result proves
the deterministic acceptance contract, not production throughput.

The deterministic performance probe reported approximate estimator 64 KiB p95
`4.737us`, approximate estimator 2 MiB p99 `232.968us`, and overall shadow
decision p99 `11.142us`. The mature local learned-scheduler benchmark was
`3.246us..4.062us/op`, `256 B/op`, and `2 allocs/op`. The dedicated bounded
global fallback was `42.716us..62.820us/op` at the default bound and
`207.955us..332.101us/op` at the absolute 1024-sample bound. The latter is an
immature/fallback worst-case diagnostic; once local TPS and TPOT are mature,
the global scan is skipped. These CPU measurements do not claim GPU or live
request latency improvement.

The builder-local production image passed the contract as version `0.10.4`,
entrypoint `/phala-inference-guard`, with image ID
`sha256:cd6d6b3fc9c48b8c78097329a33ed93261b436472d804d834d6e483a9530b593`.
This completes the exact executable clean-builder and builder-local image
layers. The evidence section itself is a later non-executable documentation
update; before commit, Dockerfile, module files, `cmd/`, `internal/`, and the
image-contract script must be byte-compared with the tested archive.

v0.10.4 is now eligible for that byte comparison, final diff review, commit,
push, annotated tag, release workflow, and immutable registry verification. It
is still not a published image, deployed runtime, live-ready canary, or
production result. `use1-cb` remains disabled until the registry and
Router-disabled shadow/enforce gates pass.
