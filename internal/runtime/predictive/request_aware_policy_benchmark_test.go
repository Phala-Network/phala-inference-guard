package predictive

import (
	"fmt"
	"testing"
	"time"
)

var benchmarkRequestAwareDecision RequestAwareDecision
var benchmarkRequestAwareManagerResult RequestAwareManagerResult

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
		MetricsFresh:           true,
		IdentityValid:          true,
		CapacityTokens:         4 * 1024 * 1024,
		UsedTokens:             100 * 1024,
		RequestReservedTokens:  32 * 1024,
		SelectionInputTokens:   32 * 1024,
		EstimatedPrefillTokens: 32 * 1024,
		Running:                4,
		EffectiveSequences:     4,
		AggregateTPSProxy:      80,
		MeanActiveTPSProxy:     20,
		TPSValid:               true,
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
			input.UsedTokens = 3_800_000
			return input
		}()},
		{name: "weighted-budget", input: func() RequestAwareInput {
			input := base
			input.EstimatedPrefillTokens = 100 * 1024
			input.PendingPrefillTokens = 200 * 1024
			return input
		}()},
		{name: "exclusive-concurrency", input: func() RequestAwareInput {
			input := base
			input.EstimatedPrefillTokens = 300 * 1024
			input.PendingLongPrefillSequences = 1
			return input
		}()},
		{name: "quiescent-busy", input: func() RequestAwareInput {
			input := base
			input.EstimatedPrefillTokens = 650 * 1024
			return input
		}()},
		{name: "quiescent-idle", input: func() RequestAwareInput {
			input := base
			input.EstimatedPrefillTokens = 650 * 1024
			input.Running = 0
			input.EffectiveSequences = 0
			input.AggregateTPSProxy = 0
			input.MeanActiveTPSProxy = 0
			input.TPSValid = false
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

func BenchmarkRequestAwareManagerDecide(b *testing.B) {
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
	for _, activeReservations := range []int{0, 48, 256} {
		b.Run(fmt.Sprintf("active-%d", activeReservations), func(b *testing.B) {
			manager := newRequestAwareTestManager(0)
			for index := range activeReservations {
				requestID := fmt.Sprintf("active-%d", index)
				manager.reservations[requestID] = reservation{
					ID:           requestID,
					Cost:         requestAwareManagerCost(8*1024, 1024),
					Assimilation: assimilationUnabsorbed,
				}
			}
			input := requestAwareManagerInput()
			input.CapacityTokens = 16 * 1024 * 1024
			input.Running = activeReservations
			input.EffectiveSequences = activeReservations
			input.AggregateTPSProxy = 0
			input.MeanActiveTPSProxy = 0
			input.TPSValid = false
			cost := requestAwareManagerCost(8*1024, 1024)

			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				benchmarkRequestAwareManagerResult = manager.DecideRequestAware(
					time.Unix(1, 0), "benchmark-request", cost, 8*1024, policy, input,
				)
			}
		})
	}
}
