# PIG Internal Component Algorithm Flow

This document summarizes the current PIG internal component algorithm in flowchart
form. It focuses on the runtime path from request arrival to backend forwarding,
and the background dynamic QoS loop that learns and publishes the effective
global intake limit.

## Component Topology

```mermaid
flowchart LR
    Client["OpenAI-compatible client"] --> Server["proxyServer ServeHTTP"]

    Server --> Health{"Path is /healthz?"}
    Health -->|yes| HealthOK["Return ok"]
    Health -->|no| Metrics{"Path is /pig/metrics or /v1/metrics?"}

    Metrics -->|yes| MetricsAuth{"Bearer token valid?"}
    MetricsAuth -->|no| Metrics401["Return 401"]
    MetricsAuth -->|yes| MetricsWriter["Write protected runtime, dynamic, backend, classifier, lane metrics"]

    Metrics -->|no| Attestation{"Path is /v1/attestation/report?"}
    Attestation -->|yes| AttestationAuth{"Bearer token valid?"}
    AttestationAuth -->|no| Attestation401["Return 401"]
    AttestationAuth -->|yes| AttestationReport["Generate attestation report"]

    Attestation -->|no| Admitted{"Path admitted by PIG_PATHS?"}
    Admitted -->|no| DirectProxy["Choose backend and proxy without QoS gate"]
    Admitted -->|yes| RequestClassifier["Classify request lane and output-token hints"]

    RequestClassifier --> Gate["QoS gate WaitAcquire"]
    Gate -->|accepted| BackendSelect["Choose routable backend"]
    Gate -->|rejected| Reject429["Return OpenAI-shaped 429"]

    BackendSelect --> ActiveTracker["Track prefill-protected active request"]
    ActiveTracker --> Streaming{"Streaming response?"}
    Streaming -->|yes| StreamProxy["SSE proxy, semantic TTFT observation, optional keepalive or early bridge"]
    Streaming -->|no| HTTPProxy["HTTP reverse proxy"]

    StreamProxy --> ObserveResult["Observe proxy result, latency, lane counters"]
    HTTPProxy --> ObserveResult
    DirectProxy --> ObserveResult

    subgraph Background["Background dynamic QoS controller"]
        PollLoop["pollLoop"]
        MetricsAdapter["Metrics adapter and per-backend or per-URL normalization"]
        CleanPipeline["Clean dynamic QoS pipeline"]
        Snapshot["Publish dynamic snapshot"]
        Notify["Notify queued waiters"]
        PollLoop --> MetricsAdapter --> CleanPipeline --> Snapshot --> Notify
    end

    Snapshot -. "current global limit" .-> Gate
    Snapshot -. "effective queue wait inputs" .-> Gate
    ActiveTracker -. "prefill protected count" .-> CleanPipeline
    StreamProxy -. "semantic TTFT histogram" .-> CleanPipeline
```

## Request Admission Flow

```mermaid
flowchart TD
    Start["Request arrives"] --> HealthMetrics{"Health, metrics, or attestation path?"}
    HealthMetrics -->|health| ReturnHealth["Return /healthz ok"]
    HealthMetrics -->|metrics| MetricsAuth{"Authorization matches TOKEN?"}
    MetricsAuth -->|no| Return401["Return 401"]
    MetricsAuth -->|yes| ReturnMetrics["Return /pig/metrics or /v1/metrics"]

    HealthMetrics -->|attestation| AttestationAuth{"Authorization matches TOKEN?"}
    AttestationAuth -->|no| Return401
    AttestationAuth -->|yes| ReturnAttestation["Return /v1/attestation/report"]

    HealthMetrics -->|model path| InScope{"Path in PIG_PATHS?"}
    InScope -->|no| ChooseDirect["Choose backend"]
    ChooseDirect --> DirectAvailable{"Backend available?"}
    DirectAvailable -->|no| BackendUnavailable["Return OpenAI-shaped 429"]
    DirectAvailable -->|yes| DirectForward["Forward request without QoS gate"]

    InScope -->|yes| Classify["Classify lane, tier, body/output size"]
    Classify --> CurrentLimit["Read currentQoSLimit"]
    CurrentLimit --> DynamicEnabled{"Dynamic enforce enabled?"}

    DynamicEnabled -->|no| StaticLimit["Use GLOBAL_LIMIT"]
    DynamicEnabled -->|yes| SnapshotLimit{"dynamic global limit > 0?"}
    SnapshotLimit -->|no and backend unavailable| BackendUnavailable
    SnapshotLimit -->|no| DynamicReject["Reject code global_dynamic_limit"]
    SnapshotLimit -->|yes| DynamicLimit["Use dynamic global limit"]

    StaticLimit --> TryAcquire["Try lane, tier, global acquire"]
    DynamicLimit --> TryAcquire
    DynamicReject --> ShortWait{"Short wait allowed?"}
    TryAcquire -->|slot available| Accepted["Accepted"]
    TryAcquire -->|no slot| ShortWait

    ShortWait -->|capacity recovers before timeout| Accepted
    ShortWait -->|timeout or no wait| Reject["Observe reject and return OpenAI-shaped 429"]

    Accepted --> Prefill["Track active request for prefill grace"]
    Prefill --> Headers["Set X-PIG-Lane, X-PIG-Tier, optional output-token hint"]
    Headers --> SelectBackend["Choose backend"]
    SelectBackend --> BackendOK{"Backend available?"}
    BackendOK -->|no| BackendUnavailable
    BackendOK -->|yes| StreamDecision{"Streaming requested?"}
    StreamDecision -->|yes| StreamingProxy["Proxy SSE and record semantic TTFT"]
    StreamDecision -->|no| PlainProxy["Proxy HTTP response"]
    StreamingProxy --> Complete["Observe latency, result status, counters"]
    PlainProxy --> Complete
    DirectForward --> Complete
```

