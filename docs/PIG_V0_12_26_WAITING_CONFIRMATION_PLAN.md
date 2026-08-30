# PIG v0.12.26 Waiting Confirmation Plan

Status: source and registry release complete on 2026-08-30. Deployment is
explicitly out of scope and was not performed.

Baseline is the clean, pushed v0.12.25 release branch at
`d32726887ce467aabcd95f012c726326736e70b9`. The production observation on
2026-08-30 showed that one 500 ms SGLang waiting sample immediately closed
Router capacity even while the 60 second mean-active TPS remained above its
reference. The smallest observed case was raw `running=1`, raw `waiting=1`,
mean-active TPS `122.87`, reference `50`, and effective Router capacity `1/1`.

## Objective

Keep waiting protection simple while removing single-sample red-state and 429
flapping:

1. the first nonzero waiting observation below `window_concurrency` remains
   open;
2. a second adjacent fresh nonzero waiting observation protects;
3. waiting at or above `window_concurrency` protects immediately;
4. the first zero-waiting observation clears waiting protection immediately,
   while independent guards remain authoritative;
5. `window_concurrency`, running-limit, TPS-window, preemption, lifecycle, and
   five-line Router metric semantics otherwise remain unchanged.

The first-sample grace is bounded by the existing atomic
`window_concurrency` reservation gate. This change adds no learner, queue,
cooldown, timer, goroutine, debt ledger, production knob, or model-specific
threshold. Admission remains O(1).

## Implementation Contract

- Preserve the previous raw waiting value from the immediately preceding
  accepted backend observation in the immutable projected state, but require
  the current observation interval to remain valid before it can confirm
  waiting.
- Reset previous waiting to zero across controller initialization and backend
  runtime reset.
- Confirm waiting when current and previous raw waiting are both nonzero.
- Escalate a first sample immediately when current raw waiting is at least the
  current `window_concurrency` bound.
- Do not use an historical request rejection or a hold lease to decide current
  waiting protection.
- Keep the existing OpenAI-shaped 429, structured reason, full diagnostics, and
  fixed five-line `/pig/metrics` contract.

## Verification And Release

1. Prove the existing implementation red on the remote Linux builder: first
   waiting observation must be admitted, while confirmed and window-sized
   waiting must protect.
2. Implement the projected-state and gate correction with focused unit,
   controller, policy, metrics, simulation, and race coverage.
3. Push every accepted source commit. On the approved remote builder run the
   legacy audit, formatting check, focused tests, full tests, race, vet, build,
   deterministic simulations, benchmarks, and production image contract.
4. Assign version `0.12.26` only after behavior is green, then repeat the exact
   complete builder matrix on the versioned commit.
5. Publish immutable source and version tags only after the complete builder
   matrix passes. Verify both registry tags resolve to one digest and verify
   OCI version/revision labels and runtime `pig_info` from a fresh pull.
6. Do not deploy, restart, reconfigure, or otherwise mutate any CVM, Router,
   backend, or running PIG instance in this task.

## Red And Focused Green Evidence

The behavior contract was first pushed at
`a6ba67a24c6a259281121b9760f714da5fb76c08` and reproduced on the independent
remote builder in
`/var/volatile/dstack/persistent/.cache/pig-v01226-waiting/red-a6ba67a-r1`.
The existing implementation failed for the intended reason:

```text
first waiting observation was not treated as transient
Action:protect
Reason:tps_reference
TPSDecisionSubreason:waiting
```

The red log SHA-256 is
`812ec6a960b76c3801caa0bf034dc661ca409e5021aa9d4eada2c6ace5e2d8e6`.
The red source archive SHA-256 is
`464714a76eb1097c3cea0e63ff2d19f75c98b710388056a88ffacc4032feb89e`.

The focused behavior candidate
`3d4ab661a35ff6bef121b2b41cd8db9105c72fec` passed formatting and all tests in
`internal/admission`, `internal/app/server`, and
`internal/simulation/tpscontrol`. Evidence is in
`focused-3d4ab66-r3`; the focused log SHA-256 is
`a82a99ed1bd53e54cb73bdf54c1799383f05d6f042eebd5c8ffe2f2e439d98d9`
and its source archive SHA-256 is
`d14c83917acd8b732e15cd2fad57e871e775854c66a395bff498a4c14ecb049a`.

## Three Review Passes

1. Model and causality: current raw waiting is compared only with the previous
   accepted raw waiting observation. Confirmation additionally requires a
   valid adjacent observation interval. A first sample at or above the current
   atomic window bound still protects immediately. First zero waiting clears
   only waiting protection; TPS, preemption, running, availability, and window
   guards remain authoritative.
2. Safety and lifecycle: publication, projection, admission, and reservation
   remain under the existing controller mutex. Invalid samples do not mutate
   state, backend runtime reset clears prior waiting, a long or invalid sample
   interval cannot confirm waiting, and Snapshot/Admit see one coherent state.
   The change adds no lock, goroutine, timer, queue, debt, learner, allocation,
   model branch, or unbounded state.
