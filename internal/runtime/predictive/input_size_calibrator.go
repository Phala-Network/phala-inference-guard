package predictive

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type InputSizeSource string

const (
	InputSizeSourceCold    InputSizeSource = "cold"
	InputSizeSourceLearned InputSizeSource = "learned"
)

type InputSizeCalibratorConfig struct {
	EstimatorVersion       string
	MinimumSamples         int
	MaximumSamplesPerClass int
	MaxAge                 time.Duration
	UpperQuantile          float64
	SafetyMargin           float64
	MinimumMultiplier      float64
	MaximumMultiplier      float64
	ColdConfidence         float64
	LearnedConfidence      float64
}

func (c InputSizeCalibratorConfig) Validate() error {
	if c.EstimatorVersion == "" {
		return fmt.Errorf("input-size estimator version is required")
	}
	if c.MinimumSamples <= 0 || c.MaximumSamplesPerClass < c.MinimumSamples {
		return fmt.Errorf("input-size sample bounds are invalid")
	}
	if c.MaxAge <= 0 {
		return fmt.Errorf("input-size sample maximum age must be positive")
	}
	if !positiveFinite(c.UpperQuantile) || c.UpperQuantile > 1 {
		return fmt.Errorf("input-size upper quantile must be in (0, 1]")
	}
	if !positiveFinite(c.SafetyMargin) || c.SafetyMargin < 1 {
		return fmt.Errorf("input-size safety margin must be finite and at least one")
	}
	if !positiveFinite(c.MinimumMultiplier) || c.MinimumMultiplier > 1 ||
		!positiveFinite(c.MaximumMultiplier) || c.MaximumMultiplier < 1 ||
		c.MaximumMultiplier < c.MinimumMultiplier {
		return fmt.Errorf("input-size multiplier bounds are invalid")
	}
	if !positiveFinite(c.ColdConfidence) || c.ColdConfidence > 1 ||
		!positiveFinite(c.LearnedConfidence) || c.LearnedConfidence > 1 ||
		c.LearnedConfidence < c.ColdConfidence {
		return fmt.Errorf("input-size confidence bounds are invalid")
	}
	return nil
}

type InputSizeEstimate struct {
	EstimatorVersion   string
	Class              RequestClass
	EstimatedAt        time.Time
	RawInputTokensLow  int64
	RawInputTokensHigh int64
	InputTokensUpper   int64
	Known              bool
	Source             InputSizeSource
	Samples            int
	Confidence         float64
}

type InputSizeOutcome struct {
	EstimatorVersion   string
	Class              RequestClass
	RawInputTokensHigh int64
	ActualPromptTokens int64
	ObservedAt         time.Time
	Attributed         bool
	Censored           bool
}

type InputSizeCalibratorSnapshot struct {
	SamplesAccepted  uint64
	SamplesRejected  uint64
	Invalidations    uint64
	SamplesStored    int
	Classes          int
	EstimatesCold    uint64
	EstimatesLearned uint64
	LastSource       InputSizeSource
	LastSamples      int
	LastRawHigh      int64
	LastUpper        int64
}

type inputSizeRatioSample struct {
	Ratio      float64
	ObservedAt time.Time
}

type InputSizeCalibrator struct {
	mu               sync.Mutex
	config           InputSizeCalibratorConfig
	samples          map[RequestClass][]inputSizeRatioSample
	samplesAccepted  uint64
	samplesRejected  uint64
	invalidations    uint64
	estimatesCold    atomic.Uint64
	estimatesLearned atomic.Uint64
	lastSource       atomic.Uint32
	lastSamples      atomic.Int64
	lastRawHigh      atomic.Int64
	lastUpper        atomic.Int64
}

func NewInputSizeCalibrator(config InputSizeCalibratorConfig) (*InputSizeCalibrator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &InputSizeCalibrator{
		config:  config,
		samples: make(map[RequestClass][]inputSizeRatioSample, supportedRequestClassCount),
	}, nil
}

func (c *InputSizeCalibrator) Estimate(now time.Time, class RequestClass, rawLow, rawHigh int64) InputSizeEstimate {
	estimate := InputSizeEstimate{
		Class:              class,
		EstimatedAt:        now,
		RawInputTokensLow:  rawLow,
		RawInputTokensHigh: rawHigh,
	}
	if c == nil || now.IsZero() || !supportedRequestClass(class) || rawLow <= 0 || rawHigh < rawLow {
		return estimate
	}
	estimate.EstimatorVersion = c.config.EstimatorVersion
	estimate.InputTokensUpper = rawHigh
	estimate.Known = true
	estimate.Source = InputSizeSourceCold
	estimate.Confidence = c.config.ColdConfidence

	c.mu.Lock()
	samples := c.freshSamplesLocked(now, class)
	if len(samples) < c.config.MinimumSamples {
		c.mu.Unlock()
		c.recordEstimate(estimate)
		return estimate
	}
	ratios := make([]float64, len(samples))
	for index, sample := range samples {
		ratios[index] = sample.Ratio
	}
	c.mu.Unlock()

	multiplier := quantileInPlace(ratios, c.config.UpperQuantile) * c.config.SafetyMargin
	if multiplier < c.config.MinimumMultiplier {
		multiplier = c.config.MinimumMultiplier
	}
	upper := scaleTokensCeiling(rawHigh, multiplier)
	if upper < rawLow {
		upper = rawLow
	}
	estimate.InputTokensUpper = upper
	estimate.Source = InputSizeSourceLearned
	estimate.Samples = len(ratios)
	estimate.Confidence = c.config.LearnedConfidence
	c.recordEstimate(estimate)
	return estimate
}

