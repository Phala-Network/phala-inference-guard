package requestaware

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
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
	if suite.HistoricalBaselineRecords == 0 {
		t.Fatal("simulation did not retain the frozen v0.12.10 baseline")
	}
	if suite.ControllerPolicyCalls == 0 {
		t.Fatal("simulation did not call the candidate AdmissionController")
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
		"sustained-worker-replacement-wave": false,
		"prefill-weighted-budget":           false, "prefill-weighted-regular-gate-recovery": false,
		"prefill-long-singleton":                          false,
		"prefill-live-weighted-upper-240k-estimate-99k":   false,
		"prefill-live-exclusive-upper-690k-estimate-285k": false,
		"prefill-quiescent-boundary-busy-512k":            false,
		"prefill-quiescent-idle-650k":                     false, "prefill-quiescent-busy-650k": false,
		"prefill-quiescent-cancel-recovery": false, "prefill-quiescent-exclusive-recovery": false,
		"completion-before-next-poll": false,
	}
	wantPolicies := []PolicyName{"no_admission", "v0.12.2", "v0.12.10", "candidate"}
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

func TestSimulationUsesAtomicResourceAndPrefillQoSGates(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	want := map[string]struct {
		admitted int
		rejected int
	}{
		"pre-poll-burst":                         {admitted: 5, rejected: 0},
		"prefill-regular-multimodal-burst":       {admitted: 8, rejected: 32},
		"prefill-weighted-regular-gate-recovery": {admitted: 3, rejected: 0},
	}
	for _, scenario := range suite.Scenarios {
		expected, ok := want[scenario.Name]
		if !ok {
			continue
		}
		metrics, present := scenario.Policies[PolicyCandidate]
		if !present || metrics.Admitted != expected.admitted || metrics.Rejected != expected.rejected {
			t.Fatalf("scenario %s candidate=%+v present=%t, want admitted/rejected=%d/%d",
				scenario.Name, metrics, present, expected.admitted, expected.rejected)
		}
		delete(want, scenario.Name)
	}
	if len(want) != 0 {
		t.Fatalf("resource/Prefill gate scenarios missing: %v", want)
	}
}

func TestSimulationDoesNotFabricateCapacityBeforeNextPoll(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	for _, scenario := range suite.Scenarios {
		if scenario.Name != "completion-before-next-poll" {
			continue
		}
		candidate := scenario.Policies[PolicyCandidate]
		if candidate.Arrivals != 7 || candidate.Admitted != 2 || candidate.Rejected != 5 ||
			candidate.HardProtects != 0 || candidate.Preemptions != 0 {
			t.Fatalf("completion-before-next-poll candidate=%+v, want 7 arrivals, 2 admits, 5 size protects, and no hard protection/preemption", candidate)
		}
		return
	}
	t.Fatal("completion-before-next-poll scenario is missing")
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

func TestTPSReferenceCandidatesPreserveSaturatedThroughputAndBoundLongRunMean(t *testing.T) {
	scenario := newTPSReferenceSaturationScenario()
	profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
	if err != nil {
		t.Fatalf("construct TPS-reference capability: %v", err)
	}
	baseline, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 0)
	if err != nil {
		t.Fatalf("run disabled-reference baseline: %v", err)
	}
	for _, test := range []struct {
		reference      float64
		maximumRunning int
	}{
		{reference: 20, maximumRunning: 7},
		{reference: 25, maximumRunning: 6},
	} {
		t.Run(fmt.Sprintf("reference_%.0f", test.reference), func(t *testing.T) {
			candidate, _, runErr := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, test.reference)
			if runErr != nil {
				t.Fatalf("run reference %.1f: %v", test.reference, runErr)
			}
			t.Logf("reference=%.1f baseline=%+v candidate=%+v", test.reference, baseline, candidate)
			if candidate.MeanActiveTPS+simulationFloatTolerance < test.reference {
				t.Fatalf("reference %.1f mean active TPS=%.3f below long-run target: %+v", test.reference, candidate.MeanActiveTPS, candidate)
			}
			if candidate.CompletionTokens+simulationFloatTolerance < 0.99*baseline.CompletionTokens {
				t.Fatalf("reference %.1f completion tokens %.3f regress saturated baseline %.3f", test.reference, candidate.CompletionTokens, baseline.CompletionTokens)
			}
			if candidate.SLOCompletionTokens+simulationFloatTolerance < baseline.SLOCompletionTokens {
				t.Fatalf("reference %.1f QoS-qualified tokens %.3f regress baseline %.3f", test.reference, candidate.SLOCompletionTokens, baseline.SLOCompletionTokens)
			}
			if candidate.MaximumRunning > test.maximumRunning || candidate.Preemptions != 0 || candidate.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds() {
				t.Fatalf("reference %.1f candidate=%+v", test.reference, candidate)
			}
		})
	}
}

