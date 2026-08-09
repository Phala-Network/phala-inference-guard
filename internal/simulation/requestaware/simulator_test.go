package requestaware

import (
	"reflect"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
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
		"pre-poll-burst": false, "prefill-regular-multimodal-burst": false, "low-flow-first-large": false,
		"transient-waiting": false, "sustained-waiting": false,
		"tps-target": false, "tps-floor": false,
		"kv-low": false, "kv-mid": false, "kv-high": false,
		"preemption": false, "stale-recovery": false,
		"small-large-output": false, "large-small-output": false,
		"cancel": false, "short-completion": false, "long-streaming": false,
		"prefill-weighted-budget": false, "prefill-weighted-regular-gate-recovery": false,
		"prefill-long-singleton":                          false,
		"prefill-live-weighted-upper-240k-estimate-99k":   false,
		"prefill-live-exclusive-upper-690k-estimate-285k": false,
		"prefill-quiescent-boundary-busy-512k":            false,
		"prefill-quiescent-idle-650k":                     false, "prefill-quiescent-busy-650k": false,
		"prefill-quiescent-cancel-recovery": false, "prefill-quiescent-exclusive-recovery": false,
	}
	wantPolicies := []PolicyName{"no_admission", "v0.12.2", "v0.12.6"}
	for _, scenario := range suite.Scenarios {
		if _, ok := required[scenario.Name]; ok {
			required[scenario.Name] = true
		}
		for _, policy := range wantPolicies {
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

func TestV0126SimulationUsesAtomicResourcePrefillAndDecodeGates(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	want := map[string]struct {
		admitted int
		rejected int
	}{
		"pre-poll-burst":                         {admitted: 5, rejected: 0},
		"prefill-regular-multimodal-burst":       {admitted: 0, rejected: 40},
		"prefill-weighted-regular-gate-recovery": {admitted: 2, rejected: 1},
	}
	for _, scenario := range suite.Scenarios {
		expected, ok := want[scenario.Name]
		if !ok {
			continue
		}
		metrics, present := scenario.Policies[PolicyName("v0.12.6")]
		if !present || metrics.Admitted != expected.admitted || metrics.Rejected != expected.rejected {
			t.Fatalf("scenario %s v0.12.6=%+v present=%t, want admitted/rejected=%d/%d",
				scenario.Name, metrics, present, expected.admitted, expected.rejected)
		}
		delete(want, scenario.Name)
	}
	if len(want) != 0 {
		t.Fatalf("resource/Prefill gate scenarios missing: %v", want)
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

func TestDeterministicRequestAwareGoodputSuiteIsPolicyOrderIndependent(t *testing.T) {
	forward, err := runSuite([]PolicyName{PolicyNoAdmission, PolicyV0122, PolicyV0126})
	if err != nil {
		t.Fatalf("forward policy order: %v", err)
	}
	reverse, err := runSuite([]PolicyName{PolicyV0126, PolicyV0122, PolicyNoAdmission})
	if err != nil {
		t.Fatalf("reverse policy order: %v", err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatal("simulation result depends on policy execution order")
	}
}

func TestDeterministicRequestAwareGoodputSuiteRejectsInvalidPolicyOrder(t *testing.T) {
	for _, order := range [][]PolicyName{
		{PolicyNoAdmission, PolicyV0122},
		{PolicyNoAdmission, PolicyV0122, PolicyV0122},
		{PolicyNoAdmission, PolicyV0122, PolicyName("unknown")},
	} {
		if _, err := runSuite(order); err == nil {
			t.Fatalf("invalid policy order passed: %v", order)
		}
	}
}

func TestCapabilityProfilesChangePreForwardPrefillDecisionUnderSameLiveState(t *testing.T) {
	scenario := scenarioSpec{capacityTokens: 4 * 1024 * 1024}
	shortProfile, shortPolicy, err := simulationCapabilityPolicy(scenario, 256*1024)
	if err != nil {
		t.Fatalf("construct short-context capability policy: %v", err)
	}
	longProfile, longPolicy, err := simulationCapabilityPolicy(scenario, 650*1024)
	if err != nil {
		t.Fatalf("construct long-context capability policy: %v", err)
	}
	input := runtimepredictive.RequestAwareInput{
		MetricsFresh:                true,
		IdentityValid:               true,
		CapacityTokens:              scenario.capacityTokens,
		UsedTokens:                  100_000,
		RequestReservedTokens:       200 * 1024,
		SelectionInputTokens:        200 * 1024,
		EstimatedPrefillTokens:      200 * 1024,
		Running:                     0,
		EffectiveSequences:          0,
		PendingPrefillSequences:     1,
		PendingPrefillTokens:        32 * 1024,
		PendingLongPrefillSequences: 1,
	}
	short := shortPolicy.Evaluate(input)
	long := longPolicy.Evaluate(input)
	if shortProfile.PrefillExclusiveTokens >= input.EstimatedPrefillTokens ||
		short.Action != runtimepredictive.RequestAwareSizeProtect ||
		short.Reason != runtimepredictive.RequestAwareReasonPrefillConcurrency {
		t.Fatalf("short-context profile/decision = %+v/%+v, want exclusive-concurrency protection", shortProfile, short)
	}
	if longProfile.PrefillExclusiveTokens <= input.EstimatedPrefillTokens ||
		long.Action != runtimepredictive.RequestAwareAdmit {
		t.Fatalf("long-context profile/decision = %+v/%+v, want work-conserving admit", longProfile, long)
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
			scenario.Policies[PolicyV0122],
			scenario.Policies[PolicyV0126],
		)
	}
	if err := ValidateAcceptance(suite); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
}

func TestValidateAcceptanceTreatsSyntheticTPSAndWaitingAsDiagnostics(t *testing.T) {
	suite := acceptanceSuite(Metrics{
		Arrivals: 1, Admitted: 1, PeakKVTokens: 64, MaximumRunning: 1,
		TPSFloorViolationSeconds: 20, WaitingSeconds: 20,
	})
	if err := ValidateAcceptance(suite); err != nil {
		t.Fatalf("diagnostic TPS/waiting rejected simulation: %v", err)
	}
}

func TestValidateAcceptanceRejectsPreemptionRegression(t *testing.T) {
	suite := acceptanceSuite(Metrics{
		Arrivals: 1, Admitted: 1, PeakKVTokens: 64, MaximumRunning: 1, Preemptions: 1,
	})
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("candidate preemption regression passed")
	}
}

func TestValidateAcceptanceRejectsKVHardLimitBreach(t *testing.T) {
	suite := acceptanceSuite(Metrics{
		Arrivals: 1, Admitted: 1, PeakKVTokens: 1_001, MaximumRunning: 1,
	})
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("candidate KV hard-limit breach passed")
	}
}

func TestValidateAcceptanceLimitsIdleWithDemandToOnePoll(t *testing.T) {
	suite := acceptanceSuite(Metrics{
		Arrivals: 1, Rejected: 1, SizeProtects: 1, HardFitIdleRejects: 1,
		MaximumIdleWithDemandSeconds: 0.6,
	})
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("idle/self-lock acceptance allowed more than one poll")
	}

	suite = acceptanceSuite(Metrics{
		Arrivals: 1, Rejected: 1, SizeProtects: 1, HardFitIdleRejects: 1,
		MaximumIdleWithDemandSeconds: 0.4,
	})
	if err := ValidateAcceptance(suite); err != nil {
		t.Fatalf("one recoverable protection was treated as self-lock: %v", err)
	}
}

func TestValidateAcceptanceRejectsBrokenRequestAccounting(t *testing.T) {
	suite := acceptanceSuite(Metrics{Arrivals: 1})
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("broken request accounting passed")
	}
}

