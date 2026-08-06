package requestaware

import "fmt"

const (
	simulationDurationEpsilon = 0.000001
	simulationGoodputEpsilon  = 0.000000001
)

func simulationOneTickBudgetSeconds() float64 {
	return simulationTick.Seconds() + simulationDurationEpsilon
}

func ValidateAcceptance(suite Suite) error {
	if suite.ProductionPolicyCalls <= 0 {
		return fmt.Errorf("production RequestAwarePolicy was not called")
	}
	for _, scenario := range suite.Scenarios {
		baseline, baselineOK := scenario.Policies[PolicyGlobalBinary]
		candidate, candidateOK := scenario.Policies[PolicyRequestAware]
		if !baselineOK || !candidateOK {
			return fmt.Errorf("scenario %s does not contain both policies", scenario.Name)
		}
		if candidate.Preemptions > baseline.Preemptions {
			return fmt.Errorf("scenario %s candidate preemptions=%d exceed baseline=%d", scenario.Name, candidate.Preemptions, baseline.Preemptions)
		}
		if candidate.TPSFloorViolationSeconds > baseline.TPSFloorViolationSeconds+simulationOneTickBudgetSeconds() {
			return fmt.Errorf("scenario %s candidate TPS-floor violation %.3fs exceeds baseline %.3fs", scenario.Name, candidate.TPSFloorViolationSeconds, baseline.TPSFloorViolationSeconds)
		}
		if candidate.WaitingSeconds > baseline.WaitingSeconds+simulationOneTickBudgetSeconds() {
			return fmt.Errorf("scenario %s candidate waiting %.3fs exceeds baseline %.3fs", scenario.Name, candidate.WaitingSeconds, baseline.WaitingSeconds)
		}
		if (scenario.Name == "short-only" || scenario.Name == "large-only" || scenario.Name == "low-flow-first-large") &&
			candidate.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
			return fmt.Errorf("scenario %s candidate idle/self-lock %.3fs exceeds one poll", scenario.Name, candidate.MaximumIdleWithDemandSeconds)
		}
		if scenario.Category == "uniform" || scenario.Category == "low-flow" || scenario.Category == "burst" || scenario.Category == "output-horizon" {
			if err := validateBoundedGoodputRegression(scenario.Name, baseline, candidate); err != nil {
				return err
			}
		}
		if scenario.Category == "mixed" || scenario.Category == "order" {
			if err := validateGoodputImprovement(scenario.Name, baseline, candidate); err != nil {
				return err
			}
		}
	}
	baseline := suite.Aggregate(PolicyGlobalBinary)
	candidate := suite.Aggregate(PolicyRequestAware)
	if candidate.TPSFloorViolationSeconds > baseline.TPSFloorViolationSeconds+simulationOneTickBudgetSeconds() {
		return fmt.Errorf(
			"suite candidate TPS-floor violation %.3fs exceeds baseline %.3fs",
			candidate.TPSFloorViolationSeconds,
			baseline.TPSFloorViolationSeconds,
		)
	}
	if candidate.WaitingSeconds > baseline.WaitingSeconds+simulationOneTickBudgetSeconds() {
		return fmt.Errorf(
			"suite candidate waiting %.3fs exceeds baseline %.3fs",
			candidate.WaitingSeconds,
			baseline.WaitingSeconds,
		)
	}
	return nil
}

func validateBoundedGoodputRegression(name string, baseline, candidate Metrics) error {
	const minimumRatio = 0.99
	if candidate.CompletionTokensPerSecond+simulationGoodputEpsilon < baseline.CompletionTokensPerSecond*minimumRatio ||
		candidate.SLOCompletionTokensPerSecond+simulationGoodputEpsilon < baseline.SLOCompletionTokensPerSecond*minimumRatio {
		return fmt.Errorf(
			"scenario %s regressed goodput beyond 1%%: total %.3f/%.3f slo %.3f/%.3f",
			name,
			candidate.CompletionTokensPerSecond,
			baseline.CompletionTokensPerSecond,
			candidate.SLOCompletionTokensPerSecond,
			baseline.SLOCompletionTokensPerSecond,
		)
	}
	return nil
}

func validateGoodputImprovement(name string, baseline, candidate Metrics) error {
	const minimumImprovement = 1.01
	totalImproved := candidate.CompletionTokensPerSecond >= baseline.CompletionTokensPerSecond*minimumImprovement
	sloImproved := candidate.SLOCompletionTokensPerSecond >= baseline.SLOCompletionTokensPerSecond*minimumImprovement
	totalNotLower := candidate.CompletionTokensPerSecond+simulationGoodputEpsilon >= baseline.CompletionTokensPerSecond
	sloNotLower := candidate.SLOCompletionTokensPerSecond+simulationGoodputEpsilon >= baseline.SLOCompletionTokensPerSecond
	if (!totalImproved && !sloImproved) || !totalNotLower || !sloNotLower {
		return fmt.Errorf(
			"scenario %s did not improve one goodput metric without regressing the other: total %.3f/%.3f slo %.3f/%.3f",
			name,
			candidate.CompletionTokensPerSecond,
			baseline.CompletionTokensPerSecond,
			candidate.SLOCompletionTokensPerSecond,
			baseline.SLOCompletionTokensPerSecond,
		)
	}
	return nil
}