func (c *InputSizeCalibrator) Observe(outcome InputSizeOutcome) error {
	if c == nil {
		return fmt.Errorf("input-size calibrator is nil")
	}
	if outcome.EstimatorVersion != c.config.EstimatorVersion {
		return c.reject(fmt.Errorf("input-size estimator version mismatch"))
	}
	if !supportedRequestClass(outcome.Class) {
		return c.reject(ErrUnsupportedRequestClass)
	}
	if !outcome.Attributed {
		return c.reject(fmt.Errorf("input-size outcome is not sufficiently attributed"))
	}
	if outcome.Censored {
		return c.reject(fmt.Errorf("input-size outcome is censored"))
	}
	if outcome.ObservedAt.IsZero() || outcome.RawInputTokensHigh <= 0 || outcome.ActualPromptTokens <= 0 {
		return c.reject(fmt.Errorf("input-size outcome is invalid"))
	}
	ratio := float64(outcome.ActualPromptTokens) / float64(outcome.RawInputTokensHigh)
	if !positiveFinite(ratio) {
		return c.reject(fmt.Errorf("input-size outcome ratio is invalid"))
	}
	if ratio < c.config.MinimumMultiplier || ratio > c.config.MaximumMultiplier {
		c.mu.Lock()
		delete(c.samples, outcome.Class)
		c.samplesRejected++
		c.invalidations++
		c.mu.Unlock()
		return fmt.Errorf("input-size outcome ratio %.6g is outside configured bounds", ratio)
	}

	c.mu.Lock()
	samples := append(c.samples[outcome.Class], inputSizeRatioSample{
		Ratio:      ratio,
		ObservedAt: outcome.ObservedAt,
	})
	if excess := len(samples) - c.config.MaximumSamplesPerClass; excess > 0 {
		copy(samples, samples[excess:])
		samples = samples[:c.config.MaximumSamplesPerClass]
	}
	c.samples[outcome.Class] = samples
	c.samplesAccepted++
	c.mu.Unlock()
	return nil
}

func (c *InputSizeCalibrator) InvalidateLearning() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.samples)
	c.invalidations++
	c.mu.Unlock()
}

func (c *InputSizeCalibrator) Snapshot(now time.Time) InputSizeCalibratorSnapshot {
	if c == nil {
		return InputSizeCalibratorSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.IsZero() {
		for class := range c.samples {
			c.freshSamplesLocked(now, class)
		}
	}
	stored := 0
	for _, samples := range c.samples {
		stored += len(samples)
	}
	return InputSizeCalibratorSnapshot{
		SamplesAccepted:  c.samplesAccepted,
		SamplesRejected:  c.samplesRejected,
		Invalidations:    c.invalidations,
		SamplesStored:    stored,
		Classes:          len(c.samples),
		EstimatesCold:    c.estimatesCold.Load(),
		EstimatesLearned: c.estimatesLearned.Load(),
		LastSource:       inputSizeSourceFromCode(c.lastSource.Load()),
		LastSamples:      int(c.lastSamples.Load()),
		LastRawHigh:      c.lastRawHigh.Load(),
		LastUpper:        c.lastUpper.Load(),
	}
}

func (c *InputSizeCalibrator) recordEstimate(estimate InputSizeEstimate) {
	if c == nil || !estimate.Known {
		return
	}
	switch estimate.Source {
	case InputSizeSourceCold:
		c.estimatesCold.Add(1)
		c.lastSource.Store(1)
	case InputSizeSourceLearned:
		c.estimatesLearned.Add(1)
		c.lastSource.Store(2)
	default:
		return
	}
	c.lastSamples.Store(int64(estimate.Samples))
	c.lastRawHigh.Store(estimate.RawInputTokensHigh)
	c.lastUpper.Store(estimate.InputTokensUpper)
}

func inputSizeSourceFromCode(code uint32) InputSizeSource {
	switch code {
	case 1:
		return InputSizeSourceCold
	case 2:
		return InputSizeSourceLearned
	default:
		return ""
	}
}

func (c *InputSizeCalibrator) freshSamplesLocked(now time.Time, class RequestClass) []inputSizeRatioSample {
	samples := c.samples[class]
	kept := samples[:0]
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > c.config.MaxAge {
			continue
		}
		kept = append(kept, sample)
	}
	if len(kept) == 0 {
		delete(c.samples, class)
		return nil
	}
	c.samples[class] = kept
	return kept
}

func (c *InputSizeCalibrator) reject(err error) error {
	c.mu.Lock()
	c.samplesRejected++
	c.mu.Unlock()
	return err
}

func supportedRequestClass(class RequestClass) bool {
	switch class {
	case RequestClassCompletion, RequestClassChat, RequestClassResponses:
		return true
	default:
		return false
	}
}

const supportedRequestClassCount = 3

func scaleTokensCeiling(tokens int64, multiplier float64) int64 {
	if tokens <= 0 || !positiveFinite(multiplier) {
		return 0
	}
	scaled := float64(tokens) * multiplier
	if scaled >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if scaled <= 1 {
		return 1
	}
	return int64(math.Ceil(scaled))
}
