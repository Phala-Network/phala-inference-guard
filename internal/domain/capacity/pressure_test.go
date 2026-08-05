package capacity

import "testing"

func TestEvaluatePressureLimitExplainsPreemptionPressure(t *testing.T) {
	cap := &PressureCap{}
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 100, 20, 0, 20, 0.10, 1, 40, true, false, false)

	if result.Limit != 15 {
		t.Fatalf("pressure limit = %d, want learned pressure cap 15", result.Limit)
	}
	if result.Reason != "severe_pressure" {
		t.Fatalf("pressure reason = %q, want severe_pressure", result.Reason)
	}
	if result.TargetReason != "preemption" {
		t.Fatalf("pressure target reason = %q, want preemption", result.TargetReason)
	}
}

func TestEvaluatePressureLimitExplainsWaitingPressure(t *testing.T) {
	cap := &PressureCap{}
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 100, 20, 1, 20, 0.10, 0, 40, true, false, false)

	if result.Limit != 15 {
		t.Fatalf("pressure limit = %d, want learned pressure cap 15", result.Limit)
	}
	if result.Reason != "waiting_pressure" {
		t.Fatalf("pressure reason = %q, want waiting_pressure", result.Reason)
	}
	if result.TargetReason != "backend_waiting" {
		t.Fatalf("pressure target reason = %q, want backend_waiting", result.TargetReason)
	}
}

func TestEvaluatePressureLimitExplainsLearnedCap(t *testing.T) {
	cap := &PressureCap{}
	cap.value.Store(12)
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 100, 20, 0, 20, 0.10, 0, 10, false, false, false)

	if result.Limit != 12 {
		t.Fatalf("pressure limit = %d, want learned cap 12", result.Limit)
	}
	if result.Reason != "learned_cap" {
		t.Fatalf("pressure reason = %q, want learned_cap", result.Reason)
	}
	if result.TargetReason != "learned_pressure_cap" {
		t.Fatalf("pressure target reason = %q, want learned_pressure_cap", result.TargetReason)
	}
}

func TestEvaluatePressureLimitKeepsLearnedCapWhenQOSHealthy(t *testing.T) {
	cap := &PressureCap{}
	cap.value.Store(12)
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 100, 20, 0, 20, 0.10, 0, 40, true, false, false)

	if result.Limit != 12 {
		t.Fatalf("pressure limit = %d, want learned cap 12 even while QoS is healthy", result.Limit)
	}
	if result.Reason != "learned_cap" {
		t.Fatalf("pressure reason = %q, want learned_cap", result.Reason)
	}
}

func TestEvaluatePressureLimitRecoversNearBaseLearnedCapWhenHealthy(t *testing.T) {
	cap := &PressureCap{}
	cap.value.Store(157)
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 159, 30, 0, 28, 0.03, 0, 110, true, false, false)

	if result.Limit != 159 {
		t.Fatalf("pressure limit = %d, want base limit 159 after near-base recovery", result.Limit)
	}
	if result.Reason != "base_limit" {
		t.Fatalf("pressure reason = %q, want base_limit", result.Reason)
	}
	if got := int(cap.Load()); got != 159 {
		t.Fatalf("pressure cap = %d, want recovered base limit 159", got)
	}
}

func TestEvaluatePressureLimitKeepsNearBaseLearnedCapWhenActivelyBinding(t *testing.T) {
	cap := &PressureCap{}
	cap.value.Store(157)
	result := EvaluatePressureLimit(cap, cleanTestConfig(), 159, 157, 0, 156, 0.03, 0, 110, true, false, false)

	if result.Limit != 157 {
		t.Fatalf("pressure limit = %d, want learned cap 157 while running is at cap", result.Limit)
	}
	if result.Reason != "learned_cap" {
		t.Fatalf("pressure reason = %q, want learned_cap", result.Reason)
	}
}

func TestEvaluatePressureLimitTransientBasePreservesLearnedCap(t *testing.T) {
	tests := []struct {
		name          string
		learned       int64
		restoredBase  int
		restoredLimit int
	}{
		{
			name:          "cap matches restored base",
			learned:       15,
			restoredBase:  15,
			restoredLimit: 15,
		},
		{
			name:          "genuine lower cap remains binding",
			learned:       12,
			restoredBase:  15,
			restoredLimit: 12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := &PressureCap{}
			cap.value.Store(tt.learned)

			transient := EvaluatePressureLimit(cap, cleanTestConfig(), 1, 1, 0, 1, 0.10, 0, 40, true, false, false)
			if transient.Limit != 1 {
				t.Fatalf("transient pressure limit = %d, want tighter base limit 1", transient.Limit)
			}
			if got := cap.Load(); got != tt.learned {
				t.Fatalf("pressure cap after transient base = %d, want retained %d", got, tt.learned)
			}

			restored := EvaluatePressureLimit(cap, cleanTestConfig(), tt.restoredBase, 1, 0, 1, 0.10, 0, 40, true, false, false)
			if restored.Limit != tt.restoredLimit {
				t.Fatalf("pressure limit after base recovery = %d, want %d", restored.Limit, tt.restoredLimit)
			}
			if got := cap.Load(); got != tt.learned {
				t.Fatalf("pressure cap after base recovery = %d, want retained %d", got, tt.learned)
			}
		})
	}
}

func TestRecoverPressureCapRequiresDemandPressure(t *testing.T) {
	cap := &PressureCap{}
	cap.value.Store(12)
	cfg := cleanTestConfig()

	RecoverPressureCap(cap, cfg, 100, 12, 0, 12, 600, true, false)
	if got := int(cap.Load()); got != 12 {
		t.Fatalf("pressure cap = %d, want unchanged 12 without demand pressure", got)
	}

	RecoverPressureCap(cap, cfg, 100, 12, 0, 12, 600, true, true)
	if got := int(cap.Load()); got <= 12 {
		t.Fatalf("pressure cap = %d, want recovery above 12 with demand pressure", got)
	}
}
