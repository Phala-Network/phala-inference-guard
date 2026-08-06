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
		if scenario.Category == "long-prefill" {
			if err := validateLongPrefillContract(scenario.Name, baseline, candidate); err != nil {
				return err
			}
		}
		if scenario.Category == "prefill-burst" {
			if err := validatePrefillBurstContract(scenario.Name, baseline, candidate); err != nil {
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

func validatePrefillBurstContract(name string, baseline, candidate Metrics) error {
	if name != "prefill-regular-multimodal-burst" {
		return fmt.Errorf("scenario %s has no registered prefill-burst contract", name)
	}
	if baseline.Admitted != 40 || candidate.Admitted != 32 || candidate.Rejected != 8 || candidate.SizeProtects != 8 {
		return fmt.Errorf(
			"scenario %s regular multimodal burst baseline/candidate=%+v/%+v, want 40 admits vs 32 admits plus 8 size protections",
			name,
			baseline,
			candidate,
		)
	}
	if candidate.TPSFloorViolationSeconds > baseline.TPSFloorViolationSeconds+simulationDurationEpsilon ||
		candidate.WaitingSeconds > baseline.WaitingSeconds+simulationDurationEpsilon {
		return fmt.Errorf("scenario %s did not reduce burst QoS pressure: baseline/candidate=%+v/%+v", name, baseline, candidate)
	}
	return nil
}

func validateLongPrefillContract(name string, baseline, candidate Metrics) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("scenario %s long-prefill contract: %s", name, fmt.Sprintf(format, args...))
	}
	switch name {
	case "prefill-weighted-budget":
		if baseline.Admitted != 2 || candidate.Admitted != 1 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("baseline/candidate=%+v/%+v, want 2 admits vs one admit plus one size protection", baseline, candidate)
		}
	case "prefill-long-singleton":
		if candidate.Admitted != 2 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("candidate=%+v, want one 300K plus one short admitted and second 300K protected", candidate)
		}
	case "prefill-live-weighted-upper-240k-estimate-99k":
		if baseline.Admitted != 3 || candidate.Admitted != 2 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("baseline/candidate=%+v/%+v, want 240K safety upper to preserve two 99K interference admits before the 256K budget protects", baseline, candidate)
		}
	case "prefill-live-exclusive-upper-690k-estimate-285k":
		if baseline.Admitted != 3 || candidate.Admitted != 2 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("baseline/candidate=%+v/%+v, want 690K safety upper with 285K interference estimate to admit one exclusive plus one short", baseline, candidate)
		}
	case "prefill-quiescent-boundary-busy-512k":
		if baseline.Admitted != 1 || candidate.Admitted != 0 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("baseline/candidate=%+v/%+v, want exact 512K busy request protected as quiescent", baseline, candidate)
		}
	case "prefill-quiescent-idle-650k":
		if candidate.Admitted != 1 || candidate.Rejected != 0 || candidate.HardFitIdleRejects != 0 || candidate.Completed != 1 {
			return fail("candidate=%+v, want idle first 650K admitted and completed without self-lock", candidate)
		}
	case "prefill-quiescent-busy-650k":
		if baseline.Admitted != 1 || candidate.Admitted != 0 || candidate.Rejected != 1 || candidate.SizeProtects != 1 {
			return fail("baseline/candidate=%+v/%+v, want busy 650K pre-forward size protection", baseline, candidate)
		}
		if candidate.TPSFloorViolationSeconds > baseline.TPSFloorViolationSeconds+simulationDurationEpsilon ||
			candidate.SLOCompletionTokens+simulationGoodputEpsilon < baseline.SLOCompletionTokens {
			return fail("candidate did not protect decode QoS: baseline/candidate=%+v/%+v", baseline, candidate)
		}
	case "prefill-quiescent-cancel-recovery":
		if candidate.Admitted != 2 || candidate.Rejected != 1 || candidate.SizeProtects != 1 || candidate.Completed != 1 {
			return fail("candidate=%+v, want cancellation to keep stale busy snapshot protected and recover after one idle poll", candidate)
		}
	case "prefill-quiescent-exclusive-recovery":
		if candidate.Admitted != 2 || candidate.Rejected != 2 || candidate.SizeProtects != 2 || candidate.Completed != 2 {
			return fail("candidate=%+v, want small blocked during 650K prefill, second 650K blocked during local decode, and small admitted after prefill before next poll", candidate)
		}
	default:
		return fail("unregistered long-prefill scenario")
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
