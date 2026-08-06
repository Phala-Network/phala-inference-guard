package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type staticRequestAwareSnapshot struct {
	input runtimepredictive.RequestAwareInput
}

func (s staticRequestAwareSnapshot) RequestAwareInput(time.Time) runtimepredictive.RequestAwareInput {
	return s.input
}

func TestRequestAwareAdapterRequestSizeChangesPreForwardDecision(t *testing.T) {
	smallAdapter, smallManager := newRequestAwareAdapterTestFixture(t, 5_000, 1)
	largeAdapter, largeManager := newRequestAwareAdapterTestFixture(t, 5_000, 1)

	small := smallAdapter.Decide(context.Background(), "small", requestAwareAdapterInput(500, 100))
	large := largeAdapter.Decide(context.Background(), "large", requestAwareAdapterInput(1_500, 100))
	if small.Outcome != predictiveAdmissionOutcomeForward || small.Reservation == nil {
		t.Fatalf("small pre-forward decision=%+v, want forward with reservation", small)
	}
	if large.Outcome != predictiveAdmissionOutcomeLoadProtection || large.Reservation != nil ||
		large.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("large pre-forward decision=%+v, want size load protection", large)
	}
	if smallManager.Snapshot().Reservations != 1 || largeManager.Snapshot().Reservations != 0 {
		t.Fatalf("reservation causality small=%d large=%d", smallManager.Snapshot().Reservations, largeManager.Snapshot().Reservations)
	}
}

func TestRequestAwareAdapterOpenForwardsHardFitLargeRequest(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	decision := adapter.Decide(context.Background(), "open-large", requestAwareAdapterInput(2_500, 500))
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil || manager.Snapshot().Reservations != 1 {
		t.Fatalf("open large decision=%+v reservations=%d", decision, manager.Snapshot().Reservations)
	}
}

func TestRequestAwareAdapterWaitingStillInspectsRequestSize(t *testing.T) {
	smallAdapter, _ := newRequestAwareAdapterTestFixture(t, 5_000, 1)
	largeAdapter, _ := newRequestAwareAdapterTestFixture(t, 5_000, 1)
	small := smallAdapter.Decide(context.Background(), "waiting-small", requestAwareAdapterInput(500, 100))
	large := largeAdapter.Decide(context.Background(), "waiting-large", requestAwareAdapterInput(3_500, 0))
	if small.Outcome != predictiveAdmissionOutcomeForward || large.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("waiting pre-forward decisions small=%+v large=%+v", small, large)
	}
}

func TestRequestAwareAdapterReservationUsesExistingLifecycle(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	decision := adapter.Decide(context.Background(), "lifecycle", requestAwareAdapterInput(500, 100))
	if decision.Reservation == nil || !decision.Reservation.MarkForwarded() ||
		!decision.Reservation.MarkPrefillComplete() ||
		!decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatalf("adapter lifecycle decision=%+v", decision)
	}
	if manager.Snapshot().Reservations != 0 {
		t.Fatalf("adapter lifecycle leaked reservation: %+v", manager.Snapshot())
	}
}

func TestRequestAwareAdapterFreshCompletionSnapshotDoesNotMislockIdleBackend(t *testing.T) {
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper:     2_000,
		ActiveKVUpper:       2_000,
		ActiveContextTokens: 2_000,
	}, domainpredictive.Constraints{}, nil)
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		SoftKVRatio: 0.60,
		HardKVRatio: 0.90,
		TPSTarget:   20,
		TPSFloor:    15,
		BlockSize:   16,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager: manager,
		Policy:  policy,
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     10_000,
			Running:            0,
			EffectiveSequences: 0,
			AggregateTPSProxy:  40,
			MeanActiveTPSProxy: 20,
			TPSValid:           true,
		}},
		ManifestID: "request-aware-http-test",
		BlockSize:  16,
		Mode:       "enforce",
		Now:        func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("newRequestAwarePredictiveAdapter: %v", err)
	}

	decision := adapter.Decide(context.Background(), "idle-after-completion", requestAwareAdapterInput(2_500, 500))
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil {
		t.Fatalf("idle completion-window adapter decision=%+v, want forward with reservation", decision)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry().RequestAware
	if telemetry.Action != runtimepredictive.RequestAwareAdmit || telemetry.Reason != runtimepredictive.RequestAwareReasonOpen ||
		telemetry.Pressure != 0 || telemetry.TPSForecastValid {
		t.Fatalf("idle completion-window telemetry=%+v, want TPS-neutral admit/open", telemetry)
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("idle completion-window reservation did not release")
	}
}

