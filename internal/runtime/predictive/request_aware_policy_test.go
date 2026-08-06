package predictive

import (
	"math"
	"testing"
)

func TestRequestAwarePolicyRejectsMissingKVElasticBandOrHardMargin(t *testing.T) {
	for _, test := range []struct {
		name   string
		softKV float64
		hardKV float64
	}{
		{name: "zero soft boundary", softKV: 0, hardKV: 0.90},
		{name: "no hard margin", softKV: 0.60, hardKV: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRequestAwarePolicy(RequestAwareConfig{
				SoftKVRatio: test.softKV,
				HardKVRatio: test.hardKV,
				TPSTarget:   20,
				TPSFloor:    15,
				BlockSize:   16,
			})
			if err == nil {
				t.Fatalf("NewRequestAwarePolicy accepted soft/hard %.2f/%.2f without required band and margin", test.softKV, test.hardKV)
			}
		})
	}
}

func TestRequestAwarePolicySelectsSmallRequestUnderSamePressure(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	small := requestAwareTestInput()
	small.UsedTokens = 2_000
	small.Waiting = 1
	small.SelectionInputTokens = 500
	small.RequestReservedTokens = 800

	large := small
	large.SelectionInputTokens = 700

	smallDecision := policy.Evaluate(small)
	largeDecision := policy.Evaluate(large)
	if smallDecision.Action != RequestAwareAdmit || largeDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("same-pressure decisions small=%+v large=%+v, want admit/size_protect", smallDecision, largeDecision)
	}
	if smallDecision.Pressure != largeDecision.Pressure || smallDecision.AllowanceTokens != largeDecision.AllowanceTokens {
		t.Fatalf("request size changed current pressure budget: small=%+v large=%+v", smallDecision, largeDecision)
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
	policy := newRequestAwareTestPolicy(t)
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
		localDecodeDecision.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("local decode plus 650K decision=%+v, want pre-forward busy protection", localDecodeDecision)
	}

	busy := input
	busy.Running = 20
	busy.EffectiveSequences = 20
	busyDecision := policy.Evaluate(busy)
	if busyDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("busy 650K decision=%+v, want pre-forward size protection before TPS feedback", busyDecision)
	}
}

