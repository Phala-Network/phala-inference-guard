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

func TestTPSWindowRetainsCompletionBetweenPollsWithOneSequenceMinimum(t *testing.T) {
	window := newTPSWindow(20)
	start := time.Unix(30_000, 0)
	if !window.observe(tpsSample{
		start: start, end: start.Add(500 * time.Millisecond), maximumInterval: time.Second,
		generatedTokens: 25,
	}) {
		t.Fatal("completion-between-polls sample caused numeric failure")
	}
	got := window.snapshot(start.Add(500 * time.Millisecond))
	if got.QualifiedSamples != 1 || math.Abs(got.QualifiedSequenceSeconds-0.5) > 1e-9 ||
		math.Abs(got.AggregateTPS-50) > 1e-9 || math.Abs(got.MeanActiveTPS-50) > 1e-9 {
		t.Fatalf("completion-between-polls snapshot=%+v", got)
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
