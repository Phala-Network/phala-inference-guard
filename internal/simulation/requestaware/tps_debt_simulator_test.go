package requestaware

import (
	"testing"
	"time"
)

func TestV01218TPSDebtSuiteExercisesBoundedForecastAndLeaseLifecycle(t *testing.T) {
	suite, err := RunTPSDebtSuite()
	if err != nil {
		t.Fatalf("RunTPSDebtSuite: %v", err)
	}
	if suite.Reference != TPSDebtSimulationReference ||
		suite.PollInterval != 500*time.Millisecond ||
		suite.DenominatorExperiment != "unchanged_current_sequence_seconds" ||
		len(suite.Policies) != 7 || suite.ControllerPolicyCalls == 0 {
		t.Fatalf("TPS debt suite identity=%+v", suite)
	}

	declared := tpsDebtScenarioByName(t, suite, "declared-95k-actual-273")
	unknown := tpsDebtScenarioByName(t, suite, "unknown-limit-actual-273")
	for _, scenario := range []TPSDebtScenarioResult{declared, unknown} {
		current := scenario.Policies[TPSDebtPolicyDeclaredLifetime]
		bounded := scenario.Policies[TPSDebtPolicyBounded10Seconds]
		if current.Admitted != 0 || current.TPSQoSBudgetAdmissions != 0 {
			t.Fatalf("scenario %s complete lifetime unexpectedly admitted: %+v", scenario.Name, current)
		}
		if bounded.Admitted != 1 || bounded.TPSQoSBudgetAdmissions != 1 ||
			bounded.MaximumQoSBudgetLeases != 1 || bounded.Completed != 1 ||
			bounded.CompletionTokens <= current.CompletionTokens {
			t.Fatalf("scenario %s bounded horizon did not improve useful work: current=%+v bounded=%+v",
				scenario.Name, current, bounded)
		}
	}

	burst := tpsDebtScenarioByName(t, suite, "one-marginal-lease-burst").Policies[TPSDebtPolicyBounded10Seconds]
	if burst.Admitted != 1 || burst.Rejected != 3 || burst.TPSQoSBudgetAdmissions != 1 ||
		burst.MaximumQoSBudgetLeases != 1 || burst.MaximumRunning > 8 {
		t.Fatalf("bounded burst spent surplus more than once: %+v", burst)
	}

	twoSecond := tpsDebtScenarioByName(t, suite, "mature-surplus-2s")
	if twoSecond.Policies[TPSDebtPolicyBounded2Seconds].Admitted != 1 ||
		twoSecond.Policies[TPSDebtPolicyBounded5Seconds].Admitted != 0 {
		t.Fatalf("two-second surplus did not distinguish horizons: %+v", twoSecond.Policies)
	}
	fiveSecond := tpsDebtScenarioByName(t, suite, "mature-surplus-5s")
	if fiveSecond.Policies[TPSDebtPolicyBounded5Seconds].Admitted != 1 ||
		fiveSecond.Policies[TPSDebtPolicyBounded10Seconds].Admitted != 0 {
		t.Fatalf("five-second surplus did not distinguish horizons: %+v", fiveSecond.Policies)
	}

	prePoll := tpsDebtScenarioByName(t, suite, "completion-before-next-poll-debt").Policies[TPSDebtPolicyBounded10Seconds]
	if prePoll.Admitted != 2 || prePoll.Rejected != 1 || prePoll.TPSQoSBudgetAdmissions != 2 ||
		prePoll.MaximumQoSBudgetLeases != 1 {
		t.Fatalf("completion-before-poll debt lifecycle=%+v", prePoll)
	}

	for _, name := range []string{
		"bounded-debt-cancel",
		"bounded-debt-error",
		"bounded-debt-disconnect",
	} {
		metrics := tpsDebtScenarioByName(t, suite, name).Policies[TPSDebtPolicyBounded10Seconds]
		if metrics.Admitted != 2 || metrics.TPSQoSBudgetAdmissions != 2 ||
			metrics.MaximumQoSBudgetLeases != 1 {
			t.Fatalf("scenario %s terminal reconciliation=%+v", name, metrics)
		}
	}
	sustainedWaiting := tpsDebtScenarioByName(t, suite, "bounded-debt-sustained-waiting").Policies[TPSDebtPolicyBounded10Seconds]
	if sustainedWaiting.Admitted != 1 || sustainedWaiting.Rejected != 2 ||
		sustainedWaiting.TPSQoSBudgetAdmissions != 1 {
		t.Fatalf("sustained waiting did not brake and recover: %+v", sustainedWaiting)
	}
}
func TestV01218TPSDebtSuitePreservesPressureBrakesAndSafetyBounds(t *testing.T) {
	suite, err := RunTPSDebtSuite()
	if err != nil {
		t.Fatalf("RunTPSDebtSuite: %v", err)
	}
	for _, scenario := range suite.Scenarios {
		current := scenario.Policies[TPSDebtPolicyDeclaredLifetime]
		for _, policy := range suite.Policies {
			metrics := scenario.Policies[policy.Name]
			if err := validateSimulationMetrics(scenario.Name, PolicyName(policy.Name), metrics); err != nil {
				t.Fatal(err)
			}
			if metrics.MaximumQoSBudgetLeases > 1 || metrics.Preemptions > current.Preemptions ||
				metrics.PeakKVTokens > scenario.CapabilityProfile.KVHardLimitTokens ||
				metrics.QueueWaitP95Seconds > metrics.QueueWaitMaximumSeconds+simulationFloatTolerance ||
				metrics.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
				t.Fatalf("scenario %s policy %s violated safety bounds: current=%+v candidate=%+v",
					scenario.Name, policy.Name, current, metrics)
			}
		}
	}

	for _, name := range []string{
		"bounded-debt-waiting-recovery",
		"bounded-debt-preemption-recovery",
		"bounded-debt-stale-recovery",
	} {
		metrics := tpsDebtScenarioByName(t, suite, name).Policies[TPSDebtPolicyBounded10Seconds]
		if metrics.Admitted != 1 || metrics.Rejected != 1 || metrics.TPSQoSBudgetAdmissions != 1 {
			t.Fatalf("scenario %s did not brake and recover: %+v", name, metrics)
		}
	}

	lowFlow := tpsDebtScenarioByName(t, suite, "bounded-debt-low-flow")
	for _, policy := range suite.Policies {
		metrics := lowFlow.Policies[policy.Name]
		if metrics.Admitted != 2 || metrics.Rejected != 0 || metrics.HardFitIdleRejects != 0 ||
			metrics.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
			t.Fatalf("low-flow policy %s self-locked: %+v", policy.Name, metrics)
		}
	}

	reset := tpsDebtScenarioByName(t, suite, "bounded-debt-backend-epoch-reset")
	currentReset := reset.Policies[TPSDebtPolicyDeclaredLifetime]
	boundedReset := reset.Policies[TPSDebtPolicyBounded10Seconds]
	if currentReset.BackendResets != 1 || boundedReset.BackendResets != 1 ||
		currentReset.ResetDroppedRequests != 0 || boundedReset.ResetDroppedRequests != 1 ||
		currentReset.Admitted != 1 || boundedReset.Admitted != 2 ||
		boundedReset.MaximumQoSBudgetLeases != 1 {
		t.Fatalf("backend epoch reset did not clear the old lease: current=%+v bounded=%+v",
			currentReset, boundedReset)
	}
}

