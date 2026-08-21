package requestaware

import "fmt"

const (
	tpsDebtScenarioRawGoodputTolerance = 0.001
	tpsDebtAggressiveGoodputTolerance  = 0.002
	tpsDebtAdjacentGoodputTolerance    = 0.001
)

type TPSDebtAcceptanceReport struct {
	SelectedPolicy                       TPSDebtPolicyName `json:"selected_policy"`
	Baseline                             Metrics           `json:"baseline"`
	Candidate                            Metrics           `json:"candidate"`
	DurationSeconds                      float64           `json:"duration_seconds"`
	TotalOutputGoodputGainRatio          float64           `json:"total_output_goodput_gain_ratio"`
	SuccessfulCompletionGain             int               `json:"successful_completion_gain"`
	SuccessfulRequestOutputTokenGain     float64           `json:"successful_request_output_token_gain"`
	CandidateMeanActiveTPS               float64           `json:"candidate_mean_active_tps"`
	CandidateSubReferenceExposureSeconds float64           `json:"candidate_sub_reference_exposure_seconds"`
	Status                               string            `json:"status"`
}

func ValidateTPSDebtAcceptance(suite TPSDebtSuite) (TPSDebtAcceptanceReport, error) {
	if suite.Reference <= 0 || suite.PollInterval != simulationPollInterval ||
		suite.DenominatorExperiment != "unchanged_current_sequence_seconds" ||
		suite.ControllerPolicyCalls <= 0 {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("TPS debt suite identity is invalid")
	}
	if err := validateTPSDebtPolicyMatrix(suite.Policies); err != nil {
		return TPSDebtAcceptanceReport{}, err
	}
	for _, scenario := range suite.Scenarios {
		baseline, baselineOK := scenario.Policies[TPSDebtPolicyDeclaredLifetime]
		candidate, candidateOK := scenario.Policies[TPSDebtPolicyBounded10Seconds]
		if !baselineOK || !candidateOK {
			return TPSDebtAcceptanceReport{}, fmt.Errorf("scenario %s is missing baseline or selected candidate", scenario.Name)
		}
		if err := validateSimulationMetrics(scenario.Name, PolicyName(TPSDebtPolicyDeclaredLifetime), baseline); err != nil {
			return TPSDebtAcceptanceReport{}, err
		}
		if err := validateSimulationMetrics(scenario.Name, PolicyName(TPSDebtPolicyBounded10Seconds), candidate); err != nil {
			return TPSDebtAcceptanceReport{}, err
		}
		if candidate.MaximumQoSBudgetLeases > 1 || candidate.MaximumRunning > 8 ||
			candidate.Preemptions > baseline.Preemptions ||
			candidate.PeakKVTokens > scenario.CapabilityProfile.KVHardLimitTokens ||
			candidate.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
			return TPSDebtAcceptanceReport{}, fmt.Errorf("scenario %s selected candidate violates a safety bound", scenario.Name)
		}
		if candidate.WaitingSeconds > baseline.WaitingSeconds+simulationTick.Seconds()+simulationDurationEpsilon ||
			candidate.QueueWaitP95Seconds > baseline.QueueWaitP95Seconds+simulationTick.Seconds()+simulationDurationEpsilon {
			return TPSDebtAcceptanceReport{}, fmt.Errorf("scenario %s selected candidate materially regresses queueing", scenario.Name)
		}
		if candidate.DecodeSequenceSeconds > 0 &&
			candidate.MeanActiveTPS+simulationFloatTolerance < suite.Reference {
			return TPSDebtAcceptanceReport{}, fmt.Errorf(
				"scenario %s selected candidate mean-active TPS %.3f is below reference %.3f",
				scenario.Name,
				candidate.MeanActiveTPS,
				suite.Reference,
			)
		}
		if candidate.CompletionTokens+simulationFloatTolerance <
			baseline.CompletionTokens*(1-tpsDebtScenarioRawGoodputTolerance) {
			return TPSDebtAcceptanceReport{}, fmt.Errorf("scenario %s selected candidate materially regresses raw output goodput", scenario.Name)
		}
	}

	baseline, baselineDuration := suite.Aggregate(TPSDebtPolicyDeclaredLifetime)
	candidate, candidateDuration := suite.Aggregate(TPSDebtPolicyBounded10Seconds)
	if baselineDuration <= 0 || baselineDuration != candidateDuration || baseline.CompletionTokens <= 0 {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("TPS debt aggregate duration or baseline is invalid")
	}
	if candidate.CompletionTokens < baseline.CompletionTokens*1.01 ||
		candidate.Completed <= baseline.Completed ||
		candidate.SuccessfulRequestOutputTokens <= baseline.SuccessfulRequestOutputTokens ||
		candidate.MeanActiveTPS+simulationFloatTolerance < suite.Reference {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("selected candidate does not improve QoS-constrained completion goodput")
	}

	aggressive, _ := suite.Aggregate(TPSDebtPolicyBounded2Seconds)
	adjacent, _ := suite.Aggregate(TPSDebtPolicyBounded5Seconds)
	conservative, _ := suite.Aggregate(TPSDebtPolicyBounded20Seconds)
	if candidate.CompletionTokens < aggressive.CompletionTokens*(1-tpsDebtAggressiveGoodputTolerance) ||
		candidate.MeanActiveTPS+simulationFloatTolerance < aggressive.MeanActiveTPS ||
		candidate.TPSFloorViolationSeconds > aggressive.TPSFloorViolationSeconds+simulationFloatTolerance {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("selected candidate is dominated by the aggressive horizon")
	}
	if candidate.CompletionTokens < adjacent.CompletionTokens*(1-tpsDebtAdjacentGoodputTolerance) ||
		candidate.MeanActiveTPS+simulationFloatTolerance < adjacent.MeanActiveTPS ||
		candidate.TPSFloorViolationSeconds > adjacent.TPSFloorViolationSeconds+simulationFloatTolerance {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("selected candidate is dominated by the adjacent horizon")
	}
	if candidate.Completed <= conservative.Completed ||
		candidate.SuccessfulRequestOutputTokens <= conservative.SuccessfulRequestOutputTokens ||
		candidate.MeanActiveTPS+simulationFloatTolerance < suite.Reference {
		return TPSDebtAcceptanceReport{}, fmt.Errorf("selected candidate does not beat the over-protective horizon")
	}

	return TPSDebtAcceptanceReport{
		SelectedPolicy:                       TPSDebtPolicyBounded10Seconds,
		Baseline:                             baseline,
		Candidate:                            candidate,
		DurationSeconds:                      candidateDuration,
		TotalOutputGoodputGainRatio:          candidate.CompletionTokens / baseline.CompletionTokens,
		SuccessfulCompletionGain:             candidate.Completed - baseline.Completed,
		SuccessfulRequestOutputTokenGain:     candidate.SuccessfulRequestOutputTokens - baseline.SuccessfulRequestOutputTokens,
		CandidateMeanActiveTPS:               candidate.MeanActiveTPS,
		CandidateSubReferenceExposureSeconds: candidate.TPSFloorViolationSeconds,
		Status:                               "passed",
	}, nil
}

func validateTPSDebtPolicyMatrix(policies []TPSDebtPolicy) error {
	want := tpsDebtPolicies()
	if len(policies) != len(want) {
		return fmt.Errorf("TPS debt policy matrix has %d entries, want %d", len(policies), len(want))
	}
	for index := range want {
		if policies[index] != want[index] {
			return fmt.Errorf("TPS debt policy matrix differs at index %d", index)
		}
	}
	return nil
}
