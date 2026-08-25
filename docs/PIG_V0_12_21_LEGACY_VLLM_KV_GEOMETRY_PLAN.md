# PIG v0.12.21 Legacy vLLM KV Geometry Plan

Status: release complete; exact source, builder gates, registry image, affected
production replacement, and requested fleet audit accepted

## Incident and scope

Fleet rollout of v0.12.20 found one unsupported backend: vLLM 0.10.2 reports
`block_size` and `num_gpu_blocks` in `vllm:cache_config_info`, but not the newer
group-aware `kv_cache_size_tokens`. PIG therefore failed closed at startup. The
affected Qwen2.5 service was first restored with its previous PIG while HAProxy
global authentication and the co-located GPT-OSS v0.12.20 service remained in
place. The guarded compatibility fix was then tested, published, and deployed
to both PIG services on that mixed CVM.

This release changes only the vLLM metric adapter and release identity. It does
not change admission policy, Router, backend configuration, or request routing.

## Contract

1. A positive explicit `kv_cache_size_tokens` or `kv_cache_size` remains the
   authoritative capacity and always wins over block-count geometry.
2. When explicit capacity is absent, PIG may derive `num_gpu_blocks *
   block_size` only if all values are unique, positive exact integers without
   overflow and cache metadata explicitly identifies a conventional
   full-attention, non-Mamba, non-sliding-window runtime.
3. Missing or conflicting guards, attention-free, Mamba/hybrid, sliding-window,
   ambiguous, non-finite, fractional, zero, or overflowing geometry fails
   closed.
4. Existing vLLM/SGLang parsing, strict public routes, reservations, policy,
   and management endpoints remain byte-for-byte behaviorally unchanged.

## Execution gates

1. Prove the focused legacy fixture red on the existing clean Linux builder.
2. Implement the smallest adapter fallback and run focused parser/startup tests.
3. Run format, vet, all-package, race, binary, and image identity/smoke gates on
   the exact pushed commit; publish only the accepted image.
4. Rebuild only the affected mixed Compose candidate: GPT-OSS and Qwen2.5 use
   the accepted v0.12.21 digest, while HAProxy retains the renamed all-auth
   config/source. Deploy through the non-dev control plane once.
5. Require both domains to return authenticated models/health/metrics 200,
   unauthenticated 401, blocked `/generate` 404, expected PIG identity, healthy
   backends, and no startup loop/OOM/fatal markers.

Rollback is the currently verified mixed Compose hash
`c7e724fc9c2d8b626e6ee184a510ba8e2317ab095172f250a8b1ce0f7b7b9777`.

## Evidence ledger

- Live vLLM 0.10.2 exposed `block_size=16`, `num_gpu_blocks=27463`, and no
  explicit token-capacity label. v0.12.20 repeatedly exited with
  `predictive startup KV capacity or block size is invalid`.
- Red archive SHA-256: `8aeba6adef9d85cd18252c993ab2dd0b93b0eb3e75ab1f0da8127330f0d6b0df`.
  The focused builder test failed on the intended missing token geometry;
  red log SHA-256: `90e6bf4946c105a130b75c727a25c413c2344637eed9a89a3dd770124026fd51`.
- Green r1 was formatting-only rejection and did not run tests.
- Green r2 archive SHA-256: `fc2d5b756492a2ad874741987b7574ea4a00f00ce74ce740bcb4725125ea8f72`.
  Formatting, focused legacy/group-aware/unsafe tests, and the complete
  Prometheus adapter package passed. Focused log SHA-256:
  `9d3ffcad3762bbf08cb675af4339da0dc46641a8da9ac23716bac4c3635be9a0`.
- Green r3 archive SHA-256: `f3ff55fd54988641a1468ee8b2fe7e28c428c08ed85dcf74b3761dbaa5d68724`.
  It adds exact multiplication-overflow, zero explicit-capacity, and ambiguous
  multi-engine coverage. Formatting, focused tests, the complete Prometheus
  adapter package, and the v0.12.21 release identity passed. Evidence hashes:
  focused `950661a02098f4a4987face33049e9161597b36669add6af71e44544601275d5`,
  package `1daf4fa1be32066239ead414073a5c2b980e89f6ca22b0274d3c293ab77ba3f5`,
  release identity `fcf058fb0c2b6145b0ecf99dbf7f4849a528c14671caedb2726d66c7a4cc1ffc`,
  and empty formatting output
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

