package requestaware

import (
	"fmt"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const TPSDebtSimulationReference = 20.0

type TPSDebtPolicyName string

const (
	TPSDebtPolicyDeclaredLifetime TPSDebtPolicyName = "declared_lifetime"
	TPSDebtPolicyBounded1Second   TPSDebtPolicyName = "bounded_1s"
	TPSDebtPolicyBounded2Seconds  TPSDebtPolicyName = "bounded_2s"
	TPSDebtPolicyBounded5Seconds  TPSDebtPolicyName = "bounded_5s"
	TPSDebtPolicyBounded10Seconds TPSDebtPolicyName = "bounded_10s"
	TPSDebtPolicyBounded20Seconds TPSDebtPolicyName = "bounded_20s"
	TPSDebtPolicyBounded30Seconds TPSDebtPolicyName = "bounded_30s"
)

type TPSDebtPolicy struct {
	Name           TPSDebtPolicyName `json:"name"`
	ControlHorizon time.Duration     `json:"control_horizon"`
}

type TPSDebtScenarioResult struct {
	Name              string                                     `json:"name"`
	Category          string                                     `json:"category"`
	DurationSeconds   float64                                    `json:"duration_seconds"`
	CapabilityProfile runtimepredictive.BackendCapabilityProfile `json:"capability_profile"`
	Policies          map[TPSDebtPolicyName]Metrics              `json:"policies"`
}

type TPSDebtSuite struct {
	Reference             float64                 `json:"reference"`
	PollInterval          time.Duration           `json:"poll_interval"`
	DenominatorExperiment string                  `json:"denominator_experiment"`
	Policies              []TPSDebtPolicy         `json:"policies"`
	Scenarios             []TPSDebtScenarioResult `json:"scenarios"`
	ControllerPolicyCalls int                     `json:"controller_policy_calls"`
}

func (s TPSDebtSuite) Aggregate(policy TPSDebtPolicyName) (Metrics, float64) {
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
		total.BackgroundOutputTokens += metrics.BackgroundOutputTokens
		total.RequestOutputTokens += metrics.RequestOutputTokens
		total.SuccessfulRequestOutputTokens += metrics.SuccessfulRequestOutputTokens
		total.SLOCompletionTokens += metrics.SLOCompletionTokens
		total.Preemptions += metrics.Preemptions
		total.BackendResets += metrics.BackendResets
		total.ResetDroppedRequests += metrics.ResetDroppedRequests
		total.TPSFloorViolationSeconds += metrics.TPSFloorViolationSeconds
		total.WaitingSeconds += metrics.WaitingSeconds
		total.DecodeSequenceSeconds += metrics.DecodeSequenceSeconds
		total.HardFitIdleRejects += metrics.HardFitIdleRejects
		total.TPSQoSBudgetAdmissions += metrics.TPSQoSBudgetAdmissions
		if metrics.QueueWaitP95Seconds > total.QueueWaitP95Seconds {
			total.QueueWaitP95Seconds = metrics.QueueWaitP95Seconds
		}
		if metrics.QueueWaitMaximumSeconds > total.QueueWaitMaximumSeconds {
			total.QueueWaitMaximumSeconds = metrics.QueueWaitMaximumSeconds
		}
		if metrics.MaximumIdleWithDemandSeconds > total.MaximumIdleWithDemandSeconds {
			total.MaximumIdleWithDemandSeconds = metrics.MaximumIdleWithDemandSeconds
		}
		if metrics.PeakKVTokens > total.PeakKVTokens {
			total.PeakKVTokens = metrics.PeakKVTokens
		}
		if metrics.MaximumRunning > total.MaximumRunning {
			total.MaximumRunning = metrics.MaximumRunning
		}
		if metrics.MaximumQoSBudgetLeases > total.MaximumQoSBudgetLeases {
			total.MaximumQoSBudgetLeases = metrics.MaximumQoSBudgetLeases
		}
	}
	if duration > 0 {
		total.CompletionTokensPerSecond = total.CompletionTokens / duration
		total.SLOCompletionTokensPerSecond = total.SLOCompletionTokens / duration
	}
	if total.DecodeSequenceSeconds > 0 {
		total.MeanActiveTPS = total.CompletionTokens / total.DecodeSequenceSeconds
	}
	return total, duration
}

