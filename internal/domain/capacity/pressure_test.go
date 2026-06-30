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
