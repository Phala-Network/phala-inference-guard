package predictive

import (
	"reflect"
	"testing"
	"time"
)

type optionalInputTokenHintCalibrator interface {
	EstimateWithHint(time.Time, RequestClass, int64, int64, int64, bool) InputSizeEstimate
	ObserveWithHint(InputSizeOutcome, int64, bool) error
}

func TestInputSizeCalibratorStartsColdAndLearnsOnlyFutureEstimates(t *testing.T) {
	now := time.Unix(10_000, 0)
	calibrator, err := NewInputSizeCalibrator(inputSizeCalibratorTestConfig())
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	cold := calibrator.Estimate(now, RequestClassChat, 100, 200)
	if !cold.Known || cold.Source != InputSizeSourceCold || cold.InputTokensUpper != 200 || cold.Samples != 0 {
		t.Fatalf("cold estimate = %+v", cold)
	}
	for index, actual := range []int64{100, 110, 120, 130} {
		before := calibrator.Estimate(now.Add(time.Duration(index)*time.Second), RequestClassChat, 100, 200)
		if before.Source != InputSizeSourceCold {
			t.Fatalf("sample %d changed its own pre-observation estimate: %+v", index, before)
		}
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 200, ActualPromptTokens: actual,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}); err != nil {
			t.Fatalf("observe input-size sample %d: %v", index, err)
		}
	}
	learned := calibrator.Estimate(now.Add(5*time.Second), RequestClassChat, 100, 200)
	if !learned.Known || learned.Source != InputSizeSourceLearned || learned.Samples != 4 {
		t.Fatalf("learned estimate identity = %+v", learned)
	}
	if learned.InputTokensUpper >= cold.InputTokensUpper || learned.InputTokensUpper < 130 {
		t.Fatalf("learned upper = %d, want safely narrower than %d and >= observed maximum", learned.InputTokensUpper, cold.InputTokensUpper)
	}
}

func TestInputSizeCalibratorRaisesFutureUpperAfterQualifiedUnderestimation(t *testing.T) {
	now := time.Unix(20_000, 0)
	calibrator, err := NewInputSizeCalibrator(inputSizeCalibratorTestConfig())
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index, actual := range []int64{140, 150, 160, 170} {
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassCompletion,
			RawInputTokensHigh: 100, ActualPromptTokens: actual,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}); err != nil {
			t.Fatalf("observe underestimation sample %d: %v", index, err)
		}
	}
	learned := calibrator.Estimate(now.Add(5*time.Second), RequestClassCompletion, 50, 100)
	if !learned.Known || learned.Source != InputSizeSourceLearned || learned.InputTokensUpper <= 170 {
		t.Fatalf("underestimation did not raise the future upper: %+v", learned)
	}
}

func TestInputSizeCalibratorFallsBackColdWithoutPermanentLockout(t *testing.T) {
	now := time.Unix(30_000, 0)
	config := inputSizeCalibratorTestConfig()
	config.MaximumMultiplier = 2
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < config.MinimumSamples; index++ {
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassResponses,
			RawInputTokensHigh: 100, ActualPromptTokens: 50,
			ObservedAt: now.Add(time.Duration(index) * time.Millisecond), Attributed: true,
		}); err != nil {
			t.Fatalf("prime optimistic calibration %d: %v", index, err)
		}
	}
	learned := calibrator.Estimate(now.Add(time.Second), RequestClassResponses, 50, 100)
	if !learned.Known || learned.Source != InputSizeSourceLearned || learned.InputTokensUpper >= 100 {
		t.Fatalf("optimistic calibration was not active before invalidation: %+v", learned)
	}
	if err := calibrator.Observe(InputSizeOutcome{
		EstimatorVersion: "approx-json-v1", Class: RequestClassResponses,
		RawInputTokensHigh: 100, ActualPromptTokens: 300,
		ObservedAt: now.Add(2 * time.Second), Attributed: true,
	}); err == nil {
		t.Fatal("out-of-range underestimation was accepted")
	}
	fallback := calibrator.Estimate(now.Add(3*time.Second), RequestClassResponses, 50, 100)
	if !fallback.Known || fallback.Source != InputSizeSourceCold || fallback.InputTokensUpper != 100 || fallback.Samples != 0 {
		t.Fatalf("invalid calibration did not return usable cold estimate: %+v", fallback)
	}
	snapshot := calibrator.Snapshot(now.Add(3 * time.Second))
	if snapshot.SamplesStored != 0 || snapshot.SamplesAccepted != uint64(config.MinimumSamples) || snapshot.SamplesRejected != 1 || snapshot.Invalidations != 1 {
		t.Fatalf("invalidation snapshot = %+v", snapshot)
	}
}

