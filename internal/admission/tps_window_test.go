package admission

import (
	"math"
	"testing"
	"time"
)

func TestTPSWindowUsesSequenceSecondWeightingAndReadiness(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(10_000, 0)
	samples := []tpsSample{
		{start: start, end: start.Add(time.Second), maximumInterval: 2 * time.Second, generatedTokens: 100, previousRunning: 5, running: 5},
		{start: start.Add(time.Second), end: start.Add(2 * time.Second), maximumInterval: 2 * time.Second, generatedTokens: 50, previousRunning: 1, running: 1},
		{start: start.Add(2 * time.Second), end: start.Add(3 * time.Second), maximumInterval: 2 * time.Second, generatedTokens: 40, previousRunning: 1, running: 1},
		{start: start.Add(3 * time.Second), end: start.Add(4 * time.Second), maximumInterval: 2 * time.Second, generatedTokens: 40, previousRunning: 1, running: 1},
	}
	for _, sample := range samples {
		if !window.observe(sample) {
			t.Fatalf("sample rejected as numeric failure: %+v", sample)
		}
	}
	snapshot := window.snapshot(start.Add(4 * time.Second))
	if !snapshot.Enabled || !snapshot.Ready || snapshot.QualifiedSamples != 4 {
		t.Fatalf("snapshot readiness=%+v", snapshot)
	}
	if math.Abs(snapshot.QualifiedActiveSeconds-4) > 1e-9 ||
		math.Abs(snapshot.QualifiedSequenceSeconds-8) > 1e-9 ||
		math.Abs(snapshot.AggregateTPS-57.5) > 1e-9 ||
		math.Abs(snapshot.MeanActiveTPS-28.75) > 1e-9 {
		t.Fatalf("weighted snapshot=%+v", snapshot)
	}
}

func TestTPSWindowDistinguishesPurePrefillFromTrackedDecodeStall(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(20_000, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(time.Second), maximumInterval: 2 * time.Second,
		previousRunning: 1, running: 1,
	}) {
		t.Fatal("pure Prefill sample caused numeric failure")
	}
	if got := window.snapshot(start.Add(time.Second)); got.QualifiedSamples != 0 {
		t.Fatalf("pure Prefill was treated as zero-TPS Decode: %+v", got)
	}

	if !window.observe(tpsSample{
		start: start.Add(time.Second), end: start.Add(2 * time.Second), maximumInterval: 2 * time.Second,
		previousRunning: 1, running: 1, previousLocalActiveDecode: 1,
	}) {
		t.Fatal("tracked Decode stall caused numeric failure")
	}
	got := window.snapshot(start.Add(2 * time.Second))
	if got.QualifiedSamples != 1 || got.QualifiedSequenceSeconds != 1 || got.AggregateTPS != 0 || got.MeanActiveTPS != 0 {
		t.Fatalf("tracked Decode stall was not retained: %+v", got)
	}
}

func TestV01215TPSWindowRetainsCompletionBetweenPollsOnlyAsAggregateEvidence(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_000, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 25,
	}) {
		t.Fatal("completion-between-polls sample caused numeric failure")
	}
	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSamples != 1 || math.Abs(got.QualifiedTokens-25) > 1e-9 ||
		math.Abs(got.QualifiedActiveSeconds-0.5) > 1e-9 ||
		got.QualifiedSequenceSeconds != 0 || math.Abs(got.AggregateTPS-50) > 1e-9 ||
		got.MeanActiveTPS != 0 || got.Ready {
		t.Fatalf("completion-between-polls snapshot=%+v", got)
	}
}

func TestV01215TPSWindowUsesForwardedLiabilitiesForBetweenPollSequenceEvidence(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_500, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 25, forwardedSequenceLiabilities: 2,
	}) {
		t.Fatal("tracked completion-between-polls sample caused numeric failure")
	}
	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSamples != 1 || got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-1) > 1e-9 ||
		math.Abs(got.AggregateTPS-50) > 1e-9 || math.Abs(got.MeanActiveTPS-25) > 1e-9 ||
		got.Ready {
		t.Fatalf("tracked completion-between-polls snapshot=%+v", got)
	}
}

