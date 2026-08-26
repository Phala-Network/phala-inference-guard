# PIG v0.12.23 TPS Health Gate Plan

Status: active design and execution plan. Version `0.12.23` is reserved for the
new behavior but must not be assigned in source, tagged, imaged, or deployed
until the source and clean-builder gates pass.

Formal starting branch: `codex/pig-v0.12.22-tps-only` at exact baseline commit
`ffa65f2375a83c4c9a69a601d7902c273fedc487`.

This plan supersedes the capacity-selection behavior in
`PIG_V0_12_22_TPS_ONLY_CONTROLLER_PLAN.md`. Public route, authentication, local
management, proxy, streaming, response, and attestation contracts remain
unchanged.

## 1. Objective

Maximize accepted, successful completion goodput while keeping the sufficiently
long mean output TPS per active Decode sequence near or above
`PREDICTIVE_TPS_REFERENCE`.

PIG combines a performance health gate with two simple admission bounds:

- it never derives, learns, warms, or explores a sequence limit from TPS;
- it does not divide aggregate TPS by the reference to derive capacity;
- it accepts arbitrary healthy concurrency that the backend continues to serve
  without material performance degradation, up to the backend's configured
  scheduler running limit when that limit is known;
- it protects only subsequent requests after qualified TPS degradation,
  waiting, preemption, running-limit exhaustion, or same-observation window
  concurrency exhaustion is observed;
- it reopens from current evidence without retaining a learned low cap.

The intended production override remains:

```text
PREDICTIVE_TPS_REFERENCE=25
```

Default backend polling remains 500 ms. The default window concurrency is 32
Decode sequences. Production normally needs no explicit setting for that
default, but `PREDICTIVE_WINDOW_CONCURRENCY` provides an initialization override.
`PREDICTIVE_RUNNING_LIMIT` provides an optional initialization override when the
backend cannot expose its scheduler limit. Both values are available through the
same revisioned admin policy API as the TPS reference.

## 2. Running Limit And Window Concurrency

Without a concurrency or resource forecast, forwarding every arrival against
one unchanged observation for as long as 500 ms could create an arbitrarily
large blind burst. PIG therefore separates three controls:

- the TPS health gate detects actual degradation and never selects capacity;
- the running-limit gate prevents projected backend running demand from
  exceeding the upstream scheduler's configured maximum when that maximum is
  known;
- the window-concurrency gate limits new Decode sequences that have been
  admitted but are not yet reflected by backend metrics.

The atomic pre-forward bounds are:

```text
projected_running = observed_running + observed_waiting +
                    unobserved_sequences + request_decode_sequences

running gate fits = running_limit disabled OR
                    projected_running <= running_limit

window gate fits  = unobserved_sequences + request_decode_sequences <=
                    window_concurrency
```

Complete request fanout charges both gates atomically. Existing unobserved
sequences consume window concurrency until observation reconciliation proves
them visible. The window value does not limit total running sequences: after a
fresh backend observation absorbs admitted work, another cohort can enter. A
healthy backend can therefore sustain concurrency far above 32; it cannot
receive more than 32 still-unobserved sequences from PIG at once by default.

Exhausting either gate returns the existing OpenAI-shaped `429` immediately; PIG
does not queue or wait. The bounded subreasons are `running_limit` and
`window_concurrency`, so Router and operators can distinguish them from
`below_reference`, `waiting`, and `preemption`.

### 2.1 Upstream running-limit discovery

The production adapters do not pretend that a current-running gauge is a
maximum:

- vLLM standard metrics expose `vllm:num_requests_running` but not
  `max_num_seqs`. The production OpenAI API does not expose the scheduler
  maximum. Current vLLM `/server_info` is a development endpoint with an
  explicit production security warning, so PIG must not depend on it. vLLM uses
  `PREDICTIVE_RUNNING_LIMIT` or the admin API when an operator wants this gate;
  zero means unknown/disabled;
- SGLang standard metrics expose `sglang:num_running_reqs` but not the configured
  `max_running_requests`. Latest upstream has a different
  `sglang:max_running_requests_under_SLO` gauge whose own source notes that it
  has no setter; it is not scheduler capacity and must not be consumed;
- SGLang's internal `/server_info` returns resolved startup `server_args`.
  On dev SGLang v0.5.18 on 2026-08-26, both top-level and scheduler internal
  state reported exact integer `max_running_requests=256`. PIG may read the
  top-level field during its existing trusted startup capability probe. Missing,
  noninteger, duplicate-inconsistent, out-of-range, wrong-content-type, timeout,
  or unsupported responses leave the running limit unknown instead of guessing.

