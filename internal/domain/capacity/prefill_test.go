package capacity

import "testing"

func TestEvaluatePrefillLimitExplainsWaitingObservedCap(t *testing.T) {
	result := EvaluatePrefillLimit(PrefillInput{
		Config:           cleanTestConfig(),
		Previous:         Previous{Source: "metrics", CapacityLearnedLimit: 20, GlobalLimit: 20},
		BaseLimit:        100,
		GlobalLimit:      100,
		Running:          20,
		Waiting:          1,
		PrefillProtected: 20,
	})

	if result.Limit != 16 {
		t.Fatalf("prefill limit = %d, want 16", result.Limit)
	}
	if result.Reason != "backend_waiting" {
		t.Fatalf("prefill reason = %q, want backend_waiting", result.Reason)
	}
	if result.TargetReason != "observed_cap" {
		t.Fatalf("prefill target reason = %q, want observed_cap", result.TargetReason)
	}
}

func TestEvaluatePrefillLimitExplainsRunningAtObservedCap(t *testing.T) {
	result := EvaluatePrefillLimit(PrefillInput{
		Config:           cleanTestConfig(),
		Previous:         Previous{Source: "metrics", CapacityLearnedLimit: 20, GlobalLimit: 20},
		BaseLimit:        100,
		GlobalLimit:      100,
		Running:          20,
		DecodeRunning:    1,
		PrefillProtected: 20,
	})

	if result.Limit != 20 {
		t.Fatalf("prefill limit = %d, want 20", result.Limit)
	}
	if result.Reason != "running_at_observed_cap" {
		t.Fatalf("prefill reason = %q, want running_at_observed_cap", result.Reason)
	}
	if result.TargetReason != "observed_cap" {
		t.Fatalf("prefill target reason = %q, want observed_cap", result.TargetReason)
	}
}

func TestEvaluatePrefillLimitDoesNotClampDecodeOnlyObservedCap(t *testing.T) {
	result := EvaluatePrefillLimit(PrefillInput{
		Config:        cleanTestConfig(),
		Previous:      Previous{Source: "metrics", CapacityLearnedLimit: 55, GlobalLimit: 55},
		BaseLimit:     114,
		GlobalLimit:   114,
		Running:       93,
		DecodeRunning: 93,
	})

	if result.Limit != 114 {
		t.Fatalf("prefill limit = %d, want base limit 114 without prefill evidence", result.Limit)
	}
	if result.Reason != "base_limit" {
		t.Fatalf("prefill reason = %q, want base_limit", result.Reason)
	}
}

func TestEvaluatePrefillLimitDoesNotClampMinorNonDecodeObservedCap(t *testing.T) {
	result := EvaluatePrefillLimit(PrefillInput{
		Config:        cleanTestConfig(),
		Previous:      Previous{Source: "metrics", CapacityLearnedLimit: 50, GlobalLimit: 50},
		BaseLimit:     112,
		GlobalLimit:   112,
		Running:       75,
		DecodeRunning: 65,
	})

	if result.Limit != 112 {
		t.Fatalf("prefill limit = %d, want base limit 112 without strong prefill evidence", result.Limit)
	}
	if result.Reason != "base_limit" {
		t.Fatalf("prefill reason = %q, want base_limit", result.Reason)
	}
}

func TestEvaluatePrefillLimitExplainsProtectedThreshold(t *testing.T) {
	result := EvaluatePrefillLimit(PrefillInput{
		Config:           cleanTestConfig(),
		BaseLimit:        100,
		GlobalLimit:      100,
		Running:          20,
		PrefillProtected: 16,
	})

	if result.Limit != 20 {
		t.Fatalf("prefill limit = %d, want 20", result.Limit)
	}
	if result.Reason != "prefill_protected" {
		t.Fatalf("prefill reason = %q, want prefill_protected", result.Reason)
	}
	if result.TargetReason != "threshold" {
		t.Fatalf("prefill target reason = %q, want threshold", result.TargetReason)
	}
}

func TestMeaningfulPrefillProtectedRequiresSubstantialShare(t *testing.T) {
	tests := []struct {
		name      string
		running   int
		protected int
		want      bool
	}{
		{name: "single protected at high load is noise", running: 75, protected: 1, want: false},
		{name: "minor high load protected share is not a freeze", running: 75, protected: 10, want: false},
		{name: "substantial high load protected share freezes", running: 75, protected: 20, want: true},
		{name: "all running protected freezes", running: 4, protected: 4, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MeaningfulPrefillProtected(tt.running, tt.protected); got != tt.want {
				t.Fatalf("MeaningfulPrefillProtected(%d, %d) = %t, want %t", tt.running, tt.protected, got, tt.want)
			}
		})
	}
}
