package admission

import (
	"math"
	"testing"
	"time"
)

type manualAdmissionClock struct {
	at time.Time
}

func (c *manualAdmissionClock) Now() time.Time {
	return c.at
}

func (c *manualAdmissionClock) Set(at time.Time) {
	c.at = at
}

func TestV01215SequenceExposureLedgerIntegratesSequentialAndConcurrentLifetimes(t *testing.T) {
	start := time.Unix(60_000, 0)
	var ledger sequenceExposureLedger
	if snapshot, ok := ledger.snapshot(start); !ok || snapshot != (sequenceExposureSnapshot{}) {
		t.Fatalf("initial exposure=%+v/%t", snapshot, ok)
	}
	if !ledger.addForwarded(start, 1) ||
		!ledger.addResponse(start.Add(100*time.Millisecond), 1) ||
		!ledger.remove(start.Add(150*time.Millisecond), 1, 1) {
		t.Fatal("sequential lifecycle failed")
	}
	if !ledger.addForwarded(start.Add(150*time.Millisecond), 2) ||
		!ledger.addResponse(start.Add(150*time.Millisecond), 2) {
		t.Fatal("concurrent lifecycle failed")
	}
	got, ok := ledger.snapshot(start.Add(400 * time.Millisecond))
	forwarded, response, secondsOK := got.seconds()
	if !ok || !secondsOK || math.Abs(forwarded-0.65) > 1e-9 ||
		math.Abs(response-0.55) > 1e-9 {
		t.Fatalf("integrated exposure=%+v/%t", got, ok)
	}
	if !ledger.remove(start.Add(400*time.Millisecond), 2, 2) ||
		ledger.activeForwardedSequences != 0 || ledger.activeResponseSequences != 0 {
		t.Fatalf("concurrent release leaked state: %+v", ledger)
	}
}

func TestV01215SequenceExposureLedgerRejectsInvalidTransitionsAndReset(t *testing.T) {
	start := time.Unix(61_000, 0)
	var ledger sequenceExposureLedger
	if ledger.addResponse(start, 1) {
		t.Fatal("response exposure started without a forwarded liability")
	}
	ledger.reset()
	if !ledger.addForwarded(start, 1) || ledger.addResponse(start.Add(time.Millisecond), 2) ||
		ledger.remove(start.Add(2*time.Millisecond), 2, 0) ||
		ledger.addForwarded(start.Add(-time.Millisecond), 1) {
		t.Fatalf("invalid transition was accepted: %+v", ledger)
	}
	ledger.reset()
	if !ledger.valid() || ledger.initialized || !ledger.lastEventAt.IsZero() {
		t.Fatalf("reset exposure=%+v", ledger)
	}
}

func TestV01215SequenceExposureWatermarksAreExactAndOverflowChecked(t *testing.T) {
	forwardedPrevious, ok := (sequenceNanoseconds{}).addDuration(1552*time.Millisecond, 1)
	if !ok {
		t.Fatal("forwarded previous watermark overflowed")
	}
	forwardedCurrent, ok := (sequenceNanoseconds{}).addDuration(1571*time.Millisecond, 1)
	if !ok {
		t.Fatal("forwarded current watermark overflowed")
	}
	responsePrevious, ok := (sequenceNanoseconds{}).addDuration(591*time.Millisecond, 1)
	if !ok {
		t.Fatal("response previous watermark overflowed")
	}
	responseCurrent, ok := (sequenceNanoseconds{}).addDuration(610*time.Millisecond, 1)
	if !ok {
		t.Fatal("response current watermark overflowed")
	}
	delta, ok := (sequenceExposureSnapshot{
		forwardedSequenceNanoseconds: forwardedCurrent,
		responseSequenceNanoseconds:  responseCurrent,
	}).subtract(sequenceExposureSnapshot{
		forwardedSequenceNanoseconds: forwardedPrevious,
		responseSequenceNanoseconds:  responsePrevious,
	})
	forwarded, response, secondsOK := delta.seconds()
	if !ok || !secondsOK || forwarded != response || math.Abs(forwarded-0.019) > 1e-12 {
		t.Fatalf("exact exposure delta=%+v seconds=%v/%v valid=%t/%t", delta, forwarded, response, ok, secondsOK)
	}

	maximum := sequenceNanoseconds{high: math.MaxUint64, low: math.MaxUint64}
	if _, ok := maximum.addDuration(time.Nanosecond, 1); ok {
		t.Fatal("128-bit exposure overflow was accepted")
	}
	if _, ok := (sequenceNanoseconds{}).subtract(sequenceNanoseconds{low: 1}); ok {
		t.Fatal("128-bit exposure underflow was accepted")
	}
}