func TestTPSReferenceBoundsHealthyMixedGoodputRegression(t *testing.T) {
	const reference = 20.0
	var scenario scenarioSpec
	found := false
	for _, candidate := range simulationScenarios(SimulationSeed) {
		if candidate.name == "mix-80-20" {
			scenario = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("mix-80-20 scenario is missing")
	}
	profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
	if err != nil {
		t.Fatalf("construct mixed capability: %v", err)
	}
	baseline, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 0)
	if err != nil {
		t.Fatalf("run reference-disabled mixed baseline: %v", err)
	}
	candidate, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, reference)
	if err != nil {
		t.Fatalf("run reference-enabled mixed candidate: %v", err)
	}
	t.Logf("healthy mixed baseline=%+v candidate=%+v", baseline, candidate)
	if baseline.MeanActiveTPS+simulationFloatTolerance < reference {
		t.Fatalf("fixture baseline TPS %.3f is below reference %.3f", baseline.MeanActiveTPS, reference)
	}
	if candidate.MeanActiveTPS+simulationFloatTolerance < 0.95*reference {
		t.Fatalf("candidate TPS %.3f falls below 95%% reference tolerance: %+v", candidate.MeanActiveTPS, candidate)
	}
	if candidate.SLOCompletionTokens+simulationFloatTolerance < 0.90*baseline.SLOCompletionTokens {
		t.Fatalf("candidate SLO goodput %.3f regresses healthy baseline %.3f by more than 10%%",
			candidate.SLOCompletionTokens, baseline.SLOCompletionTokens)
	}
	if candidate.Preemptions > baseline.Preemptions {
		t.Fatalf("candidate preemptions=%d exceed baseline=%d", candidate.Preemptions, baseline.Preemptions)
	}
}

func newTPSReferenceSaturationScenario() scenarioSpec {
	requests := make([]requestSpec, 0, 20)
	for index := 0; index < 20; index++ {
		request := shapedRequest("tps-reference-saturation", index, 3*time.Second+time.Duration(index)*500*time.Millisecond, shapeLongStreaming)
		request.actualOutput = 10_000
		requests = append(requests, request)
	}
	return scenarioSpec{
		name: "tps-reference-saturation", category: "tps-reference", duration: 60 * time.Second,
		initialKVTokens: 100_000, backgroundRunning: 4, requests: requests,
		capacityTokens: 4 * 1024 * 1024, maxModelLen: 256 * 1024,
		maximumNoWait: 12, aggregateTPSCap: 150,
	}
}

func TestTPSReferenceWarmingIsBoundedAndStopsExpansionAfterBelowReferenceEvidence(t *testing.T) {
	scenario := scenarioSpec{
		name: "tps-reference-bounded-warming", category: "tps-reference", duration: 15 * time.Second,
		initialKVTokens: 100_000, backgroundRunning: 0, capacityTokens: 4 * 1024 * 1024,
		maxModelLen: 256 * 1024, maximumNoWait: 8, aggregateTPSCap: 30,
		requests: []requestSpec{
			{id: "warming-seed", at: 0, selectionInput: 256, safetyInput: 512, decodeHorizon: 256, actualInput: 384, actualOutput: 10_000},
			{id: "warming-second", at: time.Second, selectionInput: 256, safetyInput: 512, decodeHorizon: 256, actualInput: 384, actualOutput: 10_000},
			{id: "warming-burst-must-stop", at: time.Second, selectionInput: 256, safetyInput: 512, decodeHorizon: 256, actualInput: 384, actualOutput: 10_000},
			{id: "ready-must-stop", at: 5 * time.Second, selectionInput: 256, safetyInput: 512, decodeHorizon: 256, actualInput: 384, actualOutput: 10_000},
		},
	}
	profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
	if err != nil {
		t.Fatalf("construct low-flow capability: %v", err)
	}
	metrics, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 20)
	if err != nil {
		t.Fatalf("run low-flow TPS reference: %v", err)
	}
	if metrics.Arrivals != 4 || metrics.Admitted != 2 || metrics.Rejected != 2 ||
		metrics.MaximumRunning != 2 || metrics.Preemptions != 0 {
		t.Fatalf("bounded warming/reference brake metrics=%+v", metrics)
	}
	t.Logf("bounded warming metrics=%+v", metrics)
}