## Dynamic QoS Learning Flow

```mermaid
flowchart TD
    Tick["Poll interval tick"] --> RoutingMode{"Backend routing enabled?"}

    RoutingMode -->|yes| BackendPoll["Poll each configured backend metrics URL"]
    BackendPoll --> BackendNormalize["Per backend: parse metrics, compute counter delta, compute direct generation TPS, update backend runtime status"]

    RoutingMode -->|no| URLPoll["Poll DYNAMIC_METRICS_URLS"]
    URLPoll --> URLNormalize["Per URL: parse metrics, compute counter delta, compute direct generation TPS, isolate reset or fetch failure"]

    BackendNormalize --> AnyHealthy{"Any healthy metrics sample?"}
    URLNormalize --> AnyHealthy
    AnyHealthy -->|no| StoreError["Store error snapshot: availability limit 0, backend unavailable"]
    StoreError --> NotifyWaiters["Notify QoS waiters"]

    AnyHealthy -->|yes| Aggregate["Aggregate healthy samples: running sum, waiting sum, max KV, preemption delta sum, generation TPS sum, TTFT histogram"]
    Aggregate --> PIGObs["Read local PIG observations: queue, dynamic rejects, semantic TTFT, prefill-protected active requests"]
    PIGObs --> Signals["Derive clean signals"]

    Signals --> CapacityEstimator["Capacity estimator: smooth generation TPS, raw cap, safe cap, low-confidence bound, representative load"]
    Signals --> TTFTObserve["TTFT observation: semantic streaming TTFT first, backend histogram fallback"]
    Signals --> StateSignals["Signal state: green, yellow, red from waiting, KV, preemptions, TPS, TTFT"]

    StateSignals --> StateLimit["Choose state limit"]
    TTFTObserve --> TTFTLearn["TTFT learner: learned, target, limit, reason"]
    CapacityEstimator --> ThroughputLearn["Throughput learner: learned, target, projected, reason"]
    ThroughputLearn --> ThroughputLimit["Apply throughput limit"]

    StateLimit --> QOSBase["Base QoS limit from state and immediate QoS TPS"]
    TTFTLearn --> MinStages["Candidate cap components"]
    ThroughputLimit --> MinStages
    QOSBase --> PressureGuard["Pressure guard: waiting, KV, preemption, pressure learned cap"]
    PressureGuard --> PrefillGuard["Prefill guard: protect prefill transition and observed cap"]
    PrefillGuard --> IntakeGuard["Intake guard: backend waiting or backend unavailable overrides"]
    IntakeGuard --> Enforcer["Final enforcer min(hard, state, throughput, TTFT, pressure, prefill, availability)"]
    MinStages --> Enforcer
    Enforcer --> Publish["Publish snapshot and metrics"]
    Publish --> NotifyWaiters
```

## Final Limit Composition

```mermaid
flowchart LR
    Hard["hard_global_limit"] --> Min["decision.EnforceFinalLimit"]
    State["state_limit"] --> Min
    Throughput["throughput_limit"] --> Min
    TTFT["ttft_limit"] --> Min
    Pressure["pressure_limit"] --> Min
    Prefill["prefill_limit"] --> Min
    Availability["availability_limit"] --> Min

    Waiting{"backend waiting > 0?"} -->|yes| OverrideWaiting["Override reason backend_waiting, current intake limit 0"]
    Unavailable{"all backends unavailable?"} -->|yes| OverrideUnavailable["Override reason backend_unavailable, availability limit 0"]

    OverrideWaiting --> Min
    OverrideUnavailable --> Min

    Min --> Final["pig_dynamic_global_limit"]
    Final --> Gate["QoS gate for new admitted-path requests"]
```

