package predictive

import "testing"

var benchmarkRequestAwareDecision RequestAwareDecision

func BenchmarkRequestAwarePolicyEvaluate(b *testing.B) {
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		SoftKVRatio: 0.60,
		HardKVRatio: 0.90,
		TPSTarget:   20,
		TPSFloor:    15,
		BlockSize:   16,
	})
	if err != nil {
		b.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	base := RequestAwareInput{
		MetricsFresh:          true,
		IdentityValid:         true,
		CapacityTokens:        10_000,
		UsedTokens:            2_000,
		RequestReservedTokens: 800,
		SelectionInputTokens:  500,
		Running:               4,
		EffectiveSequences:    4,
		AggregateTPSProxy:     80,
		MeanActiveTPSProxy:    20,
		TPSValid:              true,
	}

	cases := []struct {
		name  string
		input RequestAwareInput
	}{
		{name: "open", input: base},
		{name: "selective", input: func() RequestAwareInput {
			input := base
			input.Waiting = 1
			input.EffectiveSequences = 5
			return input
		}()},
		{name: "hard-kv", input: func() RequestAwareInput {
			input := base
			input.UsedTokens = 8_992
			return input
		}()},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				benchmarkRequestAwareDecision = policy.Evaluate(test.input)
			}
		})
	}
}
