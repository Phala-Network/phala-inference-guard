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
	EstimatorVersion     string
	Class                RequestClass
	EstimatedAt          time.Time
	RawInputTokensLow    int64
	RawInputTokensHigh   int64
	ApproximateTokenHint int64
	InputTokensUpper     int64
	Known                bool
	HintKnown            bool
	HintUsed             bool
	Source               InputSizeSource
	Samples              int
	HintSamples          int
	Confidence           float64
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
	SamplesAccepted       uint64
	SamplesRejected       uint64
	Invalidations         uint64
	SamplesStored         int
	Classes               int
	EstimatesCold         uint64
	EstimatesLearned      uint64
	HintSamplesStored     int
	HintInvalidations     uint64
	HintEstimatesUsed     uint64
	HintEstimatesFallback uint64
	HintEstimatesMissing  uint64
	LastSource            InputSizeSource
	LastSamples           int
	LastRawHigh           int64
	LastUpper             int64
	LastHint              int64
	LastHintSamples       int
	LastHintKnown         bool
	LastHintUsed          bool
}

type inputSizeRatioSample struct {
	RawRatio   float64
	RawKnown   bool
	HintRatio  float64
	HintKnown  bool
	ObservedAt time.Time
}

type InputSizeCalibrator struct {
	mu                sync.Mutex
	config            InputSizeCalibratorConfig
	samples           map[RequestClass][]inputSizeRatioSample
	ratioScratch      []float64
	samplesAccepted   uint64
	samplesRejected   uint64
	invalidations     uint64
	hintInvalidations uint64
	estimatesCold     atomic.Uint64
	estimatesLearned  atomic.Uint64
	hintUsed          atomic.Uint64
	hintFallback      atomic.Uint64
	hintMissing       atomic.Uint64
	lastSource        atomic.Uint32
	lastSamples       atomic.Int64
	lastRawHigh       atomic.Int64
	lastUpper         atomic.Int64
	lastHint          atomic.Int64
	lastHintSamples   atomic.Int64
	lastHintKnown     atomic.Bool
	lastHintUsed      atomic.Bool
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
	return c.estimate(now, class, rawLow, rawHigh, 0, false)
}

// EstimateWithHint optionally uses a model-neutral lexical reference after it
// has enough qualified samples. A missing or invalid hint is byte-for-byte the
// same estimate contract as Estimate and can never create an unknown result.
func (c *InputSizeCalibrator) EstimateWithHint(now time.Time, class RequestClass, rawLow, rawHigh, hint int64, hintKnown bool) InputSizeEstimate {
	hintKnown = hintKnown && hint > 0
	if !hintKnown {
		hint = 0
	}
	return c.estimate(now, class, rawLow, rawHigh, hint, hintKnown)
}

