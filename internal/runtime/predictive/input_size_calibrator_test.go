package predictive

import (
	"testing"
	"time"
)

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

func inputSizeCalibratorTestConfig() InputSizeCalibratorConfig {
	return InputSizeCalibratorConfig{
		EstimatorVersion: "approx-json-v1", MinimumSamples: 4,
		MaximumSamplesPerClass: 8, MaxAge: time.Minute,
		UpperQuantile: 0.9, SafetyMargin: 1.10,
		MinimumMultiplier: 0.25, MaximumMultiplier: 4,
		ColdConfidence: 0.90, LearnedConfidence: 0.98,
	}
}
