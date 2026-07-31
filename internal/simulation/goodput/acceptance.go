package goodput

type PolicyName string

const (
	PolicyCurrentThreshold PolicyName = "current_threshold"
	PolicyV090KVOnly       PolicyName = "v0.9.0_kv_only"
	PolicyExactKVOnly      PolicyName = "exact_token_kv_only"
	PolicyPredictiveQoS    PolicyName = "exact_token_kv_qos"
)

type Metrics struct {
	Arrivals                   int
	Admitted                   int
	Completed                  int
	SLOCompliantCompletions    int
	CompletionTokenGoodput     int64
	ExistingOrNewTPSViolations int
	TTFTViolations             int
	TPOTViolations             int
	KVHardViolations           int
	PreemptionProxyEvents      int
	FalseAccepts               int
	FalseDenies                int
	ReservationLeaks           int
	PeakProjectedKVTokens      int64
	MinimumProjectedKVHeadroom int64
}

func (m Metrics) SafetyViolations() int {
	return m.ExistingOrNewTPSViolations + m.TTFTViolations + m.TPOTViolations + m.KVHardViolations + m.PreemptionProxyEvents + m.ReservationLeaks
}

type ScenarioResult struct {
	Name            string
	LongPromptSuite bool
	Policies        map[PolicyName]Metrics
}

type SuiteResult struct {
	Scenarios []ScenarioResult
}

func RunAcceptanceSuite() (SuiteResult, error) {
	return runAcceptanceSuite()
}

func (s SuiteResult) Aggregate(policy PolicyName) Metrics {
	var result Metrics
	result.MinimumProjectedKVHeadroom = -1
	for _, scenario := range s.Scenarios {
		metrics := scenario.Policies[policy]
		result.Arrivals += metrics.Arrivals
		result.Admitted += metrics.Admitted
		result.Completed += metrics.Completed
		result.SLOCompliantCompletions += metrics.SLOCompliantCompletions
		result.CompletionTokenGoodput += metrics.CompletionTokenGoodput
		result.ExistingOrNewTPSViolations += metrics.ExistingOrNewTPSViolations
		result.TTFTViolations += metrics.TTFTViolations
		result.TPOTViolations += metrics.TPOTViolations
		result.KVHardViolations += metrics.KVHardViolations
		result.PreemptionProxyEvents += metrics.PreemptionProxyEvents
		result.FalseAccepts += metrics.FalseAccepts
		result.FalseDenies += metrics.FalseDenies
		result.ReservationLeaks += metrics.ReservationLeaks
		if metrics.PeakProjectedKVTokens > result.PeakProjectedKVTokens {
			result.PeakProjectedKVTokens = metrics.PeakProjectedKVTokens
		}
		if result.MinimumProjectedKVHeadroom < 0 || metrics.MinimumProjectedKVHeadroom < result.MinimumProjectedKVHeadroom {
			result.MinimumProjectedKVHeadroom = metrics.MinimumProjectedKVHeadroom
		}
	}
	if result.MinimumProjectedKVHeadroom < 0 {
		result.MinimumProjectedKVHeadroom = 0
	}
	return result
}

func improvementPercent(candidate, baseline int64) float64 {
	if baseline <= 0 {
		if candidate > 0 {
			return 100
		}
		return 0
	}
	return 100 * float64(candidate-baseline) / float64(baseline)
}