func TestV01215TPSWindowUsesMeasuredShortDecodeExposureInsteadOfFullIntervalLiabilityCount(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_600, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 50, forwardedSequenceLiabilities: 100,
		localResponseSequenceSeconds: 0.5,
	}) {
		t.Fatal("measured short Decode exposure caused numeric failure")
	}

	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-0.5) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-100) > 1e-9 {
		t.Fatalf("short Decode exposure was charged as full-interval liabilities: %+v", got)
	}
}

func TestV01215TPSWindowUsesForwardedExposureForNonStreamingCompletion(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_700, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 50, localForwardedSequenceSeconds: 0.5,
	}) {
		t.Fatal("non-streaming forwarded exposure caused numeric failure")
	}

	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-0.5) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-100) > 1e-9 {
		t.Fatalf("non-streaming completion lost forwarded exposure: %+v", got)
	}
}

func TestV01215TPSWindowDoesNotTurnPurePrefillExposureIntoTPSDebt(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_800, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		previousRunning: 1, running: 1, localForwardedSequenceSeconds: 0.5,
	}) {
		t.Fatal("pure Prefill exposure caused numeric failure")
	}

	if got := window.snapshot(start.Add(500 * time.Millisecond)); got.QualifiedSamples != 0 ||
		got.QualifiedSequenceSamples != 0 || got.QualifiedSequenceSeconds != 0 {
		t.Fatalf("pure Prefill exposure became TPS debt: %+v", got)
	}
}

func TestV01215TPSWindowKeepsKnownDecodeStallWithConservativeForwardedExposure(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_900, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		localForwardedSequenceSeconds: 0.5, localResponseSequenceSeconds: 0.25,
	}) {
		t.Fatal("known Decode stall exposure caused numeric failure")
	}

	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSamples != 1 || got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-0.5) > 1e-9 || got.MeanActiveTPS != 0 {
		t.Fatalf("known Decode stall was discarded or undercharged: %+v", got)
	}
}

func TestV01215TPSWindowUnionsOverlappingBackendAndLocalExposure(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(31_000, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 75, previousRunning: 2, running: 2,
		localForwardedSequenceSeconds: 1.5, localResponseSequenceSeconds: 0.5,
	}) {
		t.Fatal("overlapping exposure caused numeric failure")
	}

	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-1.5) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-50) > 1e-9 {
		t.Fatalf("backend and local exposure were summed or local excess was lost: %+v", got)
	}
}

func TestV01215TPSWindowSeparatesAggregateAndReliableSequenceEvidence(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(31_000, 0)
	for _, sample := range []tpsSample{
		{
			start: start, end: start.Add(time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 40, previousRunning: 2, running: 2,
		},
		{
			start: start.Add(time.Second), end: start.Add(1500 * time.Millisecond), maximumInterval: time.Second,
			generatedTokens: 200,
		},
	} {
		if !window.observe(sample) {
			t.Fatalf("sample caused numeric failure: %+v", sample)
		}
	}

	got := window.snapshot(start.Add(1500 * time.Millisecond))
	if math.Abs(got.AggregateTPS-160) > 1e-9 ||
		math.Abs(got.QualifiedSequenceSeconds-2) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-20) > 1e-9 {
		t.Fatalf("mixed aggregate/sequence snapshot=%+v", got)
	}
}

func TestV01215CompletionBetweenPollsCannotFabricateTPSExploration(t *testing.T) {
	window := newTPSWindow(50)
	start := time.Unix(32_000, 0)
	for index := 0; index < 4; index++ {
		end := start.Add(time.Duration(index+1) * 2 * time.Second)
		if !window.observe(tpsSample{
			start: end.Add(-2 * time.Second), end: end, maximumInterval: 3 * time.Second,
			generatedTokens: 100, previousRunning: 1, running: 1,
		}) {
			t.Fatalf("reliable sample %d caused numeric failure", index)
		}
	}
	for index, tokens := range []uint64{137, 137, 138, 138} {
		end := start.Add(8*time.Second + time.Duration(index+1)*500*time.Millisecond)
		if !window.observe(tpsSample{
			start: end.Add(-500 * time.Millisecond), end: end, maximumInterval: time.Second,
			generatedTokens: tokens,
		}) {
			t.Fatalf("completion-only sample %d caused numeric failure", index)
		}
	}

	snapshot := window.snapshot(start.Add(10 * time.Second))
	if !snapshot.Ready || math.Abs(snapshot.AggregateTPS-95) > 1e-9 ||
		math.Abs(snapshot.MeanActiveTPS-50) > 1e-9 ||
		math.Abs(snapshot.QualifiedSequenceSeconds-8) > 1e-9 {
		t.Fatalf("qualified mixed snapshot=%+v", snapshot)
	}
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 1, TPS: snapshot})
	if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != 1 {
		t.Fatalf("unreliable completion evidence opened exploration: %+v", decision)
	}
}