func RunTPSDebtSuite() (TPSDebtSuite, error) {
	policies := tpsDebtPolicies()
	suite := TPSDebtSuite{
		Reference:             TPSDebtSimulationReference,
		PollInterval:          simulationPollInterval,
		DenominatorExperiment: "unchanged_current_sequence_seconds",
		Policies:              policies,
	}
	for _, scenario := range tpsDebtScenarios() {
		profile, err := simulationCapabilityProfile(scenario, scenarioMaxModelLen(scenario))
		if err != nil {
			return TPSDebtSuite{}, fmt.Errorf("construct scenario %s capability: %w", scenario.name, err)
		}
		result := TPSDebtScenarioResult{
			Name:              scenario.name,
			Category:          scenario.category,
			DurationSeconds:   scenario.duration.Seconds(),
			CapabilityProfile: profile,
			Policies:          make(map[TPSDebtPolicyName]Metrics, len(policies)),
		}
		for _, policy := range policies {
			metrics, calls, runErr := runScenarioWithTPSDebtHorizon(
				scenario,
				PolicyCandidate,
				profile,
				TPSDebtSimulationReference,
				policy.ControlHorizon,
			)
			if runErr != nil {
				return TPSDebtSuite{}, fmt.Errorf("scenario %s policy %s: %w", scenario.name, policy.Name, runErr)
			}
			result.Policies[policy.Name] = metrics
			suite.ControllerPolicyCalls += calls
		}
		suite.Scenarios = append(suite.Scenarios, result)
	}
	return suite, nil
}

func tpsDebtPolicies() []TPSDebtPolicy {
	return []TPSDebtPolicy{
		{Name: TPSDebtPolicyDeclaredLifetime},
		{Name: TPSDebtPolicyBounded1Second, ControlHorizon: time.Second},
		{Name: TPSDebtPolicyBounded2Seconds, ControlHorizon: 2 * time.Second},
		{Name: TPSDebtPolicyBounded5Seconds, ControlHorizon: 5 * time.Second},
		{Name: TPSDebtPolicyBounded10Seconds, ControlHorizon: 10 * time.Second},
		{Name: TPSDebtPolicyBounded20Seconds, ControlHorizon: 20 * time.Second},
		{Name: TPSDebtPolicyBounded30Seconds, ControlHorizon: 30 * time.Second},
	}
}

func tpsDebtScenarios() []scenarioSpec {
	return []scenarioSpec{
		newTPSDebtScenario("mature-surplus-2s", "horizon-sensitivity", 30*time.Second,
			tpsDebtRequest("surplus-2s", 2100*time.Millisecond, 273, 95_000, false)),
		newTPSDebtScenario("mature-surplus-5s", "horizon-sensitivity", 35*time.Second,
			tpsDebtRequest("surplus-5s", 5100*time.Millisecond, 273, 95_000, false)),
		newTPSDebtScenario("declared-95k-actual-273", "declared-short", 40*time.Second,
			tpsDebtRequest("declared-short", 12*time.Second, 273, 95_000, false)),
		newTPSDebtScenario("unknown-limit-actual-273", "unknown-short", 40*time.Second,
			tpsDebtRequest("unknown-short", 12*time.Second, 273, 0, true)),
		newTPSDebtScenario("actual-output-beyond-soft-horizon", "long-running", 125*time.Second,
			tpsDebtRequest("long-running", 35*time.Second, 1_500, 95_000, false)),
		newTPSDebtScenario("one-marginal-lease-burst", "burst", 40*time.Second,
			tpsDebtRequest("burst-a", 12*time.Second, 273, 95_000, false),
			tpsDebtRequest("burst-b", 12*time.Second, 273, 95_000, false),
			tpsDebtRequest("burst-c", 12*time.Second, 273, 95_000, false),
			tpsDebtRequest("burst-d", 12*time.Second, 273, 95_000, false)),
		newTPSDebtScenario("completion-before-next-poll-debt", "terminal", 25*time.Second,
			tpsDebtRequest("pre-poll-complete", 12100*time.Millisecond, 3, 95_000, false),
			tpsDebtRequest("pre-cover-replacement", 12300*time.Millisecond, 3, 95_000, false),
			tpsDebtRequest("post-cover-replacement", 12600*time.Millisecond, 3, 95_000, false)),
		newTPSDebtTerminalScenario("bounded-debt-cancel", requestTerminalCancel),
		newTPSDebtTerminalScenario("bounded-debt-error", requestTerminalError),
		newTPSDebtTerminalScenario("bounded-debt-disconnect", requestTerminalDisconnect),
		newTPSDebtWaitingScenario(),
		newTPSDebtSustainedWaitingScenario(),
		newTPSDebtPreemptionScenario(),
		newTPSDebtStaleRecoveryScenario(),
		newTPSDebtBackendResetScenario(),
		newTPSDebtDistributionShiftScenario("short-to-long-shift", false),
		newTPSDebtDistributionShiftScenario("long-to-short-shift", true),
		newTPSDebtLowFlowScenario(),
	}
}

func newTPSDebtScenario(name, category string, duration time.Duration, requests ...requestSpec) scenarioSpec {
	return scenarioSpec{
		name: name, category: category, duration: duration,
		initialKVTokens: 100_000, backgroundRunning: 7,
		capacityTokens: 4 * 1024 * 1024, maxModelLen: 256 * 1024,
		maximumNoWait: 8, aggregateTPSByRunning: tpsDebtThroughputCurve(),
		requests: requests,
	}
}

