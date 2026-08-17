#!/bin/sh
set -u

failed=0

fail() {
    printf '%s\n' "legacy-audit: $*" >&2
    failed=1
}

search_active_go() {
    pattern=$1
    if command -v rg >/dev/null 2>&1; then
        rg -n "$pattern" internal cmd --glob '!**/*_test.go'
        return
    fi
    grep -R -n -E --include='*.go' --exclude='*_test.go' "$pattern" internal cmd
}

list_compatibility_files() {
    pattern=$1
    if command -v rg >/dev/null 2>&1; then
        rg -l "$pattern" internal --glob '*.go' --glob '!**/*_test.go'
        return
    fi
    grep -R -l -E --include='*.go' --exclude='*_test.go' "$pattern" internal
}

for path in \
    cmd/pig-kv-sim \
    cmd/pig-predictive-goodput-sim \
    scenarios/kv-admission \
    tools/validate-v0.9.0-builder.sh \
    internal/app/dynamic \
    internal/app/gate \
    internal/domain/capacity \
    internal/domain/decision \
    internal/domain/dynamic \
    internal/domain/lane \
    internal/domain/latency \
    internal/domain/output \
    internal/domain/qos \
    internal/domain/tier \
    internal/runtime/dynamic \
    internal/runtime/kvshadow \
    internal/runtime/prefill \
    internal/runtime/token \
    internal/simulation/goodput \
    internal/simulation/kv \
    internal/simulation/predictive
do
    if [ -e "$path" ]; then
        fail "retired path remains: $path"
    fi
done

for path in \
    internal/app/request/priority_injector.go \
    internal/app/request/priority_injector_test.go \
    internal/app/server/approximate_predictive_adapter.go \
    internal/app/server/approximate_predictive_adapter_test.go \
    internal/app/server/approximate_predictive_http_integration_test.go \
    internal/app/server/kv_shadow.go \
    internal/app/server/kv_shadow_integration_test.go \
    internal/app/server/predictive_completion.go \
    internal/app/server/predictive_completion_benchmark_test.go \
    internal/app/server/predictive_shadow_pending_prefill.go \
    internal/app/server/predictive_shadow_pending_prefill_benchmark_test.go \
    internal/app/server/predictive_shadow_pending_prefill_v01013_test.go \
    internal/app/server/qos.go \
    internal/app/server/sse_policy.go \
    internal/config/pigconfig/config_dynamic.go \
    internal/config/pigconfig/config_dynamic_test.go \
    internal/config/pigconfig/config_kv_admission.go \
    internal/config/pigconfig/config_kv_admission_test.go \
    internal/config/pigconfig/config_priority.go \
    internal/config/pigconfig/config_priority_test.go \
    internal/config/pigconfig/config_sse.go \
    internal/observability/metrics/dynamic.go \
    internal/observability/metrics/dynamic_test.go \
    internal/observability/metrics/kv_shadow.go \
    internal/observability/metrics/lane.go \
    internal/observability/metrics/priority.go \
    internal/runtime/predictive/coordinator_contract.go \
    internal/runtime/predictive/count.go \
    internal/runtime/predictive/count_coordinator.go \
    internal/runtime/predictive/count_cost.go \
    internal/runtime/predictive/input_size_calibrator.go \
    internal/runtime/predictive/request.go \
    internal/runtime/predictive/scheduler.go
do
    if [ -e "$path" ]; then
        fail "retired file remains: $path"
    fi
done

if search_active_go \
    'DYNAMIC_|KV_ADMISSION_|BACKEND_PRIORITY_|OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS|GLOBAL_LIMIT|QOS_QUEUE|PREDICTIVE_PREEMPTION_COOLDOWN_SECONDS|X-PIG-(Lane|Tier|Output-Tokens|Backend)|X-User-Tier' \
    >/tmp/pig-legacy-env-audit.txt
then
    cat /tmp/pig-legacy-env-audit.txt >&2
    fail "retired environment variable, header, or active documentation remains"
fi

if search_active_go \
    'LearnedScheduler|InputSizeCalibrator|CountCoordinator|ErrUnsupportedRequestClass|RequestClass(Chat|Completion|Responses)|PriorityInjector|legacyQoS|qosGate|kvShadow|dynamicController' \
    >/tmp/pig-legacy-symbol-audit.txt
then
    cat /tmp/pig-legacy-symbol-audit.txt >&2
    fail "retired algorithm symbol remains"
fi

compat_files=$(list_compatibility_files 'pig_dynamic_(observed_(running|waiting)(_raw)?|global_limit(_raw)?)' || true)
unexpected_compat=$(printf '%s\n' "$compat_files" | grep -E -v '^internal/observability/metrics/router_capacity_compatibility\.go$' || true)
if [ -n "$unexpected_compat" ]; then
    printf '%s\n' "$unexpected_compat" >&2
    fail "Router compatibility names exist outside the predictive compatibility writer"
fi

if [ "$failed" -ne 0 ]; then
    exit 1
fi

printf '%s\n' 'legacy-audit: clean'
