package server

import (
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveCompletionBenchmarkReservation struct{}

func (predictiveCompletionBenchmarkReservation) MarkForwarded() bool { return true }

func (predictiveCompletionBenchmarkReservation) MarkPrefillComplete() bool { return true }

func (predictiveCompletionBenchmarkReservation) Terminate(runtimepredictive.TerminalCause) bool {
	return true
}

func (predictiveCompletionBenchmarkReservation) ObserveCompletion(predictiveCompletionObservation) bool {
	return true
}

func (predictiveCompletionBenchmarkReservation) ReleaseResources() bool { return true }

func BenchmarkPredictiveCompletionObserverStageTelemetry(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		var counters *predictiveCompletionObserverCounters
		if enabled {
			name = "enabled"
			counters = &predictiveCompletionObserverCounters{}
		}
		b.Run(name, func(b *testing.B) {
			started := time.Unix(1, 0)
			observer := predictiveResponseObserver{
				reservation:    predictiveCompletionBenchmarkReservation{},
				requestStarted: started,
				counters:       counters,
			}
			usage := openai.CompletionUsage{
				PromptTokens:     64,
				CompletionTokens: 16,
				ObservedAt:       started.Add(time.Second),
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if observer.counters != nil {
						observer.counters.attached.Add(1)
						observer.counters.claimed.Add(1)
					}
					observer.observeUsage(usage)
					observer.observeTerminal()
				}
			})
		})
	}
}