func TestTPSReferencePrePollBurstUsesOneBoundedColdProbe(t *testing.T) {
	var scenario scenarioSpec
	found := false
	for _, candidate := range simulationScenarios(SimulationSeed) {
		if candidate.name == "pre-poll-burst" {
			scenario = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("pre-poll-burst scenario is missing")
	}
	profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
	if err != nil {
		t.Fatalf("construct pre-poll capability: %v", err)
	}
	metrics, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 20)
	if err != nil {
		t.Fatalf("run pre-poll TPS reference: %v", err)
	}
	if metrics.Admitted != 1 || metrics.MaximumRunning != scenario.backgroundRunning+1 ||
		metrics.Preemptions != 0 {
		t.Fatalf("bounded cold probe metrics=%+v", metrics)
	}
	t.Logf("bounded cold probe metrics=%+v", metrics)
}

func TestTPSReferenceSafetyAcrossRepresentativePressureScenarios(t *testing.T) {
	selected := map[string]bool{
		"mix-80-20":                        true,
		"pre-poll-burst":                   true,
		"transient-waiting":                true,
		"sustained-waiting":                true,
		"kv-high":                          true,
		"preemption":                       true,
		"stale-recovery":                   true,
		"cancel":                           true,
		"completion-before-next-poll":      true,
		"prefill-regular-multimodal-burst": true,
		"prefill-weighted-budget":          true,
		"prefill-live-exclusive-upper-690k-estimate-285k": true,
		"prefill-quiescent-boundary-busy-512k":            true,
		"prefill-quiescent-idle-650k":                     true,
		"prefill-quiescent-cancel-recovery":               true,
	}
	for _, scenario := range simulationScenarios(SimulationSeed) {
		if !selected[scenario.name] {
			continue
		}
		delete(selected, scenario.name)
		t.Run(scenario.name, func(t *testing.T) {
			profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
			if err != nil {
				t.Fatalf("construct capability: %v", err)
			}
			baseline, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 0)
			if err != nil {
				t.Fatalf("run reference-disabled baseline: %v", err)
			}
			candidate, _, err := runScenarioWithTPSReference(scenario, PolicyCandidate, profile, 20)
			if err != nil {
				t.Fatalf("run reference-enabled candidate: %v", err)
			}
			t.Logf("baseline=%+v candidate=%+v", baseline, candidate)
			if candidate.PeakKVTokens > profile.KVHardLimitTokens {
				t.Fatalf("KV hard limit exceeded: peak=%d hard=%d", candidate.PeakKVTokens, profile.KVHardLimitTokens)
			}
			if candidate.Preemptions > baseline.Preemptions {
				t.Fatalf("new preemptions: baseline=%d candidate=%d", baseline.Preemptions, candidate.Preemptions)
			}
			if candidate.HardFitIdleRejects > baseline.HardFitIdleRejects ||
				candidate.MaximumIdleWithDemandSeconds > simulationPollInterval.Seconds()+simulationDurationEpsilon {
				t.Fatalf("idle/self-lock protection: %+v", candidate)
			}
			if candidate.MaximumRunning > scenarioMaximumNoWait(scenario) {
				t.Fatalf("scheduler running bound exceeded: running=%d bound=%d", candidate.MaximumRunning, scenarioMaximumNoWait(scenario))
			}
		})
	}
	if len(selected) != 0 {
		t.Fatalf("representative TPS scenarios missing: %v", selected)
	}
}

func scenarioMaximumNoWait(scenario scenarioSpec) int {
	if scenario.maximumNoWait > 0 {
		return scenario.maximumNoWait
	}
	return simulationMaximumNoWait
}

func TestDeterministicRequestAwareGoodputSuiteIsPolicyOrderIndependent(t *testing.T) {
	forward, err := runSuite([]PolicyName{PolicyNoAdmission, PolicyV0122, PolicyV01210, PolicyCandidate})
	if err != nil {
		t.Fatalf("forward policy order: %v", err)
	}
	reverse, err := runSuite([]PolicyName{PolicyCandidate, PolicyV01210, PolicyV0122, PolicyNoAdmission})
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
		{PolicyNoAdmission, PolicyV0122, PolicyV0122, PolicyCandidate},
		{PolicyNoAdmission, PolicyV0122, PolicyV01210, PolicyName("unknown")},
	} {
		if _, err := runSuite(order); err == nil {
			t.Fatalf("invalid policy order passed: %v", order)
		}
	}
}

