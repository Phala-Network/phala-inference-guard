package decision

import "testing"

func TestEvaluateSignalsDisablesZeroThresholds(t *testing.T) {
	result := EvaluateSignals(SignalConfig{}, SignalInput{
		Running:         100,
		Waiting:         100,
		KVCacheUsage:    1,
		PreemptionDelta: 100,
	})

	if result.State != "green" {
		t.Fatalf("state = %q, want green", result.State)
	}
	if len(result.YellowReasons) != 0 || len(result.RedReasons) != 0 {
		t.Fatalf("yellow/red reasons = %v/%v, want none", result.YellowReasons, result.RedReasons)
	}
}

func TestEvaluateSignalsSuppressesUserTPSDuringPrefill(t *testing.T) {
	cfg := SignalConfig{
		UserTPSEnabled: true,
		UserTPSYellow:  25,
		UserTPSRed:     20,
		UserTPSMinRun:  1,
	}
	input := SignalInput{
		DecodeRunning:             8,
		UserTPS:                   5,
		UserTPSValid:              true,
		UserTPSYellowReady:        true,
		UserTPSRedReady:           true,
		RepresentativeUserTPSLoad: true,
		PrefillTransition:         true,
	}

	result := EvaluateSignals(cfg, input)
	if result.State != "green" {
		t.Fatalf("state = %q, want green while prefill suppresses user TPS", result.State)
	}
	if containsReason(result.YellowReasons, "single_user_tps") || containsReason(result.RedReasons, "single_user_tps") {
		t.Fatalf("user TPS reasons = %v/%v, want none during prefill", result.YellowReasons, result.RedReasons)
	}

	input.PrefillTransition = false
	result = EvaluateSignals(cfg, input)
	if result.State != "red" {
		t.Fatalf("state = %q, want red after prefill suppression ends", result.State)
	}
	if !containsReason(result.RedReasons, "single_user_tps") {
		t.Fatalf("red reasons = %v, want single_user_tps", result.RedReasons)
	}
}

func TestEvaluateSignalsTTFTRedTakesPrecedenceOverYellow(t *testing.T) {
	result := EvaluateSignals(SignalConfig{
		TTFTEnabled: true,
	}, SignalInput{
		TTFTYellowReady: true,
		TTFTRedReady:    true,
	})

	if result.State != "red" {
		t.Fatalf("state = %q, want red", result.State)
	}
	if !containsReason(result.RedReasons, "ttft_latency") {
		t.Fatalf("red reasons = %v, want ttft_latency", result.RedReasons)
	}
	if containsReason(result.YellowReasons, "ttft_latency") {
		t.Fatalf("yellow reasons = %v, want no duplicate ttft_latency", result.YellowReasons)
	}
}

func containsReason(reasons []string, target string) bool {
	for _, reason := range reasons {
		if reason == target {
			return true
		}
	}
	return false
}
