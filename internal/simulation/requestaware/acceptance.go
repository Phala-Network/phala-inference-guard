package requestaware

import (
	"fmt"
	"math"
)

const simulationDurationEpsilon = 0.000001

func ValidateAcceptance(suite Suite) error {
	if suite.ProductionPolicyCalls <= 0 {
		return fmt.Errorf("production v0.12.5 Manager policy was not called")
	}
	for _, scenario := range suite.Scenarios {
		noAdmission, noAdmissionOK := scenario.Policies[PolicyNoAdmission]
		v0122, v0122OK := scenario.Policies[PolicyV0122]
		candidate, candidateOK := scenario.Policies[PolicyV0125]
		if !noAdmissionOK || !v0122OK || !candidateOK {
			return fmt.Errorf("scenario %s does not contain all three policies", scenario.Name)
		}
		if noAdmission.Arrivals != v0122.Arrivals || noAdmission.Arrivals != candidate.Arrivals {
			return fmt.Errorf(
				"scenario %s policies saw different arrivals no-admission/v0.12.2/v0.12.5=%d/%d/%d",
				scenario.Name,
				noAdmission.Arrivals,
				v0122.Arrivals,
				candidate.Arrivals,
			)
		}
		for policy, metrics := range scenario.Policies {
			if err := validateSimulationMetrics(scenario.Name, policy, metrics); err != nil {
				return err
			}
		}
		if candidate.Preemptions > noAdmission.Preemptions || candidate.Preemptions > v0122.Preemptions {
			return fmt.Errorf(
				"scenario %s candidate preemptions=%d exceed no-admission/v0.12.2=%d/%d",
				scenario.Name,
				candidate.Preemptions,
				noAdmission.Preemptions,
				v0122.Preemptions,
			)
		}
		if hardLimit := scenario.CapabilityProfile.KVHardLimitTokens; hardLimit > 0 && candidate.PeakKVTokens > hardLimit {
			return fmt.Errorf(
				"scenario %s candidate peak KV=%d exceeds hard limit=%d",
				scenario.Name,
				candidate.PeakKVTokens,
				hardLimit,
			)
		}
		if candidate.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
			return fmt.Errorf(
				"scenario %s candidate idle-with-demand %.3fs exceeds one observation",
				scenario.Name,
				candidate.MaximumIdleWithDemandSeconds,
			)
		}
	}
	return nil
}

func validateSimulationMetrics(name string, policy PolicyName, metrics Metrics) error {
	if metrics.Arrivals < 0 || metrics.Admitted < 0 || metrics.Rejected < 0 ||
		metrics.HardProtects < 0 || metrics.SizeProtects < 0 || metrics.Completed < 0 ||
		metrics.Preemptions < 0 || metrics.HardFitIdleRejects < 0 || metrics.PeakKVTokens < 0 ||
		metrics.MaximumRunning < 0 {
		return fmt.Errorf("scenario %s policy %s contains a negative counter", name, policy)
	}
	if metrics.Admitted+metrics.Rejected != metrics.Arrivals {
		return fmt.Errorf(
			"scenario %s policy %s admitted+rejected=%d does not equal arrivals=%d",
			name,
			policy,
			metrics.Admitted+metrics.Rejected,
			metrics.Arrivals,
		)
	}
	if metrics.HardProtects+metrics.SizeProtects != metrics.Rejected ||
		metrics.Completed > metrics.Admitted || metrics.HardFitIdleRejects > metrics.Rejected {
		return fmt.Errorf("scenario %s policy %s contains inconsistent request counters", name, policy)
	}
	values := []float64{
		metrics.CompletionTokens,
		metrics.SLOCompletionTokens,
		metrics.CompletionTokensPerSecond,
		metrics.SLOCompletionTokensPerSecond,
		metrics.TPSFloorViolationSeconds,
		metrics.WaitingSeconds,
		metrics.MaximumIdleWithDemandSeconds,
	}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("scenario %s policy %s contains an invalid metric", name, policy)
		}
	}
	if metrics.SLOCompletionTokens > metrics.CompletionTokens+simulationFloatTolerance ||
		metrics.SLOCompletionTokensPerSecond > metrics.CompletionTokensPerSecond+simulationFloatTolerance {
		return fmt.Errorf("scenario %s policy %s SLO goodput exceeds raw goodput", name, policy)
	}
	return nil
}
