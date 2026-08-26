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
