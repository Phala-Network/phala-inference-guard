package latency

import "testing"

func TestLearnCapExplainsLatencyDownTargets(t *testing.T) {
	tests := []struct {
		name             string
		assessment       Assessment
		wantLearnedLimit int
		wantReason       string
		wantTargetReason string
	}{
		{
			name: "p95",
			assessment: Assessment{
				High:        true,
				YellowReady: true,
				HighCount:   HighConsecutive,
				Signal:      2.0,
				P95Signal:   2.0,
			},
			wantLearnedLimit: 17,
			wantReason:       "ttft_above_target",
			wantTargetReason: "p95_latency",
		},
		{
			name: "p99",
			assessment: Assessment{
				TailHigh:      true,
				YellowReady:   true,
				RedReady:      true,
				TailHighCount: P99HighConsecutive,
				Signal:        10.0,
				P99Signal:     10.0,
			},
			wantLearnedLimit: 14,
			wantReason:       "ttft_red_latency",
			wantTargetReason: "p99_latency",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LearnCap(LearnInput{
				PreviousSource:       "metrics",
				PreviousLearnedLimit: 20,
				PreviousTargetLimit:  20,
				BaseLimit:            100,
				Running:              20,
				Observation: Observation{
					Valid:       true,
					Count:       MinWindowCount,
					SmoothedP95: tt.assessment.P95Signal,
					SmoothedP99: tt.assessment.P99Signal,
				},
				Assessment:        tt.assessment,
				RecoveryLoadLimit: 20,
			})

			if result.State != "ttft_down" {
				t.Fatalf("state = %q, want ttft_down", result.State)
			}
			if result.LearnedLimit != tt.wantLearnedLimit {
				t.Fatalf("learned limit = %d, want %d", result.LearnedLimit, tt.wantLearnedLimit)
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
			if result.TargetReason != tt.wantTargetReason {
				t.Fatalf("target reason = %q, want %q", result.TargetReason, tt.wantTargetReason)
			}
		})
	}
}

func TestLearnCapRequiresQualifiedLoadBeforeLatencyDown(t *testing.T) {
	result := LearnCap(LearnInput{
		PreviousSource:       "metrics",
		PreviousLearnedLimit: 50,
		PreviousTargetLimit:  50,
		BaseLimit:            100,
		Running:              1,
		Observation: Observation{
			Valid:       true,
			Count:       MinWindowCount,
			SmoothedP95: 2,
		},
		Assessment: Assessment{
			High:        true,
			YellowReady: true,
			HighCount:   HighConsecutive,
			Signal:      2,
			P95Signal:   2,
		},
		RecoveryLoadLimit: 50,
		RequireLoadSignal: true,
	})

	if result.State != "ttft_hold" {
		t.Fatalf("state = %q, want ttft_hold", result.State)
	}
	if result.Reason != "latency_signal_underutilized" {
		t.Fatalf("reason = %q, want latency_signal_underutilized", result.Reason)
	}
	if result.LearnedLimit != 50 || result.TargetLimit != 50 || result.Limit != 50 {
		t.Fatalf("learned/target/limit = %d/%d/%d, want 50/50/50", result.LearnedLimit, result.TargetLimit, result.Limit)
	}
}

