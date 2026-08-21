package requestaware

import (
	"fmt"
	"math"
)

const simulationDurationEpsilon = 0.000001

func ValidateAcceptance(suite Suite) error {
	if suite.ControllerPolicyCalls <= 0 {
		return fmt.Errorf("candidate AdmissionController policy was not called")
	}
	for _, scenario := range suite.Scenarios {
		noAdmission, noAdmissionOK := scenario.Policies[PolicyNoAdmission]
		v0122, v0122OK := scenario.Policies[PolicyV0122]
		v01210, v01210OK := scenario.Policies[PolicyV01210]
		candidate, candidateOK := scenario.Policies[PolicyCandidate]
		if !noAdmissionOK || !v0122OK || !v01210OK || !candidateOK {
			return fmt.Errorf("scenario %s does not contain every required comparison policy", scenario.Name)
		}
		if noAdmission.Arrivals != v0122.Arrivals || noAdmission.Arrivals != v01210.Arrivals || noAdmission.Arrivals != candidate.Arrivals {
			return fmt.Errorf(
				"scenario %s policies saw different arrivals no-admission/v0.12.2/v0.12.10/candidate=%d/%d/%d/%d",
				scenario.Name,
				noAdmission.Arrivals,
				v0122.Arrivals,
				v01210.Arrivals,
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
	noAdmission := suite.Aggregate(PolicyNoAdmission)
	v0122 := suite.Aggregate(PolicyV0122)
	v01210 := suite.Aggregate(PolicyV01210)
	candidate := suite.Aggregate(PolicyCandidate)
	if candidate.CompletionTokens+simulationFloatTolerance < noAdmission.CompletionTokens ||
		candidate.CompletionTokens+simulationFloatTolerance < v0122.CompletionTokens ||
		candidate.CompletionTokens+simulationFloatTolerance < v01210.CompletionTokens {
		return fmt.Errorf(
			"candidate aggregate output-token goodput %.3f regresses no-admission/v0.12.2/v0.12.10 %.3f/%.3f/%.3f",
			candidate.CompletionTokens,
			noAdmission.CompletionTokens,
			v0122.CompletionTokens,
			v01210.CompletionTokens,
		)
	}
	if candidate.SLOCompletionTokens+simulationFloatTolerance < noAdmission.SLOCompletionTokens ||
		candidate.SLOCompletionTokens+simulationFloatTolerance < v0122.SLOCompletionTokens ||
		candidate.SLOCompletionTokens+simulationFloatTolerance < v01210.SLOCompletionTokens {
		return fmt.Errorf(
			"candidate aggregate QoS-qualified output-token goodput %.3f regresses no-admission/v0.12.2/v0.12.10 %.3f/%.3f/%.3f",
			candidate.SLOCompletionTokens,
			noAdmission.SLOCompletionTokens,
			v0122.SLOCompletionTokens,
			v01210.SLOCompletionTokens,
		)
	}
	return nil
}

func validateSimulationMetrics(name string, policy PolicyName, metrics Metrics) error {
	if metrics.Arrivals < 0 || metrics.Admitted < 0 || metrics.Rejected < 0 ||
		metrics.HardProtects < 0 || metrics.SizeProtects < 0 || metrics.Completed < 0 ||
		metrics.Preemptions < 0 || metrics.BackendResets < 0 || metrics.ResetDroppedRequests < 0 ||
		metrics.HardFitIdleRejects < 0 || metrics.PeakKVTokens < 0 ||
		metrics.MaximumRunning < 0 || metrics.TPSQoSBudgetAdmissions < 0 ||
		metrics.MaximumQoSBudgetLeases < 0 {
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
		metrics.BackgroundOutputTokens,
		metrics.RequestOutputTokens,
		metrics.SuccessfulRequestOutputTokens,
		metrics.SLOCompletionTokens,
		metrics.CompletionTokensPerSecond,
		metrics.SLOCompletionTokensPerSecond,
		metrics.TPSFloorViolationSeconds,
		metrics.WaitingSeconds,
		metrics.QueueWaitP95Seconds,
		metrics.QueueWaitMaximumSeconds,
		metrics.MaximumIdleWithDemandSeconds,
		metrics.DecodeSequenceSeconds,
		metrics.MeanActiveTPS,
	}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("scenario %s policy %s contains an invalid metric", name, policy)
		}
	}
	breakdownProvided := metrics.BackgroundOutputTokens > 0 || metrics.RequestOutputTokens > 0 ||
		metrics.SuccessfulRequestOutputTokens > 0
	if metrics.SLOCompletionTokens > metrics.CompletionTokens+simulationFloatTolerance ||
		metrics.SLOCompletionTokensPerSecond > metrics.CompletionTokensPerSecond+simulationFloatTolerance ||
		metrics.SuccessfulRequestOutputTokens > metrics.RequestOutputTokens+simulationFloatTolerance ||
		metrics.RequestOutputTokens > metrics.CompletionTokens+simulationFloatTolerance ||
		(breakdownProvided && math.Abs(metrics.BackgroundOutputTokens+metrics.RequestOutputTokens-metrics.CompletionTokens) > simulationFloatTolerance) {
		return fmt.Errorf("scenario %s policy %s contains inconsistent output-token goodput", name, policy)
	}
	return nil
}
