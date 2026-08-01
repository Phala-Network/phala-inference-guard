package predictive

import (
	"testing"
	"time"
)

var learnedSchedulerBenchmarkPrediction SchedulerPrediction

func BenchmarkLearnedSchedulerPredictCalibratedTTFT(b *testing.B) {
	scheduler, err := NewLearnedScheduler(testLearnedProfile(), testResidualConfig())
	if err != nil {
		b.Fatalf("new learned scheduler: %v", err)
	}
	now := time.Unix(80_000, 0)
	state := learnedTestState()
	cost := learnedTestCost()
	key := scheduler.featureCell(schedulerFeatures(state, cost))
	samples := make([]residualSample, scheduler.config.MaximumSamplesPerCell)
	for index := range samples {
		samples[index] = residualSample{
			ObservedAt: now.Add(-time.Duration(index) * time.Second),
			TTFTRatio:  0.80 + float64(index%4)*0.05,
			TTFTValid:  true,
		}
	}
	scheduler.cells[key] = &residualCell{CreatedSequence: 1, Samples: samples}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		learnedSchedulerBenchmarkPrediction = scheduler.Predict(now, state, cost)
	}
}