func tpsDebtRequest(
	id string,
	at time.Duration,
	actualOutput float64,
	outputLimit int64,
	unknown bool,
) requestSpec {
	return requestSpec{
		id: id, at: at,
		selectionInput: 128, estimatedPrefill: 128,
		safetyInput: 256, decodeHorizon: 256,
		actualInput: 192, actualOutput: actualOutput,
		outputLimit: outputLimit, outputLimitUnknown: unknown,
	}
}

func tpsDebtThroughputCurve() []float64 {
	return []float64{0, 30, 60, 88, 112, 130, 142, 150, 156, 160, 163}
}

func newTPSDebtTerminalScenario(name string, kind requestTerminalKind) scenarioSpec {
	first := tpsDebtRequest(name+"-first", 12*time.Second, 10_000, 95_000, false)
	first.terminalAfter = 3 * time.Second
	first.terminalKind = kind
	return newTPSDebtScenario(name, "terminal", 30*time.Second,
		first,
		tpsDebtRequest(name+"-replacement", 16*time.Second, 273, 95_000, false),
	)
}

func newTPSDebtWaitingScenario() scenarioSpec {
	scenario := newTPSDebtScenario("bounded-debt-waiting-recovery", "waiting", 30*time.Second,
		tpsDebtRequest("waiting-protected", 12100*time.Millisecond, 273, 95_000, false),
		tpsDebtRequest("waiting-recovered", 13100*time.Millisecond, 273, 95_000, false),
	)
	scenario.forcedWaiting = []timeWindow{{start: 12 * time.Second, end: 13 * time.Second}}
	return scenario
}

func newTPSDebtSustainedWaitingScenario() scenarioSpec {
	scenario := newTPSDebtScenario("bounded-debt-sustained-waiting", "waiting", 35*time.Second,
		tpsDebtRequest("sustained-waiting-protected-a", 12100*time.Millisecond, 273, 95_000, false),
		tpsDebtRequest("sustained-waiting-protected-b", 14*time.Second, 273, 95_000, false),
		tpsDebtRequest("sustained-waiting-recovered", 16100*time.Millisecond, 273, 95_000, false),
	)
	scenario.forcedWaiting = []timeWindow{{start: 12 * time.Second, end: 16 * time.Second}}
	return scenario
}

func newTPSDebtPreemptionScenario() scenarioSpec {
	scenario := newTPSDebtScenario("bounded-debt-preemption-recovery", "preemption", 30*time.Second,
		tpsDebtRequest("preemption-protected", 12600*time.Millisecond, 273, 95_000, false),
		tpsDebtRequest("preemption-recovered", 13600*time.Millisecond, 273, 95_000, false),
	)
	scenario.preemptionAt = 12 * time.Second
	scenario.preemptionCooldown = []timeWindow{{start: 12 * time.Second, end: 13 * time.Second}}
	return scenario
}

func newTPSDebtStaleRecoveryScenario() scenarioSpec {
	scenario := newTPSDebtScenario("bounded-debt-stale-recovery", "stale", 30*time.Second,
		tpsDebtRequest("stale-protected", 12100*time.Millisecond, 273, 95_000, false),
		tpsDebtRequest("stale-recovered", 13600*time.Millisecond, 273, 95_000, false),
	)
	scenario.staleMetrics = []timeWindow{{start: 12 * time.Second, end: 13 * time.Second}}
	return scenario
}

func newTPSDebtBackendResetScenario() scenarioSpec {
	recovery := tpsDebtRequest("epoch-reset-recovery", 16100*time.Millisecond, 16, 16, false)
	recovery.decodeHorizon = 16
	scenario := newTPSDebtScenario("bounded-debt-backend-epoch-reset", "epoch-reset", 25*time.Second,
		tpsDebtRequest("epoch-reset-dropped", 12*time.Second, 1_500, 95_000, false),
		recovery,
	)
	scenario.backendResetAt = 14 * time.Second
	return scenario
}
func newTPSDebtDistributionShiftScenario(name string, longFirst bool) scenarioSpec {
	short := tpsDebtRequest(name+"-short", 55*time.Second, 273, 95_000, false)
	long := tpsDebtRequest(name+"-long", 30*time.Second, 1_500, 95_000, false)
	if !longFirst {
		short.at = 30 * time.Second
		long.at = 55 * time.Second
	}
	return newTPSDebtScenario(name, "distribution-shift", 150*time.Second, long, short)
}

func newTPSDebtLowFlowScenario() scenarioSpec {
	return scenarioSpec{
		name: "bounded-debt-low-flow", category: "low-flow", duration: 25 * time.Second,
		initialKVTokens: 100_000, capacityTokens: 4 * 1024 * 1024,
		maxModelLen: 256 * 1024, maximumNoWait: 4,
		aggregateTPSByRunning: tpsDebtThroughputCurve(),
		requests: []requestSpec{
			tpsDebtRequest("low-flow-a", 100*time.Millisecond, 64, 0, true),
			tpsDebtRequest("low-flow-b", 6*time.Second, 64, 0, true),
		},
	}
}
