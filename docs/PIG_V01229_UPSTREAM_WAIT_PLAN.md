# PIG v0.12.29: wait for upstream readiness

## Contract (2026-09-12)

An unavailable or still-initializing SGLang must not make PIG exit after its
startup metrics timeout. Wait until coherent backend identity/counters arrive,
then initialize admission normally. Apply the same lifecycle to vLLM. Preserve
TPS, waiting-to-429, window, premium, authentication and route policies.

Scope: source, remote Linux tests and standard immutable image publication.
No CVM deployment, restart, Router/Redpill change or production inference.
Worktree: `codex/pig-v0.12.29-upstream-wait`, based on `25c9f546ea36f85f53b0812f35fc883978415962`.
Other dirty/active worktrees must remain untouched.

## Minimal design

- Keep initialization synchronous: no inference intake before a coherent
  metrics sample. The process is alive while waiting; the HTTP listener starts
  only after initialization. This is readiness, not liveness.
- Keep the existing startup timeout as a per-attempt bound, not a lifetime
  deadline. Retry unavailable/invalid upstream responses indefinitely overall.
  Invalid local configuration still fails immediately.
- Use bounded HTTP requests, cancellable waits and owned/closed transports.
  Propagate SIGTERM/SIGINT cancellation through initialization, including
  optional SGLang running-limit discovery. No startup goroutine may outlive it.
- Waiting/recovery logs must be bounded and sanitized, without raw URL, error
  bodies, credentials or model text. Do not add a production configuration knob.
- Preserve runtime observer retry/recovery and identity-drift protection.

## Execution and evidence

1. Reproduce the old timeout failure remotely with failing behavioral tests.
2. Implement bounded-resource, cancellable startup retry. Cover 503, refused
   connection, incomplete/ambiguous metrics, delayed readiness, cancellation,
   invalid config and runtime outage/recovery. No synthetic production traffic.
3. Run focused/full/race/vet/build, policy simulations and representative hot-path
   benchmarks on the approved remote Linux builder, not local Windows.
4. Record three reviews: behavior; lifecycle/safety; evidence/release.
5. Freeze clean source/version and push source. Record source/archive/locks/base/
   tool identities in pre-build inputs. Build twice with fixed source epoch,
   verify runtime reproducibility, image behavior, SBOM and max provenance.
6. Publish immutable version/revision references with mandatory OCI/Phala
   annotations and digest-bound release artifacts. Independently read registry
   tags, subjects, annotations and referrers back. Record exact results below.

## Status

Source inspection complete. Root cause: `newDefaultAdmissionService` propagates
the total startup-probe timeout through `newProxyServer` to `Run`, which exits.
Runtime observer failures already retry; startup needs the missing lifecycle.
Complete: implementation, remote source gates, source push, reproducible image
builds, installed-image lifecycle checks, registry publication and immutable
pull/readback. No deployment was requested or performed in this task.

## Source evidence and three reviews

Builder: `1faf4bfc-864d-4f4e-8271-265c095f1912`, live verified running dev
Linux/amd64, one CPU. Tests used the Dockerfile-pinned Go image, Go 1.24.13.
Evidence root: `/var/volatile/dstack/persistent/pig-v01229-release-20260912`.

- Red archive SHA256: `3a26e035b480bb12aae0aee9fb2b52e058ebf2338f29aaba81bb471a255b1e7e`.
  The original factory returned early on both 503 and incomplete metrics. The
  coherent SGLang fixture passed independently, so the failure was behavioral.
  Red log: `ac2ca6968aaf7637a459b7968dc9731f5043fd607e84f6b42a99813514189e79`.
- Focused green log: `8f7d7fe7c771ca287d88a033ff49c3ae85af32b4afc7fce428369267f75dc1cc`.
  Verified delayed readiness, refused connection for SGLang and vLLM,
  cancellation during fetch/retry/optional discovery, invalid local config,
  runtime stale-to-open recovery and clean startup/HTTP shutdown.
