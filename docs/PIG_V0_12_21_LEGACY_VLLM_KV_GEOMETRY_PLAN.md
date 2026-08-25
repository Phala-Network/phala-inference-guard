# PIG v0.12.21 Legacy vLLM KV Geometry Plan

Status: source implemented and focused builder green; exact-source full matrix, image, and final production replacement pending

## Incident and scope

Fleet rollout of v0.12.20 found one unsupported backend: vLLM 0.10.2 reports
`block_size` and `num_gpu_blocks` in `vllm:cache_config_info`, but not the newer
group-aware `kv_cache_size_tokens`. PIG therefore failed closed at startup. The
affected Qwen2.5 service was restored with its previous PIG while HAProxy global
authentication and the co-located GPT-OSS v0.12.20 service remained in place.

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
   full matrix, image publication, and production replacement remain pending.