func TestTPSWindowRejectsMissedIntervalExpiresBucketsAndResets(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(40_000, 250_000_000)
	if !window.observe(tpsSample{
		start: start, end: start.Add(3 * time.Second), maximumInterval: 2 * time.Second,
		generatedTokens: 300, previousRunning: 2, running: 2,
	}) {
		t.Fatal("missed interval caused numeric failure")
	}
	if got := window.snapshot(start.Add(3 * time.Second)); got.QualifiedSamples != 0 {
		t.Fatalf("missed interval qualified: %+v", got)
	}
	if !window.observe(tpsSample{
		start: start.Add(3 * time.Second), end: start.Add(4 * time.Second), maximumInterval: 2 * time.Second,
		generatedTokens: 40, previousRunning: 2, running: 2,
	}) {
		t.Fatal("valid interval caused numeric failure")
	}
	if got := window.snapshot(start.Add(65 * time.Second)); got.QualifiedSamples != 0 || got.QualifiedActiveSeconds != 0 {
		t.Fatalf("expired TPS evidence remained authoritative: %+v", got)
	}
	window.reset()
	if got := window.snapshot(start.Add(65 * time.Second)); got.Enabled != true || got.QualifiedSamples != 0 || got.Ready {
		t.Fatalf("reset snapshot=%+v", got)
	}
}

func TestV01218TPSWindowAttributesRawAndSelectedDenominatorEvidence(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(50_000, 0)
	samples := []tpsSample{
		{
			start: start, end: start.Add(time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 10, previousRunning: 2, running: 2,
			localExposureMeasured: true, localForwardedSequenceSeconds: 1, localResponseSequenceSeconds: 0.5,
		},
		{
			start: start.Add(time.Second), end: start.Add(2 * time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 10, previousRunning: 1, running: 1,
			localExposureMeasured: true, localForwardedSequenceSeconds: 3, localResponseSequenceSeconds: 2,
		},
		{
			start: start.Add(2 * time.Second), end: start.Add(3 * time.Second), maximumInterval: 2 * time.Second,
			generatedTokens:       10,
			localExposureMeasured: true, localForwardedSequenceSeconds: 0.5, localResponseSequenceSeconds: 1,
		},
		{
			start: start.Add(3 * time.Second), end: start.Add(4 * time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 10, forwardedSequenceLiabilities: 2,
		},
		{
			start: start.Add(4 * time.Second), end: start.Add(5 * time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 10, previousRunning: 1, running: 1,
			localExposureMeasured: true, localForwardedSequenceSeconds: 1, localResponseSequenceSeconds: 0.5,
		},
		{
			start: start.Add(5 * time.Second), end: start.Add(6 * time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 10, localExposureMeasured: true,
		},
	}
	for _, sample := range samples {
		if !window.observe(sample) {
			t.Fatalf("denominator evidence sample failed: %+v", sample)
		}
	}
	evidence := window.snapshot(start.Add(6 * time.Second)).Denominator
	if evidence.EndpointSelections != 1 || evidence.LocalForwardedSelections != 1 ||
		evidence.LocalResponseSelections != 1 || evidence.FallbackLiabilitySelections != 1 ||
		evidence.TieSelections != 1 || evidence.NoneSelections != 1 ||
		math.Abs(evidence.EndpointSequenceSeconds-4) > 1e-9 ||
		math.Abs(evidence.LocalForwardedSeconds-5.5) > 1e-9 ||
		math.Abs(evidence.LocalResponseSeconds-4) > 1e-9 ||
		math.Abs(evidence.FallbackLiabilitySeconds-2) > 1e-9 ||
		math.Abs(evidence.SelectedSequenceSeconds-9) > 1e-9 {
		t.Fatalf("denominator evidence=%+v", evidence)
	}
	window.reset()
	if afterReset := window.snapshot(start.Add(6 * time.Second)).Denominator; afterReset != evidence {
		t.Fatalf("rolling-window reset decreased process-lifetime denominator evidence: before=%+v after=%+v", evidence, afterReset)
	}
}