func TestRequestAwarePolicyPrefillBoundaries(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	base := requestAwareTestInput()
	base.CapacityTokens = 4 * 1024 * 1024
	base.UsedTokens = 0
	base.ReservedTokens = 0
	base.Running = 20
	base.Waiting = 0
	base.EffectiveSequences = 20
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
		{name: "at 512K", tokens: 512 * 1024, wantClass: RequestAwarePrefillQuiescent, wantAction: RequestAwareSizeProtect},
		{name: "at 650K", tokens: 650 * 1024, wantClass: RequestAwarePrefillQuiescent, wantAction: RequestAwareSizeProtect},
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

func TestRequestAwarePolicyCapsAggregateRegularPrefillBurstWithoutBlockingShortBehindExclusive(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	regularBurst := requestAwareTestInput()
	regularBurst.CapacityTokens = 4 * 1024 * 1024
	regularBurst.UsedTokens = 0
	regularBurst.ReservedTokens = 0
	regularBurst.RequestReservedTokens = 8 * 1024
	regularBurst.SelectionInputTokens = 8 * 1024
	regularBurst.EstimatedPrefillTokens = 8 * 1024
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
	if short.Action != RequestAwareAdmit || short.Reason != RequestAwareReasonOpen {
		t.Fatalf("short behind exclusive decision=%+v, want work-conserving admit/open", short)
	}
}

func TestRequestAwarePolicyRejectsInvalidPrefillThresholdOrdering(t *testing.T) {
	_, err := NewRequestAwarePolicy(RequestAwareConfig{
		SoftKVRatio: 0.60, HardKVRatio: 0.90, TPSTarget: 20, TPSFloor: 15, BlockSize: 16,
		PrefillRegularTokens: 64 * 1024, PrefillExclusiveTokens: 256 * 1024,
		PrefillQuiescentTokens: 512 * 1024, PrefillAggregateBudgetTokens: 128 * 1024,
	})
	if err == nil {
		t.Fatal("NewRequestAwarePolicy accepted aggregate prefill budget below exclusive threshold")
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
		decision.Pressure != 0 || decision.TPSForecastValid {
		t.Fatalf("idle completion-window decision=%+v, want TPS-neutral admit/open", decision)
	}

	afterLocalReservations := input
	afterLocalReservations.EffectiveSequences = 2
	constrained := policy.Evaluate(afterLocalReservations)
	if constrained.Action != RequestAwareSizeProtect || constrained.PressureSource != RequestAwarePressureTPS ||
		!constrained.TPSForecastValid || math.Abs(constrained.ProjectedMeanActiveTPSProxy-(40.0/3.0)) > 1e-9 {
		t.Fatalf("same-snapshot post-admit completion forecast=%+v, want TPS size protection", constrained)
	}
}

func TestRequestAwarePolicyWaitingIsSelectiveNotGlobalClose(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	small := requestAwareTestInput()
	small.UsedTokens = 2_000
	small.Running = 4
	small.Waiting = 1
	small.SelectionInputTokens = 500
	small.RequestReservedTokens = 600

	large := small
	large.SelectionInputTokens = 2_600
	large.RequestReservedTokens = 2_600

	smallDecision := policy.Evaluate(small)
	largeDecision := policy.Evaluate(large)
	if smallDecision.Action != RequestAwareAdmit || largeDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("waiting decisions small=%+v large=%+v, want admit/size_protect", smallDecision, largeDecision)
	}
	if smallDecision.PressureSource != RequestAwarePressureTPS || !smallDecision.TPSForecastValid ||
		math.Abs(smallDecision.ProjectedMeanActiveTPSProxy-16) > 1e-9 {
		t.Fatalf("waiting forecast=%+v, want TPS source and projected TPS=16", smallDecision)
	}
}

func TestRequestAwarePolicyForecastsPostAdmitTPSAndStopsAtProjectedFloor(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.UsedTokens = 2_000
	input.SelectionInputTokens = 2_500
	input.RequestReservedTokens = 2_500

	atTarget := input
	atTarget.MeanActiveTPSProxy = 20
	atTarget.AggregateTPSProxy = 80
	midway := input
	midway.MeanActiveTPSProxy = 19.5
	midway.AggregateTPSProxy = 78
	atFloor := input
	atFloor.MeanActiveTPSProxy = 15
	atFloor.AggregateTPSProxy = 60
	belowFloor := input
	belowFloor.MeanActiveTPSProxy = 10
	belowFloor.AggregateTPSProxy = 40
	tinyAtFloor := atFloor
	tinyAtFloor.SelectionInputTokens = 1
	tinyAtFloor.RequestReservedTokens = 16

	targetDecision := policy.Evaluate(atTarget)
	midwayDecision := policy.Evaluate(midway)
	floorDecision := policy.Evaluate(atFloor)
	belowFloorDecision := policy.Evaluate(belowFloor)
	tinyAtFloorDecision := policy.Evaluate(tinyAtFloor)
	if targetDecision.Action != RequestAwareAdmit || midwayDecision.Action != RequestAwareSizeProtect ||
		floorDecision.Action != RequestAwareSizeProtect || belowFloorDecision.Action != RequestAwareSizeProtect ||
		tinyAtFloorDecision.Action != RequestAwareSizeProtect {
		t.Fatalf(
			"TPS decisions target=%+v midway=%+v floor=%+v below=%+v tiny=%+v",
			targetDecision, midwayDecision, floorDecision, belowFloorDecision, tinyAtFloorDecision,
		)
	}
	if targetDecision.AllowanceTokens <= midwayDecision.AllowanceTokens ||
		floorDecision.AllowanceTokens != 0 || belowFloorDecision.AllowanceTokens != floorDecision.AllowanceTokens ||
		floorDecision.Pressure != 1 || belowFloorDecision.Pressure != 1 ||
		floorDecision.PressureSource != RequestAwarePressureTPS || tinyAtFloorDecision.Reason != RequestAwareReasonRequestSize ||
		!midwayDecision.TPSForecastValid || math.Abs(midwayDecision.ProjectedMeanActiveTPSProxy-15.6) > 1e-9 {
		t.Fatalf(
			"TPS allowance/floor target=%+v midway=%+v floor=%+v below=%+v tiny=%+v",
			targetDecision, midwayDecision, floorDecision, belowFloorDecision, tinyAtFloorDecision,
		)
	}
}

func TestRequestAwarePolicyEffectiveSequencesChangeSameSnapshotVerdict(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	first := requestAwareTestInput()
	first.UsedTokens = 2_000
	first.SelectionInputTokens = 400
	first.RequestReservedTokens = 500
	first.MeanActiveTPSProxy = 19.8
	first.AggregateTPSProxy = 79.2

	second := first
	second.EffectiveSequences++

	firstDecision := policy.Evaluate(first)
	secondDecision := policy.Evaluate(second)
	if firstDecision.Action != RequestAwareAdmit || secondDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("effective-sequence decisions first=%+v second=%+v, want admit/size_protect", firstDecision, secondDecision)
	}
	if !firstDecision.TPSForecastValid || !secondDecision.TPSForecastValid ||
		firstDecision.ProjectedMeanActiveTPSProxy <= secondDecision.ProjectedMeanActiveTPSProxy {
		t.Fatalf("effective-sequence forecast did not decrease: first=%+v second=%+v", firstDecision, secondDecision)
	}
}

func TestRequestAwarePolicyHealthySnapshotDoesNotReuseOptimisticTPSAfterReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	first := requestAwareTestInput()
	first.UsedTokens = 2_000
	first.SelectionInputTokens = 300
	first.RequestReservedTokens = 400

	second := first
	second.EffectiveSequences++

	firstDecision := policy.Evaluate(first)
	secondDecision := policy.Evaluate(second)
	if firstDecision.Action != RequestAwareAdmit || secondDecision.Action != RequestAwareSizeProtect {
		t.Fatalf("healthy same-snapshot decisions first=%+v second=%+v, want admit/size_protect", firstDecision, secondDecision)
	}
	if !firstDecision.TPSForecastValid || !secondDecision.TPSForecastValid ||
		math.Abs(firstDecision.ProjectedMeanActiveTPSProxy-20) > 1e-9 ||
		math.Abs(secondDecision.ProjectedMeanActiveTPSProxy-(80.0/6.0)) > 1e-9 {
		t.Fatalf("healthy same-snapshot forecast first=%+v second=%+v", firstDecision, secondDecision)
	}
}

func TestRequestAwarePolicyBlockAlignsOperationalKVLimits(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
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
		t.Fatalf("soft boundary decision=%+v, want open at block-aligned soft limit", decision)
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
		{name: "preemption", mutate: func(input *RequestAwareInput) { input.PreemptionCooldown = true }, reason: RequestAwareReasonPreemption},
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
	t.Helper()
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		SoftKVRatio: 0.60,
		HardKVRatio: 0.90,
		TPSTarget:   20,
		TPSFloor:    15,
		BlockSize:   16,
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