An explicit environment or admin value overrides automatic SGLang discovery.
The effective source (`sglang_server_info`, `environment`, `admin`, or
`unknown`) is observable. This is initialization/administration, not learning.

Waiting and preemption are immediate current-observation pressure evidence and
pause new intake. They are not converted into a learned cap, cooldown, or
consecutive-clear requirement. Existing streams are never terminated by this
gate.

## 3. Decision Contract

The gate evaluates fresh normalized backend observations and the immutable TPS
window snapshot in this order:

1. TPS reference disabled: admit with `disabled`.
2. Invalid, missing, stale, drifted, or corrupt controller state: retain the
   existing availability protection.
3. Backend waiting is nonzero: protect with `waiting`.
4. The latest preemption delta is nonzero: protect with `preemption`.
5. The rolling window is not ready: health is open with `warming`.
6. No reliable Decode denominator exists for the current interval: health is
   open with
   `no_current_evidence`; this includes idle and pure Prefill intervals.
7. Rolling mean active TPS is at or above the reference: admit with
   `healthy_window`, even if one current interval is below the reference.
8. Rolling mean is below the reference but the latest qualified interval mean
   is at or above the reference: admit with `recovered_current` so recovery does
   not wait for the 60-second window to flush.
9. Rolling and latest qualified interval means are both below the reference:
   protect with `below_reference` until either becomes healthy.
10. When TPS health is open, evaluate the running and window gates atomically.
    Protect with `running_limit` or `window_concurrency` when the complete demand
    does not fit; otherwise reserve and forward.

This OR-based recovery preserves the long-average objective: one short bad poll
does not close a healthy long window, while a fresh healthy poll reopens a
degraded long window immediately. It adds no hysteresis knob, sequence cap,
model threshold, or learner.

The TPS window owns current-interval qualification and denominator selection.
The gate must not independently divide by raw ending concurrency because that
can report false health while requests start or finish between polls. A tracked
Decode interval with zero generated tokens is qualified zero TPS; a pure
Prefill interval without known Decode exposure is not qualified TPS evidence.

## 4. Architecture And SOLID Boundaries

```text
request shape and Decode fanout validation
  -> fresh normalized backend observation
  -> TPS window updates rolling and latest qualified interval evidence
  -> pure TPS health decision
  -> running-limit and window-concurrency projection
  -> reserve lifecycle exposure only when admitted
  -> forward and reconcile terminal lifecycle
```

- Request parsing validates protocol and reports Decode multiplicity; it never
  selects performance capacity.
- Backend adapters normalize counters and identity; they never choose policy.
- The TPS window exclusively owns time weighting, denominator selection,
  qualification, rolling mean, and latest qualified interval evidence.
- The TPS gate is a pure health predicate over an immutable snapshot.
- A running-limit policy consumes one immutable initialized/administered value;
  it never infers capacity from TPS.
- A window-concurrency policy consumes one immutable revisioned value and the
  unobserved overlay; it never changes TPS health.
- The reservation ledger owns sequence exposure and terminal reconciliation;
  unobserved sequence totals only consume window concurrency and never define
  sustainable concurrency.
- HTTP code maps decisions and emits bounded evidence; it never recomputes TPS.

Remove the obsolete QoS-budget layer and all fields used only to select or spend
a sequence limit: `qosBudgeted`, QoS-budget leases/subreasons, sequence limit,
post-admit sequence count, and current-sequence capacity comparison. Keep
unobserved sequences, sequence liabilities, live reservations, and residual
debts where required for exposure accounting, lifecycle correctness, and the
window-concurrency projection.

The hot decision path remains O(1), allocation-free after bounded request-shape
parsing, and independent of the number of live reservations.

## 5. Observability Contract

Logs, status, and metrics must expose the health decision without pretending a
capacity estimate exists:

- configured TPS reference;
- rolling readiness, qualified samples/sequence-seconds, aggregate TPS, and
  mean active TPS;
- latest interval qualified flag, aggregate TPS, mean active TPS, and
  denominator evidence;
- running, waiting, preemption delta, unobserved sequences, liabilities, and
  live reservations as observations only;
- effective running limit, its source, projected running, window concurrency,
  and projected unobserved window demand;
- decision result and one bounded subreason;
- enforced reject counters attributable to `waiting`, `preemption`, or
  `below_reference`, `running_limit`, or `window_concurrency`.

Delete TPS-derived sequence-limit, post-admit, and QoS-budget metrics/log fields.
Do not log
request bodies, tokens, credentials, prefixes, user identifiers, or unbounded
labels.

