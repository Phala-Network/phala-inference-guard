# PIG v0.12.25 Minimal Router Metrics And Waiting Guard Plan

Status: active implementation and release plan.

Baseline is immutable tag `v0.12.24` at
`4106ed49379935ca9ca99fb1493688776012caf8`. The active source branch is
`codex/pig-v0.12.25-minimal-router-metrics`. Version `0.12.25` is assigned in
source but has no tag, registry image, or deployment yet.

## 1. Objective

Keep the TPS health controller simple while preventing backend waiting from
growing across successive 500 ms metrics observations.

The correction has two independent parts:

- `/pig/metrics` is the fixed five-line Router capacity contract. Full PIG and
  backend diagnostics remain on authenticated `/v1/metrics`.
- Waiting protection does not depend on whether TPS reference protection is
  enabled. A fresh observation with `waiting > 0` closes new intake immediately.

PIG still does not learn a concurrency limit. `window_concurrency` is not a
total-running cap and does not limit requests that have reached first byte.

## 2. Pending-First-Byte Guard

Every admitted Decode sequence consumes the existing atomic window budget until
one of these events occurs:

1. the upstream response produces its first byte;
2. the request completes, fails, is cancelled, disconnects, or times out; or
3. a short lease expires and a successful fresh backend observation reports
   `waiting = 0`.

The lease duration is derived internally as three metrics polling intervals.
With the production default poll interval of 500 ms, the lease is 1.5 seconds.
It is not another production setting and is not exposed through the admin API.

An ordinary metrics poll alone must not release a pending request. This fixes
the v0.12.24 failure where the event watermark marked every forwarded request as
visible without proving backend materialization, allowing another complete
window every poll and permitting waiting to accumulate above the configured
window concurrency.

The implementation reuses the existing reservation lifecycle and Controller
mutex. It does not add a second debt ledger, watchdog, background reaper, model
threshold, learner, cooldown, or queue. Admission remains O(1), and protected
requests receive the existing OpenAI-shaped 429 immediately.

## 3. Safety Contract

- admission and pending-first-byte reservation are atomic;
- first byte and every terminal path release the guard exactly once;
- duplicate lifecycle calls cannot underflow or reuse capacity;
- an invalid, failed, stale, or identity-drifted observation cannot expire a
  lease;
- a fresh observation with `waiting > 0` cannot expire a lease;
- runtime reset and shutdown fence old handles and clear bounded state;
- long TTFT cannot hold the guard indefinitely when fresh metrics remain
  healthy, while a normal 500 ms poll cannot prematurely refill it;
- disabling TPS reference for a test does not disable waiting protection.

## 4. Router Metrics Contract

Authenticated `/pig/metrics` outputs exactly these five lines in fixed order:

```text
pig_dynamic_observed_running <value>
pig_dynamic_observed_waiting <value>
pig_dynamic_global_limit <value>
pig_predictive_admission_enforce <0|1>
pig_predictive_router_backpressure_applied <0|1>
```

Open state requires `global_limit=0` and `backpressure_applied=0`. Protected
state requires `global_limit>0` and `backpressure_applied=1`.

## 5. Test And Release Sequence

1. Add focused red tests for waiting independence, poll-boundary retention,
   first-byte release, terminal release, healthy lease expiry, and no expiry
   under waiting.
2. Implement the smallest reservation-state correction and update current
   documentation.
3. On the remote Linux builder run formatting, focused tests, full tests, race,
   vet, build, deterministic simulations, and image/HTTP contract smoke. Do not
   run executable Go tests on local Windows.
4. Push every accepted source update. Publish the immutable `0.12.25` image only
   after the exact versioned source passes the complete builder matrix.
5. Re-query all target CVMs. Skip stopped or drifting targets. For non-dev CVMs,
   update through Phala Compose; never SSH-edit them.
6. Roll out by model group with one canary first and at least one other node
   still serving. Validate process readiness, authenticated models/metrics,
   exact five-line Router metrics, backend identity, waiting, reservations,
   restarts, OOM, and fatal logs. Send no synthetic production inference.
7. Expand only after the canary is healthy, then perform a final fleet audit.

Target groups from the 2026-08-27 preflight are:

```text
Gemma4 / vLLM
  bf47b91b-77f9-44ab-a081-284268e205f7
  210665da-6868-469d-a729-c342b8dc59e4
  5d961f5e-0b3a-4419-a9c0-a3df600ad4ca
  19696a78-17a8-4d85-8899-4eccd24adf93
  9949143b-4c06-4b81-8c24-f96a8b1593eb

Qwen3.8 / SGLang
  3e4d7151-d56b-4e26-8403-42717d1f7367
  5c2c59ea-3bf3-4a2f-8ae8-99a10cc21037
  a1212298-f34d-4688-be54-162d84fef662
```

