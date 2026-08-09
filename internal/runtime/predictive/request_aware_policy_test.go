package predictive

import (
	"testing"
)

func TestRequestAwarePolicyRejectsInvalidFrozenKVLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		hardKV int64
	}{
		{name: "zero hard boundary", hardKV: 0},
		{name: "negative hard boundary", hardKV: -16},
		{name: "unaligned hard boundary", hardKV: 9_001},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRequestAwarePolicy(RequestAwareConfig{
				HardKVLimitTokens:            test.hardKV,
				BlockSize:                    16,
				PrefillRegularTokens:         DefaultRequestAwarePrefillRegularTokens,
				PrefillExclusiveTokens:       DefaultRequestAwarePrefillExclusiveTokens,
				PrefillQuiescentTokens:       DefaultRequestAwarePrefillQuiescentTokens,
				PrefillAggregateBudgetTokens: DefaultRequestAwarePrefillAggregateBudgetTokens,
			})
			if err == nil {
				t.Fatalf("NewRequestAwarePolicy accepted invalid hard KV limit %d", test.hardKV)
			}
		})
	}
}

func TestRequestAwarePolicyDifferentiatesByPrefillWorkUnderSameBackendState(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	small := requestAwareTestInput()
	small.CapacityTokens = 4 * 1024 * 1024
	small.UsedTokens = 128 * 1024
	small.SelectionInputTokens = 8 * 1024
	small.EstimatedPrefillTokens = 8 * 1024
	small.RequestReservedTokens = 16 * 1024

	large := small
	large.SelectionInputTokens = 650 * 1024
	large.EstimatedPrefillTokens = 650 * 1024
	large.RequestReservedTokens = 700 * 1024

	smallDecision := policy.Evaluate(small)
	largeDecision := policy.Evaluate(large)
	if smallDecision.Action != RequestAwareAdmit || largeDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("same-pressure decisions small=%+v large=%+v, want admit/size_protect", smallDecision, largeDecision)
	}
	if largeDecision.Reason != RequestAwareReasonDecodeInterference ||
		largeDecision.PressureSource != RequestAwarePressureDecode ||
		largeDecision.PrefillClass != RequestAwarePrefillQuiescent {
		t.Fatalf("large request protection=%+v, want quiescent Decode protection", largeDecision)
	}
}

func TestRequestAwarePolicyOpenAdmitsHardFitLargeRequest(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.UsedTokens = 2_000
	input.SelectionInputTokens = 2_500
	input.RequestReservedTokens = 3_000

	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareAdmit || decision.Reason != RequestAwareReasonOpen || decision.Pressure != 0 {
		t.Fatalf("open large decision=%+v, want admit/open", decision)
	}
}

func TestRequestAwarePolicyProtectsQuiescentPrefillBeforeFeedback(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.CapacityTokens = 4 * 1024 * 1024
	input.UsedTokens = 128 * 1024
	input.ReservedTokens = 0
	input.RequestReservedTokens = 650 * 1024
	input.SelectionInputTokens = 650 * 1024
	input.Waiting = 0
	input.AggregateTPSProxy = 0
	input.MeanActiveTPSProxy = 0
	input.TPSValid = false

	idle := input
	idle.Running = 0
	idle.EffectiveSequences = 0
	idleDecision := policy.Evaluate(idle)
	if idleDecision.Action != RequestAwareAdmit {
		t.Fatalf("idle 650K decision=%+v, want first quiescent prefill admitted", idleDecision)
	}
	localDecode := idle
	localDecode.EffectiveSequences = 1
	localDecodeDecision := policy.Evaluate(localDecode)
	if localDecodeDecision.Action != RequestAwareSizeProtect ||
		localDecodeDecision.Reason != RequestAwareReasonDecodeInterference ||
		localDecodeDecision.PressureSource != RequestAwarePressureDecode {
		t.Fatalf("local decode plus 650K decision=%+v, want pre-forward Decode protection", localDecodeDecision)
	}

	busy := input
	busy.Running = 20
	busy.EffectiveSequences = 20
	busyDecision := policy.Evaluate(busy)
	if busyDecision.Action != RequestAwareSizeProtect ||
		busyDecision.Reason != RequestAwareReasonDecodeInterference ||
		busyDecision.PressureSource != RequestAwarePressureDecode {
		t.Fatalf("busy 650K decision=%+v, want pre-forward Decode protection before TPS feedback", busyDecision)
	}
}

