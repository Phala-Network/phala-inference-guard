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
Implementation and remote source gates passed; image release pending.

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
