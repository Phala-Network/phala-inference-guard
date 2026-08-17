package admission

import (
	"testing"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestPolicyKeepsMinimumRequestOpenAfterContended96KProtection(t *testing.T) {
	policy := testPolicy(t)
	state := ProjectedState{ObservedKVTokens: 1_000_000, EffectiveKVTokens: 1_000_000, RawRunning: 20}

	large := policy.evaluate(state, testWork(t, 96*1024, 144*1024, 256))
	if large.action != ActionProtect || large.reason != ReasonPrefillContention ||
		large.scope != ProtectionRequest || large.prefillClass != PrefillWeighted {
		t.Fatalf("96K contention decision=%+v", large)
	}

	small := policy.evaluate(state, testWork(t, 1024, 1536, 256))
	if small.action != ActionAdmit || small.reason != ReasonOpen || small.scope != ProtectionNone {
		t.Fatalf("1K decision after large protection=%+v", small)
	}
}

func TestPolicyWaitingDoesNotSelfLockMinimumRequest(t *testing.T) {
	policy := testPolicy(t)
	state := ProjectedState{ObservedKVTokens: 1_000_000, EffectiveKVTokens: 1_000_000, RawWaiting: 7}
	decision := policy.evaluate(state, policy.minimumWork)
	if decision.action != ActionAdmit || decision.reason != ReasonOpen {
		t.Fatalf("minimum request under unknown waiting=%+v", decision)
	}
}

func TestPolicySeparatesContextKVAndPrefillGates(t *testing.T) {
	policy := testPolicy(t)

	context := policy.evaluate(ProjectedState{}, testWork(t, 900_000, 900_000, 256))
	if context.action != ActionProtect || context.reason != ReasonInputLimit || context.scope != ProtectionRequest {
		t.Fatalf("context decision=%+v", context)
	}
	if context.postAdmitKVTokens <= 0 || context.pendingPrefillTokensAfter != 900_000 {
		t.Fatalf("context protection lost counterfactual forecast=%+v", context)
	}

	kvState := ProjectedState{ObservedKVTokens: 7_999_800, EffectiveKVTokens: 7_999_800}
	kv := policy.evaluate(kvState, testWork(t, 32*1024, 64*1024, 256))
	if kv.action != ActionProtect || kv.reason != ReasonKVCapacity || kv.scope != ProtectionLoad {
		t.Fatalf("KV decision=%+v", kv)
	}

	prefillState := ProjectedState{PendingPrefillTokens: 256 * 1024, PendingPrefillSequences: 4}
	prefill := policy.evaluate(prefillState, testWork(t, 16*1024, 24*1024, 256))
	if prefill.action != ActionProtect || prefill.reason != ReasonPrefillBudget ||
		prefill.scope != ProtectionLoad {
		t.Fatalf("Prefill decision=%+v", prefill)
	}
}

func TestPolicyEnforcesExclusiveAndQuiescentOwnership(t *testing.T) {
	policy := testPolicy(t)

	exclusive := policy.evaluate(ProjectedState{}, testWork(t, 300*1024, 450*1024, 256))
	if exclusive.action != ActionAdmit || exclusive.prefillClass != PrefillExclusive {
		t.Fatalf("exclusive open decision=%+v", exclusive)
	}
	blockedByOwner := policy.evaluate(ProjectedState{
		PendingPrefillTokens:      300 * 1024,
		PendingPrefillSequences:   1,
		PendingExclusiveSequences: 1,
	}, testWork(t, 1024, 1536, 256))
	if blockedByOwner.action != ActionProtect || blockedByOwner.reason != ReasonPrefillExclusive ||
		blockedByOwner.scope != ProtectionLoad {
		t.Fatalf("decision behind exclusive owner=%+v", blockedByOwner)
	}

	quiescentBusy := policy.evaluate(ProjectedState{RawRunning: 1}, testWork(t, 600*1024, 900*1024, 256))
	if quiescentBusy.action != ActionProtect || quiescentBusy.reason != ReasonPrefillContention ||
		quiescentBusy.prefillClass != PrefillQuiescent {
		t.Fatalf("quiescent busy decision=%+v", quiescentBusy)
	}
}

func TestPolicyPreservesPhysicalGateReasonPrecedenceOverTPS(t *testing.T) {
	policy := testPolicy(t)
	state := ProjectedState{
		RawRunning: 100,
		TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20,
			QualifiedSamples: 4, QualifiedSequenceSeconds: 100,
			AggregateTPS: 20, MeanActiveTPS: 1,
		},
	}
	decision := policy.evaluate(state, testWork(t, 900_000, 900_000, 256))
	if decision.reason != ReasonInputLimit || decision.scope != ProtectionRequest {
		t.Fatalf("TPS changed request-intrinsic Context protection: %+v", decision)
	}
}

func testPolicy(t *testing.T) admissionPolicy {
	t.Helper()
	capability := testCapability()
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	policy, err := newAdmissionPolicy(capability)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func testCapability() Capability {
	return Capability{
		Fingerprint:                  "test-capability",
		MaxModelLenTokens:            1_048_576,
		KVCapacityTokens:             10_000_000,
		KVBlockSize:                  64,
		KVHardLimitTokens:            8_000_000,
		MaximumInputTokens:           800_000,
		MinimumDecodeHorizonTokens:   256,
		PrefillRegularTokens:         64 * 1024,
		PrefillExclusiveTokens:       256 * 1024,
		PrefillQuiescentTokens:       512 * 1024,
		PrefillContendedBudgetTokens: 64 * 1024,
		PrefillAggregateBudgetTokens: 256 * 1024,
	}
}

func testWork(t *testing.T, selection, reservation, decode int64) predictive.RequestWork {
	t.Helper()
	work, err := predictive.BuildRequestWork(predictive.RequestEstimate{
		SelectionInputTokens:     selection,
		KVReservationInputTokens: reservation,
		DecodeHorizonTokens:      decode,
	}, testCapability().KVBlockSize)
	if err != nil {
		t.Fatal(err)
	}
	return work
}
