package predictive

import (
	"testing"
	"time"
)

var learnedSchedulerBenchmarkPrediction SchedulerPrediction

func BenchmarkLearnedSchedulerPredictCalibratedTPSAndLatency(b *testing.B) {
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
			ObservedAt:   now.Add(-time.Duration(index) * time.Second),
			UserTPSRatio: 0.70 + float64(index%4)*0.05,
			UserTPSValid: true,
			TTFTRatio:    0.80 + float64(index%4)*0.05,
			TTFTValid:    true,
		}
	}
	scheduler.cells[key] = &residualCell{CreatedSequence: 1, Samples: samples}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		learnedSchedulerBenchmarkPrediction = scheduler.Predict(now, state, cost)
	}
}

func BenchmarkLearnedSchedulerPredictGlobalFallbackBounded(b *testing.B) {
	for _, benchmark := range []struct {
		name           string
		minimumSamples int
		maximumSamples int
		maximumCells   int
	}{
		{name: "default_bound", minimumSamples: 3, maximumSamples: 64, maximumCells: 64},
		{name: "hard_bound", minimumSamples: 256, maximumSamples: 256, maximumCells: 256},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			config := testResidualConfig()
			config.MinimumSamples = benchmark.minimumSamples
			config.MaximumSamplesPerCell = benchmark.maximumSamples
			config.MaximumCells = benchmark.maximumCells
			scheduler, err := NewLearnedScheduler(testLearnedProfile(), config)
			if err != nil {
				b.Fatalf("new learned scheduler: %v", err)
			}
			now := time.Unix(81_000, 0)
			state := learnedTestState()
			cost := learnedTestCost()
			features := schedulerFeatures(state, cost)
			scheduler.globalSamples = make([]residualSample, scheduler.globalLimit)
			for index := range scheduler.globalSamples {
				scheduler.globalSamples[index] = residualSample{
					ObservedAt:   now.Add(-time.Duration(index%30) * time.Second),
					Features:     features,
					UserTPSRatio: 0.70 + float64(index%4)*0.05,
					TTFTRatio:    0.80 + float64(index%4)*0.05,
					TPOTRatio:    0.80 + float64(index%4)*0.05,
					UserTPSValid: true,
					TTFTValid:    true,
					TPOTValid:    true,
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				learnedSchedulerBenchmarkPrediction = scheduler.Predict(now, state, cost)
			}
		})
	}
}