func (c *InputSizeCalibrator) estimate(now time.Time, class RequestClass, rawLow, rawHigh, hint int64, hintKnown bool) InputSizeEstimate {
	estimate := InputSizeEstimate{
		Class:                class,
		EstimatedAt:          now,
		RawInputTokensLow:    rawLow,
		RawInputTokensHigh:   rawHigh,
		ApproximateTokenHint: hint,
		HintKnown:            hintKnown,
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
	rawSamples := 0
	hintSamples := 0
	for _, sample := range samples {
		if sample.RawKnown {
			rawSamples++
		}
		if sample.HintKnown {
			hintSamples++
		}
	}
	estimate.HintSamples = hintSamples
	useHint := hintKnown && hintSamples >= c.config.MinimumSamples
	selectedSamples := rawSamples
	if useHint {
		selectedSamples = hintSamples
	}
	if selectedSamples < c.config.MinimumSamples {
		c.mu.Unlock()
		c.recordEstimate(estimate)
		return estimate
	}
	// The calibrator serializes sample access already. Reuse one bounded scratch
	// slice while holding that lock so mature hot-path estimates do not allocate
	// a fresh ratio slice for every request.
	ratios := c.ratioScratch[:0]
	for _, sample := range samples {
		if useHint && sample.HintKnown {
			ratios = append(ratios, sample.HintRatio)
		} else if !useHint && sample.RawKnown {
			ratios = append(ratios, sample.RawRatio)
		}
	}
	multiplier := quantileInPlace(ratios, c.config.UpperQuantile) * c.config.SafetyMargin
	sampleCount := len(ratios)
	c.ratioScratch = ratios
	c.mu.Unlock()

	if multiplier < c.config.MinimumMultiplier {
		multiplier = c.config.MinimumMultiplier
	}
	reference := rawHigh
	if useHint {
		reference = hint
	}
	upper := scaleTokensCeiling(reference, multiplier)
	if upper < rawLow {
		upper = rawLow
	}
	estimate.InputTokensUpper = upper
	estimate.Source = InputSizeSourceLearned
	estimate.Samples = sampleCount
	estimate.HintUsed = useHint
	estimate.Confidence = c.config.LearnedConfidence
	c.recordEstimate(estimate)
	return estimate
}

func (c *InputSizeCalibrator) Observe(outcome InputSizeOutcome) error {
	return c.ObserveWithHint(outcome, 0, false)
}

// ObserveWithHint qualifies the optional hint independently of the existing
// raw interval. A bad hint is discarded without rejecting a usable raw sample;
// a missing hint preserves the original Observe behavior.
func (c *InputSizeCalibrator) ObserveWithHint(outcome InputSizeOutcome, hint int64, hintKnown bool) error {
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
	rawRatio := float64(outcome.ActualPromptTokens) / float64(outcome.RawInputTokensHigh)
	if !positiveFinite(rawRatio) {
		return c.reject(fmt.Errorf("input-size outcome ratio is invalid"))
	}
	if rawRatio > c.config.MaximumMultiplier {
		c.mu.Lock()
		delete(c.samples, outcome.Class)
		c.samplesRejected++
		c.invalidations++
		c.mu.Unlock()
		return fmt.Errorf("input-size outcome ratio %.6g is outside configured bounds", rawRatio)
	}
	rawKnown := rawRatio >= c.config.MinimumMultiplier
	hintKnown = hintKnown && hint > 0
	var hintRatio float64
	qualifiedHint := false
	invalidateHint := false
	if hintKnown {
		hintRatio = float64(outcome.ActualPromptTokens) / float64(hint)
		qualifiedHint = positiveFinite(hintRatio) && hintRatio >= c.config.MinimumMultiplier && hintRatio <= c.config.MaximumMultiplier
		invalidateHint = positiveFinite(hintRatio) && hintRatio > c.config.MaximumMultiplier
	}
	if !rawKnown && !qualifiedHint {
		if invalidateHint {
			c.mu.Lock()
			c.invalidateHintLearningLocked(outcome.Class)
			c.hintInvalidations++
			c.mu.Unlock()
		}
		return c.reject(fmt.Errorf("input-size outcome ratio %.6g is below configured bounds", rawRatio))
	}

	c.mu.Lock()
	if invalidateHint {
		c.invalidateHintLearningLocked(outcome.Class)
		c.hintInvalidations++
	}
	samples := append(c.samples[outcome.Class], inputSizeRatioSample{
		RawRatio:   rawRatio,
		RawKnown:   rawKnown,
		HintRatio:  hintRatio,
		HintKnown:  qualifiedHint,
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
	hintStored := 0
	for _, samples := range c.samples {
		stored += len(samples)
		for _, sample := range samples {
			if sample.HintKnown {
				hintStored++
			}
		}
	}
	return InputSizeCalibratorSnapshot{
		SamplesAccepted:       c.samplesAccepted,
		SamplesRejected:       c.samplesRejected,
		Invalidations:         c.invalidations,
		SamplesStored:         stored,
		Classes:               len(c.samples),
		EstimatesCold:         c.estimatesCold.Load(),
		EstimatesLearned:      c.estimatesLearned.Load(),
		HintSamplesStored:     hintStored,
		HintInvalidations:     c.hintInvalidations,
		HintEstimatesUsed:     c.hintUsed.Load(),
		HintEstimatesFallback: c.hintFallback.Load(),
		HintEstimatesMissing:  c.hintMissing.Load(),
		LastSource:            inputSizeSourceFromCode(c.lastSource.Load()),
		LastSamples:           int(c.lastSamples.Load()),
		LastRawHigh:           c.lastRawHigh.Load(),
		LastUpper:             c.lastUpper.Load(),
		LastHint:              c.lastHint.Load(),
		LastHintSamples:       int(c.lastHintSamples.Load()),
		LastHintKnown:         c.lastHintKnown.Load(),
		LastHintUsed:          c.lastHintUsed.Load(),
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
	c.lastHint.Store(estimate.ApproximateTokenHint)
	c.lastHintSamples.Store(int64(estimate.HintSamples))
	c.lastHintKnown.Store(estimate.HintKnown)
	c.lastHintUsed.Store(estimate.HintUsed)
	if !estimate.HintKnown {
		c.hintMissing.Add(1)
	} else if estimate.HintUsed {
		c.hintUsed.Add(1)
	} else {
		c.hintFallback.Add(1)
	}
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

func (c *InputSizeCalibrator) invalidateHintLearningLocked(class RequestClass) {
	samples := c.samples[class]
	kept := samples[:0]
	for _, sample := range samples {
		sample.HintRatio = 0
		sample.HintKnown = false
		if sample.RawKnown {
			kept = append(kept, sample)
		}
	}
	if len(kept) == 0 {
		delete(c.samples, class)
		return
	}
	c.samples[class] = kept
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
