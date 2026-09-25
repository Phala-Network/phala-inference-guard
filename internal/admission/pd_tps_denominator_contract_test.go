package admission

import (
	"math"
	"testing"
	"time"
)

func TestPDDecodeTPSExcludesPrefillTransferExposure(t *testing.T) {
	start := time.Unix(70_000, 0)
	for _, test := range []struct {
		name      string
		pdDecode  bool
		wantMean  float64
		wantAdmit bool
	}{
		{name: "pd decode", pdDecode: true, wantMean: 50, wantAdmit: true},
		{name: "unified", wantMean: 6.25},
	} {
		t.Run(test.name, func(t *testing.T) {
			window := newTPSWindow(25)
			window.pdDecode = test.pdDecode
			for step := 0; step < 4; step++ {
				at := start.Add(time.Duration(step) * time.Second)
				if !window.observe(tpsSample{
					start: at, end: at.Add(time.Second), maximumInterval: 2 * time.Second,
					generatedTokens: 100, previousRunning: 2, running: 2,
					localExposureMeasured:         true,
					localForwardedSequenceSeconds: 16, localResponseSequenceSeconds: 1,
				}) {
					t.Fatal("TPS observation failed")
				}
			}
			snapshot := window.snapshot(start.Add(4 * time.Second))
			if !snapshot.Ready || math.Abs(snapshot.MeanActiveTPS-test.wantMean) > 1e-9 {
				t.Fatalf("TPS snapshot=%+v", snapshot)
			}
			decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 2, TPS: snapshot}, 128, RequestPriorityBasic)
			if decision.fits != test.wantAdmit {
				t.Fatalf("TPS decision=%+v", decision)
			}
		})
	}
}

func TestPDDecodeTransferExposureDoesNotCausePreForwardTPSRejection(t *testing.T) {
	start := time.Unix(71_000, 0)
	for _, test := range []struct {
		name      string
		pdDecode  bool
		wantAdmit bool
	}{
		{name: "pd decode", pdDecode: true, wantAdmit: true},
		{name: "unified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &manualAdmissionClock{at: start}
			controller, err := NewAdmissionController(ControllerConfig{
				RuntimeIdentity: testRuntimeIdentity,
				TPS:             TPSPolicyConfig{Reference: 25}, PDDecode: test.pdDecode,
				WindowConcurrency: 128, RunningLimit: 128,
				RunningLimitSource: RunningLimitSourceAdmin, Now: clock.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			publishObservation(t, controller, testObservation(start, 2, 0, 0, 0))
			for index := 0; index < 16; index++ {
				result := controller.Admit(start, testDemand(1))
				if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
					t.Fatalf("forwarded reservation %d: %+v", index, result.Decision)
				}
			}
			for step := 1; step <= 4; step++ {
				at := start.Add(time.Duration(step) * time.Second)
				clock.Set(at)
				observation := testObservation(at, 2, 0, uint64(step*100), 0)
				observation.DecodePending = 14
				publishObservation(t, controller, observation)
			}
			decision := controller.Admit(start.Add(4*time.Second+time.Millisecond), testDemand(1)).Decision
			if decision.Admitted() != test.wantAdmit {
				t.Fatalf("PD transfer admission=%+v", decision)
			}
		})
	}
}

func TestPDDecodeStillProtectsGenuinelySlowGeneration(t *testing.T) {
	window := newTPSWindow(25)
	window.pdDecode = true
	start := time.Unix(72_000, 0)
	for step := 0; step < 4; step++ {
		at := start.Add(time.Duration(step) * time.Second)
		if !window.observe(tpsSample{
			start: at, end: at.Add(time.Second), maximumInterval: 2 * time.Second,
			generatedTokens: 20, previousRunning: 2, running: 2,
			localExposureMeasured:         true,
			localForwardedSequenceSeconds: 16, localResponseSequenceSeconds: 1,
		}) {
			t.Fatal("slow Decode observation failed")
		}
	}
	snapshot := window.snapshot(start.Add(4 * time.Second))
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 2, TPS: snapshot}, 128, RequestPriorityBasic)
	if !snapshot.Ready || math.Abs(snapshot.MeanActiveTPS-10) > 1e-9 || decision.fits ||
		decision.subreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("slow Decode was not protected: snapshot=%+v decision=%+v", snapshot, decision)
	}
}
