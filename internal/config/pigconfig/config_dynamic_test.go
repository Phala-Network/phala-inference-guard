package pigconfig

import (
	"testing"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
)

func TestLoadDynamicConfigReadsTTFTPolicy(t *testing.T) {
	t.Setenv("DYNAMIC_TTFT_TARGET_SECONDS", "2")
	t.Setenv("DYNAMIC_TTFT_RED_SECONDS", "4")
	t.Setenv("DYNAMIC_TTFT_P99_TARGET_SECONDS", "5")
	t.Setenv("DYNAMIC_TTFT_P99_RED_SECONDS", "9")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	policy := cfg.DynamicTTFTPolicy
	if policy.TargetSeconds != 2 {
		t.Fatalf("TargetSeconds = %v, want 2", policy.TargetSeconds)
	}
	if policy.RedSeconds != 4 {
		t.Fatalf("RedSeconds = %v, want 4", policy.RedSeconds)
	}
	if policy.P99TargetSeconds != 5 {
		t.Fatalf("P99TargetSeconds = %v, want 5", policy.P99TargetSeconds)
	}
	if policy.P99RedSeconds != 9 {
		t.Fatalf("P99RedSeconds = %v, want 9", policy.P99RedSeconds)
	}
}

func TestValidateDynamicTTFTConfigRejectsInvertedThresholds(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "p95 red below target",
			cfg: Config{
				DynamicTTFTEnabled: true,
				DynamicTTFTPolicy:  mustTTFTPolicy(3, 2, 3, 8),
			},
		},
		{
			name: "p99 red below target",
			cfg: Config{
				DynamicTTFTEnabled: true,
				DynamicTTFTPolicy:  mustTTFTPolicy(1, 3, 9, 8),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDynamicConfig(tt.cfg); err == nil {
				t.Fatalf("validateDynamicConfig accepted inverted TTFT thresholds")
			}
		})
	}
}

func mustTTFTPolicy(target, red, p99Target, p99Red float64) latency.Policy {
	return latency.Policy{
		TargetSeconds:    target,
		RedSeconds:       red,
		P99TargetSeconds: p99Target,
		P99RedSeconds:    p99Red,
	}
}
