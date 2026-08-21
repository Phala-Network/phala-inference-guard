package requestaware

import (
	"fmt"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const SimulationSeed int64 = 0x504947_0110

type PolicyName string

const (
	PolicyNoAdmission PolicyName = "no_admission"
	PolicyV0122       PolicyName = "v0.12.2"
	PolicyV01210      PolicyName = "v0.12.10"
	PolicyCandidate   PolicyName = "candidate"
)

type Metrics struct {
	Arrivals                      int     `json:"arrivals"`
	Admitted                      int     `json:"admitted"`
	Rejected                      int     `json:"rejected"`
	HardProtects                  int     `json:"hard_protects"`
	SizeProtects                  int     `json:"size_protects"`
	Completed                     int     `json:"completed"`
	CompletionTokens              float64 `json:"completion_tokens"`
	BackgroundOutputTokens        float64 `json:"background_output_tokens"`
	RequestOutputTokens           float64 `json:"request_output_tokens"`
	SuccessfulRequestOutputTokens float64 `json:"successful_request_output_tokens"`
	SLOCompletionTokens           float64 `json:"slo_completion_tokens"`
	CompletionTokensPerSecond     float64 `json:"completion_tokens_per_second"`
	SLOCompletionTokensPerSecond  float64 `json:"slo_completion_tokens_per_second"`
	Preemptions                   int     `json:"preemptions"`
	BackendResets                 int     `json:"backend_resets"`
	ResetDroppedRequests          int     `json:"reset_dropped_requests"`
	TPSFloorViolationSeconds      float64 `json:"tps_floor_violation_seconds"`
	WaitingSeconds                float64 `json:"waiting_seconds"`
	QueueWaitP95Seconds           float64 `json:"queue_wait_p95_seconds"`
	QueueWaitMaximumSeconds       float64 `json:"queue_wait_maximum_seconds"`
	MaximumIdleWithDemandSeconds  float64 `json:"maximum_idle_with_demand_seconds"`
	HardFitIdleRejects            int     `json:"hard_fit_idle_rejects"`
	PeakKVTokens                  int64   `json:"peak_kv_tokens"`
	MaximumRunning                int     `json:"maximum_running"`
	TPSQoSBudgetAdmissions        int     `json:"tps_qos_budget_admissions"`
	MaximumQoSBudgetLeases        int     `json:"maximum_qos_budget_leases"`
	DecodeSequenceSeconds         float64 `json:"decode_sequence_seconds"`
	MeanActiveTPS                 float64 `json:"mean_active_tps"`
}

type ScenarioResult struct {
	Name              string                                     `json:"name"`
	Category          string                                     `json:"category"`
	DurationSeconds   float64                                    `json:"duration_seconds"`
	CapabilityProfile runtimepredictive.BackendCapabilityProfile `json:"candidate_capability_profile"`
	Policies          map[PolicyName]Metrics                     `json:"policies"`
}

type Suite struct {
	Seed                      int64            `json:"seed"`
	HistoricalBaselineRecords int              `json:"historical_baseline_records"`
	ControllerPolicyCalls     int              `json:"controller_policy_calls"`
	Scenarios                 []ScenarioResult `json:"scenarios"`
}

func RunSuite() (Suite, error) {
	return runSuite([]PolicyName{PolicyNoAdmission, PolicyV0122, PolicyV01210, PolicyCandidate})
}

func runSuite(policyOrder []PolicyName) (Suite, error) {
	if err := validatePolicyOrder(policyOrder); err != nil {
		return Suite{}, err
	}
	frozenV01210, err := loadFrozenV01210()
	if err != nil {
		return Suite{}, err
	}
	usedHistorical := make(map[string]struct{}, len(frozenV01210))
	suite := Suite{Seed: SimulationSeed}
	for _, scenario := range simulationScenarios(SimulationSeed) {
		profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
		if err != nil {
			return Suite{}, fmt.Errorf("construct scenario %s capability policy: %w", scenario.name, err)
		}
		result := ScenarioResult{
			Name:              scenario.name,
			Category:          scenario.category,
			DurationSeconds:   scenario.duration.Seconds(),
			CapabilityProfile: profile,
			Policies:          make(map[PolicyName]Metrics, 4),
		}
		for _, policyName := range policyOrder {
			var metrics Metrics
			var calls int
			var runErr error
			if policyName == PolicyV01210 {
				var exists bool
				metrics, exists = frozenV01210[scenario.name]
				if !exists {
					return Suite{}, fmt.Errorf("scenario %s has no frozen v0.12.10 baseline", scenario.name)
				}
				usedHistorical[scenario.name] = struct{}{}
				suite.HistoricalBaselineRecords++
			} else {
				metrics, calls, runErr = runScenario(scenario, policyName, profile)
			}
			if runErr != nil {
				return Suite{}, fmt.Errorf("scenario %s policy %s: %w", scenario.name, policyName, runErr)
			}
			result.Policies[policyName] = metrics
			if policyName == PolicyCandidate {
				suite.ControllerPolicyCalls += calls
			}
		}
		suite.Scenarios = append(suite.Scenarios, result)
	}
	if len(usedHistorical) != len(frozenV01210) {
		return Suite{}, fmt.Errorf("frozen v0.12.10 baseline contains unused scenarios")
	}
	return suite, nil
}

func scenarioMaxModelLen(scenario scenarioSpec) int64 {
	if scenario.maxModelLen > 0 {
		return scenario.maxModelLen
	}
	return 1024 * 1024
}

func validatePolicyOrder(policyOrder []PolicyName) error {
	if len(policyOrder) != 4 {
		return fmt.Errorf("simulation policy order must contain four policies")
	}
	seen := make(map[PolicyName]struct{}, 4)
	for _, policyName := range policyOrder {
		switch policyName {
		case PolicyNoAdmission, PolicyV0122, PolicyV01210, PolicyCandidate:
		default:
			return fmt.Errorf("simulation policy order contains unknown policy %q", policyName)
		}
		if _, duplicate := seen[policyName]; duplicate {
			return fmt.Errorf("simulation policy order repeats policy %q", policyName)
		}
		seen[policyName] = struct{}{}
	}
	return nil
}

func simulationCapabilityProfile(
	scenario scenarioSpec,
	maxModelLen int64,
) (runtimepredictive.BackendCapabilityProfile, error) {
	capacity := scenario.capacityTokens
	if capacity <= 0 {
		capacity = simulationCapacityTokens
	}
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: "deterministic-request-aware-simulation",
		KVCapacityTokens:    capacity,
		KVBlockSize:         simulationBlockSize,
		KVHardRatio:         simulationHardKVRatio,
		MaxModelLen:         maxModelLen,
		Source:              runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		return runtimepredictive.BackendCapabilityProfile{}, err
	}
	return profile, nil
}