### Exact source and builder matrix

- Accepted source commit:
  `be90c2e2e1afcd0c6afc966a5413817599d9680e`; pushed branch:
  `pig-origin/codex/pig-v0.12.21-legacy-vllm-kv`.
- Exact pushed-commit archive SHA-256:
  `33a9a7575822ab9fd50e3977d19698f2641999fa5c88acc37ced1cf63c7844ed`.
- Builder CVM: `4f167f6e-4c50-415f-99f2-94b65652beba`; builder app:
  `ff40ee31b95e89ebb242c223514adc715ac8a301`; pinned Go image:
  `golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac`.
  The Docker daemon exposed one effective CPU.
- Exact-commit formatting, focused adapter tests, release identity, and
  `go vet ./...` passed. Relevant evidence SHA-256 values are focused
  `88381b18a57e4dc1d211648cc69cf643ae0fa8d5384eaf4d9dae4ffb3488df37`,
  formatting
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
  and vet
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- The unchanged 4 MiB estimator wall-clock test was the only aggregate-matrix
  failure. Two exact-commit aggregate runs measured p99 `100.897324 ms` and
  `109.438296 ms` against the frozen `100 ms` gate. The same exact test was
  then run independently five times on the same one-CPU builder; every run
  passed, with p99 values from `31.795419 ms` through `67.483728 ms`. No
  estimator code, workload, or threshold was changed. This is retained as a
  builder scheduling fluctuation, not represented as an unconditional full
  matrix pass.
- All functional tests other than that separately accepted wall-clock test,
  race tests other than the same test, and the binary build passed. Evidence
  SHA-256 values are functional
  `f66e4f1866d1954ddeb0293ae575eb466ff0bd83fe1785221434b235249f31e4`,
  race
  `c602addc2ee1248d73acfb6b7a1a0431b1d8c955abc786786bd4ef3cad6fd3f8`,
  empty build output
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
  and result marker
  `e160c916c5701d297f7d449f3b0080e8357a0726188d315e1addead4d6b55559`.

### Image and registry acceptance

- Published image:
  `ghcr.io/phala-network/phala-inference-guard:0.12.21@sha256:e5bf503db943e57368270f45e69495f48d048a4bde869afeb0b09764c0636ca6`.
  Immutable tag: `0.12.21-be90c2e2e1af`.
- Image ID:
  `sha256:71356fd74587142facbc2a47945814863133f84e156cb9d3051f2a6f1823500e`;
  entrypoint `/phala-inference-guard`; OCI version `0.12.21`; OCI revision
  `be90c2e2e1afcd0c6afc966a5413817599d9680e`.
- Image smoke used the production legacy geometry `block_size=16`,
  `num_gpu_blocks=27463`, `is_attention_free=False`,
  `mamba_page_size_padded=None`, and `sliding_window=None`. PIG started with
  capacity `439408`; authenticated models succeeded, unauthenticated models
  returned 401, blocked `/generate` returned 404, backend admission attempts
  and reservations remained zero, and there was no restart or OOM. Smoke
  summary SHA-256:
  `b1ecd3c9255e6ea71774a6c437f6092803f930ab07197aeaf05286d407c1f499`.
- Registry push log SHA-256 values are immutable
  `3bb365333589c246a8e02a325ace2cf4ab5cc65e790dc981a4bdddd58af6da8e`
  and release
  `1ea5b3391c36c772d20a44ce93f92fd15244b73b5f6b5b3965ab342f99110fda`.
  Anonymous immutable-tag, release-tag, and digest manifests are identical,
  each with SHA-256
  `1d4f433faee2e452fe2e39c046adc48063331e2c07a9c13f2d7aed20c808a5e9`.
  Anonymous digest pull reproduced the accepted image and OCI identity.

