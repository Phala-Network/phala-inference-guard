package prometheus

import (
	"strconv"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

// SGLangTopology describes attention DP within a global TP group. Explicit
// geometry prevents incomplete replica scrapes from becoming lower load.
// Zero values retain the existing single-replica contract.
type SGLangTopology struct {
	TPSize int
	DPSize int
}

func (t SGLangTopology) Enabled() bool { return t.TPSize != 0 || t.DPSize != 0 }

func (t SGLangTopology) Valid() bool {
	return !t.Enabled() || (t.TPSize > 0 && t.TPSize <= 4096 &&
		t.DPSize > 0 && t.DPSize <= t.TPSize && t.TPSize%t.DPSize == 0)
}

func metricRank(raw string, size int) (int, bool) {
	n, err := strconv.Atoi(raw)
	return n, err == nil && n >= 0 && n < size && strconv.Itoa(n) == raw
}

func parseSGLangDPSample(index metricIndex, topology SGLangTopology) telemetry.Sample {
	invalid := telemetry.Sample{BackendKind: "sglang"}
	groups := make([]metricIndex, topology.DPSize)
	for i := range groups {
		groups[i] = metricIndex{samples: make(map[string][]indexedMetricSample), types: index.types}
	}
	cacheTopologyValid := true
	for name, items := range index.samples {
		if !strings.HasPrefix(name, "sglang:") {
			continue
		}
		for _, item := range items {
			tpRank, valid := metricRank(item.labels["tp_rank"], topology.TPSize)
			dpRank := tpRank / (topology.TPSize / topology.DPSize)
			valid = valid && item.labelsValid
			if raw, exists := item.labels["dp_rank"]; exists {
				declared, ok := metricRank(raw, topology.DPSize)
				valid = valid && ok && declared == dpRank
			}
			if !valid {
				if name == "sglang:prefill_effective_tokens_total" {
					cacheTopologyValid = false
					continue
				}
				return invalid
			}
			groups[dpRank].samples[name] = append(groups[dpRank].samples[name], item)
		}
	}

	const maximumExact = uint64(1 << 53)
	maximumInt := int(^uint(0) >> 1)
	result := telemetry.Sample{
		BackendKind: "sglang", ModelNameValid: true,
		RunningValid: true, WaitingValid: true, PreemptionsValid: true, GenerationValid: true,
		KVTokenMetricsValid: true, KVBlockSizeValid: true, CacheTokensValid: cacheTopologyValid,
	}
	for i, group := range groups {
		sample := parseSGLangSample(group)
		if !sample.ModelNameValid || !sample.RunningValid || !sample.WaitingValid ||
			!sample.PreemptionsValid || !sample.GenerationValid {
			return invalid
		}
		if i == 0 {
			result.ModelName = sample.ModelName
			result.KVBlockSize = sample.KVBlockSize
		} else if sample.ModelName != result.ModelName {
			return invalid
		}
		if sample.Running > maximumInt-result.Running || sample.Waiting > maximumInt-result.Waiting ||
			sample.Generation > maximumExact-result.Generation || sample.Preemptions > maximumExact-result.Preemptions {
			return invalid
		}
		result.Running += sample.Running
		result.Waiting += sample.Waiting
		result.Generation += sample.Generation
		result.Preemptions += sample.Preemptions

		if sample.KVCapacityTokens > int64(maximumExact)-result.KVCapacityTokens ||
			sample.KVUsedTokens > int64(maximumExact)-result.KVUsedTokens ||
			sample.KVAvailableTokens > int64(maximumExact)-result.KVAvailableTokens ||
			sample.KVEvictableTokens > int64(maximumExact)-result.KVEvictableTokens {
			return invalid
		}
		result.KVTokenMetricsValid = result.KVTokenMetricsValid && sample.KVTokenMetricsValid
		result.KVBlockSizeValid = result.KVBlockSizeValid && sample.KVBlockSizeValid && sample.KVBlockSize == result.KVBlockSize
		result.KVCapacityTokens += sample.KVCapacityTokens
		result.KVUsedTokens += sample.KVUsedTokens
		result.KVAvailableTokens += sample.KVAvailableTokens
		result.KVEvictableTokens += sample.KVEvictableTokens
		if sample.CacheQueryTokens > maximumExact-result.CacheQueryTokens || sample.CacheHitTokens > maximumExact-result.CacheHitTokens {
			result.CacheTokensValid = false
		} else {
			result.CacheQueryTokens += sample.CacheQueryTokens
			result.CacheHitTokens += sample.CacheHitTokens
		}
		result.CacheTokensValid = result.CacheTokensValid && sample.CacheTokensValid
	}
	if result.Running > maximumInt-result.Waiting {
		return invalid
	}
	if result.KVTokenMetricsValid && result.KVCapacityTokens > 0 {
		result.KVCacheUsage = float64(result.KVUsedTokens) / float64(result.KVCapacityTokens)
	}
	if !result.CacheTokensValid {
		result.CacheQueryTokens = 0
		result.CacheHitTokens = 0
	}
	return result
}
