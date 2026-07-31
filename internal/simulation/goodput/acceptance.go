package goodput

type PolicyName string

const (
	PolicyCurrentThreshold PolicyName = "current_threshold"
	PolicyV090KVOnly       PolicyName = "v0.9.0_kv_only"
	PolicyExactKVOnly      PolicyName = "exact_token_kv_only"
	PolicyPredictiveQoS    PolicyName = "exact_token_kv_qos"
)

type Metrics struct {
	Arrivals                   int   `json:"arrivals"`
	Admitted                   int   `json:"admitted"`
	Completed                  int   `json:"completed"`
	SLOCompliantCompletions    int   `json:"slo_compliant_completions"`
	CompletionTokenGoodput     int64 `json:"completion_token_goodput"`
	ExistingOrNewTPSViolations int   `json:"existing_or_new_tps_violations"`
	TTFTViolations             int   `json:"ttft_violations"`
	TPOTViolations             int   `json:"tpot_violations"`
	KVHardViolations           int   `json:"kv_hard_violations"`
	PreemptionProxyEvents      int   `json:"preemption_proxy_events"`
	FalseAccepts               int   `json:"false_accepts"`
	FalseDenies                int   `json:"false_denies"`
	ReservationLeaks           int   `json:"reservation_leaks"`
	PeakProjectedKVTokens      int64 `json:"peak_projected_kv_tokens"`
	MinimumProjectedKVHeadroom int64 `json:"minimum_projected_kv_headroom"`
}

func (m Metrics) SafetyViolations() int {
	return m.ExistingOrNewTPSViolations + m.TTFTViolations + m.TPOTViolations + m.KVHardViolations + m.PreemptionProxyEvents + m.ReservationLeaks
}

type ScenarioResult struct {
	Name            string                 `json:"name"`
	LongPromptSuite bool                   `json:"long_prompt_suite"`
	Policies        map[PolicyName]Metrics `json:"policies"`
}

type SuiteResult struct {
	Scenarios []ScenarioResult `json:"scenarios"`
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