func TestRequestAwarePolicyPrefillBoundaries(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	base := requestAwareTestInput()
	base.CapacityTokens = 4 * 1024 * 1024
	base.UsedTokens = 0
	base.ReservedTokens = 0
	base.Running = 0
	base.Waiting = 0
	base.EffectiveSequences = 0
	base.AggregateTPSProxy = 0
	base.MeanActiveTPSProxy = 0
	base.TPSValid = false

	for _, test := range []struct {
		name       string
		tokens     int64
		wantClass  RequestAwarePrefillClass
		wantAction RequestAwareAction
	}{
		{name: "below 64K", tokens: 64*1024 - 1, wantClass: RequestAwarePrefillRegular, wantAction: RequestAwareAdmit},
		{name: "at 64K", tokens: 64 * 1024, wantClass: RequestAwarePrefillWeighted, wantAction: RequestAwareAdmit},
		{name: "below 256K", tokens: 256*1024 - 1, wantClass: RequestAwarePrefillWeighted, wantAction: RequestAwareAdmit},
		{name: "at 256K", tokens: 256 * 1024, wantClass: RequestAwarePrefillExclusive, wantAction: RequestAwareAdmit},
		{name: "below 512K", tokens: 512*1024 - 1, wantClass: RequestAwarePrefillExclusive, wantAction: RequestAwareAdmit},
		{name: "at 512K", tokens: 512 * 1024, wantClass: RequestAwarePrefillQuiescent, wantAction: RequestAwareAdmit},
		{name: "at 650K", tokens: 650 * 1024, wantClass: RequestAwarePrefillQuiescent, wantAction: RequestAwareAdmit},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.SelectionInputTokens = test.tokens
			input.EstimatedPrefillTokens = test.tokens
			input.RequestReservedTokens = test.tokens
			decision := policy.Evaluate(input)
			if decision.PrefillClass != test.wantClass || decision.Action != test.wantAction {
				t.Fatalf("boundary decision=%+v, want class/action %s/%s", decision, test.wantClass, test.wantAction)
			}
		})
	}
}

func TestRequestAwarePolicyCapsAggregateRegularPrefillBurstAndBlocksShortBehindKnownLong(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	regularBurst := requestAwareTestInput()
	regularBurst.CapacityTokens = 4 * 1024 * 1024
	regularBurst.UsedTokens = 0
	regularBurst.ReservedTokens = 0
	regularBurst.RequestReservedTokens = 8 * 1024
	regularBurst.SelectionInputTokens = 8 * 1024
	regularBurst.EstimatedPrefillTokens = 8 * 1024
	regularBurst.Running = 0
	regularBurst.EffectiveSequences = 0
	regularBurst.PendingPrefillSequences = 32
	regularBurst.PendingPrefillTokens = 256*1024 - 4*1024
	regularBurst.AggregateTPSProxy = 0
	regularBurst.MeanActiveTPSProxy = 0
	regularBurst.TPSValid = false

	atBoundary := regularBurst
	atBoundary.PendingPrefillTokens = 256*1024 - atBoundary.EstimatedPrefillTokens
	boundary := policy.Evaluate(atBoundary)
	if boundary.Action != RequestAwareAdmit || boundary.Reason != RequestAwareReasonOpen {
		t.Fatalf("regular exact aggregate boundary decision=%+v, want admit/open", boundary)
	}

	protected := policy.Evaluate(regularBurst)
	if protected.Action != RequestAwareSizeProtect || protected.Reason != RequestAwareReasonPrefillBudget ||
		protected.PressureSource != RequestAwarePressurePrefill || protected.PrefillClass != RequestAwarePrefillRegular {
		t.Fatalf("regular aggregate burst decision=%+v, want pre-forward prefill budget protection", protected)
	}

	withExclusive := regularBurst
	withExclusive.PendingPrefillSequences = 1
	withExclusive.PendingPrefillTokens = 300 * 1024
	withExclusive.PendingLongPrefillSequences = 1
	short := policy.Evaluate(withExclusive)
	if short.Action != RequestAwareSizeProtect || short.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("short behind exclusive decision=%+v, want known-Prefill protection", short)
	}
}