## 6. Test-First Execution

### Phase 1: freeze the v0.12.22 failure

Add focused tests matching the dev risks and the revised contract:

- a healthy request that fits running and window bounds must not be rejected by
  a lower TPS-derived sequence cap;
- a same-observation burst admits complete fanout exactly through the configured
  window concurrency, then rejects the next request with
  `window_concurrency` and zero reservation;
- a request whose projected running exceeds a known running limit rejects with
  `running_limit` and zero reservation;
- changing TPS evidence cannot change either configured bound, and changing a
  bound cannot relabel TPS health.

The first test must fail on v0.12.22 because its derived sequence limit is
exhausted. The other tests must fail because v0.12.22 lacks these independent
policy contracts. No failure may come from invalid fixture state.

### Phase 2: implement the health gate

1. Extend the TPS window snapshot with its latest qualified interval result.
2. Replace capacity selection with the decision contract in section 3.
3. Add strict optional SGLang startup discovery plus explicit initialization and
   admin overrides for running limit.
4. Add atomic running-limit and window-concurrency gates.
5. Remove QoS-budget policy and reservation ownership.
6. Remove obsolete decision fields and reconcile logs, metrics, status, tests,
   simulation, README, and advanced documentation.
7. Preserve controller freshness, identity, atomic lifecycle, and runtime-reset
   safety.

Focused coverage must prove:

- warming and idle do not reject high concurrency without pressure evidence;
- healthy rolling TPS admits regardless of raw, unobserved, reserved, or batch
  sequence counts when both explicit bounds fit;
- a same-epoch burst spends window concurrency atomically and excess demand gets
  an immediate 429 with no reservation or backend call;
- a known running limit cannot be overspent by concurrent requests;
- one low current interval does not close a healthy rolling window;
- sustained low rolling plus low current TPS protects;
- current recovery reopens immediately while the long window is still low;
- waiting/preemption protect for their current observation and the first fresh
  clear healthy observation reopens;
- pure Prefill is not mislabeled zero-TPS degradation;
- tracked Decode with zero tokens is qualified degradation;
- stale metrics, counter rollback, runtime reset, cancellation, failure, and
  terminal races remain correct;
- vLLM and SGLang adapters produce the same normalized TPS contract.
- SGLang startup discovery accepts only coherent `max_running_requests`; vLLM
  standard metrics never fabricate a maximum from current running.

### Phase 3: three reviews and clean-builder verification

Record three reviews and correct every issue before release assignment:

1. Model and causality: prove only qualified TPS/waiting/preemption changes a
   pre-forward decision and sequence counts do not.
2. Safety and lifecycle: prove no stale evidence, exposure leak, double release,
   reset bypass, or race.
3. Evidence and release: prove focused red/green validity, documentation and
   observability consistency, and exact clean-builder provenance.

Run on the approved remote builder, not local Windows:

```text
scripts/verify-no-legacy-mode.sh
gofmt check
go test ./...
go test -race ./...
go vet ./...
go build ./...
focused hot-path benchmarks
deterministic TPS health traces
```

### Phase 4: release and isolated dev test

Only after Phase 3 succeeds:

1. assign `0.12.23`, commit, push, and create an immutable tag;
2. build on the remote builder and verify OCI version/revision and production
   image contract;
3. push immutable and release tags, then verify the registry digest;
4. over SSH, update only `phala-inference-guard-b` on dev CVM
   `19a2d062-af63-49eb-807d-84ddfbbc905a`; never use `phala deploy` or restart
   the CVM, SGLang, PIG-A, or HAProxy;
5. retain runtime secrets without printing them;
6. verify health/auth/routes/chat/streaming with minimal requests, then exercise
   representative sequential and concurrent traffic without turning one
   numeric result into a universal hard gate;
7. inspect TPS, waiting, preemption, reservation cleanup, errors, and restarts.

Source must be pushed for each accepted source update. A registry image is
published only after the complete builder verification succeeds.

## 7. Current State

- `v0.12.22` source, tag, image, and dev PIG-B runtime are reproducible baseline
  evidence; they are not production candidates;
- the v0.12.23 behavior is implemented and pushed on branch
  `codex/pig-v0.12.23-tps-health-gate`. The exact executable review HEAD is
  `654931dcff21c844be977a70e103bf526a861db3`; source still reports
  `PIG-v0.12.22`, so no v0.12.23 version assignment, tag, image, or deployment
  exists yet;
