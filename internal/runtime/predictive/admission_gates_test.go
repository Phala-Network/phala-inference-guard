package predictive

import "testing"

func TestResourceSafetyGateOwnsFreshnessInputCeilingOverflowAndHardKV(t *testing.T) {
	gate, err := NewResourceSafetyGate(ResourceSafetyGateConfig{
		HardKVLimitTokens:            8_992,
		BlockSize:                    16,
		MaximumAdmissibleInputTokens: 8_736,
	})
	if err != nil {
		t.Fatalf("NewResourceSafetyGate: %v", err)
	}
	base := ResourceSafetyGateInput{
		MetricsFresh:          true,
		IdentityValid:         true,
		CapacityTokens:        10_000,
		UsedTokens:            8_000,
		RequestReservedTokens: 992,
		SafetyInputTokens:     512,
	}
	fit := gate.Evaluate(base)
	if !fit.Fits || fit.Reason != RequestAwareReasonOpen || fit.PostAdmitKV != 8_992 || fit.RemainingKV != 992 {
		t.Fatalf("hard-boundary resource result=%+v", fit)
	}
	overKV := base
	overKV.RequestReservedTokens++
	if result := gate.Evaluate(overKV); result.Fits || result.Reason != RequestAwareReasonKV {
		t.Fatalf("over-hard resource result=%+v, want KV protection", result)
	}
	overInput := base
	overInput.SafetyInputTokens = 8_737
	if result := gate.Evaluate(overInput); result.Fits || result.Reason != RequestAwareReasonInputLimit {
		t.Fatalf("over-input resource result=%+v, want input-limit protection", result)
	}
	stale := base
	stale.MetricsFresh = false
	if result := gate.Evaluate(stale); result.Fits || result.Reason != RequestAwareReasonStale {
		t.Fatalf("stale resource result=%+v, want stale protection", result)
	}
	overflow := base
	overflow.ReservedTokens = 1<<63 - 1
	if result := gate.Evaluate(overflow); result.Fits || result.Reason != RequestAwareReasonInvalid {
		t.Fatalf("overflow resource result=%+v, want invalid protection", result)
	}
}

func TestPrefillQoSGateUsesContentionAsRegimeNotGlobalLock(t *testing.T) {
	gate, err := NewPrefillQoSGate(PrefillQoSGateConfig{
		PrefillRegularTokens:         64,
		PrefillExclusiveTokens:       256,
		PrefillQuiescentTokens:       512,
		PrefillContendedBudgetTokens: 64,
		PrefillAggregateBudgetTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewPrefillQoSGate: %v", err)
	}

	regular := gate.Evaluate(PrefillQoSGateInput{
		EstimatedPrefillTokens: 49,
		RawRunning:             4,
	})
	if !regular.Admit || !regular.Contended || regular.Reason != RequestAwareReasonOpen {
		t.Fatalf("contended regular result=%+v, want bounded admission", regular)
	}
	weighted := gate.Evaluate(PrefillQoSGateInput{
		EstimatedPrefillTokens: 96,
		PreemptionObserved:     true,
	})
	if weighted.Admit || weighted.HardProtection || !weighted.Contended ||
		weighted.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("preemption-selected weighted result=%+v, want ordinary size protection", weighted)
	}
	openRegular := gate.Evaluate(PrefillQoSGateInput{
		EstimatedPrefillTokens:         8,
		PendingPrefillSequences:        1,
		PendingPrefillTokens:           100,
		PendingUnknownPrefillSequences: 0,
	})
	if !openRegular.Admit || openRegular.Contended || openRegular.PostAdmitPendingPrefillTokens != 108 {
		t.Fatalf("open regular behind weighted result=%+v, want aggregate admission", openRegular)
	}
}

func TestRequestAwarePolicyComposesResourceSafetyBeforePrefillQoS(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.UsedTokens = 8_990
	input.RequestReservedTokens = 32
	input.SelectionInputTokens = DefaultRequestAwarePrefillQuiescentTokens
	input.SafetyInputTokens = input.SelectionInputTokens
	input.EstimatedPrefillTokens = input.SelectionInputTokens
	input.Running = 10
	input.EffectiveSequences = 10

	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareHardProtect || decision.Reason != RequestAwareReasonInputLimit {
		t.Fatalf("composed decision=%+v, want resource input ceiling before QoS", decision)
	}
}