func TestInputSizeCalibratorSafeLowRatioRejectPreservesMatureLearning(t *testing.T) {
	now := time.Unix(32_000, 0)
	config := inputSizeCalibratorTestConfig()
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index, actual := range []int64{50, 55, 60, 65} {
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 100, ActualPromptTokens: actual,
			ObservedAt: now.Add(time.Duration(index) * time.Millisecond), Attributed: true,
		}); err != nil {
			t.Fatalf("prime mature calibration %d: %v", index, err)
		}
	}
	before := calibrator.Estimate(now.Add(time.Second), RequestClassChat, 50, 100)
	if !before.Known || before.Source != InputSizeSourceLearned || before.Samples != config.MinimumSamples {
		t.Fatalf("calibration did not mature before low-ratio sample: %+v", before)
	}

	if err := calibrator.Observe(InputSizeOutcome{
		EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
		RawInputTokensHigh: 100, ActualPromptTokens: 10,
		ObservedAt: now.Add(2 * time.Second), Attributed: true,
	}); err == nil {
		t.Fatal("safe low-ratio sample was accepted")
	}
	after := calibrator.Estimate(now.Add(3*time.Second), RequestClassChat, 50, 100)
	if !after.Known || after.Source != InputSizeSourceLearned || after.Samples != config.MinimumSamples {
		t.Fatalf("safe low-ratio rejection destroyed mature learning: before=%+v after=%+v", before, after)
	}
	snapshot := calibrator.Snapshot(now.Add(3 * time.Second))
	if snapshot.SamplesStored != config.MinimumSamples || snapshot.SamplesAccepted != uint64(config.MinimumSamples) || snapshot.SamplesRejected != 1 || snapshot.Invalidations != 0 {
		t.Fatalf("safe low-ratio rejection changed established safety state: %+v", snapshot)
	}
}

func TestInputSizeCalibratorSparseLowFlowNeverSelfLocks(t *testing.T) {
	now := time.Unix(35_000, 0)
	config := inputSizeCalibratorTestConfig()
	config.MinimumSamples = 8
	config.MaxAge = 2 * time.Second
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < 20; index++ {
		at := now.Add(time.Duration(index) * 3 * time.Second)
		estimate := calibrator.Estimate(at, RequestClassChat, 50, 100)
		if !estimate.Known || estimate.Source != InputSizeSourceCold || estimate.InputTokensUpper != 100 {
			t.Fatalf("sparse request %d self-locked instead of using cold estimate: %+v", index, estimate)
		}
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 100, ActualPromptTokens: 80,
			ObservedAt: at, Attributed: true,
		}); err != nil {
			t.Fatalf("observe sparse sample %d: %v", index, err)
		}
	}
	final := calibrator.Estimate(now.Add(60*time.Second), RequestClassChat, 50, 100)
	if !final.Known || final.Source != InputSizeSourceCold || final.InputTokensUpper != 100 {
		t.Fatalf("expired sparse learner became sticky: %+v", final)
	}
}

func TestInputSizeCalibratorBoundsSamplesRejectsCensoredAndExpires(t *testing.T) {
	now := time.Unix(40_000, 0)
	config := inputSizeCalibratorTestConfig()
	config.MaximumSamplesPerClass = 4
	config.MaxAge = 10 * time.Second
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < 12; index++ {
		if err := calibrator.Observe(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 100, ActualPromptTokens: 80 + int64(index%2),
			ObservedAt: now.Add(time.Duration(index) * time.Millisecond), Attributed: true,
		}); err != nil {
			t.Fatalf("observe bounded sample %d: %v", index, err)
		}
	}
	if err := calibrator.Observe(InputSizeOutcome{
		EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
		RawInputTokensHigh: 100, ActualPromptTokens: 80,
		ObservedAt: now.Add(time.Second), Attributed: true, Censored: true,
	}); err == nil {
		t.Fatal("censored size outcome was accepted")
	}
	snapshot := calibrator.Snapshot(now.Add(2 * time.Second))
	if snapshot.SamplesStored != 4 || snapshot.SamplesAccepted != 12 || snapshot.SamplesRejected != 1 || snapshot.Classes != 1 {
		t.Fatalf("bounded calibrator snapshot = %+v", snapshot)
	}
	expired := calibrator.Estimate(now.Add(20*time.Second), RequestClassChat, 50, 100)
	if !expired.Known || expired.Source != InputSizeSourceCold || expired.Samples != 0 {
		t.Fatalf("expired calibrator did not return cold: %+v", expired)
	}
}