3. Evidence and release: the red failure and green behavior are tied to exact
   pushed commits. The versioned commit was then tested from a fresh clone with
   the fixed Go image, including race, deterministic simulation, benchmarks,
   and the production image contract. Registry publication happened only after
   those gates passed. No production action was used as a test shortcut.

## Complete Builder Matrix

The exact versioned executable source
`53876a89c3955de9023ab04e066b7d825f6b97b3` passed the complete matrix in:

```text
/var/volatile/dstack/persistent/.cache/pig-v01226-waiting/full-53876a8-r1
```

The builder was Linux amd64 with Docker `25.0.3` and fixed Go image
`golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
Go reported `go1.24.13 linux/amd64`. The source archive SHA-256 was
`afa36f818406383675333e4ff0271ac63402bb4fbd0cadb56c8064613567006c`.
Every command exited zero:

- legacy-mode ownership audit;
- gofmt zero-diff check;
- focused admission/server/simulation tests;
- `go test ./... -count=1`;
- `go test -race ./... -count=1`;
- `go vet ./...` and `go build ./...`;
- two byte-identical TPS-controller simulations;
- three admission benchmark iterations;
- production image build and contract validation.

Material SHA-256 values are:

```text
builder-environment.txt  9c3d59f47106b7d3641eaedcccf6bbe009900037b56ef579d79eca6c04949b84
source-environment.txt   8894cfb8f657674b44c0b0958da93aa2595a72594b8571c52b329b422da37f02
legacy.log               455cf163ebdc8cd358ea90370bf09603ddeec7deb7a64d3c3018975046aba5c0
format.log               e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
focused.log              68d9841973f909a1953e09b45b520164825ead11d0928ca8ea878fd0189e93fd
full.log                 dbbac93aa562d8544d85cba410c515e6469eaf22e394e2a522feed42d33fd53d
race.log                 76ca55a0e326616195cb6620d3098d5e09acc8c84f3985bc1be00fa55414393c
vet.log                  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
build.log                e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
simulation-1.json        25878d06c4f9b241a3e10773813106bd76c0366907b3344adc74a8d2f76ae7a4
benchmark.log            a7b45fcd3848f7ce34044067a85bf03e974cca217c49fcf974d604eac976ea9f
image-contract.log       908c3b38a3a95d8eeb640070b195f758d16a7a3e8ca55e2446cd425fb967a870
image-inspect.txt        9b2de003a57c91932685da28a1789634a2993826b2622efde846889f4dd4e155
```

All measured admission benchmarks remained allocation-free. Snapshot measured
`552.2--635.3 ns/op`, protected admission `901.5--1158 ns/op`, admit-and-cancel
`560.4--588.3 ns/op`, and publication with 4096 reservations
`468498--508272 ns/op`. These are builder microbenchmarks, not production
throughput claims.

## Source Tag And Registry Image

Annotated tag `v0.12.26` points to the exact tested executable commit
`53876a89c3955de9023ab04e066b7d825f6b97b3`. GitHub Actions run
`33299968531` completed successfully. It published both immutable references:

```text
ghcr.io/phala-network/phala-inference-guard:v0.12.26
ghcr.io/phala-network/phala-inference-guard:v0.12.26-53876a89c395
```

A fresh pull of both references in `registry-53876a8-r4` proved that they have
the same manifest digest and image ID:

```text
manifest digest  sha256:9cdeed169d6c82d7c4a4c0873cce5b5c7a37ec49cacd19941125cf3eda4641db
image ID         sha256:c4a361d85b3ef1106f3f9cf3f3d3706aaffd3d1caecf0e73aee395a886ce65d4
OCI version      0.12.26
OCI revision     53876a89c3955de9023ab04e066b7d825f6b97b3
```

The two inspect records have identical SHA-256
`929d468534f99df490a0069b3040ca2f4d3ec90cb5565a2fa08a11d4dfe71479`.
The release-identity and combined-metrics tests prove
`pig_info{version="PIG-v0.12.26"} 1` in the exact tested source. The builder has
only `runc`/`sysbox-runc` and no NVIDIA runtime or NVML device, so starting the
production image there is not a valid runtime smoke test. No CVM was used to
work around that limitation because deployment and runtime mutation are out of
scope. Runtime readiness is therefore intentionally unclaimed.

## Final Scope Audit

- PIG source branch, exact release tag, and both GHCR tags are pushed.
- Source behavior, concurrency safety, race behavior, deterministic simulation,
  hot-path allocation, OCI labels, native-NVML image contract, and registry
  identity are verified.
- No CVM, Router, Compose file, backend, production configuration, container,
  route, or running PIG instance was deployed, restarted, or modified.
- Live readiness and production effectiveness remain a separate deployment
  layer and are not claimed by this release task.