func TestRequestAwareAdapterCloseBeforeForwardCommitRejectsAndReleasesReservation(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	snapshot := adapter.snapshot.(staticRequestAwareSnapshot).input
	snapshot.CapacityTokens = 4 * 1024 * 1024
	snapshot.TPSValid = false
	adapter.snapshot = staticRequestAwareSnapshot{input: snapshot}
	input := requestAwareAdapterInput(690*1024, 0)
	input.Cost.ApproximateInputTokens = 285 * 1024
	decision := adapter.Decide(context.Background(), "close-before-forward", input)
	if decision.Reservation == nil || manager.Snapshot().Reservations != 1 {
		t.Fatalf("setup decision=%+v manager=%+v", decision, manager.Snapshot())
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close request-aware adapter: %v", err)
	}
	if decision.Reservation.MarkForwarded() {
		t.Fatal("reservation committed forward after adapter Close linearized first")
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) ||
		decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("close-rejected divergent reservation did not release exact once through terminal lifecycle")
	}
	managerSnapshot := manager.Snapshot()
	if managerSnapshot.IntakeOpen || managerSnapshot.Reservations != 0 {
		t.Fatalf("close-before-forward lifecycle leaked or reopened intake: %+v", managerSnapshot)
	}
}

func TestRequestAwareAdapterUnknownLexicalHintFallsBackToSafetyUpper(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 0, 0)
	snapshot := adapter.snapshot.(staticRequestAwareSnapshot).input
	snapshot.CapacityTokens = 4 * 1024 * 1024
	snapshot.TPSValid = false
	adapter.snapshot = staticRequestAwareSnapshot{input: snapshot}
	input := requestAwareAdapterInput(650*1024, 0)
	input.Cost.ApproximateInputTokens = 0
	input.Cost.ApproximateInputTokensKnown = false

	decision := adapter.Decide(context.Background(), "unknown-lexical-hint", input)
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reservation != nil ||
		telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		telemetry.RequestAware.PrefillClass != runtimepredictive.RequestAwarePrefillQuiescent ||
		telemetry.RequestAware.SelectionInputTokens != 650*1024 ||
		telemetry.RequestAware.EstimatedPrefillTokens != 650*1024 ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("unknown lexical fallback decision/telemetry/manager=%+v/%+v/%+v", decision, telemetry, manager.Snapshot())
	}
}

func TestRequestAwareAdapterShadowIsSideEffectFree(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixtureWithMode(t, 5_000, 1, "shadow")
	before := manager.Snapshot()
	small := adapter.Decide(context.Background(), "shadow-small", requestAwareAdapterInput(500, 100))
	large := adapter.Decide(context.Background(), "shadow-large", requestAwareAdapterInput(1_500, 100))
	after := manager.Snapshot()
	if small.Outcome != predictiveAdmissionOutcomeForward || small.Reservation != nil {
		t.Fatalf("shadow small decision=%+v, want forward without reservation", small)
	}
	if large.Outcome != predictiveAdmissionOutcomeLoadProtection || large.Reservation != nil ||
		large.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("shadow large decision=%+v, want would-protect without reservation", large)
	}
	if after.Reservations != before.Reservations || after.EventSequence != before.EventSequence || after.Virtual != before.Virtual {
		t.Fatalf("shadow changed Manager state: before=%+v after=%+v", before, after)
	}
}

func TestRequestAwareAdapterTelemetryPublishesSelectiveVerdictAndInspectCapacity(t *testing.T) {
	adapter, _ := newRequestAwareAdapterTestFixture(t, 5_000, 1)
	decision := adapter.Decide(context.Background(), "telemetry-large", requestAwareAdapterInput(1_500, 100))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("setup decision=%+v, want size protection", decision)
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.Attempts.Attempts != 1 || snapshot.Attempts.Risks != 1 ||
		snapshot.Attempts.LastRejectReason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("selective attempts telemetry=%+v", snapshot.Attempts)
	}
	if snapshot.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		snapshot.RequestAware.Reason != runtimepredictive.RequestAwareReasonRequestSize ||
		snapshot.RequestAware.PressureSource != runtimepredictive.RequestAwarePressureTPS ||
		snapshot.RequestAware.SelectionInputTokens != 1_500 || snapshot.RequestAware.ReservedTokens != 1_600 ||
		snapshot.RequestAware.EffectiveKV != 5_000 || snapshot.RequestAware.PostAdmitKV != 6_600 ||
		snapshot.RequestAware.RemainingKV != 3_992 || snapshot.RequestAware.AllowanceTokens != 598 ||
		snapshot.RequestAware.Running != 4 || snapshot.RequestAware.Waiting != 1 ||
		snapshot.RequestAware.EffectiveSequences != 4 || snapshot.RequestAware.AggregateTPSProxy != 80 ||
		snapshot.RequestAware.MeanActiveTPSProxy != 20 || snapshot.RequestAware.ProjectedTPSProxy != 16 ||
		!snapshot.RequestAware.TPSForecastValid {
		t.Fatalf("selective request-aware telemetry=%+v", snapshot.RequestAware)
	}
	if !snapshot.RouterBackpressure.Active || snapshot.RouterBackpressure.InspectCapacity != 1 ||
		snapshot.RouterBackpressure.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("selective Router telemetry=%+v, want active inspect capacity 1", snapshot.RouterBackpressure)
	}
}

