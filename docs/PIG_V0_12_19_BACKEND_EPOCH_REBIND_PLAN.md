# PIG v0.12.19 backend epoch rebind repair plan

## Objective

Repair the production failure in which an independently restarted vLLM
runtime reports a slightly different KV capacity and PIG permanently closes
with `capability_drift`. PIG must fail closed while the backend is unavailable,
discard all old-runtime lifecycle ownership, accept a bounded and explicitly
identified new runtime epoch, and reopen without weakening same-runtime model,
block-geometry, Prefill, KV, waiting, preemption, request-size, or TPS
protection.

This is a lifecycle correctness repair. It is not a new throughput-policy
experiment and it does not authorize changes to vLLM, Router routing policy,
TPS reference, tokenizer behavior, long-input policy, or Prefill policy.

## Production incident evidence

At `2026-08-24T00:28:21Z`, the Gemma4 Router had nine enabled routes but only
two selectable routes. `use2-19`, `use2-3b`, `use2-4c`, `use2-9b`, `use2-bb`,
`use2-cb`, and `use2-db` all exposed fresh PIG metrics with:

```text
request_aware_protected
hard_protect / capability_drift
availability / capability_drift / unavailable
raw running/waiting/global_limit = 0/0/0
effective running/waiting/global_limit = 1/0/1
```

All seven vLLM EngineCore processes had been killed by the Linux host-memory
OOM killer. Their replacement runtimes kept the same backend kind, model,
maximum model length, and KV block size, but startup profiling changed:

```text
old KV capacity          1,936,443 tokens
new KV capacity          1,936,065 tokens
delta                    -378 tokens (-0.0195%)
old/new KV memory        100.02 / 100.00 GiB
old/new GPU blocks       20,484 / 20,480
KV block size            64 / 64
max model length         262,144 / 262,144
```

PIG v0.12.17 first entered `observation_stale`, then permanently closed when
the recovered backend emitted the new capacity. Existing v0.12.18 source has
the same strict capacity comparison and permanent-close behavior.

The vLLM host-memory growth remains a separate incident. The repair must make
PIG survive an independently restarted backend, but it must not claim to fix
or hide vLLM OOMs.

## Repair contract

1. A capability rebind is eligible only when both the previous and new
   `process_start_time_seconds` values are present and different.
2. Backend kind, canonical model identity, maximum model length, and KV block
   size remain immutable. Any change permanently fail-closes.
3. The new KV capacity must be positive, must contain the existing hard KV
   limit, and must validate with the existing immutable policy geometry.
4. The observer requires two consecutive coherent samples for the same new
   runtime start and capability before publishing the rebind. A scrape error,
   invalid sample, metadata failure, or changing candidate breaks the streak.
5. `/v1/models` metadata is revalidated before a restart sample can rebind.
6. Rebind is atomic with runtime reset. Old reservations, pending Prefill,
   cache evidence, TPS evidence, exposure, and old handles are invalidated
   before the new epoch is available.
7. Existing hard KV, maximum-input, and Prefill limits remain unchanged during
   the live Controller lifetime. This is conservative when capacity increases
   and safe when it decreases but still contains the hard limit. The next PIG
   process initialization may derive a new policy from the then-current
   capacity.
8. Capacity drift without an explicit backend runtime change remains permanent
   fail-close. A capacity drop at or below the existing hard limit also remains
   permanent fail-close.
9. Rebind updates exported capability capacity, runtime epoch, a monotonic
   rebind counter, and a structured log. Router protection must remain active
   while samples are stale or the rebind is unconfirmed.
10. No request is forwarded based on a candidate sample that has not passed the
    full rebind contract.

## Code structure

- `internal/admission` owns the atomic runtime-reset transaction, immutable
  identity checks, safe capacity containment, lifecycle invalidation, and old
  handle rejection.
- `internal/app/server/admission_backend_observer.go` owns consecutive-sample
  qualification and metadata revalidation, not policy decisions.
- `internal/app/server/admission_runtime.go` owns the race-free runtime profile
  and rebind observability exposed to logs and metrics.