func TestV0125RequestAwarePolicyTPSIsTelemetryNotAdmissionAuthority(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	healthy := requestAwareTestInput()
	healthy.UsedTokens = 2_000
	healthy.SelectionInputTokens = 1
	healthy.EstimatedPrefillTokens = 1
	healthy.RequestReservedTokens = 16

	degraded := healthy
	degraded.AggregateTPSProxy = 40
	degraded.MeanActiveTPSProxy = 10

	healthyDecision := policy.Evaluate(healthy)
	degradedDecision := policy.Evaluate(degraded)
	if healthyDecision.Action != RequestAwareAdmit || degradedDecision.Action != RequestAwareAdmit {
		t.Fatalf("TPS-only change altered admission: healthy=%+v degraded=%+v, want both admitted", healthyDecision, degradedDecision)
	}
	if healthyDecision.PressureSource != RequestAwarePressureNone || degradedDecision.PressureSource != RequestAwarePressureNone {
		t.Fatalf("TPS remained admission authority: healthy=%+v degraded=%+v", healthyDecision, degradedDecision)
	}
}

func TestV0125RequestAwarePolicyPendingQuiescentProtectsRegular(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	base := requestAwareTestInput()
	base.CapacityTokens = 4 * 1024 * 1024
	base.UsedTokens = 128 * 1024
	base.PendingPrefillSequences = 1
	base.PendingPrefillTokens = 650 * 1024
	base.PendingLongPrefillSequences = 1
	base.PendingQuiescentPrefillSequences = 1

	regular := base
	regular.SelectionInputTokens = 8 * 1024
	regular.EstimatedPrefillTokens = 8 * 1024
	regular.RequestReservedTokens = 16 * 1024
	regularDecision := policy.Evaluate(regular)
	if regularDecision.Action != RequestAwareSizeProtect || regularDecision.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("regular request behind quiescent=%+v, want known-Prefill protection", regularDecision)
	}

	exclusive := base
	exclusive.SelectionInputTokens = 300 * 1024
	exclusive.EstimatedPrefillTokens = 300 * 1024
	exclusive.RequestReservedTokens = 320 * 1024
	exclusiveDecision := policy.Evaluate(exclusive)
	if exclusiveDecision.Action != RequestAwareSizeProtect ||
		exclusiveDecision.Reason != RequestAwareReasonPrefillExclusive {
		t.Fatalf("exclusive request behind quiescent=%+v, want size protection", exclusiveDecision)
	}
}

func TestV0125RequestAwarePolicyBlocksRegularBehindKnownNonRegularPrefill(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	for _, test := range []struct {
		name             string
		pendingTokens    int64
		pendingQuiescent int
	}{
		{name: "weighted", pendingTokens: 195 * 1024},
		{name: "exclusive", pendingTokens: 300 * 1024},
		{name: "quiescent", pendingTokens: 650 * 1024, pendingQuiescent: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := requestAwareTestInput()
			input.CapacityTokens = 4 * 1024 * 1024
			input.Running = 0
			input.EffectiveSequences = 0
			input.PendingPrefillSequences = 1
			input.PendingPrefillTokens = test.pendingTokens
			input.PendingLongPrefillSequences = 1
			input.PendingQuiescentPrefillSequences = test.pendingQuiescent
			input.SelectionInputTokens = 8 * 1024
			input.EstimatedPrefillTokens = 8 * 1024
			input.RequestReservedTokens = 16 * 1024

			decision := policy.Evaluate(input)
			if decision.Action != RequestAwareSizeProtect ||
				decision.Reason != RequestAwareReasonPrefillBusy ||
				decision.PressureSource != RequestAwarePressurePrefill {
				t.Fatalf("regular behind %s Prefill=%+v, want size_protect/prefill_busy", test.name, decision)
			}
		})
	}

	regularBehindRegular := requestAwareTestInput()
	regularBehindRegular.CapacityTokens = 4 * 1024 * 1024
	regularBehindRegular.Running = 0
	regularBehindRegular.EffectiveSequences = 0
	regularBehindRegular.PendingPrefillSequences = 1
	regularBehindRegular.PendingPrefillTokens = 32 * 1024
	regularBehindRegular.SelectionInputTokens = 8 * 1024
	regularBehindRegular.EstimatedPrefillTokens = 8 * 1024
	regularBehindRegular.RequestReservedTokens = 16 * 1024
	if decision := policy.Evaluate(regularBehindRegular); decision.Action != RequestAwareAdmit {
		t.Fatalf("regular behind regular Prefill=%+v, want work-conserving admission", decision)
	}
}

