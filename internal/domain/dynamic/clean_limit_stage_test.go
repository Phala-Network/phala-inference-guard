package dynamic

import (
	"testing"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
)

func TestEvaluateCleanQOSLimitAnnotatesTTFTCapacity(t *testing.T) {
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true
	builder := decision.NewBuilder(nil, nil)

	qosLimit, next := evaluateCleanQOSLimit(cfg, cleanSignals{}, cleanTTFTStage{
		Limit:      12,
		Assessment: latency.Assessment{RedReady: true},
	}, builder, 50, false)

	if qosLimit != 12 {
		t.Fatalf("qos limit = %d, want TTFT cap 12", qosLimit)
	}
	if !containsString(next.RedReasons(), "ttft_latency_capacity") {
		t.Fatalf("red reasons = %v, want ttft_latency_capacity", next.RedReasons())
	}
	if builder.State() != "green" {
		t.Fatalf("original builder state = %q, want unchanged green", builder.State())
	}
}

func TestApplyCleanThroughputLimitReturnsReasonedBuilder(t *testing.T) {
	tests := []struct {
		name       string
		signals    cleanSignals
		state      string
		wantLimit  int
		wantYellow bool
	}{
		{
			name:      "lower capacity without pressure is only a cap component",
			signals:   cleanSignals{Running: 4},
			state:     "hold_underutilized",
			wantLimit: 8,
		},
		{
			name:      "running at cap without learn down is only a cap component",
			signals:   cleanSignals{Running: 8},
			state:     "hold_underutilized",
			wantLimit: 8,
		},
		{
			name:       "learn down state explains user TPS capacity",
			signals:    cleanSignals{Running: 4},
			state:      "pig_down",
			wantLimit:  8,
			wantYellow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, builder := applyCleanThroughputLimit(tt.signals, cleanThroughputStage{
				Result: capacity.LearnResult{State: tt.state},
			}, decision.NewBuilder(nil, nil), 20, 8)

			if limit != tt.wantLimit {
				t.Fatalf("limit = %d, want %d", limit, tt.wantLimit)
			}
			hasReason := containsString(builder.YellowReasons(), "single_user_tps_capacity")
			if hasReason != tt.wantYellow {
				t.Fatalf("yellow reasons = %v, contains single_user_tps_capacity = %t, want %t", builder.YellowReasons(), hasReason, tt.wantYellow)
			}
		})
	}
}

func TestApplyCleanPressureLimitPreservesCurrentSeverity(t *testing.T) {
	greenLimit, greenBuilder := applyCleanPressureLimit(cleanPressureStage{Limit: 7}, decision.NewBuilder(nil, nil), 20)
	if greenLimit != 7 {
		t.Fatalf("green pressure limit = %d, want 7", greenLimit)
	}
	if !containsString(greenBuilder.YellowReasons(), "scheduler_pressure_capacity") {
		t.Fatalf("green yellow reasons = %v, want scheduler_pressure_capacity", greenBuilder.YellowReasons())
	}

	redLimit, redBuilder := applyCleanPressureLimit(cleanPressureStage{Limit: 6}, decision.NewBuilder(nil, []string{"preemptions"}), 20)
	if redLimit != 6 {
		t.Fatalf("red pressure limit = %d, want 6", redLimit)
	}
	if !containsString(redBuilder.RedReasons(), "scheduler_pressure_capacity") {
		t.Fatalf("red reasons = %v, want scheduler_pressure_capacity", redBuilder.RedReasons())
	}
}

func TestApplyCleanPrefillLimitKeepsSoftPrefillCapOutOfStateReasons(t *testing.T) {
	limit, builder := applyCleanPrefillLimit(cleanPrefillStage{Limit: 9}, decision.NewBuilder(nil, nil), 20)

	if limit != 9 {
		t.Fatalf("prefill limit = %d, want 9", limit)
	}
	if containsString(builder.YellowReasons(), "prefill_pressure_capacity") {
		t.Fatalf("yellow reasons = %v, want no prefill_pressure_capacity for a soft cap", builder.YellowReasons())
	}
}

func TestEnforceCleanUserTPSCapacityLimitRecomputesStateLimit(t *testing.T) {
	cfg := cleanEvaluateConfig()
	cfg.GlobalGreen = 50
	cfg.GlobalYellow = 30
	cfg.GlobalRed = 10
	signals := cleanSignals{
		DecodeRunning:      4,
		QOSTPS:             1000,
		QOSTPSValid:        true,
		UserTPS:            19,
		UserTPSRedReady:    true,
		UserTPSYellowReady: true,
	}

	qosLimit, baseLimit, builder := enforceCleanUserTPSCapacityLimit(cfg, Input{GlobalLimit: 50}, signals, decision.NewBuilder(nil, nil), 50, 40, true)
	if qosLimit != 10 || baseLimit != 10 {
		t.Fatalf("red qos/base limits = %d/%d, want 10/10", qosLimit, baseLimit)
	}
	if !containsString(builder.RedReasons(), "single_user_tps_capacity") {
		t.Fatalf("red reasons = %v, want single_user_tps_capacity", builder.RedReasons())
	}

	signals.UserTPS = 22
	qosLimit, baseLimit, builder = enforceCleanUserTPSCapacityLimit(cfg, Input{GlobalLimit: 50}, signals, decision.NewBuilder(nil, nil), 50, 40, true)
	if qosLimit != 30 || baseLimit != 30 {
		t.Fatalf("yellow qos/base limits = %d/%d, want 30/30", qosLimit, baseLimit)
	}
	if !containsString(builder.YellowReasons(), "single_user_tps_capacity") {
		t.Fatalf("yellow reasons = %v, want single_user_tps_capacity", builder.YellowReasons())
	}
}