- Existing initializer and policy code remain the only source of initial KV,
  Prefill, maximum-input, and TPS geometry.

This preserves SOLID ownership: telemetry qualification, admission safety,
runtime presentation, and policy derivation remain separate.

## Test-first acceptance matrix

Red tests must first reproduce the current failure and then pass only after the
repair:

1. vLLM runtime start changes and KV capacity decreases slightly: the first
   coherent sample remains uncommitted, the second atomically rebinds, and the
   Controller reopens.
2. The rebind clears reservations and rejects every old-epoch handle operation.
3. A third ordinary sample from the rebound runtime remains usable and does not
   re-enter drift.
4. Same-runtime KV capacity drift permanently closes.
5. Restart plus model, max-model-length, backend-kind, or block-size drift
   permanently closes.
6. Restart plus capacity at or below the old hard limit permanently closes.
7. A failed metadata lookup or interrupted candidate streak leaves the old
   observation to become stale and never publishes the candidate.
8. Concurrent admission, terminal release, policy update, and rebind preserve
   atomicity under `go test -race`.
9. vLLM and SGLang observer contracts continue to pass; no backend-specific
   shortcut is introduced into the admission domain.
10. Existing controller, lifecycle, property, simulation, metrics, HTTP, and
    release-identity suites pass unchanged except for the new version contract.

Required remote gates:

```text
gofmt check
go vet ./...
focused admission and observer tests
go test ./...
go test -race ./internal/admission ./internal/app/server
git diff --check
docker image build
container startup/version smoke
registry manifest and digest pullability
```

No executable tests are run on local Windows.

## Release and deployment plan

The repair version is `v0.12.19`. Source is committed and pushed before image
publication. The image is published only after all acceptance gates pass and
is deployed by immutable digest.

Authorized targets:

```text
use1-4c  bf47b91b-77f9-44ab-a081-284268e205f7
use2-19  210665da-6868-469d-a729-c342b8dc59e4
use2-3b  5d961f5e-0b3a-4419-a9c0-a3df600ad4ca
use2-4c  19696a78-17a8-4d85-8899-4eccd24adf93
use2-5d  9949143b-4c06-4b81-8c24-f96a8b1593eb
use2-9b  728dd4a2-aed3-4300-bddf-926f6ed1c601
use2-bb  abfdf7b1-d145-48a6-8bbb-c9f536fa75a1
use2-cb  4aa0f5cc-e98a-427d-9dcd-afd2eb4452d7
use2-db  a1b2c2a4-7aa2-4688-a74b-017904c05c8d
```

Before each mutation, refresh the exact Router enabled set, live Compose hash,
PIG image, and vLLM process start. For each target:

1. disable only that route if it is enabled;
2. wait for Router, PIG, and backend running/waiting plus PIG reservations to
   drain to zero;
3. recheck Compose and route identity for drift;
4. change only the PIG image reference to the accepted v0.12.19 digest;
5. prove the vLLM image, command, container identity, and process start did not
   change;
6. require PIG `v0.12.19`, fresh observations, `request_aware_open`, no
   lifecycle failures, and Router `selectable=true` while enabled;
7. restore the route only if it was enabled before the update;
8. observe before advancing to the next target.

The rollout stops on any vLLM restart, host OOM, model/config drift, PIG restart
loop, stale metrics, lifecycle failure, unexpected 429 protocol, or inability
to restore the exact route state. No whole-CVM restart is permitted.

## Completion boundary

The repair is complete only after source push, remote test evidence, immutable
image publication, all nine serial deployments, exact route restoration, and
a final Router/PIG/vLLM audit. The unresolved vLLM host-memory growth is
reported separately and is not mislabeled as repaired by PIG v0.12.19.

## Source execution record

Status at `2026-08-24T01:30Z`: the implementation candidate and remote source
matrix are accepted for an exact-source commit. Image publication, Compose
mutation, and production rollout have not yet started.

The red contract was committed and pushed first:

