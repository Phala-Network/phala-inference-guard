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