func TestSimulationCandidateSizeAwareAdmissionContracts(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	want := map[string][2]int{
		"low-flow-first-large":                   {1, 1},
		"prefill-weighted-budget":                {1, 1},
		"prefill-weighted-regular-gate-recovery": {2, 1},
		"prefill-long-singleton":                 {1, 2},
		"prefill-quiescent-idle-650k":            {1, 0},
		"prefill-quiescent-busy-650k":            {0, 1},
		"prefill-quiescent-exclusive-recovery":   {2, 2},
	}
	for _, scenario := range suite.Scenarios {
		contract, exists := want[scenario.Name]
		if !exists {
			continue
		}
		candidate := scenario.Policies[PolicyV0126]
		if candidate.Admitted != contract[0] || candidate.Rejected != contract[1] {
			t.Fatalf(
				"scenario %s admitted/rejected=%d/%d want=%d/%d",
				scenario.Name,
				candidate.Admitted,
				candidate.Rejected,
				contract[0],
				contract[1],
			)
		}
		delete(want, scenario.Name)
	}
	if len(want) != 0 {
		t.Fatalf("size-aware scenarios missing: %v", want)
	}
}

func acceptanceSuite(candidate Metrics) Suite {
	baseline := Metrics{Arrivals: 1, Admitted: 1, PeakKVTokens: 64, MaximumRunning: 1}
	return Suite{
		ProductionPolicyCalls: 1,
		Scenarios: []ScenarioResult{{
			Name: "acceptance-unit",
			CapabilityProfile: runtimepredictive.BackendCapabilityProfile{
				KVHardLimitTokens: 1_000,
			},
			Policies: map[PolicyName]Metrics{
				PolicyNoAdmission: baseline,
				PolicyV0122:       baseline,
				PolicyV0126:       candidate,
			},
		}},
	}
}