func TestRequestAwarePolicyRejectsInvalidPrefillThresholdOrdering(t *testing.T) {
	_, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens: 9_008, BlockSize: 16,
		PrefillRegularTokens: 64 * 1024, PrefillExclusiveTokens: 256 * 1024,
		PrefillQuiescentTokens: 512 * 1024, PrefillAggregateBudgetTokens: 128 * 1024,
	})
	if err == nil {
		t.Fatal("NewRequestAwarePolicy accepted aggregate prefill budget below exclusive threshold")
	}
}

func TestRequestAwarePolicyRequiresInitializedPrefillProfile(t *testing.T) {
	_, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens: 9_008,
		BlockSize:         16,
	})
	if err == nil {
		t.Fatal("policy accepted missing initialized Prefill profile")
	}
}

func TestRequestAwarePolicyFreshCompletionWindowDoesNotMislockIdleBackend(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.UsedTokens = 2_000
	input.Running = 0
	input.EffectiveSequences = 0
	input.AggregateTPSProxy = 40
	input.MeanActiveTPSProxy = 20
	input.TPSValid = true
	input.SelectionInputTokens = 2_500
	input.RequestReservedTokens = 3_000

	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareAdmit || decision.Reason != RequestAwareReasonOpen ||
		decision.Pressure != 0 {
		t.Fatalf("idle completion-window decision=%+v, want TPS-neutral admit/open", decision)
	}

}

func TestRequestAwarePolicyWaitingIsSelectiveNotGlobalClose(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	small := requestAwareTestInput()
	small.CapacityTokens = 4 * 1024 * 1024
	small.UsedTokens = 128 * 1024
	small.Running = 4
	small.Waiting = 1
	small.SelectionInputTokens = 8 * 1024
	small.EstimatedPrefillTokens = 8 * 1024
	small.RequestReservedTokens = 16 * 1024

	large := small
	large.SelectionInputTokens = 100 * 1024
	large.EstimatedPrefillTokens = 100 * 1024
	large.RequestReservedTokens = 128 * 1024

	smallDecision := policy.Evaluate(small)
	largeDecision := policy.Evaluate(large)
	if smallDecision.Action != RequestAwareAdmit || largeDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("waiting decisions small=%+v large=%+v, want admit/size_protect", smallDecision, largeDecision)
	}
	if largeDecision.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("waiting class-aware decisions small=%+v large=%+v", smallDecision, largeDecision)
	}
}

func TestRequestAwarePolicyBlockAlignsOperationalKVLimits(t *testing.T) {
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens:            896,
		BlockSize:                    16,
		PrefillRegularTokens:         DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("construct block-aligned policy: %v", err)
	}
	input := requestAwareTestInput()
	input.CapacityTokens = 1_000
	input.UsedTokens = 576
	input.SelectionInputTokens = 1
	input.RequestReservedTokens = 16

	decision := policy.Evaluate(input)
	if decision.HardKVLimit != 896 || decision.RemainingKV != 320 {
		t.Fatalf("block-aligned decision=%+v, want hard=896 remaining=320", decision)
	}
	if decision.Pressure != 0 || decision.Action != RequestAwareAdmit {
		t.Fatalf("hard-boundary decision=%+v, want open below the block-aligned hard limit", decision)
	}
}

func TestRequestAwarePolicyUsesFrozenAbsoluteKVLimits(t *testing.T) {
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens:            800_000,
		BlockSize:                    64,
		PrefillRegularTokens:         DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("construct absolute-limit policy: %v", err)
	}
	input := requestAwareTestInput()
	input.CapacityTokens = 2_000_000
	input.RequestReservedTokens = 850_000
	input.SelectionInputTokens = 1
	input.EstimatedPrefillTokens = 1
	decision := policy.Evaluate(input)
	if decision.Reason != RequestAwareReasonKV || decision.HardKVLimit != 800_000 {
		t.Fatalf("absolute-limit decision = %+v, want frozen hard limit 800000", decision)
	}
}