- Full source matrix: `matrix-r2.log`, SHA256
  `9b30625c2fc3d10f9da62d0393b105cfccaed086a529640f9ee085e441405bbb`.
  `gofmt`, `go mod verify`, full tests, full race tests, startup/lifecycle race
  tests repeated 20 times, `go vet`, CGO build and admission benchmarks passed.
  Complete tests include TPS simulations, authentication/routes, streaming,
  Responses, priority, window/waiting and reservation contracts.
- On this one-CPU builder the first default-parallel full run hit existing
  wall-clock-sensitive priority queue tests. That failed `matrix.log` is kept,
  not counted as acceptance. The full matrix above serializes packages/tests
  (`-p=1 -parallel=1`, `GOMAXPROCS=1`); explicitly concurrent/race tests still run
  their concurrent goroutines. No production timeout or admission policy was
  loosened to make tests pass.
- Three benchmark repeats: controller snapshot 380–384 ns/op, protected
  admission 427–429 ns/op, admit/cancel 407–410 ns/op; all zero allocations.
  These are CPU microbenchmarks, not a production throughput improvement claim.

Review 1 — behavior: the only admission-construction change is persistent
readiness retry. No metrics parser, controller, waiting, TPS, premium, request
rewriting, public route or authentication behavior changed. Invalid local
configuration remains fatal; incomplete remote metrics remain not-ready.

Review 2 — lifecycle/safety: all startup requests/waits are context-cancellable;
owned transports close idle connections. Optional SGLang discovery now shares
cancellation and closes its transport. SIGTERM/SIGINT works both before and
after readiness; HTTP shutdown is bounded to five seconds and the status logger
stops with its context. Waiting logs are sanitized and at most once per 30s.
No reservation exists before successful initialization.

Review 3 — source/release boundary: red and green use the production factory;
SGLang and vLLM parsing are not conflated. Remote-only tests are retained with
hashes. Source is assigned v0.12.29. Image release still requires two independent
builds, installed-image lifecycle tests, native SBOM/max provenance, annotations
and registry/referrer verification. No deployment is implied by source success.

## Final release — 2026-09-12 CST

Source commit: `ef0035cb60ef05568c2d2663d3881843ec25d16d`, pushed to
`Phala-Network/phala-inference-guard`, branch
`codex/pig-v0.12.29-upstream-wait`. Source tree:
`33452037402ecd29ef885c7bb4b5b286601c73f4`.
Clean Git archive SHA256:
`9360228d7967e455e06135b19a37d2e8f1daf5eb4d124b4aea17237cb469405c`.
The final documentation-only commit does not change this executable boundary.

Accepted references (both read back to the same index):

```text
ghcr.io/phala-network/phala-inference-guard:v0.12.29
ghcr.io/phala-network/phala-inference-guard:v0.12.29-ef0035cb60ef
ghcr.io/phala-network/phala-inference-guard:v0.12.29@sha256:6c378595b5eb9f63eb0dc966adf3ef4dcb6e60a140458e200f19a059cf8ffcd2
```

- Index: `sha256:6c378595b5eb9f63eb0dc966adf3ef4dcb6e60a140458e200f19a059cf8ffcd2`.
- Linux/amd64 platform: `sha256:13df3b6e1e20890859f707f277ba126369eb47acc521af2d943e27f1b9089f9c`.
- Runtime config: `sha256:e70a4425bb38f3ded450d5e95840d148018a72809e7eab64360d5c1d5feb6024`.
- Pre-build inputs: `sha256:f58dfdc2cee60904f3c893c7567e649ac8b5ee059167f15caba4d2cb45834c2d`;
  OCI input artifact `sha256:35b4b7553d2e08f47d9c241a016be457787d37175a610743b3e18e3269f5ce2f`.
- Post-build manifest content: `sha256:3a508db6ef08b7f870a848c0cdc7612cc7698e250566308dd3d75383e6d9c882`;
  index referrer `sha256:ba62c210520cbd5993319405181d6ed9493889c21db86f7266b96b65cd5e1fb0`.

The builder used BuildKit **v0.32.2** and buildx **v0.36.1**, pinned by immutable
tool image digests in `build-inputs.json`; Go/runtime base digests and `go.sum`
were also frozen. Two independent fresh workers built with `--no-cache`,
`--pull=false`, source epoch `1789156478`, native SBOM and max provenance, and
`rewrite-timestamp=true`. Platform manifest, runtime config and every runtime
layer are byte-identical. SBOM comparisons normalize only generated
`creationInfo.created` and `documentNamespace`; native build-specific
attestations account for the different outer indexes. No identical-index or
production-throughput claim is made.

