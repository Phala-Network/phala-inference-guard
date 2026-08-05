# PIG v0.10 model-agnostic approximate predictive admission

Status: active plan. The v0.9.4 Gemma4-exact deployment path is superseded and must not be deployed.

Last updated: 2026-08-03 (Asia/Shanghai).

## 1. Authoritative objective

PIG predicts before a request enters the upstream. Feedback only improves later
predictions. The primary optimization objective is total useful throughput with
a bounded QoS loss budget, not zero TPS variation. The red per-user TPS floor,
reciprocal hard TPOT bound, KV capacity, workspace safety, and preemption remain
hard protection constraints. Yellow TPS and reciprocal yellow TPOT are soft
learning/exploration targets: attributed misses are allowed so the learner can
discover more throughput, while a mature higher-concurrency bucket that provides
no aggregate-throughput gain must reduce similar admissions. TTFT is measurement,
learning, diagnostics, and offline-comparison data only: it never rejects,
activates Router backpressure, or changes Router capacity.

Input size is deliberately approximate and model-agnostic:

- no exact model tokenizer or chat-template reproduction;
- no Gemma, Qwen, Llama, GLM, or other family-specific runtime code;
- no upstream tokenizer RPC on the request path;
- estimate cheaply, then calibrate with qualified `usage.prompt_tokens`;
- accept imperfect cold-start estimates and become more accurate with samples;
- keep prediction, learning, and reservation state bounded and race-safe.

The tokenizer is an auxiliary sensor, not an admission controller or a source
of capacity truth. Its only purpose is to provide a cheap approximate
`input-size hint`. PIG combines that hint with the existing bounded byte/JSON
interval, request class and decode horizon, observed backend state, unabsorbed
reservations, and learned TPS/TPOT behavior. No tokenizer result may by itself
admit, reject, publish Router backpressure, reduce effective node capacity, or
lock a node. A missing, unsupported, saturated, or failed tokenizer hint falls
back to the existing bounded estimator; it does not introduce a new failure
mode. Qualified backend `usage.prompt_tokens` calibrates only future hints and
never retroactively changes the producing request.

The combined forecast may still reject a request when the conservative
post-admit KV or QoS counterfactual violates a current hard constraint. That is
a resource/QoS prediction using one approximate demand feature, not a
tokenizer-only verdict.

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

## 3. Reuse the existing fast estimator and add only an auxiliary hint

`internal/domain/kvadmission.EstimateJSON` already performs a model-agnostic,
bounded JSON scan. It estimates an interval from string bytes, tool/function and
response-schema bytes, message/tool counts, multimodal markers, and the requested
output limit or bounded fallback.

The predictive path must consume this cost directly instead of cloning the body,
rendering a model-specific prompt, and calling the native exact tokenizer. The
classifier already buffers the bounded body and extracts output-token fields; do
not add another body copy. Fuse the current field and feature scans only if remote
builder benchmarks prove the second linear scan material.

`EstimateJSON` remains the always-available fallback and conservative interval.
v0.10.8 adds one lightweight approximate lexical-token hint from the same
already-buffered body. It samples at most 256 string bytes per request and 64
bytes per string across at most four positions, classifies only ASCII word,
whitespace, punctuation, and UTF-8 leading-byte density, scales by string
length, and adds bounded message overhead. It deliberately does not fully walk
Unicode or decode JSON escapes. Once the sampling budget is exhausted, later
strings use a constant-time four-byte fallback. It emits a count only: no token
IDs, vocabulary, template, model identity, asset, native runtime, allocation,
or network dependency. Overflow, missing hint evidence, or insufficient
learning make only the hint unavailable and retain the existing byte/JSON
fallback. The size calibrator learns the relationship between this hint and
qualified actual prompt tokens while retaining the cold interval as the safe
fallback. Do not replace the backend-reported KV capacity, current KV usage,
reservation accounting, TPS/TPOT learner, or manager constraints with the hint.

The vLLM Router tokenizer was inspected read-only at upstream commit
`d60711dc72ab8f073e33f9a3d93ee81b97274c26` (2026-07-24). Its useful design
ideas are a narrow shared interface and bounded timing/error metrics, but its
HuggingFace/Tiktoken factories, model/file selection, Hub download, vocabularies,
chat templates, token IDs, decode, and stop-sequence machinery are deliberately
not imported. Those features are exact/model-bound serving functions rather
than the cheap model-neutral reference required by PIG.

## 4. Real admission transaction

```text
bounded request classification
  -> model-agnostic byte/JSON interval plus optional approximate-token hint
  -> conservative learned size multiplier
  -> backend observation plus every unabsorbed reservation
  -> post-admit KV/TPS/TPOT forecast plus observational TTFT estimate
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
17. The tokenizer hint is optional evidence. Holding the request body, output
    horizon, backend observation, reservations, and learned state fixed, hint
    changes may refine the forecast; however, absence or failure of only the
    hint must produce the estimator fallback rather than `unknown`, a tokenizer
    error reject, or node-wide backpressure.
18. A hint cannot bypass an ordinary KV, red-TPS, TPOT, workspace, preemption,
    freshness, or lifecycle constraint. Conversely, a hint alone cannot create
    a separate hard constraint. The manager evaluates only the combined
    post-admit forecast and retains one atomic reservation transaction.
19. QoS feedback is graded. A hard-band TPS/TPOT violation, waiting, or
    preemption may cause an immediate bounded retreat. A yellow-only miss above
    the red floor is a qualified learning sample, not an immediate hard adverse
    override: a bounded number of similar requests may still run, then the
    normal minimum sample count and conservative quantile progressively lower
    that pressure cell's forecast. Time, sparse traffic, or one yellow miss may
    not publish node fullness or create a persistent lock.

Use one generic calibrator per PIG/backend epoch and request class for the first
implementation. This supports any model deployed behind a PIG without source or
image changes. Future multi-model isolation, if required, must use a bounded
generic identity key and global fallback, never family-specific code.

## 6. Qualified feedback

Extend response usage parsing to include `prompt_tokens`. A size sample is valid
only when the request was forwarded, exactly one terminal usage object exists,
`prompt_tokens > 0`, estimator/backend identities match, the raw estimate was
retained in bounded reservation state, and the request completed successfully.

TPS/TPOT learning and observational TTFT learning keep the same
next-request-only rule. Prefer backend mean ITL/generation duration and use local
semantic timing only as a qualified fallback. Missing usage is telemetry, not
fabricated evidence. No TTFT estimate may enter the admission constraints or
Router-capacity projection.

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

## 7. Throughput-first graded-QoS prediction

For every candidate:

1. project observed KV + unabsorbed reservations + candidate upper;
2. predict existing users' post-admit TPS;
3. predict the candidate user's TPS;
4. predict TPOT upper bounds and an observational TTFT estimate;
5. risk/reject on KV, workspace, preemption cooldown, red TPS, or reciprocal
   red TPOT violation, never TTFT; yellow-only forecast/outcome bands guide
   bounded exploration and later learning instead of becoming immediate hard
   rejects;
6. otherwise reserve future demand atomically.

The primary objective is useful completion-token throughput under hard resource
and red-band QoS constraints, not raw admitted requests and not zero yellow-band
variation. A request that completes above the red floor but below yellow can
still contribute useful throughput; the miss is counted and used to make later
shape-compatible predictions less optimistic. Cold QoS uses explicit
conservative defaults and exposes `source=cold`; learned values become active
only after the minimum qualified sample count. The coordinator continues
admitting cold-safe work until an explicit, current KV, workspace, red TPS/red
TPOT, preemption, freshness, or lifecycle constraint binds. A learned state,
yellow-only miss, or TTFT observation is not by itself a reason to stop intake.

## 8. Configuration simplification

Remove predictive runtime requirements for served model/revision, tokenizer and
template paths/hashes, model-family renderer version, native tokenizer ABI, and
exact token-ID oracles.

Keep only mode, backend observer, metric freshness/timeouts, preemption cooldown,
protected KV budget, decode bounds, red/yellow TPS/TPOT targets, observational
TTFT settings, estimator settings, and calibrator age/sample/memory bounds.
Observe vLLM KV capacity/block size from metrics instead of duplicating an exact
model profile. Capacity/epoch drift must still reset reservations and learning
safely.

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
- with all other inputs fixed, a useful approximate-token hint changes only the
  combined input-size feature/forecast; disabling, failing, or corrupting only
  that hint returns to the byte/JSON fallback without a reject, sticky zero, or
  Router backpressure;
- tokenizer hint underestimation is corrected only for later requests by
  qualified `usage.prompt_tokens`; the producing request's immutable estimate
  and reservation never change.

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

- KV-hard, workspace, reservation, lifecycle, and unexplained or repeated hard
  red-TPS/red-TPOT violations: zero; one previously unknown hard knee may expose
  exactly one qualified frontier probe, after which the next compatible
  prediction must retreat and no higher bucket may run;
- TTFT has no violation threshold because it is observation-only; changing only
  TTFT must never change admission, exploration, Router protection, or effective
  capacity;
- yellow-only TPS/TPOT misses may be non-zero, but must be counted with fixed
  labels and must mature through the normal bounded sample/quantile path without
  immediate hard retreat or node-wide protection;
- false accepts, false locks, self-locks, sticky-zero episodes, reservation
  leaks, and double releases: zero;
- every safe low-flow, recovery, and post-drain workload keeps making progress
  until a named current constraint binds;
- mature approximate predictor aggregate hard-band-compliant completion-token
  goodput is not below the current threshold baseline and improves at least one
  mixed pressure workload;
- compare completed tokens and requests that satisfy red TPS/red TPOT, KV,
  workspace, and preemption constraints. Yellow-only misses are reported
  separately and remain eligible useful throughput; rejected, failed, or
  repeated hard-band-violating work does not count as throughput improvement;
- any percentage improvement is reported from raw simulation output and never
  presented as live GPU throughput.

## 12. Three review passes

1. Model and causality: no family dependency; feedback changes only later
   predictions; throughput-first graded-QoS logic is prospective, not
   retrospective throttling.
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

#### v0.10.4 publication and immutable registry evidence — complete

The exact executable candidate was byte-compared with the r7 tested archive for
all 240 applicable files under `.dockerignore`, `Dockerfile`, `go.mod`,
`go.sum`, `cmd/`, `internal/`, and
`tools/validate-production-image-contract.sh`; there were zero missing files,
zero extra files, and zero content-hash mismatches. The candidate was committed
and pushed on `codex/pig-v0.10.0-model-agnostic` as
`a1b1608cea1d0c08380925985535380b4fe1d0cf` (`release: close predictive
protection loop in v0.10.4`). Annotated tag `v0.10.4`, tag object
`0cdf2dfc98f264fa46046225ede63b65455f72da`, points to that commit and was
pushed. Publish Image workflow run `30743812789` completed successfully for
head branch `v0.10.4` and the same head SHA.

The published deployment input is the immutable registry reference:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:d72a3b315a0650a315c4d104d8b033e53232e4d23f6dabc5e427cdccc47b2258
```

The builder inspected that exact digest as Linux amd64, OCI version `0.10.4`,
entrypoint `/phala-inference-guard`, and registry image ID
`sha256:1f38f5092ffc56c20c117f83fae08417836398fc0e4fc5f60444f17278f99f2a`.
From the r7 candidate source, the production image contract was rerun with that
digest as `PIG_IMAGE_UNDER_TEST` and returned:

```text
PIG_PRODUCTION_IMAGE_CONTRACT_OK image=ghcr.io/phala-network/phala-inference-guard@sha256:d72a3b315a0650a315c4d104d8b033e53232e4d23f6dabc5e427cdccc47b2258 version=0.10.4
```

The registry evidence archive is
`tmp/pig-v0104-use1-cb-20260802/registry-r1/pig-v0104-registry-r1-evidence.tar.gz`,
SHA-256
`34c018cdade57151ec5479dea574d9a9d76b623217361e513706121ead2e08cc`.
The downloaded archive matched its separately downloaded outer hash; all four
files covered by its inner `SHA256SUMS` matched, and `contract.status=0`.

This completes source, clean-builder executable, published registry image, and
immutable registry-contract layers. It does not prove deployment or live
readiness. The next gate is a fresh live snapshot followed by Router-disabled
shadow and enforce validation on only
`a0f0bfb3-e46f-4b22-814e-24872f251193`. `use1-cb` must remain disabled until
those gates prove the Router-consumed metrics and log control loop, protocol,
attestation, lifecycle, latency, low-flow recovery, and no-preemption
requirements.

#### v0.10.4 fresh live preflight — passed for Router-disabled shadow deploy

Fresh evidence was captured at `2026-08-02T10:56:08.7287099Z` under
`tmp/pig-v010-use1-cb-live-20260802/v0104-preflight-20260802T105608Z`.
The authorized CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` was `running` with
`in_progress=false`. Its live Compose SHA-256 was
`2f81a07a71df7ac3a0291c0b9948b41bae0f9960489aeef4b4d3266ce6f2bf35`;
PIG was still the v0.10.3 immutable image and both PIG and vLLM containers were
running. Authenticated `/v1/models`, `/pig/metrics`, `/v1/metrics`, and
attestation returned HTTP 200; both metrics endpoints returned 401 without
authentication; the attestation NVIDIA payload was non-empty. PIG predictive
failures, active reservations, shadow observations, deferred outcomes, vLLM
running/waiting, KV use, error completions, and preemptions were all zero.

Router config digest was
`sha256:1b62b992f37b1f3c3ddc3894373cf2a10368d64350b689052c642c2712967c3f`.
The exact enabled set was `use1-4c,use1-9b`; `use1-cb` was disabled with Router
running zero. Because disabled upstreams are not polled, its Router status
correctly reported `pig_ok=false`, `stale=true`, and `error=not_collected` even
though the target metrics endpoints independently returned 200.

Fresh state also corrected an earlier path assumption: this Router currently
uses `metrics_poll_ms=1000`, `metrics_timeout_ms=800`,
`metrics_stale_ms=3000`, and `metrics_path=/v1/metrics`. That authenticated
endpoint currently exposes all five Router-consumed PIG field families:
`pig_dynamic_observed_running`, `pig_dynamic_observed_waiting`,
`pig_dynamic_global_limit`, `pig_tier_basic_limit`, and `pig_tier_inflight`.
The authorization permits only changing `use1-cb.enabled`, so this rollout must
not rewrite the Router path. Disabled shadow/enforce validation will inspect the
actual `/v1/metrics` response, and the enabled canary must separately prove the
Router polls it and reports the expected at-least-100% fullness during a
protection episode.

Candidates were generated from the exact fresh live Compose. The shadow
candidate SHA-256 is
`6dba91f82c0b50caa3be1c72577f19098ae690da6977133e13065c773932045f`
and differs only in the PIG image and `PREDICTIVE_ADMISSION_MODE=enforce` to
`shadow`. The enforce candidate SHA-256 is
`d014719f6d3926ad08c5ac76f1462ed72e4a1e130a0397b9f9c4e3b889568e29`
and differs only in the PIG image. Both use the immutable v0.10.4 digest recorded
above. The exact fresh live Compose is the rollback input; no `.env` file will
be supplied to the centralized-KMS update.

#### v0.10.4 Router-disabled shadow evidence — passed

The shadow candidate was deployed only to the authorized CVM from
`2026-08-02T11:03:31.3776017Z` to `11:07:50.5860702Z`; `phala deploy --wait`
returned zero for the exact candidate SHA-256 and no env file was supplied.
The platform then reported `running`, `in_progress=false`, and the same Docker
Compose hash. Runtime inspection proved the published v0.10.4 digest and
registry image ID rather than only the submitted Compose.

vLLM needed approximately five minutes to profile, capture CUDA graphs, and
become ready. It reported an 862,437-token GPU KV cache. The first PIG process
reached its configured 300-second startup-probe timeout immediately before
vLLM became available and restarted once; the replacement obtained coherent
metrics and remained stable. This was a disabled-rollout readiness observation,
not a serving outage or an accepted readiness signal. Readiness was declared
only after authenticated `/v1/models`, `/pig/metrics`, `/v1/metrics`, and
attestation all returned 200, the NVIDIA attestation payload was non-empty,
metrics returned 401 without authentication, and both PIG and vLLM uptime kept
increasing.

The v0.10.4 startup log proved `predictive_admission=shadow`,
`dynamic_ttft_protect=false`, `predictive_ttft_observe=true`, and
`predictive_ttft_protect=false`. Fifty-six captured periodic status records
included predictive attempts/fit/risk/unknown/reject, reservation, virtual
decode, deferred, Router backpressure, and raw/effective capacity state; no
fatal or panic record was found. Idle status was green with
`router_bp=0/0/none`, `effective=0/50`, and `raw=0/50`.

Both metrics endpoints exposed the new Router-backpressure state and the raw,
effective, and PIG-local limits. At idle they reported active/applied zero,
raw/effective running zero, raw/effective global limit 50, local admission
limit 50, no preemption, and 862,437 available KV tokens. Every Router-consumed
field in `/pig/metrics` was byte-equal to the same field in the Router's actual
`/v1/metrics` scrape path.

Five protocol probes covering normal, streaming usage, a required tool call,
strict structured output, and CJK all returned 200 and retained no response
bodies. Six identical short non-streaming probes demonstrated qualification
discipline: their 2-3-token outputs could train request-size approximation but
not reliable TPS. Five identical non-streaming 41-token probes advanced the
model-agnostic input-size estimator to `source=learned`. Five identical
streaming 41-token probes then supplied local token timing; qualified scheduler
samples advanced from one to six, and the next prediction used
`source=calibrated` with five samples. After all probes, reservations and
deferred outcomes were zero and Router backpressure remained unapplied in
shadow.

The first 100-request low-flow matrix completed all request, learning,
lifecycle, terminal-zero, false-lock, and no-preemption checks but intentionally
failed its final latency assertion: prediction was 99/100 at or below 0.25 ms
and 100/100 at or below 1 ms; estimator was 97/100 and 98/100 respectively,
with two samples in the 1-2 ms bucket and a 104.64 us mean. That result remains
red evidence and was not relabeled as a pass. The matrix mixed input-size
learning, a 4 KiB low-ratio sample, and ordinary sparse traffic.

An independent interval-delta r2 then sent 100 identical tiny requests. All
returned 200. Prediction was 100/100 at or below 0.25 ms and 1 ms with a
31.03 us mean; estimator was 100/100 at or below both thresholds with a
57.93 us mean. Terminal reservations, deferred outcomes, backpressure,
running, and waiting were all zero; Router field parity held and preemption
delta was zero. Across the two contiguous 100-request intervals, the combined
estimator distribution was 197/200 at or below 0.25 ms and 198/200 at or below
1 ms, satisfying the declared p95 and p99 contract while retaining the rare
1-2 ms tail as a live-canary observation target.

This completes the disabled shadow gate. It does not prove enforce behavior or
the Router control loop. Before enforce deployment, the live shadow Compose,
Router digest and enabled set, endpoints, attestation, reservations, backend
running/waiting, and preemptions must be captured again and remain unchanged
and drained.

#### v0.10.4 Router-disabled enforce and protection-visibility evidence — passed

The enforce candidate was deployed only to the authorized CVM from
`2026-08-02T11:48:56.7011803Z` to `11:53:04.7549241Z`; the exact live Compose
SHA-256 is
`d014719f6d3926ad08c5ac76f1462ed72e4a1e130a0397b9f9c4e3b889568e29`.
Runtime inspection proved the immutable v0.10.4 image digest and registry image
ID, `PREDICTIVE_ADMISSION_MODE=enforce`, protected endpoint readiness,
attestation, and zero initial predictive/backend state. The same startup-probe
boundary restart seen in shadow occurred while vLLM completed its approximately
five-minute startup; the replacement PIG became stable and readiness was not
declared from the deploy exit or container state alone.

The cold-first gate passed three ordered cases. A small request returned 200
and advanced PIG fit plus vLLM success. A 1,600,124-byte standalone request was
rejected pre-forward with HTTP 429 and `kv_over_budget`; vLLM success and prompt
tokens did not advance, preemption stayed zero, and all reservations/deferred
state returned to zero. An immediate small recovery request returned 200.
Before, during, and after that standalone oversized reject, predictive Router
backpressure active/applied/activation counters remained zero and the existing
Router fields remained raw/effective `0/50`. This proves a request-specific
oversized failure does not suppress the whole node or leave a low-flow lock.

The load-dependent protection gate then held one long streaming decode open
and sent a second small request. The second request returned HTTP 429 in
`802.931 ms` with `existing_tps_at_risk` from the static predictor. Across the
reject, all three independent pre-forward counters were byte-for-byte stable:
PIG backend accepted `8 -> 8`, vLLM success `4 -> 4`, and vLLM prompt tokens
`127 -> 127`. In the same activation, both the Router's actual configured
`/v1/metrics` path and `/pig/metrics` reported:

```text
pig_predictive_router_backpressure_active 1
pig_predictive_router_backpressure_applied 1
pig_dynamic_observed_running_raw 1
pig_dynamic_observed_running 1
pig_dynamic_global_limit_raw 50
pig_dynamic_global_limit 1
pig_dynamic_admission_limit 50
```

Every Router-consumed field compared by the gate was equal across the two
endpoints and effective fullness was exactly `1/1 = 100%`. Logs contained
exactly one matching payload-free `event=activated` record and exactly one
matching `event=router_capacity_applied activation=3` record. Cancellation
converged reservations, deferred outcomes, effective running, and waiting to
zero without preemption. The retained summary is
`tmp/pig-v010-use1-cb-live-20260802/v0104-enforce-ready-20260802T120153Z/router-backpressure-cold-20260802T122947Z/summary.json`,
SHA-256
`6a919634acb0c6b37248758d4dc60096afdaabf5b4f788f68bfac80d8cb47556`;
the matching runtime log SHA-256 is
`622d26c4121c0014235d3bc26b78f314ca15c30506133e2c9a6f39d82442b69a`.
No request/response body was retained and the evidence secret scan passed.

Two earlier harness results remain explicitly non-green. A sequential
`/v1/metrics` then `/pig/metrics` comparison crossed the fixed two-second
episode expiry and correctly failed as timing-inconclusive. A parallel retry
captured both endpoints in the same episode with correct active/applied and
raw/effective values, but its curl write-out label parser rejected the already
valid files. The final runner removed that parser dependency, required both
HTTP transfers to succeed, asserted episode identity and full field parity, and
then produced the passing evidence above. Neither harness failure was relabeled
as a product failure or pass.

Enforce protocol and learning gates also passed. Normal completion, streaming
usage, required tool call, strict structured output, and CJK returned 200 with
no retained bodies. A subsequent 33-completion-token stream used a mature
`calibrated/fit` prediction, advanced both qualified scheduler learning and
local TPS outcome by one, and returned to terminal zero. A separate twenty-
request sparse/low-flow matrix returned 20/20 HTTP 200, advanced the
model-agnostic size estimator from cold to learned, safely rejected a low-ratio
training sample without invalidating mature state, and reported prediction and
estimator latency `20/20 <= 0.25 ms` and `20/20 <= 1 ms`. It ended with intake
open, no false/sticky lock, no active reservation/deferred state, no lifecycle
failure, and preemption delta zero.

This completes the Router-disabled shadow/enforce, protocol, learning,
low-flow, pre-forward, protection-log, and effective-metrics gates for the exact
v0.10.4 image. It still does not prove that Router completed an active scrape or
stopped selecting the route. Local China-to-use1 endpoint timing is too noisy
to substitute for Router timing: five `/pig/metrics` reads ranged from
approximately `1.33` to `5.01 s`, and five larger `/v1/metrics` reads from
approximately `7.09` to `11.14 s`. The next live gate must therefore prove from
Router state itself that `use1-cb` becomes `pig_ok=true`, `stale=false`, has a
fresh `age_ms`, and observes at least one active protection projection with
`observed_running/global_limit >= 1`; otherwise the target is immediately
disabled and the PIG metrics-delivery path must be repaired in a new version.

An unrelated live Router drift remains visible: the fresh pre-enforce digest
had enabled set `use1-4c,use1-9b`, while the post-enforce digest
`sha256:8969f268ba986f106f9085ffe64f48db9199c5527d0d4dd83c92b44b0a2499c1`
has only `use1-4c`; the sole upstream difference is
`use1-9b.enabled=true -> false`. This task did not perform that mutation and
must not restore it. Any authorized canary mutation is still limited to the
current `use1-cb.enabled` field and must preserve the then-current state of
every other upstream. At the end of this evidence, `use1-cb` remains disabled
and drained.

### v0.10.4 Router canary results and v0.10.5 TPS prefill-interference repair plan — active 2026-08-02

v0.10.4 proved the protection-delivery half of the reported Router contract,
but it failed the primary TPS objective and must not be re-enabled. The active
repair version is v0.10.5. This section supersedes any earlier statement that
v0.10.4 is eligible for a continued canary. It does not change the current
contract that TTFT is observation-only and cannot reject a request.

The first Router-enabled attempt ran from approximately `12:59:47Z` to
`13:07:24Z`. Product health, preemption, lifecycle, and Router PIG-scrape gates
remained green, but one local high-frequency observer GET timed out and the
original harness treated one network error as a product failure. The supervisor
safely disabled only `use1-cb`. The observer now retries ordinary GETs once and
requires three consecutive fast-observer failures before aborting. This partial
interval is harness-red evidence and cannot contribute to a future 30-minute
gate.

The second attempt enabled only `use1-cb` at `13:10:50Z`. Its first fresh Router
scrape reported `pig_ok=true`, `stale=false`, `age_ms=541`, and effective
`observed_running/global_limit=3/3`. Across the attempt, the Router fast observer
captured 19 healthy samples, ten samples at effective 100% fullness, zero PIG
unhealthy samples, and zero fast-observer errors. Predictive Router-backpressure
activations advanced `84 -> 129`; PIG attempts advanced `280 -> 420`, risk
decisions `116 -> 193`, enforced rejects `118 -> 196`, and vLLM successes
`157 -> 213`. This proves that the existing Router consumed the v0.10.4
effective-capacity projection and that protection was visible outside PIG.

The same attempt nevertheless had a real TPS-first failure. Around `13:14:03Z`
the backend had three running requests, including one prefill and two decode
requests. PIG activated protection around `13:14:06Z`, and Router samples then
observed effective `8/8`, but requests admitted between bounded two-second
episodes accumulated to six backend running requests and seven predictive
reservations. The observed per-user TPS then fell through approximately
`16.9`, `7.5`, `3.3`, `1.5`, `0.5`, `0.2`, `0.1`, and effectively zero before
generation recovered. At the retained slow sample at `13:14:27Z`, running and
decode were both six, waiting was zero, KV use was only `0.115764`, observed
per-user TPS was `0.394209`, and the dynamic raw limit had finally fallen
`50 -> 1`. The supervisor correctly stopped on
`single_user_tps_red_guard_active` and disabled only `use1-cb` at
`13:14:55Z`. Preemptions, predictive failures, deferred drops, resource-release
failures, and vLLM errors remained zero. This interval is a product-red result,
not a completed canary and not a throughput improvement claim.

The retained v0.10.4 r2 artifacts are under
`tmp/pig-v010-use1-cb-live-20260802/v0104-enforce-ready-20260802T120153Z/canary-r2-20260802T131050Z`.
The observer summary SHA-256 is
`1e9b8e94002685cac97e7115cebe2b1ea13798a3af4b187e6f51edafd7654096`,
the slow numeric sample log SHA-256 is
`8717fd6da0fb5896a2b7caa271d4485b9f376ba1185a88af164c99191b6b1038`,
and the fast Router sample log SHA-256 is
`cda6e74243cebf958a7ef2bdac5f45271816ffdcd86a1d0800f1608604a46036`.
The exact PIG status chronology was inspected live during the stop but was not
retained as a raw file before the platform log window expired; it must be
treated as a derived chronology, not as a hash-addressed raw log. All v0.10.5
canaries must capture bounded, payload-free PIG and vLLM log windows into the
retained evidence directory before evaluating the interval.

A fresh read-only post-failure audit at `13:25:27Z` is retained under
`tmp/pig-v010-use1-cb-live-20260802/v0105-fresh-disabled-audit-20260802T132527Z`.
It proved CVM `a0f0bfb3-e46f-4b22-814e-24872f251193` is running with
`in_progress=false`, the unchanged v0.10.4 enforce Compose SHA-256
`d014719f6d3926ad08c5ac76f1462ed72e4a1e130a0397b9f9c4e3b889568e29`,
the immutable v0.10.4 image and registry image ID, and healthy authenticated
models, both metrics endpoints, and attestation. Router digest is the restored
disabled baseline
`sha256:8969f268ba986f106f9085ffe64f48db9199c5527d0d4dd83c92b44b0a2499c1`,
the exact enabled set is only `use1-4c`, and `use1-cb` is disabled with route
running zero. PIG reservations, shadow observations, deferred outcomes,
backend running/waiting/KV, vLLM errors, predictive failures, release failures,
and preemptions are all zero. The unrelated externally disabled `use1-9b`
remains untouched.

#### v0.10.5 model and implementation contract

The failure is not repaired by lengthening the Router hold or by lowering a
feedback-only dynamic limit. Protection must remain fixed-episode and
low-flow-safe. The repair instead makes request size affect the pre-forward TPS
counterfactual and separates evidence that can prove new-user decode capacity
from evidence that can prove existing-user safety during prefill:

1. The production approximate scheduler must no longer configure a zero prefill
   TPS effect. The existing model-agnostic input estimate and vLLM-derived
   capacity/block information produce a bounded, conservative prefill pressure
   prior. It is arithmetic-only on the request hot path and introduces no model
   assets, exact tokenizer, cache lookup, Router change, or vLLM change.
2. Static prediction distinguishes two phases. New-user TPS predicts decode
   after the request's prefill, while existing-user TPS predicts the users
   already decoding while the candidate prefill consumes service capacity.
   Request size can therefore change existing-user admission while all current
   backend metrics are held constant.
3. Completion-token cadence from the joining request may calibrate post-prefill
   decode capacity and `NewUserTPSLower`. It must not directly certify
   `ExistingUserTPSLower`: that cadence starts after first semantic output and
   cannot observe an existing user's prefill-era stall.
4. Existing-user prefill evidence comes only from stable consecutive vLLM poll
   windows with at least one pre-existing decode, at least one live
   not-yet-semantic prefill reservation, an unchanged predictive event
   sequence and virtual state throughout the interval, coherent monotonically
   increasing generation counters, no epoch reset or preemption, and no
   ambiguous attribution. The observed generation delta divided by elapsed
   time and the number of existing decoders supplies the next-request-only
   existing-user target. A zero-generation interval is valid adverse evidence,
   not a malformed positive-ratio sample.
5. New-user and existing-user TPS residuals are stored, selected, bounded,
   expired, and reported separately. One qualified adverse existing-user
   sample may tighten the next prediction immediately; optimistic relaxation
   still requires the configured minimum qualified sample count and lower-tail
   quantile. Feedback never changes the request or reservation that produced
   it.
6. A coarse feature-cell match is not sufficient for optimistic reuse.
   Existing-user prefill evidence requires the retained sample to be at least
   as stressful as the query for decode concurrency and normalized uncached
   prefill, active context, physical KV, and active KV pressure. A small prompt
   or lower-concurrency prefill in the same coarse bucket cannot grant headroom
   to a larger or more concurrent prefill. Joining-user completion cadence is a
   different phase: after the configured minimum qualified samples it may
   calibrate a bounded aggregate decode-capacity lower bound and transfer that
   capacity across decode concurrency by conservation of total completion TPS.
   It still cannot certify the existing-user prefill bound, and all optimistic
   transfer remains subject to pressure compatibility, multiplier caps, age,
   attribution, and distribution-shift invalidation.
7. Cold behavior remains progressive rather than locked. With no existing
   decoder, existing-user TPS is explicitly not applicable. Qualified decode
   outcomes can establish bounded capacity headroom; the conservative
   request-size prior permits only bounded prefill exploration, and stable
   prefill windows decide whether later requests of compatible or lower
   pressure may advance. When load and reservations return to zero, neither an
   adverse sample nor Router backpressure may create a zero-capacity latch.
8. The v0.10.4 Router projection remains the delivery contract. Every
   load-dependent v0.10.5 TPS rejection must immediately update predictive
   counters/state, emit the bounded payload-free activation record, clamp the
   existing Router-consumed effective running/limit fields while real load
   exists, and emit exactly one capacity-applied record for the first applied
   projection. Raw capacity and PIG-local admission metrics remain separately
   truthful.

The implementation stays behind narrow interfaces: request-cost estimation
owns approximate size; the scheduler owns phase-specific prediction and bounded
residual selection; the vLLM observer owns coherent poll deltas; the manager
owns atomic state/decision/reservation and exposes only a bounded immutable
snapshot needed for observation qualification; the adapter owns HTTP lifecycle;
and observability only projects snapshots. No component may reach into another
component's mutable state.

#### v0.10.5 required red/green and release gates

Focused red tests must first fail on the exact v0.10.4 executable behavior and
cover all of the following:

- with current metrics and decoded-capacity evidence fixed, increasing only the
  candidate input estimate changes a pre-forward fit into
  `existing_tps_at_risk`;
- a joining request's high completion TPS can raise a later new-user decode
  bound but cannot alone raise the existing-user prefill bound;
- a stable existing-decode plus large-prefill interval with zero/low generation
  tightens the next compatible prediction before forwarding, including with
  low KV and waiting zero;
- one adverse qualified interval tightens immediately, while optimistic
  relaxation requires the minimum sample count;
- changing the event sequence, virtual state, epoch, counter monotonicity,
  preemption state, or attribution during a poll window censors the sample;
- a larger request cannot reuse smaller-request headroom merely because both
  fall in one coarse token bucket; a lower-pressure request may reuse mature
  higher-pressure evidence;
- a burst spanning multiple fixed Router episodes cannot accumulate beyond the
  TPS-safe pre-forward forecast, and the backend/red controller need not fail
  first;
- every resulting load-dependent rejection appears in the Router-consumed
  effective metrics and bounded structured logs; an idle standalone oversized
  reject still remains local;
- drain-to-zero immediately restores bounded forward progress and twenty sparse
  low-flow requests do not self-lock, false-lock, or leave a reservation,
  deferred outcome, pressure episode, or learned zero-capacity latch;
- TTFT-only forecast changes never alter admission, and the legacy dynamic TTFT
  limiter remains disabled and rejected in predictive modes; and
- manager decisions, snapshots, observer polls, learning, reconciliation,
  release, cancellation, and shutdown remain race-safe and bounded.

After focused green evidence, run only on a fresh remote builder: formatting,
focused packages, full Go tests, `go vet`, race tests, deterministic simulation,
goodput comparison, request-estimator and prediction hot-path benchmarks, and
the production image contract. Record the exact source archive/commit, builder
environment, commands, statuses, and evidence hashes. No executable Go/PIG test
is run on local Windows.

Only after the full builder matrix passes may v0.10.5 be committed, pushed,
annotated, published immutably, and verified by registry digest. Then repeat the
complete live flow on only the authorized CVM: fresh disabled/drained snapshot,
Router-disabled shadow deployment and protocol/learning/latency/low-flow gates,
Router-disabled enforce deployment and pre-forward/backpressure gates, fresh
Router state comparison, and changing only `use1-cb.enabled=false -> true`.
The canary timer starts only after Router scrape health and real inference are
proved and runs continuously for a new full 30 minutes. Any TPS failure,
preemption, lifecycle leak, metrics/log projection gap, false lock, observer
failure threshold, unexpected Router mutation, or material goodput regression
causes immediate single-field disable, drain, retained evidence, a new version,
and repetition of the complete process. No partial interval is combined.

#### v0.10.5 plan review pass 1 — model and causality

The first review rejected the tempting interpretation that Router ignored PIG:
the fast Router samples prove otherwise. It also rejected the existing learned
TPS contract because a completion-only target was applied to both new and
existing users while production prefill penalty was zero. The plan now requires
phase-specific targets, a nonzero size-dependent prior, stable prefill-window
evidence, and a constant-current-metrics causality test.

#### v0.10.5 plan review pass 2 — safety, efficiency, and SOLID

The second review found that raw request shapes inside one coarse cell were not
rechecked before optimistic local reuse and that a zero-generation stall could
not pass the old positive-ratio target validation. The plan now requires
dominance compatibility for local and global evidence, admits zero only as
qualified adverse existing-user evidence, separates fast downward learning from
slow upward relaxation, hard-bounds all stores, and keeps poll qualification,
scheduling, atomic reservation, and observability in separate consumer-owned
interfaces. The hot path remains one bounded estimate, arithmetic forecast,
bounded indexed lookup, and atomic reservation.

#### v0.10.5 plan review pass 3 — evidence and rollout

The third review kept v0.10.4 log/metrics delivery as a mandatory regression
instead of rewriting Router fields, recorded the exact r2 artifact hashes and
the missing raw-log limitation, and added pre-canary retained log capture. It
also re-proved the disabled/drained live baseline and preserved the external
`use1-9b` mutation. v0.10.5 remains a plan and source-repair target only at this
point: no v0.10.5 test, image, registry, Compose, deployment, or canary evidence
exists yet.

### v0.10.5 protection publication correction — active 2026-08-02

The user reported that a PIG protection decision was not visible in the logs or
metrics and Router therefore continued to send traffic. The earlier v0.10.4
Router canary proves one `existing_tps_at_risk` episode was consumed correctly,
but that narrow success is not a proof that every enforced protection path has
the same publication behavior. This correction strengthens the contract; it
does not erase the retained v0.10.4 evidence and does not authorize another
deployment before the complete v0.10.5 builder matrix passes.

The source audit found that the current Router-metrics integration test starts
from synthetic, already-active telemetry. It verifies metric rendering but does
not execute a real scheduler rejection through the approximate adapter before
scraping the Router-consumed fields. It can therefore stay green if a future
decision path rejects pre-forward without publishing protection. The ordinary
`LastReason` telemetry is also overwritten by a later fit, so a short risk can
be absent from a later diagnostic scrape even though aggregate risk/reject
counters advanced.

A deeper HTTP-path review then found a second publication gap: when request
classification could not produce a supported approximate cost, the proxy
returned an enforced predictive 429 before invoking the adapter. The HTTP
counter therefore advanced while the adapter had no opportunity to publish a
request-scoped reject, durable last-reject diagnostics, or a bounded log. The
proxy also inferred every decision from a nil reservation, which erased the
difference between a deliberate request reject, load protection, availability
protection, and an invalid adapter result.

The v0.10.5 source repair replaces that inference with a typed pre-forward
outcome (`forward`, `request_reject`, `load_protection`, or
`availability_protection`). Unsupported or unscannable individual requests now
enter the adapter and publish request-scoped evidence without suppressing an
idle node. CountCoordinator/Manager availability is separately typed from
invalid request input; current coordinator or upstream health drives the
availability sentinel and recovery. These source changes remain candidate-only
until the exact post-format archive passes the complete remote-builder matrix.

Internal failures are not silently folded into the same nil result. Scheduler
identity/prediction or health-probe panics are converted inside the predictive
boundary into node availability quarantine, so their 429, durable diagnostics,
availability activation, and Router sentinel remain one path. A proxy wrapper
panic, invalid typed result, or failed forward commit is conservatively scoped
to that request, emits a bounded payload-free failure log, and advances a fixed
`phase` failure counter; it does not globally suppress an idle node without
evidence that the node itself is unavailable.

The repaired publication contract is:

1. A real enforce decision has one typed outcome: fit, request-scoped reject,
   load-dependent protection reject, or globally unavailable/unknown. The HTTP
   decision, reason counters, durable last-risk diagnostics, protection state,
   and structured transition event are derived from that same outcome. Proxy
   code must not reconstruct the reason from a nil reservation.
2. Every load-dependent TPS, TPOT, KV, workspace, or preemption reject with
   existing predicted work immediately activates bounded Router backpressure.
   In the same coherent metrics snapshot, PIG exposes active/applied state and
   clamps the existing Router-consumed `pig_dynamic_observed_running` and
   `pig_dynamic_global_limit` to effective 100% fullness while preserving raw
   capacity and the PIG-local admission limit separately.
3. A request that is individually too large remains request-scoped: it advances
   the fixed-cardinality reason telemetry and a bounded payload-free rejection
   log but must not suppress an otherwise idle node. This distinction is
   explicit in both logs and metrics.
4. A fail-closed node-wide predictor/upstream-health failure must not return a
   stream of indistinguishable 429 responses while Router still sees ordinary
   spare capacity. It publishes an availability protection state that clears
   from current health, not from a sliding request TTL. Request cancellation or
   an unsupported individual request shape does not create node-wide pressure.
5. The cumulative risk/reject counters and a bounded, fixed-enum last-risk
   snapshot remain visible after the active episode expires; a later fit cannot
   erase the fact, reason, source, scope, or timestamp of the latest protection.
   No request ID, model name, prompt, response, body, bearer value, user, or
   token content is used as a metric label or log field.
6. `event=activated` is emitted once for each protection episode and
   `event=router_capacity_applied` once when a Router-consumed projection is
   actually produced. Request-scoped rejects use a separate bounded event and
   never claim that Router capacity was applied. Metrics rendering and logging
   must use the same immutable snapshot so their reason, scope, activation, and
   effective running/limit cannot disagree.
7. TTFT remains measurement-only. A TTFT observation, TTFT residual, or TTFT
   threshold cannot activate this protection state, reject a request, or alter
   Router capacity.

Required red/green coverage now includes a real approximate-adapter enforce
rejection followed immediately by both PIG-local and combined metrics scrapes;
all load-dependent reason variants; a later fit that does not erase last-risk
diagnostics; request-oversized non-suppression; fail-closed health publication
and recovery; no dynamic-snapshot lag gap; exactly-once transition/capacity
logs; concurrent decision/scrape race coverage; drain-to-zero recovery; and a
Router-parser fixture proving that the exact three fields Router consumes make
the protected route blocked relative to a healthy peer. A test that fabricates
an already-active telemetry snapshot is retained only as a writer unit test and
cannot satisfy this end-to-end gate.

#### protection publication review pass 1 — causality

The first review rejected the assumption that one successful v0.10.4 episode
proves all future paths. The mandatory test now begins with the real prediction
and verifies the pre-forward reject, protection transition, logs, and
Router-consumed values from that same decision.

#### protection publication review pass 2 — safety and efficiency

The second review separated node-wide load/availability protection from an
individually oversized or unsupported request. This prevents both failure
modes: Router continuing to feed a node that cannot accept any request, and an
idle node being globally locked by one unusual request. All retained
dimensions and labels are fixed and bounded; no body or identity data is kept.

#### protection publication review pass 3 — operations and evidence

The third review requires raw and effective metrics in the retained canary log
window, plus Router's fresh parsed view, instead of accepting only PIG-local
counters. The future 30-minute canary still restarts from zero after real
inference is proved. Any mismatch among the reject, activation log, PIG metrics,
Router parsed fullness, or route selection immediately disables only
`use1-cb`, drains it, and requires a new version and complete repeated flow.

#### v0.10.5 protection publication behavioral red — exact builder evidence

The publication gap is reproduced against the exact `v0.10.4` source baseline,
not inferred from source inspection. The baseline archive SHA-256 is
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`.
The injected regression test SHA-256 is
`2dc5f9897c326b01f162502a31bcfc258f42a199cfdda0051d594b4726def55d`.
It was compiled and executed on the remote builder under
`/tmp/pig-v0105-red-r4-a` with Go 1.24.5 on linux/amd64, using:

```text
/usr/local/go/bin/go test ./internal/app/server \
  -run 'TestV0104UnavailableRejectDoesNotReachRouterConsumedCapacity|TestV0104UnscannableHTTPRejectBypassesAdapterPublication' \
  -count=1
```

The test executable ran and exited 1 for the intended behavioral failures. In
the node-unavailable case, PIG returned a predictive unknown while publishing
`pig_predictive_router_backpressure_active 0`, effective
`pig_dynamic_observed_running 0`, and effective
`pig_dynamic_global_limit 50`; Router therefore still saw spare capacity. In
the unscannable HTTP case, the adapter snapshot remained `Attempts=0` and
`Unknown=0`, proving that the proxy rejected before adapter publication. The
retained `red.log` SHA-256 is
`17f14cc71e96857b67f6d7c2fb85adbc2430ac73df9375e477e5a13a6a118c3`;
the status artifact contains `1` and has SHA-256
`4355a46b19d348dc2f57c046f8ef63d4538ebb936000f3c9ee954a27460dd865`.
This is valid behavioral red evidence, not a missing dependency, invalid test
path, or compilation failure.

#### v0.10.5 protection publication focused green r7 — interim evidence

The current candidate was reconstructed from the same baseline archive plus
working-tree patch SHA-256
`ac8ea4cfffe2b906e73af699090aa4885930c684db269afd8ca0d3c1f03dc0bc`
under `/tmp/pig-v0105-green-r7-a/src` on builder CVM
`4f167f6e-4c50-415f-99f2-94b65652beba`. The environment was Go 1.24.5,
linux/amd64, `CGO_ENABLED=1`. The builder first required an empty `gofmt -d`
over `cmd` and `internal`, then ran:

```text
/usr/local/go/bin/go test \
  ./internal/runtime/predictive \
  ./internal/app/server \
  ./internal/observability/metrics \
  -count=1
```

All three packages passed. The retained evidence hashes are:

```text
environment.log  a79072a19a935018b519f71274715beaf0a20238501e4bc208839da23643350b
focused.log      203577d50461d189a49abeaec519236672b5b8c44626a90aa5dca7aa8963e0d1
focused.status   9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa
gofmt.diff       e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

The status is `0` and `gofmt.diff` is empty. This is deliberately classified as
interim focused green only. It does not satisfy the full clean-builder matrix,
release, image, registry, Compose, deployment, Router-enable, or 30-minute
canary gates. An earlier r6 attempt had a production-helper/test-fixture name
collision and is not green evidence; r7 is the first focused archive after that
compilation issue was corrected.

#### v0.10.5 focused green r12 — attribution and progressive-capacity correction

The next source review found that a forwarded prefill could still be absent
from vLLM `running` during a stable poll window. Subtracting the PIG pending
count from raw running in that state undercounted existing decoders and could
produce an optimistic existing-user TPS target. The corrected learner accepts
only one precisely attributable pending prefill whose immutable pre-forward
feature vector is still present in the manager snapshot and whose total decode
count has materialized in vLLM. Multiple, unmaterialized, changed, failed-fetch,
or otherwise ambiguous windows are censored and counted; no request identity or
payload is retained.

The same review initially applied decode-concurrency dominance to every TPS
residual. Exact r9 focused execution rejected that formulation because it also
disabled the mature joining-user completion-capacity path and regressed the
existing progressive-concurrency and HTTP goodput tests. The final split keeps
strict concurrency dominance for existing-user prefill evidence while treating
qualified joining-user completion cadence as a bounded aggregate decode
capacity. r8 and r11 stopped at formatting gates; r9 failed the intended
behavioral compatibility tests; none is green evidence. r10 passed before the
final fixed-state tokenizer-causality and twenty-sparse-request tests were
added. r12 is the first focused archive containing all of these corrections and
tests.

r12 was reconstructed from baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`
plus working patch SHA-256
`9a5fde4e8f70e41b39ec41ad8fe8a4c72658127198477eba02f2974dc274e823`
under `/tmp/pig-v0105-green-r12-a/src` on builder CVM
`4f167f6e-4c50-415f-99f2-94b65652beba`. The environment remained Go 1.24.5,
linux/amd64, `CGO_ENABLED=1`. An empty `gofmt -d` gate preceded:

```text
/usr/local/go/bin/go test \
  ./internal/runtime/predictive \
  ./internal/app/server \
  ./internal/observability/metrics \
  -count=1
```

All three packages passed. Retained evidence hashes are:

```text
environment.log  a79072a19a935018b519f71274715beaf0a20238501e4bc208839da23643350b
focused.log      f65e8a759bcd039a8e202a25e41c1513189526ff49d71833e40d28a99b17dd0d
focused.status   9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa
gofmt.diff       e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

The status is `0` and the formatting artifact is empty. This remains focused
green only; the exact post-document archive must still pass the full builder
matrix before any version, image, deployment, Router, or canary action.

#### v0.10.5 r13 pre-matrix publication and lifecycle correction — active 2026-08-02

A further static review of the candidate found two remaining consistency
hazards and one shutdown race before the full matrix. First, a later rejection
inside the same fixed Router episode replaced its reason/source/sample fields.
The original `event=activated` and a delayed first
`event=router_capacity_applied` scrape could therefore describe different
facts under one activation ID. Episode identity is now immutable; the bounded
durable `last_reject` snapshot remains the authoritative record of the newest
rejection. Second, the HTTP layer accepted some impossible typed combinations,
including a reject outcome carrying a reservation or an unknown outcome
carrying a reservation. Enforce mode now validates the complete outcome and
reservation product, fails closed on every invalid combination, terminates any
returned reservation exactly once, and records the fixed-cardinality decision
failure without inventing node-wide protection. Third, an adapter close racing
the coordinator result used to classify that result as request-scoped even
though the adapter was already unavailable. It now rolls back any newly
created reservation and publishes availability protection.

The publication order and counter meaning are now explicit. The approximate
adapter atomically records the decision, durable last-reject fields, and Router
protection snapshot before it returns the typed rejection to the proxy. Its
bounded activation log callback also completes before the proxy writes the
429. `pig_predictive_admission_enforced_rejects_total` is deliberately the
cumulative count of HTTP requests for which the proxy emitted an enforced
predictive rejection, so there can be a sub-request interval in which the
already-published protection state is visible while this response counter is
still unchanged. Router must not use that response counter. In that interval
the three Router-consumed capacity metrics must already report effective
fullness; after the response, the counter advances as well.

The added candidate tests cover the pre-response scrape ordering, immutable
activation identity, every invalid typed-result shape and reservation cleanup,
close-during-decision rollback and availability publication, a real
`LearnedScheduler + CountCoordinator + approximate HTTP` TPS rejection through
the exact Router parser, payload-free logging including model/user/request ID,
and repeated burst probes spanning four expired Router episodes without
gaining another reservation. They also recheck forward progress after drain.
These changes supersede r12 executable evidence. They are not green evidence
until a new exact r13 archive passes formatting and focused tests on the remote
builder; no version, image, deployment, Router, or canary action is authorized
by this section.

The active contract still excludes TTFT from protection. The thread Goal text
predates that correction and mentions TTFT protection, but current user
instructions and the active plan control: TTFT remains measurement, learning,
and diagnosis only and cannot reject, activate backpressure, or change Router
capacity.

#### v0.10.5 focused green r15 — publication closure evidence

r13 stopped at its formatting gate before any Go test because the new metric
field comment split a `gofmt` alignment group. A mistaken first invocation of
that script on the builder host also stopped at missing host `git`; it did not
execute Go and is not candidate evidence. r14 passed formatting and reached Go
compilation, then failed because the new HTTP integration test imported the
dynamic controller from `internal/runtime/dynamic` instead of its existing
`internal/app/dynamic` constructor package. The already-built predictive and
metrics packages passed, but the server package did not compile, so r14 is not
green. Both issues were corrected without changing the production algorithm.

r15 was reconstructed in the existing `pig-ubuntu-builder` container from
baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`
plus working patch SHA-256
`5c246fd891bb9f6bec610e4cdb64022288be8f064a50c109a458e0888f41e80f`.
The focused runner SHA-256 is
`1505a6d4641026d0f6c90c7958216060722add47ff92a754fc4b0f944640fa3e`.
The builder remained Go 1.24.5, linux/amd64, `CGO_ENABLED=1`. An empty `gofmt
-d` gate preceded:

```text
/usr/local/go/bin/go test \
  ./internal/runtime/predictive \
  ./internal/app/server \
  ./internal/observability/metrics \
  -count=1
```

All three packages passed. Retained evidence hashes are:

```text
environment.log  a79072a19a935018b519f71274715beaf0a20238501e4bc208839da23643350b
focused.log      2ead6be373bff92bb482fc1ec25773b318e632b3339b4bdb067808bf01ab5dac
focused.status   9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa
gofmt.diff       e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
results.tar      a35f2d1180fdb0577936a4a88cb5412b413992bae4c8f73d3cfd563b0e139ecf
```

The status is `0` and the formatting artifact is empty. This focused run
includes the same-decision pre-response protection scrape, real scheduler HTTP
and Router parser chain, immutable activation identity, invalid typed-result
cleanup, close-race availability publication, and multi-episode burst tests.
It remains focused green only. This evidence paragraph changes the full patch,
so the complete pre-release matrix must use a new exact archive; r15 cannot be
used as the final release archive. No image, deployment, Router enablement, or
canary action is authorized yet.

#### v0.10.5 full matrix r16 — product red and shadow-prefill evidence correction

The first complete pre-release attempt after r15 used baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
working patch SHA-256
`c48e072bdcb1618175e46dc2c505599f34f25996a5c6d0f30fb7cc8e59b7bbf1`,
and runner SHA-256
`e6b0508c93c3e5ab9023acd5086e88ab47ce299de4f3da0de736a5238b3173bf`.
It ran inside `pig-ubuntu-builder` on builder CVM
`4f167f6e-4c50-415f-99f2-94b65652beba`, using Go 1.24.5,
linux/amd64, and `CGO_ENABLED=1`. Formatting and `go vet ./...` passed, but
`go test ./... -count=1` failed, so no race, build, simulation, benchmark,
version, image, deployment, Router, or canary gate was reached.

The red result is a product/acceptance failure rather than an environment or
harness failure. The aggregate current-threshold policy delivered 39,840
SLO-compliant completion tokens with zero TPS, TPOT, KV-hard, or preemption
proxy violation. Predictive admission delivered 23,072 with the same protected
safety counts, zero false accepts, 41 false denies, and zero reservation leaks:
`-42.09%` versus current, while the acceptance gate requires at least `+5%`.
`TestPredictiveWarmupEnablesOnlyLearnedSafeConcurrency` also showed learned-safe
concurrency three still rejected as `existing_tps_at_risk`. A third failure
expected the obsolete cold reason `existing_tps_at_risk`; the current cold
reason is `new_tps_at_risk`, but only the reason assertion may be updated after
the underlying next-request-only causality is preserved. The goodput gate,
protected safety gates, phase separation, and dominance rules must not be
weakened to make this run green.

The exact recovered evidence is retained under
`tmp/pig-v0105-use1-cb-20260802/full-r16/recovered-b/evidence`. The remote-to-
local evidence archive SHA-256 is
`a4a9075e6d094ab99598f47ee2b92d77bcedb02d789cebb192c3fc318fe2a020`.
Material hashes are:

```text
environment.log   a79072a19a935018b519f71274715beaf0a20238501e4bc208839da23643350b
gofmt.diff        e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
vet.log           e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
full-test.log     bb615d4a2f626fe7259e624a656505cd3977d176acdbe4aadce319a8dd056e53
gofmt.status      9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa
vet.status        9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa
full-test.status  4355a46b19d348dc2f57c046f8ef63d4538ebb936000f3c9ee954a27460dd865
overall.status    4355a46b19d348dc2f57c046f8ef63d4538ebb936000f3c9ee954a27460dd865
```

The source review following r16 found two related simulation/production gaps.
First, the goodput simulator still constructs the old cold profile with
`BaseCompletionTPS = userTPSTarget` and zero size-dependent prefill penalty,
while the production factory uses a conservative doubled cold completion
capacity and a nonzero model-agnostic prefill penalty. The simulator must use
the same production policy inputs and retain separate existing-prefill versus
new-decode ground truth. Second, simulated warmup feeds only joining-user
completion outcomes, so it cannot legitimately certify existing-user prefill
safety.

More importantly, the same missing evidence path exists in production shadow
mode. Shadow risk requests live in the adapter's bounded observation store and
do not enter Manager resource reservations. The vLLM stable-prefill observer
currently sees only Manager `ForwardedPendingPrefillFeatures`, so a
Router-disabled shadow request can produce joining-user completion learning but
cannot supply the anonymous pending-prefill feature needed to qualify
existing-user evidence. Simulation must not fabricate evidence the real
runtime cannot observe.

The next red/green slice therefore requires a narrow shadow-prefill observation
bridge. It must expose at most the bounded, immutable, anonymous feature
snapshot required by the vLLM observer; it must not contain request ID, model,
user, prompt, payload, or token IDs; it must not enter Manager KV/workspace or
concurrency accounting; and it must disappear on semantic output, termination,
drop, close, or expiry. Exactly one stable, materialized shadow prefill may
qualify; multiple pending shadow prefills, changed attribution, event sequence,
virtual state, backend epoch, preemption, non-monotonic generation, or failed
polls must censor. Completion-only feedback remains unable to certify this
phase. Focused red tests must prove that current shadow evidence is absent,
then prove safe progressive concurrency, unsafe-next rejection before upstream,
resource-accounting isolation, bounded cleanup, and low-flow drain recovery.

Only after that production path exists may the simulator generate the same
evidence and rerun the unchanged goodput and safety acceptance gates. The
already-corrected protection publication contract remains mandatory throughout:
a load-protection decision publishes Router-consumed effective fullness and its
bounded activation log before the proxy emits 429; request-scoped rejection
does not suppress idle capacity; availability protection publishes the sentinel
until current health recovers. r16 blocks every release and live action.

#### v0.10.5 r22-r24 ready-decoder and attribution correction — product red

The shadow-prefill bridge was subsequently guarded by the Manager event
sequence captured atomically with the admission decision. A shadow observation
may qualify only when that immutable decision sequence equals the preceding
stable Manager snapshot sequence and the existing single-prefill, stable-store,
stable-vLLM, no-waiting, no-preemption, no-reset, and successful-fetch gates all
hold. A decision followed by any intervening Manager event before the first
poll is censored. The bridge remains anonymous and bounded: it stores no request
ID, model, user, prompt, payload, or token IDs and never enters Manager KV,
workspace, or concurrency accounting.

r22 used baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`f1a39a9dab88f2a8e8d8530991805b6c090f4be8071ea832f65104ed793a8381`,
and runner SHA-256
`bb72f148ee4fe4fbda19d783bdeae805e09aa76ecaae2bcc904458ad359906f8`.
Formatting and the runtime-predictive, server, predictive-metrics, and
predictive-simulation packages passed on builder CVM
`4f167f6e-4c50-415f-99f2-94b65652beba`, Go 1.24.5, linux/amd64,
`CGO_ENABLED=1`. The goodput package correctly remained red: predictive
completion-token goodput was `40096` versus current `39840`, only `+0.64%`
against the unchanged `>=5%` requirement. TPS, TPOT, KV-hard, preemption proxy,
false accepts, and reservation leaks were all zero; false denies were 34. The
downloaded r22 evidence archive SHA-256 was
`c263bb9da607c7273cd7ff681f9d88c5641485c3e57176f545238ce2ac9a2be0`.

The next causal defect was that `DecodeSequences` represented both ready
decoders and admitted requests still in prefill. Static existing-user TPS was
therefore protecting users that did not yet exist in decode. The candidate now
tracks a bounded integer `PendingPrefillSequences`; only
`ExistingDecodeSequences - ExistingPendingPrefillSequences` participates in
existing-user prefill TPS, while total projected sequences still drive
post-prefill new-user TPS, TPOT, and KV forecasts. `MarkPrefillComplete` removes
the pending phase without changing total concurrency. r23 used overlay SHA-256
`884fa2420f5dcbf51d13eef20158ee0fa28aec3b79dc0d02fc75fba09744b8a2`
but stopped at formatting before Go tests; it is not green evidence.

r24 used overlay SHA-256
`3c29f17611c6f86adc1d697a13505e1e59e052cd4c46a23538f205bdef05b1ee`
and runner SHA-256
`b1890428348ea3bc21ce8a305c36b377598b0814aeca2699647c34ac46fb4f85`.
Formatting, predictive metrics, predictive simulation, and all but two focused
runtime/server compatibility tests passed. Predictive goodput rose to `41376`,
or `+3.86%`, with zero protected violations, false accepts, or leaks, but still
missed the product threshold. Its trace proved the split fixed the nonexistent
existing-user guard; the remaining safe third request was rejected as
`new_tps_at_risk`. The remote r24 directory had already expired before the
evidence archive could be downloaded, so r24 is retained only as
contemporaneous diagnostic output and is not hash-addressed release evidence.

#### v0.10.5 r25-r27 signal-phase compatibility correction — focused green

The r24 review found that one generic pressure predicate was incorrectly used
for two different feedback targets. Existing-user TPS measures interference
during prefill, whereas joining-user TPS and TPOT measure aggregate decode
capacity after prefill. Pending-prefill count and uncached-prefill work are
therefore causal for the first target but decision-phase-only features for the
second. Keeping them in every optimistic evidence gate caused mature safe
decode capacity to be ignored during same-poll bursts.

r25 was a harness failure, not a source result. It used overlay SHA-256
`d64fc66269da0779ea9f921046fe0e5b1ccf2e0ac87a07165260b0bbdfE18E0E`
and runner SHA-256
`780e0cd2c4650faef4c2fcaa820bedff1b34df2b6808458aec431672bf2ab243`;
the non-login container PATH omitted `/usr/local/go/bin`, so `gofmt` was not
found, `gofmt.status=1`, and focused tests were deliberately skipped with
status 125. Its evidence archive SHA-256 is
`d017e068bb8c31ccdfbcb76133eab793f6ef71583ea9e01c5094dbffa4467b5d`.
No product claim inherits from r25.

r26 corrected only that runner defect and kept the same source overlay. It was
format-clean and all runtime-predictive, server, predictive-metrics, and
predictive-simulation packages passed. The goodput package reached aggregate
`43168` versus current `39840` (`+8.35%`) with zero TPS, TPOT, KV-hard,
preemption proxy, false-accept, or leak counts. It nevertheless remained red
because the repeated-prefix cache-cold contract admitted only three of four
ground-safe requests: predicted TPOT was 48 ms while ground TPOT was 27 ms,
and peak reservation was `10944` rather than four full cold costs `14592`.
The source still charged every repeated prefix cache-cold; the rejection came
from feedback compatibility, not a cache-hit assumption. r26 evidence archive
SHA-256 is
`a01338e9b13ec644f3662e76cab39d624dfaa4f10368224267c4f7b85ca6d035`;
its focused log SHA-256 is
`e998ff05b5c1002ede924b37b95940afe5784caed3c8872726681544bb60e2af`.

The final phase-specific implementation uses three small predicates:

- existing-user prefill TPS requires decode-concurrency, pending-prefill,
  uncached-prefill, active-context, physical-KV, and active-KV dominance;
- post-prefill TPS and TPOT ignore decision-phase pending/uncached-prefill state
  but retain active-context, physical-KV, and active-KV dominance;
- TTFT retains its separate request-complexity compatibility and remains
  observation-only.

This is a constant-time hot-path split over existing integer features; it adds
no model asset, cache lookup, request identity, backend fetch, map, or unbounded
state. Adverse TPS evidence remains immediately conservative, optimistic
evidence still requires the configured mature sample count, and optimistic
decode evidence cannot cross into higher decode pressure.

r27 was reconstructed from baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`83a35af9d1835f47b93576e48652fd3ffb468046681f6ab8965b8ce9edc660ac`,
inner runner SHA-256
`5d2f5bba8eced2b9d8a2a711231d0ea443221f69b065b1a2664a9a6b7273b71a`,
and host runner SHA-256
`ea92c4d41a89aae09676099744518f984ff956cc32fb0a77f16577cb4258b8ce`.
`gofmt.status`, `focused.status`, and `overall.status` were all zero. The five
packages were:

```text
./internal/runtime/predictive
./internal/app/server
./internal/observability/metrics
./internal/simulation/goodput
./internal/simulation/predictive
```

The first downloaded r27 evidence archive SHA-256 is
`00a301d4db81484c718574742e926be6bd316e8dddb5b2d149610daacdeaec3b`;
its empty `gofmt.diff` SHA-256 is
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`
and focused log SHA-256 is
`9cb089eec07ad78de6840b90d22a74a4dc224c9b0f7894a68ffb240c288cc40e`.

An additional verbose run on the identical r27 source produced predictive
completion-token goodput `44704` versus current `39840` (`+12.21%`) and
v0.9.0 KV-only `37536`. Predictive admission recorded 51 SLO-compliant
completions, zero TPS/TPOT/KV-hard/preemption-proxy violations, zero false
accepts, seven false denies, zero reservation leaks, and four TTFT-only
diagnostics. The repeated-prefix scenario reached `1024` completion tokens and
the four-full-cold-cost assertion passed. The additive verbose evidence archive
SHA-256 is
`0844b119a8b3b5d7d081feea5a6aab33b96f996e7ace960bfb3fcc14636b07cd`;
`goodput-verbose.log` SHA-256 is
`c404e5f5a328ac397a159e75b7c42c386ee57ee332a4614aea76b659c5e2cda4`.

#### v0.10.5 post-r27 review pass 1 — model and causality

The review traced real HTTP classification through approximate size estimate,
post-admit scheduler features, atomic Manager decision/reservation, typed
adapter outcome, proxy rejection, lifecycle observation, and future learning.
It confirms that feedback never changes the current request. The publication
callback observes the adapter's locked protection snapshot before the proxy
increments `pig_predictive_admission_enforced_rejects_total` and writes 429;
at that earlier point the exact Router fields already report `running=1`,
`waiting=0`, `limit=1`. Joining-user TPS/TPOT evidence is now matched to its
post-prefill phase, while existing-user TPS retains prefill interference
dominance. No TTFT value reaches `domain.Evaluate` as a rejecting constraint.

#### v0.10.5 post-r27 review pass 2 — safety, efficiency, and SOLID

The review rechecked close/decision rollback, invalid typed-result cleanup,
forward/semantic/completion/release/terminal idempotence, bounded shadow and
deferred stores, Manager event-sequence attribution, current-health availability
recovery, and load drain-to-zero escape. Request-scoped oversized input cannot
lock an idle node; availability quarantine cannot emit invisible repeated 429s;
load protection stops applying as soon as current predicted load drains even if
the fixed diagnostic episode has not expired. Signal-specific predicates keep
feature interpretation inside the scheduler, while proxy, adapter, Router
projection, metrics writer, and lifecycle coordination retain narrow consumer
interfaces. The new comparisons are O(1), allocation-free integer operations
on already-present features.

#### v0.10.5 post-r27 review pass 3 — evidence and release

The review classifies r25 as invalid harness evidence, r26 as valid product red,
and r27 as focused green only. It retains the unchanged acceptance thresholds
and the real adapter-to-HTTP-to-Router-parser regression, but does not promote
focused success into release readiness. The next mandatory gate is a fresh
post-document full builder archive covering format, vet, all tests, targeted and
full race, build, deterministic KV and predictive-goodput simulations,
candidate/baseline hot-path benchmarks, and the builder-local production image
contract. Until that complete matrix is green there is no v0.10.5 version bump,
commit, push, tag, image publication, Compose change, CVM deployment, Router
enablement, or 30-minute canary authorization.

#### v0.10.5 full matrix r28 and evidence archive repair — superseded green

The first complete matrix after r27 used baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`c1a6eec44d9a932f3cc48b8de75b356c0a35fa7d66862e512d5e08fdd8446250`,
inner-runner SHA-256
`086690d2ff27ccc11d389fc5c99862fa08fd643ca903c553c205816b04377846`,
and host-runner SHA-256
`90b1bc1f7fe7a6800378aeb38f11c5240e443023fd44d55c8113c8f80569f9ab`.
On the remote `pig-ubuntu-builder` container, every actual status was zero for
formatting, `go vet ./...`, all tests, targeted race, full `go test -race
./...`, build, deterministic KV simulation, KV performance, verbose and JSON
goodput simulations, candidate and v0.10.4 baseline benchmarks, fallback
benchmark, builder-local production image build, production image contract,
image inspection, and overall result.

The functional results remained predictive completion-token goodput `44,704`
versus current threshold `39,840` (`+12.21%`), with zero protected TPS, TPOT,
KV-hard, preemption-proxy, false-accept, or reservation-leak violations. The
estimator recorded 64-KiB p95 `7.715 us`, 2-MiB p99 `124.481 us`, and shadow
decision p99 `8.398 us`. The builder-local, unpublished image was
`pig-v0105-matrix-r28:local`, image ID
`sha256:3568e0d62b5513437d746d0eb98cd42a60fe6d65eec0bfb514cd9ae620cf12b7`,
and still carried version label `0.10.4`; it was only a structure/contract
check and was never pushed or deployed.

The original evidence packaging used a BusyBox-incompatible `xargs -0` and
therefore produced an empty checksum file after all functional gates had
already run. r28b repackaged the unchanged evidence without rerunning or
skipping a functional gate. Its archive SHA-256 is
`1e70592a46a853c3ed04a13a67e16aa9a6e66d20eb88a360c37d69a5049af48c`;
its internal checksums were verified once on the builder and twice locally.
This was a valid complete green result for that exact source, but the later
reservation-layout optimization changed executable bytes and superseded it.
It cannot authorize a release or deployment of the current candidate.

#### v0.10.5 focused r29 — shadow-only pointer optimization accepted

Static allocation review found that r28 embedded a complete anonymous pending-
prefill observation in every `approximatePredictiveReservation`, including
normal enforce requests that can never use shadow-prefill attribution. The
accepted correction stores only a nil-able pointer in the common reservation
and allocates the observation/handle object only for a shadow-mode risk request
that has a valid pending-prefill observation. All cleanup remains centralized
and idempotent. This preserves the bounded anonymous shadow contract while
removing the large enforce-path object penalty.

r29 used the same baseline SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`42e3efc04cdd6e096de1dc94f79df5d32428d19b8743f511ed29ebfaa65e4e97`,
inner-runner SHA-256
`a1c3a2014fb96610c427292b3a3af6a7b31ce4a1ca56c4cf74c3f438e9f5cd17`,
and host-runner SHA-256
`6fa98275ab1f2d0515ef73f6aae29d2e4a51668f539137ed6cb642167da38c7b`.
Formatting and all five focused packages were zero:

```text
./internal/runtime/predictive
./internal/app/server
./internal/observability/metrics
./internal/simulation/goodput
./internal/simulation/predictive
```

Candidate and v0.10.4 baseline lifecycle benchmarks also exited zero. The r29
evidence archive SHA-256 is
`0537dfa35b143c6746e2689e56137b1fb15847b26ea9f1f00a0c6f3f20c39e8a`;
the focused log SHA-256 is
`a5354139ae38e82d696353c2188f809c22478c5a106a551f48b3816e19e249e5`,
candidate benchmark log SHA-256 is
`4650966d824c1b34e03071bcb4c35c6fd14d83e24acb95fa118b0435c59ed9d3`,
and baseline benchmark log SHA-256 is
`4fccf068af10465deee51d6c491dd75d689a56eb99a669bc6bdd76bef57d1f38`.
The archive's internal checksums passed on the builder and locally.

The enforce normal lifecycle now records `848 B/op`, three allocations, versus
v0.10.4 `832 B/op`, three allocations. This is a large reduction from the r28
candidate's approximately `976 B/op`; the remaining 16 bytes are not another
allocation. Deferred lifecycle medians remain about six to seven percent above
the old baseline in bytes while retaining three allocations. Sequential CPU
timings were noisy, so r29c repeated the exact r29 source directories in the
opposite benchmark order. Its runner SHA-256 is
`6e59f41b193df39ec0fdcd8a19ca63e63b3d5bffc436ca59bfb252c8e2abfb0d`,
archive SHA-256 is
`08b8c78f8d3b1e43276b4f18c310a0db47b33fbccf2d26cf07337267709db90a`,
and both status values were zero. Normal lifecycle was faster in the reverse
order while deferred timing varied in the other direction, confirming that a
single sequential timing delta is not a stable algorithm regression. No
serving-performance claim is inferred from these microbenchmarks.

#### v0.10.5 focused r30 sidecar experiment — rejected without product change

A final efficiency experiment moved the shadow-prefill pointer out of each
reservation into a lazy, bounded shadow-only sidecar map. This was test-first:
the candidate added cleanup assertions for completed and unforwarded shadow
observations and retained the five focused packages and dual-order lifecycle
benchmarks. r30 used baseline SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`e80a762461d05f6a6eec5025c986d6bb76aa443f9712428db781b8be73c73c1d`,
inner-runner SHA-256
`1ba3c5129c08d75511753e5bd107ddcd9325a0f529d070e2f3684210ae1c6b67`,
and host-runner SHA-256
`3d68b5a7742ba1fa5b6eb121fe7b73a8cec090d5366e951faa78ef7f80cbff89`.
All formatting, focused, four benchmark, and overall statuses were zero. The
locally revalidated evidence archive SHA-256 is
`3e89824d201bd07eb6071f90bce6af92402095e54fb5213c868fa82bc2fa66f3`.

The result rejected the experiment: normal enforce remained exactly
`848 B/op` and three allocations in both orders, so the sidecar did not remove
the remaining 16-byte difference. It only added a second lifecycle map and
lookups. The sidecar source and its test-only assertions were therefore removed
with `apply_patch`; the simpler r29 optional-pointer design remains active. A
byte-for-byte comparison over all 34 overlay paths between the post-revert
working tree and reconstructed r29 source produced the identical deterministic
content-manifest SHA-256
`0d91f1626fd4dbb358a5540a117a87895e1140eaf340807d52e3f0f9c6388d2a`.
Thus r29 focused functional evidence applies to the active executable bytes;
r30 is retained only as negative optimization evidence.

#### v0.10.5 current release gate after r30 review

The active executable implementation is now the r29 pointer candidate plus
this documentation-only audit update. It has focused builder evidence and a
superseded full matrix, but it does not yet have a complete matrix for the
current archive. The next required gate is a fresh exact full-builder archive
covering formatting, vet, all tests, targeted and full race, build,
deterministic KV and goodput simulations, candidate/baseline benchmarks, and a
builder-local production image contract. Only after that matrix is green may
the version be changed consistently to `v0.10.5`; because the version update
changes executable and Docker build inputs, the final versioned archive must
then repeat the applicable builder and image-contract gates before commit,
push, annotated tag, immutable registry image, or any Router-disabled CVM
deployment.

No v0.10.5 commit, push, tag, registry publication, Compose change, deployment,
Router mutation, production request, or 30-minute canary has occurred. TTFT
remains measurement/learning/diagnosis only and must not become an admission or
Router-capacity condition in any subsequent gate.

#### v0.10.5 pre-version full matrix r31 — complete green

r31 used baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
overlay SHA-256
`7a310f45e2c8a7a568abc9c1091f5d76536b63990bcb792f85fa7fc1a8d127eb`,
inner-runner SHA-256
`8b23c5766e252fc88009ae405c6c5b2133ce94035a890342f29996ba38e354f7`,
and host-runner SHA-256
`4bc3378346f31c9a25051e424b6b9482f92c0bc0fa7a18c1f18759a1eb74230a`.
It ran inside the existing `pig-ubuntu-builder` environment with Go 1.24.5,
linux/amd64, and `CGO_ENABLED=1`. Every status was zero for formatting, vet,
all tests, targeted race, full race, build, deterministic KV simulation, KV
performance, verbose goodput, JSON goodput, candidate benchmarks, fallback
benchmark, v0.10.4 baseline benchmarks, source overall, builder-local image
build, production image contract, image inspection, and final overall.

The deterministic aggregate remained:

```text
current threshold completion-token goodput: 39840
v0.9.0 KV-only completion-token goodput:    37536
predictive completion-token goodput:        44704
predictive improvement versus current:     +12.21%
predictive SLO-compliant completions:       51
predictive TPS violations:                  0
predictive TPOT violations:                 0
predictive KV-hard violations:              0
predictive preemption-proxy events:         0
predictive false accepts:                   0
predictive false denies:                    7
predictive reservation leaks:               0
TTFT diagnostics only:                      4
```

The low-flow progress and drain-recovery scenarios remained green, and the
repeated-prefix scenario again passed full cache-cold charging. Performance
recorded 64-KiB estimator p95 `1.985 us`, 2-MiB estimator p99 `124.567 us`,
and shadow decision p99 `6.955 us`. The unpublished builder-local image was
`pig-v0105-matrix-r31:local`, image ID
`sha256:6136d1024a79c34abd325f11276aa9931f81dfa76721a1b4b02030d4f23b875d`,
with contract version `0.10.4`, entrypoint `/phala-inference-guard`, and
`NVIDIA_VISIBLE_DEVICES=all`.

The downloaded evidence archive SHA-256 is
`f051cdcd36fdab00abda90949da473471f0f1fb13398ab744dcdf9000b584359`.
Its internal SHA256SUMS passed on the builder and a local second verification.
Material log SHA-256 values are:

```text
full-test.log             10fce57d8c92c5f58126d54e2e35d49953892a27116cf9b51806ccf7f28bc176
targeted-race.log         18cdc9d7735f25de53babb428668472700ace2454c44bc3690dd60ecee7acee4
full-race.log             61e091f0538153f09b2ae13d4e2afc8225bcab715853b9b38e389fe15eeee93b
goodput-verbose.log       92c325cdc87373b2b4a978005a07bd6b211e4a619ec8bc70a0a4f291bc872337
kv-performance.log       29af024bddb5c8bf72ea11a00725453a5786b264a27e73082b91503c14403879
candidate-benchmarks.log ace8bc7a29c15a35918d89b1598b2b48884bb59d468b9ff46e3cc708cd7cd8b5
baseline-benchmarks.log  d3da1c24c6550c9ebe2c04dc2a51a6d9dca5ac67b5ec9011673a7b04786c2ed5
image-contract.log       4165703e25a5059114ebfde0ac5e667f3bfbdf19b355f4228e66830d4879ed03
```

r31 proves the complete pre-version source and image shape only. It did not
publish an image or authorize deployment because the runtime and image label
still identified v0.10.4.

#### v0.10.5 version unification — active after r31

The working tree now consistently identifies the candidate as v0.10.5 in the
Docker OCI version label, runtime `PIG-v0.10.5` constant, README section and
Compose example, advanced configuration heading, observability example, and
goodput acceptance comment. A current-tree search found no non-historical
v0.10.4 reference outside this audit plan. These changes affect executable and
Docker inputs, so r31 cannot be reused as final versioned evidence.

The next mandatory gate is a fresh exact versioned builder matrix and production
image contract expecting `v0.10.5`. It must at minimum repeat formatting, vet,
all tests, targeted and full race, build, deterministic simulations, candidate
benchmarks, builder-local image build, image contract, and image inspection.
Only after that exact archive is green may source be committed and pushed,
annotated tag `v0.10.5` be created and pushed, and an immutable registry image
be built/published and verified. No Compose, CVM, Router, or production-traffic
action is authorized by r31 or the version-text update alone.

#### v0.10.5 final versioned full matrix r32 — complete green

r32 used baseline archive SHA-256
`6ee67ee4d426a29893e9959c01065aae73bab2b76d9ce4b1f6852c0e42adf7a8`,
versioned overlay SHA-256
`8b1d660e5524b0e56fb245dd9bbd7e2c9139a0b1bf7db3a8a36aaaa8513a1b28`,
inner-runner SHA-256
`3a3b843e29fd6ccbfdfab1aae5d22e4b4d7eae67dd205944bafe07b5bd295bde`,
and host-runner SHA-256
`499ff938cb8d4c70d7fd5914abe92b1475cfef3477ab5202f8a7104a3b7b7ac3`.
The environment remained Go 1.24.5, linux/amd64, `CGO_ENABLED=1` in the
approved remote builder container.

Every status was zero for formatting, vet, all tests, targeted race, full race,
build, deterministic KV simulation, KV performance, verbose and JSON goodput,
candidate benchmarks, candidate fallback benchmark, v0.10.4 baseline
benchmarks, source overall, builder-local image build, the production image
contract expecting `v0.10.5`, image inspection, and final overall. The
deterministic goodput and safety result remained `44,704` predictive completion
tokens versus `39,840` current (`+12.21%`), 51 SLO-compliant completions, zero
TPS/TPOT/KV-hard/preemption-proxy violations, zero false accepts, seven false
denies, and zero reservation leaks. TTFT remained four diagnostics and was not
an admission constraint.

The final versioned performance run recorded 64-KiB estimator p95 `2.922 us`,
2-MiB estimator p99 `200.269 us`, and shadow decision p99 `2.607 us`; all are
inside the unchanged acceptance thresholds. The builder-local image
`pig-v0105-matrix-r32:local` has image ID
`sha256:d881fd5d1339c4c2411ed438645cd0d7c14c46375ac24b244c510c7baa1d5760`,
OCI version `0.10.5`, entrypoint `/phala-inference-guard`, and
`NVIDIA_VISIBLE_DEVICES=all`. The contract output is exactly
`PIG_PRODUCTION_IMAGE_CONTRACT_OK image=pig-v0105-matrix-r32:local
version=0.10.5`.

The downloaded r32 evidence archive SHA-256 is
`654c4cfbe20295332522b601cee567679da300ed85cb4873253d7f3078dc3c81`.
Its internal checksums passed on the builder and in a local second
verification. Material log SHA-256 values are:

```text
full-test.log             f978f75e8802bbda35376bf8abced42bdddf64f7ecb3465a17895e2b8ab6241b
targeted-race.log         551c615a5fed4f8ad32c4cb0e804ad1bcecc6c313fb435cfd90a8c1a2dde297f
full-race.log             720ec3e351bc7b91ee44a8df26f9f46e31b459b059196a8c862c54ce8807e8df
goodput-verbose.log       08b611bfb37e3cc47671eb0db6788dca6fefb952e47eb86bcecc0860f9101228
kv-performance.log       4c5a74970f7236f43bcadca31374729b3fa7906861609e6a4d5d90ac71ab671f
candidate-benchmarks.log b648b5ca67daccec52aab767a6c224d37092fbabbdbea0a035db85de1159a16c
baseline-benchmarks.log  bc0b803140c6bd52b6b799dca77906b16522489cf1faecf968b6618a14efe435
image-contract.log       40a2b196341f21c026de571e4f5d442ffc8d5278baddb924650bfc57be5f4827
image-inspect.json       b31b05a043846daea2eae0c6db9d00b96321c906fa00bcc0251bd7010831076f
```

This section is a documentation-only evidence update after r32. The final
commit gate must prove that every `cmd/`, `internal/`, `go.mod`, `go.sum`, and
`Dockerfile` byte remains identical to the r32 reconstructed source. If that
check passes, the r32 executable and image-contract evidence applies to the
commit candidate; otherwise a new matrix is mandatory.

#### v0.10.5 final review pass 1 — model and causality

The complete request path was retraced from bounded model-neutral request-size
classification through current observation plus unabsorbed reservations,
post-admit Scheduler prediction, atomic Manager decision/reservation, typed
adapter result, and proxy forward or pre-forward reject. Feedback can only
update a later prediction. The real HTTP regression proves that a load reject
does not call upstream and that, inside the synchronous activation callback
before the 429 counter or response, Router already parses effective
`running=1`, `waiting=0`, `limit=1`. TTFT observations do not reach an admission
constraint or Router projection. Request-scoped oversized/unsupported input
remains local and cannot suppress an otherwise idle node.

#### v0.10.5 final review pass 2 — safety, efficiency, and SOLID

The lifecycle review rechecked reservation check-and-create atomicity,
forward-commit failure, prefill completion, early resource release, deferred
completion outcome, cancellation/error/timeout/disconnect, invalid typed
results, adapter close races, epoch reset, and exactly-once terminal cleanup.
Manager and shadow stores remain bounded; retained state and logs contain no
model, user, request ID, prompt, body, bearer, token ID, or tokenizer asset.
Availability protection clears from current health, load projection stops as
soon as current predicted load drains, and fixed episodes cannot create a
low-flow sticky lock. The r30 sidecar experiment was removed because it added
state without reducing `B/op`; the accepted r29 optional pointer keeps shadow-
only allocation off the enforce path without extra maps. Components retain
narrow estimator, calibrator, scheduler, coordinator, adapter, lifecycle,
metrics, and Router-projection responsibilities.

#### v0.10.5 final review pass 3 — evidence and release boundary

The evidence review distinguishes r25 harness failure, r26 product red, r27
focused green, r28 superseded full green, r29 accepted hot-path focused green,
r30 rejected optimization evidence, r31 pre-version full green, and r32 final
versioned full green. Exact archives, runner hashes, environment, status files,
logs, simulations, benchmarks, image contract, image inspection, and local
checksum verification are retained. r32 authorizes only the next source-release
steps after byte-identity, secret, diff, and Git gates. It does not by itself
prove a registry image, Compose integration, Router-disabled deployment, live
readiness, Router enablement, or a 30-minute traffic canary. Those layers must
remain separately evidenced, in that order, with `use1-cb` disabled until both
shadow and enforce live gates pass.

#### v0.10.5 source release and immutable registry evidence — complete

The r32 executable inputs remained byte-identical through the release commit.
The nested repository release state is:

```text
branch:        codex/pig-v0.10.0-model-agnostic
commit:        85047e4e5a1b7bafa5c628cd92f0481b86fcd65b
annotated tag: v0.10.5
tag object:    87f0cc266f25c205449855762b57a86c231ce7fc
```

The branch and annotated tag were pushed to `pig-origin`, which is the
`Phala-Network/phala-inference-guard` repository. The similarly named `origin`
remote points to a different repository and was not used.

The tag workflow published the immutable deployment input:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:84eee2b3008dec884ced7471d20f38eb64d0406473eb9d7196eb5acc962ab577
```

An independent registry pull passed the production image contract with OCI
version `0.10.5`, entrypoint `/phala-inference-guard`, and the expected NVIDIA
environment. The SHA-256 of `/phala-inference-guard` inside the published image
is exactly the r32 tested-binary SHA-256:

```text
1f8f8d9ad5a19cd9b08987c0977e4348e365750f614a0e7656518a096180120b
```

The registry verification archive is
`tmp/pig-v0105-use1-cb-20260802/registry-verify-evidence.tar`, SHA-256
`2068fb6be73f67c9c7f6b78a6509d32e46ff3384efa2bb189a782d5c8e43f00b`.
This proves registry identity and binary equivalence, not Compose integration,
deployment, live readiness, Router enablement, or production behavior.

#### v0.10.5 fresh live preflight — passed for Router-disabled shadow deploy

At `2026-08-02T19:42:08Z`, a new read-only preflight was captured under
`tmp/pig-v0105-use1-cb-live-20260802/predeploy-shadow-20260802T194208Z`.
The target `a0f0bfb3-e46f-4b22-814e-24872f251193` was `running` with
`in_progress=false`, no boot error, and exact live Docker Compose SHA-256
`d014719f6d3926ad08c5ac76f1462ed72e4a1e130a0397b9f9c4e3b889568e29`.
That byte-exact Compose is retained as the rollback input.

The live PIG, vLLM, HAProxy, and dstack-ingress containers were running. The
PIG and vLLM were idle: predictive reservations, shadow observations, deferred
outcomes, vLLM running, vLLM waiting, KV usage, and preemptions were all zero;
predictive intake was open and lifecycle failure counters were zero.
Authenticated `/v1/models`, `/pig/metrics`, `/v1/metrics`, and attestation
returned HTTP 200, NVIDIA attestation was non-empty, and both metrics paths
returned 401 without authentication. The captured artifact secret scan passed.

The current Router digest was
`sha256:8969f268ba986f106f9085ffe64f48db9199c5527d0d4dd83c92b44b0a2499c1`.
Its current enabled set was exactly `use1-4c`; `use1-9b` and `use1-cb` were
both disabled. This differs from older snapshots and is now the preservation
baseline: this rollout must not enable, disable, or otherwise modify `use1-9b`
or another upstream. The target `use1-cb` route was disabled and had
`running=0`. Its Router-side PIG state was `not_collected`/stale because the
disabled route was not being polled; this is not evidence of a live metrics
failure, and after a later authorized enable the canary must prove
`pig_ok=true`, `stale=false`, and correct protection projection from fresh
Router state.

The next gate is to generate a shadow candidate from the exact rollback
Compose and prove that its only semantic changes are the PIG image digest above
and `PREDICTIVE_ADMISSION_MODE=enforce` to `shadow`. vLLM, HAProxy,
dstack-ingress, all thresholds, `DYNAMIC_TTFT_ENABLED=false`, and every other
field must remain unchanged. The centralized-KMS update must not pass `.env` to
`phala deploy`.

The generated Router-disabled shadow candidate is
`tmp/pig-v0105-use1-cb-live-20260802/predeploy-shadow-20260802T194208Z/use1-cb.v0105.shadow.candidate.yaml`,
SHA-256 `6644b9f2c148ddfa6b74b7f3b6fbc8c1abecb2965fa93245b201f6a76d24983f`.
Its unified diff contains exactly the two authorized lines, and reversing those
two replacements reproduces the rollback Compose byte-for-byte. A second live
drift check at `2026-08-02T19:45:44Z` reproduced the same Compose SHA-256,
Router digest, enabled set `use1-4c`, disabled/drained `use1-cb`, endpoint
gates, and zero active PIG/vLLM resource state. This authorizes only the
Router-disabled shadow deployment of that exact candidate.

### v0.10.6 sustained protection publication and shadow-feedback repair plan — active 2026-08-02

This section is the current corrective authority and supersedes the v0.10.5
claim that load protection must use a non-renewing fixed episode. Fresh review
of the retained v0.10.4 Router canary proves that claim was wrong. It also
supersedes any earlier promotion statement for v0.10.5: the deployed v0.10.5
runtime remains a Router-disabled shadow diagnostic only until every gate below
passes on a new executable version.

The user-visible defect is a broken publication control loop. PIG can reject a
request pre-forward while its structured status log and Router-consumed metrics
already present ordinary spare capacity. Router then continues selecting the
node, another request reaches PIG, and the node alternates between 429 and new
traffic instead of applying one coherent backpressure interval. Returning the
correct 429 is therefore insufficient: the reject, durable diagnostics,
effective-capacity projection, and later recovery must describe the same
admission state.

#### Corrected live evidence and root cause

The retained v0.10.4 canary r2 is direct evidence of this failure. Predictive
risk decisions advanced `116 -> 193`, enforced rejects advanced `118 -> 196`,
and Router processed advanced `237759 -> 237901`. The fast Router observer saw
fresh, healthy PIG scrapes but alternating effective capacity such as:

```text
13:12:41Z  observed_running/global_limit = 2/2   protected
13:12:43Z  observed_running/global_limit = 1/50  unprotected
13:12:45Z  observed_running/global_limit = 1/50  unprotected
13:12:49Z  observed_running/global_limit = 2/2   protected
13:12:51Z  observed_running/global_limit = 1/50  unprotected
13:12:54Z  observed_running/global_limit = 3/3   protected
13:12:56Z  observed_running/global_limit = 2/50  unprotected
```

Across only 244.922 seconds, activation count advanced `84 -> 129`; nineteen
fast Router samples contained only ten full-capacity projections while traffic
and rejects continued. At the first retained slow sample, PIG already had 149
risk decisions and 151 enforced rejects, yet metrics reported
`router_backpressure_active=0`, `router_backpressure_applied=0`, effective
running `2`, and effective global limit `50`. The same snapshot reported 96
activations and 52 extensions. This is internally inconsistent with sustained
load protection and explains why Router continued to feed the node.

Source review identifies the exact mechanism. The v0.10.5 state uses a hold
derived from PIG's backend poll interval and clamps it to two through five
seconds. A repeated load-dependent reject during an active episode increments
`extensions` but does not advance `until`. The state therefore expires relative
to the first reject even while later requests are still being rejected. A
subsequent reject creates a new short activation, producing the observed
protected/unprotected oscillation. The narrow cold test scraped inside one
episode and could not detect this sustained-load failure. PIG's internal backend
poll interval is not a valid proxy for Router's independent scrape cadence or
jitter.

Logging has the same semantic gap. The first load reject emits
`event=activated`, but rejects inside the active interval produce no bounded
extension event; a later five-second status line may therefore show inactive
capacity even though cumulative enforced rejects continued. Metrics expose a
cumulative extension count, but not a renewed deadline or a rate-limited record
that ties the latest reject to the current lease. This is observability loss,
not merely a log-format preference.

The current deployed v0.10.5 shadow also produced a separate release blocker.
Router remains disabled and shadow request-scoped risk correctly forwards
without node-wide backpressure, but large cold `/v1/completions` risk requests
created and terminated observation-only records without advancing accepted or
rejected input-size feedback. A small fit `/v1/completions` request with the same
2xx JSON usage shape did advance the input-size rejected counter. The defect is
therefore narrowed to the observation-only risk response feedback path; it must
be reproduced by a focused HTTP red test and fixed before v0.10.6 release. It
must not be explained as `finish_reason=length`, because current usage parsing
does not use finish reason and the retained response satisfies the parser's
status, content type, body bound, choice-count, and positive-usage contract.

#### v0.10.6 behavioral contract

1. Prediction and rejection still occur before any vLLM action. Feedback may
   only train a later request. TTFT remains measurement, learning, diagnostics,
   and offline-comparison data only; it cannot reject, activate backpressure, or
   modify Router capacity.
2. One request-scoped oversized, unsupported, or otherwise intrinsically unsafe
   request advances durable fixed-cardinality reject diagnostics and a bounded
   payload-free log, but does not suppress an idle node. Only a load-dependent
   reject with existing predicted work, or current node availability failure,
   may create node-wide Router backpressure.
3. Every load-dependent enforce reject atomically renews the current protection
   lease from the **latest** reject, not the first reject. `until` must advance
   monotonically to at least `reject_time + hold`; it must never move backward.
   Rejections that continue faster than the hold therefore cannot create an
   unprotected gap. A new activation identity is created only after a real
   expiry, not for every short pulse.
4. The hold is a PIG publication-delivery setting, independent of the backend
   metrics poll interval. It must cover at least one complete Router scrape plus
   normal jitter in the deployed configuration and be explicitly observable.
   The implementation may expose a bounded configuration value, but invalid,
   zero, negative, overflow, or unreasonably large values must fail startup or
   normalize conservatively. The production default must not be the current
   two-second value derived from a 100 ms backend poll.
5. Backpressure `active` means a current load or availability lease exists.
   Backpressure `applied` means that the same coherent snapshot has enough
   current/predicted work to project Router fullness. While applied, Router's
   exact consumed fields expose `effective_running >= 1`, `waiting` truthfully,
   and `effective_global_limit <= effective_running`, while raw running, raw
   global limit, and PIG-local admission limit remain separately truthful.
6. A lease does not create low-flow or drain self-lock. If current and predicted
   running both reach zero, capacity projection is not applied even before the
   lease timer expires. With no later load reject, the lease expires after the
   bounded hold. A subsequent cold-safe request is admitted, and all
   active/applied/effective state converges to ordinary capacity without a
   restart, manual reset, or new traffic requirement.
7. The first reject in an episode emits one payload-free `event=activated`
   record before the proxy writes 429. Renewals advance cumulative counters and
   latest-reject/deadline metrics synchronously before 429. A bounded,
   rate-limited `event=renewed` record must expose the activation, reason,
   extension/suppression count, latest reject time, and renewed deadline without
   logging every request. Metrics and logs may contain only fixed enums and
   numeric/timestamp state—never model, user, request ID, prompt, response,
   bearer, token IDs, or request body.
8. The HTTP reject counter, decision reason, durable last reject, activation or
   renewal, and capacity projection are derived from one typed decision under
   the same adapter lock. The proxy may write 429 only after that state commit
   and synchronous transition callback return. Metrics scraping may occur
   concurrently but must observe either the complete previous snapshot or the
   complete new snapshot, never a mixed state.
9. Shadow mode records the same prediction reason and diagnostics but never
   rejects or activates Router capacity. A valid 2xx usage response for an
   observation-only shadow risk must be claimed, parsed, passed to
   `ObserveCompletion`, and produce an accepted or safely rejected input-size
   outcome before terminal cleanup. Failure phases use bounded counters so a
   missing observer/usage outcome cannot remain silent.
10. PIG still does not route and no Router or vLLM source change is authorized.
    PIG's responsibility ends at publishing a coherent, timely capacity view;
    the live gate separately proves that the existing Router consumes it and
    stops selecting the node during sustained protection.

#### Required red/green tests before implementation is accepted

- A deterministic-clock unit red must show the current behavior: reject at
  `t=0`, repeat at `t<hold`, then observe inactive state at the original
  deadline even though the latest reject is newer. Green requires a monotonic
  renewed deadline, one activation, one or more extensions, and no inactive
  interval until `last_reject + hold`.
- A real approximate-adapter HTTP enforce test must hold one decode open and
  produce repeated load-dependent TPS rejects. Concurrent metrics scrapes at
  adversarial points around the original deadline must always expose
  active/applied and Router fullness while rejects continue. Upstream counters
  remain unchanged for every 429.
- A pre-429 publication test must execute the metrics/Router parser from inside
  the synchronous activation and renewal callbacks and prove that the complete
  new projection is already visible before `predictiveEnforcedRejects`, total
  429, or the response writer advances.
- A Router-cadence simulation must vary scrape phase, latency, and jitter. A
  sustained stream of load rejects must be observed as continuously full after
  the first delivered scrape; Router processed/selection is not allowed to
  advance because of a protection-expiry gap. The simulation must also prove a
  finite last reject yields bounded recovery.
- A drain/low-flow test must renew protection many times, release all live and
  reserved work, and prove `applied=0` immediately at terminal zero, `active=0`
  after the hold, then admit a cold-safe request. Include cancellation,
  completion, timeout, upstream error, coordinator reset, and close races.
- Request-scoped large/unsupported input must still return typed 429 with
  durable reason/log metrics while `router_backpressure_active=0` and ordinary
  effective capacity remains visible. This prevents a single unusual request
  from globally locking the node.
- Availability protection remains health-driven rather than request-renewed and
  clears as soon as coherent upstream/coordinator health returns. It must not be
  conflated with the load lease.
- Log tests require exactly one activation per episode, bounded renewal output
  with correct suppression totals, a durable latest-reject timestamp/deadline,
  and no payload/high-cardinality data. Race tests cover simultaneous rejects,
  telemetry scrapes, expiry, health transitions, and close.
- A focused shadow HTTP red must use cold `/v1/completions`, a request-scoped KV
  risk, a non-stream 2xx JSON response with one choice and positive usage, and
  assert observation create/forward/claim/parse/ObserveCompletion/terminate plus
  an accepted-or-rejected input-size counter delta. Green must leave
  observations, reservations, and deferred outcomes at zero.
- Full remote-builder gates remain mandatory: formatting/static checks, all Go
  tests, race tests, deterministic simulations, hot-path benchmarks, image
  build and off/shadow/enforce contract smoke, protocol/auth tests, terminal
  state, archive hashes, and secret scan. No Go/native test or benchmark is run
  on local Windows.

#### Version, review, and live execution order

Executable changes require v0.10.6; v0.10.5 source, tag, registry digest, and
evidence remain immutable. Work stays in the nested PIG repository. The source
change must first prove focused red against v0.10.5 behavior, then focused
green, complete builder matrix, and three recorded review passes: (1) causality
and publication timing, (2) safety/efficiency/SOLID/lifecycle, and (3) evidence,
version, image provenance, and rollout scope. Only then may source be committed,
pushed and tagged, and an immutable image published and independently
pull-verified for binary equivalence.

Live execution remains strictly ordered on
`a0f0bfb3-e46f-4b22-814e-24872f251193`: fresh read-only drift/rollback audit;
Router-disabled shadow; protocol, observer-feedback, latency, sparse/low-flow,
and terminal-zero gates; Router-disabled enforce; standalone request-scoped
reject; sustained load-protection renewals; logs and both metrics paths before
429; drain/recovery; and zero preemption/failure/leak gates. The target Router
route and every unrelated route remain unchanged throughout these phases.

Only after all disabled-route gates pass may exactly `use1-cb` be enabled while
preserving the then-current enabled set. The existing Router must prove
`pig_ok=true`, `stale=false`, a fresh age, at least one sustained protection
interval with continuous full-capacity projection, no selection/processed
advance attributable to expiry gaps during that interval, and prompt recovery
after the final reject/drain. The 30-minute real-traffic observation then starts
from zero and simultaneously tracks Router selection/processed, PIG attempts
and rejects, vLLM inference, single-user TPS/TPOT, KV, waiting, preemption,
goodput, logs, and metrics. Any publication mismatch, continued selection during
sustained protection, TPS/TPOT/KV/preemption violation, observer failure,
sticky lock, leak, fatal/OOM/Xid, or unrelated route drift immediately disables
only `use1-cb`, retains bounded evidence, and restarts the repair loop with a new
version.

Current safe live state is unchanged: v0.10.5 runs in shadow on the authorized
CVM, `DYNAMIC_TTFT_ENABLED=false`, `use1-cb` is Router-disabled and idle, and no
enforce promotion or 30-minute canary is authorized by this plan update.

#### v0.10.6 plan review pass 1 — causality and publication timing

The first review retraced the real transaction and confirmed that the failure
is not delayed backend feedback: the unsafe candidate is rejected from current
prediction, but publication expires from the first reject while later rejects
continue. The corrected causal invariant is
`active_until >= latest_load_reject + hold`, committed under the adapter lock
before the typed decision returns to
the proxy. The synchronous callback executes only after that lock is released,
so a callback may safely scrape the just-committed snapshot without deadlock and
must complete before the proxy increments its enforced-reject/429 counters or
writes the response.

This review also corrected an over-broad Router assertion. Router `processed`
or running can move briefly because a selection already in flight before the
new scrape may complete. Live acceptance therefore captures activation time,
first fresh full-capacity Router scrape, and a bounded propagation allowance;
only selection/processed growth that begins after that boundary and is not an
already-recorded in-flight request is a publication failure. The deterministic
simulation has no such network ambiguity and still requires zero post-scrape
selection during continuous protection.

#### v0.10.6 plan review pass 2 — safety, efficiency, and SOLID

The second review rejected any permanent latch or request-triggered global
lock. Renewal is allowed only for load-scoped rejects whose atomic prediction
contains existing sequences (including tracked pending-prefill reservations),
or for the separately health-driven availability state. Intrinsically oversized
or unsupported requests remain request-scoped. Projection is derived from an
immutable telemetry snapshot and is not stored in the scheduler, manager, or
Router client; this preserves estimator, scheduler, lifecycle, publication, and
rendering responsibilities.

The production publication lease is decoupled from `DYNAMIC_POLL_INTERVAL`.
The implementation target is an optional
`PREDICTIVE_ROUTER_BACKPRESSURE_HOLD` duration with a five-second default and a
validated two-through-thirty-second bound. Direct adapter tests may inject a
shorter deterministic hold, but production config cannot silently become 200
ms merely because backend metrics poll every 100 ms. Every renewal is O(1),
uses existing fixed state, and adds no request-keyed map or payload retention.
Rate-limited renewal logs prevent request-volume amplification. Immediate
`applied=0` at current and predicted running zero plus bounded expiry proves
drain recovery and prevents low-flow self-lock.

#### v0.10.6 plan review pass 3 — evidence and rollout boundary

The third review separates four facts that earlier evidence conflated: a local
429, a synchronous state commit, a PIG metrics scrape inside one short episode,
and the Router actually consuming sustained protection. The new builder red
must fail on non-renewal itself; the green must cover adversarial expiry/scrape
interleavings, not only a convenient immediate scrape. Live shadow cannot prove
429 or Router capacity behavior, and a disabled Router cannot prove consumption;
therefore shadow feedback, disabled-route enforce publication, Router scrape,
and the new 30-minute canary remain distinct gates.

The review also keeps the observation-only `/v1/completions` anomaly as an
independent v0.10.6 blocker. A passing renewal fix cannot hide missing feedback,
and a passing observer fix cannot authorize Router traffic until sustained
publication is proven. No current live mutation is needed for either focused
red; all executable tests remain remote-builder-only. The plan document is the
only changed repository path at this review point.

#### v0.10.6 sustained-publication focused red r1 — exact builder evidence

The first executable v0.10.6 gate ran remotely on
`vllm-v024-patch-builder-use1` (`app_89811a9add5b20427ee1fbf4dc22a33984e41959`),
not on local Windows. Direct SSH required the current `id_ed25519` key, the
DStack TLS `ProxyCommand`, and gateway port `443`; the earlier port-22 attempt
timed out before authentication. The long-running Ubuntu builder container had
Git and Docker but no Go toolchain, so the test ran in the builder-local
`golang:1.24.5-bookworm` image with registry digest
`sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`.
The recorded environment was Go `1.24.5`, `linux/amd64`, `CGO_ENABLED=1`.

The exact v0.10.5 baseline archive and test-only overlay were independently
hashed before execution and again after transfer:

```text
base.tar.gz   b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
tests.patch   89a86fbaa02559d76272dcb561f19a2fa251d362bfefcf59a73c3a9bdaebb52e
builder-red.sh 0635d88abc50b999eb226e44cbf779603dbcd1b11a2950a48864568459fa5ce6
```

`TestPredictiveRouterBackpressureRenewsFromLatestLoadReject` exited `1` for
the intended product reason. After a first reject at `t=0`, a second reject at
`t=1.5s`, and a snapshot at `t=2.5s`, the state reported `Extensions=1` but
`Active=false`, activation `0`, and the original `Until=t+2s`. It therefore
proved that the counter advanced while the publication lease did not renew.
This is the deterministic source-level equivalent of the retained live
protected/unprotected Router-capacity oscillation.

The independent
`TestApproximatePredictiveHTTPShadowRequestScopedCompletionRiskFeedsInputCalibration`
fixture exited `0`: the small non-stream completion fixture successfully
created and terminated its observation and produced input-size feedback. That
pass does **not** close the retained live 1.6 MB completion anomaly. It narrows
the next evidence step to a bounded large-body/ReverseProxy/response-observer
fixture or observer-stage telemetry; no speculative learner change is allowed.

Formatting was clean. The evidence archive is retained under
`tmp/pig-v0106-use1-cb-20260802/red-r1/`, has SHA-256
`5318211992fa3749abc7624d700fbb8ba0e5ab66eb228cf67bca53ab20bab1b6`,
and records `overall.status=intended_renewal_red`. This red authorizes the
smallest coherent v0.10.6 implementation slice; it is not green evidence, an
image, a deployment, or permission to enable Router traffic.

#### v0.10.6 minimal renewal slice focused green r2 — interim evidence

The first green attempt, r1, was correctly rejected rather than promoted: the
new zero-value hold validation exposed a test proxy fixture that had not set the
five-second production default, and the formatting gate found three `gofmt`
alignment diffs. The renewal test itself reported no separate failure, while
the config and metrics packages passed. Its failed evidence archive is retained
with SHA-256
`562cd3dd24b0979f169c8dada04ac9abff4e33d3ab19466645d90a958fc83a5e`.

After changing only that fixture and the reported formatting, r2 ran from the
following independently verified inputs on the same Go 1.24.5 builder image:

```text
base.tar.gz       b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
source.patch      bd6915d6e181a71e0f54b3fb1707093800cfcb474337fc0f94f19c389ef31b8c
builder-focused.sh 576d4be71b6a37c934d70f4b24bd9db8387349675e8819ee62151e0c7702bebc
```

The focused server suite passed the deterministic latest-reject renewal,
immutable activation, bounded renewal log, request-scoped non-lock,
availability overlap, effective-capacity, status, and small shadow completion
feedback cases. Predictive config tests passed the independent five-second
default, explicit duration, invalid/zero/out-of-range rejection, and TTFT-off
contract. Predictive metrics writer tests passed, and `gofmt.diff` was empty.
All three test statuses and `overall.status` were zero.

The locally retained r2 evidence archive is under
`tmp/pig-v0106-use1-cb-20260802/green-r2/` with SHA-256
`6836af62568e5fbbc8e3caaf4a62f70a1e3f013eaf8b0ff2599b877449aae85a`.
This is focused source green only. It does not yet prove sustained real HTTP
429 publication across the old deadline, callback-before-429 visibility,
adversarial Router scrape cadence, drain/race behavior, the retained large-body
shadow anomaly, a complete builder matrix, image provenance, deployment, or
live Router consumption.

#### v0.10.6 sustained HTTP publication focused green r3 — interim evidence

The r3 source added a real proxy HTTP sequence around the former expiry gap.
Its independently verified remote-builder inputs were:

```text
base.tar.gz       b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
source.patch      ec02ba59390f4c8906afc76a8b824c5e62b7427cf18da88e61ed3fdbe9bd70b9
builder-focused.sh 8b6ae9fa9f324d6c720a9a03e7b3d9f38b4e2f862c9033dfef5313ec4e33b964
```

Three HTTP requests at `t=0`, `t=1.5s`, and `t=1.6s` all returned 429 and
made zero upstream calls. The activation callback scraped metrics before the
first enforced-reject counter advanced; the first renewal callback scraped
before the second counter advanced. Both callback snapshots already exposed
`active=1`, `applied=1`, and the Router-consumed `running=1, limit=1` view. The
third renewal log was rate-limited, but its state still advanced synchronously.

At `t=3.55s`, both the original deadline and the logged-renewal deadline had
passed. Only the rate-limited third renewal remained, and metrics still exposed
one activation, two extensions, one renewal log, one suppressed renewal log,
the latest reject at `t=1.6s`, `active=1`, `applied=1`, and Router fullness
`1/1`. At `last_reject + hold + 1ns`, metrics returned to `active=0`,
`applied=0`, and the ordinary Router limit `50`. This directly closes the
source-level protected/unprotected gap while retaining bounded recovery.

The expanded server, config, and metrics focused suites all exited zero and
`gofmt.diff` was empty. The retained evidence archive is
`tmp/pig-v0106-use1-cb-20260802/green-r3/evidence.tar`, SHA-256
`1a86a803836e90d9321ac49027d0e99a53454656a2eb44714da81b4f335cf593`.
This remains focused source evidence; race, cadence simulation, large-body
shadow feedback, full-matrix, image, disabled-route runtime, and Router-live
consumption gates are still open.

#### v0.10.6 cadence, drain, and bounded large-body focused green r4 — interim evidence

r4 extended the deterministic coverage beyond an immediate metrics scrape. Its
independently verified remote-builder inputs were:

```text
base.tar.gz       b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
source.patch      208fd41fe2a0973efb02507e8112d127a9e38dcd914732490cba544b112a5f99
builder-focused.sh 367688226b3df168b5040d66f5a76a8f96f7878c30765be2aa5e72fd9b2ccb96
```

The focused server command selected all predictive Router-backpressure tests,
including the cadence/jitter simulation, together with HTTP renewal,
request-scoped non-lock, multi-renewal drain/recovery, availability, and shadow
completion feedback cases. The cadence test varied scrape phase and positive and
negative jitter while load rejects arrived once per second. After the first
delivered protected scrape, no later scrape observed an inactive or non-full
capacity gap before `last_reject + hold`; the finite final reject still recovered
within the bound. The drain test included a logged renewal and a rate-limited
renewal, then cleared current/predicted work while the lease remained active and
proved `applied=0`, ordinary Router capacity, timer expiry without new traffic,
and a subsequent cold-safe admission. A bounded 1.6 MB shadow fixture also
completed without a reservation/observation leak, but its request shape was not
yet the exact retained live trailing-whitespace shape.

Server, config, and metrics focused suites and formatting all exited zero. The
downloaded archive is
`tmp/pig-v0106-use1-cb-20260802/green-r4/evidence.tar`, SHA-256
`f2537fec0c796301add8f0bd9887f3bbd245d39f0a395d32bc32720a7da3f69c`.
The archive's internal `SHA256SUMS` was independently rechecked after download.
This still does not constitute a complete race/full matrix, image, deployment,
or live Router-consumption result.

#### v0.10.6 exact live-shape shadow feedback focused green r5 — interim evidence

Fresh inspection of the retained request established that the 1.6 MB live case
was not a 1.6 MB prompt. It was an otherwise ordinary small JSON request followed
by 1,600,000 bytes of trailing whitespace. r5 changed the fixture to that exact
request-body shape and used a more vLLM-like non-stream `/v1/completions` 2xx JSON
response with one choice, `finish_reason=length`, and positive prompt/completion
usage. Its builder inputs were:

```text
base.tar.gz       b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
source.patch      a3791cd77c8acdf042633d053e6e9c3e63cc04d2abce5391d087ee4965b6cdc7
builder-focused.sh 68fe6a3488e825704c4b909e4bb17c0b62c1ecc6d2ba10e922ea0ddf3fcbaa34
```

The fixture returned HTTP 200 with exactly one backend call, one risk decision,
no Router backpressure in shadow, one created and terminated observation, one
accepted-or-rejected input-size feedback delta, and zero remaining manager
reservations. Server, config, metrics, and formatting statuses were all zero.
This disproves a deterministic parser failure caused merely by the trailing
whitespace or the vLLM-like response shape; it does not prove which stage was
missing in the retained deployed runtime.

The downloaded archive is
`tmp/pig-v0106-use1-cb-20260802/green-r5/evidence.tar`, SHA-256
`c9a4613cccb809a809abc3126c3ce8dac71cb2d130dd4159b1b9ffd39d5a43af`.
Its internal `SHA256SUMS` also passed after download.

#### v0.10.6 completion-observer stage telemetry r6/r7 — failed gate then focused green

Because a passing synthetic fixture cannot localize the earlier live anomaly,
the next source slice added four fixed-cardinality lifecycle counters:
`attached`, `claimed`, `usage`, and `terminal`. They add no model, user, request,
prompt, response, token, or bearer labels. The same four cumulative values are
published in predictive metrics and the periodic status line so the next
Router-disabled shadow gate can distinguish attachment, claim/content-type,
usage parsing, and terminal/calibration failures without payload logging.

r6 used source patch
`63b64bbc73a1c7e3bbe195553dff2343fad3332ba583fbeee0b14771656f1f12`
and reusable runner
`e63b042a5baf63d9a4e8295d723762ddc67aa6aa9d42b09436eb436b52a72b3c`.
All three Go suites exited zero, but the formatting gate exited `2` with four
gofmt differences; consequently `overall.status=1`. Its downloaded evidence
archive SHA-256 is
`5e011f90bd86a16da862d15f3a511153f9b2105222a99734e89231817a586b61`.
r6 is retained as a failed gate and is not green evidence.

After applying exactly the reported formatting, r7 used source patch
`57f2927cb2feeb505e31896b300c7085255d0843a41767ae15fa9f93d7b654d8`
with the same runner. The exact live-shape fixture proved observer lifecycle
`attached/claimed/usage/terminal = 1/1/1/1`, one input-size feedback outcome, and
zero remaining reservations. Server, config, metrics, and formatting statuses
all exited zero. The downloaded archive is
`tmp/pig-v0106-use1-cb-20260802/green-r7/evidence.tar`, SHA-256
`ec21f8c785e90f5cf08bef9b9b36dbb3d0f959e965142cf083a12306215a113e`;
its internal `SHA256SUMS` passed independently after download.

r7 is the latest focused source green only. The executable release identity is
still v0.10.5, and no commit, push, tag, image build/publication, Compose change,
CVM deployment, Router enablement, or production request was performed. The
next mandatory gate is source review followed by the complete remote-builder
test/race/simulation/benchmark/static matrix; focused evidence cannot authorize
live traffic.

#### v0.10.6 completion telemetry benchmark r8/r9 — superseded reconstruction then focused green

The first benchmark attempt, r8, correctly remained superseded. Its focused
server/config/metrics and formatting statuses were zero, but the locally new
benchmark file was still untracked and therefore absent from the ordinary
`git diff --binary` reconstruction patch. The remote command consequently
compiled the r7 executable source and printed no benchmark rows. r8 is not used
as performance evidence and did not change any live or release state.

r9 marked the new benchmark path as intent-to-add only so the exact reconstruction
patch contained it. The independently checked inputs were:

```text
base.tar.gz       b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16
source.patch      ded8d4cc752605601b39d13173e06622d49a9ca2a4d445bdee2f9fc1585dd42b
builder-focused.sh e63b042a5baf63d9a4e8295d723762ddc67aa6aa9d42b09436eb436b52a72b3c
```

The reconstructed source included
`BenchmarkPredictiveCompletionObserverStageTelemetry`. Focused server, config,
metrics, and formatting gates all exited zero. The 8-thread remote-builder
contention benchmark ran five one-second samples for the same observer lifecycle
with and without the four fixed counters:

```text
disabled: 12.30 .. 12.61 ns/op, 0 B/op, 0 allocs/op
enabled:  147.0 .. 148.2 ns/op, 0 B/op, 0 allocs/op
delta:    approximately 135 ns/lifecycle under shared-counter contention
```

This is a narrow CPU microbenchmark, not end-to-end service latency or GPU
throughput evidence. It does establish that the diagnostic slice adds no heap
allocation and remains well below the existing microsecond-scale estimator and
admission budget, so a more complex sharded telemetry design is not justified
before live evidence. The downloaded evidence archive is
`tmp/pig-v0106-use1-cb-20260802/green-r9/evidence.tar`, SHA-256
`70340ad5bac2b3bfa6991ae805e1a3e4ba56962337394560f7711c7b671fba16`.
Its internal checksums and both focused and benchmark statuses were independently
verified after download.

#### v0.10.6 implementation review pass 1 — causal publication and observable coherence

The post-implementation review retraced the actual request path rather than the
state helper alone. A typed load-dependent reject is recorded under the adapter
mutex; every in-episode reject advances `latestRejectAt`, monotonically extends
`until` to at least `latestRejectAt + hold`, and updates fixed cumulative renewal
telemetry before unlocking. The synchronous transition callback runs after the
unlock, so it can scrape the committed snapshot without deadlock, but still
returns before the proxy increments enforced-reject/429 counters or writes the
response. The r3 HTTP callback test directly exercises this ordering.

The metrics review confirmed that the same immutable adapter snapshot feeds both
the predictive diagnostics and the exact Router-consumed dynamic fields. While
load protection is active and current/predicted running is non-zero, it publishes
`active=1`, `applied=1`, effective running at least one, and effective global
limit no greater than effective running, while keeping raw running, raw global
limit, waiting, and the PIG-local admission limit separate. r3 proves this across
the old expiry deadline, and r4 proves it across Router scrape phases/jitter.
Activation and renewal logs carry only fixed enums, counters, sizes, and times;
the durable last-reject and periodic status fields expose any rate-limited renewal
tail. No request, model, user, prompt, body, bearer, or token identifier enters
the publication state.

The review found no remaining causal publication gap in the current source.
Request-scoped oversized/unsupported inputs intentionally do not suppress an
idle node, and load projection stops applying immediately at current and
predicted running zero. The next review pass remains contingent on complete race,
lifecycle, simulation, and performance evidence; this source review alone is not
a release or deployment gate.

#### v0.10.6 pre-version full matrix r10/r11 — harness failure then intended stale-test red

r10 used base archive
`b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16`,
source patch
`e557f79f55a059a78d74170ff457646b579dcc9a41a3131b7e782e0b848b7661`,
inner runner
`d8dc9bb6daa0ec665dfaac615288d7849a3deb8d9a60b2e7dc5498fc38c313fd`,
and host runner
`b94d49ddc4ae0cb813fc20b0c92940091c0837b8d01b65fae71d063d63853b9d`.
It did not enter any Go or image gate: the host runner incorrectly used the
builder-container path `/persistent` as if it were writable on the DStack host.
Host cache-directory creation failed immediately because that host path is
read-only. The failure archive SHA-256 is
`a01d37dc29820d9cfad86ae58932c89a1d56da9f8777b9facecfa7661c5ed7f2`.
r10 is a harness failure only and proves no product red or green.

r11 preserved the same source inputs and inner runner, changed only the host
runner to SHA-256
`ed13b9a1ae068cf4a9daae3599ac930bee37294f4273af5c1671a446f88021c6`,
and used the real host persistent path
`/var/volatile/dstack/persistent`. Reconstruction, formatting, the static
model-agnostic/no-cache/TTFT-off contract, and `go vet ./...` all exited zero.
The complete `go test ./...` gate then exited one for exactly one stale test:

```text
TestApproximatePredictiveEnforcePublishesBoundedRouterBackpressure
Extensions=1
Activations=1
renewal callback count observed=2, old assertion expected=1
```

The test named every `OnRouterBackpressure` callback an "activation" and still
encoded the superseded v0.10.5 expectation that an in-episode load reject emits
no callback. The new callback was the required bounded `event=renewed`; the
actual activation count correctly remained one and the renewed deadline was
present. The r11 archive SHA-256 is
`3a99ee0b38b2084d7144f9643571fcc11f6fc576b2f2cb449c48af57852440a7`.
r11 is a valid test-suite red against obsolete test semantics, not a reason to
remove renewal from the product.

The correction changes only that test: it records transition kinds and requires
the first callback to be `activated`, the second to be `renewed`, cumulative
`Activations=1`, and `Extensions=1`. No production path changed. A new focused
builder run must pass this exact corrected test before a fresh full matrix with a
new run identity; r10 or r11 directories and evidence are not reused.

#### v0.10.6 corrected renewal test focused green r12 — accepted interim evidence

r12 reconstructed the corrected unversioned candidate from baseline archive
SHA-256
`b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16`
and source patch SHA-256
`c72b97eca8b551efe61f5319f9487528e701b50567c7ea5c0984b7b2a5f4d71c`.
The generic focused runner was
`e63b042a5baf63d9a4e8295d723762ddc67aa6aa9d42b09436eb436b52a72b3c`
and ran in
`docker.io/library/golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`
on builder CVM `vllm-v024-patch-builder-use1`. Server, config, metrics,
formatting, and generic overall statuses were all zero.

Because the generic focused regex did not select the corrected stale test, the
same reconstructed source then independently ran:

```text
go test ./internal/app/server \
  -run TestApproximatePredictiveEnforcePublishesBoundedRouterBackpressure \
  -count=1
```

That exact test exited zero and proved one `activated` callback, one `renewed`
callback, cumulative `Activations=1`, and `Extensions=1` under the corrected
test semantics. The first evidence-control connection timed out after the
remote test had completed, and its status writer encoded a literal `0n`; that
malformed status artifact was rejected. The final accepted archive rewrote only
the evidence status as bytes `30 0a`, regenerated the internal checksums, and
retained the successful test log. Its downloaded archive is
`tmp/pig-v0106-use1-cb-20260802/green-r12/evidence.tar`, SHA-256
`2c3e6429a7ae461206e5ca9b154219f2558a04d3be3db28c2d746749f7d6a868`.
All internal checksums, `exact-renewal-test.status=0`, and `overall.status=0`
were independently verified after download.

The retained r10 harness-failure archive and r11 stale-test-red archive were
also downloaded without modification and their outer and internal SHA-256
manifests were independently verified. r12 is focused evidence only: it does
not replace the required fresh pre-version full matrix, complete race and
simulation evidence, versioned-source matrix, image provenance, or any live
gate.

#### v0.10.6 pre-version full matrix r13 — complete green

r13 used a fresh, non-reused run identity and reconstructed baseline archive
SHA-256
`b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16`
with source patch SHA-256
`d6dd70b5262fc40642d3123efba7ecd92d3d465ddc6e371e7cbe825c9bbc9d05`.
The inner runner was
`d8dc9bb6daa0ec665dfaac615288d7849a3deb8d9a60b2e7dc5498fc38c313fd`
and the corrected host runner was
`777f109ee585dc1682e671582eb08abf2db5249ac193772d93b4531cd70cdd5f`.
Both runner syntax checks and all remote input hashes passed before execution.

The matrix ran on builder CVM `vllm-v024-patch-builder-use1` with Go 1.24.5,
`linux/amd64`, `CGO_ENABLED=1`, and toolchain image
`docker.io/library/golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`.
Every status was zero for reconstruction, formatting, the
model-agnostic/no-cache/TTFT-off source contract, `go vet ./...`,
`go test ./... -count=1`, targeted race, full race, `go build ./...`,
deterministic KV and KV-performance simulations, verbose and JSON goodput,
candidate/baseline benchmarks in both execution orders, builder-local image
build, production image contract, image inspect, source overall, and final
overall.

Across the 21-scenario goodput suite, model-agnostic approximate QoS completed
51 SLO-compliant requests and 44,704 completion tokens, versus 37 and 39,840
for the current threshold, 31 and 37,024 for exact-token KV-only, and 33 and
37,536 for v0.9.0 KV-only. It recorded zero TPS, TPOT, KV-hard,
preemption-proxy, false-accept, and reservation-leak violations, with seven
false denies. Four TTFT violations remained diagnostic only and did not alter
admission. The KV performance gate measured estimator 64 KiB p95 at 1.911 us,
estimator 2 MiB p99 at 99.14 us, and shadow decision p99 at 4.276 us.

The new four-stage completion-observer telemetry remained zero-allocation. Its
eight-thread shared-atomic samples were 132.6-146.3 ns per lifecycle versus
12.18-12.40 ns with counters disabled, a narrow approximately 120-134 ns CPU
delta rather than a serving-latency or GPU-throughput claim. Existing admission
and predictive-runtime benchmark samples remained in the same ranges in both
candidate/baseline execution orders.

The downloaded evidence archive is
`tmp/pig-v0106-use1-cb-20260802/full-r13-preversion/evidence.tar`, SHA-256
`fbeecf99774b1ce8cbf1bda9b9c1f560aaf56b2ac1f6d33b67a5aa525c3c8429`.
Its relative-path internal manifest and every status were independently
verified after download. This is pre-version evidence for executable identity
v0.10.5 and cannot be used as the final v0.10.6 image or release matrix.

#### v0.10.6 implementation review pass 2 — safety, lifecycle, SOLID, and efficiency

The second post-implementation review rechecked the complete production diff
and the real HTTP transaction. The adapter mutex serializes durable last-reject
state, monotonic `latestRejectAt`, renewed `until`, activation/extension
counters, and the immutable telemetry snapshot. Concurrent decisions whose
captured clocks complete out of order cannot move the deadline backward because
both latest-reject and deadline updates are maxima. Callback execution remains
outside that mutex to avoid scrape/log deadlock, yet each requesting goroutine
waits for its synchronous callback before incrementing the enforced-reject
counter or writing 429. The targeted and full race gates plus concurrent
reject/scrape integration test remained green.

Lifecycle review confirmed that only typed load-dependent rejects with existing
predicted work renew the load lease. Request-scoped oversized, unsupported, or
intrinsically unsafe requests retain durable low-cardinality diagnostics but do
not reduce idle Router capacity. Health-driven availability protection remains
separate. At current and predicted running zero, `applied` becomes zero
immediately even if the lease is still active; the lease then expires from the
final reject without new traffic, and a later cold-safe request progresses.
Reservations, shadow observations, deferred outcomes, cancellations, timeouts,
upstream failures, reset, close, and release/terminal races left no leak in the
complete matrix.

Responsibility boundaries remain narrow: the estimator/calibrator owns bounded
approximate size learning, the scheduler/coordinator owns the atomic
counterfactual decision and reservation, the adapter owns typed protection
state, the capacity projector maps one snapshot into existing Router-consumed
metrics, and observability renders fixed-cardinality logs/status/metrics. No
Router or vLLM source changed, no cache inspection was introduced, and
production Go source contains no model-family branch or tokenizer asset.
Renewal is O(1), stores no request-keyed or payload data, and rate-limits logs;
the only new normal-forward hot-path work is four optional atomic telemetry
increments with the measured zero-allocation cost above.

No executable correction was required by this review. The only correction was
to the plan's top-level authority, which still described TTFT as a rejecting
constraint despite the later v0.10.6 section. The top-level objective,
transaction, feedback, and TPS-first steps now state directly that TTFT is
observation-only. Because this is a plan-only edit, it does not change the r13
executable tree, but it will be included in the later versioned-source archive.

#### v0.10.6 executable identity unification — complete, final matrix pending

After r13 and implementation review pass 2, the current release surface was
advanced without moving or rewriting v0.10.5:

- the runtime constant is `PIG-v0.10.6`;
- the OCI image label is `0.10.6`;
- README, current Compose example, ADVANCED predictive configuration, and
  OBSERVABILITY examples identify v0.10.6;
- ADVANCED now documents
  `PREDICTIVE_ROUTER_BACKPRESSURE_HOLD=5s` with the validated `2s..30s` range,
  independence from `DYNAMIC_POLL_INTERVAL_MS`, and latest-reject renewal;
- OBSERVABILITY now documents activation/renewal logs, durable lease metrics,
  Router-consumed dynamic fields, completion-observer stages, and the idle
  `active=1,applied=0` escape hatch;
- the goodput simulator comment is version-independent while retaining TTFT as
  diagnostic-only behavior.

Historical v0.10.5 plan evidence, tag, registry digest, and deployment state
remain unchanged. Static diff checks found no current v0.10.5 identity outside
the mixed-history plan, and no stale fixed-expiry or backend-poll-derived hold
description remains in current README, ADVANCED, or OBSERVABILITY. No Go test,
race, simulation, benchmark, or image command was run on local Windows. A fresh
versioned full matrix against the exact v0.10.6 source is mandatory and may not
inherit r13's executable/image status.

#### v0.10.6 final versioned full matrix r14 — complete green

r14 used a fresh versioned run identity with baseline archive SHA-256
`b85204d484e93365bfa65f47d1ceab2335e6bf9ca118405db46c5000596d6e16`,
exact v0.10.6 source patch SHA-256
`4deb57c265edc79281314ac5bb7ec1486be795a6f6b5934231cb8d4c4c526b4f`,
inner runner
`d8dc9bb6daa0ec665dfaac615288d7849a3deb8d9a60b2e7dc5498fc38c313fd`,
and host runner
`b2cf12f8d7af2dad385f7aec5d4e47d77198efa09210ddf5ab881ad2b211b68d`.
Remote hashes and both runner syntax checks passed before execution.

On the same pinned Go 1.24.5 Linux builder environment, every r14 status was
zero for reconstruction, formatting, model-agnostic/no-cache/TTFT-off source
contract, vet, all Go tests, targeted race, full race, build, deterministic KV
and performance simulations, verbose/JSON goodput, both candidate/baseline
benchmark orders, builder-local image build, image contract, image inspect,
source overall, and final overall. The production image contract returned:

```text
PIG_PRODUCTION_IMAGE_CONTRACT_OK image=pig-v0106-versioned-r14:local version=0.10.6
```

The downloaded archive is
`tmp/pig-v0106-use1-cb-20260802/full-r14-versioned/evidence.tar`, SHA-256
`3c53694640943bf3a72140223e3082518a77705deb7678f0d5ade37b5fe7573b`.
Its relative-path internal manifest and all statuses were independently
verified. The goodput and performance results remain the r13 values because the
intervening executable changes were release identity and a
version-independent simulator comment; r14 nevertheless reran every gate and
did not inherit those results.

#### v0.10.6 implementation/release review pass 3 — evidence, identity, and scope

The final pre-release review distinguishes the intended r1 non-renewal red,
focused green r2-r9, r10 host-harness failure, r11 stale-test red, corrected
focused r12, complete pre-version r13, and complete versioned r14. No failed or
superseded artifact is counted as green. Exact inputs, builder identity,
toolchain digest, statuses, simulations, benchmarks, image contract, outer
archive hashes, and internal manifests are retained locally.

Current identity review found runtime `PIG-v0.10.6`, OCI label `0.10.6`, and
current README/ADVANCED/OBSERVABILITY release surfaces at v0.10.6. No v0.10.5
identity remains outside mixed historical plan evidence, no local or remote
`v0.10.6` tag exists yet, and `pig-origin` is the authoritative
`Phala-Network/phala-inference-guard` remote. Diff and bounded secret scans were
clean. The r14-tested executable diff excluding this evidence plan hashes as Git
blob `c4dd85e69c25f0fe7f94f8d40e582c75f9d5aaf0`; this post-r14 section changes
only the plan and must leave that hash unchanged at the commit gate.

r14 authorizes the next source-release operations only: commit the reviewed
tree, push the existing branch to `pig-origin`, create and push a new annotated
`v0.10.6` tag, build/publish the immutable tag image from that commit, and
independently pull-verify its OCI identity and binary equivalence. It does not
prove a registry image, Compose integration, CVM deployment, live readiness,
Router consumption, Router enablement, or the 30-minute traffic observation.
`use1-cb` remains Router-disabled until all disabled-route shadow and enforce
gates pass.

#### v0.10.6 source release and immutable registry evidence — complete

The reviewed source was committed and pushed to the authoritative `pig-origin`
remote:

```text
branch:        codex/pig-v0.10.0-model-agnostic
commit:        5ca048d6905e7a054d5290199488471bcf02723a
annotated tag: v0.10.6
tag object:    12ae853f65c6c509d286f074c4f02d662c9f7c67
```

Remote tag dereference resolves to the same source commit. The tag-triggered
GitHub workflow `Publish Image` run `30771150853` completed successfully at
[GitHub Actions](https://github.com/Phala-Network/phala-inference-guard/actions/runs/30771150853):
checkout, GHCR login, v0.10.6 production image contract, build, and push all
reported success.

The independently pulled immutable registry image is:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:96ae0b9ffbae926932ce62b0bc01702a40c9740cf559fce5b96946f803417848
```

The binary extracted from the r14 builder-local tested image and the binary
extracted after an independent pull by that registry digest are byte-identical:

```text
aca9e8f65b3cfae96ee1609ad6bd055a4b1508eb83c04230b19ab6e13b8568f0
```

The digest-pinned image independently passed the production image contract with
OCI version `0.10.6`. Registry verification evidence is
`tmp/pig-v0106-use1-cb-20260802/registry-verify-r15/evidence.tar`, SHA-256
`da395cfd1195b506ed2c224d2da9556d501609d16b663d28f2aa95920d316428`;
every internal checksum and status was verified after download.

This completes source, tag, workflow publication, immutable digest, and binary
equivalence only. It still does not prove Compose integration, deployment,
runtime readiness, Router-disabled shadow/enforce behavior, Router consumption,
route enablement, or production traffic behavior. No CVM or Router mutation has
occurred in this v0.10.6 release phase yet.

#### v0.10.6 Router-disabled shadow live gate — stopped by interim-status accounting defect

Fresh read-only preflight at `2026-08-02T23:08:49Z`, repeated immediately
before deployment at `2026-08-02T23:11:40Z`, proved CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` was `running`,
`in_progress=false`, idle, and still Router-disabled. The complete Router
enabled set was exactly `use1-4c`; `use1-cb` and the unrelated `use1-9b`
remained disabled. The live rollback Compose SHA-256 was
`6644b9f2c148ddfa6b74b7f3b6fbc8c1abecb2965fa93245b201f6a76d24983f`.
Authenticated `/v1/models`, `/pig/metrics`, `/v1/metrics`, and attestation were
200, unauthenticated metrics were 401, and the target reported zero running,
waiting, KV usage, and preemptions.

The shadow candidate was regenerated from that exact live Compose and changed
only the PIG immutable digest from v0.10.5 to the independently verified
v0.10.6 digest plus the new explicit
`PREDICTIVE_ROUTER_BACKPRESSURE_HOLD=5s`. `PREDICTIVE_ADMISSION_MODE=shadow`
and `DYNAMIC_TTFT_ENABLED=false` remained unchanged. Candidate SHA-256 was
`f94d85c3d977fae346caa0f0c03544f165f8e4fea7d440efd931ca1a1cbfeeb2`.
Deployment supplied no `.env`, exited zero, and the live Compose hash exactly
matched the candidate. The immutable PIG digest and container image ID were
verified independently. vLLM restarted cleanly and `/v1/models` recovered from
503 to 200 after approximately 4 minutes 44 seconds; no OOM, Xid, fatal, or
restart loop was observed. Startup logs proved `PIG-v0.10.6`, shadow mode,
legacy and predictive TTFT protection disabled, TTFT observation enabled, and
the 5-second Router publication hold. Router state and enabled set did not
change.

The ordinary shadow gates passed before the stopping defect:

- normal chat, streaming with usage, tool call, strict structured output, and
  CJK requests all returned valid 200 responses;
- 21 sparse learning requests all progressed, reached the learned approximate
  input-size source after two maturity requests, preserved learning through a
  deliberately low-ratio sample, and ended with zero reservations,
  observations, deferred outcomes, running, waiting, and preemptions;
- all 21 incremental estimator and decision samples were at or below 0.25 ms;
- Router backpressure remained inactive and unapplied throughout shadow.

The required 1.6 MiB request-scoped shadow-risk gate then exposed a new
production defect. `curl` automatically sent `Expect: 100-continue`. vLLM
completed the request and the client received final HTTP 200; vLLM success and
all four completion-observer stages each increased by one. PIG nevertheless
recorded the request in the `1xx` response-status class, increased
`pig_predictive_tps_outcomes_total{result="rejected"}`, and did not add the
completion's input-size or scheduler feedback. The shadow observation was
created and terminated but not qualified. Router backpressure correctly
remained inactive/unapplied and all live state returned to zero, so the target
did not self-lock, but the observation/metrics disagreement violates the
feedback contract and prevents mature learning for affected large requests.

The root cause is `infra/http.StatusRecorder`: a backend provisional 100
Continue forwarded by `ReverseProxy` was stored as the request's final status;
the later final 200 was ignored. This also explains the exact live discrepancy
between successful client/vLLM behavior and PIG metrics. v0.10.6 is therefore
not eligible for Router-disabled enforce or Router enablement. The target
remains Router-disabled in v0.10.6 shadow while the repair is built and tested.

#### v0.10.7 interim-response repair plan — active

v0.10.7 will keep forwarding informational 100-199 responses to the client but
exclude them from final status and first-final-response accounting. HTTP 101
remains final because it switches protocols. The first non-informational
status, or the implicit 200 created by a body/flush, remains authoritative.
This is a narrow transport-accounting fix: it does not change admission policy,
the approximate estimator, TTFT observation-only semantics, Router source,
vLLM source, cache behavior, or model independence.

The mandatory evidence sequence is:

1. run a test-only patch against the v0.10.6 release baseline and prove that a
   recorder receiving `100`, `103`, then `200` incorrectly returns `100`;
2. apply the narrow recorder fix and prove the same focused test green, plus
   preserve 101 as a final status;
3. add a proxy/predictive integration gate proving a final 200 after backend
   informational response increments the 2xx class and completion/input-size
   feedback exactly once rather than the 1xx/rejected path;
4. rerun formatting, vet, all tests, targeted and full race, build,
   deterministic simulations, goodput, both-order benchmarks, builder-local
   image and production image contract on the remote builder only;
5. repeat the three implementation reviews for causality/correctness,
   lifecycle/SOLID/efficiency, and evidence/release scope;
6. unify runtime, OCI, and current documentation identity as v0.10.7, rerun the
   exact versioned full matrix, commit/push/tag, publish, independently pull by
   digest, and prove binary equivalence;
7. regenerate rollback and shadow candidates from fresh live truth, repeat all
   Router-disabled shadow gates including an explicit
   `Expect: 100-continue` large request, then repeat Router-disabled enforce
   publication/renewal/recovery gates;
8. only after every gate passes may `use1-cb` alone be enabled and begin a new
   30-minute real-traffic observation window.

Any mismatch among client status, vLLM success, PIG response class, predictive
terminal cause, feedback counters, log state, Router-consumed capacity, or
terminal-zero state stops the release and restarts the repair loop.

#### v0.10.7 interim-response implementation and pre-version evidence

The implementation is deliberately transport-local. `StatusRecorder` forwards
informational `100`-`199` responses but does not retain them as final status or
first-final-response time; `101 Switching Protocols` remains final. The first
later non-informational status, or implicit `200` from body/flush, remains the
single terminal status consumed by proxy accounting and predictive feedback.
No admission constraint, estimator, learner, Router capacity rule, model
identity, cache behavior, TTFT policy, Router source, or vLLM source changed.

The first remote r1 attempt proved the recorder red but its integration fixture
was missing the `io` import, so it is retained only as invalid harness evidence.
Clean r2 then reproduced both intended v0.10.6 failures: the recorder returned
`100` after `100 -> 103 -> 200`, and the complete proxy path recorded one
rejected TPS outcome for a client/backend-successful informational response.
Its archive SHA-256 is
`d381177f23d22b1eb056774d1ae42030ef7ebc331ed5b06a7bc521fef2d5a527`.
The same r2 green phase stopped at formatting and is not green evidence.

Focused r3 applied only the narrow recorder fix after reproducing both reds in
the same reconstruction. Formatting, the recorder/proxy focused tests, focused
race, affected package tests, and vet all exited zero. Its archive SHA-256 is
`fca7c802a986ec89992968e54dc30f1fd030a9884596fadfc3c5c707d634c427`;
all files listed by the internal `SHA256SUMS` were independently verified.

The full pre-version r4 reconstructed the v0.10.6 release baseline
`b62e56db2829b7b6a9602225f1ec95c07f1feb126e57e47cf656e30aeff7e445`
with source patch
`f26a793ca1772699dd727c4d466f8adcf3f871d5a21f8978a235ac265691aaf9`
inside the immutable Go 1.24.5 toolchain
`docker.io/library/golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`.
Reconstruction, gofmt, source contract, vet, all tests, targeted race, full
race, build, deterministic KV/performance, verbose and JSON goodput, both
candidate/baseline benchmark orders, builder-local image build, production
image contract, and image inspection all exited zero. The downloaded evidence
archive SHA-256 is
`b14e6eb08b795734c0760cdeab3efd00b5a4b152172602ee278906211c0f3b92`;
21 status files were zero and all 47 internally hashed files matched.

This pre-version candidate intentionally still reported runtime/OCI `0.10.6`.
Its builder-local image was
`sha256:5acc92ea471c15ca124f8e6b0f9688e0c8ff05bce973b1ed70169b59e9438e0c`.
Estimator performance remained bounded at 64 KiB p95 `1.906 us`, 2 MiB p99
`118.512 us`, and predictive decision p99 `1.982 us`. The aggregate deterministic
goodput comparison remained predictive `44,704` completion tokens with zero
TPS, TPOT, KV-hard, preemption-proxy, false-accept, or reservation-leak events,
versus KV-only `37,536` with 32 TPS, 32 TPOT, one KV-hard, one
preemption-proxy, and 16 false-accept events. Four TTFT violations remained
diagnostic-only; predictive had seven false denies.

Three post-r4 reviews were completed and revised as follows:

1. Causality and correctness confirmed that provisional responses cannot
   determine final status, terminal cause, status class, or learning. The tests
   were strengthened to prove provisional responses do not establish
   first-final-response timing. The final success must produce exactly one TPS
   outcome: `missing`, not `rejected`, because this non-stream fixture exposes
   no backend timing or semantic TTFT from which a TPS target could be derived.
   It must also produce exactly one safely rejected input-size ratio outcome;
   no target quality is fabricated from absent timing or an extreme compressed
   whitespace ratio.
2. Lifecycle, SOLID, and efficiency review found the responsibility remains in
   the HTTP recorder, preserves the `101` boundary, introduces only a constant
   branch on response headers, and adds no model/cache/tokenizer/Router
   coupling. The full race result was clean.
3. Evidence and release review accepted r2 only as red, r3 as focused green,
   and r4 as a pre-version full matrix. Because the strengthened tests and
   version identity postdate r4, no r4 result is inherited as final v0.10.7
   evidence. A fresh exact versioned full matrix is mandatory.

Runtime, OCI, README/Compose example, advanced configuration, and current
observability identity are now unified at v0.10.7. Historical v0.10.6 evidence
above remains unchanged. No source push, tag, registry image, new Compose,
deployment, enforce request, Router mutation, or Router enablement has occurred
at this point.

#### v0.10.7 exact versioned r5/r6 matrix evidence

The first r5 invocation stopped before reconstruction because the host runner's
expected inner-runner hash omitted one hexadecimal `b`; the printed strings
looked similar, but byte/length audit showed 63 expected characters versus the
64-character SHA-256. The corrected host runner then started a fresh r5 run.
Reconstruction, source contract, gofmt, and vet passed, but full tests correctly
failed the newly strengthened informational-response assertion. The fixture has
no backend ITL/generation time and no semantic TTFT, so the implementation
truthfully produced one TPS `missing` outcome rather than the test's incorrect
requirement for a backend/local learned target. It still produced zero TPS
`rejected` outcomes and one safely rejected input-size ratio outcome. The r5
failure archive is retained at SHA-256
`0707a76b96ed68098da955a5a961ae55a73f53922597bd77b42bac84cf53c416`;
it is not green evidence.

The assertion and the preceding review wording were corrected without changing
runtime source: the explicit contract is TPS backend/local `0/0`, missing `1`,
rejected `0`, input-size accepted/rejected `0/1`, final status `200`, response
classes `1xx/2xx = 0/1`, and completion observer `1/1/1/1`. This avoids both
misclassifying a successful response and fabricating a TPS learning target.

Fresh versioned r6 used:

```text
v0.10.6 base archive:
b62e56db2829b7b6a9602225f1ec95c07f1feb126e57e47cf656e30aeff7e445

exact v0.10.7 source patch:
08e3d5bdb47fb8ec899518bf5b97d6e1598be0832faa9a11968a9070498bc751

inner runner:
0c29db4014199bbba1e298dc110886cf5ee3a28e67e76cad23f4977c0db84197

host runner:
b0990f207e9549e0e386d3351993d7756be76b62a3bcdebbfc03f8f9ca758607
```

All 21 status files were zero: reconstruction/apply, gofmt, the
model-agnostic/no-cache/TTFT-off plus exact-version source contract, vet, all
tests, targeted race, full race, build, deterministic KV/performance, verbose
and JSON goodput, both-order candidate/baseline benchmarks, builder-local image
build, production image contract, and image inspection. The independently
downloaded evidence archive SHA-256 is
`fd4326e8267a71c4693186b3aecbaf294ea6a0f5b6ef90653f0e9b1690960857`;
all 47 internal `SHA256SUMS` entries matched.

The builder-local versioned image is
`sha256:6f996145efe76a2c299f960eb619831f7fc89d9fa24240e03333d71639894674`
with OCI label `0.10.7`; its extracted binary SHA-256 is
`7dcac5a23c309851d35c2dd1900028e28718c7068e6f5a443f7ca7955cdba911`.
The production image contract reported `version=0.10.7`. Performance was 64 KiB
estimator p95 `1.912 us`, 2 MiB estimator p99 `129.267 us`, and predictive
decision p99 `1.231 us`. Candidate and baseline both-order benchmarks retained
the same allocation counts and overlapping timing noise; the recorder fix adds
no admission hot-path work.

Deterministic goodput remained predictive `44,704`, current-policy `39,840`,
and KV-only `37,536` completion tokens. Predictive had zero TPS, TPOT, KV-hard,
preemption-proxy, false-accept, or reservation-leak events and seven false
denies. The four TTFT violations were observation-only diagnostics and did not
alter admission. This is exact source and builder-local image evidence only;
registry publication, digest pull, binary equivalence, live deployment, and
Router behavior remain unverified at this point.

#### v0.10.7 source and registry release evidence

The reviewed source was committed as
`c1ff24e03024a31bce1df309ace483ec663042ff`, pushed only to
`pig-origin/codex/pig-v0.10.0-model-agnostic`, and annotated tag `v0.10.7`
(tag object `a078142196fc0edbc2d3621c26f503586d9b63c0`) was pushed to the
same `pig-origin`. GitHub `Publish Image` run `30773617149` completed with
`success` for that exact commit and tag.

An independent builder pull of
`ghcr.io/phala-network/phala-inference-guard:v0.10.7` resolved the immutable
registry reference:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:112ad16bee5498c0b5fd233538047075ce360d135bd14587b57cb1f5ce684d5d
```

The pulled amd64 image ID is
`sha256:89286d0b54c53de4c5934386d39626a2741adf1fa88e4ce0f0b949b8292ae2a9`,
its OCI label is `0.10.7`, and the production image contract passed. The
registry binary SHA-256 is
`7dcac5a23c309851d35c2dd1900028e28718c7068e6f5a443f7ca7955cdba911`;
it is byte-identical by both SHA-256 and `cmp` to the r6 builder-local binary.
The registry image is therefore eligible to generate a fresh Router-disabled
shadow candidate, but it has not yet been put into any live Compose.

#### v0.10.7 Router-disabled shadow deployment and gates

The immutable registry digest above was subsequently deployed only to the
authorized CVM `a0f0bfb3-e46f-4b22-814e-24872f251193` while `use1-cb` remained
Router-disabled. The deployment used Compose-only update without `.env`. The
predeploy rollback Compose SHA-256 was
`f94d85c3d977fae346caa0f0c03544f165f8e4fea7d440efd931ca1a1cbfeeb2`; the live
v0.10.7 shadow Compose SHA-256 is
`e8099a2793e65694b7cc753feda6f4317939e1491ebc408774c9397abd9f410a`.
The only Compose text change was the immutable PIG digest. Runtime startup
reported `PIG-v0.10.7`, `predictive_admission=shadow`,
`dynamic_ttft_protect=false`, `predictive_ttft_protect=false`, and a five-second
predictive Router-backpressure hold. TTFT therefore remains observation-only
and is not a protection or capacity signal.

Fresh read-only state at `2026-08-03T00:30:00.1431482Z` and again after all
shadow gates at `2026-08-03T00:36:23.6122122Z` proved the CVM, PIG, and vLLM
containers running with no platform operation in progress; authenticated
models, both metrics paths, and attestation returned HTTP 200; unauthenticated
metrics returned 401. Router digest remained
`sha256:8969f268ba986f106f9085ffe64f48db9199c5527d0d4dd83c92b44b0a2499c1`,
the enabled set remained exactly `use1-4c`, target processed remained `237910`,
and `use1-cb` remained disabled. Its Router-side `pig_ok=false/stale=true` is the
expected not-collected state for a disabled route and is not a reason to mutate
Router configuration.

The Router-disabled protocol gate returned HTTP 200 for all five cases: normal
chat, streaming with terminal usage, required tool call, strict JSON schema,
and CJK. The low-flow gate then completed 21 sparse requests with 21 HTTP 200
responses. Input-size estimation reached learned mode after the second maturity
request; final counters recorded 20 learned estimates, 22 accepted samples,
four safely rejected samples, zero invalidations, and 22 bounded stored samples.
The deliberate safe low-ratio sample did not invalidate or erase mature state.
Prediction and estimator histograms both recorded 21/21 observations at or
below 0.25 ms. Risk, unknown, enforced reject, preemption, and lifecycle-failure
deltas were zero; intake stayed open and reservations, shadow observations,
deferred outcomes, backend running, and backend waiting all returned to zero.
This passes the sparse-progress, no-false-lock, no-self-lock, no-sticky-zero,
latency, and terminal-convergence shadow gates.

The first explicit `Expect: 100-continue` harness invocation is retained only
as a harness failure. PowerShell `Start-Process` split the header value into a
second curl URL, so curl completed one real 200 request and then reported DNS
failure for `100-continue`. It is not product red or green evidence. The
corrected harness placed the explicit header in a temporary header file that
was scrubbed after execution and started from a new metrics baseline.

The corrected 1,600,119-byte request proved a provisional HTTP 100 followed by
client-final HTTP 200. vLLM success increased by one. PIG final 2xx increased by
one and 1xx by zero; predictive risk increased by one while enforced rejects
remained unchanged in shadow. Input-size feedback terminated exactly once, TPS
rejected increased by zero, completion-observer
`attached/claimed/usage/terminal` each increased by one, and shadow-observation
created/terminated each increased by one. `/pig/metrics` and `/v1/metrics`
published identical PIG-local values after terminal convergence. Router
backpressure stayed inactive and unapplied, raw/effective Router capacity stayed
equal, preemptions did not increase, and every reservation, observation,
deferred outcome, running, and waiting gauge returned to zero. This closes the
v0.10.7 live interim-response shadow gate.

#### v0.10.7 Router-disabled enforce publication gate — mandatory next step

The user has reiterated that returning a local 429 is not a complete protection
loop. Before any Router enablement, a load- or availability-scoped protection
must be committed and observable before the client receives 429: the typed
last reject, activation or renewal state, structured event log, both metrics
paths, and the exact effective running/global-limit fields consumed by Router
must describe one coherent 100%-full capacity view. Otherwise Router continues
to feed the node and the protection is ineffective.

No further source change is justified solely by the old v0.10.4 oscillation:
v0.10.6 changed the lease to renew from the latest load reject, and current
source tests cover the former gap. That source evidence is not live proof.
Generate a fresh candidate from the just-queried v0.10.7 shadow Compose and
change exactly `PREDICTIVE_ADMISSION_MODE=shadow` to `enforce`; preserve the
same immutable digest, `DYNAMIC_TTFT_ENABLED=false`, the five-second hold, all
other Compose bytes, and the Router-disabled state. Then require all of the
following from the deployed runtime:

1. A request-scoped 429 publishes its fixed reason, scope, counter, and bounded
   payload-free log but leaves Router backpressure `active=0`, `applied=0`, raw
   and effective capacity equal, and a subsequent cold-safe request able to
   enter. It must not lock an idle node.
2. A load-scoped 429 makes zero upstream calls and, before returning the 429,
   publishes the same reason/source/scope/activation in the reject record,
   `event=activated`, `event=router_capacity_applied`, `/pig/metrics`, and
   `/v1/metrics`. Both paths must expose `active=1`, `applied=1`, effective
   running greater than zero, and effective global limit no greater than
   effective running so the Router parser computes at least 100% fullness.
3. Repeated load-scoped rejects renew one continuous episode from the latest
   reject. Every decision remains pre-forward, cumulative extension counters
   advance, renewal logs are bounded rather than absent, and no Router scrape
   phase or jitter may observe the old protected/unprotected oscillation while
   current or predicted work remains non-zero.
4. When current and predicted work both reach zero, `applied` returns to zero
   immediately and raw capacity is restored even if the finite lease is still
   active. With no new traffic, `active` then expires. A later cold-safe request
   must pass; sticky zero, timer-driven self-lock, and low-flow lock are release
   blockers.
5. Availability protection must publish the same coherent full-capacity
   sentinel and recover from current health. Unsupported input, an oversized
   individual request, client cancellation, or TTFT observation must never be
   reclassified as node-wide availability or load pressure.
6. Status logs and metrics must expose the durable latest reject, activation,
   extension/renewal, current active/applied state, and raw/effective capacity.
   No model, prompt, user, request ID, body, response, bearer, or token content
   may appear. TTFT metrics may change only as observation; they must never
   change the admission result, Router-backpressure state, or Router capacity.

Any missing log, disagreement between the two metrics paths, Router-consumed
fullness below 100% during a binding load/availability rejection, upstream
counter increase on a 429, renewal gap, premature recovery, delayed drain,
sticky protection, preemption, or terminal leak stops the live sequence. In
that case keep `use1-cb` disabled, retain the exact evidence, implement a new
version, and repeat builder, shadow, and enforce gates. Only a complete green
enforce result can authorize enabling `use1-cb` and starting the separately
timed 30-minute real-traffic canary.

#### v0.10.7 real-traffic result and v0.10.8 throughput-learning correction — active 2026-08-03

This section supersedes the preceding “mandatory next step”. The v0.10.7
Router-disabled enforce publication gate passed, and the separately timed
real-traffic canary completed its strict interval, but the release failed the
product throughput objective. Safety and publication correctness do not make a
release acceptable when it protects TPS far above the configured service band
and leaves most usable capacity idle.

The target was enabled for the strict UTC window `2026-08-03T01:44:28Z` through
`2026-08-03T02:14:28Z`. The supervisor completed normally, and a fresh capture
at `02:15:43Z` covered the final interval. The canary recorded 1,222 prediction
attempts, 746 fits, 444 forecast risks, 32 unknown requests, 476 enforced
rejects, and 716 vLLM successes. There were zero preemptions, zero sampled
waiting, zero vLLM errors, zero predictive failures, zero resource release
failures, zero dropped deferred outcomes, and no final deferred leak.
Protection was visible and coherent in the typed reject, bounded logs, both PIG
metrics paths, and Router effective capacity. TTFT protection was disabled and
there were no TTFT rejects or TTFT backpressure activations.

The exact same-window Grafana comparison was captured read-only from the Model
Nodes Historical Load dashboard:

| 30-minute mean unless noted | old `use1-4c` | v0.10.7 `use1-cb` | `cb / 4c` |
| --- | ---: | ---: | ---: |
| Running requests | 18.4 | 1.81 | 9.84% |
| Output TPS | 568 tok/s | 241 tok/s | 42.43% |
| KV cache usage | 38.55% | 3.76% | 9.75% |
| Tok/s per running request | 54.3 tok/s | 147.0 tok/s | 270.72% |
| Queue p95 | 623.80 ms | 308.45 ms | 49.45% |
| ITL p95 | 2.094 s | 46.311 ms | 2.21% |
| Prompt TPS | 1,248 tok/s | 1,281 tok/s | 102.64% |
| Cache hit rate | 4.96% | 7.31% | 147.38% |
| Spec-decode acceptance | 63.75% | 69.81% | 109.51% |
| Preemptions / paused | 0 | 0 | equal zero |

The live traffic shapes were not identical: prompt-token p95 means were 36,582
versus 12,433, generated-token p95 means were 7,601 versus 2,175, and requested
maximum-token p95 means were 139,292 versus 126,682. Therefore the raw 568
versus 241 output-TPS difference is not a controlled causal estimate. The
combined result is nevertheless a product failure: `use1-cb` used only about a
tenth of the old node's running concurrency and KV, had much higher per-running
request TPS and much lower ITL, saw comparable prompt TPS, had no cache-hit or
speculative-acceptance disadvantage, and neither node preempted. Returning to
the old uncontrolled policy is not the correction because its QoS was
materially worse. The candidate must move toward the safe capacity frontier
instead of remaining far above the TPS target.

The canary was removed from Router at `2026-08-03T02:36:10Z` by changing only
`use1-cb.enabled=true` to `false`. The enabled set is exactly `use1-4c`.
`use1-4c`, weights, policies, bearer configuration, metrics paths, Router and
vLLM source, CVM Compose, and running containers were not changed. At the
disable evidence capture, the target CVM still ran the v0.10.7 enforce Compose
and immutable image while Router-disabled; this is a timestamped fact and must
be freshly re-queried before any later deployment. Evidence is retained under
`tmp/pig-v0107-use1-cb-live-20260803/canary-r19`, including
`analysis-summary.json`, `grafana-use1-4c-vs-use1-cb.md`, the fresh final health
and log captures, and `router-disable-throughput-fail/mutation-summary.json`.

##### Source-and-telemetry root-cause hypothesis to reproduce before repair

Learning was wired and active. Global scheduler samples increased from 7 to
192, input-size stored samples from 14 to 91, learned input estimates from 81
to 1,256, and the last decision moved from static to calibrated. The failure is
that the current learner can retreat but cannot reliably recover or acquire
evidence beyond the concurrency it already admits.

The strongest concrete mechanism is the existing-prefill TPS residual path:

1. The observer accepts a stable one-prefill window and computes aggregate
   generation-counter delta divided by elapsed time and existing decoders.
2. During the canary, accepted existing-prefill evidence advanced from 2 to 6
   at sample 19, whose last observed existing-user TPS was only `5.000989`.
   Later accepted evidence reported `235.119367`, `387.519504`, and the fresh
   final value `237.595379` tok/s.
3. `preferExistingTPSRatios` calls `minimumAdverseRatio` before applying the
   normal `MinimumSamples=3` maturity rule. One ratio below 1 therefore becomes
   an immediately calibrated lower bound.
4. The minimum adverse ratio is retained across compatible local and global
   evidence for the full configured 1,800-second learning age. Later healthy
   ratios do not displace it. The multiplier is clamped to the configured 0.10
   minimum rather than returning to a healthy estimate.
5. The canary consequently ended with calibrated
   `existing_tps_at_risk`; 180 of 210 protection activations had exactly that
   reason/source, while sampled running peaked at 5, average KV was 3.85%, and
   single-user TPS samples were 106.72–448.09 tok/s.

This explains a sticky conservative frontier without claiming that every
5 tok/s stable-prefill sample is measurement noise. A real severe sample must
cause an immediate bounded retreat. The bug is allowing one such sample to
dominate for the entire generic learning age with no qualified recovery path.
The static TPOT slope, residual compatibility, outcome attribution, and
calibrated decision order can also contribute and must be isolated by failing
tests; they are not yet accepted as additional live root causes.

##### v0.10.8 product and algorithm contract

Version v0.10.8 must remain model-agnostic and tokenizer-assisted, not
tokenizer-controlled. Prediction, decision, and reservation occur before
forwarding to vLLM. Feedback changes only later decisions. The lightweight
lexical tokenizer contributes only an approximate input-size hint to the
combined forecast. It is not KV-capacity truth, cannot independently admit or
reject, and its absence or failure falls back to the existing bounded JSON/byte
estimate. Before qualified hint samples mature, it cannot shrink the cold input
upper. The approximate estimator remains simple and bounded; there are no
model-specific assets, exact-tokenizer requirement, cache-aware or cache-hit
algorithm, routing responsibility, Router source change, vLLM source change,
or TTFT admission protection.

The correction is a bounded throughput-first, graded-QoS frontier learner, not
a lower TPS threshold and not an unconditional concurrency increase:

1. Keep the red TPS value as the hard service floor. Use the yellow TPS value
   only to classify and count soft QoS loss; it is not a prerequisite for a
   one-step probe. An outcome between red and yellow is an allowed learning
   sample rather than an immediate hard failure. Whether a mature below-yellow
   bucket may lead to one more probe depends on conservative aggregate
   completion throughput, not on yellow margin alone. A mixed aggregate value
   is not itself a per-user guarantee. Pass both red and yellow TPS targets and
   their reciprocal TPOT bounds explicitly into scheduler configuration; do not
   infer absolute bands from a residual ratio or replace the manager's existing
   red hard constraint.
2. A newly qualified hard-band TPS/TPOT outcome below red/above reciprocal red
   immediately lowers or freezes the learned frontier and starts a bounded
   cooldown. Waiting, any preemption, observer/epoch incoherence, workspace
   risk, or hard KV risk also blocks exploration and uses the existing
   conservative protection path. A waiting observation records a
   fixed-cardinality load-pressure event so a newly healthy scrape cannot
   instantly reuse the old optimistic frontier; preemption continues to
   invalidate learning and also starts the cooldown. A yellow-only miss does
   none of these things. Availability loss blocks admission from current health
   but never creates or clears throughput evidence.
3. Replace the indefinite minimum-adverse latch with a separate, short adverse
   evidence horizon derived from the observer maximum-metrics-age contract,
   strictly shorter than the generic learning age. Do not add a second
   user-facing timer knob unless builder evidence proves it necessary. One
   qualified hard adverse sample may trigger immediate retreat, but it
   cannot remain the sole controlling statistic for the generic 1,800-second
   learning age. Repeated qualified hard adverse outcomes refresh the short
   horizon;
   after it expires, recovery still requires the normal minimum number of
   newer, qualified, compatible healthy outcomes. Censored, unattributed,
   stale, request-unknown, and TTFT-only outcomes neither raise nor clear the
   evidence frontier.
4. Reuse the existing bounded scheduler residual cells as the safe evidence
   frontier. Mature optimistic evidence may extrapolate to exactly one decode
   bucket above the highest compatible mature evidence and no farther. The
   query must not have greater normalized prefill/context/KV pressure than the
   evidence permits. Mark this prediction as exploratory; no timer alone raises
   capacity.
5. An exploratory prediction is eligible only when observer and coordinator
   samples are coherent, waiting is zero, the preemption cooldown is clear,
   compatible evidence has reached the minimum sample count, and the ordinary
   counterfactual hard bounds for red TPS/TPOT, physical KV,
   active KV, workspace, preemption, confidence, request size, and lifecycle
   all pass. The normal evaluator must still produce `fit`; exploration never
   overrides a reject. Unknown, availability, oversized-request, or
   hard-resource decisions can therefore never be bypassed.
6. The existing manager lock, virtual upper state, and reservation remain the
   only admission transaction. Once one next-bucket reservation is present, a
   concurrent next request observes another decode bucket and cannot reuse the
   same evidence to skip a second level. The exploratory bit is retained in the
   immutable prediction/reservation for fixed-cardinality telemetry and outcome
   qualification; it is not a second lease or independently mutable permit.
7. A qualified, uncensored outcome at the exploratory bucket becomes ordinary
   bounded residual evidence at that bucket. Only the normal minimum sample
   count can make it mature. A mature bucket above red may qualify exactly one
   further probe even when it is below yellow, but only when a separately
   qualified conservative aggregate-completion-throughput window shows that the
   higher concurrency improved total throughput over the preceding mature
   bucket. If total throughput did not improve, the learned frontier remains at
   the preceding bucket. A hard-band result enters the short adverse horizon and
   retreats the next comparable prediction immediately; a yellow-only result
   does not. A censored outcome supplies no promotion or adverse evidence;
   normal terminal handling still releases the reservation exactly once.
8. Add no new request-keyed map or parallel capacity controller. Reuse the
   existing fixed `MaximumCells`, per-cell sample bound, global sample bound,
   backend-epoch identity, maximum age, invalidation, and eviction semantics.
   Any added adverse-horizon state is fixed-size scheduler state and uses a
   monotonic timestamp plus bounded counters, never model IDs, prompts, users,
   or payload data.
9. Exploration changes only admission on the target PIG process. It does not
   select an upstream. Node-wide load/availability protection remains published
   coherently to the typed reject, bounded logs, `/pig/metrics`, `/v1/metrics`,
   and Router effective capacity. Request-scoped risk still rejects only that
   request and cannot lock an idle node.
10. The existing 500 ms metrics request timeout remains unchanged until a
    controlled test proves it is the cause of false availability. The five
    naturally recovered canary availability episodes remain a separate
    observation, not justification for fail-open or a blind timeout increase.

Keep SOLID ownership narrow: the scheduler owns forecast calibration, adverse
horizon, and the one-step evidence rule; the manager owns the unchanged atomic
check/reserve transaction; the observer supplies qualified backend evidence and
health events; and the HTTP adapter owns only request lifecycle plus bounded
publication. Do not add another controller that independently mutates
admission limits outside the predictive transaction. Observer health callbacks
must run after releasing the observer mutex, may not call back into Manager from
inside the scheduler lock, and must use panic-safe narrow interfaces to preserve
the existing lock order.

##### Mandatory red/green and efficiency gates

Before implementation is accepted, the current v0.10.7 source must fail a
focused deterministic reproduction that feeds healthy existing-prefill
evidence, one `5.000989`-style adverse sample, then multiple newer 200+ tok/s
same-or-higher-pressure samples. The red must prove both failure modes: the
single adverse residual remains controlling beyond its intended cooldown, and
the scheduler cannot cross the rejected next bucket despite sustained margin.
The red is invalid if it fails only because of a fixture, identity, timestamp,
or missing dependency.

Focused red r1 was reproduced on the remote builder against exact tag
`v0.10.7`, commit `c1ff24e03024a31bce1df309ace483ec663042ff`, using the
immutable Go 1.24.5 toolchain named below. The baseline archive SHA-256 was
`7386d3fb799edc4d37e639afffb7afc7d19ba3b46dd1825b725a8d16964090c8`,
and the test-only patch SHA-256 was
`bf138a019b13773618d645a082d1db779cfaa5ac0226eb84cbe7df09dc11aead`.
Reconstruction, patch application, and gofmt validation passed. The selected Go
test command failed for both intended product reasons: the old adverse sample
still forced an existing-user TPS lower bound of 7.2 after newer mature healthy
evidence, and mature optimistic evidence at decode bucket 2 produced a static,
unchanged prediction at bucket 3. The harness rejected any unexpected pass and
required both exact failure markers before recording red success.

Downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/red-r1/evidence.tar`, SHA-256
`2ff7d88c840da940177df94627fa7541b317e3f4dc1fa7f0ffd487c7eb7d91b7`.
Its `reconstruct`, `test-apply`, `gofmt`, red-validation, runner, and overall
status are successful. This is focused behavioral red only; no candidate source
change, green test, race, full matrix, image, deployment, or live validation is
implied.

The later tokenizer-reference correction has an independent focused red against
the executable r6 throughput-learning candidate reconstructed from HEAD
`98437fd...` plus candidate patch SHA-256
`dd6aef85a505b7ba441a83b4d541f27fa00986dee37d0e9e6b6c16c7b9b261d4`.
The test-only LF patch SHA-256 is
`e52f04ba31304378b233b46f93b7f2ac4b30d2407ea356dc0a284d1a9c2f0777`.
Remote r3 applied both patches and passed gofmt; the selected tests failed only
with the required markers that equal-byte ASCII/CJK shapes had no optional hint
provider and the calibrator had no optional-hint/next-request learning
interface. Evidence is
`tmp/pig-v0108-use1-cb-20260803/hint-red-r3/evidence.tar`, SHA-256
`9048b15d3e457a1e86c22875388f5bb612b4798e1bd5bbce21409c11bf04a5f6`.
The two preceding harness attempts are invalid evidence: r1 lacked a host
`base64` executable and r2 retained CRLF in patch paths. Neither reached a Go
product test, and neither is counted as behavioral red.

The v0.10.8 candidate must then pass all of the following:

1. In a deterministic backend whose safe capacity maintains 50 TPS through
   decode concurrency 8, cold admission remains conservative, then bounded
   probes reach decode 8 and at least 80% of the oracle's hard-band-compliant
   output-token goodput. The candidate must improve completion-token goodput by
   at least 30% over a clearly labelled no-exploration ablation baseline without
   a red-TPS/red-TPOT, waiting, preemption, KV, workspace, lifecycle, or
   publication violation. This ablation is not labelled exact v0.10.7 evidence;
   exact v0.10.7/candidate comparisons remain a separate builder gate and run in
   both orders on the same fixed workload.
2. Test harmful, beneficial, and hard knees beyond a mature safe decode-6
   frontier. At 22 TPS/user with red/yellow 15/25, decode 7 aggregate throughput
   is `154`, below decode 6 aggregate `300`; the candidate may collect the normal
   minimum of three qualified samples, must then retreat to decode 6, count the
   yellow misses, and never try decode 8. At 44 TPS/user with red/yellow 15/45,
   decode 7 aggregate `308` is above decode 6 aggregate `300`; the candidate must
   retain decode 7 and may continue one bucket at a time while conservative
   aggregate throughput keeps rising, even though per-user TPS is below yellow.
   At a hard decode-7 knee of 10 TPS, it may expose decode 7 exactly once, must
   retreat on the next comparable prediction, and must never try decode 8.
   Repeated hard adverse outcomes prevent recovery; newer compatible healthy
   evidence may resume only bounded one-step extrapolation after cooldown.
3. A single qualified zero/near-zero generation-counter interval below the red
   floor causes an immediate bounded retreat but not a 30-minute sticky minimum;
   a yellow-only result never enters that hard path. Metric scrape timing,
   counter lag/reset, empty decode, prefill-only windows, and censored or
   unattributed outcomes cannot falsely promote or permanently suppress the
   frontier.
4. Bursts, mixed short/long prompt and decode horizons, oversized input,
   request-size unknown, cancellation, disconnect, slow downstream, response
   before the next poll, stale/failed metrics, availability loss, backend epoch
   reset, waiting, preemption, and shutdown preserve conservative behavior and
   release every ordinary/probe reservation exactly once.
5. Parallel decisions under the manager lock cannot cross more than one bucket
   beyond the highest mature compatible evidence. Race and property tests prove
   no oversubscription, double release, stale promotion, counter underflow, or
   unsafe reuse of one-step evidence.
6. Low-flow and sparse traffic do not self-lock. A request-scoped rejection,
   an unqualified outcome, cooldown expiry, no natural probe opportunity, or
   completed probe cannot create sticky zero, continuous Router fullness, or a
   permanent inability to admit a cold-safe request.
7. Load and availability protection still appears coherently before 429 in the
   typed reject, activation/renewal log, both metrics paths, and Router effective
   capacity. Probe telemetry uses fixed enums/counters/gauges and contains no
   payload, user, request, model, token content, bearer, or unbounded label.
8. TTFT can be observed and reported, but changing only TTFT never changes a
   decision, frontier, probe, Router backpressure state, or effective capacity.
   Cache-hit and routing state are absent from the prediction and learner.
9. Focused tests, all Go tests, race tests, formatting/static checks,
   deterministic simulations, count/size-estimator tests, and applicable native
   gates pass on a clean remote builder. No Go/native/race/build/image test runs
   on local Windows.
10. Predictor and estimator benchmarks are compared to exact v0.10.7 in both
    orders on the same builder. The predictive hot path must add no unbounded
    work or request-keyed allocation and must not regress steady-state ns/op or
    allocations/op by more than 10%. Standalone builder timing remains CPU
    evidence, not a production latency or GPU-throughput claim.

##### Version and execution order

Do not mutate the released v0.10.7 tag. Implement the smallest coherent
vertical slice as v0.10.8 only after preserving the focused red against the
exact v0.10.7 executable tree. Run focused green first, then the complete clean
builder matrix and controlled v0.10.7/candidate comparison. Record source
commit/archive SHA-256, builder identity, exact commands, exits, and evidence
hashes. Complete and revise the three review passes below before version/tag.

Only a reviewed, clean-builder-green commit may be pushed to `pig-origin`,
tagged v0.10.8, built on the approved builder, and published as an immutable
multi-architecture registry image. Source, tag, amd64 image ID, registry digest,
and attested binary version must agree. This does not authorize production
traffic by itself.

After registry smoke, generate a one-field image candidate from fresh live
Compose and deploy only to authorized CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` while `use1-cb` remains Router-disabled.
Repeat protocol, identity, estimator/predictor latency, learning, sparse/low-flow,
sticky-zero, terminal-zero, log/metrics coherence, availability, and no-fatal
gates in shadow and then Router-disabled enforce. Any failure retains v0.10.7
or the last known live rollback and keeps `use1-cb` disabled.

Only after every disabled-route gate passes may the then-current Router document
be changed solely from `use1-cb.enabled=false` to `true`. Start a fresh strict
30-minute window at the first proven real request. Collect Router fast samples,
PIG/vLLM metrics, logs, serial logs, endpoint/attestation health, and the exact
same-window Grafana comparison. Judge SLO-compliant completion goodput, output
TPS, running/KV utilization, reject rate, TPS/TPOT distribution, waiting,
preemption, errors, estimator/predictor latency, and publication coherence
together. Real traffic is observational, not a controlled A/B. A safety,
publication, lifecycle, or material throughput failure disables only
`use1-cb`, preserves evidence, and repeats the flow with a new version. If no
obvious problem remains, retain the node enabled for continued observation.

##### v0.10.8 plan review pass 1 — model and causality

Completed 2026-08-03 and revised the contract before executable changes. The
first draft introduced a separate frontier controller and one probe per
pressure class. That duplicated scheduler ownership and could allow unrelated
classes to stack several unverified probes against one GPU. It was replaced by
one-step extrapolation from the existing bounded residual evidence. The
manager's locked virtual upper state makes the first exploratory reservation
visible to the next decision, so the same mature bucket cannot skip two levels.

The review also found that terminal completion cannot be the sole promotion
signal: `markLiveOutcomesInterferedLocked` correctly censors an older request
when later work was absent from its forecast. v0.10.8 therefore creates no weak
synthetic target. It reuses only already qualified, uncensored completion
outcomes and stable existing-prefill outcomes, requires the normal sample
minimum, and keeps pressure compatibility directional. The focused red must
independently prove the short-adverse recovery and the one-step evidence path.
Every producing request retains its immutable pre-forward prediction; an
outcome is observed only after forwarding/terminal or a later stable metrics
window and can affect only a subsequent call to `Predict`.

##### v0.10.8 plan review pass 2 — safety, lifecycle, efficiency, and SOLID

Completed 2026-08-03 and revised the contract before executable changes. The
review found that “healthy evidence” was underspecified: a residual ratio above
one does not prove the user's absolute TPS band. Scheduler configuration must
therefore receive the yellow TPS target and reciprocal TPOT target explicitly
for soft-loss classification, while ordinary admission continues to use the red
floor in Manager. A later correction removes yellow margin as an exploration
prerequisite and requires aggregate-throughput benefit instead. TTFT is absent
from both conditions.

The review also found a recovery race after backend waiting. Current health
correctly blocks admission while waiting is non-zero, but clearing waiting could
immediately expose old optimistic samples. The plan now requires a bounded
load-pressure cooldown on waiting and preemption, while keeping transient
availability separate and retaining conservative evidence. The cooldown is
derived from the already bounded metrics-freshness contract rather than another
operator timer. Repeated adverse or pressure evidence refreshes it; time alone
can only remove a temporary adverse override and can never promote a bucket.

The one-step rule remains inside Scheduler and the ordinary reservation remains
inside Manager, so cancellation, disconnect, forward failure, timeout,
completion, deferred outcome, epoch reset, close, and panic keep their existing
single ownership. No probe resource exists to leak. Any exploratory marker is
immutable metadata only. New observer-to-scheduler pressure feedback must be a
narrow panic-safe call made outside the observer lock, with no reverse callback,
which preserves lock order. Existing bounded cells/global samples are reused;
there is no new request-keyed allocation or label. Low flow falls back to the
cold policy after temporary evidence expires and therefore cannot become zero
or permanently closed merely because no promotion sample arrives.

##### v0.10.8 plan review pass 3 — evidence and release boundary

Completed 2026-08-03 and revised the contract before executable changes. The
review distinguishes the exact v0.10.7 executable baseline from the current
documentation HEAD: prove executable-tree equivalence or use tag `v0.10.7`
directly in an independent builder checkout before accepting the focused red.
The test patch and deterministic workload must be byte-identical for baseline
and candidate, and their order must be reversed on the same builder. A harness,
dependency, identity, or timestamp failure is not product red evidence.

The deterministic 40-TPS-through-decode-8 and capacity-knee-6 scenarios provide
absolute safety/frontier assertions; the 80%-of-oracle and 30%-over-v0.10.7
requirements provide relative goodput assertions. Both are required. CPU
microbenchmarks, deterministic goodput, Router-disabled protocol tests, and
live Grafana observations remain separate evidence layers. In particular, the
mixed live 568 versus 241 tok/s comparison proves the product problem when
combined with utilization/QoS evidence but is not reused as the v0.10.8 causal
gain number.

The review also corrected a drift-prone operational statement: v0.10.7 Compose,
container, and Router-disabled state are recorded at the disable capture, not
asserted indefinitely. Fresh CVM, Compose hash, image digest, Router document,
enabled set, endpoints, and rollback are mandatory before a later write. No
source green authorizes an image; no image green authorizes Compose; no
Router-disabled green authorizes traffic. Only the explicit ordered gates above
can reach the strict 30-minute canary, and any failure changes only
`use1-cb.enabled` back to false. The current user contract and this later plan
section supersede older goal wording that mentioned TTFT protection: TTFT is
observation-only throughout v0.10.8.

##### v0.10.8 plan correction — throughput-first graded QoS

Added after the 2026-08-03 strict absolute-yellow focused green. The user
clarified that PIG may accept some temporary per-user TPS reduction when that is
needed to discover materially higher total throughput. Learning should make
unproductive reductions less frequent over time without turning every yellow
miss into a long protection interval. This correction supersedes earlier text
that treated the first absolute yellow miss as hard adverse evidence.

The active policy is:

1. The manager's configured red TPS floor and reciprocal red TPOT bound remain
   the ordinary pre-forward hard QoS gate. Predicted red-band violations,
   physical/active KV overflow, workspace/preemption risk, unavailable or stale
   state, and lifecycle failure remain normal rejects. Exploration never
   overrides them.
2. Yellow TPS and reciprocal yellow TPOT are soft exploration objectives. One
   attributed outcome between red and yellow does not enter the single-sample
   hard-adverse channel, erase the mature lower frontier, publish Router load
   protection, require a full healthy generation before progress resumes, or by
   itself stop the next one-step throughput probe.
3. A yellow-only miss is retained in the same bounded residual cell as ordinary
   evidence. Until the normal minimum sample count is reached, one-step atomic
   exploration may expose another comparable request; it still cannot skip a
   decode bucket or stack another unforecast bucket while a probe reservation
   is live. Once the sample minimum matures, the existing conservative quantile
   lowers the corresponding TPS/TPOT forecast. If that mature forecast remains
   above red, the learned concurrency may continue and may probe one more bucket
   when conservative aggregate completion throughput improved; if aggregate
   throughput did not improve, it retreats to the preceding mature bucket. If
   the forecast falls below red, normal admission protection applies.
4. A qualified outcome below red, above reciprocal red TPOT, or associated with
   waiting/preemption remains hard adverse evidence. It causes immediate bounded
   retreat and prevents old optimistic evidence from automatically returning.
   Recovery requires newer compatible evidence; idle/cold-safe progress and
   terminal release remain available so the mechanism cannot self-lock.
5. Repeated healthy evidence can gradually replace old soft misses through the
   bounded window, age, and quantile. No separate PID, token bucket, request map,
   or model-specific controller is added in v0.10.8. A more explicit QoS-loss
   budget is considered only if deterministic and live evidence shows the simple
   bounded-sample rule cannot balance throughput and QoS.
6. The approximate tokenizer remains only an optional input-size feature. It
   helps residual cells distinguish small and large prompt pressure, but has no
   hard/soft QoS thresholds, cooldown, publication state, or independent admit
   decision.

Replace the earlier zero-TPS-violation acceptance wording with graded evidence:

- repeated or unexplained hard red-TPS/red-TPOT violations, and all KV,
  workspace, preemption, reservation, self-lock, sticky-zero,
  false-publication, and lifecycle violations must be zero. A deterministic
  previously unknown hard knee may expose exactly one qualified frontier probe;
  the next comparable prediction must retreat and no higher bucket may run;
- yellow-only misses may be non-zero, but the report must count them by fixed
  category, show that comparable misses reduce later exposure when the higher
  bucket provides no aggregate-throughput gain, preserve a below-yellow bucket
  when it does provide a gain, and prove no second unverified bucket is skipped;
- candidate SLO-compliant completion-token goodput must remain no worse than the
  current baseline overall and must materially improve at least one constrained
  mixed workload; raw admits, GPU utilization, or throughput bought through
  repeated red violations do not count;
- compare both a soft knee (between red and yellow) and a hard knee (below red).
  The soft knee may settle at the higher-throughput below-yellow level when it
  stays above red; the hard knee must retreat after the first qualified result;
- sparse/low-flow, no natural probe opportunity, expiry, cancellation, waiting,
  preemption, reset, and hint failure must all retain cold-safe progress without
  node-wide false protection.

The already downloaded strict-yellow focused green at candidate patch
`bd52dce4a41bc809397d4f53818f1e7a7740e0ee1c987aef1cf5b7d0340946b3`
and evidence SHA-256
`17d79b787edc3878d4afdc5bd4b169bce655f84e430514c4fecfb4a3106e868c`
proves that the old strict behavior was executable, race-clean in the focused
packages, and not a harness failure. It is now a superseded comparison artifact,
not final v0.10.8 green. No image, deployment, Router change, or live traffic was
performed from it.

##### v0.10.8 second correction — aggregate benefit, not strict TPS-first

Added after R10 was executed on the remote builder and after the user clarified
that total throughput is the primary objective. Limited per-user TPS reduction
is acceptable above the red floor. Learning must make a reduction less likely
when it buys no total throughput, but it must not turn yellow into a hidden hard
limit that holds GPU utilization below the useful frontier. This section
supersedes every earlier sentence that requires a yellow-band forecast before a
one-step probe or that stops solely because a mature prediction is below yellow.

The throughput and QoS signals remain deliberately separate:

1. Qualified completion outcomes provide the joining request's TPS/TPOT and
   observational TTFT. They train per-user QoS for later predictions. The real
   HTTP completion producer normally does not contain a simultaneous
   `ExistingUserTPS`, so R10's requirement for joining and existing TPS in one
   `SchedulerOutcome` would make aggregate-frontier learning unreachable in the
   real path.
2. Do not replace that missing value with `joining UserTPS * admission-time
   concurrency`. Other users can complete during a long response, workloads are
   unequal, and a fast joining request can otherwise certify an optimistic total
   throughput that never existed. Do not splice the separate existing-prefill
   outcome into the completion outcome; the windows and causal targets differ.
3. Aggregate completion throughput comes from the vLLM generation-token counter
   already fetched by the asynchronous observer. A sample is qualified only
   across a bounded stable window with positive generation progress, identical
   positive running concurrency at both ends, zero waiting, no preemption or
   counter reset, no Manager or shadow lifecycle event during the interval, and
   no pending prefill at either end. The sample contains only backend identity,
   start/end time, decode-concurrency bucket, and aggregate completion TPS.
4. The scheduler owns a separately bounded, anonymous concurrency curve using
   the existing minimum-sample, maximum-age, per-cell sample, and maximum-cell
   limits. Aggregate throughput is an objective estimate, not a per-user QoS
   lower bound, so observation computes an independent lower median instead of
   reusing the residual calibrator's p10 setting. Pre-forward prediction performs
   only O(1) current/previous-bucket lookups. Restart, epoch reset, preemption
   invalidation, age expiry, and bounded eviction remove its authority.
5. The current bucket reaches a throughput frontier only after both current and
   immediately preceding buckets are mature and the current robust aggregate
   TPS estimate is no greater than the previous estimate. Lack of aggregate
   evidence never fabricates a reject. Red TPS/TPOT, waiting, preemption, KV,
   workspace, confidence, availability, and lifecycle gates remain independent
   and higher priority.
6. Yellow outcomes are counted QoS debt. They are not free: deterministic and
   live reports must show their frequency, duration, aggregate throughput gain,
   red-band distance, and whether exposure declines at an unproductive bucket.
   No yellow-only miss activates Router protection. A mature
   no-throughput-gain frontier may publish the existing load-dependent capacity
   contract because the node is then at its learned useful concurrency.

R10 patch SHA-256
`0562ff2ccbfb287e6d81d4d83ec5ce46231668f26e008e9bafac7e05c590f15a`
was not green. Remote evidence archive SHA-256
`30020682fbedf7e189e9fb96f09d3a4b8af4d814f142ce23f8c078e32d3d5c4c`
recorded focused/default/race failures plus gofmt differences. The first product
failure was strict-yellow logic returning `throughput_frontier_reached` at
decode 2 before any probe; the beneficial-soft simulation consequently matured
only decode 1. The format failure is separate. R10 is retained as red design
evidence, not source green, release evidence, an image, or deployment authority.

The corrected deterministic matrix is:

- safe: 50 TPS/user through decode 8, reaching aggregate 400;
- harmful soft: 50 TPS/user through 6 then 22 at 7, collecting three soft
  samples and retreating from aggregate 154 to the mature aggregate 300 bucket;
- beneficial soft: 50 through 6, then below-yellow TPS that still raises
  aggregate throughput across one or more buckets; retain every beneficial
  bucket and stop only at the first mature bucket whose aggregate no longer
  improves;
- hard: 50 through 6 then 10 at 7, allowing at most one qualified exposure and
  immediately retreating without trying 8;
- producer causality: a real completion containing only joining TPS cannot
  mature aggregate evidence; a separate stable generation-counter outcome can,
  and feedback changes only the next prediction;
- robustness: unequal users, fast joining/slow existing users, counter lag or
  reset, no-generation windows, churn, pending prefill, waiting, preemption,
  censored intervals, sparse traffic, expiry, cancellation, epoch invalidation,
  hint failure, and low-flow recovery cannot promote a false frontier or create
  a self-lock.

Review pass 1, model and causality: the completion proxy was rejected because it
is not produced with simultaneous existing-user TPS and multiplication would
misattribute changing concurrency. The selected counter is the direct aggregate
token producer already observed from vLLM, while tokenizer size, per-user QoS,
and aggregate benefit remain independent inputs to one scheduler decision.

Review pass 2, safety, lifecycle, efficiency, and SOLID: aggregate evidence is
anonymous, bounded by existing configuration, invalidated with scheduler
learning, and never stored per request. Stable-window qualification stays in the
observer; curve ownership and prediction stay in Scheduler; Manager remains the
only atomic check/reserve owner. Feedback-path quantiles preserve O(1)
pre-forward lookup. Missing or stale curve evidence permits only the ordinary
red/resource-safe one-step learning path and cannot independently claim either
capacity or risk.

Review pass 3, evidence and release: R9 proves only the prior graded-QoS slice
and R10 is red; both are superseded for final algorithm claims. The corrected
producer tests and four frontier simulations must pass focused and race gates on
the remote builder before any full matrix. Every executable change invalidates
earlier candidate hashes. No version commit, tag, push, image, CVM deployment,
Router enable, or live-traffic window is allowed until the ordered gates in this
plan are newly satisfied.

##### v0.10.8 third correction — throughput objective with bounded QoS debt

This latest correction makes the optimization order explicit. PIG maximizes
useful aggregate completion-token throughput subject to hard resource/lifecycle
safety and the red per-user TPS/reciprocal-red-TPOT floor. Among policies with
similar useful throughput, it prefers the one with fewer and shorter yellow-band
misses. It does not optimize for zero TPS variation, and it does not require a
yellow-band prediction before a red-safe one-bucket probe. This section
supersedes the older `TPS-first` wording in the persistent goal and all plan text
that could make yellow a hidden admission threshold.

The operational interpretation is deliberately simple:

1. A limited, temporary TPS reduction above red is acceptable while acquiring
   evidence. Yellow misses are fixed-cardinality QoS-debt observations used to
   improve later predictions; they do not independently reject, start cooldown,
   publish Router protection, reduce Router capacity, or erase mature evidence.
2. If a mature higher-concurrency bucket increases aggregate completion TPS, it
   remains useful even when its per-user TPS is below yellow, and one-step
   exploration may continue. If the mature bucket provides no aggregate gain,
   later comparable requests retreat to the immediately preceding higher-
   throughput bucket. A hard red miss, waiting, or preemption still retreats on
   the next comparable prediction without waiting for aggregate maturity.
3. The aggregate curve uses the lower median (`q=0.50`) of fresh qualified
   stable-window samples. With the default minimum of three samples,
   `[100, 310, 320]` estimates `310`, so one non-zero scrape outlier cannot close
   a `300`-TPS preceding frontier. A second low observation producing
   `[100, 310, 320, 100]` estimates `100` under the existing lower-quantile
   selector, so repeated low evidence closes it. The residual calibrator keeps
   its configured p10 for conservative per-user QoS; the two statistics must not
   be conflated.
4. Code, logs, status, Router-capacity events, and fixed-cardinality metrics call
   the aggregate values `estimate`, not `lower`. Missing, stale, reset, zero-
   progress, churned, waiting, preemption-affected, lifecycle-overlapped, or
   pending-prefill windows create no estimate and therefore no frontier reject.

Review pass 1, objective and causality: multiplying one request's TPS by an
admission-time concurrency remains rejected as a fabricated aggregate target.
The vLLM generation-token counter is the direct aggregate producer; the
approximate tokenizer hint, per-user QoS residuals, and aggregate curve remain
separate evidence feeding one pre-forward decision. The aggregate statistic is
therefore a robust throughput estimate rather than a safety lower bound.

Review pass 2, safety, lifecycle, efficiency, and SOLID: the median changes only
the bounded anonymous aggregate curve. It cannot weaken red/resource evaluation
or create a second controller. Observation performs bounded sample selection;
`Predict` retains O(1) adjacent-bucket lookup; Manager remains the only atomic
check/reserve owner. A focused test must prove one low aggregate outlier neither
closes the frontier nor enters hard adverse state, repeated low evidence does
close it, and the existing single-sample hard per-user retreat still passes.

Review pass 3, evidence and release boundary: R11 exposed the obsolete strict-
yellow test and invalid ablation; R12 cleared all behavioral gates but retained
one gofmt difference. R13 was the last exact focused and race green for its input
patch. Its evidence archive was downloaded to
`tmp/pig-v0108-use1-cb-20260803/frontier-r13/evidence.tar`; the local outer
SHA-256 exactly matches the builder value
`a8663e3aae89f3184788f6bbc78b17f655328892c9920b87731e0ab6f6cf6957`.
R13 recorded safe aggregate `400`, harmful-soft retreat `154 -> 300`,
beneficial-soft progression through aggregate `308` and `320`, and exactly one
hard-knee exposure. The later median and public-name changes are executable, so
R13 is superseded stage evidence, not current source green. A fresh exact R14 or
later focused/race run is mandatory before the low-flow/full-builder matrix.
There is still no v0.10.8 commit, tag, push, image, deployment, Router change, or
new live-traffic authorization implied by these source edits.

R14 used tracked patch SHA-256
`29c47a56512bfa13f606a4c3d4bb155d4a3b56283d8be5521e6e03ed39cc1873`,
the unchanged hint/simulation patches recorded above, and focused runner
SHA-256
`d55405720ce4246eea816b2ed08e94e4853e0957d72d42f4a320a01f959a8885`.
The remote and local inputs matched. Its downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r14/evidence.tar`, SHA-256
`7268cc010ae1b98140e63c18357ab7823841ea6f246f49b3bbbc75f796852f92`.
Observer and frontier simulation gates passed; scheduler, combined focused, and
race gates failed on the new median test. The failure was a test-state error,
not accepted product red: `Predict` adds the joining request to virtual state,
so `state.DecodeSequences=2` queried prospective bucket 3 while the fixture had
populated only buckets 1 and 2. Correcting the state to 1 queries prospective
bucket 2 as intended. R14 also recorded six gofmt-only alignment hunks, which
were copied exactly from remote evidence. Any corrected source requires a fresh
R15 or later run; R14 is not green evidence.

R15 used corrected tracked patch SHA-256
`188b096e1dbb550d081d90339b6fd81995dc62c559f3533d105e9259b1de48b3`.
Its downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r15/evidence.tar`, SHA-256
`83132e0f9ad08b831fb92235954ad953d2a10764a78f038f60c35c293b41c6f7`.
Scheduler, observer, all four frontier simulations, combined focused packages,
and race tests passed. The safe/harmful-soft/beneficial-soft/hard results were
respectively `400`, `154 -> 300`, progression through `308` and `320`, and one
hard exposure. Overall remained non-zero only because one manually copied gofmt
alignment contained a single extra space in `PreemptionRiskUpper`. That exact
remote formatting correction is executable-tree neutral but changes the patch;
therefore R15 remains stage evidence and a full exact R16 rerun is required.

R16 is the exact focused green for executable tracked patch SHA-256
`f698b3586d7c98c3ee301f8afd50d8bf6afc62885f936295b77f560b3a80e930`,
hint patch SHA-256
`28f443e91fc8970874842a037c032d8cf2ad964562291266b03fed0c201988ff`,
simulation patch SHA-256
`ac3da115aebce6182cff426450582c243c289cbe6424cf9564def848f43f08c3`,
and focused runner SHA-256
`d55405720ce4246eea816b2ed08e94e4853e0957d72d42f4a320a01f959a8885`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r16/evidence.tar`, SHA-256
`b6eafc10ae905833fe5c7144e3a9ab7bd3b0237500eda591f5051423155a876a`.
All statuses are zero: gofmt, scheduler, observer, four frontier simulations,
combined focused packages, race, and overall. Deterministic results are safe
aggregate `400` versus ablation `100` and oracle `400`; harmful-soft mature 6,
attempted 7, aggregate `154 -> 300`, three soft outcomes and no decode-8
attempt; beneficial-soft mature 8, attempted 9, retained aggregate `308` then
`320`, stopped at `297`; hard mature 6, attempted 7, exactly one hard outcome
and no decode-8 attempt. This focused evidence authorizes only the next
low-flow/full-builder/benchmark/review gates. It is not a full matrix, version,
commit, push, image, deployment, Router change, or live validation.

After adding benchmark-only coverage for the lexical hint, aggregate observation,
and aggregate-frontier prediction lookup, R17 used tracked patch SHA-256
`faba24ae297b1bdf9096dbe9a5050dbf077c44b4e1db4b886b6bfef1388bd748`.
Its downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r17/evidence.tar`, SHA-256
`5ed523b9f7fc9b4c6632221e505a948944125586da48d954d4fe7076f7ffd1f4`.
All R16 focused, simulation, race, and formatting statuses remained zero. R17
proves the added benchmark source compiles with the candidate; it did not execute
the benchmarks and therefore supplies no timing claim. The complete matrix must
run the exact-v0.10.7 predictor and estimator comparisons in both orders plus
the new candidate-only aggregate and lexical-hint measurements.

##### v0.10.8 fourth correction — bounded lexical sampling after full-r1

Full-r1 reconstructed candidate tracked patch SHA-256
`d3b0c7b909fdf3cfb9a6c2eb3e64ee0ffe97e53632fa4caf4d90bca6a5376390`
and exact v0.10.7 archive SHA-256
`087cad84f637444cb93ae684a90b7d2df62bd55de412760ef4c8898d9c4afcac`
on the fixed builder. Inner/outer runner SHA-256 values were
`ed558847337a1ebb8714903798807220c819e4684c1ffd09bc30f21d999b659e`
and
`5f98a685a366f0854e4e272cbfe535d489de1d052392f0c5c7e0a392d5ea7520`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/full-r1/evidence.tar`, SHA-256
`b956952633436ad483c0954acde1015ce3b1de46c31ec28bfba06f48458e33a2`;
its outer and every internal checksum were independently verified.

All executable statuses were zero: low-flow/self-lock boundaries, gofmt, vet,
build, all Go tests, all-race, deterministic goodput/frontier tests, both-order
candidate/exact-v0.10.7 goodput, predictor and estimator benchmarks, aggregate
lookup/observation, and lexical-hint benchmark. Candidate deterministic
completion-token goodput was `44704` with zero protected violations, false
accepts, or leaks; the older acceptance workload produced the same result on
exact v0.10.7, while the new frontier simulations separately proved safe `400`,
harmful-soft `154 -> 300`, beneficial-soft `308 -> 320`, and one hard exposure.

Full-r1 is nevertheless a performance red, not final green. Median exact-
v0.10.7 versus candidate predictor time was approximately `931.6 -> 1661 ns/op`
in candidate-first order and `934.1 -> 1657 ns/op` in reverse order. Candidate
allocation improved from `152 B, 2 allocs` to `128 B, 1 alloc`, and the isolated
aggregate lookup itself was only `447.4 -> 459.0 ns/op` and `449.5 -> 459.4
ns/op` with zero allocations, a 2-3% increment. The additional predictor time
therefore belongs to the broader graded-QoS/adverse recovery semantics, not the
O(1) aggregate lookup.

The first lexical implementation was too exact for its product role. It scanned
every string byte and decoded Unicode/escapes even though the result is only a
rough feature. At 64 KiB, exact-v0.10.7 estimator medians were about `1.877 us`
and `1.874 us`, while the candidate was about `128.225 us` and `125.410 us`;
the direct lexical pass was about `138.055 us`. Those absolute times are small
relative to inference, but the linear second pass contradicts the requested
simple, extremely cheap hint and the predeclared performance gate.

The corrected hint uses fixed-budget stratified sampling instead:

1. sample at most 256 string bytes for the whole request and at most 64 bytes
   from any one string, across up to four positions;
2. estimate only rough ASCII-word, whitespace, punctuation, and UTF-8 leading-
   byte density, then scale by string length; do not fully decode Unicode or
   escapes and do not allocate;
3. after the request budget is exhausted, use the constant-time four-byte
   fallback for later strings; JSON structure scanning and the existing raw
   conservative interval remain unchanged;
4. keep equal-byte ASCII/CJK separation, overflow/failure fallback, next-request-
   only calibration, bounded state, and no independent admission authority.

The earlier blanket 10% comparison is refined rather than silently waived.
The isolated new admission lookup must remain within 10% of the same-candidate
no-curve path with identical allocations. The full graded-QoS predictor must
remain below 2 microseconds median on this builder and use no more allocations
than exact v0.10.7. The bounded lexical pass must remain below 1 microsecond at
1 KiB, 64 KiB, and 2 MiB with zero allocations; its cost should be approximately
size-independent. The complete JSON estimator must add no more than 1
microsecond median over exact v0.10.7 at those sizes and retain zero allocations.
These are builder CPU gates only, not service latency or GPU-throughput claims.
Any executable correction requires fresh focused/race and full-matrix evidence.

##### v0.10.8 fifth correction — throughput priority reaffirmed and R18 disposition

The latest user contract reaffirms rather than reverses the third correction:
PIG must not over-enforce the nominal TPS target. Limited, temporary per-user
TPS reduction above the red floor is an acceptable exploration cost when it can
raise useful aggregate completion throughput. Learning should reduce the
frequency and duration of yellow-band debt at mature, unproductive concurrency,
but must retain a mature below-yellow bucket when that bucket improves aggregate
throughput. The optimization order is therefore:

1. preserve hard KV/workspace/lifecycle/preemption safety and the red TPS/TPOT
   floor;
2. maximize useful aggregate completion-token throughput;
3. among throughput-equivalent choices, prefer fewer and shorter yellow TPS/TPOT
   misses.

This is not a strict `TPS-first` controller, and yellow is not a hidden hard
threshold. A yellow-only observation still cannot reject, start cooldown,
invalidate mature evidence, or publish Router protection. Repeated mature
evidence that a bucket adds no aggregate throughput closes that frontier;
beneficial evidence keeps it open. The deterministic harmful-soft and
beneficial-soft cases remain mandatory because together they prove both sides of
this tradeoff and guard against over-protection.

R18 used tracked patch SHA-256
`30f75c77cc9cb3afada82f6d4f9e2e3c477a6cdcfaff3501c2c67bf6b58775c1`,
hint patch SHA-256
`c696409a05f4a0517c5739fcf90434231b744405ddc38c32cd43e289cc9050ee`,
the unchanged simulation patch SHA-256
`ac3da115aebce6182cff426450582c243c289cbe6424cf9564def848f43f08c3`,
and focused runner SHA-256
`d55405720ce4246eea816b2ed08e94e4853e0957d72d42f4a320a01f959a8885`.
Its downloaded archive is
`tmp/pig-v0108-use1-cb-20260803/frontier-r18/evidence.tar`, SHA-256
`f2628da2b8a16032b464856ebbfd0a39ab6da886ffe02b2e28b6c48f0eeef635`;
the archive and internal checksum manifest were verified. Scheduler, observer,
and frontier simulation gates passed. Combined focused and race gates failed on
one stale expectation in
`TestApproximatePredictiveHTTPInformationalResponseUsesFinalStatusAndFeedback`;
R18 also recorded one formatting-only constant alignment difference. R18 is red
stage evidence, not current source green.

The failed fixture contains 94 bytes of valid JSON followed by 1,600,000 JSON
whitespace bytes. The unchanged raw whole-body high is therefore 800,047 tokens,
so backend `usage.prompt_tokens=4` cannot qualify the raw channel. The bounded
lexical hint excludes trailing JSON whitespace and returns 14 tokens: nine for
the model string and five for the prompt string. Its qualified ratio is
`4/14 = 0.2857`, inside the configured `[0.25, 8]` band. Accepting this hint
sample is the intended correction of an overly conservative byte estimate and
matches the existing requirement that a safely high raw fallback must not block
hint learning. The corrected tests must assert one accepted/stored hint sample,
the unchanged final-status and lifecycle behavior, and the exact raw/hint
fixture boundary. A fresh exact R19 focused/race run is required; no R18 result
authorizes a full matrix, release, image, deployment, or Router change.

R19 used corrected tracked patch SHA-256
`b2d86838171ac40c465678add0fb645b2c1e6c875a8b21494197631597e5cb2c`,
hint patch SHA-256
`f389e6cc9b373f316e3cfed7a3b829904ed86d60af79498a271d307f65b79bae`,
the unchanged simulation patch SHA-256
`ac3da115aebce6182cff426450582c243c289cbe6424cf9564def848f43f08c3`,
inner runner SHA-256
`d55405720ce4246eea816b2ed08e94e4853e0957d72d42f4a320a01f959a8885`,
and outer runner SHA-256
`14a44b8e81900b36584824cd9dc53ca8aa261479f9a05467da856102add685d3`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r19/evidence.tar`, SHA-256
`b63939611abf7ac35ed343fde6dcd9ec96816abadbc18561743f447344881899`;
the outer archive, internal checksum manifest, input hashes, and every zero
status were independently verified. Formatting, scheduler, observer, all four
frontier simulations, combined focused packages, and race tests passed.
Deterministic results remained safe aggregate `400` versus ablation `100` and
oracle `400`; harmful-soft matured decode 6, attempted decode 7, retreated from
aggregate `154` to `300`, counted three soft outcomes, and never attempted
decode 8; beneficial-soft matured decode 8, attempted decode 9, retained
aggregate `308` and `320`, and stopped at `297`; hard matured decode 6,
attempted decode 7 exactly once, and never attempted decode 8. This exact
focused green authorizes only the fresh full builder and performance matrix. It
is not a versioned build, release, image, deployment, Router change, or live
validation.

Full-r2 reconstructed the current candidate from tracked patch SHA-256
`e384ff45521d93f72e827162632b15eec73abb7fd5232d7b086987abc0fede62`,
hint patch SHA-256
`f389e6cc9b373f316e3cfed7a3b829904ed86d60af79498a271d307f65b79bae`,
simulation patch SHA-256
`ac3da115aebce6182cff426450582c243c289cbe6424cf9564def848f43f08c3`,
and exact v0.10.7 archive SHA-256
`087cad84f637444cb93ae684a90b7d2df62bd55de412760ef4c8898d9c4afcac`.
Inner and outer runner SHA-256 values were
`8f867537f3c783e0b502c67ca0f4bf765631fd82783472bcdb2e1501c92461fd`
and
`af5b5b91c9e3320c68c3c337ba49c2feff7764fc9b504a0cb700b823baeed653`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/full-r2/evidence.tar`, SHA-256
`dd8cecffe7ab216820bf325d9c0e1f6567a6cc2b10dcf4fadc963be6da48d7a1`;
the archive, internal checksum manifest, input hashes, and every status were
verified. Low-flow/self-lock boundaries, formatting, vet, build, all tests, all
race tests, deterministic goodput/frontier simulations, both-order baseline
comparisons, and every benchmark command returned zero. Candidate deterministic
completion-token goodput was `44,704` with zero protected TPS/TPOT/KV,
preemption-proxy, false-accept, or reservation-leak violations. Frontier results
matched R19.

Every requested benchmark regex produced exactly five samples per binary,
order, and size. Median results were:

- full predictor candidate `1.665/1.661 us` versus exact v0.10.7
  `0.9361/0.9417 us`; candidate used `128 B, 1 alloc` versus `152 B, 2 allocs`
  and stayed below the absolute 2-microsecond gate in both orders;
- aggregate current/previous lookup candidate `458.7/464.3 ns` versus the
  same-candidate no-curve path `446.3/447.0 ns`, an increase of approximately
  `2.8%/3.9%`, with zero allocations; aggregate observation was `440.4 ns`,
  zero allocations;
- direct bounded lexical hint was `102.8 ns` at 1 KiB, `102.5 ns` at 64 KiB,
  and `100.7 ns` at 2 MiB, always zero allocations;
- complete estimator additive medians at 1 KiB were `104.7/107.2 ns` and at
  64 KiB were `103/118 ns`, always zero allocations.

The initial 2 MiB complete-estimator comparison conflicted across order:
candidate-first reported `64.908 us` candidate versus `61.766 us` baseline,
while reverse order reported `63.370 us` candidate versus `63.750 us`
baseline. Full-r2 is therefore functional green but cannot alone establish the
2 MiB additive performance gate. It does not permit selecting only the
favorable order.

A dedicated paired run precompiled both test binaries, pinned one container to
CPU 0 (`cpuset_effective=0`), warmed both binaries, and alternated candidate-
first and v0.10.7-first for eight 3-second rounds. Its inner and outer runner
SHA-256 values were
`43dbbd5acaaa2d018bfa32ce7e77d071f4682e5f87a02259077c54f2310693ef`
and
`246c277a273a74aecfe8828a09bcf4896acc5023ef85903691c83201c21e6a72`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/full-r2/paired-2m-r1/evidence.tar`, SHA-256
`f8b870fdf28b33c9818ca9f20c15269124bfcbb58a17e7d366e20724aed22e29`;
its archive, internal checksum manifest, exact full-r2 source inputs, binary
hashes, and zero status were verified. Across all eight balanced samples,
candidate median was `63.815 us` and exact v0.10.7 median was `64.146 us`, an
additive `-0.331 us`; both remained zero-allocation. This balanced evidence
resolves the contradictory unpinned full-r2 order as CPU/order noise and passes
the 2 MiB additive gate without changing or waiving the threshold. Full-r2 plus
the paired supplement authorize the three recorded source/evidence review
passes, not versioning, push, image, deployment, Router change, or live traffic.

##### v0.10.8 sixth correction — material-throughput deadband from review pass 1

Review pass 1 found one mismatch between the stated objective and the executable
frontier. The plan says that throughput-equivalent policies should prefer fewer
and shorter yellow-band misses, but the implementation considered every
positive floating-point aggregate difference useful. Ordinary scrape and CPU
measurement noise could therefore preserve a higher-concurrency bucket with
material QoS debt for a reported gain such as 0.1%, preventing learning from
reducing the very yellow exposure it records.

Keep the correction fixed, relative, and small: a mature current bucket must
exceed the immediately preceding bucket's robust aggregate completion-TPS
estimate by more than 1% to justify another probe. A gain of 1% or less is
treated as throughput-equivalent and closes the frontier in favor of the lower-
concurrency, lower-QoS-debt bucket. This is not a yellow TPS threshold and does
not alter red TPS/TPOT, KV, workspace, waiting, preemption, confidence,
availability, or lifecycle constraints. It also does not require an absolute
TPS value and remains model-neutral.

The deadband is intentionally below the deterministic beneficial-soft margins:
`308/300 - 1 = 2.67%` and `320/308 - 1 = 3.90%` remain useful and continue,
whereas a `302/300 - 1 = 0.67%` gain stops and a `304/300 - 1 = 1.33%` gain
continues. Focused tests must prove both boundary sides plus all existing safe,
harmful-soft, beneficial-soft, hard, low-flow, and Router-publication behavior.
Because this changes pre-forward admission semantics, the predictor identity
advances from `adaptive-tps-kv-v4` to `adaptive-tps-kv-v5`, preventing old
learned cells from being treated as the new policy. All prior executable hashes
remain valuable stage evidence but are superseded for final source green. A
fresh R20 or later focused/race run and a fresh complete builder/performance
matrix are mandatory before versioning or release work.

R20 used tracked patch SHA-256
`dd8c460a6f5b9fc992f5ad3214eb1ba3bfa6fe2b90de64a99142ed66c113c0cb`,
the unchanged hint and simulation patches, inner runner SHA-256
`2b05998a2f2bc1375a8c956f194786d8c2115ccdbe1046b109f10b47efbde07d`,
and outer runner SHA-256
`f9cb23996fd96d9652c08f7a91d5fab588ac0ee9f03d26b7e9ecca4fcf891427`.
Its downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r20/evidence.tar`, SHA-256
`640d5658c85f469533a53e6369092f0b90ae2f32609b5c324df5a78f5c44d030`.
The new sub-1% stop and above-1% continue test, all earlier scheduler and
observer cases, all four deterministic frontier simulations, combined focused
packages, and race tests passed. Overall remained non-zero only because the
four constants required gofmt alignment; R20 is red stage evidence, not current
source green. The exact formatter output is copied into source, so a fresh R21
or later focused/race run is required.

R21 is the exact focused green for tracked patch SHA-256
`0e1a2dd0a1be5af5dc27bebc7a7c9f28fca7a43870c72bcecd3a7b404f85e12a`,
the unchanged hint and simulation patches, inner runner SHA-256
`2b05998a2f2bc1375a8c956f194786d8c2115ccdbe1046b109f10b47efbde07d`,
and outer runner SHA-256
`62505c62efcda8906604c660ee7d4da5bc57847e8b5e5ea1c8c7e6c27e91bb46`.
Its downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/frontier-r21/evidence.tar`, SHA-256
`e6a47fe1bed037155aa08e6b3b0b5e8c3b39547a9f4a2926612f430581881b8c`.
Formatting, the explicit deadband test, scheduler and observer focused tests,
all four deterministic frontier simulations, combined focused packages, race,
and overall all returned zero. Safe aggregate stayed `400`; harmful-soft stayed
`154 -> 300`; beneficial-soft retained `308` and `320`; hard still exposed
decode 7 exactly once and never attempted 8. R21 authorizes only a fresh full
matrix for the v5 predictor tree, not versioning, release, image, deployment,
Router change, or live traffic.

Full-r3 is the complete pre-version builder green for the v5 predictor tree. It
used tracked patch SHA-256
`16c83c395800443bf28ce6fa61d44d1079c17945f71261693b28c96136dec634`,
the unchanged hint and simulation patches, exact v0.10.7 archive SHA-256
`087cad84f637444cb93ae684a90b7d2df62bd55de412760ef4c8898d9c4afcac`,
inner runner SHA-256
`68a2eb48f7201b86187e22de18e4f89c7210889462b3d9eeaddfe3c6dba73871`,
and outer runner SHA-256
`2b51cab7d636ce8961bff53d4934fa25ea77c5be61c3a33bc66a4b6f9cfa1ae5`.
The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/full-r3/evidence.tar`, SHA-256
`08e8f70148fc67f54be24cb327797e968b60af94b7e92e510f93ea5ed4b8bb6c`;
the archive, internal checksum manifest, inputs, and every status were verified.
Low-flow/self-lock and deadband boundaries, formatting, vet, build, all tests,
all race tests, deterministic goodput/frontier simulations, both-order baseline
comparisons, and every benchmark command returned zero.

Every expected benchmark group contained exactly five samples. Candidate
predictor medians were `1.683/1.700 us`, `128 B, 1 alloc`, versus exact v0.10.7
`0.9340/0.9341 us`, `152 B, 2 allocs`. Aggregate lookup medians were
`460.3/467.7 ns` versus same-candidate no-curve `451.9/453.3 ns`, approximately
`1.9%/3.2%` overhead with zero allocations; aggregate observation was
`445.8 ns`, zero allocations. Direct lexical hint medians were `104.0 ns` at
1 KiB, `102.2 ns` at 64 KiB, and `100.5 ns` at 2 MiB, all zero-allocation.
Complete-estimator additive medians were `103.6/103.2 ns` at 1 KiB,
`96/88 ns` at 64 KiB, and `462/241 ns` at 2 MiB, all zero-allocation and below
the 1-microsecond gate in both orders. Deterministic completion-token goodput was
`44,704` with zero protected TPS/TPOT/KV, preemption-proxy, false-accept, or
reservation-leak violations. Safe, harmful-soft, beneficial-soft, and hard
frontier outputs remained `400`, `154 -> 300`, retained `308/320`, and exactly
one hard-knee exposure respectively.

Final review pass 1, model and causality: the first audit found that an exact
`current <= previous` comparison did not implement the promised preference for
lower QoS debt at throughput-equivalent buckets. The fixed 1% relative deadband
and predictor identity v5 correct that gap. R21 and full-r3 prove both deadband
sides while retaining beneficial yellow-band throughput. Input hint, per-user
red/soft QoS evidence, and direct aggregate generation-token evidence remain
independent; all decisions and reservations still occur before forward, and
feedback affects only later requests.

Final review pass 2, safety, lifecycle, efficiency, and SOLID: no further
executable defect was found. `Manager` remains the single atomic
check/predict/reserve and lifecycle-reconciliation owner. The JSON estimator and
fixed-budget lexical sensor, input-size calibrator, vLLM stable-window producer,
learned scheduler, HTTP adapter, and Router/log/metrics publishers have separate
narrow responsibilities. All maps, sample slices, observations, deferred
outcomes, retired reservations, and metric labels are bounded. Epoch/reset,
preemption, waiting, stale/fetch failure, cancellation, disconnect, completion-
before-poll, early resource release, and shutdown paths preserve conservative
fallback and release semantics. Prediction keeps O(1) adjacent aggregate
lookups, the hint and aggregate paths allocate zero in their isolated hot-path
benchmarks, and full race tests are green. TTFT is observation-only; no cache,
model asset, routing, Router source, or vLLM source behavior was introduced.

Final review pass 3, evidence and release boundary: R21 is exact focused/race
green and full-r3 is exact complete builder/performance green for its hashes.
Red R18 and R20, performance-red full-r1, pre-deadband full-r2, and all older
hashes are retained without being mislabelled as current release evidence. The
runtime version, OCI label, and current README still identify v0.10.7, so this
is not yet a v0.10.8 versioned build. The next permitted actions are to unify
those identities as v0.10.8 and rerun an exact clean builder version gate. No
commit, tag, push, image, registry digest, Compose integration, CVM deployment,
Router change, or live-traffic validation exists yet for v0.10.8.

##### v0.10.8 seventh correction — exact version gate and final throughput-first interpretation

The latest user clarification confirms the fifth and sixth corrections. The
controller is not required to hold every user at the nominal yellow TPS target.
It may accept a bounded, temporary yellow-band TPS/TPOT debt when the mature
higher-concurrency bucket increases useful aggregate completion throughput. The
learner should progressively avoid repeated debt only when that concurrency is
unproductive; it must not learn a hidden yellow admission threshold or trade
away measurable throughput merely to make the yellow counter zero. The active
optimization order remains hard resource and red QoS safety, useful aggregate
completion-token throughput, then lower yellow debt among throughput-equivalent
choices. This clarification requires no executable change after the v5 1%
measurement-noise deadband because the harmful-soft and beneficial-soft tests
already prove both sides of the required tradeoff.

Full-r4 is the exact versioned remote-builder green for the v0.10.8 executable
tree. It reconstructed the candidate from base archive SHA-256
`07ea9c43f132f134497bd0ecc279a4eb32a01b3576d80d7e6641fcb23b387dde`,
tracked patch SHA-256
`e2350aa57c0ee76e7cef60f0514c52c003bfcbf70e66e0e067695b117c552113`,
unchanged hint patch SHA-256
`f389e6cc9b373f316e3cfed7a3b829904ed86d60af79498a271d307f65b79bae`,
unchanged simulation patch SHA-256
`ac3da115aebce6182cff426450582c243c289cbe6424cf9564def848f43f08c3`,
and exact v0.10.7 archive SHA-256
`087cad84f637444cb93ae684a90b7d2df62bd55de412760ef4c8898d9c4afcac`.
The full-matrix inner, version-contract inner, and outer runner SHA-256 values
were respectively
`68a2eb48f7201b86187e22de18e4f89c7210889462b3d9eeaddfe3c6dba73871`,
`4b1df26add9600254ae2f14d846a6fdb986369b33bd52fbc50368a90c87fbdf8`,
and
`43b4b2ebc049751deefe3e1718d72d55bdf12fc45f48a67933572611eeb61760`.
The pinned Go image digest remained
`sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`
with actual builder image ID
`sha256:f14dd5573539be535f8d24abe277d750b937d584b6850ed0aed839bc737747f5`.

The downloaded evidence is
`tmp/pig-v0108-use1-cb-20260803/full-r4/evidence.tar`, SHA-256
`9387fd94dc86d981cf5a65968de547258ecd0de07cee91a280a665c1d7a1283b`.
The local copy matches the remote hash; all 60 internal manifest entries were
independently verified, and all 28 status files are zero. Formatting, vet,
build, all tests, all race tests, low-flow/self-lock/deadband boundaries,
deterministic goodput/frontier tests, both-order v0.10.7 comparisons, and every
benchmark command passed. The compiled binary SHA-256 was
`94b85099a8d78f4535c38fb03af3ed7d66bc14c8724c14386e1cfbe3b4629ed6`;
its actual startup line identified `PIG-v0.10.8`. The source contract also
proved runtime constant `PIG-v0.10.8`, OCI version label `0.10.8`, current
README image tag `v0.10.8`, and current advanced/observability documentation,
with no non-historical `0.10.7` in those release-identity files.

Every expected benchmark group again contained exactly five samples. Candidate
predictor medians were `1.731/1.705 us`, `128 B, 1 alloc`, versus exact v0.10.7
`0.9331/0.9332 us`, `152 B, 2 allocs`. Aggregate frontier lookup medians were
`460.9/461.5 ns` versus same-candidate no-curve `450.7/446.6 ns`, approximately
`2.26%/3.34%` overhead with zero allocations; aggregate observation was
`445.0 ns`, zero allocations. Direct lexical hint medians were `104.9 ns` at
1 KiB, `103.5 ns` at 64 KiB, and `100.0 ns` at 2 MiB, all zero-allocation.
Complete-estimator additive medians were `104.3/104.6 ns` at 1 KiB,
`109/98 ns` at 64 KiB, and `698/-2510 ns` at 2 MiB, all zero-allocation and
below the 1-microsecond additive gate in both orders.

Deterministic predictive completion-token goodput remained `44,704` versus the
current threshold controller's `39,840` (`+12.21%`), with zero protected
violations across 21 scenarios. Safe frontier aggregate remained `400` versus
ablation `100`; harmful soft debt matured at decode 6, probed 7, and returned to
aggregate `300`; beneficial soft debt matured at decode 8, probed 9, and
retained aggregate `308 -> 320`; the hard knee exposed decode 7 exactly once
and never attempted 8. This is builder/simulation evidence, not a claim about
production GPU throughput or live per-user QoS.

This plan-only evidence record is the sole source change after full-r4 and does
not alter the executable tree, Dockerfile, current release-identity docs, tests,
or image output. Full-r4 therefore permits a final staged-path audit, one
v0.10.8 source commit, annotated tag, and push only to `pig-origin`. It does not
yet prove a builder-local image, published registry image, Compose integration,
CVM deployment, live readiness, Router enablement, or a 30-minute canary. Those
layers remain separate gates, and `use1-cb` must stay Router-disabled until the
published immutable image passes fresh shadow and Router-disabled enforce
validation.

##### v0.10.8 source publication and immutable image evidence — complete

The reviewed source was committed as
`9f135840806d4242ed34191cb3adcb1e9803b3d1` (`feat: release throughput-first
predictive admission v0.10.8`). Annotated tag object
`954dd88863a68ce586749117cae39bc0461dbe9d` names `v0.10.8` and dereferences to
that exact commit. Branch and tag were pushed atomically only to `pig-origin`;
the unrelated `origin` remote was not touched. The official `Publish Image`
workflow run
`https://github.com/Phala-Network/phala-inference-guard/actions/runs/30796047711`
completed successfully for head branch `v0.10.8` and head SHA
`9f135840806d4242ed34191cb3adcb1e9803b3d1` at `2026-08-03T08:07:05Z`.

An independent fixed-builder pull resolved the mutable tag to the deployment
input:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:c05bbb572292067a93dd1bdfce3da9b4ae0197e79e796f1d53ce2c0d551f1d3c
```

The builder image ID is
`sha256:7f4618ded6722d850e7448d31ccbfaa8392a7f175ee171a770a3593c1a76f68f`,
the OCI version label is `0.10.8`, and the image is `linux/amd64` with
`NVIDIA_VISIBLE_DEVICES=all`. The production image contract passed against the
immutable digest, including distroless/CGO/native-NVML requirements. Extracted
runtime binary SHA-256 is
`c11c1db7242b33bacfe119e24fb290677d65e8f617a0145569e626df67e9d158`,
and a real container startup line identified `PIG-v0.10.8` with predictive,
KV-admission, and dynamic controllers explicitly off for the isolated smoke.

The image evidence is
`tmp/pig-v0108-use1-cb-20260803/image-r1/evidence.tar`, SHA-256
`0f95608f216878871799f31041e95bc2cbddbb8f91ea0ef6dcc360a6eb272419`.
The downloaded copy matches the remote hash; all 19 internal manifest entries
were independently verified and all seven status files are zero. This completes
source commit/push/tag, official registry publication, immutable digest pull,
image inspection, production image contract, extracted binary identity, and
isolated runtime-version layers. It does not prove Compose integration, CVM
deployment, live readiness, Router enablement, real GPU traffic, or a 30-minute
canary. The next permitted action is a fresh read-only snapshot of the sole
authorized CVM and Router state, followed only by an exact Router-disabled
shadow candidate with byte-exact rollback.

##### v0.10.8 Router-disabled live learning recovery — algorithm red

The immutable v0.10.8 image was deployed only to the authorized CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` while Router upstream `use1-cb`
remained disabled. Shadow protocol, low-flow/self-lock, interim-response,
Router-disabled enforce request-scope, load-scope publication, metrics/log
agreement, recovery, and terminal-state gates completed before the learning
recovery probe. This did not authorize Router enablement.

The retained live runner is
`../tmp/pig-v0108-use1-cb-live-20260803/run-enforce-learning-recovery-v0108.ps1`,
SHA-256
`e58dc6874fa0c285e00e262eaaa55d98c138260093d7b9b80f976bd687153c5b`.
Its retained evidence is intentionally outside the nested repository under
`../tmp/pig-v0108-use1-cb-live-20260803/`.

Run r22 is a harness red and supplies no product conclusion. The original
prompt completed after only about 38 output tokens, so the sampler never
observed the required sustained `decode_running=2` condition and the third
request was not sent. It ended without cleanup errors, temporary paths,
preemption, or sticky state. The prompt was corrected before the next run.

Run r23 is a valid algorithm red. Its retained
`enforce-learning-recovery-r23/learning-recovery-failure.json` and Prometheus
snapshots establish:

- the two cold controlled requests both returned HTTP 200 with 31 prompt and
  4096 completion tokens, complete SSE termination, approximately 490
  aggregate generation tokens/s, waiting zero, and preemption zero;
- the cold third request was rejected pre-forward with HTTP 429 and
  `new_tps_at_risk/static/load`, zero samples, Router protection active/applied,
  predictive/effective running two, and effective global limit two;
- after natural completion plus one further two-request training round,
  accepted, new-decode, and global residual samples each reached three,
  aggregate samples reached 39, aggregate cells reached two, and neither
  yellow TPS nor yellow TPOT debt was recorded;
- the approximate input learner was mature and stable: raw cold high 137
  tokens, lexical hint 36, learned upper 39, and backend actual prompt 31;
- with two matching requests running again, aggregate generation throughput
  was approximately 469--480 tokens/s, waiting remained zero, preemption
  remained zero, and vLLM KV usage was approximately 2.16%;
- despite that mature and healthy evidence, the same third request was again
  rejected pre-forward with HTTP 429 and
  `new_tps_at_risk/calibrated/load`, three samples, and exploratory false;
- both producing requests then terminated naturally. Accepted/global samples
  subsequently reached four and reservations, deferred outcomes, running,
  waiting, and Router protection returned to zero without cleanup errors or
  temporary paths.

The result proves that feedback reached the learner and changed the next
prediction from static to calibrated. It does not prove that learning failed
to collect samples. Instead, the mature prediction was still prevented from
forming the one-step exploratory fit needed to test decode concurrency three.
The v0.10.8 release is therefore not eligible for Router enablement or a
30-minute canary.

##### v0.10.9 active correction — request-shape-safe progressive-decode exploration

Source inspection identifies the causal mismatch in
`internal/runtime/predictive/scheduler.go`. Joining-user TPS and TPOT
one-bucket exploration currently requires
`decodePressureAtLeast(sample, query)`. Residual samples retain their immutable
admission-time aggregate context/KV features. Before a later joining request,
the already-running requests have generated more tokens, so current observed
active context and KV legitimately exceed the earlier admission-time values
even when:

- the joining request has the same raw request-complexity upper and decode
  horizon;
- the earlier decode bucket completed at high per-user and aggregate
  throughput;
- current KV is far below the hard limit;
- waiting, preemption, adverse evidence, and cooldown are all absent.

Requiring the older admission-time aggregate pressure to dominate that later
progressive-decode observation rejects the only mature optimistic transfer.
The query then retains the cold `base_completion_tps / 3` joining-user prior
and fails the hard red TPS floor even though the live two-user state has orders
of magnitude more measured headroom.

v0.10.9 will make the smallest coherent correction:

1. Change only joining-user TPS and TPOT *exactly-one-next-decode-bucket*
   optimistic exploration. Preserve strict pressure dominance for ordinary
   same/conservative residual reuse and for existing-user prefill protection.
2. For this one-step exploration, require the completed sample's immutable
   request shape to dominate the query request shape using both
   `RequestComplexityTokensUpper` and `DecodeHorizonUpper`. A larger input
   complexity or longer requested decode horizon must never reuse optimistic
   evidence from a smaller request.
3. Do not require the sample's admission-time aggregate active context/KV to
   dominate current progressive aggregate context/KV for this narrow transfer.
   Hard post-admit physical KV, active KV, workspace, lifecycle, confidence,
   red TPS, red TPOT, and preemption constraints remain in the atomic manager
   decision.
4. Preserve minimum samples, exact one-bucket progression, age and backend
   identity, bounded residual multipliers, adverse-evidence invalidation,
   waiting/preemption cooldown, aggregate-throughput frontier, 1% throughput
   deadband, and bounded learner cardinality.
5. Preserve the throughput-first contract: yellow TPS/TPOT debt alone neither
   rejects nor publishes Router protection; red TPS/TPOT and hard resource risk
   still reject before forward; TTFT remains observation-only; cache-aware
   admission and routing remain out of PIG scope.
6. Bump the predictor identity from `adaptive-tps-kv-v5` to
   `adaptive-tps-kv-v6` so no v0.10.8 residual can cross the semantic change.
   Manage the release version as v0.10.9 only after the complete source and
   evidence gates pass.

The required test-first acceptance sequence is:

1. Add a focused behavioral test that records at least three healthy matching
   samples at post-admit decode concurrency two, with high joining-user TPS and
   healthy TPOT.
2. Query concurrency three with the same request shape after existing requests
   have progressively increased current active context and KV above the
   samples' admission-time normalized pressure, while KV remains far below
   hard capacity and waiting/preemption are zero.
3. Prove exact v0.10.8 fails the intended assertion as
   non-exploratory `new_tps_at_risk`, not because of compilation, fixtures,
   configuration, or runner failure.
4. After the correction, require `calibrated`, `exploratory`, and `fit` for the
   same-shape one-step query.
5. Require both a larger `RequestComplexityTokensUpper` and a larger
   `DecodeHorizonUpper` to remain ineligible for that optimistic transfer.
6. Retain and rerun the hard-adverse test proving exactly one hard-knee
   exposure immediately blocks subsequent exploration, plus waiting,
   preemption, stale sample, identity, cardinality, race, reservation, and
   terminal lifecycle gates.
7. Run focused, race, deterministic simulation, both-order benchmark, and full
   clean-builder matrices only on the fixed remote Linux builder. Local Windows
   execution remains source inspection/editing and evidence review only.
8. Complete the three recorded review passes. Only then create/push the
   v0.10.9 source release and immutable image to `pig-origin`; never push the
   unrelated `origin` remote.
9. Re-run Router-disabled shadow and enforce protocol, low-flow/self-lock,
   learning, same-shape expansion, larger-shape protection, hard adverse,
   request/load-scope publication, log/metrics agreement, recovery, and
   terminal-zero gates on the sole authorized CVM.
10. Enable Router upstream `use1-cb` only after all preceding gates pass, then
    observe it continuously for 30 minutes against the old `use1-4c` baseline.
    Compare useful aggregate completion-token throughput, per-user TPS/TPOT
    distribution and yellow debt, KV/GPU utilization, running/waiting,
    preemption, reject/protection events, and log/metrics consistency. Any
    material safety, QoS, self-lock, or overprotection regression requires
    disabling only `use1-cb`, retaining evidence, and returning to this source
    loop.

##### v0.10.9 test-first and clean-builder evidence

The exact behavioral-red source archive is SHA-256
`ac596780bc4583f49fa7a591d8c2ba7aaf9e6e111691c47db4120e91f62ed55e`.
On exact v0.10.8, the new progressive-decode test compiled and the source had
an empty `gofmt` diff, but the intended assertion failed with a static,
zero-sample, non-exploratory prediction. Its joining-user TPS lower bound was
approximately 16.67 tokens/s against the 20 tokens/s hard floor. This is the
required product-behavior red rather than a runner, dependency, formatting, or
configuration failure.

The smallest production correction replaces aggregate
`decodePressureAtLeast` only for joining-user TPS/TPOT optimistic transfer to
exactly one higher decode bucket. The replacement requires the completed
sample to dominate both immutable joining-request dimensions:
`RequestComplexityTokensUpper` and `DecodeHorizonUpper`. Existing-user prefill
transfer, ordinary residual reuse, physical and active KV, workspace,
preemption, hard TPS/TPOT, aggregate frontier, adverse evidence, cooldown,
atomic reservation, and lifecycle checks are unchanged. Predictor identity is
isolated as `adaptive-tps-kv-v6`.

The focused green proves all of the following in one deterministic fixture:

- three healthy samples at post-admit decode concurrency two are sufficient to
  produce a calibrated, exploratory concurrency-three prediction for the same
  request shape despite legitimate progressive growth of current context and
  KV;
- the manager returns `fit` and atomically reserves the request;
- a request with either a larger complexity upper or a larger decode horizon
  cannot reuse the smaller optimistic evidence;
- explicit terminal cancellation releases the successful reservation and the
  manager snapshot returns to zero reservations;
- the runtime factory identity equals the v6 constant.

An initial green archive,
`bc65969e0fbc92dd24bad1c70d19cc0c1f953776f50711a9be92ed305572ba73`,
passed the predictive, server, simulation, and predictive race packages. It is
not the final candidate because the later review added explicit v6 factory
identity and terminal-release assertions. A subsequent full-r2 archive,
`7bf1db00f76d4b7b9b271a4ef364292308d9d78e57b69acc009f05e83c677670`,
failed `go vet` on a test-only suspect-OR expression comparing the predictor
version to two expected values. Splitting that expression into two independent
assertions corrected the test without changing production behavior.

Full-r3 source
`fe33a1f767774332eab8c9098d977fff1209eb50846753c494eba8adbcafac12`
then passed the complete matrix. The second safety/lifecycle review still found
that the new test did not explicitly release its successful manager
reservation and that the two larger-shape cases could be clearer in fixed
table order. Those test-only weaknesses were corrected before final evidence,
so full-r3 is retained evidence but is not inherited as the final source
candidate.

The final pre-release full-r4 source archive is SHA-256
`9c34feae485310c810bdff312f78c6359a972cfca1b4fb8b488af569be2bc954`.
It was tested only on builder `vllm-v024-patch-builder-use1` using the fixed Go
image
`docker.io/library/golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40`,
image ID
`sha256:f14dd5573539be535f8d24abe277d750b937d584b6850ed0aed839bc737747f5`.
The exact baseline is the v0.10.8 archive
`fea7ed29163cfb4fd4190f3b089edaea73aa28692dc6b760123fb46d9d291229`;
the fixed matrix runner is
`68a2eb48f7201b86187e22de18e4f89c7210889462b3d9eeaddfe3c6dba73871`.

Focused full-r4 tests passed for the progressive-decode and model-neutral
factory identity fixtures. The complete matrix then produced status zero for
every independent gate: `gofmt`, boundary matrix, `go vet`, build, all tests,
all race tests, deterministic goodput/frontier, both-order candidate/baseline
goodput, both-order predictor and estimator benchmarks, both-order aggregate
lookup, aggregate observation, lexical hint, overall, and wrapper. The
downloaded evidence archive is
`tmp/v0109-progressive-full-r4/evidence.tar`, SHA-256
`dc0247b58131ea9a35aed215b8ae6e3dd3b6b633715ac72b02ba40cd23da400b`.
Its internal `SHA256SUMS` independently verifies with zero mismatches, and its
`gofmt.diff` is empty.

Against the exact v0.10.8 baseline, both execution orders reproduce:

- completion-token goodput `44,704 -> 45,216`, approximately `+1.15%`;
- SLO-compliant completions `51 -> 53`;
- false denies `7 -> 3`;
- zero existing/new TPS violations, TPOT violations, KV hard violations,
  preemption proxy events, false accepts, and reservation leaks.

The only changed deterministic scenario is
`same_poll_short_burst_near_kv`, whose predictive goodput increases from 512
to 1,024 while protected violations remain zero. Frontier simulations still
reach safe decode eight at 400 completion tokens/s versus the 100 tokens/s
ablation; retreat from the harmful soft knee after probing seven; retain the
beneficial soft knee through mature decode eight while probing nine; and expose
the hard knee at decode seven exactly once without attempting eight.

The calibrated predictor medians are 1,690 ns versus 1,686 ns in order one and
1,698 ns versus 1,705 ns in order two, with 128 bytes and one allocation. Both
remain below the 2 microsecond gate and show no material release regression.
Aggregate-frontier lookup medians are 462.3 ns versus 448.4 ns (`+3.10%`) and
464.6 ns versus 450.4 ns (`+3.15%`), both below the 10% gate and both with zero
allocations. Aggregate observation has a 448.3 ns median and zero allocations.
The fixed-budget lexical hint medians are 102.2 ns for 1 KiB, 102.1 ns for
64 KiB, and 100.7 ns for 2 MiB, all with zero allocations.

One performance-evidence nuance must remain visible. In the complete matrix,
the unchanged 2 MiB estimator was 1.189 microseconds faster than baseline in
the first order but 1.253 microseconds slower in the second, narrowly crossing
the earlier per-order 1 microsecond additive heuristic. A predeclared
candidate/baseline/baseline/candidate confirmation with ten 3-second samples
per phase measured approximately `+0.474` and `+1.267` microseconds in the two
orders. The original and confirmation results are both retained; no favorable
rerun replaces them. The confirmation archive is
`tmp/v0109-progressive-full-r4/estimator-confirm-r1.tar`, SHA-256
`cf923fafafa713b14e593ef6df7da85a64e7a7be25a65c6cad9f01a3ae4e16e9`,
and its internal checksums verify with zero mismatches.

This is not accepted as evidence of a v0.10.9 estimator regression: recursive
diff proves the candidate and baseline `internal/domain/kvadmission` trees are
byte-identical, and `go.mod` and `go.sum` hashes are also identical. The package
imports only the standard library, so the measured direction and size cannot
be caused by this source change. For an unchanged package, byte identity is the
causal regression gate; absolute estimator latency and allocation remain
reported, while the separately changed predictor and newly consumed lexical
hint retain their explicit hot-path gates. This avoids both a false release
block from sequential builder drift and the invalid practice of rerunning until
a favorable sample appears.

##### v0.10.9 three-pass review conclusion before release identity

Review pass one, model and causality, found the original aggregate-pressure
comparison invalid only for the one-step joining-request exploration transfer.
The final correction is request-shape bounded, changes a pre-forward decision,
and is proven against exact v0.10.8 behavioral red evidence. It does not add
cache-aware admission, model-specific assets, routing behavior, or TTFT
protection. No further model/causality issue remains in the full-r4 source.

Review pass two, safety and lifecycle, added the explicit terminal release and
zero-reservation assertion and converted the larger-shape negatives to a fixed
table. Full race, atomic burst, cancellation, completion-before-poll,
disconnect, epoch invalidation, waiting/preemption recovery, hard adverse,
bounded learner, and terminal gates all pass. Hard KV and red QoS checks remain
outside and after the narrow optimistic-transfer compatibility test. No
further safety/lifecycle issue remains in the full-r4 source.

Review pass three, evidence and release, independently verified the source,
baseline, runner, Go image, downloaded evidence, internal checksums, every
status, goodput counters, protected-violation counters, and benchmark medians.
It retained and explained the unchanged-estimator timing variance rather than
discarding it. The evidence supports promotion from complete clean-builder
matrix to release-version preparation. It does not yet prove a v0.10.9 commit,
tag, pushed source, image, registry artifact, deployment, live readiness,
Router enablement, or 30-minute production canary. Those remain separate gates.

##### v0.10.9 exact versioned r5 matrix and builder-local image

After the pre-release reviews passed, the current release identities were
unified as:

- runtime `PIG-v0.10.9`;
- OCI label `0.10.9`;
- predictor `adaptive-tps-kv-v6`;
- README image tag `v0.10.9`;
- current Advanced and Observability references `v0.10.9`.

No non-historical v0.10.8 identity remains in those current files. The exact
versioned tracked-source archive is
`tmp/v0109-progressive-versioned-r5-source.tar`, SHA-256
`5712ef99baba2758e8f12cd12143a8c8366fea766a1c450b47cdd0a8e2c019c7`.
All 272 tracked paths were compared with the archive. There were no missing
paths. The sole raw-byte difference was the unmodified `LICENSE` worktree CRLF
versus Git-archive LF representation; both canonicalize to Git blob
`e62ec04cdeece724caeeeeaeb6ae1f6af1bb6b9a`. Every changed file matched exactly.
The builder upload independently reproduced the archive SHA-256.

Because the runtime constant changes the binary, no pre-version executable
evidence was inherited as the final release matrix. The exact r5 archive reran
the complete fixed matrix against the same exact v0.10.8 baseline and fixed Go
image. Source-version contract, `gofmt`, boundary matrix, `go vet`, build, all
tests, all race tests, deterministic goodput/frontier, every both-order
comparison, overall, and wrapper each returned status zero. The downloaded
evidence is `tmp/v0109-progressive-versioned-r5/evidence.tar`, SHA-256
`c5bbe57589c69c7c6227aa6f0c556925f281bd7603178fa680d2fb22dbaaabe1`.
Its internal checksums verify with zero mismatches and `gofmt.diff` is empty.

The final versioned matrix exactly reproduces the pre-version behavior result:
completion-token goodput `44,704 -> 45,216`, SLO-compliant completions
`51 -> 53`, false denies `7 -> 3`, and zero existing/new TPS, TPOT, KV-hard,
preemption-proxy, false-accept, and reservation-leak violations in both orders.
The safe, harmful-soft, beneficial-soft, and hard-knee frontier results are
unchanged.

Final versioned predictor medians are 1,700 ns versus 1,694 ns and 1,698 ns
versus 1,676 ns, with 128 bytes and one allocation. Aggregate lookup is
467.8 ns versus 449.7 ns (`+4.03%`) and 460.9 ns versus 449.4 ns (`+2.56%`),
with zero allocations. Aggregate observation is 450 ns with zero allocations.
Lexical hint medians are 104.7 ns, 102.5 ns, and 100.8 ns for 1 KiB, 64 KiB,
and 2 MiB, all with zero allocations. The unchanged 2 MiB estimator again
shows sequential noise: `+2.018` microseconds in the first order and `+0.397`
microseconds in the second. This remains retained under the byte-identity
causality analysis above rather than being hidden or used as evidence of a
changed estimator.

The exact r5 source then built builder-local image
`pig-v0109-versioned-r5:local` with:

- image ID
  `sha256:98cc4762e72b44aacc1f796590bc41503105e2841710cf83fbdc4e808b0ed588`;
- Linux amd64 size 29,447,023 bytes;
- OCI version `0.10.9`;
- entrypoint `/phala-inference-guard`;
- extracted binary SHA-256
  `57dc7cb7496c26c4805d8d21d0cff3fe2efe22a5451dd7e177efeea4b5c80a9e`.

The repository production image contract returned:

```text
PIG_PRODUCTION_IMAGE_CONTRACT_OK image=pig-v0109-versioned-r5:local version=0.10.9
```

The first outer image runner stopped after the successful build, successful
production contract, and successful binary extraction because the builder
container lacks the optional `file` command. This harness-only failure is
retained as status 127; startup had not yet been attempted. The resumed runner
used available `readelf`, returned status zero, started the same immutable image,
verified it remained running, captured both `PIG-v0.10.9` startup and
`pig_status v=PIG-v0.10.9`, and removed the temporary container. Build,
production contract, readelf, startup version, and resumed overall are all
zero.

The uncompressed builder evidence tar is SHA-256
`db97ad2ef08d82f72427536429b0aefd099bc69a9ff483f79af612f149b06be4`.
The first SCP transfer was interrupted by the gateway after 1,802,240 bytes and
is retained locally only as `image-evidence.scp-partial-r1.tar`, never as valid
evidence. The same immutable tar was gzip-compressed on the builder and
downloaded as
`tmp/v0109-progressive-versioned-r5/image-evidence.tar.gz`, SHA-256
`b2d87e419a4617d85214527b4c70bd7c5110d490f2e1be9566642b5379fa9f0b`.
Its internal `SHA256SUMS` verifies with zero mismatches, including the extracted
binary.

This completes the exact versioned complete-builder and builder-local image
layers. This evidence record itself is documentation-only and does not change
`Dockerfile`, `go.mod`, `go.sum`, `cmd`, or `internal`. A final source-identity
gate must prove those executable and image inputs byte-identical to r5 before
commit and tag. Registry publication, immutable registry pull, Compose, CVM,
Router, and live traffic remain unproven and unauthorized by builder-local
success alone.

##### v0.10.9 source release and immutable registry evidence

The final r6 source-identity gate proved the release archive differs from the
fully tested r5 archive only by this authoritative evidence record. Dockerfile,
`go.mod`, `go.sum`, `cmd`, `internal`, and the production image-contract script
remain byte-identical. The exact r6 source archive is
`tmp/v0109-progressive-release-r6-source.tar`, SHA-256
`7ac4cd0ab799cfbdc818350d3905cdfcb46c41102599b6c333bfedfc5b358686`.
The identity evidence is
`tmp/v0109-progressive-versioned-r5/release-r6-identity-evidence.tar`, SHA-256
`34636e5379092348a427ddbad603926c7dec704362a114e4ed86b4662854c385`.

Release commit `03fbec2ab81ecd2120adffeb64137cbade44418b` and annotated tag
`v0.10.9` were pushed only to the PIG repository remote `pig-origin`; the
peeled tag resolves to the same commit. The published Linux amd64 image is:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:58190cab622795a99a75b9c0d94ac92b920ca2a38703cba2f0dd26c8609894b5
```

An independent pull by that immutable digest reproduced builder image ID
`sha256:98cc4762e72b44aacc1f796590bc41503105e2841710cf83fbdc4e808b0ed588`,
binary SHA-256
`57dc7cb7496c26c4805d8d21d0cff3fe2efe22a5451dd7e177efeea4b5c80a9e`,
OCI version `0.10.9`, entrypoint `/phala-inference-guard`, the production image
contract, and startup identity `PIG-v0.10.9`. The registry evidence is
`tmp/v0109-progressive-versioned-r5/registry-r1-evidence.tar.gz`, SHA-256
`819c033de503212657779e7f423db51ad76e7983d9e1bdadf34f817a3517736d`.
Source release and registry publication are therefore complete. They do not by
themselves prove Compose integration, CVM deployment, live readiness, Router
enablement, or real-traffic throughput.

##### v0.10.9 fresh use1-cb preflight and shadow candidate

A fresh read-only preflight captured at
`2026-08-04T16:42:39.2773657Z` for the only authorized CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` found the platform `running` with
`in_progress=false`; PIG and vLLM containers were running; authenticated
models, PIG metrics, vLLM metrics, and attestation endpoints returned HTTP 200;
unauthenticated metrics returned HTTP 401; and the attestation response retained
non-empty NVIDIA evidence. Router upstream `use1-cb` remained disabled with
zero routed running/processed work. The backend itself was idle with zero vLLM
running/waiting/KV/preemptions and zero PIG reservations, shadow observations,
or deferred outcomes. Router reports its disabled, unpolled target metrics as
stale/not-ok; direct endpoint success proves that is expected disabled-route
observation state rather than target endpoint failure. The retained artifact
directory is `../tmp/pig-v0109-use1-cb-live-20260804/preflight-r1`, and its
exact-token secret scan is clean.

The byte-exact rollback Compose has SHA-256
`677b7f90099f2e17b1484373d1a69967acaf2448ec5077088be9a2ff1eea191b`.
It still runs the v0.10.8 digest in `enforce` mode with
`DYNAMIC_TTFT_ENABLED=false` and the existing five-second Router backpressure
hold. The generated Router-disabled v0.10.9 shadow candidate has SHA-256
`19117db7dc3df0102be18800dfed905853dc0121aba6a9694eaf03008e86ed4f`.
YAML parsing, exact-token secret scan, and byte-exact reverse substitution pass.
Its only two changes are the PIG immutable digest above and
`PREDICTIVE_ADMISSION_MODE=enforce -> shadow`; vLLM, HAProxy, ingress, model,
resource, and every other PIG setting remain unchanged.

No v0.10.9 deployment or Router mutation has occurred at this checkpoint. A
second fresh read-only predeploy snapshot must reproduce the rollback Compose
hash, Router digest and enabled set, disabled/drained target, endpoint health,
and idle lifecycle state before the exact candidate is eligible for Compose-only
shadow deployment without an env file. `use1-cb` must remain Router-disabled
through shadow and enforce gates.

##### v0.10.9 Router-disabled shadow deployment and live gates

The second read-only predeploy snapshot at
`2026-08-04T16:47:06.4285947Z` reproduced the exact rollback Compose SHA-256,
Router digest and enabled set, disabled/drained target, endpoint health, and
terminal-zero state. The exact candidate was then deployed Compose-only, with
no env file, from `2026-08-04T16:48:19.3965676Z` to
`2026-08-04T16:52:30.0676244Z`; the deployment command exited zero. This CLI
result was not treated as readiness.

The first postdeploy snapshot correctly found the new Compose SHA-256 and
immutable PIG image ID but HTTP 503 while vLLM was still loading. Readiness was
withheld. Logs then showed target and MTP weights loading, cache-hit AOT
compilation, CUDA graph capture, 89.1 GiB available KV memory, and a reported
KV capacity of 862,437 tokens. vLLM recorded `Application startup complete` at
`2026-08-04T16:57:54.887028243Z`; no fatal, CUDA OOM, EngineDead, or Xid occurred.
The fresh ready snapshot at `2026-08-04T16:58:16.6579951Z` proved:

- live Compose SHA-256 exactly
  `19117db7dc3df0102be18800dfed905853dc0121aba6a9694eaf03008e86ed4f`;
- immutable PIG digest
  `sha256:58190cab622795a99a75b9c0d94ac92b920ca2a38703cba2f0dd26c8609894b5`
  and image ID
  `sha256:98cc4762e72b44aacc1f796590bc41503105e2841710cf83fbdc4e808b0ed588`;
- `pig_version_info{version="PIG-v0.10.9"}` and
  `pig_predictive_admission_mode_info{mode="shadow"}`;
- authenticated models/PIG metrics/combined metrics/attestation HTTP 200,
  unauthenticated metrics HTTP 401, expected model
  `google/gemma-4-31B-it`, and non-empty NVIDIA attestation;
- `use1-cb` still Router-disabled and drained, with the Router digest and
  enabled set unchanged;
- intake open and zero reservations, shadow observations, deferred outcomes,
  backend running/waiting/KV, and preemptions.

The first 20-request cold low-flow runner, retained as
`../tmp/pig-v0109-use1-cb-live-20260804/shadow-lowflow-r5`, stopped on a harness
off-by-one assertion after all requests had completed. The runner incorrectly
required the third request's pre-forward estimate to consume the third sample,
but `PREDICTIVE_LEARNING_MINIMUM_SAMPLES` is three and that sample becomes
available only after the third request completes. The evidence proves requests
one through three used cold/fallback estimates and request four onward used
learned/hint estimates. All 20 input samples were accepted; 17 later estimates
used learned hint calibration; no invalidation, preemption, Router protection,
or terminal leak occurred. This is a harness finding, not an algorithm failure,
and the original failed artifact is retained.

The corrected runner primes four ordinary requests, applies the safe
low-information-ratio shape fifth, and checks recovery sixth. Its first
20-request run, `shadow-lowflow-r6`, passed correctness, learning, mature-hint,
low-ratio, metrics-parity, TTFT-off, false-lock, preemption, and terminal gates
but retained a live histogram failure: prediction was `20/20 <= 250 us`, while
estimator classification was `17/20 <= 250 us` and `20/20 <= 1 ms`, with a
142.3 us mean. The preceding r5 window had prediction and estimator both
`20/20 <= 250 us`, with estimator mean 72 us. Neither result was discarded.

A predeclared larger confirmation then ran 100 serial, terminal-zero-separated
requests rather than rerunning until favorable. The retained
`shadow-lowflow-r7-confirm100/summary.json` is fully green: all 100 client
responses were HTTP 200; all 100 estimates used learned input-size and lexical
hint calibration; no input or hint invalidation, Router activation, preemption,
false/sticky lock, reservation, prefill, shadow/deferred, running, or waiting
state remained; PIG and combined metrics matched; prediction and estimator were
both `100/100 <= 250 us` and `100/100 <= 1 ms`. The correct interpretation is
that the fixed-budget path meets the declared larger-sample live gate while
rare host/runtime scheduling can still move complete request classification
from below 250 us into the 250-1,000 us bucket. This nuance remains release
evidence rather than being hidden.

Five direct protocol gates then passed HTTP 200 for ordinary chat, SSE streaming
with usage, a required function tool call, strict JSON-schema output, and CJK
input; no response body was retained. The explicit `Expect: 100-continue` gate
sent a 1,600,119-byte request containing 1,600,000 trailing whitespace bytes.
The client observed both provisional HTTP 100 and final HTTP 200, while PIG
recorded exactly one 2xx and zero 1xx final responses. Shadow predicted risk but
correctly forwarded; vLLM succeeded once; input feedback, completion-observer
attach/claim/usage/terminal, and shadow create/terminate each occurred exactly
once; enforced rejects, TPS rejects, Router protection, and preemptions remained
zero; every active lifecycle state returned to zero.

The final fresh shadow snapshot at `2026-08-04T17:18:30.3141694Z` retained the
exact Compose/image/Router identity and healthy authenticated endpoints. Its
cumulative direct-test result was 146 vLLM successes, 145 predictive fit
decisions, one shadow risk, zero unknowns, zero enforced rejects/failures/vLLM
errors/preemptions, and terminal-zero state. The independent 45-minute log and
serial audit in `../tmp/pig-v0109-use1-cb-live-20260804/shadow-final-logs-r11`
captured one Engine initialization and one application-startup marker; vLLM,
PIG, and serial critical-pattern counts were all zero and the secret scan was
clean.

The Router-disabled shadow gate is therefore complete, with both harness
findings explicitly retained. The next eligible mutation is not Router traffic:
generate a candidate from the fresh byte-exact live shadow Compose and change
only `PREDICTIVE_ADMISSION_MODE=shadow` to `enforce`. Repeat fresh predeploy,
Compose-only deployment, readiness, protocol, cancellation/recovery,
request-scope and load-scope publication, progressive same-shape learning,
larger-shape rejection, latency, metrics/log agreement, and terminal-zero
gates before `use1-cb` may be enabled.

##### v0.10.9 Router-disabled enforce deployment and live gates

The enforce candidate was generated from the fresh byte-exact live shadow
Compose and changed only `PREDICTIVE_ADMISSION_MODE=shadow` to `enforce`.
Reverse substitution reproduced shadow SHA-256
`19117db7dc3df0102be18800dfed905853dc0121aba6a9694eaf03008e86ed4f`;
the enforce candidate SHA-256 is
`ccf2613730258e33ac83dd00aabe37e0070056476faec20fb674f00e7f6b57c2`.
YAML parsing, exact-token secret scan, and fresh predeploy drift/idle/Router
checks passed. Compose-only deployment, without an env file, ran from
`2026-08-04T17:23:35.9193102Z` to `2026-08-04T17:27:55.5624668Z` and exited
zero. Readiness was again withheld through the expected HTTP 503 model-loading
window. vLLM loaded the target and MTP weights, reported the same 89.1 GiB /
862,437-token KV capacity, completed graph and multi-modal warmup, and recorded
`Application startup complete` at `2026-08-04T17:33:20.408804593Z`.

The ready snapshot at `2026-08-04T17:33:55.2341565Z` proved exact enforce
Compose/image identity, `PIG-v0.10.9`, predictive mode `enforce`, legacy KV mode
`off`, TTFT protection zero, expected model, authenticated endpoint and
attestation success, unauthenticated metrics HTTP 401, terminal-zero state, and
the unchanged Router-disabled `use1-cb`. Five ordinary chat/stream/tool/schema/
CJK protocol gates then all returned HTTP 200.

The request-scoped hard gate sent a 1,600,090-byte completion request. PIG
returned pre-forward HTTP 429 with `reason=kv_over_budget`, `source=static`, and
`scope=request`; backend accepted/success/prompt counters did not change;
Router backpressure remained inactive and raw/effective capacity identical;
PIG and combined metrics matched; and the matching reject log contained no
request content. A subsequent 120-byte request returned HTTP 200, terminal state
returned to zero, and preemptions remained unchanged.

The first progressive learning runner, retained as
`../tmp/pig-v0109-use1-cb-live-20260804/enforce-learning-recovery-r18`, stopped
at its old assertion that three global samples imply same-shape maturity. It
started with one previously qualified short protocol sample. After one cold
long pair and one training pair, metrics showed three accepted/global/new-decode
samples but three distinct learner cells. Only two samples had the joining
request's 4,096-token decode horizon. The third long request correctly remained
`source=static`, `samples=0`, and was rejected load-scope 429 rather than using
the smaller-horizon sample. The runner's global-count assertion was invalid for
the v0.10.9 request-shape safety contract. Its failure, cleanup, request results,
and metrics are retained; cleanup had zero errors and no temporary paths.

That rejection's two already-admitted long requests then completed and produced
the third compatible long sample. A focused continuation gate in
`../tmp/pig-v0109-use1-cb-live-20260804/enforce-progressive-r19` repeated the
same-shape decode two-to-three transition. The third request was admitted with
`reason=fit`, `source=calibrated`, `samples=3`, `exploratory=1`; predictive and
observed running both reached three; and all three streams completed HTTP 200
with 4,096 completion tokens each. A subsequent small request returned 200;
PIG/combined metrics matched; preemption delta was zero; terminal state and
secret scan were clean. Together r18 and r19 prove both sides of the v0.10.9
boundary: incompatible smaller horizon evidence is not reused, while three
compatible same-shape samples unlock bounded progressive admission.

The two r18 load-scope rejects also proved synchronous publication. PIG and
combined metrics showed active/applied protection, one activation each, typed
load scope, matching reason/source/sample state, predictive/raw/effective
running, and effective global limit equal to current running. PIG logs contain
one `event=activated` and one `event=router_capacity_applied` for each exact
activation ID. One recorded example changed raw global limit 50 to effective
limit 2 while two long requests were active. After the finite hold and drain,
protection returned inactive; no sticky capacity suppression remained.

The inherited fast-recovery runner was deliberately not used because it
requires a very small request to be rejected whenever three long decodes are
running. That requirement conflicts with the active throughput-first contract:
a small request may be admitted when it increases aggregate useful throughput
without crossing a red floor. A corrected cancellation gate instead admitted
three already-proven same-shape streams, observed predictive and backend decode
running at three with waiting zero, then terminated all clients. Process
cancellation took 42.173 ms; reservation/deferred/backend running and waiting
state converged to zero in 2,680.018 ms; a small recovery request immediately
returned HTTP 200. Resource-release failure and preemption deltas were zero;
metrics parity, TTFT-off, terminal-zero, cleanup, and secret-scan gates passed.

The final enforce snapshot at `2026-08-04T17:45:24.3450921Z` retained exact
Compose/image/Router identity and endpoint health. Cumulative enforce evidence
contained 23 attempts, 20 fit decisions, three risk decisions and three enforced
rejects, zero unknown/predictive/resource-release failures, 17 vLLM successes,
zero vLLM errors/preemptions, and terminal-zero state. The independent log and
serial audit in `../tmp/pig-v0109-use1-cb-live-20260804/enforce-final-logs-r22`
captured one Engine initialization and one startup-complete marker; all vLLM,
PIG, and serial critical-pattern counts were zero and the secret scan was clean.

Router-disabled enforce is therefore complete. The current live Router baseline
must be treated as fresh truth: at these snapshots only `use1-19` was enabled;
historical `use1-4c` topology is not the current enabled set. Before canary,
capture one more drift/idle snapshot, verify every active state remains zero,
then change only `use1-cb.enabled=false` to `true`. Observe for a fresh continuous
30-minute interval beginning with real routed traffic. Use current `use1-19` as
the primary live comparator and historical `use1-4c` only as separately labeled
context. Any safety, lifecycle, publication, QoS, or obvious throughput finding
must disable only `use1-cb`, preserve evidence, and return to the source loop.

##### v0.10.9 real-traffic canary failure and v0.10.10 repair loop — active 2026-08-04

The fresh r23 preflight reproduced exact enforce Compose SHA-256
`ccf2613730258e33ac83dd00aabe37e0070056476faec20fb674f00e7f6b57c2`,
the immutable v0.10.9 image, Router digest
`sha256:a2a4e0f648d8ad6d81772211832b8986c0378f6d1a3c8f023ea96a2aa0044ff1`,
the enabled set containing only `use1-19`, endpoint/auth/attestation health, and
terminal-zero target state. The r24 Router mutation did change only
`use1-cb.enabled=false -> true`, but the old supervisor compared equivalent
enabled sets in different orders and treated them as drift. Its emergency path
immediately disabled only `use1-cb`. During that approximately one-second
window Router processed three target requests; PIG recorded three attempts, two
fits, one risk/reject, and matching `activated` plus `router_capacity_applied`
events. There was no preemption, hard adverse evidence, yellow debt, or
lifecycle failure. r24 is harness-red and not a valid algorithm canary. The
state helper now sorts and de-duplicates both expected and actual enabled sets.

Fresh r25 preflight again proved the disabled target and exact baseline. The r26
canary then normalized the enabled-set comparison and began its observation at
the first real routed request at `2026-08-04T18:09:41.0702112Z`. It stopped after
54.664 seconds and automatically disabled only `use1-cb` for:

```text
red_tps_or_tpot_adverse_evidence_increased
protection_active_applied_mismatch
```

The target received 44 Router requests, made 44 predictive attempts, recorded
22 backend successes and 20 enforced rejects, and finished with 21 risk and
three unknown decisions. It produced 5,595 completion tokens, approximately
102.35 aggregate completion tokens/s, and request TPOT mean 8.280 ms. There were
zero preemptions, vLLM error completions, persistent waiting windows,
resource-release failures, reservation/deferred leaks, or yellow TPS/TPOT debt,
but eight hard-adverse learning events. The comparator `use1-19` produced 66,588
completion tokens, approximately 1,218.14 aggregate completion tokens/s, and no
new preemption in the same wall-clock window. The nodes had materially different
traffic, active sequence counts, and Router allocation, so this is not an
equivalent-load throughput A/B and the raw 102-versus-1,218 ratio must not be
used as an algorithm effect estimate.

The first production defect is a load-protection publication discontinuity.
r26 sample zero exposed active/applied protection with Router running and limit
both one. At the next scrape, the fixed five-second lease was still active but
raw and predictive running had briefly reached zero; the current capacity code
therefore published `active=1`, `applied=0`, and restored the raw global limit
50. A Router-visible idle instant can consequently punch through the lease and
immediately deliver more traffic. This exactly reproduces the previously
reported mismatch between PIG protection state, metrics/logs, and continued
Router traffic. An active load-scope lease must publish a minimum effective
running sentinel of one until its bounded hold expires. Expiry must still
restore raw capacity immediately; this finite sentinel must not create a
low-flow, drain, sticky-zero, or self-lock condition.

The second production defect is unqualified QoS attribution. Stock vLLM did not
return per-response `mean_itl_ms` or `generation_time_ms`; final telemetry was
39 local TPS outcomes, zero backend outcomes, nine missing, and six rejected.
The local fallback computes decode time as PIG wall time minus semantic TTFT.
That interval includes downstream/Router/client response-copy stalls and is not
pure backend decode time. Nevertheless the v0.10.9 scheduler treated every
local TPS/TPOT value as hard evidence. In the r26 adverse window, the backend
`request_time_per_output_token_seconds` histogram recorded 19 new completions:
12 at or below 10 ms, six at 10-25 ms, one at 25-50 ms, and none above the
configured 50 ms red TPOT boundary. The eight hard-adverse increments are thus
not qualified backend red evidence and must not be preserved by lowering the
red floor or relaxing the canary.

v0.10.10 must make QoS evidence source-aware before another release:

1. Backend-provided response mean-ITL or generation duration remains qualified
   request QoS evidence after existing structural validation.
2. Local semantic timing remains a cheap diagnostic, but it may train TPS/TPOT
   or create hard adverse evidence only when a fresh, stable, overlapping,
   model-neutral vLLM generation-counter window corroborates its direction.
3. A local-red outcome with fresh backend per-active-decode TPS/TPOT clearly
   inside the red floor is censored from QoS learning and cannot create hard
   adverse evidence. Missing or stale backend corroboration is likewise
   unqualified; input-size feedback remains independently eligible.
4. Genuine response-backend red evidence, or local red evidence corroborated by
   a fresh stable backend-red window, still causes immediate bounded retreat.
   Waiting and preemption remain independent hard load-pressure evidence.
5. Telemetry must distinguish backend-qualified, local-corroborated,
   local-censored, missing, and structurally rejected outcomes; it must expose
   hard adverse dimensions (`existing_tps`, `new_tps`, `tpot`) and bounded
   observed TPS/TPOT histograms without request/model labels.
6. The scheduler/predictor semantic identity advances from
   `adaptive-tps-kv-v6` to `adaptive-tps-kv-v7`; older residuals must not cross
   this qualification boundary.

Availability-scope churn seen in r26 (three profile-unknown activations) must be
explained against observer freshness and epoch transitions during the same
repair. It must not be hidden with a generic grace period unless a focused red
test proves a harmless transient. The log auditor must also classify the one
retained multi-modal request `ValueError` separately from runtime-fatal
EngineDead/OOM/Xid/restart evidence; the traceback did not restart vLLM or PIG.

The first post-canary read-only repair preflight r29, captured at
`2026-08-04T18:27:08.8577568Z`, proves safe isolation after the emergency
disable: `use1-cb.enabled=false`, target Router running zero, enabled set only
`use1-19`, exact Router digest above, exact v0.10.9 Compose/image identity,
authenticated endpoint and attestation HTTP 200, unauthorized metrics HTTP 401,
and zero PIG reservations/deferred outcomes or vLLM running/waiting/KV. The
artifact directory is
`../tmp/pig-v0109-use1-cb-live-20260804/canary-repair-preflight-r29` and its
secret scan is clean.

The mandatory loop is now: add focused red tests for the lease discontinuity,
slow-downstream/local-censoring path, genuinely corroborated hard-red path,
metrics source/dimension parity, finite expiry, and no false/self lock; implement
the smallest coherent v0.10.10 slice; complete the three review passes; run the
entire matrix only on the approved remote builder; publish a new immutable
source/tag/image without moving v0.10.9; then repeat Router-disabled shadow and
enforce gates on `use1-cb`. Only after those gates pass may exactly `use1-cb` be
enabled for a fresh continuous 30-minute real-traffic canary. Any new material
finding disables only `use1-cb` and restarts this repair loop. The Goal remains
active until a complete canary has no obvious safety, QoS, lifecycle,
observability, or throughput defect.

##### v0.10.10 focused implementation evidence and three-pass review — active 2026-08-04

The lease discontinuity, source-qualified QoS path, hard-adverse dimensions,
bounded qualified-QoS histograms, and later-decision causality are implemented
in the current uncommitted candidate. The runtime and OCI identities, README
image example, Advanced contract, and Observability contract now consistently
name v0.10.10. The predictor identity is `adaptive-tps-kv-v7`; the model-neutral
estimator remains `json-cost-lexical-hint-v2`. This working-tree identity update
invalidates focused r11 as final release evidence even though it does not alter
the r11 behavioral conclusions. A new exact archive must pass the complete
builder matrix before commit, tag, image publication, or live deployment.

The test-first review found and closed an additional causal defect after the
initial canary repair. A stable completion-QoS window could remain fresh after
the next observer interval became invalid because its running set changed or
its generation counter made no progress. A long request that still overlapped
the older interval could then reuse stale qualified evidence. The focused test
`TestPredictiveVLLMObserverInvalidStableWindowClearsPriorCompletionQoS` failed
for the intended behavioral reason:

- exact source archive:
  `../tmp/pig-v01010-builder-20260804/candidate-red-r7-source.tar.gz`;
- archive SHA-256:
  `949526de1c76250bf592b12175ec38abeef0bfdd0bfba5e92753bc536b8f7794`;
- builder log:
  `../tmp/pig-v01010-builder-20260804/candidate-red-r7.log`;
- log SHA-256:
  `cdb6b10798d73f7fc3e73b5cee7a15398e9a6d7a9710b0e6e4cb76e1dfa9477f`;
- exit status: `1`;
- exact failure:
  `invalid running-transition window retained prior completion QoS evidence`.

The correction clears old evidence whenever the immediately preceding interval
is not qualified, including a running transition or no generation progress.
The next genuinely stable interval may publish new fresh evidence. Intermediate
r8, r9, and r10 were fixture/expectation corrections, not product red or green:
r8 omitted the production hard target and therefore correctly classified a
100 ms TPOT against its 10 ms prior as adverse; r9 and r10 incorrectly expected
one idle adverse request to lock a later idle request, contradicting the
required low-flow self-lock guard.

Focused candidate r11 uses a real `CountCoordinator`, `LearnedScheduler`,
adapter lifecycle, backend-corroborated local timing, and the pre-forward
decision path. It keeps one decoded request active, admits a joining request,
records qualified red feedback only for `new_tps`, and proves the next joining
request is rejected pre-forward as calibrated `new_tps_at_risk`; all
reservations finally return to zero. The exact evidence is:

- source archive:
  `../tmp/pig-v01010-builder-20260804/candidate-green-r11-source.tar.gz`;
- archive SHA-256:
  `ebdbae035c61735d73d921bda7d6394c15314524528a492c8659a522fe4e4cdd`;
- archive file count: `273`, including the new production file
  `internal/app/server/predictive_qos_histogram.go`;
- focused builder log:
  `../tmp/pig-v01010-builder-20260804/candidate-green-r11.log`;
- focused log SHA-256:
  `7b7c6c508a024437cff445390a12047d2af1a4c7b59d271c0e9cc85fdd1946ae`;
- empty `gofmt -d cmd internal` log:
  `../tmp/pig-v01010-builder-20260804/candidate-green-r11-gofmt.log`;
- empty gofmt log SHA-256:
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`;
- successful packages: `./internal/app/server`,
  `./internal/runtime/predictive`, and `./internal/observability/metrics`.

Review pass 1, model and causality, is complete for the current implementation.
Backend response timing remains directly qualified. Stock-vLLM local wall time
is only a direction check; fresh, stable, overlapping generation-counter
evidence supplies the learned magnitude. A local/backend red-green disagreement,
missing or stale evidence, no overlap, waiting, preemption, counter reset,
running transition, or no generation progress censors QoS learning. Valid
input-size feedback remains independent. The r11 end-to-end test proves hard
feedback changes a later concurrent pre-forward prediction rather than the
already-forwarded request. TTFT remains observation-only.

Review pass 2, safety, lifecycle, SOLID, and efficiency, is complete for the
current implementation. Observer polling, close, load-pressure, learning, and
epoch invalidation release the observer lock before calling the coordinator or
learner; no observer-lock to adapter-lock reverse path was found. Metrics
scraping copies the fixed histogram struct under the adapter lock, then
materializes cumulative Prometheus buckets after unlock. Completion remains a
fixed bounded `O(log B)` update. Existing lifecycle tests cover early resource
release, deferred terminal handling, shadow records, cancel/error/timeout,
exactly-once release, and concurrency. The active load lease publishes
`MinimumRunning=1` until exact expiry, then returns immediately to raw capacity.
The idle admission guard deliberately prevents a single adverse request from
self-locking an otherwise idle node.

The three r26 availability-scope activations at `18:09:35.789Z`,
`18:10:27.637Z`, and `18:10:35.819Z` coincide with vLLM waiting-pressure polls:
the waiting-pressure event counter grew from zero to three. Preemptions stayed
zero; there was no epoch reset, EngineDead, OOM, Xid, restart, or observer
freshness failure. `Healthy()` intentionally closes temporary intake while the
last coherent sample reports waiting work. Therefore the activations are real
hard load-pressure evidence, not unexplained availability jitter. No generic
grace is added; the next coherent `waiting=0` poll and metrics scrape recover
capacity, as covered by waiting/low-flow recovery tests. The separate
`request_size_unknown` event is not classified as availability pressure.

Review pass 3, evidence and release, remains open. r7 is accepted behavioral
red and r11 is accepted focused green only. No complete versioned matrix,
release commit, v0.10.10 tag, builder-local image, registry artifact, Compose
candidate, deployment, readiness result, Router enablement, or production
success exists yet. `use1-cb` must remain Router-disabled until a new exact
v0.10.10 archive passes formatting, focused/full tests, vet, targeted/full race,
build, deterministic and goodput simulations, two-order benchmarks,
builder-local image, and image-contract gates on the approved remote builder.

##### v0.10.10 exact versioned r12 matrix and paired performance supplement — complete 2026-08-04

The exact versioned candidate archive contains 273 files, including the new
production histogram implementation. Its path is
`../tmp/pig-v01010-builder-20260804/candidate-versioned-r12-source.tar.gz` and
its SHA-256 is
`4defb9b8263913ecae650a72a83308837fe7620cc318a50882fe7372e3f9cd46`.
The exact immutable v0.10.9 baseline is tag/commit
`03fbec2ab81ecd2120adffeb64137cbade44418b`; its 272-file Git archive SHA-256
is `7b05fad0213a958f630bdc34853adf5e9ca40a9da6d3cd6b38bf4ce31f04abea`.
The fixed r12 runner SHA-256 is
`118ef15f7be4f88bccd78b9613740afa7de09cf6ba878737acc50be0e64e8b14`.
The builder independently reproduced both archive hashes before extraction.

The matrix ran only inside `pig-ubuntu-builder` on approved builder CVM
`4f167f6e-4c50-415f-99f2-94b65652beba`, with Go 1.24.5, linux/amd64,
`CGO_ENABLED=1`, eight CPUs, Docker client 29.1.3, and Docker server 25.0.3.
Before the run, the dedicated builder had only about 2 GiB free because its
reproducible Go build cache occupied about 3.7 GiB. `/usr/local/go/bin/go clean
-cache -testcache` removed only that regenerable cache and restored about
5.3 GiB free; module cache, work directories, evidence, running containers, and
production state were not removed.

Every independent r12 stage returned status zero:

- source/version contract and empty `gofmt`;
- focused server, predictive-runtime, and predictive-metrics tests;
- `go vet ./...`, `go test ./...`, targeted race, full `go test -race ./...`,
  and `go build ./...`;
- deterministic KV simulations in text and JSON form, plus both KV performance
  forms;
- candidate/baseline deterministic goodput in candidate-first and
  baseline-first orders;
- scheduler, adapter lifecycle, completion telemetry, full estimator, and
  lexical-hint benchmarks in both candidate/baseline orders;
- builder-local image build, v0.10.10 production image contract, image inspect,
  and extracted-binary hashing.

Material log SHA-256 values include:

```text
full-test.log                    3fcc008a971987c7f5dc0146f2468b1d0e4d917b217e25f24a8c2465d2cc8bab
targeted-race.log                3b665ce6a06042b000203d5962bff803890d95a5edd7354b78a05a8bcd12dbb9
full-race.log                    b93a0edb4c34c11f1b2f9b662d01c5cc3ed2b2048e9db475ce8f8e721eb32b9a
kv-performance.log              23b7eba0ea3920d5e356811f9924fdbb620e17543b3856b91564c4c90fd1534a
goodput-candidate-order1.log     f4d8622b81e8f7ea9381eb1b6b9fff399d8d3039b4f3e585cd8f8262d5b31dd2
benchmarks-candidate-order1.log  7c9ca8d3461cf5bd790941e0e358e1c75bef469c83ea816afda9cc37d3ebd394
benchmarks-baseline-order1.log   938746a7d4f0cb50134fc482fa59975a36f2bd564ce57a6b40fe747969d06c32
benchmarks-baseline-order2.log   3f08806d1495e96cfeac8df2b5cf3806bee9701198b59b89c5074cbe16228607
benchmarks-candidate-order2.log  d8de4383ac5be8a09fa0177bcedeac758c518ddd3f8fa1079481bc87a820db4f
image-build.log                  da175182435e1e7f490939c6a5c57bd9119c05f119da400a56e604043313a584
image-contract.log               bbbf0b1aa46269debee44f5f0822cdf475cc1ef22756ccca6a16b2c0dc7af880
image-inspect.log                3c1ff9afd79822419b4bea428c1d89687689af92963f9b1988e9bd2019c024d9
```

The deterministic goodput result is byte-for-behavior identical in both
orders and in exact v0.10.9 versus v0.10.10 because this repair changes live
feedback qualification and Router publication rather than the fixed simulator
scenario policy:

```text
current-threshold completion-token goodput: 39840
v0.9.0 KV-only completion-token goodput:    37536
predictive completion-token goodput:        45216
predictive improvement versus current:      +13.49%
predictive SLO-compliant completions:        53
existing/new TPS violations:                0
TPOT violations:                            0
KV-hard violations:                         0
preemption-proxy events:                    0
false accepts:                              0
false denies:                               3
reservation leaks:                          0
TTFT diagnostics only:                      4
```

The first text performance pass measured 64-KiB estimator p95 `2.056 us`,
2-MiB estimator p99 `175.040 us`, and shadow-decision p99 `6.717 us`; the
independent JSON pass measured `3.855 us`, `171.723 us`, and `5.117 us`.
Both are far inside the fixed `200 us`, bounded-large-body, and live-shadow
targets. Direct lexical-hint medians remained approximately `101--110 ns/op`
at 1 KiB, 64 KiB, and 2 MiB with zero allocations. Complete-estimator medians
remained zero-allocation and at most about `0.831 us`, `2.341 us`, `31.551 us`,
and `68.236 us` for 16 KiB, 64 KiB, 1 MiB, and 2 MiB in the recorded candidate
orders. Adapter lifecycle retained two allocations; its candidate allocation
size increase remained below ten percent, while both measured order medians
were no slower than baseline.

The unpinned scheduler predictor comparison conflicted: candidate versus
baseline was `3594/2957 ns` (`+21.54%`) in candidate-first order and
`3764/3652 ns` (`+3.07%`) in reverse order. Both versions were also much slower
than prior builder evidence, so neither the favorable nor unfavorable order was
accepted alone. A precompiled, CPU-7-pinned, `GOMAXPROCS=1`, six-round
alternating-order supplement ran each exact test binary for three seconds after
warmup. Candidate samples were `2065--2088 ns/op`; baseline samples were
`2071--2093 ns/op`. Their medians were `2080` and `2083.5 ns/op`, respectively,
or candidate `-0.168%`, with identical `128 B/op` and one allocation. This
resolves the unpinned conflict as CPU/order noise and proves no predictor
regression; the absolute approximately 2.08 microsecond builder result remains
recorded as CPU evidence, not service-chain or GPU latency.

The paired candidate and baseline test-binary SHA-256 values are
`c0c1ae8a77a31cbc42955e6c212012ec44cc8bc76b065b6ea647c828e48f4591`
and `477e49699ab41d9273863fe8c1dacd407ad11dc78433cb7fd173f0ec420cbe28`.
The paired evidence archive is
`../tmp/pig-v01010-builder-20260804/paired-predictor-r1.tar.gz`, SHA-256
`4c9667eeedfa06a99bbc1ab05ecc080a05641b8d1265d86e149d2044700ce7e9`.
Its internal checksum manifest passes locally. The complete matrix evidence is
`../tmp/pig-v01010-builder-20260804/evidence-r12.tar.gz`, SHA-256
`284ff0e0833d676b466d04d3ae4ca5510f254a99e8e8aa472d07afea6a0657d6`;
its internal manifest also passes independently after download.

The builder-local image `pig-v01010-versioned-r12:local` has image ID
`sha256:f0208eddc2bc88e849eb984e724d34a4e67a52e0269cb7c89a2b094fa293cdc3`,
size 29,463,663 bytes, OCI version `0.10.10`, entrypoint
`/phala-inference-guard`, and `NVIDIA_VISIBLE_DEVICES=all`. Its extracted binary
SHA-256 is
`0ed1dd512f3356c5f9f900662751d06fb9e7ea163766fc132276cb842681c1f8`.
The contract output is exactly:

```text
PIG_PRODUCTION_IMAGE_CONTRACT_OK image=pig-v01010-versioned-r12:local version=0.10.10
```

This appended audit record is documentation-only. A post-matrix comparison of
all 243 executable/image input paths (`Dockerfile`, `go.mod`, `go.sum`, `cmd/`,
and `internal/`) against the exact r12 archive found zero mismatches. Review
pass 3 is therefore complete for exact source behavior, the complete clean
builder matrix, and the builder-local image layer. It does not yet prove a
commit, pushed branch/tag, registry image, Compose deployment, live readiness,
Router enablement, or production success. Those remain ordered gates; v0.10.9
and its immutable tag/image must not move.

##### v0.10.10 source release and immutable registry evidence — complete 2026-08-04

The reviewed executable source and release identities are:

```text
branch:        codex/pig-v0.10.0-model-agnostic
commit:        a24345e22628d5444314207b3cdba2ec4bf9cf6b
annotated tag: v0.10.10
tag object:    75485799b5f79433969aa5df3b2cc1df300c5569
```

The branch and annotated tag were pushed only to the authoritative PIG remote
`pig-origin=https://github.com/Phala-Network/phala-inference-guard.git`.
Remote tag dereference resolves exactly to release commit
`a24345e22628d5444314207b3cdba2ec4bf9cf6b`. The unrelated `origin` remote was
not used, and the v0.10.9 tag was not moved.

The tag-triggered GitHub `Publish Image` workflow run `30945299654` completed
successfully for the exact release commit at
<https://github.com/Phala-Network/phala-inference-guard/actions/runs/30945299654>.
The repository workflow supplied its own bounded GHCR package credential,
validated the v0.10.10 production image contract, built, and pushed. The
builder's absent local GHCR credential was therefore not bypassed or copied
from another system and was not needed for the auditable release path.

An independent public registry pull on the approved builder resolved:

```text
ghcr.io/phala-network/phala-inference-guard@sha256:1f5d469103fa081a5e8a53dbfac15cfaac1666f9655198f4da8064c028085b8b
```

The pulled linux/amd64 image ID is
`sha256:d7777f156760843c4ac8d90e8382309db2435db793a2c9717ce4d276285279ee`,
its size is 29,463,663 bytes, its OCI version is `0.10.10`, its entrypoint is
`/phala-inference-guard`, and the production image contract passed. Its
extracted binary SHA-256 is
`0ed1dd512f3356c5f9f900662751d06fb9e7ea163766fc132276cb842681c1f8`,
byte-identical by both SHA-256 and `cmp` to the exact r12 builder-local tested
binary. A temporary registry-image runtime remained running for the bounded
startup check, emitted `PIG-v0.10.10`, and was removed. The different registry
and builder-local image IDs reflect independent BuildKit layer metadata, not a
binary or contract difference; deployment must use the immutable registry
digest above.

Registry verification evidence is
`../tmp/pig-v01010-builder-20260804/registry-r1.tar.gz`, SHA-256
`f0092d5c05ed7000ee855a4cf90015bd83e848a7e2620b1cb677f1874f87100e`.
Its internal checksum manifest passes after download. This completes source,
tag, workflow publication, immutable digest, independent pull, production
contract, startup identity, and binary-equivalence layers only. It does not
prove Compose integration, CVM deployment, endpoint readiness, Router-disabled
shadow/enforce behavior, Router consumption, Router enablement, or production
traffic. `use1-cb` must remain disabled until all ordered live gates pass.

##### v0.10.10 Router-disabled shadow and enforce live gates — complete 2026-08-04

All live work in this section used only CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` (`use1-cb`). Router source, vLLM
source, another CVM, another upstream, production thresholds, secrets, and
public API behavior were not modified. The target stayed Router-disabled
throughout every shadow and enforce gate. The exact Router enabled set remained
`use1-19`; the target route stayed at `running=0` and `processed=57`, so none of
the direct test requests were confused with routed production traffic.

The fresh preflight in
`../tmp/pig-v01010-use1-cb-live-20260804/fresh-preflight-r1` captured the
previous live v0.10.9 enforce Compose before mutation. It proved CVM
`running`, `in_progress=false`, authenticated models/PIG/vLLM
metrics/attestation HTTP 200, unauthenticated metrics HTTP 401, an idle target,
and the Router baseline digest
`sha256:a2a4e0f648d8ad6d81772211832b8986c0378f6d1a3c8f023ea96a2aa0044ff1`.
The shadow candidate was generated mechanically from that byte-exact live
Compose and changed only the immutable PIG image to the v0.10.10 registry
digest and `PREDICTIVE_ADMISSION_MODE=enforce` to `shadow`. Its SHA-256 is
`5cb8aa8c2369935cce099cf96e997123006ebad42371135774b12b289841bbb9`;
reverse substitution reproduced the original bytes, YAML parsing passed, and
the saved candidate/rollback/evidence secret scans were clean.

The one shadow Compose-only deploy was accepted by the platform, but the local
CLI wait reached its 124-second command timeout. It was not repeated. Later
live state independently proved the exact candidate Compose and image were
accepted, platform update completed, the model finished loading, and this boot
recorded exactly one engine initialization and one `Application startup
complete`. This keeps deploy-command completion distinct from live acceptance
and service readiness. The ready snapshot in `shadow-ready-r7` proved
`PIG-v0.10.10`, mode `shadow`, legacy KV admission `off`, dynamic TTFT
protection disabled, the expected `google/gemma-4-31B-it` model, endpoint/auth
and NVIDIA-attestation behavior, terminal-zero state, and the unchanged Router
baseline.

The five protocol cases in `shadow-protocol-r8` all returned HTTP 200: normal
chat, streaming with usage, required tool call, structured JSON schema, and
CJK. `shadow-lowflow-r10` sent 20 sparse requests and proved that the input-size
learner reached a mature shape after the first few requests, retained
`source=learned` and the optional lexical hint, accumulated zero input/hint
invalidations, made 20/20 requests progress, and finished with no reservation,
deferred outcome, prefill, running, waiting, Router activation, preemption,
sticky zero, false lock, or self-lock. All 20 complete predictions and
estimators were at or below 0.25 ms.

The first QoS-source fixture in `shadow-qos-source-r11` asked for a nominally
long result that the model ended after 38 tokens; it is fixture-red, not product
evidence. The corrected long decode in `shadow-qos-source-r12` completed 4,096
tokens in 17.968 seconds and produced one locally corroborated qualified TPS
and TPOT outcome with no hard-adverse dimension. The externally rate-limited
1,024-token stream in `shadow-qos-source-r13` took 174.834 seconds at a curl
limit of 10 KiB/s, but PIG still observed the roughly 230-TPS backend/local
completion. This proves the public ingress/HAProxy path buffered at least the
approximately 1.8 MiB SSE response and did not propagate that client-side
rate limit back to PIG. It does **not** prove a direct slow-downstream censor
case and does not make the locally corroborated outcome false. The deployed
image is non-dev, SSH was refused, and the Compose exposes no direct PIG/vLLM
port; no dev image, port exposure, or security-surface expansion was introduced
only to manufacture that live condition. Direct slow-downstream causality
remains covered by the exact builder focused red/green tests.

The 100-request live latency run in
`shadow-latency-r14/latency-r2-20260804T204048Z/summary.json` (SHA-256
`467ef7db93025d7a05517d99ec3b86f4c54d7fe1fe476b9ed5d9a1271cb53c05`)
returned 100/100 HTTP 200. Complete prediction latency averaged 30.71
microseconds, with 99/100 at or below 0.25 ms and 100/100 at or below 1 ms.
Complete estimator latency averaged 47.44 microseconds, with 100/100 at or
below both 0.25 ms and 1 ms. These are
end-to-end PIG prediction/estimator-path observations for ordinary small
requests, not the approximately 0.10-microsecond lexical-sampler microbenchmark
and not a GPU throughput claim. The final shadow snapshot in `shadow-final-r16`
contained 132 attempts and 132 fit decisions, 8,688 prompt tokens, 10,524
generation tokens, input-size cold/learned counts `3/129`, hint
used/fallback/missing counts `129/3/0`, zero risk/unknown/reject, zero vLLM
error, zero preemption, zero hard-adverse events, and terminal-zero state.
`shadow-final-logs-r15` found no PIG, vLLM, or serial critical pattern and no
secret. Router remained disabled.

The enforce candidate was generated from the fresh live shadow Compose and
changed only `PREDICTIVE_ADMISSION_MODE=shadow` to `enforce`. Its SHA-256 is
`7831debce24924a7770b548f818068d2e08138411c246aa084491a3ff17821a9`;
reverse substitution, YAML parsing, secret scan, and the fresh drift/idle gate
passed. The one enforce Compose-only deploy again reached the 124-second local
CLI wait timeout and was not repeated. Subsequent platform and endpoint state
proved the exact Compose and immutable image were accepted and ready. The
`enforce-ready-r19` snapshot proved the expected image/mode/model, TTFT-off,
KV-off, authenticated endpoint and attestation success, unauthenticated metrics
HTTP 401, idle raw/effective Router capacity `50/50`, terminal-zero state, and
the unchanged disabled target. All five enforce protocol cases in
`enforce-protocol-r20` returned HTTP 200.

The original lease runners `enforce-lease-r21`, `r23`, `r24`, and `r26` are
retained as fixture-assertion iterations. They repeatedly proved a pre-forward
load-scoped HTTP 429, active/applied Router protection, finite exact expiry,
small-request recovery, zero preemption, and clean process/file cleanup, but an
old assertion expected `predictive_running=0` after real running drained. The
source contract in `predictive_router_capacity.go` applies
`MinimumRunning=1` to the predictive projection while a load lease is active.
Therefore the correct active, real-idle sentinel is:

```text
raw_running=0
predictive_running=1
effective_running=1
effective_global_limit=1
```

`predictive_running` is the already-normalized predictive projection, not raw
backend running. `pig_dynamic_observed_running` intentionally publishes the
Router-visible effective value. The fixed clean gate in
`enforce-lease-clean-r28` passed: two 1,536-token requests ran concurrently;
the third and a bounded renewal were rejected pre-forward with
`scope=load/reason=new_tps_at_risk`; active/applied were synchronously 1/1; the
post-completion sentinel was exactly `0/1/1/1`; and 2,579.912 ms later the
lease snapshot was exactly `active=0`, `applied=0`, raw/predictive/effective
running `0/0/0`, and raw/effective limits `50/50`. A small request then returned
HTTP 200. Preemption, resource-release failure, and all three hard-adverse
deltas were zero; metrics parity, terminal-zero, cleanup, and secret-scan gates
passed. This supersedes the old metric assertion without discarding its raw
evidence.

The request-scoped hard gate in `enforce-request-scope-r29` sent a 1,600,090
byte completion body. It returned pre-forward HTTP 429 with
`reason=kv_over_budget`, `source=static`, and `scope=request`; PIG/backend/vLLM
accepted, success, and prompt-token counters proved it did not reach the
upstream. Router protection remained inactive, raw/effective capacity stayed
equal, PIG and combined metrics matched, the matching reject log contained no
request content, and a subsequent 120-byte request returned HTTP 200 with no
sticky lock or preemption.

That large-body gate also exposed an instrumentation boundary that must not be
misreported as tokenizer CPU cost. The prediction histogram added one
83-microsecond observation, but `pig_predictive_admission_estimator_duration`
added approximately 2.880119 seconds. Source inspection proves the estimator
timer currently begins before `classifyRequest`, whose first step reads the
complete request body from the public connection. The large-body histogram
therefore includes ingress body-upload wait as well as in-memory parsing and
estimation. It does not contradict the exact builder's approximately
0.172--0.175 ms 2-MiB in-memory p99, does not mean the lexical hint consumed
seconds, and does not affect TPS/TPOT enforcement: accumulated local admission
latency feeds only the explicitly observation-only TTFT estimate. The ordinary
live latency gate remains within its fixed thresholds. A future telemetry
version may split body-receive and estimator-compute durations, but changing
only that naming/accounting would require a new source/image/deployment cycle
and is not silently folded into v0.10.10.

The lifecycle gate in `enforce-cancellation-r30` admitted two concurrent
1,536-token streams and observed predictive/backend decode `2/2` with waiting
zero. Both client processes were cancelled in 24.214 ms; reservation,
deferred, backend running, and waiting state converged to zero in 2,616.062 ms.
A small recovery request returned HTTP 200. Resource-release failure and
preemption deltas were zero; TTFT remained observation-only; metrics parity,
terminal-zero, cleanup, and secret scan passed.

The progressive learning evidence is the ordered pair
`enforce-progressive-learning-r31` and
`enforce-progressive-continuation-r32`. r31 intentionally held request shape
and backend path fixed while making three bounded two-request training rounds.
Each round rejected the third request pre-forward and only then allowed the two
already-forwarded 4,096-token requests to complete and train a later
prediction. The provisional runner allowed three total attempts, but each pair
can create only one compatible `DecodeSequences=2` sample for a later
decode-three query. Thus its third attempt occurred with only two previously
compatible samples; its final two completions created the required third
sample. The runner's bounded failure is an off-by-one fixture finding, not a
learner failure. Cleanup was exact. Across those rounds there was no hard or
soft QoS miss, waiting/preemption pressure, invalidation, preemption, or
lifecycle leak, while qualified per-user TPS remained far above the 20-TPS
hard floor.

r32 immediately reused that completed evidence. With the same 4,096-token
shape and two requests already running, the third request changed to
`reason=fit`, `source=calibrated`, `samples=3`, and `exploratory=1` before
forward. PIG and vLLM both reached three running requests; all three returned
HTTP 200 with 4,096 completion tokens each. A small recovery request returned
HTTP 200, preemption delta was zero, and metrics parity, terminal-zero, cleanup,
and secret scan passed. Together r31 and r32 prove the required causal
direction: feedback never changes the producing request, but compatible mature
feedback changes a later pre-forward decision and raises useful concurrency
from two to three without a hard-QoS or preemption event.

The final log audit in `enforce-final-logs-r33` captured 360 vLLM lines, 410 PIG
lines, and 1,080 serial lines. It found exactly one vLLM engine initialization
and one application-startup marker, zero vLLM/PIG/serial critical match, and no
secret. PIG logs contain each applicable `event=activated`,
`event=router_capacity_applied`, and renewal, plus status intervals with
`router_bp=1/1` and the matching effective limit while protection is active;
later status lines return to `router_bp=0/0` and raw/effective `0/50`. The PIG,
serial, and vLLM log SHA-256 values are respectively
`d3f06ceead3e9add3103236a60acbfdcc7873955bd0a02e9be0fb59ccf9f77b1`,
`efc31ac15f051675a5bcee63adc7a739e83ff43c417c10e753ba10c83e53c8de`,
and `f0867dcfd2f1a82e4da289548bcf9c40c08a39543efdb2e28f211592349a0771b`.

The final fresh snapshot in `enforce-final-r34`, captured at
`2026-08-04T21:28:58.6785292Z`, again proved exact Compose/image/mode/model,
CVM `running`, `in_progress=false`, endpoint/auth/attestation health, unchanged
Router digest and membership, target `processed=57/running=0`, and terminal
raw/effective capacity `50/50`. Cumulative enforce evidence contains 49
attempts, 36 fits, 13 risks/rejects, zero unknown/predictive/resource-release
failure, 30 vLLM successes, 1,070 prompt tokens, 54,324 generation tokens, zero
vLLM error/preemption, 13 qualified and eight censored deferred outcomes, zero
hard-adverse event, zero soft miss, zero waiting/preemption pressure, input-size
cold/learned `4/45`, hint used/fallback `45/4`, and all terminal state zero.

##### v0.10.10 live review pass 1: model and causality — complete 2026-08-04

The live model/causality review traced the real request transaction rather than
accepting counters alone. Approximate input size influenced the pre-forward
cost without a model tokenizer, asset, template, cache, Router, or model-family
branch. r29 proved request-specific cost can reject before upstream without
node-wide capacity suppression. r28 proved a load-specific QoS rejection is
published synchronously to Router-consumed capacity and expires exactly. The
r31-to-r32 sequence proved compatible qualified feedback changes only a later
prediction and can increase useful concurrency from two to three. TTFT remained
observation-only in configuration, decision reasons, metrics, and status logs.
No model- or causality-level release blocker remains for the bounded canary.

##### v0.10.10 live review pass 2: safety, efficiency, and SOLID — complete 2026-08-04

The safety/SOLID review checked request/load/availability scope separation,
atomic reservation visibility, cancellation, exact terminal release, finite
Router leases, raw versus predictive/effective capacity semantics, low-flow
progress, protocol invariance, bounded learner/input state, and secrets. It
classified r21/r23/r24/r26 and r31 as fixture-assertion findings only after
their raw product behavior and cleanup were preserved; r28 and r32 are the
corresponding clean behavioral gates. No preemption, hard/soft QoS miss,
waiting-pressure event, duplicate/dropped release, leak, false lock, sticky
zero, OOM, Xid, EngineDead, panic, or restart occurred. The large-body duration
finding is a documented telemetry-boundary limitation, not unbounded estimator
compute or a hidden admission constraint. It does not justify adding another
hot-path abstraction before canary; splitting that metric remains an explicit
future source-version decision.

##### v0.10.10 live review pass 3: evidence and release — complete 2026-08-04

The evidence/release review kept source, clean-builder matrix, immutable image,
Compose acceptance, runtime readiness, direct functional gates, and production
traffic as distinct layers. Both local deploy waits timed out, but live
Compose/image identity and platform/readiness evidence independently closed the
deployment layer without a repeated deploy. Shadow and enforce gates covered
protocol, auth, attestation, typical live latency, low-flow/self-lock,
request-scoped and load-scoped rejection, Router-visible protection,
cancellation, progressive learning, logs, secret scans, and terminal-zero
state. The slow-downstream public-path limitation and large-body timing metric
semantics are explicitly bounded rather than promoted into false evidence.

v0.10.10 is therefore temporarily eligible for exactly one next mutation: a
fresh drift/idle snapshot followed by changing only
`use1-cb.enabled=false` to `true`. The 30-minute timer must begin only after the
first real routed request is proved jointly by Router processed movement and
PIG/vLLM inference-counter movement. The supervisor must use the v0.10.10
five-way TPS outcome labels, three hard-adverse dimensions, active/applied
parity, sorted enabled sets, raw/predictive/effective sentinel semantics,
low-flow progress, and lifecycle counters. Any safety, lifecycle, telemetry,
QoS, or obvious throughput problem must immediately disable only `use1-cb`,
preserve evidence, and return to the source/builder/image/disabled-route loop.
The Goal remains active until that real-traffic observation and any required
repair loop are complete.

##### v0.10.10 30-minute canary r40 hard-existing-TPS finding and v0.10.11 repair loop — active 2026-08-04

The corrected r40 supervisor enabled only `use1-cb` after a clean preflight and
observed real traffic for 1,399.118 seconds. It then disabled only `use1-cb`
when `pig_predictive_learning_hard_adverse_total{dimension="existing_tps"}`
increased from zero to one. The target processed 887 routed requests, made 977
predictive attempts (`620/354/3` fit/risk/unknown), enforced 357 pre-forward
rejects, completed 536 vLLM requests, and produced 311,981 completion tokens.
There were zero vLLM preemptions, PIG preemption-pressure events, predictive
failures, release failures, dropped deferred outcomes, error completions,
new-user hard-TPS outcomes, or hard-TPOT outcomes. Router publication was
visible and bounded; the supervisor restored the exact disabled baseline.

The first post-disable snapshot still contained one naturally draining request.
The fresh terminal snapshot in
`../tmp/pig-v01010-use1-cb-live-20260804/terminal-zero-r42-20260804T224745Z`
supersedes that transient state. Its 16/16 validator proves CVM and containers
running, immutable v0.10.10 image and enforce Compose unchanged, Router digest
restored, enabled set exactly `use1-19`, target route running zero, PIG
reservations/deferred/running/waiting zero, vLLM running/waiting zero, KV zero,
and raw/effective capacity `50/50`. The cumulative hard counter correctly
remains one. v0.10.10 must not be re-enabled as if that counter and learner
history did not exist.

This hard outcome is not the joining request's completion histogram and is not
a cache, tokenizer, TTFT, Router, or vLLM fault. The accepted existing-prefill
counter advanced from 14 to 15 at approximately `22:38:19Z`; the last accepted
backend value is `1.998185` tokens/s. The observer required one unchanged
forwarded pending prefill, two existing decode sequences, identical manager and
shadow event sequences across the sample, unchanged running count, zero
waiting, unchanged preemption counter, a matching model/capacity/block-size
epoch, and a fresh generation-counter delta. The contemporaneous status then
showed `running=3 prefill=1 decode=2 gen_tps=4.0`; after prefill ended, aggregate
generation recovered to about 521--554 tokens/s and per-user status to about
176--189 tokens/s. This is a short but real, backend-corroborated prefill
interference event below the configured 20-token/s red floor.

Source review found two incomplete learning/evidence semantics:

1. `ObserveExistingPrefill` reconstructs a static prediction from the pending
   request's feature vector. The manager snapshot retains the original features
   but not whether the admitted prediction was exploratory. Consequently the
   hard counter cannot prove whether this was the one allowed unknown-frontier
   probe or a non-exploratory forecast failure. Treating every hard increment as
   immediately fatal in the canary contradicts the documented one-probe hard
   knee budget, while ignoring the distinction would hide a genuine predictor
   miss.
2. A hard residual is the immediate minimum for the three-second
   `MaxMetricsAge` adverse horizon. After that horizon it is neither an adverse
   override nor an ordinary residual, although its timestamp invalidates older
   optimism for the 1,800-second learning age. Only three newer compatible
   healthy samples can therefore reopen one-step exploration and the numeric
   hard result contributes no longer-run statistical weight. The current hard
   frontier simulation stops after its first rejected stabilization check and
   does not exercise repeated safe-evidence/re-probe cycles. This is weaker
   than the stated requirement that feedback make later predictions
   progressively more accurate and can permit the same unknown knee to be
   exposed too frequently.

v0.10.11 must preserve the model-neutral, approximate-tokenizer-assisted
architecture and make the smallest coherent correction:

1. Keep the fresh hard-adverse override and exploration block exactly bounded
   by the existing maximum-metrics-age contract. This retains immediate retreat
   without introducing a user-facing timer, sticky zero, or node-wide idle
   latch.
2. After that immediate horizon expires, retain each qualified hard TPS/TPOT
   residual in the existing bounded local/global residual population until the
   ordinary learning age, invalidation, or eviction removes it. It participates
   in the existing 0.10 TPS lower quantile or 0.90 TPOT upper quantile under the
   same adverse compatibility/dominance rules. Hard classification uses the
   absolute red boundary, while an ordinary residual is relative to the static
   prior; those directions can differ when the prior is already beyond red.
   Normalize a hard TPS residual as `actual / max(prior, red_target)` so it is
   strictly below one, and normalize hard TPOT as
   `actual / min(prior, red_slo)` so it is strictly above one. A qualified hard
   result must never become optimistic evidence merely because its static prior
   was more pessimistic. The generic minimum-TPS or maximum-latency multiplier
   clamp must also not move a selected hard tail back across the absolute red
   boundary: while the nearest-rank quantile still selects hard statistical
   debt, the final TPS estimate must remain strictly below `red_target` and the
   final TPOT estimate strictly above `red_slo`. Track only the transient count
   of hard values already present in the selected bounded residual slice; do not
   allocate or retain a second sample population. Older optimistic evidence
   remains cut off; only newer qualified compatible evidence joins the
   post-adverse generation. With one
   hard value, the 0.10 nearest-rank lower quantile needs at least ten newer
   healthy values before the hard value leaves the selected lower tail.
   Repeated hard results add proportionate statistical debt, while sustained
   qualified health can recover without a permanent minimum-adverse latch. Do
   not add a request map, second controller, model/user/prompt key, unbounded
   store, or extra hot-path allocation.
3. Carry the original immutable `prediction.Exploratory` bit through manager
   pending-prefill and shadow snapshots into `ExistingPrefillOutcome`. Record
   hard-adverse origin as fixed-cardinality `exploratory` versus
   `non_exploratory` counters for all three hard dimensions; completion-derived
   QoS already owns its original scheduler prediction, while existing-prefill
   evidence must use the newly propagated bit. Advance the predictor semantic
   identity so older residual semantics cannot cross the boundary.
4. Expose the origin counters in `/pig/metrics` and combined `/v1/metrics`, and
   include cumulative hard-origin counts plus the last existing-prefill TPS and
   exploratory bit in the bounded status log. Logs and metrics must agree after
   a hard event without request, model, user, or prompt labels. Existing
   dimension counters remain for compatibility and must equal the sum of their
   two origin counters.
5. Keep request prediction, evaluation, and atomic reservation pre-forward.
   Feedback still changes only later requests. The lexical input-size hint,
   cold-safe fallback, KV hard bounds, lifecycle reconciliation, Router capacity
   publication, and TTFT observation-only behavior remain unchanged. Cache
   metrics remain observation-only. Router and vLLM source remain out of scope.

Mandatory v0.10.11 red/green evidence is:

- a current-source focused red proving that, after one qualified exploratory
  hard existing-prefill result and expiry of the immediate horizon, only the
  normal three newer healthy samples can reopen the same failed frontier; green
  must keep it closed while the hard result remains in the 0.10 lower tail and
  must recover only after enough newer healthy evidence moves that quantile;
- repeated-hard deterministic coverage proving additional hard values increase
  the healthy-evidence debt, bounded stores and expiry still work, and idle or
  one-user low flow always makes progress;
- absolute-hard/residual-direction coverage where the static TPS prior is below
  red or static TPOT prior is above red, proving a hard outcome remains
  conservative during both the immediate override and post-horizon quantile;
- manager/observer red/green coverage proving the original exploratory bit is
  preserved only for exactly one attributable pending prefill and is censored
  for concurrent or incoherent prefills;
- scheduler, metrics, combined-metrics, and status-log coverage proving
  dimension/origin parity, last backend TPS visibility, and no label or log
  cardinality growth;
- the existing source-qualified completion QoS, waiting/preemption, hard-knee,
  beneficial/harmful soft-knee, input-size, atomic reservation, lease,
  cancellation, low-flow/self-lock, protocol, simulation, and performance
  suites without weakened thresholds.

Local Windows may perform source review, documentation, AST/static script
checks, and artifact analysis only. It must not run PIG Go tests, race tests,
simulations, benchmarks, image builds, or local service tests. Exact
reconstruction on the approved remote builder must run focused red against
v0.10.10, focused green, formatting, full tests, vet, focused and full race,
build, deterministic simulation, benchmark/allocation gates, image contract,
and binary/image identity checks before release. Version and semantic identity
must advance autonomously to v0.10.11 only after those gates pass.

After an immutable v0.10.11 image exists, deploy it only to
`a0f0bfb3-e46f-4b22-814e-24872f251193` while `use1-cb` remains Router-disabled.
Repeat exact Compose/image/readiness, shadow then enforce, protocol, latency,
source-qualified QoS, progressive learning, statistical hard-memory,
request/load publication, lease expiry, cancellation, low-flow, no-self-lock,
terminal-zero, log, metrics, and secret gates. No other CVM or upstream may be
changed.

The next 30-minute real-traffic supervisor must baseline the new origin
counters. One previously unknown hard-adverse *event* is observation-eligible
only when every hard dimension it increments is marked exploratory, no
non-exploratory hard counter grows, no preemption/waiting/lifecycle/publication
failure accompanies it, immediate retreat is visible, and the same compatible
frontier is not exposed again during the observation. Any non-exploratory hard
event, a second exploratory hard event, dimension/origin parity failure, hard
resource violation, preemption, persistent waiting, low-flow lock, telemetry
under-publication, lifecycle failure, or obvious throughput collapse must
immediately disable only `use1-cb`. A single eligible event is not automatic
success: the full 30 minutes must finish and show that later pre-forward
predictions retained the learned statistical debt while useful throughput and
low-flow progress continued.

Three plan review passes are mandatory before editing executable source:

1. Model/causality review: prove the backend window, original-prediction origin,
   post-horizon quantile population, and later pre-forward decision form one
   causal chain; do not infer per-user completion QoS from aggregate status or
   confuse Router allocation with PIG prediction.
2. Correctness/efficiency/SOLID review: prove quantile semantics for one and
   repeated hard values, fixed memory and labels, no new hot-path allocation,
   exact lock order, single-owner lifecycle, low-flow escape, and unchanged
   observer/scheduler/manager/adapter responsibilities.
3. Evidence/release review: keep source, remote red, remote green/matrix,
   image, Router-disabled deployment, readiness, direct gates, and production
   canary as separate layers; require exact hashes, secret scans, terminal-zero
   evidence, and rollback before each mutation.

The Goal remains active. v0.10.10 is not production-ready, v0.10.11 is not yet
implemented, and no Router enablement is authorized until the ordered source,
builder, image, Router-disabled live gates, and fresh canary preflight pass.

##### v0.10.11 pre-implementation three-pass plan review — complete 2026-08-04

Pass 1 traced both real paths. Completion-derived TPS/TPOT calls `Observe` with
the immutable admission prediction, so its exploratory origin already exists.
Existing-prefill learning instead takes the one anonymous pending reservation's
original feature vector from `Manager.Snapshot`, validates it against stable
vLLM generation/running/waiting/preemption counters, then constructs a new
static prediction; only this path loses origin. The planned boolean propagation
therefore repairs an evidence field without changing admission, attribution,
or request identity. The r40 value and status arithmetic agree exactly:
`4.0 / 2 = about 1.998` tokens/s. No completion-histogram, cache, or TTFT signal
is needed for that conclusion.

Pass 2 checked the nearest-rank quantile and bounded data path. At lower
quantile 0.10, one lowest hard residual plus ten higher healthy residuals gives
11 values and selects index one; one hard plus only three healthy values still
selects index zero. This review also found and corrected an initially missing
absolute-hard/residual-direction rule: if a static TPS prior is already below
red, `actual/prior` can exceed one despite an absolute hard miss; TPOT has the
inverse case. Red-boundary denominator normalization now guarantees hard TPS
ratios below one and hard TPOT ratios above one before either immediate or
post-horizon use. The implementation review later found a second, symmetric
boundary requirement: generic multiplier clamps can still move that ratio back
across red, so selected hard-tail debt must preserve the absolute boundary after
the clamp. Reclassifying an expired immediate-adverse value into the
already allocated ordinary residual slice therefore supplies proportional,
directionally conservative statistical debt with no new map, timer, label
value, request state, or prediction-path allocation. The latest-adverse cutoff
still excludes optimism that predates the failure; normal cell/global bounds,
age, backend-epoch invalidation, dominance, and eviction still apply. Idle
admission retains its existing no-decoder floor, so the change cannot make an
adverse concurrent shape latch an otherwise empty node. Repeated values
naturally occupy a larger fraction of the same fixed sample budget rather than
mutating a second capacity controller.

Pass 3 checked evidence feasibility and mutation scope. The origin metric has
exactly six fixed series (three dimensions times two origins); existing totals
remain a parity check. The status line can report cumulative counters and the
last anonymous backend value without request/model/user labels. Focused red can
be reconstructed from exact v0.10.10 source, and all executable validation can
remain on the remote builder. The only later live target remains `use1-cb`,
with Router disabled through deployment and direct gates. One exploratory
hard-event budget is measurable as one adverse-event delta with only
exploratory origin deltas; a non-exploratory delta or a second event remains an
unambiguous automatic disable. The residual-direction correction remains
entirely inside the scheduler and requires no Router, vLLM, cache-aware
admission, TTFT protection, model assets, or local Go execution. Executable
v0.10.11 work may now begin.

##### v0.10.11 focused red and implementation-review correction — active 2026-08-04

The approved builder environment had drifted operationally but not in scope.
PowerShell blocked the `phala.ps1` shim, and `phala.cmd ssh --dry-run` printed
the correct endpoint before a Node/UV assertion. Direct Windows OpenSSH through
the Git OpenSSL dstack TLS proxy restored access. The prior
`pig-ubuntu-builder` container no longer existed; the remaining Ubuntu helper
had a full 204 MiB root filesystem and no Go. On the same approved builder CVM,
an isolated `pig-v01011-builder` container was therefore recreated from the
Dockerfile's exact pinned Go image digest
`sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`,
with only the dedicated persistent builder path mounted at `/work`. It reports
Go 1.24.13, GCC 12.2.0, Git 2.39.5, and about 167 GiB free. This is builder
repair only; no GPU, Router, vLLM, or production node changed.

All five r1 inputs matched local and remote SHA-256. Exact tag `v0.10.10` plus a
test-only source-compatible test failed as required:

```text
TestV01011HardExistingPrefillDebtSurvivesThreeHealthySamples
prior existing TPS = 40
one hard existing-prefill TPS = 5
three newer healthy TPS values = 80
v0.10.10 post-horizon prediction = 80, samples = 3
failure = three healthy samples reopened failed hard frontier
```

This proves the requested red independently of v0.10.11-only origin fields.
The first candidate preflight then stopped before tests on two pure gofmt
alignment differences; those differences were applied locally and r2 gofmt was
clean. r2 focused green exposed a deeper correctness issue rather than being
repeated until passing. With prior existing TPS 72, hard actual TPS 5, red TPS
20, and `MinimumTPSMultiplier=0.50`, the normalized hard ratio was selected but
the generic clamp raised the final estimate to 36. That estimate remained below
the prior but above red and could therefore reopen the failed frontier. TPOT has
the symmetric failure when `MaximumLatencyMultiplier * prior_tpot` remains
below the red SLO.

The corrected implementation keeps one transient `StandardAdverseCount` beside
each already allocated residual slice. It does not retain another sample,
request, timer, map, or label. For TPS, nearest-rank index
`ceil(0.10*n)-1 < hard_count` means the lower tail still selects hard debt. For
TPOT, `ceil(0.90*n)-1 >= n-hard_count` means the upper tail still selects hard
debt. In either case the normal multiplier clamp still executes, after which
the estimate is bounded strictly across the absolute red line. One hard plus
ten healthy and two hard plus nineteen healthy no longer select hard debt;
ordinary clamp behavior then resumes automatically. The count is reconstructed
in the existing local/global scan, obeys the same latest-adverse generation,
age, compatibility, capacity, and eviction rules, and creates no prediction
path allocation. The existing zero-decoder TPS/TPOT floor still overrides
concurrent-shape debt for idle progress.

Three correction reviews were completed before the next builder upload:

1. Causality: hard classification, direction-normalized residual, bounded tail
   count, quantile selection, generic clamp, absolute-boundary preservation,
   and the next pre-forward evaluation now form one ordered path. Feedback
   never changes the request that produced it.
2. Correctness/efficiency/SOLID: TPS and TPOT use symmetric nearest-rank tail
   predicates and scheduler-owned final-estimate helpers; observer, manager,
   adapter, Router, and vLLM responsibilities do not change. Only an integer in
   a transient scan result is added, with no durable controller or allocation.
3. Evidence: the exact v0.10.10 red remains valid; r2 failure is retained as a
   negative candidate result. New tests must prove both immediate and
   post-horizon TPS/TPOT red-boundary preservation under masking multiplier
   clamps, the one/two-hard nearest-rank thresholds, existing quantile recovery,
   and low-flow progress before focused green can authorize the full matrix.

Candidate r3 included that correction and the new symmetric tests. Its base
HEAD archive, independent red test, candidate patch, and runner matched on the
builder with SHA-256 values:

```text
base HEAD archive  7fc57892b3b253cb3fe2df11a2f1e15606dbfa5f048e703df4268aca1d52cdd0
red test           bfa5d0c2e0217bef8b894b1ed4939c4efa2f6eac8c6f5712a0fe4f0e6fe9acb7
candidate r3       c0ce7cb4cccda66d0d87bdfc748a1fc5830cffe9177d74d732bb0647b6b53960
runner r3          b916024448fb31252cc56985cfff71ca68ef7883d7a3c3fd1f5ed94205ebe221
```

Remote gofmt produced an empty diff. The focused green then passed exactly once
for the three ownership packages:

```text
ok  internal/runtime/predictive       0.033s
ok  internal/app/server               1.915s
ok  internal/observability/metrics    0.004s
```

This focused green authorizes only a new exact-source full builder matrix. The
plan update itself changes the candidate archive hash, so r3 must not be cited
as the hash for that full matrix. No commit, tag, image, deployment, Router
enablement, or production readiness follows from the focused result alone.

##### v0.10.11 final-review correction and full builder matrix — complete 2026-08-05

The first nominally complete r5/r6 matrix is retained but superseded. r5 ran
source contract, secret scan, gofmt, focused/full tests, vet, targeted/full
race, build, KV simulation/performance, both-order goodput, and the first
candidate benchmark stage with status zero. Its foreground SSH wrapper ended
between benchmark stages while the builder container remained healthy
(`running=true`, `OOMKilled=false`, `RestartCount=0`). r6 then ran the complete
four-order paired benchmark contract and builder-local image gates with status
zero. The combined archive copied from the builder is:

```text
../tmp/pig-v01011-builder-20260804/
  pig-v01011-full-r5-r6-evidence-20260805T0036Z.tar.gz
SHA-256 93a608e1420fd560bd8fb225c5ed03e7afeeb8db41c08e1765bbf8a670340a97
r5 manifest entries 51, status files 16, nonzero 0
r6 manifest entries 37, status files 10, nonzero 0
```

That green result cannot be promoted. A source review performed after r6 found
that production `MinimumSamples=3` still converted one expired hard residual
back to `static`/zero samples in sparse traffic. It also found that the global
fallback could select a different mature decode-sequence group without joining
compatible expired hard debt from the original group. Those are product
defects in the r5/r6 candidate, not harness failures. They directly permit a
known bad frontier to be exposed again after the three-second immediate horizon
and therefore supersede every green r5/r6 release conclusion while retaining
the artifacts as negative audit history.

The exact r7 test-only correction audit reproduced both defects on the r5
candidate:

```text
TestV01011ExpiredHardResidualRemainsEffectiveWithoutNewerHealthySamples
  got Source=static Samples=0
TestV01011GlobalExpiredHardDebtCannotBeBypassedByAnotherMatureDecodeGroup
  got Standard=[2 2 2] StandardAdverseCount=0
red test SHA-256 b33714d077e4d250b54a85501eb5bbb1ca9d2faa1339c38c8e053a792ee564c5
runner SHA-256   32a18f1811338c69851c38250d56dd936fd032f7b6abd0827abd6b2fbef15ab0
```

r8 is retained as an intermediate product red. It fixed the cross-group bypass
but the sparse single-hard test still failed because the helper accepted hard
debt while the caller independently required `len(ratios) >= minimum`. The
final correction removes that contradictory second maturity gate. A compatible
expired hard value can itself produce a calibrated protective estimate, while
global fallback first joins every compatible expired hard value with at most
one selected healthy decode-sequence group. TPS and TPOT then use the same
bounded-tail contract. No second population, request map, timer, lock, durable
controller, or label was added.

r9 passed the two correction tests and the three owning packages. r10 added the
joining-user TPS/TPOT symmetric sparse test and passed all three exact tests:

```text
TestExpiredHardResidualRemainsEffectiveWithoutNewerHealthySamples
TestExpiredHardJoiningTPSAndTPOTRemainEffectiveWithoutNewerHealthySamples
TestGlobalExpiredHardDebtCannotBeBypassedByAnotherMatureDecodeGroup

scheduler.go SHA-256
  7f46bf8ba0e7126936922793a626878f596075e577617ec0ae70e5e912f204f8
scheduler_test.go SHA-256
  405b51a42160f8e98d40285a37a8962bae92a19a60ca0c96cd99620c2f276c12
r10 runner SHA-256
  4fa9cb22af59939a939659556736966d09065eae94d1bdd102274a6340bd284d
runtime/predictive 0.033s; app/server 1.917s; observability/metrics 0.004s
```

The accepted full r11 matrix used a fresh non-reused directory:

```text
/var/volatile/dstack/persistent/pig-builder-work/
  pig-v01011-full-r11-14d6b389
```

Its exact changed inputs were:

```text
scheduler.go
  7f46bf8ba0e7126936922793a626878f596075e577617ec0ae70e5e912f204f8
scheduler_test.go
  405b51a42160f8e98d40285a37a8962bae92a19a60ca0c96cd99620c2f276c12
benchmark contract
  33d555db871a9d20bb78be47e71a81e54132b1f8c09c259d83d406701dff44f3
r11 runner
  c620202f84e2e55c105f1710fc2431dc1b000ecef2135a13ba532e2af471af9a
```

The builder remained the isolated `pig-v01011-builder` container on approved
builder application `89811a9add5b20427ee1fbf4dc22a33984e41959`, using pinned
Go image digest
`sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
It reported Go 1.24.13, GCC 12.2.0, Git 2.39.5, 167 GiB free,
`OOMKilled=false`, and restart count zero.

All 24 r11 status files are zero: source delta/contract, secret scan, gofmt,
focused tests, vet, full test, targeted/full race, build, KV
simulation/performance, four goodput orders, four benchmark orders, benchmark
contract, image build, production image contract, and image inspection. The
runner ended normally with `PIG_V01011_FULL_R11_MATRIX_OK`.

The deterministic goodput result was identical in both execution orders. The
predictive policy completed 53 SLO-compliant requests and 45,216 completion
tokens versus 33 and 37,536 for the old KV-only policy. It reported zero
existing/new TPS violations, zero TPOT violations, zero KV hard violations,
zero preemption proxies, zero false accepts, and zero reservation leaks. Four
TTFT violations remained diagnostic only, as required. The hard-knee scenario
made exactly one decode-seven probe and then retained mature decode six at 300
aggregate completion TPS. The beneficial soft knee reached mature decode eight
at 320 TPS, the harmful soft knee retained decode six at 300 TPS, and the safe
frontier matched the 400 TPS oracle at decode eight.

The two-order/five-sample paired benchmark contract passed with no allocation
regression. Important combined candidate/baseline timing ratios are:

```text
PredictCalibratedTPSAndLatency             +3.5683%  128 B/op 1 alloc/op
PredictAggregateFrontier baseline path     +6.7802%    0 B/op 0 allocs/op
PredictAggregateFrontier curve path        +9.3824%    0 B/op 0 allocs/op
ObserveAggregateThroughput                 -0.6688%    0 B/op 0 allocs/op
Admission lifecycle                        +0.7412%  912 B/op 2 allocs/op
Deferred outcome lifecycle                 +0.2564% 1664-1689 B/op 2 allocs/op
```

The frontier change is a real watchpoint rather than hidden noise: both orders
showed approximately 9.1--9.6 percent on the curve path. It nevertheless stays
inside the fixed ten-percent combined regression gate, remains about
`504--509 ns/op`, and adds no allocation. The complete calibrated prediction
path remained `1.751--1.790 us/op`. This bounded sub-microsecond cost is accepted
for the required cross-group hard-debt join; any later executable change to
reduce it must invalidate r11 and rerun the full matrix rather than silently
inherit this evidence.

The unchanged model-neutral lexical hint remained about `88.42--93.07 ns/op`,
zero bytes, and zero allocations for 1 KiB through 2 MiB benchmark bodies. Full
estimator medians were approximately `1.968--1.969 us/op` at 64 KiB and
`63.073--64.375 us/op` at 2 MiB, also with zero allocations. The independent
performance runner reported 64 KiB estimator p95 between `2.014--2.36 us`,
2 MiB p99 between `119.918--131.802 us`, and shadow-decision p99 between
`2.102--2.32 us` across its text and JSON runs. These are CPU path gates, not a
claim about end-to-end request or GPU latency.

The r11 builder-local image passed the production contract with:

```text
image tag        pig-v01011-full-r11:local
image ID         sha256:5633bf59f27b019443f839eca267d799a4746c02bfc68ab4c922a85c80ccb37e
platform         linux/amd64
size             29,475,951 bytes
OCI version      0.10.11
entrypoint       /phala-inference-guard
binary SHA-256   8bf73af6b5c358f3fe854e675f86a600005e240084a4053a4195c5a25a8e52f9
```

The downloaded r11 archive is:

```text
../tmp/pig-v01011-builder-20260804/
  pig-v01011-full-r11-evidence-20260805T0110Z.tar.gz
remote and local SHA-256
  5b832c2cd76eda6da86fcc61c67910d2d0fd28032fb84eb1d5573dce3a53ccf6
```

Independent local artifact analysis verified every entry in its internal
`SHA256SUMS`, counted exactly 24 status files, and found all 24 zero. This is
accepted complete clean-builder evidence for the exact r11 executable source.
It is not yet a commit, pushed source, tag, registry image, Compose integration,
CVM deployment, live readiness result, Router enablement, or production canary.

##### v0.10.11 final implementation review pass 1 — model and causality

The review retraced the complete causal path after the r7/r8 correction.
Request cost and the fixed-budget lexical input-size hint are calculated before
the scheduler. `Predict` evaluates local/global compatible residuals and the
post-admit virtual state before `Manager` atomically decides and reserves. The
forwarded reservation retains the original immutable exploratory bit. A single
stable backend prefill window can therefore attribute its existing-user TPS to
that prediction; concurrent, changed, stale, waiting, preempted, or incoherent
windows remain censored. Hard feedback is normalized and stored only after the
outcome, so it cannot retroactively change its own request. On a later request,
fresh hard evidence supplies the immediate adverse override; expired evidence
joins the existing bounded statistical population and affects the 0.10 TPS or
0.90 TPOT tail. The r7 exact red and r10/r11 green tests prove that this later
pre-forward estimate changes from the previously unsafe static reopening to a
calibrated protected result. No cache, TTFT, Router, or model-specific signal is
part of that decision.

##### v0.10.11 final implementation review pass 2 — correctness, efficiency, lifecycle, and SOLID

The local and global scanners apply the same age, backend identity,
compatibility, dominance, and latest-adverse-generation rules. One compatible
expired hard residual is sufficient to remain effective under sparse traffic;
another mature decode group cannot bypass it; one hard value recovers only
after ten newer healthy values and two hard values only after nineteen. The
absolute TPS/TPOT red boundary remains conservative even when the static prior
or generic multiplier clamp points in the opposite direction. Idle/no-decoder
floors supersede concurrent-shape debt, preserving low-flow progress without a
sticky-zero latch. Existing sample/cell/global bounds, eviction, age expiry,
backend-epoch invalidation, manager ownership, atomic reservation, terminal
release, cancellation, and lock order are unchanged and passed full race and
simulation gates.

Responsibilities remain separated: the estimator owns only bounded approximate
size; scheduler owns residual selection and prediction; manager owns atomic
reservation lifecycle; observer owns qualified anonymous backend windows;
metrics owns fixed-cardinality rendering. The change reuses existing residual
slices and global scan, adds only transient hard counts and fixed counters, and
adds no request map, second population, timer, model/user/prompt key, label
cardinality, or hot-path allocation. The approximately 9.38-percent frontier
microbenchmark regression is close to but below the predetermined ten-percent
gate and remains about half a microsecond with zero allocations. It is recorded
as a live CPU/latency watchpoint but is not a correctness or release blocker.

##### v0.10.11 final implementation review pass 3 — evidence and release boundary

The evidence chain now explicitly retains the r5/r6 false-green candidate, r7
valid product red, r8 intermediate product red, r9/r10 focused green, and r11
complete matrix without counting a superseded result as success. Exact input,
runner, archive, log, image, and binary hashes are recorded above. Local Windows
performed only source/document review and artifact hashing; every executable
Go, race, simulation, benchmark, build, and image gate ran on the approved
builder. Secret scan and the internal/external archive hashes passed.

The next mutation gate is exact release-source identity. `Dockerfile`,
`go.mod`, `go.sum`, `cmd/**`, `internal/**`, and the production image-contract
script must be byte-identical to the r11 tested candidate after this plan-only
append. The complete worktree, staged paths, `git diff --check`, release
identity, and secret scan must then be audited. Only if those checks pass may
v0.10.11 be committed, annotated-tagged, and pushed to `pig-origin`; the nested
repository's unrelated `origin` remote must not be used. Registry publication,
independent immutable-digest pull, OCI/startup/binary equivalence, the
Router-disabled `use1-cb` deployment, direct live gates, and a newly timed
30-minute canary remain later independent layers. `use1-cb` remains disabled,
no v0.10.11 deployment has occurred, and the Goal remains active.

##### v0.10.11 release-source identity gate — complete 2026-08-05

After the evidence/review append above, the downloaded r11
`candidate-source.sha256` manifest was compared directly with the current
working tree. All 246 release-relevant files were byte-identical: the gate
included `.dockerignore`, `Dockerfile`, `go.mod`, `go.sum`, every file under
`cmd/**` and `internal/**`, and
`tools/validate-production-image-contract.sh`. There were zero executable,
test, module, Docker, or production image-contract mismatches.

The complete 273-file manifest had exactly two expected non-release byte
differences. This plan differs because it records post-r11 evidence. `LICENSE`
is unmodified in Git but its Windows working-tree newline representation differs
from the normalized archive byte representation. Neither path enters the
compiled binary contract; the committed Git archive will normalize the latter.
No result for an executable path was excused on that basis.

`git diff --check` was clean, no paths were staged at the time of the audit, the
working tree contained exactly the 25 intended v0.10.11 paths recorded in the
diff, and the current release identity was consistent: runtime
`PIG-v0.10.11`, OCI `0.10.11`, image reference `v0.10.11`, predictor
`adaptive-tps-kv-v8`, and unchanged estimator `json-cost-lexical-hint-v2`.
No current `PIG-v0.10.10`, v0.10.10 image reference, OCI 0.10.10 label, or
`adaptive-tps-kv-v7` remained outside the mixed historical plan. The scoped
secret scan was clean. Local and remote `v0.10.11` tags did not yet exist, and
both local HEAD and `pig-origin/codex/pig-v0.10.0-model-agnostic` still pointed
to `a926d0dcae73e5ae467f12a2d7eef8084f7a201a` before release mutation.

This gate authorizes staging and committing the exact reviewed v0.10.11 source
and this audit history. It does not authorize use of the unrelated nested
`origin` remote, nor does it substitute for the annotated tag, pushed source,
registry image, immutable pull, live deployment, readiness, or canary layers.

##### v0.10.11 source release and immutable registry evidence — complete 2026-08-05

The 25 reviewed paths were staged explicitly; cached and working-tree
`diff --check` were clean and no unrelated path entered the commit. The release
commit is:

```text
c1ec43b08a062ed658c0e1d8554b8152876db7db
feat: retain predictive hard QoS memory v0.10.11
```

Its Git archive is
`../tmp/pig-v01011-builder-20260804/v01011-release-c1ec43b-source.tar.gz`,
SHA-256
`168d06b2967f07f5dda4f418b963d94f4b28e9947c808df4751062bef58c294c`.
An object-level comparison against the r11 candidate manifest again found zero
mismatches across all 246 release-relevant paths. The only complete-manifest
difference was this post-r11 plan history. This proves the committed executable
tree, Docker contract, module files, and tests are the exact r11 inputs.

Annotated tag `v0.10.11` has tag object
`979f93b47eebe3b6aee3f66adccc9767625c9574` and peels to the same release
commit. The branch and tag were pushed only to `pig-origin`; remote branch
`codex/pig-v0.10.0-model-agnostic` and `refs/tags/v0.10.11^{}` both resolve to
`c1ec43b08a062ed658c0e1d8554b8152876db7db`. The unrelated nested `origin`
remote was not used.

The tag triggered public GitHub Actions workflow `Publish Image`, run
`30966327629`, which completed successfully for the same head SHA. The official
registry tag resolves to:

```text
tag
  ghcr.io/phala-network/phala-inference-guard:v0.10.11
immutable digest
  ghcr.io/phala-network/phala-inference-guard@sha256:2983036b1454ed60d075d907d8991b6d2e8d5ea1b81ca7ca21acbf98dccaca9e
registry image ID
  sha256:1a3064cf8fdc40baa16764b9882c9d04955bcc6a5a5cd570d4ed73df272ba632
size/platform
  29,475,951 bytes; linux/amd64
OCI version / entrypoint
  0.10.11 / ["/phala-inference-guard"]
binary SHA-256
  8bf73af6b5c358f3fe854e675f86a600005e240084a4053a4195c5a25a8e52f9
```

The first registry verifier r1 completed tag and digest pulls, production image
contract, binary comparison, and startup identity, but its final evidence
enumeration used BusyBox-unsupported `find -printf`. Because the pipeline lacked
`pipefail`, it generated only the empty-stdin `-` checksum and printed a false
OK. r1 is retained as a verifier-harness failure and is not counted as registry
green. Its product outputs are corroborating only.

Fresh r2 used a new directory and BusyBox-compatible enumeration, repeated both
tag and immutable-digest pulls, ran the exact r11 production image-contract
script against the digest, extracted the binary, compared it byte-for-byte with
the r11 builder-local binary, and started a bounded container. The runtime log
reported `PIG-v0.10.11`, `predictive_admission=off`,
`dynamic_ttft_protect=false`, and six zero hard-origin status values. All 11
evidence files passed their internal checksum. The downloaded archive is:

```text
../tmp/pig-v01011-builder-20260804/
  pig-v01011-registry-r2-evidence-20260805T0133Z.tar.gz
remote and local SHA-256
  5eca5a3821b80dda1db0da3c95e15f4b1308ed6af9acb9a57e32683d55ccad4d
```

Independent local extraction repeated all 11 checksums and the binary hash.
The official registry image is therefore eligible to generate a fresh minimal
Compose candidate for the authorized Router-disabled `use1-cb` target. Because
the live rollback Compose is v0.10.10 `enforce` while the required new-image
validation order begins in `shadow`, that first candidate may change exactly
two values: the immutable PIG digest and `PREDICTIVE_ADMISSION_MODE` from
`enforce` to `shadow`. Every vLLM, Router, TTFT, cache, KV, and other Compose
value must remain byte-identical. This
does not itself prove Compose integration, CVM deployment, backend readiness,
shadow/enforce behavior, Router publication, or real-traffic safety. Those
remain ordered live gates; `use1-cb` is still disabled at this checkpoint and
the Goal remains active.

##### v0.10.11 Router-disabled shadow deployment and direct-live gates — complete 2026-08-05

Fresh predeploy evidence reconfirmed CVM
`a0f0bfb3-e46f-4b22-814e-24872f251193` running with no operation in progress,
Router upstream `use1-cb` disabled and drained, enabled set exactly `use1-19`,
and the v0.10.10 rollback Compose at SHA-256
`7831debce24924a7770b548f818068d2e08138411c246aa084491a3ff17821a9`.
The shadow candidate changed exactly the immutable PIG digest and
`PREDICTIVE_ADMISSION_MODE=enforce -> shadow`; it retained TTFT protection off,
legacy KV admission off, the five-second Router backpressure hold, every vLLM
field, and every other Compose byte. Candidate SHA-256 is
`645bf6790eec052e6b4e8636074210b1467b2424f066843bc2289403c1db3b24`.
One Compose-only deploy ran from `2026-08-05T01:44:10.8624968Z` through
`2026-08-05T01:48:20.6171638Z` and returned zero. The first postdeploy snapshot
at approximately 30 seconds observed startup HTTP 503 and was not treated as
readiness or failure.

The existing startup completed without a second deploy. vLLM logged
`Application startup complete` at `2026-08-05T01:53:40.264784332Z`; PIG then
reported `PIG-v0.10.11`, `predictive_admission=shadow`, KV admission off,
dynamic TTFT protection off, backend `1/1`, and
`hard_origin=0/0/0/0/0/0`. Fresh ready snapshot `shadow-ready-r5` proved the
exact Compose, immutable image digest and image ID, expected
`google/gemma-4-31B-it` model, authenticated models/PIG metrics/combined
metrics/attestation HTTP 200, unauthenticated metrics HTTP 401, Router still
disabled, and terminal zero. The v0.10.11 validator proved exactly six fixed
hard-origin series, all initially zero, exact per-dimension origin-sum parity,
the existing-prefill exploratory gauge, 885 stable PIG/combined metric lines in
parity, and restored idle capacity `50/50`.

The first log-capture attempt `shadow-startup-logs-r6` is retained as a harness
red: the current container-log API returned `{"error":"Invalid since"}` for the
documented RFC3339 form while the CLI still exited zero. Fresh r7 used the
accepted relative `30m` form and passed with one vLLM startup-complete marker,
one PIG v0.10.11 shadow startup, 85 zero-origin status records, 84 green backend
records, zero vLLM/PIG/serial critical matches, and a clean secret scan.

Direct protocol gate `shadow-protocol-r8` returned HTTP 200 for all five
independent shapes: normal chat, stream with final usage, required tool call,
strict JSON-Schema structured output, and CJK. Post-protocol snapshot r9
returned to terminal zero with five fit decisions, no prediction failures,
waiting, preemption, hard-adverse event, or Router activation; all six origin
counters remained zero and the v0.10.11 validator passed again.

Sparse gate `shadow-lowflow-r10` sent 20 intentionally separated requests. All
20 returned HTTP 200; the input-size learner was mature by request four, the
low lexical-ratio request caused zero input or hint invalidations, the following
ordinary request remained learned, and all 20 test requests used the learned
input estimate and lexical hint. It proved no false lock, sticky zero,
preemption, Router activation, or terminal residue. Its live histograms placed
20/20 predictions under one millisecond and 19/20 estimator samples under
0.25 milliseconds.

The independent 100-request latency gate `shadow-latency-r11` passed all HTTP,
metrics-parity, preemption, and terminal checks. The request-path estimator
histogram mean was `53.81 us`, with 100/100 at or below 0.25 milliseconds. The
complete prediction histogram mean was `61.84 us`, with 97/100 at or below
0.25 milliseconds and 99/100 at or below one millisecond. This meets the fixed
live gate but retains one prediction sample above one millisecond as a live-tail
watchpoint; it is not evidence that the path has zero jitter or that these CPU
histograms equal end-to-end serving latency.

QoS-source run `shadow-qos-source-r12` produced one normal 4,096-token decode
with qualified TPS/TPOT, then a deliberately client-throttled 1,024-token
stream that took `175.288303 s` at curl. The inherited v0.10.10 harness stopped
because it required that second request to increment only `local_censored`.
That requirement was too narrow, not a product failure: PIG recorded one
`local_corroborated` outcome using a fresh stable backend magnitude of
`234.720221 TPS` and `0.004260 s` TPOT, while the client wall clock implied only
`5.836100 TPS`. Independent v0.10.11 validation proved the client wall clock did
not supply the learned magnitude, qualified count advanced exactly once, the
backend magnitude remained on the safe side of the `20 TPS` and `0.05 s` hard
boundaries, every hard counter stayed unchanged, input-size feedback remained
live, preemptions stayed zero, and lifecycle returned to terminal zero. The
original harness red and corrected product-contract validation are both
retained. Post-QoS snapshot r13 passed the exact runtime/origin/metrics validator
with three bounded scheduler samples and all origin counters still zero.

Shadow cancellation gate r14 loaded two simultaneous decode requests with
predictive and observed decode counts both equal to two and waiting zero. After
the two client processes were terminated, all reservations, deferred state,
running, waiting, and Router protection returned to zero in `1,269.599 ms`; an
immediate small recovery request returned HTTP 200. Preemption and
resource-release-failure deltas were zero and PIG/combined lifecycle metrics
remained equal.

Final snapshot `shadow-final-r15` retained the exact Compose/image/model,
Router-disabled state, terminal zero, zero hard-origin values with dimension
parity, and all endpoint/authentication checks. Final 45-minute log audit r16
contained exactly one vLLM startup and one PIG startup, 288 zero-origin status
records, 287 green backend records, and zero vLLM/PIG/serial critical matches.
The cumulative shadow run reached 130 fit attempts, 128 successful vLLM
requests, zero predictive risk/unknown/enforced reject/failure, zero waiting,
zero preemption, and zero hard event. Consequently shadow proves protocol,
model-neutral request-size learning, low-flow progress, bounded request-path
latency, qualified QoS sourcing, cancellation reconciliation, telemetry parity,
and runtime stability. It does not exercise a nonzero live hard-origin event;
that behavior remains covered by the exact builder matrix and must still be
observed under the later origin-aware canary rules.

The Router-disabled shadow gate is complete. `use1-cb` remains disabled and the
enabled set remains exactly `use1-19`. The next authorized step is to generate a
fresh enforce candidate from the byte-exact live shadow Compose, change only
`PREDICTIVE_ADMISSION_MODE=shadow -> enforce`, repeat drift/idle preflight,
deploy once, and then repeat readiness, protocol, pre-forward rejection,
progressive learning, cancellation, lease expiry, origin parity, terminal-zero,
and critical-log gates. No Router enablement or production-readiness conclusion
follows from shadow alone; the Goal remains active.