func TestV01218TPSDebtSuiteIsDeterministic(t *testing.T) {
	first, err := RunTPSDebtSuite()
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunTPSDebtSuite()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Scenarios) != len(second.Scenarios) {
		t.Fatalf("scenario count changed %d/%d", len(first.Scenarios), len(second.Scenarios))
	}
	for index := range first.Scenarios {
		if first.Scenarios[index].Name != second.Scenarios[index].Name {
			t.Fatalf("scenario order changed at %d", index)
		}
		for _, policy := range first.Policies {
			if first.Scenarios[index].Policies[policy.Name] != second.Scenarios[index].Policies[policy.Name] {
				t.Fatalf("scenario %s policy %s is nondeterministic", first.Scenarios[index].Name, policy.Name)
			}
		}
	}
}

func TestV01218TPSDebtAcceptanceSelectsBalancedTenSecondHorizon(t *testing.T) {
	suite, err := RunTPSDebtSuite()
	if err != nil {
		t.Fatal(err)
	}
	report, err := ValidateTPSDebtAcceptance(suite)
	if err != nil {
		t.Fatalf("ValidateTPSDebtAcceptance: %v", err)
	}
	if report.Status != "passed" || report.SelectedPolicy != TPSDebtPolicyBounded10Seconds ||
		report.TotalOutputGoodputGainRatio <= 1.01 || report.SuccessfulCompletionGain <= 0 ||
		report.SuccessfulRequestOutputTokenGain <= 0 ||
		report.CandidateMeanActiveTPS+simulationFloatTolerance < suite.Reference ||
		report.Candidate.MaximumQoSBudgetLeases != 1 {
		t.Fatalf("balanced TPS debt acceptance=%+v", report)
	}
}

func TestV01218TPSDebtAcceptanceRejectsQoSAndSafetyRegressions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Metrics)
	}{
		{name: "preemption", mutate: func(metrics *Metrics) { metrics.Preemptions++ }},
		{name: "second lease", mutate: func(metrics *Metrics) { metrics.MaximumQoSBudgetLeases = 2 }},
		{name: "mean TPS", mutate: func(metrics *Metrics) { metrics.MeanActiveTPS = TPSDebtSimulationReference - 1 }},
		{name: "queue", mutate: func(metrics *Metrics) { metrics.QueueWaitP95Seconds += time.Second }},
	} {
		t.Run(test.name, func(t *testing.T) {
			suite, err := RunTPSDebtSuite()
			if err != nil {
				t.Fatal(err)
			}
			scenario := &suite.Scenarios[0]
			metrics := scenario.Policies[TPSDebtPolicyBounded10Seconds]
			test.mutate(&metrics)
			scenario.Policies[TPSDebtPolicyBounded10Seconds] = metrics
			if _, err := ValidateTPSDebtAcceptance(suite); err == nil {
				t.Fatal("regressed candidate passed TPS debt acceptance")
			}
		})
	}
}

func tpsDebtScenarioByName(t *testing.T, suite TPSDebtSuite, name string) TPSDebtScenarioResult {
	t.Helper()
	for _, scenario := range suite.Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("TPS debt scenario %q is missing", name)
	return TPSDebtScenarioResult{}
}