func TestCapabilityProfilesKeepFixedPrefillBandsAcrossContextLengths(t *testing.T) {
	scenario := scenarioSpec{capacityTokens: 4 * 1024 * 1024}
	shortProfile, err := simulationCapabilityProfile(scenario, 256*1024)
	if err != nil {
		t.Fatalf("construct short-context capability: %v", err)
	}
	longProfile, err := simulationCapabilityProfile(scenario, 650*1024)
	if err != nil {
		t.Fatalf("construct long-context capability: %v", err)
	}
	if shortProfile.PrefillRegularTokens != longProfile.PrefillRegularTokens ||
		shortProfile.PrefillExclusiveTokens != longProfile.PrefillExclusiveTokens ||
		shortProfile.PrefillQuiescentTokens != longProfile.PrefillQuiescentTokens ||
		shortProfile.MaximumAdmissibleInputTokens >= longProfile.MaximumAdmissibleInputTokens {
		t.Fatalf("short/long capability profiles changed band semantics: short=%+v long=%+v", shortProfile, longProfile)
	}
	short := admissionDecisionForProfile(t, shortProfile)
	long := admissionDecisionForProfile(t, longProfile)
	if !short.Admitted() || !long.Admitted() {
		t.Fatalf("same fitting request received context-scaled decisions: short=%+v long=%+v", short, long)
	}
}

func admissionDecisionForProfile(t *testing.T, profile runtimepredictive.BackendCapabilityProfile) coreadmission.DecisionRecord {
	t.Helper()
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{Capability: simulationAdmissionCapability(profile)})
	if err != nil {
		t.Fatalf("construct admission Controller: %v", err)
	}
	defer controller.Close()
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("start admission sample window")
	}
	now := time.Unix(1, 0)
	publication := controller.PublishObservation(window, coreadmission.BackendObservation{
		CapabilityFingerprint: simulationManifestID,
		MaxModelLenTokens:     profile.MaxModelLenTokens,
		KVCapacityTokens:      profile.KVCapacityTokens,
		KVBlockSize:           profile.KVBlockSize,
		ObservedAt:            now,
		MaximumAge:            time.Second,
		UsedKVTokens:          100_000,
	})
	if !publication.Accepted {
		t.Fatalf("publish admission observation: %+v", publication)
	}
	result := controller.Admit(now, simulationRequestEstimate(requestSpec{
		id:             "fixed-band-contract",
		selectionInput: 200 * 1024,
		safetyInput:    200 * 1024,
		decodeHorizon:  256,
	}))
	if result.Decision.Admitted() {
		result.Handle.Terminate(coreadmission.TerminalCancel)
	}
	return result.Decision
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
			scenario.Policies[PolicyCandidate],
		)
	}
	t.Logf(
		"aggregate no_admission=%+v v0.12.2=%+v v0.12.10=%+v candidate=%+v",
		suite.Aggregate(PolicyNoAdmission),
		suite.Aggregate(PolicyV0122),
		suite.Aggregate(PolicyV01210),
		suite.Aggregate(PolicyCandidate),
	)
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

func TestValidateAcceptanceRequiresEveryComparisonPolicy(t *testing.T) {
	candidate := Metrics{Arrivals: 1, Admitted: 1, PeakKVTokens: 64, MaximumRunning: 1}
	for _, missing := range []PolicyName{PolicyNoAdmission, PolicyV0122, PolicyV01210, PolicyCandidate} {
		t.Run(string(missing), func(t *testing.T) {
			suite := acceptanceSuite(candidate)
			delete(suite.Scenarios[0].Policies, missing)
			if err := ValidateAcceptance(suite); err == nil {
				t.Fatalf("acceptance passed without comparison policy %q", missing)
			}
		})
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

func TestValidateAcceptanceRejectsAggregateGoodputRegression(t *testing.T) {
	suite := acceptanceSuite(Metrics{
		Arrivals: 1, Admitted: 1, Completed: 1,
		CompletionTokens: 1, SLOCompletionTokens: 1,
		PeakKVTokens: 64, MaximumRunning: 1,
	})
	baseline := suite.Scenarios[0].Policies[PolicyNoAdmission]
	baseline.Completed = 1
	baseline.CompletionTokens = 2
	baseline.SLOCompletionTokens = 2
	baseline.CompletionTokensPerSecond = 2
	baseline.SLOCompletionTokensPerSecond = 2
	suite.Scenarios[0].Policies[PolicyNoAdmission] = baseline
	if err := ValidateAcceptance(suite); err == nil {
		t.Fatal("aggregate output-token goodput regression passed")
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
		"low-flow-first-large":                   {2, 0},
		"prefill-weighted-budget":                {1, 1},
		"prefill-weighted-regular-gate-recovery": {3, 0},
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
		candidate := scenario.Policies[PolicyCandidate]
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
		HistoricalBaselineRecords: 1,
		ControllerPolicyCalls:     1,
		Scenarios: []ScenarioResult{{
			Name: "acceptance-unit",
			CapabilityProfile: runtimepredictive.BackendCapabilityProfile{
				KVHardLimitTokens: 1_000,
			},
			Policies: map[PolicyName]Metrics{
				PolicyNoAdmission: baseline,
				PolicyV0122:       baseline,
				PolicyV01210:      candidate,
				PolicyCandidate:   candidate,
			},
		}},
	}
}