- dev/backend capability audit on 2026-08-26 confirmed that SGLang-B standard
  metrics contains only current `sglang:num_running_reqs`, while internal
  `/server_info` and its scheduler state both return
  `max_running_requests=256`. vLLM upstream source exposes only current
  `vllm:num_requests_running` in standard metrics; its `/server_info` belongs to
  explicitly unsafe development endpoints. These findings produced the strict
  discovery contract in section 2.1;
- the first focused red test is valid at exact pushed commit
  `90c6d56b7dd39eda1495174768088999a2521996`, source archive SHA-256
  `3b5f315d5cf676828dcdc89c53e32ec2e84e53d2f01bb09bff7e8a5e38971d92`.
  On builder `4f167f6e-4c50-415f-99f2-94b65652beba` with pinned Go image
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`,
  `TestV01223HealthyWindowDoesNotTurnBlindFlightBudgetIntoConcurrencyCap`
  failed for the intended behavior: healthy TPS with `running=1`,
  `unobserved=3`, and the fourth window admission was rejected at inherited
  `sequenceLimit=4`, `postAdmit=5`, subreason `qos_budget_unobserved`. The red
  output SHA-256 is
  `50719fc92444cddbcf7641f627ff6911f5aa7144a6c63f2f72bd76363439ea05`;
- focused and integration implementation now replaces TPS-derived capacity with
  the independent TPS-health, running-limit, and same-observation window gates.
  Request fanout is reserved atomically; policy updates, SGLang initialization
  discovery, lifecycle reconciliation, reporting, metrics, logs, status, and
  deterministic simulations are wired to the new contract;
- each successful non-reset observation samples unreconciled Decode sequences
  before reconciliation. The cumulative histogram uses finite bounds
  `0,1,2,4,6,8,10,12,16,20,24,28,32,36,40,44,48,52,56,60,64`; all values above
  `64` are combined only in `+Inf`. This work runs on the observer path, not the
  request admission hot path;
- the complete builder test at exact commit
  `83fe43da7fd862d57a1563e68eed758130efe08f` passed `go test ./... -count=1`
  with exit `0` on builder `4f167f6e-4c50-415f-99f2-94b65652beba`, using
  `go1.24.13 linux/amd64` and pinned image
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
  Evidence directory is
  `/var/volatile/dstack/persistent/.cache/pig-v01223-health-gate/full-83fe43d-r1`;
  `test.log` SHA-256 is
  `0d23f562bc2117f7415d7977349bcfcdb96643d4baea28ea419eaf4e18f410f2`;

### Review 1: model and causality

- TPS only reports health; it cannot select, learn, warm, or lower a concurrency
  limit. Waiting, a fresh preemption, or qualified rolling and current TPS below
  reference changes the pre-forward decision. Running and same-observation
  bounds are independent configured facts;
- request fanout changes only the atomic running/window projections and complete
  reservation size. Histogram collection samples the pre-reconciliation overlay
  on the observer path and cannot affect admission;
- review found that an explicit startup `PREDICTIVE_RUNNING_LIMIT=0` was
  indistinguishable from an absent variable, so SGLang discovery incorrectly
  enabled a limit. The valid builder red at commit
  `89f69e628842a3ca2ed6bc74bb76819467cb13da` observed
  `Value=256 Source=sglang_server_info calls=1`; red log SHA-256 is
  `3cfde40ac59b32ec6b8fc4dfc2ad316534740e9ba6cbfe68c449ee3b753a2118`.
  Presence-aware initialization at `bf89bce3538ef62c11731d349424c6f8ca5e3219`
  passed the focused config/server tests with log SHA-256
  `1ec053a6c77a230b5d422d288f119fe43c965975a038b7e1f5d4845299ea1ab3`;
- the finalized histogram finite bounds are
  `0,1,2,4,6,8,10,12,16,20,24,28,32,36,40,44,48,52,56,60,64`. All observations
  above 64 are combined in the single Prometheus `+Inf` bucket.

### Review 2: safety and lifecycle

- admission check and full-fanout reservation share one Controller mutex.
  Reconciliation is sample-watermark fenced; completion, cancel, failure,
  timeout, disconnect, reset, shutdown, duplicate terminal calls, and stale
  handles converge on bounded lifecycle behavior;
- startup discovery is initialization only. vLLM never infers a maximum.
  SGLang accepts one bounded top-level integer only when the environment variable
  is absent; explicit zero disables discovery and the gate;
- review found that the default HTTP client could follow `/server_info`
  redirects, violating the same-origin contract. The builder red at commit
  `23a51ffa062c7f68c068ae8edf1f6917127833ca` accepted redirected value 256 and
  called the target once; red log SHA-256 is
  `dbcb571b140b02c0c2610a8cccb404010aa1d89672372e1e97bdd363cd2899fb`.
  The fixed client disables redirects and environment proxying. Focused discovery
  tests passed at `3c2890960904491d3b0db218da015bf5716cbf59` with log SHA-256
  `cc00349e7f3528446b5297ac9616441e674ea2400c4769d804e65fc4291f15d4`;
- discovery failure logging now uses fixed low-cardinality fields and does not
  print the raw client error or endpoint URL. No request body, prompt, token,
  credential, user identifier, or unbounded label was added.

### Review 3: evidence and release

- the first matrix at `da7ba4398d755df77156fda7df62a0eadbb442af`
  passed legacy audit, formatting, full tests, race, vet, and build, then correctly
  exposed stale benchmark setup: fixtures tried to preload 4096 reservations
  under the production default window of 32. Only the benchmark fixture was
  changed to declare its required window; the production default remains 32;
- the complete clean-builder matrix passed at exact HEAD
  `654931dcff21c844be977a70e103bf526a861db3` on builder
  `4f167f6e-4c50-415f-99f2-94b65652beba`, `go1.24.13 linux/amd64`, using pinned
  image `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
  Evidence is
  `/var/volatile/dstack/persistent/.cache/pig-v01223-health-gate/full-654931d-r1`;
  source archive SHA-256 is
  `afedc4d570ee853ace604da230579533fc4e71674b9bf3baab28fa5210a40496`;