All eight were running, not in progress, non-dev `dstack-nvidia-0.5.9`, on PIG
`0.12.24`, with only `PREDICTIVE_TPS_REFERENCE=25` explicitly configured. This
inventory is a preflight snapshot and must be refreshed before deployment.

## 6. Review Gates

Review 1, model and causality: prove only waiting, TPS health, configured bounds,
first byte, terminal lifecycle, or a qualified lease expiry changes admission.

Review 2, safety and efficiency: prove exact release under concurrency, no
underflow/leak, no invalid-observation expiry, bounded O(1) hot-path state, and
no long-TTFT permanent lock.

Review 3, evidence and release: bind tests, image, digest, Compose candidates,
and live readiness to exact immutable source. Keep source, image publication,
deployment, and healthy runtime as separate completion layers.

## 7. Red Evidence

The focused contract was pushed at exact commit
`9b75ab9b096a7a2512c0810691a84fb4a8c9a9ff`. On remote builder
`4f167f6e-4c50-415f-99f2-94b65652beba`, the first runner stopped before tests
because a login shell removed Go from `PATH`; it is retained only as runner
failure and is not behavior evidence.

The corrected `red-9b75ab9-r2` runner used `go1.24.13 linux/amd64` from pinned
image
`golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
The focused test exited `1` and failed all four intended v0.12.24 behaviors:

- TPS reference zero admitted while backend waiting was one;
- the first ordinary metrics poll erased two pending-first-byte reservations;
- a later observation with waiting one erased an expired lease and reported
  intake available; and
- a forwarded timeout retained its pending-first-byte window slot.

The red log SHA-256 is
`c463277341d4461ec971d4b5a87173d631d54d240e0cea98a63e5b3603a287b7`;
the environment record SHA-256 is
`7efbbc23c81b06391e71920a448d0dfdabdb52e8c2db70547f3f43473ae18705`.
No failure came from compilation, dependencies, invalid fixtures, or an
unrelated package.

## 8. First Green Runner Correction

The first green candidate was pushed at
`544f1e6ba7035997aff76f637d3c957f9bad7431`. Builder directory
`green-544f1e6-r1` stopped at the first legacy audit, before formatting or any
Go test. The five-line Router endpoint had rendered compatibility metric names
directly in `internal/app/server/metrics.go`, while the repository contract
requires those names to be owned exclusively by
`internal/observability/metrics/router_capacity_compatibility.go`.

This is a valid architecture failure in the earlier metrics-only change, not a
waiting-test result. The correction moves the exact five-line rendering to a
dedicated observability writer; server code now only computes the snapshot and
passes typed values. No metric name, order, value, admission decision, or
lifecycle behavior changes. The failed evidence directory is retained and is
not reused.

## 9. Second Green Runner Compatibility Correction

The architecture correction was pushed at
`0f2482c865d4f58c21a90ee2abd7a373dcf39d76`. Builder directory
`green-0f2482c-r2` passed the legacy ownership audit, formatting check, and all
focused admission, waiting-lifecycle, and Router-metrics tests. The log hashes
are:

```text
legacy.log
455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0

format.log
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855

focused.log
1c9ca72104e137653a4dff38b2f645a0e093e757b3cd51fd4d108a09e5439fba
```

The full suite then stopped at
`TestV01215PredictivePolicyAPIAppliesCASAndExportsMetricsAndStatus`. That
pre-existing test still queried `/pig/metrics` for the complete policy metric
set. The new contract intentionally keeps that set on authenticated
`/v1/metrics` and reserves `/pig/metrics` for the fixed five-line Router
surface. The test must follow this endpoint ownership; restoring diagnostic
metrics to `/pig/metrics` would violate the release contract. No later gate ran
in this evidence directory, and the directory will not be reused.

## 10. Third Green Runner Backend-Call Correction

The endpoint compatibility correction was pushed at
`d95914f03685c39d292cd388446d61fb03ac31c6`. Builder directory
`green-d95914f-r3` again passed the legacy ownership audit, formatting check,
and focused admission, waiting-lifecycle, and Router-metrics tests. Its focused
log SHA-256 is
`c3020959158a658ecf2e2ad821bdb3524c8ec9ea6a055bd1a57dc5f44929a6cc`.

The full suite reached the corrected policy test and proved the complete
metrics were available on `/v1/metrics`, then failed its old final expectation
that policy and metrics requests together never call the backend. The combined
metrics endpoint intentionally fetches backend metrics once. The corrected
test now separately proves policy GET/PATCH calls have zero backend calls and
that one authenticated `/v1/metrics` request has exactly one backend call. The
full log SHA-256 is
`e7373746bf77d62de9982e0d6d63ae5708b87c11a99ddae2690d47c9d21bd991`;
the environment record SHA-256 is
`a16b02d928f984d6df8769058899dadcc505cd1afc8e66c841284e615c1e1864`.
No later gate ran in this evidence directory, and the directory will not be
reused.