func TestInputSizeCalibratorUsesOptionalHintOnlyForFutureEstimates(t *testing.T) {
	now := time.Unix(45_000, 0)
	config := inputSizeCalibratorTestConfig()
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	hinted, ok := any(calibrator).(optionalInputTokenHintCalibrator)
	if !ok {
		t.Fatal("input-size calibrator has no optional approximate-token hint interface")
	}

	fallback := calibrator.Estimate(now, RequestClassChat, 100, 400)
	missingHint := hinted.EstimateWithHint(now, RequestClassChat, 100, 400, 0, false)
	if !reflect.DeepEqual(missingHint, fallback) {
		t.Fatalf("missing hint did not preserve the existing estimator fallback: fallback=%+v hinted=%+v", fallback, missingHint)
	}

	cold := hinted.EstimateWithHint(now, RequestClassChat, 100, 400, 100, true)
	if !cold.Known || cold.Source != InputSizeSourceCold || cold.InputTokensUpper != 400 {
		t.Fatalf("an untrained hint changed cold admission instead of remaining reference-only: %+v", cold)
	}
	for index, actual := range []int64{190, 200, 210, 220} {
		before := hinted.EstimateWithHint(now.Add(time.Duration(index)*time.Second), RequestClassChat, 100, 400, 100, true)
		if before.Source != InputSizeSourceCold {
			t.Fatalf("sample %d changed its own pre-observation estimate: %+v", index, before)
		}
		if err := hinted.ObserveWithHint(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 400, ActualPromptTokens: actual,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}, 100, true); err != nil {
			t.Fatalf("observe hinted input-size sample %d: %v", index, err)
		}
	}

	learned := hinted.EstimateWithHint(now.Add(5*time.Second), RequestClassChat, 100, 400, 100, true)
	if !learned.Known || learned.Source != InputSizeSourceLearned || learned.Samples != config.MinimumSamples {
		t.Fatalf("qualified hint feedback did not affect a later estimate: %+v", learned)
	}
	if learned.InputTokensUpper >= cold.InputTokensUpper || learned.InputTokensUpper < 220 {
		t.Fatalf("hinted future upper = %d, want safely narrower than cold %d and >= observed maximum", learned.InputTokensUpper, cold.InputTokensUpper)
	}
	if cold.InputTokensUpper != 400 || cold.Source != InputSizeSourceCold {
		t.Fatalf("feedback retroactively changed the producing estimate: %+v", cold)
	}

	postLearningFallback := hinted.EstimateWithHint(now.Add(6*time.Second), RequestClassChat, 100, 400, -1, true)
	wantFallback := calibrator.Estimate(now.Add(6*time.Second), RequestClassChat, 100, 400)
	if !reflect.DeepEqual(postLearningFallback, wantFallback) {
		t.Fatalf("invalid hint became a new failure mode: fallback=%+v hinted=%+v", wantFallback, postLearningFallback)
	}
}

func TestInputSizeCalibratorCanLearnHintWhenRawFallbackIsSafelyTooHigh(t *testing.T) {
	now := time.Unix(46_000, 0)
	config := inputSizeCalibratorTestConfig()
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < config.MinimumSamples; index++ {
		if err := calibrator.ObserveWithHint(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 1_000, ActualPromptTokens: 100,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}, 50, true); err != nil {
			t.Fatalf("observe hint-only qualified sample %d: %v", index, err)
		}
	}

	rawFallback := calibrator.Estimate(now.Add(5*time.Second), RequestClassChat, 50, 1_000)
	if rawFallback.Source != InputSizeSourceCold || rawFallback.InputTokensUpper != 1_000 {
		t.Fatalf("hint-only evidence silently rewrote the raw fallback: %+v", rawFallback)
	}
	hinted := calibrator.EstimateWithHint(now.Add(5*time.Second), RequestClassChat, 50, 1_000, 50, true)
	if hinted.Source != InputSizeSourceLearned || !hinted.HintUsed || hinted.InputTokensUpper < 100 || hinted.InputTokensUpper >= 1_000 {
		t.Fatalf("qualified hint did not refine the later combined input estimate: %+v", hinted)
	}
}

func TestInputSizeCalibratorDiscardsBadHintWithoutPoisoningRawLearning(t *testing.T) {
	now := time.Unix(47_000, 0)
	config := inputSizeCalibratorTestConfig()
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < config.MinimumSamples; index++ {
		if err := calibrator.ObserveWithHint(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 100, ActualPromptTokens: 80,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}, 1, true); err != nil {
			t.Fatalf("bad optional hint rejected usable raw sample %d: %v", index, err)
		}
	}
	learned := calibrator.EstimateWithHint(now.Add(5*time.Second), RequestClassChat, 50, 100, 1, true)
	if learned.Source != InputSizeSourceLearned || learned.HintUsed || learned.InputTokensUpper <= 80 || learned.InputTokensUpper >= 100 {
		t.Fatalf("bad hint poisoned or replaced mature raw fallback: %+v", learned)
	}
	if snapshot := calibrator.Snapshot(now.Add(5 * time.Second)); snapshot.SamplesStored != config.MinimumSamples || snapshot.HintSamplesStored != 0 {
		t.Fatalf("bad hint was retained as qualified evidence: %+v", snapshot)
	}
}