func (s Suite) Aggregate(policy PolicyName) Metrics {
	var total Metrics
	var duration float64
	for _, scenario := range s.Scenarios {
		metrics, ok := scenario.Policies[policy]
		if !ok {
			continue
		}
		duration += scenario.DurationSeconds
		total.Arrivals += metrics.Arrivals
		total.Admitted += metrics.Admitted
		total.Rejected += metrics.Rejected
		total.HardProtects += metrics.HardProtects
		total.SizeProtects += metrics.SizeProtects
		total.Completed += metrics.Completed
		total.CompletionTokens += metrics.CompletionTokens
		total.BackgroundOutputTokens += metrics.BackgroundOutputTokens
		total.RequestOutputTokens += metrics.RequestOutputTokens
		total.SuccessfulRequestOutputTokens += metrics.SuccessfulRequestOutputTokens
		total.SLOCompletionTokens += metrics.SLOCompletionTokens
		total.Preemptions += metrics.Preemptions
		total.BackendResets += metrics.BackendResets
		total.ResetDroppedRequests += metrics.ResetDroppedRequests
		total.TPSFloorViolationSeconds += metrics.TPSFloorViolationSeconds
		total.WaitingSeconds += metrics.WaitingSeconds
		total.DecodeSequenceSeconds += metrics.DecodeSequenceSeconds
		total.HardFitIdleRejects += metrics.HardFitIdleRejects
		total.TPSQoSBudgetAdmissions += metrics.TPSQoSBudgetAdmissions
		if metrics.QueueWaitP95Seconds > total.QueueWaitP95Seconds {
			total.QueueWaitP95Seconds = metrics.QueueWaitP95Seconds
		}
		if metrics.QueueWaitMaximumSeconds > total.QueueWaitMaximumSeconds {
			total.QueueWaitMaximumSeconds = metrics.QueueWaitMaximumSeconds
		}
		if metrics.MaximumIdleWithDemandSeconds > total.MaximumIdleWithDemandSeconds {
			total.MaximumIdleWithDemandSeconds = metrics.MaximumIdleWithDemandSeconds
		}
		if metrics.PeakKVTokens > total.PeakKVTokens {
			total.PeakKVTokens = metrics.PeakKVTokens
		}
		if metrics.MaximumRunning > total.MaximumRunning {
			total.MaximumRunning = metrics.MaximumRunning
		}
		if metrics.MaximumQoSBudgetLeases > total.MaximumQoSBudgetLeases {
			total.MaximumQoSBudgetLeases = metrics.MaximumQoSBudgetLeases
		}
	}
	if duration > 0 {
		total.CompletionTokensPerSecond = total.CompletionTokens / duration
		total.SLOCompletionTokensPerSecond = total.SLOCompletionTokens / duration
	}
	if total.DecodeSequenceSeconds > 0 {
		total.MeanActiveTPS = total.CompletionTokens / total.DecodeSequenceSeconds
	}
	return total
}