All required OCI/Phala annotations exist on both index and platform, including
source/version/revision, Dockerfile digest, builder, epoch and pre-build-inputs
link/digest. License metadata is GPL-3.0-only, based on this repository's GPLv3
`LICENSE`; it is not inherited from a generic release template.

Native statements, both bound to the accepted platform:

- SPDX SBOM blob: `sha256:161d343b460aafec7adcb7fae1fd0fd01f01c0a4b4a8e47f423c90a30f2ac50c`.
- SLSA v1 provenance blob: `sha256:a84714713a9a48d0b0c15eb51be22f4eb7ca4dc08450180735b5a3ac4ee264f9`.
- Native attestation manifest: `sha256:b2b8fa1ce882e5f1d924c812521ab97debae7a7bcad047d1ddebef31cdf8f785`.
- SPDX and provenance statements are also discoverable as OCI referrers:
  `sha256:82ce2ce5e4d3231880d1df68f59f416fbc45fff234fc5f20b8f2755456f265b4`
  and `sha256:75e0fbd5c387d84c6ef420aaf9c2b3bbddf1049f6d768654501f55c82de99a1b`.

The raw native statements and referrer statement JSON have different formatting
digests but identical parsed content/subjects. No signature or SLSA level is
claimed. Post-build results are referrers; no image/artifact digest cycle exists.

### Installed-image acceptance

Using the exact built runtime config, without mounting source into PIG:

- Mock SGLang remained unavailable across five 200ms diagnostic timeouts. PIG
  stayed running with the same container/start time, restart policy `no`, no
  inference calls, and no prematurely open listener.
- Coherent metrics enabled `/v1/models` in that same container. Unauthorized
  models/Router metrics returned 401; authorized metrics stayed exactly five
  lines and `/v1/metrics` identified `PIG-v0.12.29`.
- A subsequent runtime metrics outage caused stale protection with a positive
  projected limit; recovery reopened admission without container restart.
- SIGTERM exited normally in 0.072s during startup wait and 0.072s after
  readiness. Invalid local configuration still exited nonzero.
- Native NVML/CGO, distroless runtime, NVIDIA visibility and version/revision
  checks passed via `tools/validate-production-image-contract.sh`.
- Anonymous registry manifest/blob/referrer reads and a fresh immutable Docker
  pull matched the tested config/layers. The production image contract passed
  again against that registry digest.

### Failed attempts retained, not accepted

The first OCI build lacked an explicit image name, so BuildKit emitted empty
attestation subjects; its generic license metadata was also inappropriate.
It was not published as a release. Frozen inputs were superseded by the `r2`
evidence set; named local OCI exports bind the native statements correctly.
An initial ORAS copy used an unsupported generic authentication flag and failed
before mutation. A post-build adapter used ORAS's documented
`--to-registry-config`; frozen build inputs and executable source were unchanged.
The original diagnostic input artifact is retained for audit, not an accepted
image reference. No existing version tag was overwritten and no `v*` Git tag
triggered an independent CI publisher.

### Evidence and boundary audit

Remote release evidence:
`/var/volatile/dstack/persistent/pig-v01229-release-20260912/r2`.
Local copy: `tmp/pig-v01229-release-assets/evidence` in the parent workspace.
Release evidence archive SHA256:
`8e6e5d32e6f36ca4252baade1cbae5a179d49f32520a28bea7ada96697d39f55`.
Source evidence archive SHA256:
`0469dec57350ffc0c34da6c812f7a973b13cbf000ff21360d6522e629e9a8485`.
Final summary SHA256:
`4db5e15ee32be225709c7170239f8dcbbee8ae705386c35e73fbf73edb9b70ca`.

Temporary image-test containers and both temporary BuildKit workers were
removed. Existing builder containers and read-only registry credentials were
preserved. No production Compose, process, CVM, Router or Redpill state changed;
no production inference or GPU benchmark was performed. Production rollout and
real model loading-time validation remain outside this release-only task.