func TestInputSizeCalibratorSevereHintUnderestimateInvalidatesOnlyHintChannel(t *testing.T) {
	now := time.Unix(48_000, 0)
	config := inputSizeCalibratorTestConfig()
	calibrator, err := NewInputSizeCalibrator(config)
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	for index := 0; index < config.MinimumSamples; index++ {
		if err := calibrator.ObserveWithHint(InputSizeOutcome{
			EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
			RawInputTokensHigh: 100, ActualPromptTokens: 80,
			ObservedAt: now.Add(time.Duration(index) * time.Second), Attributed: true,
		}, 80, true); err != nil {
			t.Fatalf("prime dual-channel sample %d: %v", index, err)
		}
	}
	before := calibrator.EstimateWithHint(now.Add(5*time.Second), RequestClassChat, 50, 100, 80, true)
	if !before.HintUsed {
		t.Fatalf("hint did not mature before shift: %+v", before)
	}
	if err := calibrator.ObserveWithHint(InputSizeOutcome{
		EstimatorVersion: "approx-json-v1", Class: RequestClassChat,
		RawInputTokensHigh: 100, ActualPromptTokens: 80,
		ObservedAt: now.Add(6 * time.Second), Attributed: true,
	}, 1, true); err != nil {
		t.Fatalf("severe bad hint rejected still-usable raw sample: %v", err)
	}
	after := calibrator.EstimateWithHint(now.Add(7*time.Second), RequestClassChat, 50, 100, 80, true)
	if after.Source != InputSizeSourceLearned || after.HintUsed || after.InputTokensUpper <= 80 || after.InputTokensUpper >= 100 {
		t.Fatalf("severe hint shift did not fall back to preserved raw learning: before=%+v after=%+v", before, after)
	}
	if snapshot := calibrator.Snapshot(now.Add(7 * time.Second)); snapshot.HintSamplesStored != 0 || snapshot.HintInvalidations != 1 || snapshot.SamplesStored != config.MinimumSamples+1 {
		t.Fatalf("hint-only invalidation state = %+v", snapshot)
	}
}

func inputSizeCalibratorTestConfig() InputSizeCalibratorConfig {
	return InputSizeCalibratorConfig{
		EstimatorVersion: "approx-json-v1", MinimumSamples: 4,
		MaximumSamplesPerClass: 8, MaxAge: time.Minute,
		UpperQuantile: 0.9, SafetyMargin: 1.10,
		MinimumMultiplier: 0.25, MaximumMultiplier: 4,
		ColdConfidence: 0.90, LearnedConfidence: 0.98,
	}
}

func BenchmarkInputSizeCalibratorEstimateMature(b *testing.B) {
	for _, useHint := range []bool{false, true} {
		name := "raw"
		if useHint {
			name = "hint"
		}
		b.Run(name, func(b *testing.B) {
			now := time.Unix(90_000, 0)
			config := inputSizeCalibratorTestConfig()
			config.MaximumSamplesPerClass = 64
			calibrator, err := NewInputSizeCalibrator(config)
			if err != nil {
				b.Fatalf("new input-size calibrator: %v", err)
			}
			for index := 0; index < config.MaximumSamplesPerClass; index++ {
				outcome := InputSizeOutcome{
					EstimatorVersion:   config.EstimatorVersion,
					Class:              RequestClassChat,
					RawInputTokensHigh: 400,
					ActualPromptTokens: 100 + int64(index%4),
					ObservedAt:         now.Add(time.Duration(index) * time.Millisecond),
					Attributed:         true,
				}
				if err := calibrator.ObserveWithHint(outcome, 100, useHint); err != nil {
					b.Fatalf("prime mature input-size sample %d: %v", index, err)
				}
			}
			if estimate := calibrator.EstimateWithHint(now.Add(time.Second), RequestClassChat, 50, 400, 100, useHint); estimate.Source != InputSizeSourceLearned {
				b.Fatalf("input-size calibrator did not mature: %+v", estimate)
			}

			b.ReportAllocs()
			b.ResetTimer()
			var estimate InputSizeEstimate
			for index := 0; index < b.N; index++ {
				estimate = calibrator.EstimateWithHint(now.Add(time.Second), RequestClassChat, 50, 400, 100, useHint)
			}
			if !estimate.Known {
				b.Fatal("mature input-size estimate became unknown")
			}
		})
	}
}