### Production replacement

- Target CVM: `73eb2d38-2fdd-4f3d-ae0e-850c17c3568b`
  (`gpt-oss-20b-qwen-2-5-7b-use1`). Verified rollback Compose SHA-256:
  `c7e724fc9c2d8b626e6ee184a510ba8e2317ab095172f250a8b1ce0f7b7b9777`;
  accepted Compose SHA-256:
  `2aa2dcd6cdaeae5d313238e64409f2b9ca6e04e3d82a8135e02a8ebd5306cb5b`.
- The candidate changed only the two PIG image references and the legacy
  Qwen PIG environment. Qwen removed obsolete `MODEL_NAME` and `BACKENDS`,
  added `UPSTREAM=http://vllm-qwen7:8000` and
  `PREDICTIVE_TPS_REFERENCE=25`, and retained the already accepted HAProxy
  `haproxy_cfg_authall_v01220` config/source and both global-auth frontends.
  Builder `docker compose config --quiet` passed.
- Two CLI attempts that included `-e .env` failed before control-plane
  submission because CLI v1.1.20 looked for a top-level
  `encrypted_env_pubkey` while the current schema reports the key under
  `kms_info`. The existing CVM did not require an environment replacement.
  One Compose-only submission without `-e` was accepted. Its 300-second client
  wait expired, but a read-only re-query proved `status=running`,
  `in_progress=false`, and the exact accepted Compose hash. No duplicate
  deployment was submitted.
- Both PIGs are running v0.12.21. GPT-OSS/SGLang reports KV capacity `679857`;
  Qwen2.5/vLLM reports guarded legacy capacity `439408`; backend metric failure
  is zero on both. Authenticated models, health, and metrics returned 200;
  unauthenticated health returned 401; blocked `/generate` returned 404.
  All seven service containers are running, both backends are healthy, and the
  retained log audit found no OOM, fatal, panic, or restart-loop marker.

### Requested fleet completion audit

The requested fleet contains 12 CVMs and 14 running PIG containers. The strict
public-route and HAProxy global-auth change is v0.12.20 behavior. v0.12.21 is a
narrow compatibility point release for the one legacy vLLM metrics shape, so
the other healthy nodes were not restarted merely to make version labels
uniform. No target uses PIG-to-vllm-proxy: Gemma4 and the legacy Qwen2.5 service
use direct PIG-to-vLLM; Muse, Qwen3.8, DeepSeek, and GPT-OSS use direct
PIG-to-SGLang.