func TestLearnCapExplainsHealthyFastProbeUp(t *testing.T) {
	result := LearnCap(LearnInput{
		PreviousSource:       "metrics",
		PreviousLearnedLimit: 20,
		PreviousTargetLimit:  20,
		BaseLimit:            100,
		Running:              20,
		StepUpRatio:          0.10,
		Observation: Observation{
			Valid:       true,
			Count:       MinWindowCount,
			SmoothedP95: 0.20,
			SmoothedAvg: 0.20,
		},
		Assessment: Assessment{
			Healthy:      true,
			HealthyCount: HealthyConsecutive,
		},
		RecoveryLoadLimit: 20,
	})

	if result.State != "ttft_probe_up" {
		t.Fatalf("state = %q, want ttft_probe_up", result.State)
	}
	if result.LearnedLimit != 22 {
		t.Fatalf("learned limit = %d, want 22", result.LearnedLimit)
	}
	if result.TargetLimit != 22 {
		t.Fatalf("target limit = %d, want 22", result.TargetLimit)
	}
	if result.Limit != 22 {
		t.Fatalf("limit = %d, want recovered learned limit 22 while TTFT recovers", result.Limit)
	}
	if result.Reason != "healthy_window_satisfied" {
		t.Fatalf("reason = %q, want healthy_window_satisfied", result.Reason)
	}
	if result.TargetReason != "fast_recovery_probe" {
		t.Fatalf("target reason = %q, want fast_recovery_probe", result.TargetReason)
	}
}

func TestLearnCapHealthySignalDoesNotClampWithStaleLowLearnedLimit(t *testing.T) {
	result := LearnCap(LearnInput{
		PreviousSource:       "metrics",
		PreviousLearnedLimit: 4,
		PreviousTargetLimit:  4,
		BaseLimit:            100,
		Running:              3,
		Observation: Observation{
			Valid:       true,
			Count:       MinWindowCount,
			SmoothedP95: 0.71,
			SmoothedP99: 0.71,
			SmoothedAvg: 0.33,
		},
		Assessment: Assessment{
			Healthy:      true,
			HealthyCount: 1,
		},
		RecoveryLoadLimit: 4,
	})

	if result.State != "ttft_healthy" {
		t.Fatalf("state = %q, want ttft_healthy", result.State)
	}
	if result.LearnedLimit != 4 {
		t.Fatalf("learned limit = %d, want stale learned limit 4 preserved", result.LearnedLimit)
	}
	if result.TargetLimit != 5 {
		t.Fatalf("target limit = %d, want recovery probe target 5", result.TargetLimit)
	}
	if result.Limit != 4 {
		t.Fatalf("limit = %d, want learned limit 4 until healthy recovery window is satisfied", result.Limit)
	}
	if result.Reason != "healthy_window_accumulating" {
		t.Fatalf("reason = %q, want healthy_window_accumulating", result.Reason)
	}
	if result.TargetReason != "recovery_probe" {
		t.Fatalf("target reason = %q, want recovery_probe", result.TargetReason)
	}
}

func TestLearnCapExplainsInsufficientLatencySignal(t *testing.T) {
	result := LearnCap(LearnInput{
		PreviousSource:       "metrics",
		PreviousLearnedLimit: 50,
		PreviousTargetLimit:  80,
		BaseLimit:            100,
	})

	if result.State != "no_signal" {
		t.Fatalf("state = %q, want no_signal", result.State)
	}
	if result.TargetLimit != 50 {
		t.Fatalf("target limit = %d, want 50", result.TargetLimit)
	}
	if result.Reason != "insufficient_latency_signal" {
		t.Fatalf("reason = %q, want insufficient_latency_signal", result.Reason)
	}
	if result.TargetReason != "learned_limit" {
		t.Fatalf("target reason = %q, want learned_limit", result.TargetReason)
	}
	if result.Limit != 50 {
		t.Fatalf("limit = %d, want learned limit 50 while TTFT has no current signal and no demand pressure", result.Limit)
	}
}

func TestLearnCapNoSignalReleasesClampUnderDemandPressure(t *testing.T) {
	result := LearnCap(LearnInput{
		PreviousSource:       "metrics",
		PreviousLearnedLimit: 50,
		PreviousTargetLimit:  80,
		BaseLimit:            100,
		DemandPressure:       true,
	})

	if result.State != "no_signal" || result.Reason != "insufficient_latency_signal" {
		t.Fatalf("state/reason = %s/%s, want no_signal/insufficient_latency_signal", result.State, result.Reason)
	}
	if result.Limit != 100 {
		t.Fatalf("limit = %d, want base limit 100 while demand pressure needs a no-signal probe", result.Limit)
	}
}