```text
branch                 codex/pig-v0.12.19-backend-epoch-rebind
red commit             957652de2e57a0a660ffe2c7418884a4b0b5d50b
builder app            ff40ee31b95e89ebb242c223514adc715ac8a301
builder environment    Linux amd64; Docker 25.0.3; golang:1.24-bookworm
Go                     go1.24.13 linux/amd64
```

The current implementation failed for the intended old behavior:

```text
controller-red.log     c3d8f716b465ef5e9868e0e1df236c2ce43010ee55610867c8d3f0029d874149
observer-red.log       4b168b1116a19faba2d78ef15486f6b67f375b9156ad6322aaadf5f915338819
```

The Controller permanently returned `capability_drift` for a safe new runtime
whose capacity decreased by 384 tokens, while the Observer performed zero
metadata calls for that candidate. Unsafe restart capacity and same-runtime
capacity drift remained closed in the red baseline.

The implemented vertical slice is:

1. Controller separates immutable model/context/block/policy geometry from the
   only rebindable field, raw KV capacity.
2. A safe explicit runtime-change candidate immediately makes the old
   observation unavailable; no request can use it while confirmation is
   pending.
3. Observer metadata failure or a changing/invalid sample breaks the candidate
   streak. Two coherent samples are required before publication.
4. Controller accepts the publication only when the new capacity still
   validates and contains the existing hard limit. Rebind, epoch advance, old
   lifecycle invalidation, and new observation publication share one lock.
5. Same-runtime drift, identity/context/block drift, and unsafe capacity remain
   permanent fail-close. No request-size, Prefill, KV hard-limit, TPS, cache, or
   routing policy changed.
6. Runtime telemetry projects the Controller-owned current raw capacity without
   a second mutable profile. Runtime epoch, pending state, rebind count, and one
   structured acceptance log are exported.

The first concurrent gate found that assigning the complete `Capability`
structure during rebind raced with `Admit` reading the immutable block size
before it enters the Controller lock. The final implementation updates only the
rebindable `KVCapacityTokens` field. It therefore preserves the allocation-free
request-work build outside the lock and removes the race without widening the
hot-path critical section.

Final pre-commit executable patch SHA-256:

```text
a5452aba8b26715e553e5efccffd6cec4a11bd14a6160d46daecc4d71448b5ba
```

Remote r4 gates all passed:

```text
go vet ./...                                             passed
focused Controller/Observer/runtime/metrics/version      passed
go test ./... -count=1                                  passed
go test -race ./internal/admission ./internal/app/server passed
go build ./cmd/phala-inference-guard                     passed
git diff --check                                         passed

controller-focused.log  1d0071d86a17a8eae8128613f8112b6d9cba1f0faa0ab3cbaf5bd24b93973f25
server-focused.log      fbc3ef44bb36b96760e76a165297e9fcc2a8bf8931511600507106916a6273d5
full.log                ac3ba197c0f660cdf64c4fdeb5a63a15db35fc317769dd69c910486710a4ab47
race.log                f69baf850aec542315dfcedfaa603c73eeb65ba3b4c0abab18e3d6871fe13947
vet.log                 39104f880de0ffdd58aa15a00d98079e713b227957166da382d33a1a457ae61e
```

Three required review passes:

1. Model and causality: passed. The change is triggered only by an explicit
   backend process identity transition and affects availability before
   forwarding. Feedback, cache, TPS, and request estimation do not authorize
   rebind.
2. Safety and lifecycle: passed after the r3 race correction. Pending state is
   availability-protected; unsafe and same-runtime drift close; old handles are
   epoch-fenced; reservations, Prefill, cache, TPS, and exposure state reset in
   the accepted transaction; concurrent admission, terminal, policy update,
   and rebind pass race testing.
3. Evidence and release: passed for source candidacy. Red evidence is tied to
   pushed commit `957652d`; green r4 is tied to the exact executable patch above;
   documentation changes after r4 do not alter Dockerfile build inputs under
   `cmd/` or `internal/`. An exact committed-source gate still precedes image
   construction. No image or production readiness is claimed here.
