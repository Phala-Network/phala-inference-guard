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
	PolicyV0128       PolicyName = "v0.12.8"
)

type Metrics struct {
	Arrivals                     int     `json:"arrivals"`
	Admitted                     int     `json:"admitted"`
	Rejected                     int     `json:"rejected"`
	HardProtects                 int     `json:"hard_protects"`
	SizeProtects                 int     `json:"size_protects"`
	Completed                    int     `json:"completed"`
	CompletionTokens             float64 `json:"completion_tokens"`
	SLOCompletionTokens          float64 `json:"slo_completion_tokens"`
	CompletionTokensPerSecond    float64 `json:"completion_tokens_per_second"`
	SLOCompletionTokensPerSecond float64 `json:"slo_completion_tokens_per_second"`
	Preemptions                  int     `json:"preemptions"`
	TPSFloorViolationSeconds     float64 `json:"tps_floor_violation_seconds"`
	WaitingSeconds               float64 `json:"waiting_seconds"`
	MaximumIdleWithDemandSeconds float64 `json:"maximum_idle_with_demand_seconds"`
	HardFitIdleRejects           int     `json:"hard_fit_idle_rejects"`
	PeakKVTokens                 int64   `json:"peak_kv_tokens"`
	MaximumRunning               int     `json:"maximum_running"`
}

type ScenarioResult struct {
	Name              string                                     `json:"name"`
	Category          string                                     `json:"category"`
	DurationSeconds   float64                                    `json:"duration_seconds"`
	CapabilityProfile runtimepredictive.BackendCapabilityProfile `json:"candidate_capability_profile"`
	Policies          map[PolicyName]Metrics                     `json:"policies"`
}

type Suite struct {
	Seed                  int64            `json:"seed"`
	ProductionPolicyCalls int              `json:"production_policy_calls"`
	Scenarios             []ScenarioResult `json:"scenarios"`
}

func RunSuite() (Suite, error) {
	return runSuite([]PolicyName{PolicyNoAdmission, PolicyV0122, PolicyV0128})
}

func runSuite(policyOrder []PolicyName) (Suite, error) {
	if err := validatePolicyOrder(policyOrder); err != nil {
		return Suite{}, err
	}
	suite := Suite{Seed: SimulationSeed}
	for _, scenario := range simulationScenarios(SimulationSeed) {
		profile, policy, err := simulationCapabilityPolicy(scenario, 650*1024)
		if err != nil {
			return Suite{}, fmt.Errorf("construct scenario %s capability policy: %w", scenario.name, err)
		}
		result := ScenarioResult{
			Name:              scenario.name,
			Category:          scenario.category,
			DurationSeconds:   scenario.duration.Seconds(),
			CapabilityProfile: profile,
			Policies:          make(map[PolicyName]Metrics, 3),
		}
		for _, policyName := range policyOrder {
			metrics, calls, runErr := runScenario(scenario, policyName, profile, policy)
			if runErr != nil {
				return Suite{}, fmt.Errorf("scenario %s policy %s: %w", scenario.name, policyName, runErr)
			}
			result.Policies[policyName] = metrics
			suite.ProductionPolicyCalls += calls
		}
		suite.Scenarios = append(suite.Scenarios, result)
	}
	return suite, nil
}

func validatePolicyOrder(policyOrder []PolicyName) error {
	if len(policyOrder) != 3 {
		return fmt.Errorf("simulation policy order must contain three policies")
	}
	seen := make(map[PolicyName]struct{}, 3)
	for _, policyName := range policyOrder {
		switch policyName {
		case PolicyNoAdmission, PolicyV0122, PolicyV0128:
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

func simulationCapabilityPolicy(
	scenario scenarioSpec,
	maxModelLen int64,
) (runtimepredictive.BackendCapabilityProfile, *runtimepredictive.RequestAwarePolicy, error) {
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
		return runtimepredictive.BackendCapabilityProfile{}, nil, err
	}
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            profile.KVHardLimitTokens,
		BlockSize:                    profile.KVBlockSize,
		PrefillRegularTokens:         profile.PrefillRegularTokens,
		PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
	})
	if err != nil {
		return runtimepredictive.BackendCapabilityProfile{}, nil, err
	}
	return profile, policy, nil
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
		total.SLOCompletionTokens += metrics.SLOCompletionTokens
		total.Preemptions += metrics.Preemptions
		total.TPSFloorViolationSeconds += metrics.TPSFloorViolationSeconds
		total.WaitingSeconds += metrics.WaitingSeconds
		total.HardFitIdleRejects += metrics.HardFitIdleRejects
		if metrics.MaximumIdleWithDemandSeconds > total.MaximumIdleWithDemandSeconds {
			total.MaximumIdleWithDemandSeconds = metrics.MaximumIdleWithDemandSeconds
		}
		if metrics.PeakKVTokens > total.PeakKVTokens {
			total.PeakKVTokens = metrics.PeakKVTokens
		}
		if metrics.MaximumRunning > total.MaximumRunning {
			total.MaximumRunning = metrics.MaximumRunning
		}
	}
	if duration > 0 {
		total.CompletionTokensPerSecond = total.CompletionTokens / duration
		total.SLOCompletionTokensPerSecond = total.SLOCompletionTokens / duration
	}
	return total
}
