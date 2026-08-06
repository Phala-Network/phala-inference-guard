package requestaware

import (
	"reflect"
	"testing"
)

func TestDeterministicRequestAwareGoodputSuiteUsesProductionPolicyAndRequiredMatrix(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if suite.Seed != SimulationSeed {
		t.Fatalf("seed=%d want=%d", suite.Seed, SimulationSeed)
	}
	if suite.ProductionPolicyCalls == 0 {
		t.Fatal("simulation did not call production RequestAwarePolicy")
	}
	required := map[string]bool{
		"short-only": false, "large-only": false,
		"mix-80-20": false, "mix-50-50": false, "mix-20-80": false,
		"small-then-large": false, "large-then-small": false,
		"pre-poll-burst": false, "low-flow-first-large": false,
		"transient-waiting": false, "sustained-waiting": false,
		"tps-target": false, "tps-floor": false,
		"kv-low": false, "kv-mid": false, "kv-high": false,
		"preemption": false, "stale-recovery": false,
		"small-large-output": false, "large-small-output": false,
		"cancel": false, "short-completion": false, "long-streaming": false,
	}
	for _, scenario := range suite.Scenarios {
		if _, ok := required[scenario.Name]; ok {
			required[scenario.Name] = true
		}
		for _, policy := range []PolicyName{PolicyGlobalBinary, PolicyRequestAware} {
			if _, ok := scenario.Policies[policy]; !ok {
				t.Fatalf("scenario %q missing policy %q", scenario.Name, policy)
			}
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("required scenario %q is missing", name)
		}
	}
}

func TestDeterministicRequestAwareGoodputSuiteIsReplayable(t *testing.T) {
	first, err := RunSuite()
	if err != nil {
		t.Fatalf("first RunSuite: %v", err)
	}
	second, err := RunSuite()
	if err != nil {
		t.Fatalf("second RunSuite: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fixed-seed request-aware simulation is not replayable")
	}
}

func TestDeterministicRequestAwareGoodputSuiteMeetsRegisteredAcceptance(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	for _, scenario := range suite.Scenarios {
		t.Logf(
			"scenario=%s baseline=%+v candidate=%+v",
			scenario.Name,
			scenario.Policies[PolicyGlobalBinary],
			scenario.Policies[PolicyRequestAware],
		)
	}
	if err := ValidateAcceptance(suite); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
}

func TestValidateAcceptanceDoesNotAccumulatePerScenarioTPSFloorTolerance(t *testing.T) {
	suite := Suite{
		ProductionPolicyCalls: 1,
		Scenarios: []ScenarioResult{
			{
				Name: "first",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {},
					PolicyRequestAware: {TPSFloorViolationSeconds: 0.1},
				},
			},
			{
				Name: "second",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {},
					PolicyRequestAware: {TPSFloorViolationSeconds: 0.1},
				},
			},
		},
	}
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("aggregate acceptance allowed per-scenario TPS-floor tolerance to accumulate")
	}
}

func TestValidateAcceptanceDoesNotAccumulatePerScenarioWaitingTolerance(t *testing.T) {
	suite := Suite{
		ProductionPolicyCalls: 1,
		Scenarios: []ScenarioResult{
			{
				Name: "first",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {},
					PolicyRequestAware: {WaitingSeconds: 0.1},
				},
			},
			{
				Name: "second",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {},
					PolicyRequestAware: {WaitingSeconds: 0.1},
				},
			},
		},
	}
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("aggregate acceptance allowed per-scenario waiting tolerance to accumulate")
	}
}

func TestValidateBoundedGoodputRegressionDoesNotUseDurationTolerance(t *testing.T) {
	baseline := Metrics{CompletionTokensPerSecond: 1, SLOCompletionTokensPerSecond: 1}
	candidate := Metrics{CompletionTokensPerSecond: 0.90, SLOCompletionTokensPerSecond: 0.90}
	if err := validateBoundedGoodputRegression("units", baseline, candidate); err == nil {
		t.Fatal("goodput regression was hidden by a seconds-valued tolerance")
	}
}

func TestValidateGoodputImprovementDoesNotHideOtherMetricRegression(t *testing.T) {
	baseline := Metrics{CompletionTokensPerSecond: 1, SLOCompletionTokensPerSecond: 1}
	candidate := Metrics{CompletionTokensPerSecond: 0.95, SLOCompletionTokensPerSecond: 1.01}
	if err := validateGoodputImprovement("units", baseline, candidate); err == nil {
		t.Fatal("one improved goodput metric hid a regression in the other through a seconds-valued tolerance")
	}
}

func TestValidateAcceptanceRejectsBurstGoodputRegressionWithinQoSBudget(t *testing.T) {
	suite := Suite{
		ProductionPolicyCalls: 1,
		Scenarios: []ScenarioResult{
			{
				Name:     "burst-regression",
				Category: "burst",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {
						CompletionTokensPerSecond:    100,
						SLOCompletionTokensPerSecond: 100,
						TPSFloorViolationSeconds:     0.1,
					},
					PolicyRequestAware: {
						CompletionTokensPerSecond:    70,
						SLOCompletionTokensPerSecond: 70,
					},
				},
			},
		},
	}
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("burst goodput regression passed merely because candidate removed an allowed TPS-floor tick")
	}
}

func TestValidateAcceptanceLimitsIdleWithDemandToOnePoll(t *testing.T) {
	suite := Suite{
		ProductionPolicyCalls: 1,
		Scenarios: []ScenarioResult{
			{
				Name: "short-only",
				Policies: map[PolicyName]Metrics{
					PolicyGlobalBinary: {},
					PolicyRequestAware: {MaximumIdleWithDemandSeconds: 0.6},
				},
			},
		},
	}
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("idle/self-lock acceptance allowed one poll plus a duration budget")
	}
}