func TestRequestAwarePolicyPostAdmitKVHardFitsWithoutDoubleChargingSoftPressure(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	low := requestAwareTestInput()
	low.UsedTokens = 5_900
	low.SelectionInputTokens = 100
	low.RequestReservedTokens = 100

	high := low
	high.RequestReservedTokens = 1_000
	overHard := low
	overHard.RequestReservedTokens = 3_200

	lowDecision := policy.Evaluate(low)
	highDecision := policy.Evaluate(high)
	overHardDecision := policy.Evaluate(overHard)
	if lowDecision.Pressure != 0 || highDecision.Pressure != 0 || highDecision.PostAdmitKV != 6_900 ||
		highDecision.Action != RequestAwareAdmit || overHardDecision.Action != RequestAwareHardProtect ||
		overHardDecision.Reason != RequestAwareReasonKV {
		t.Fatalf("post-admit KV hard-fit low=%+v high=%+v over=%+v", lowDecision, highDecision, overHardDecision)
	}
}

func TestRequestAwarePolicyHardGuardsCannotBeOverriddenBySmallRequest(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	base := requestAwareTestInput()
	base.SelectionInputTokens = 1
	base.RequestReservedTokens = 16

	cases := []struct {
		name   string
		mutate func(*RequestAwareInput)
		reason RequestAwareReason
	}{
		{name: "stale", mutate: func(input *RequestAwareInput) { input.MetricsFresh = false }, reason: RequestAwareReasonStale},
		{name: "kv", mutate: func(input *RequestAwareInput) {
			input.UsedTokens = 8_990
			input.RequestReservedTokens = 32
		}, reason: RequestAwareReasonKV},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			decision := policy.Evaluate(input)
			if decision.Action != RequestAwareHardProtect || decision.Reason != test.reason {
				t.Fatalf("hard guard decision=%+v, want hard_protect/%s", decision, test.reason)
			}
		})
	}
}

func TestRequestAwarePolicyNewPreemptionGuardIsCandidateClassAware(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	regular := requestAwareTestInput()
	regular.CapacityTokens = 4 * 1024 * 1024
	regular.PreemptionObserved = true
	regular.SelectionInputTokens = 8 * 1024
	regular.EstimatedPrefillTokens = 8 * 1024
	regular.RequestReservedTokens = 16 * 1024

	weighted := regular
	weighted.SelectionInputTokens = 100 * 1024
	weighted.EstimatedPrefillTokens = 100 * 1024
	weighted.RequestReservedTokens = 128 * 1024

	regularDecision := policy.Evaluate(regular)
	weightedDecision := policy.Evaluate(weighted)
	if regularDecision.Action != RequestAwareAdmit || weightedDecision.Action != RequestAwareHardProtect ||
		weightedDecision.Reason != RequestAwareReasonPreemption {
		t.Fatalf("preemption class-aware decisions regular=%+v weighted=%+v", regularDecision, weightedDecision)
	}
}

func TestRequestAwarePolicyIsHistoryIndependent(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	target := requestAwareTestInput()
	target.UsedTokens = 7_000
	target.SelectionInputTokens = 500
	target.RequestReservedTokens = 600
	want := policy.Evaluate(target)

	noisy := requestAwareTestInput()
	noisy.Waiting = 20
	noisy.MeanActiveTPSProxy = 15
	for range 100 {
		_ = policy.Evaluate(noisy)
	}
	got := policy.Evaluate(target)
	if got != want {
		t.Fatalf("history changed pure decision: before=%+v after=%+v", want, got)
	}
}

func newRequestAwareTestPolicy(t *testing.T) *RequestAwarePolicy {
	return newRequestAwareTestPolicyWithLimit(t, 8_992)
}

func newLargeRequestAwareTestPolicy(t *testing.T) *RequestAwarePolicy {
	return newRequestAwareTestPolicyWithLimit(t, 3_774_864)
}

func newRequestAwareTestPolicyWithLimit(t *testing.T, hard int64) *RequestAwarePolicy {
	t.Helper()
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens:            hard,
		BlockSize:                    16,
		PrefillRegularTokens:         DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	return policy
}

func requestAwareTestInput() RequestAwareInput {
	return RequestAwareInput{
		MetricsFresh:       true,
		IdentityValid:      true,
		CapacityTokens:     10_000,
		Running:            4,
		EffectiveSequences: 4,
		AggregateTPSProxy:  80,
		MeanActiveTPSProxy: 20,
		TPSValid:           true,
	}
}