func TestRequestAwareAdapterTelemetryPublishesHardProtectionWithZeroInspectCapacity(t *testing.T) {
	adapter, _ := newRequestAwareAdapterTestFixture(t, 8_990, 0)
	decision := adapter.Decide(context.Background(), "telemetry-hard", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("setup decision=%+v, want hard KV protection", decision)
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.RequestAware.Action != runtimepredictive.RequestAwareHardProtect ||
		!snapshot.RouterBackpressure.Active || snapshot.RouterBackpressure.InspectCapacity != 0 ||
		snapshot.RouterBackpressure.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("hard protection telemetry request=%+v Router=%+v", snapshot.RequestAware, snapshot.RouterBackpressure)
	}
}

func TestRequestAwareAdapterTelemetryProbeRecoversWithoutNewBusinessRequest(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 8_990, 0)
	decision := adapter.Decide(context.Background(), "telemetry-recovery", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("setup decision=%+v, want hard protection", decision)
	}
	beforeProbe := manager.Snapshot()
	protected := adapter.PredictiveAdmissionTelemetry()
	afterProbe := manager.Snapshot()
	if !protected.RouterBackpressure.Active || protected.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("protected Router telemetry=%+v, want hard clamp", protected.RouterBackpressure)
	}
	if beforeProbe.Reservations != afterProbe.Reservations || beforeProbe.EventSequence != afterProbe.EventSequence ||
		beforeProbe.Virtual != afterProbe.Virtual || protected.Attempts.Attempts != 1 {
		t.Fatalf("telemetry probe changed business state: before=%+v after=%+v attempts=%+v", beforeProbe, afterProbe, protected.Attempts)
	}

	if err := manager.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:     5_000,
			ActiveKVUpper:       5_000,
			DecodeSequences:     4,
			ActiveContextTokens: 5_000,
		},
		StartedSequence:  beforeProbe.EventSequence,
		FinishedSequence: beforeProbe.EventSequence,
	}); err != nil {
		t.Fatalf("reconcile recovered state: %v", err)
	}
	recovered := adapter.PredictiveAdmissionTelemetry()
	if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("recovered Router telemetry=%+v, want immediate OPEN without another request", recovered.RouterBackpressure)
	}
	if recovered.Attempts.Attempts != 1 || manager.Snapshot().Reservations != 0 || manager.Snapshot().EventSequence != beforeProbe.EventSequence {
		t.Fatalf("recovery probe mutated business accounting: telemetry=%+v manager=%+v", recovered.Attempts, manager.Snapshot())
	}
}

func TestRequestAwareAdapterConcurrentTelemetryAndAdmissionRecoversFreshOpen(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	const decisionCount = 128
	const scraperCount = 8
	const scrapesPerWorker = 256

	start := make(chan struct{})
	errors := make(chan error, decisionCount)
	var wait sync.WaitGroup
	for worker := 0; worker < scraperCount; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for scrape := 0; scrape < scrapesPerWorker; scrape++ {
				_ = adapter.PredictiveAdmissionTelemetry()
			}
		}()
	}
	for index := 0; index < decisionCount; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decision := adapter.Decide(
				context.Background(),
				fmt.Sprintf("telemetry-race-%d", index),
				requestAwareAdapterInput(64, 16),
			)
			if decision.Reservation == nil {
				return
			}
			if !decision.Reservation.MarkForwarded() {
				errors <- fmt.Errorf("reservation %d did not commit forward", index)
				return
			}
			if !decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
				errors <- fmt.Errorf("reservation %d did not terminate", index)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	managerSnapshot := manager.Snapshot()
	if managerSnapshot.Reservations != 0 || managerSnapshot.Virtual.Upper.ActiveKVUpper != 5_000 ||
		managerSnapshot.Virtual.Upper.DecodeSequences != 4 {
		t.Fatalf("concurrent scrape/admission lifecycle leaked virtual capacity: %+v", managerSnapshot)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.Attempts.Attempts != decisionCount {
		t.Fatalf("business attempts=%d, want %d without scrape accounting", telemetry.Attempts.Attempts, decisionCount)
	}
	if telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("fresh post-race scrape retained a sticky Router clamp: %+v", telemetry.RouterBackpressure)
	}
}