func TestV01215ControllerPublishesMeasuredSequentialExposure(t *testing.T) {
	start := time.Unix(62_000, 0)
	clock := &manualAdmissionClock{at: start}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 20},
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(start, 0, 0, 0, 0))

	for index := 0; index < 100; index++ {
		requestStart := start.Add(time.Duration(index) * 5 * time.Millisecond)
		clock.Set(requestStart)
		result := controller.Admit(
			requestStart,
			testDemand(1),
		)
		if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
			t.Fatalf("request %d forward=%+v", index, result.Decision)
		}
		clock.Set(requestStart.Add(time.Millisecond))
		if !result.Handle.MarkFirstByte() {
			t.Fatalf("request %d first byte failed", index)
		}
		clock.Set(requestStart.Add(5 * time.Millisecond))
		if !result.Handle.Terminate(TerminalSuccess) {
			t.Fatalf("request %d terminal failed", index)
		}
	}
	clock.Set(start.Add(500 * time.Millisecond))
	publishObservation(t, controller, testObservation(
		start.Add(500*time.Millisecond), 0, 0, 50, 0,
	))

	got := controller.Snapshot(start.Add(501 * time.Millisecond)).State.TPS
	if got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-0.5) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-100) > 1e-9 {
		t.Fatalf("sequential controller exposure=%+v", got)
	}
}

func TestV01215ControllerDefersEventsAfterSampleWatermarkWithoutLosingExposure(t *testing.T) {
	start := time.Unix(63_000, 0)
	clock := &manualAdmissionClock{at: start}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 20},
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(start, 0, 0, 0, 0))

	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	clock.Set(start.Add(100 * time.Millisecond))
	result := controller.Admit(
		clock.Now(),
		testDemand(1),
	)
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() ||
		!result.Handle.MarkFirstByte() {
		t.Fatalf("intervening lifecycle=%+v", result.Decision)
	}
	clock.Set(start.Add(200 * time.Millisecond))
	if !result.Handle.Terminate(TerminalSuccess) {
		t.Fatal("intervening terminal failed")
	}
	first := controller.PublishObservation(window, testObservation(
		start.Add(500*time.Millisecond), 0, 0, 10, 0,
	))
	if !first.Accepted {
		t.Fatalf("first publication=%+v", first)
	}
	if got := controller.Snapshot(start.Add(501 * time.Millisecond)).State.TPS; got.QualifiedSequenceSeconds != 0 {
		t.Fatalf("post-watermark exposure entered early sample: %+v", got)
	}

	clock.Set(start.Add(500 * time.Millisecond))
	publishObservation(t, controller, testObservation(
		start.Add(time.Second), 0, 0, 20, 0,
	))
	got := controller.Snapshot(start.Add(time.Second + time.Millisecond)).State.TPS
	if got.QualifiedSequenceSamples != 1 ||
		math.Abs(got.QualifiedSequenceSeconds-0.1) > 1e-9 ||
		math.Abs(got.MeanActiveTPS-100) > 1e-9 {
		t.Fatalf("deferred exposure was lost or duplicated: %+v", got)
	}
}

func TestV01215ControllerRuntimeResetClearsSequenceExposure(t *testing.T) {
	start := time.Unix(64_000, 0)
	clock := &manualAdmissionClock{at: start}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 20},
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(start, 0, 0, 100, 0))
	result := controller.Admit(
		start,
		testDemand(1),
	)
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
		t.Fatalf("pre-reset lifecycle=%+v", result.Decision)
	}
	clock.Set(start.Add(200 * time.Millisecond))
	reset := publishObservation(t, controller, testObservation(
		start.Add(500*time.Millisecond), 0, 0, 1, 0,
	))
	if !reset.RuntimeReset || result.Handle.Terminate(TerminalCancel) {
		t.Fatalf("runtime reset did not invalidate exposure lifecycle: %+v", reset)
	}
	clock.Set(start.Add(500 * time.Millisecond))
	publishObservation(t, controller, testObservation(
		start.Add(time.Second), 0, 0, 1, 0,
	))
	got := controller.Snapshot(start.Add(time.Second + time.Millisecond)).State.TPS
	if got.QualifiedSamples != 0 || got.QualifiedSequenceSeconds != 0 {
		t.Fatalf("pre-reset exposure survived epoch change: %+v", got)
	}
}
