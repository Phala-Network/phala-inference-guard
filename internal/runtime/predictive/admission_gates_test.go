package predictive

import "testing"

func TestResourceGateOwnsFreshnessOverflowAndHardKVFit(t *testing.T) {
	gate, err := NewResourceGate(ResourceGateConfig{
		HardKVLimitTokens: 8_992,
		BlockSize:         16,
	})
	if err != nil {
		t.Fatalf("NewResourceGate: %v", err)
	}
	base := ResourceGateInput{
		MetricsFresh:          true,
		IdentityValid:         true,
		CapacityTokens:        10_000,
		UsedTokens:            8_000,
		RequestReservedTokens: 992,
	}
	fit := gate.Evaluate(base)
	if !fit.Fits || fit.Reason != RequestAwareReasonOpen || fit.PostAdmitKV != 8_992 || fit.RemainingKV != 992 {
		t.Fatalf("hard-boundary resource result=%+v", fit)
	}
	over := base
	over.RequestReservedTokens++
	if result := gate.Evaluate(over); result.Fits || result.Reason != RequestAwareReasonKV {
		t.Fatalf("over-hard resource result=%+v, want KV protection", result)
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

func TestInterferenceGateIsCandidateClassAware(t *testing.T) {
	gate, err := NewInterferenceGate(InterferenceGateConfig{
		PrefillRegularTokens:         64,
		PrefillExclusiveTokens:       256,
		PrefillQuiescentTokens:       512,
		PrefillAggregateBudgetTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewInterferenceGate: %v", err)
	}
	base := InterferenceGateInput{
		PendingPrefillSequences:          1,
		PendingPrefillTokens:             512,
		PendingLongPrefillSequences:      1,
		PendingQuiescentPrefillSequences: 1,
	}
	regular := base
	regular.EstimatedPrefillTokens = 63
	regularResult := gate.Evaluate(regular)
	if regularResult.Admit || regularResult.HardProtection ||
		regularResult.Reason != RequestAwareReasonPrefillBusy ||
		regularResult.PrefillClass != RequestAwarePrefillRegular {
		t.Fatalf("regular behind quiescent result=%+v, want bounded Prefill protection", regularResult)
	}
	exclusive := base
	exclusive.EstimatedPrefillTokens = 256
	exclusiveResult := gate.Evaluate(exclusive)
	if exclusiveResult.Admit || exclusiveResult.HardProtection ||
		exclusiveResult.Reason != RequestAwareReasonPrefillExclusive ||
		exclusiveResult.PrefillClass != RequestAwarePrefillExclusive {
		t.Fatalf("exclusive behind quiescent result=%+v, want size protection", exclusiveResult)
	}
	preemption := InterferenceGateInput{EstimatedPrefillTokens: 64, PreemptionObserved: true}
	preemptionResult := gate.Evaluate(preemption)
	if preemptionResult.Admit || !preemptionResult.HardProtection || preemptionResult.Reason != RequestAwareReasonPreemption {
		t.Fatalf("preemption result=%+v, want hard protection", preemptionResult)
	}
}

func TestRequestAwarePolicyComposesResourceBeforeInterference(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.UsedTokens = 8_990
	input.RequestReservedTokens = 32
	input.EstimatedPrefillTokens = DefaultRequestAwarePrefillQuiescentTokens
	input.SelectionInputTokens = input.EstimatedPrefillTokens
	input.Running = 10
	input.EffectiveSequences = 10

	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareHardProtect || decision.Reason != RequestAwareReasonKV {
		t.Fatalf("composed decision=%+v, want resource KV guard before interference", decision)
	}
}