func TestRequestAwareAdapterShadowNeverPublishesRouterClamp(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixtureWithMode(t, 8_990, 0, "shadow")
	decision := adapter.Decide(context.Background(), "telemetry-shadow", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("setup shadow decision=%+v, want would-protect", decision)
	}
	before := manager.Snapshot()
	telemetry := adapter.PredictiveAdmissionTelemetry()
	after := manager.Snapshot()
	if telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("shadow Router telemetry=%+v, want no clamp", telemetry.RouterBackpressure)
	}
	if before.Reservations != after.Reservations || before.EventSequence != after.EventSequence || before.Virtual != after.Virtual {
		t.Fatalf("shadow telemetry changed Manager: before=%+v after=%+v", before, after)
	}
}

func TestRequestAwareAdapterProtectionLogUsesExactVerdictAndBoundsDuplicates(t *testing.T) {
	adapter, _ := newRequestAwareAdapterTestFixture(t, 5_000, 1)
	now := time.Unix(2_000, 0)
	adapter.now = func() time.Time { return now }
	adapter.decisionLogInterval = time.Second
	var events []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		events = append(events, event)
	}
	for _, requestID := range []string{"log-large-1", "log-large-2"} {
		decision := adapter.Decide(context.Background(), requestID, requestAwareAdapterInput(1_500, 100))
		if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
			t.Fatalf("%s decision=%+v, want protection", requestID, decision)
		}
	}
	if len(events) != 1 {
		t.Fatalf("immediate duplicate protection logs=%d, want one", len(events))
	}
	now = now.Add(time.Second)
	adapter.Decide(context.Background(), "log-large-3", requestAwareAdapterInput(1_500, 100))
	if len(events) != 2 || events[1].Suppressed != 1 {
		t.Fatalf("bounded protection events=%+v, want second event with one suppression", events)
	}
	line := requestAwareDecisionLogLine(events[1])
	for _, want := range []string{
		"event=admission_decision",
		"mode=enforce",
		"enforced=true",
		"action=size_protect",
		"reason=request_size",
		"http_reason=request_size_at_pressure",
		"scope=load",
		"pressure_source=tps",
		"pressure=0.800000",
		"selection_input_tokens=1500",
		"reserved_tokens=1600",
		"allowance_tokens=598",
		"effective_kv=5000",
		"post_admit_kv=6600",
		"remaining_kv=3992",
		"running=4",
		"waiting=1",
		"effective_sequences=4",
		"aggregate_tps_proxy=80.000000",
		"mean_active_tps_proxy=20.000000",
		"projected_tps_proxy=16.000000",
		"tps_forecast_valid=true",
		"suppressed=1",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("request-aware decision log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"request_id=", "model=", "prompt=", "body=", "bearer="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("request-aware decision log contains forbidden field %q: %s", forbidden, line)
		}
	}
}

func newRequestAwareAdapterTestFixture(t testing.TB, usedTokens int64, waiting int) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	return newRequestAwareAdapterTestFixtureWithMode(t, usedTokens, waiting, "enforce")
}

func newRequestAwareAdapterTestFixtureWithMode(t testing.TB, usedTokens int64, waiting int, mode string) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	t.Helper()
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper:     usedTokens,
		ActiveKVUpper:       usedTokens,
		DecodeSequences:     4 + waiting,
		ActiveContextTokens: usedTokens,
	}, domainpredictive.Constraints{}, nil)
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		SoftKVRatio: 0.60,
		HardKVRatio: 0.90,
		TPSTarget:   20,
		TPSFloor:    15,
		BlockSize:   16,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager: manager,
		Policy:  policy,
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     10_000,
			Running:            4,
			Waiting:            waiting,
			AggregateTPSProxy:  80,
			MeanActiveTPSProxy: 20,
			TPSValid:           true,
		}},
		ManifestID: "request-aware-http-test",
		BlockSize:  16,
		Mode:       mode,
		Now:        func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("newRequestAwarePredictiveAdapter: %v", err)
	}
	return adapter, manager
}

func requestAwareAdapterInput(inputTokens, decodeTokens int64) predictiveShadowInput {
	return predictiveShadowInput{Cost: kvadmission.Cost{
		Supported:                   true,
		EstimatedInputLow:           inputTokens,
		EstimatedInputHigh:          inputTokens,
		ApproximateInputTokens:      inputTokens,
		ApproximateInputTokensKnown: true,
		BoundedDecodeTokens:         decodeTokens,
	}}
}