The final limit only controls new intake. PIG does not cancel requests that have
already been forwarded to the backend. When backend waiting is present, PIG
closes the current intake path, but the throughput learner does not overwrite
the long-term learned capacity with zero.

## Component Responsibilities

| Component | Main responsibility | Key files |
| --- | --- | --- |
| HTTP proxy server | Route health, metrics, admitted model paths, and direct proxy paths. | `internal/app/server/proxy.go`, `internal/app/server/server_setup.go` |
| Request classifier | Choose lane from body size and output-token hints. | `internal/app/request`, `internal/domain/output` |
| QoS gate | Enforce current global/tier/lane limit, short wait, and 429 rejection. | `internal/app/gate`, `internal/app/server/qos.go` |
| Dynamic controller | Poll metrics, run the clean pipeline, publish snapshots, notify waiters. | `internal/app/dynamic` |
| Metrics adapter | Normalize vLLM/SGLang metrics per backend or per static URL before aggregation. | `internal/infra/prometheus`, `internal/runtime/backend`, `internal/app/dynamic/metrics_adapter.go` |
| Telemetry aggregation | Merge backend samples and TTFT histograms with empty and single-backend fast paths. | `internal/runtime/telemetry/sample.go`, `internal/runtime/telemetry/histogram.go` |
| Clean pipeline orchestrator | Wire signal derivation, stage learners, cap application, enforcer, and final snapshot projection. | `internal/domain/dynamic/clean_pipeline.go`, `internal/domain/dynamic/clean_limit_stage.go`, `internal/domain/dynamic/snapshot_mapper.go` |
| Signal derivation | Compute generation TPS, decode running, single-user TPS, representative load, counter deltas, and prefill state. | `internal/domain/dynamic/clean_signals.go` |
| Capacity estimator | Produce smoothed TPS, raw cap, safe cap, confidence, and low-confidence bound. | `internal/domain/capacity/estimator.go` |
| Throughput learner | Learn the throughput cap upward slowly and downward quickly when evidence is representative. | `internal/domain/dynamic/clean_throughput_stage.go`, `internal/domain/capacity/clean_learning.go` |
| TTFT learner | Learn a TTFT cap from semantic TTFT or backend TTFT histograms. | `internal/domain/dynamic/clean_ttft_stage.go`, `internal/domain/latency/learning.go` |
| Pressure guard | Apply immediate protection for waiting, KV pressure, and preemptions. | `internal/domain/dynamic/clean_pressure_stage.go`, `internal/domain/capacity/pressure.go` |
| Prefill guard | Separate running from decode-running during prefill and avoid false capacity drops. | `internal/domain/dynamic/clean_prefill_stage.go`, `internal/domain/capacity/prefill.go`, `internal/runtime/prefill` |
| QoS base and cap application | Apply immediate QoS/TTFT, throughput, pressure, and prefill cap components while accumulating reasons. | `internal/domain/dynamic/clean_qos_stage.go`, `internal/domain/dynamic/clean_cap_application.go` |
| Enforcer | Compose the fixed ordered cap component list and expose the winning reason. | `internal/domain/dynamic/clean_final_enforcer.go`, `internal/domain/dynamic/clean_intake_guard.go`, `internal/domain/decision` |
| Observability | Expose protected metrics and compact status logs for every cap component. | `internal/observability/metrics`, `internal/observability/status` |

## Key Invariants

- Backend metrics are normalized before global aggregation, so one backend
  counter reset does not poison the full QoS window.
- Aggregation keeps the common single-backend poll cheap while preserving
  deterministic TTFT bucket ordering when sorting is needed.
- `generation_tps / decode_running` is the primary user-visible throughput
  signal, not total token throughput.
- `decode_running = running - prefill_protected`, so long prefill does not look
  like bad decode throughput.
- Counter deltas and prefill state are derived in small helpers before learner
  stages consume them.
- Upward learning requires representative healthy load and consecutive healthy
  windows.
- Downward protection can react quickly to low QoS, high TTFT, backend waiting,
  KV pressure, preemption, or backend unavailability.
- `waiting > 0` closes current new intake but does not force the long-term
  throughput learned capacity to zero.
- Client-facing rejects remain OpenAI-compatible `429`; internal reasons
  are exposed through protected metrics and status logs.
- Final cap composition is an ordered `min()` over hard global, state, TTFT,
  throughput, pressure, prefill, and availability components.
