package latency

import "testing"

func TestAssessRequiresLoadSignalBeforeHighLatency(t *testing.T) {
	assessment := Assess(AssessInput{
		Previous: WindowState{
			HighCount: 1,
		},
		Running:           1,
		RecoveryLoadLimit: 50,
		RequireLoadSignal: true,
	}, Observation{
		Valid:       true,
		Count:       MinWindowCount,
		Avg:         2,
		P95:         2,
		P99:         2,
		SmoothedAvg: 2,
		SmoothedP95: 2,
		SmoothedP99: 2,
	})

	if assessment.High || assessment.YellowReady || assessment.RedReady {
		t.Fatalf("high/yellow/red = %t/%t/%t, want all false for low-load TTFT signal", assessment.High, assessment.YellowReady, assessment.RedReady)
	}
	if assessment.HighCount != 0 {
		t.Fatalf("high count = %d, want decayed 0 without representative load or pressure", assessment.HighCount)
	}
}

func TestAssessAllowsHighLatencyUnderDemandPressure(t *testing.T) {
	assessment := Assess(AssessInput{
		Previous: WindowState{
			HighCount: 1,
		},
		Running:           1,
		RecoveryLoadLimit: 50,
		RequireLoadSignal: true,
		DemandPressure:    true,
	}, Observation{
		Valid:       true,
		Count:       MinWindowCount,
		Avg:         2,
		P95:         2,
		P99:         2,
		SmoothedAvg: 2,
		SmoothedP95: 2,
		SmoothedP99: 2,
	})

	if !assessment.High || !assessment.YellowReady {
		t.Fatalf("high/yellow = %t/%t, want qualified TTFT high under demand pressure", assessment.High, assessment.YellowReady)
	}
	if assessment.HighCount != HighConsecutive {
		t.Fatalf("high count = %d, want %d", assessment.HighCount, HighConsecutive)
	}
}
