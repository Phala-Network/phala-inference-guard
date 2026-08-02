package goodput

type PolicyName string

const (
	PolicyCurrentThreshold PolicyName = "current_threshold"
	PolicyV090KVOnly       PolicyName = "v0.9.0_kv_only"
	PolicyExactKVOnly      PolicyName = "exact_token_kv_only"
	PolicyPredictiveQoS    PolicyName = "model_agnostic_approximate_qos"
)

type Metrics struct {
	Arrivals                   int              `json:"arrivals"`
	Admitted                   int              `json:"admitted"`
	Completed                  int              `json:"completed"`
	SLOCompliantCompletions    int              `json:"slo_compliant_completions"`
	CompletionTokenGoodput     int64            `json:"completion_token_goodput"`
	ExistingOrNewTPSViolations int              `json:"existing_or_new_tps_violations"`
	TTFTViolations             int              `json:"ttft_violations"`
	TPOTViolations             int              `json:"tpot_violations"`
	KVHardViolations           int              `json:"kv_hard_violations"`
	PreemptionProxyEvents      int              `json:"preemption_proxy_events"`
	FalseAccepts               int              `json:"false_accepts"`
	FalseDenies                int              `json:"false_denies"`
	ReservationLeaks           int              `json:"reservation_leaks"`
	PeakProjectedKVTokens      int64            `json:"peak_projected_kv_tokens"`
	PeakReservedKVTokens       int64            `json:"peak_reserved_kv_tokens"`
	MinimumProjectedKVHeadroom int64            `json:"minimum_projected_kv_headroom"`
	AdmissionTrace             []AdmissionTrace `json:"admission_trace,omitempty"`
}

// AdmissionTrace is a payload-free deterministic explanation of one simulated
// pre-forward decision. Request IDs are synthetic scenario identifiers; no
// prompt, request body, token IDs, or credentials enter the simulation report.
type AdmissionTrace struct {
	RequestID                 string     `json:"request_id"`
	AtMilliseconds            int64      `json:"at_milliseconds"`
	Policy                    PolicyName `json:"policy"`
	Admitted                  bool       `json:"admitted"`
	Reason                    string     `json:"reason"`
	GroundProtectedSafe       bool       `json:"ground_protected_safe"`
	GroundKVSafe              bool       `json:"ground_kv_safe"`
	GroundTPSSafe             bool       `json:"ground_tps_safe"`
	GroundTPOTSafe            bool       `json:"ground_tpot_safe"`
	GroundTTFTSafe            bool       `json:"ground_ttft_safe"`
	GroundProjectedKVTokens   int64      `json:"ground_projected_kv_tokens"`
	GroundUserTPS             float64    `json:"ground_user_tps"`
	GroundTPOTMicroseconds    int64      `json:"ground_tpot_microseconds"`
	GroundTTFTMicroseconds    int64      `json:"ground_ttft_microseconds"`
	PredictionSource          string     `json:"prediction_source,omitempty"`
	PredictionSamples         int        `json:"prediction_samples,omitempty"`
	ExistingDecodeSequences   int        `json:"existing_decode_sequences,omitempty"`
	ProjectedDecodeSequences  int        `json:"projected_decode_sequences,omitempty"`
	PredictedExistingUserTPS  float64    `json:"predicted_existing_user_tps,omitempty"`
	PredictedNewUserTPS       float64    `json:"predicted_new_user_tps,omitempty"`
	PredictedTPOTMicroseconds int64      `json:"predicted_tpot_microseconds,omitempty"`
	PredictedTTFTMicroseconds int64      `json:"predicted_ttft_microseconds,omitempty"`
	PredictedPhysicalKVTokens int64      `json:"predicted_physical_kv_tokens,omitempty"`
	ReservedPhysicalKVTokens  int64      `json:"reserved_physical_kv_tokens,omitempty"`
}

func (m Metrics) SafetyViolations() int {
	// TTFT remains measured, but the current predictive contract removes it from admission
	// protection. Keep TTFTViolations as diagnostic evidence without treating it
	// as a protected-QoS failure.
	return m.ExistingOrNewTPSViolations + m.TPOTViolations + m.KVHardViolations + m.PreemptionProxyEvents + m.ReservationLeaks
}

func (m Metrics) ObservedViolations() int {
	return m.SafetyViolations() + m.TTFTViolations
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
	hasHeadroom := false
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
		if metrics.PeakReservedKVTokens > result.PeakReservedKVTokens {
			result.PeakReservedKVTokens = metrics.PeakReservedKVTokens
		}
		if !hasHeadroom || metrics.MinimumProjectedKVHeadroom < result.MinimumProjectedKVHeadroom {
			result.MinimumProjectedKVHeadroom = metrics.MinimumProjectedKVHeadroom
			hasHeadroom = true
		}
	}
	if !hasHeadroom {
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
