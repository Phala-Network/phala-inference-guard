package kv

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/kvshadow"
)

const (
	Estimator64KiBP95Limit = 250 * time.Microsecond
	Estimator2MiBP99Limit  = 10 * time.Millisecond
	ShadowDecisionP99Limit = 50 * time.Microsecond
)

type PerformanceResult struct {
	Estimator64KiBP95 time.Duration `json:"estimator_64kib_p95_ns"`
	Estimator2MiBP99  time.Duration `json:"estimator_2mib_p99_ns"`
	ShadowDecisionP99 time.Duration `json:"shadow_decision_p99_ns"`
	Estimator64KiBN   int           `json:"estimator_64kib_samples"`
	Estimator2MiBN    int           `json:"estimator_2mib_samples"`
	ShadowDecisionN   int           `json:"shadow_decision_samples"`
}

func MeasurePerformance() (PerformanceResult, error) {
	cfg := kvadmission.DefaultEstimatorConfig()
	body64 := estimatorBody(64 * 1024)
	body2MiB := estimatorBody(2 * 1024 * 1024)
	for index := 0; index < 50; index++ {
		if !kvadmission.EstimateJSON(body64, 256, true, cfg).Supported {
			return PerformanceResult{}, fmt.Errorf("64 KiB estimator warmup unsupported")
		}
	}
	d64, err := measureDurations(2000, func(index int) error {
		if !kvadmission.EstimateJSON(body64, 256, true, cfg).Supported {
			return fmt.Errorf("64 KiB estimator unsupported")
		}
		return nil
	})
	if err != nil {
		return PerformanceResult{}, err
	}
	d2MiB, err := measureDurations(200, func(index int) error {
		if !kvadmission.EstimateJSON(body2MiB, 256, true, cfg).Supported {
			return fmt.Errorf("2 MiB estimator unsupported")
		}
		return nil
	})
	if err != nil {
		return PerformanceResult{}, err
	}

	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	manager := kvshadow.New(policy)
	now := time.Unix(100, 0)
	backend := kvadmission.BackendSnapshot{
		Name:              "perf",
		Kind:              kvadmission.BackendVLLM,
		CapacityTokens:    1000000,
		UsedTokens:        1000,
		Updated:           now,
		GenerationTokens:  1000,
		TokenMetricsValid: true,
	}
	cost := kvadmission.Cost{Supported: true, EstimatedInputLow: 100, EstimatedInputHigh: 200, BoundedDecodeTokens: 64}
	decisionDurations, err := measureDurations(20000, func(index int) error {
		id := strconv.Itoa(index)
		decision, reserved := manager.DecideAndReserve(now, id, cost, []kvadmission.BackendSnapshot{backend})
		if decision.Reason != kvadmission.ReasonFit || !reserved {
			return fmt.Errorf("shadow performance decision=%s reserved=%t", decision.Reason, reserved)
		}
		manager.Release(id)
		return nil
	})
	if err != nil {
		return PerformanceResult{}, err
	}

	result := PerformanceResult{
		Estimator64KiBP95: quantileDuration(d64, 0.95),
		Estimator2MiBP99:  quantileDuration(d2MiB, 0.99),
		ShadowDecisionP99: quantileDuration(decisionDurations, 0.99),
		Estimator64KiBN:   len(d64),
		Estimator2MiBN:    len(d2MiB),
		ShadowDecisionN:   len(decisionDurations),
	}
	if result.Estimator64KiBP95 > Estimator64KiBP95Limit {
		return result, fmt.Errorf("64 KiB estimator p95=%s exceeds %s", result.Estimator64KiBP95, Estimator64KiBP95Limit)
	}
	if result.Estimator2MiBP99 > Estimator2MiBP99Limit {
		return result, fmt.Errorf("2 MiB estimator p99=%s exceeds %s", result.Estimator2MiBP99, Estimator2MiBP99Limit)
	}
	if result.ShadowDecisionP99 > ShadowDecisionP99Limit {
		return result, fmt.Errorf("shadow decision p99=%s exceeds %s", result.ShadowDecisionP99, ShadowDecisionP99Limit)
	}
	return result, nil
}

func estimatorBody(size int) []byte {
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	payloadSize := size - len(prefix) - len(suffix)
	if payloadSize < 0 {
		payloadSize = 0
	}
	body := make([]byte, 0, len(prefix)+payloadSize+len(suffix))
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("x"), payloadSize)...)
	body = append(body, suffix...)
	return body
}

func measureDurations(count int, run func(int) error) ([]time.Duration, error) {
	values := make([]time.Duration, count)
	for index := 0; index < count; index++ {
		started := time.Now()
		if err := run(index); err != nil {
			return nil, err
		}
		values[index] = time.Since(started)
	}
	return values, nil
}

func quantileDuration(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted))*quantile + 0.999999)
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}
