package server

import (
	"fmt"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

var predictiveShadowPendingPrefillBenchmarkSnapshot predictiveShadowPendingPrefillSnapshot

func BenchmarkPredictiveShadowPendingPrefillStoreSnapshot(b *testing.B) {
	for _, size := range []int{1, 2, defaultMaximumShadowObservations} {
		b.Run(fmt.Sprintf("active_%d", size), func(b *testing.B) {
			store := newPredictiveShadowPendingPrefillStore(size)
			for index := range size {
				if store.Begin(predictiveShadowPendingPrefillBenchmarkObservation(int64(index+1))) == nil {
					b.Fatalf("benchmark observation %d was not retained", index)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				predictiveShadowPendingPrefillBenchmarkSnapshot = store.Snapshot()
			}
		})
	}
}

func BenchmarkPredictiveShadowPendingPrefillStoreBeginEnd(b *testing.B) {
	store := newPredictiveShadowPendingPrefillStore(1)
	observation := predictiveShadowPendingPrefillBenchmarkObservation(1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		handle := store.Begin(observation)
		if handle == nil || !handle.End() {
			b.Fatal("benchmark lifecycle did not complete")
		}
	}
}

func predictiveShadowPendingPrefillBenchmarkObservation(tokens int64) runtimepredictive.PendingPrefillObservation {
	return runtimepredictive.PendingPrefillObservation{
		Tokens: tokens,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences:         1,
			DecodeSequences:                 2,
			ExistingPendingPrefillSequences: 0,
			PendingPrefillSequences:         1,
			ExistingActiveContextTokens:     1_000,
			ExistingUncachedPrefill:         0,
			ExistingPhysicalKVUpper:         1_000,
			ExistingActiveKVUpper:           1_000,
			RequestComplexityTokensUpper:    tokens,
			ActiveContextTokens:             1_000 + tokens,
			UncachedPrefillTokens:           tokens,
			PhysicalKVUpper:                 1_000 + tokens,
			ActiveKVUpper:                   1_000 + tokens,
		},
		DecisionManagerSequence: 1,
	}
}