- all nine required step exit codes are zero. Material log SHA-256 values are:
  legacy audit `455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0`,
  full tests `073aa3a027e79560c96e3ca342d5a3f43bf99c8d8f952846a7e280b0a45b3dbb`,
  race `d4ff9ef3427cdb1a9f9de60692db65acd0e05f9c79cbb2d232515cb20f88af3e`,
  controller benchmark
  `d1273ecadd07021953c5ec7229144f61f7b9782d4a1bb1574e71b59a05f34f77`,
  scanner benchmark
  `b6f4f8d07dbe64ad6537e27516c4bafb9540ce9e6f2b032e008531c911e4c971`,
  and simulation log
  `633fedd632b40bbfebf5a1a20ea82da4abdd3f0f512d8c5cd2a034e03d43c4a5`.
  Gofmt, vet, and build logs are empty by design;
- deterministic simulation outputs were byte-identical with SHA-256
  `e56d63166749ff968c26844d2a1909a00f0300c150b706399ee465dccd7ac13a`.
  Controller hot paths used zero allocations: snapshot 471-476 ns/op,
  protected admission 826-891 ns/op, admit/cancel 572-693 ns/op, and a 4096
  reservation observation 365-384 microseconds/op. The 4 MiB full classifier was
  6.86-8.01 ms/op and its zero-allocation shape parser was 5.51-5.68 ms/op;
- phase 3 is complete. Version `0.12.23` was assigned at exact release commit
  `a4527759284b7ec3a7be060111f638740d7345a4`; the branch and annotated
  `v0.12.23` source tag are pushed and resolve to that commit. The post-assignment
  builder source gate passed legacy audit, formatting, full tests, race, vet,
  and build in
  `/var/volatile/dstack/persistent/.cache/pig-v01223-health-gate/release-source-a452775-r1`;
  its source archive SHA-256 is
  `4cdc60577a7a196f8965c5975b8b751e7ef93db98ce7698e488c51e37ba42234`;
- the builder-local release candidate passed the production image contract with
  image ID
  `sha256:0ceca4eccb11c1ebcbe92fe7d9312c789b46e9bffface5827d77879c50e61ea1`,
  OCI version `0.12.23`, and exact OCI revision. Evidence is
  `/var/volatile/dstack/persistent/.cache/pig-v01223-health-gate/image-a452775-r1`;
- the tag workflow independently published
  `ghcr.io/phala-network/phala-inference-guard:v0.12.23` at digest
  `sha256:d7a8161a6dd909b525369475c454f2508c8ccd716afb91733929f9d10cca6e56`.
  A digest pull proved exact version/revision and passed the production image
  contract; evidence is
  `/var/volatile/dstack/persistent/.cache/pig-v01223-health-gate/ci-vtag-a452775-r1`;
- the production-convention tags `0.12.23` and `0.12.23-a4527759284b` remain
  absent. The first direct push attempt wrote neither tag because the builder
  GHCR credential was expired. A GitHub device authorization with
  `write:packages` is pending; no registry tag will be overwritten. Dev PIG-B
  remains unchanged until both tags and their common digest are verified.
