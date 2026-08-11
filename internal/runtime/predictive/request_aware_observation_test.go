package predictive

import (
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestCurrentRequestAwareDecisionUsesAtomicallyPublishedObservation(t *testing.T) {
	now := time.Unix(20_000, 0)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	if err := manager.InitializeRequestAwareObservation(RequestAwareObservation{
		ObservedAt:          now,
		MaximumAge:          time.Minute,
		IdentityValid:       true,
		ObservationSequence: 0,
		CapacityTokens:      1_000,
	}); err != nil {
		t.Fatalf("initialize observation: %v", err)
	}
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens:            896,
		BlockSize:                    16,
		MaximumAdmissibleInputTokens: 640,
		PrefillRegularTokens:         DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       DefaultRequestAwarePrefillQuiescentTokens,
		PrefillContendedBudgetTokens: 640,
		PrefillAggregateBudgetTokens: DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}

	first := manager.DecideCurrentRequestAware(
		now, "first", requestAwareManagerCost(96, 16), 96, policy,
	)
	if first.Decision.Action != RequestAwareAdmit || first.Observation.ObservationSequence != 0 {
		t.Fatalf("initial decision=%+v observation=%+v, want sequence-0 admit", first.Decision, first.Observation)
	}

	started := manager.StartSampleWindow()
	finished := manager.EventSequence()
	observation := RequestAwareObservation{
		ObservedAt:          now.Add(time.Second),
		MaximumAge:          time.Minute,
		IdentityValid:       true,
		ObservationSequence: 1,
		CapacityTokens:      1_000,
		UsedTokens:          800,
		Running:             1,
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper:     800,
			ActiveKVUpper:       800,
			DecodeSequences:     1,
			ActiveContextTokens: 800,
		},
		StartedSequence:         started,
		FinishedSequence:        finished,
		ObservationSequence:     1,
		RequestAwareObservation: &observation,
	}); err != nil {
		t.Fatalf("publish observation: %v", err)
	}

	second := manager.DecideCurrentRequestAware(
		now.Add(time.Second), "second", requestAwareManagerCost(96, 16), 96, policy,
	)
	if second.Decision.Action != RequestAwareHardProtect || second.Decision.Reason != RequestAwareReasonKV ||
		second.Observation.ObservationSequence != 1 || second.Observation.UsedTokens != 800 {
		t.Fatalf("published decision=%+v observation=%+v, want one coherent sequence-1 KV protection",
			second.Decision, second.Observation)
	}
}