| CVM | Workload | Accepted PIG | Compose/live SHA-256 | Result |
| --- | --- | --- | --- | --- |
| `bf47b91b-77f9-44ab-a081-284268e205f7` | Gemma4 `use1-4c` | v0.12.20 | `29c6d209d02179ebce41493a312cc3a94afb2443d9eb778881d8cfc6cf7a504c` | running, strict routes/auth passed |
| `e6fe8bda-9fe9-4547-a469-e487d39694c3` | Muse Glimmer | v0.12.20 | `80b080c2817c804b6b8f6ae115adea0001ac18d2705584609e4cefc3c132c0be` | running, strict routes/auth passed |
| `73eb2d38-2fdd-4f3d-ae0e-850c17c3568b` | GPT-OSS + Qwen2.5 | v0.12.21 x2 | `2aa2dcd6cdaeae5d313238e64409f2b9ca6e04e3d82a8135e02a8ebd5306cb5b` | running, both backends ready |
| `210665da-6868-469d-a729-c342b8dc59e4` | Gemma4 `use2-19` | v0.12.20 | `cffd2df4f149a2ce8a407ec65f970e1c5ee8fb70092b5ee8387dc66010049c45` | running, strict routes/auth passed |
| `5d961f5e-0b3a-4419-a9c0-a3df600ad4ca` | Gemma4 `use2-3b` | v0.12.20 | `891c489d04783a57f1a707b3a0ff02db30051c6c4ea44d122a489b9f1aaee650` | running, strict routes/auth passed |
| `19696a78-17a8-4d85-8899-4eccd24adf93` | Gemma4 `use2-4c` | v0.12.20 | `089ab468af372fc3e8c2846194f816f7dc645451b61b5496749d3c618ab6a648` | running, strict routes/auth passed |
| `9949143b-4c06-4b81-8c24-f96a8b1593eb` | Gemma4 `use2-5d` | v0.12.20 | `a1e3f958eac0e34a71f5e215a71765d1e7e3bc798c0308978b8285ad334735cd` | running, strict routes/auth passed |
| `3e4d7151-d56b-4e26-8403-42717d1f7367` | Qwen3.8 `use2-9b` | v0.12.20 | `71da9429615d9dc0ef0a9221fe066ff38fe3e352d64ef9f18969cd43adfb3867` | running, strict routes/auth passed |
| `5c2c59ea-3bf3-4a2f-8ae8-99a10cc21037` | Qwen3.8 `use2-bb` | v0.12.20 | `455c6e5979b52bf3c2ecbd034544db22f781270bb97ccc0de9dcc11463c6b0ec` | running, strict routes/auth passed |
| `a1212298-f34d-4688-be54-162d84fef662` | Qwen3.8 `use2-cb` | v0.12.20 | `ad15553cf1183546bb057fc13b6de7ee9dafaeb7438fc2f61d9abd2302a0c528` | running, strict routes/auth passed |
| `97f35bc8-f077-478c-8fdd-b6ae9412b751` | Qwen3.8 `use2-db` | v0.12.20 | `96e49cfc8df28a83baeb8a90e1aa759a53063835f58f74314ad42a9441354b5e` | running, strict routes/auth passed |
| `19a2d062-af63-49eb-807d-84ddfbbc905a` | DeepSeek `usc2-a/b` | v0.12.20 x2 | live host `5969632bfb0f1286c9da98bbf2394a19598b6d774229367ff7834a32e983da2e` | SSH-only update, both replicas running |

Every HAProxy frontend now applies
`http-request deny deny_status 401 unless is_authorized`; no target retains a
selective `if is_* !is_authorized` rule. Each inline HAProxy config was renamed
and its service `source` changed to the same name; the DeepSeek live guest uses
`haproxy_cfg_auth_all_20260825`. Obsolete PIG `MODEL_NAME`/`BACKENDS` variables
are absent from the updated PIG containers; backend model variables are not PIG
configuration and were retained.

The final read-only container audit found every PIG running and no unexpected
service exit. Expected completed model-downloader containers and the retained
two-week-old DeepSeek image-regression container are not service failures. The
final Router enabled sets are exactly:

- Gemma4: `use1-4c,use2-19,use2-3b,use2-4c,use2-5d`;
- Qwen3.8: `use2-9b,use2-bb,use2-cb,use2-db`;
- DeepSeek: `usc2-a,usc2-b`.

All enabled routes report PIG metrics `ok=true` and `stale=false`. Gemma4
`use1-4c` was temporarily non-selectable because PIG reported the intentional
`request_aware_protected` backpressure state; it remained enabled and was not
misclassified as unhealthy. The final audit did not submit a generation
request, change Router state, restart a container, or redeploy an already
healthy CVM.

## Review record

1. Capacity authority and failure behavior: the existing explicit capacity
   path remains first. A missing explicit label alone permits the guarded
   fallback; invalid or zero explicit capacity cannot silently fall back.
2. Legacy safety: every block count and size must be a unique positive exact
   integer; conflicting engines, missing/conflicting guards, non-finite or
   fractional values, hybrid cache metadata, and multiplication overflow all
   fail closed.
3. Scope and evidence: executable behavior changes only in the vLLM metric
   adapter. No admission, reservation, route, authentication, management,
   lifecycle, Router, backend, or HAProxy behavior changed. Exact pushed-source
   gates, image publication, anonymous registry verification, affected
   production replacement, Router restoration, and final requested fleet audit
   are complete. The one-CPU estimator aggregate timing fluctuation is retained
   explicitly and does not get rewritten as a clean aggregate-matrix pass.
