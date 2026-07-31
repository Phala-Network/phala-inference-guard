package predictive

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type ModelIdentity struct {
	ProfileID        string
	BackendEpoch     string
	PredictorVersion string
}

func (i ModelIdentity) Validate() error {
	if i.ProfileID == "" {
		return fmt.Errorf("scheduler profile id is required")
	}
	if i.BackendEpoch == "" {
		return fmt.Errorf("scheduler backend epoch is required")
	}
	if i.PredictorVersion == "" {
		return fmt.Errorf("scheduler predictor version is required")
	}
	return nil
}

type StaticSchedulerProfile struct {
	Identity                      ModelIdentity
	BaseCompletionTPS             float64
	PrefillTPSPenaltyPerKToken    float64
	BaseTTFT                      time.Duration
	TTFTPerUncachedPrefillToken   time.Duration
	BaseTPOT                      time.Duration
	TPOTPerExistingDecodeSequence time.Duration
	WorkspaceRiskUpper            float64
	PreemptionRiskUpper           float64
	Confidence                    float64
}

func (p StaticSchedulerProfile) Validate() error {
	if err := p.Identity.Validate(); err != nil {
		return err
	}
	if !positiveFinite(p.BaseCompletionTPS) {
		return fmt.Errorf("scheduler base completion TPS must be finite and positive")
	}
	if !nonNegativeFinite(p.PrefillTPSPenaltyPerKToken) {
		return fmt.Errorf("scheduler prefill TPS penalty must be finite and non-negative")
	}
	if p.BaseTTFT <= 0 || p.TTFTPerUncachedPrefillToken < 0 {
		return fmt.Errorf("scheduler TTFT profile is invalid")
	}
	if p.BaseTPOT <= 0 || p.TPOTPerExistingDecodeSequence < 0 {
		return fmt.Errorf("scheduler TPOT profile is invalid")
	}
	if !nonNegativeFinite(p.WorkspaceRiskUpper) || !nonNegativeFinite(p.PreemptionRiskUpper) {
		return fmt.Errorf("scheduler risk profile must be finite and non-negative")
	}
	if !positiveFinite(p.Confidence) || p.Confidence > 1 {
		return fmt.Errorf("scheduler confidence must be in (0, 1]")
	}
	return nil
}

type ResidualCalibratorConfig struct {
	Identity                 ModelIdentity
	MinimumSamples           int
	MaximumSamplesPerCell    int
	MaxAge                   time.Duration
	LowerQuantile            float64
	UpperQuantile            float64
	MinimumTPSMultiplier     float64
	MaximumTPSMultiplier     float64
	MinimumLatencyMultiplier float64
	MaximumLatencyMultiplier float64
	CalibratedConfidence     float64
	DecodeSequenceBucket     int
	ContextTokenBucket       int64
	PrefillTokenBucket       int64
	KVTokenBucket            int64
}

func (c ResidualCalibratorConfig) Validate() error {
	if err := c.Identity.Validate(); err != nil {
		return err
	}
	if c.MinimumSamples <= 0 || c.MaximumSamplesPerCell < c.MinimumSamples {
		return fmt.Errorf("scheduler residual sample bounds are invalid")
	}
	if c.MaxAge <= 0 {
		return fmt.Errorf("scheduler residual maximum age must be positive")
	}
	if !positiveFinite(c.LowerQuantile) || c.LowerQuantile >= 1 || !positiveFinite(c.UpperQuantile) || c.UpperQuantile >= 1 || c.LowerQuantile > c.UpperQuantile {
		return fmt.Errorf("scheduler residual quantiles are invalid")
	}
	if !positiveFinite(c.MinimumTPSMultiplier) || c.MinimumTPSMultiplier > 1 || !positiveFinite(c.MaximumTPSMultiplier) || c.MaximumTPSMultiplier < 1 || c.MaximumTPSMultiplier < c.MinimumTPSMultiplier {
		return fmt.Errorf("scheduler TPS multiplier bounds are invalid")
	}
	if !positiveFinite(c.MinimumLatencyMultiplier) || c.MinimumLatencyMultiplier > 1 || !positiveFinite(c.MaximumLatencyMultiplier) || c.MaximumLatencyMultiplier < 1 || c.MaximumLatencyMultiplier < c.MinimumLatencyMultiplier {
		return fmt.Errorf("scheduler latency multiplier bounds are invalid")
	}
	if !positiveFinite(c.CalibratedConfidence) || c.CalibratedConfidence > 1 {
		return fmt.Errorf("scheduler calibrated confidence must be in (0, 1]")
	}
	if c.DecodeSequenceBucket <= 0 || c.ContextTokenBucket <= 0 || c.PrefillTokenBucket <= 0 || c.KVTokenBucket <= 0 {
		return fmt.Errorf("scheduler feature buckets must be positive")
	}
	return nil
}

type PredictionSource string

const (
	PredictionSourceStatic     PredictionSource = "static"
	PredictionSourceCalibrated PredictionSource = "calibrated"
)

type SchedulerFeatures struct {
	DecodeSequences       int
	ActiveContextTokens   int64
	UncachedPrefillTokens int64
	CachedPrefillExpected int64
	PhysicalKVUpper       int64
	ActiveKVUpper         int64
	DecodeHorizonUpper    int64
}

type SchedulerPrediction struct {
	Identity    ModelIdentity
	PredictedAt time.Time
	Features    SchedulerFeatures
	Prior       domain.SchedulerEstimate
	Estimate    domain.SchedulerEstimate
	Source      PredictionSource
	Samples     int
	Confidence  float64
}

type SchedulerOutcome struct {
	Identity             ModelIdentity
	ObservedAt           time.Time
	Attributed           bool
	ExistingUserTPS      float64
	ExistingUserTPSValid bool
	AllUserTPS           float64
	AllUserTPSValid      bool
	TTFT                 time.Duration
	TTFTValid            bool
	TPOT                 time.Duration
	TPOTValid            bool
}

type LearnedSchedulerSnapshot struct {
	SamplesAccepted uint64
	SamplesRejected uint64
	Cells           int
}

type featureCell struct {
	DecodeSequences       int
	ActiveContextTokens   int64
	UncachedPrefillTokens int64
	CachedPrefillExpected int64
	PhysicalKVUpper       int64
	ActiveKVUpper         int64
	DecodeHorizonUpper    int64
}

type residualSample struct {
	ObservedAt       time.Time
	ExistingTPSRatio float64
	AllTPSRatio      float64
	TTFTRatio        float64
	TPOTRatio        float64
	ExistingTPSValid bool
	AllTPSValid      bool
	TTFTValid        bool
	TPOTValid        bool
}

type LearnedScheduler struct {
	mu              sync.Mutex
	profile         StaticSchedulerProfile
	config          ResidualCalibratorConfig
	cells           map[featureCell][]residualSample
	samplesAccepted uint64
	samplesRejected uint64
}

func NewLearnedScheduler(profile StaticSchedulerProfile, config ResidualCalibratorConfig) (*LearnedScheduler, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if profile.Identity != config.Identity {
		return nil, fmt.Errorf("scheduler profile and residual calibrator identities differ")
	}
	return &LearnedScheduler{
		profile: profile,
		config:  config,
		cells:   make(map[featureCell][]residualSample),
	}, nil
}

func (s *LearnedScheduler) Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction {
	if s == nil {
		return SchedulerPrediction{}
	}
	features := schedulerFeatures(state, request)
	prior := s.staticEstimate(features)
	prediction := SchedulerPrediction{
		Identity:    s.profile.Identity,
		PredictedAt: now,
		Features:    features,
		Prior:       prior,
		Estimate:    prior,
		Source:      PredictionSourceStatic,
		Confidence:  s.profile.Confidence,
	}

	key := s.featureCell(features)
	s.mu.Lock()
	samples := append([]residualSample(nil), s.cells[key]...)
	s.mu.Unlock()
	fresh := freshResiduals(samples, now, s.config.MaxAge)
	if len(fresh) < s.config.MinimumSamples {
		return prediction
	}

	existingRatios, allRatios, ttftRatios, tpotRatios := residualRatios(fresh)
	if len(existingRatios) < s.config.MinimumSamples || len(allRatios) < s.config.MinimumSamples || len(ttftRatios) < s.config.MinimumSamples || len(tpotRatios) < s.config.MinimumSamples {
		return prediction
	}
	existingMultiplier := clampFloat(quantile(existingRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
	allMultiplier := clampFloat(quantile(allRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
	ttftMultiplier := clampFloat(quantile(ttftRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
	tpotMultiplier := clampFloat(quantile(tpotRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
	prediction.Estimate.ExistingUserTPSLower = prior.ExistingUserTPSLower * existingMultiplier
	prediction.Estimate.AllUserTPSLower = prior.AllUserTPSLower * allMultiplier
	prediction.Estimate.TTFTUpper = scaleDuration(prior.TTFTUpper, ttftMultiplier)
	prediction.Estimate.TPOTUpper = scaleDuration(prior.TPOTUpper, tpotMultiplier)
	prediction.Source = PredictionSourceCalibrated
	prediction.Samples = minimumInt(len(existingRatios), len(allRatios), len(ttftRatios), len(tpotRatios))
	prediction.Confidence = s.config.CalibratedConfidence
	return prediction
}

func (s *LearnedScheduler) Observe(prediction SchedulerPrediction, outcome SchedulerOutcome) error {
	if s == nil {
		return fmt.Errorf("learned scheduler is nil")
	}
	if prediction.Identity != s.profile.Identity || outcome.Identity != s.profile.Identity || prediction.Identity != outcome.Identity {
		return s.rejectOutcome(fmt.Errorf("scheduler outcome identity mismatch"))
	}
	if !outcome.Attributed {
		return s.rejectOutcome(fmt.Errorf("scheduler outcome is not sufficiently attributed"))
	}
	if outcome.ObservedAt.IsZero() || outcome.ObservedAt.Before(prediction.PredictedAt) {
		return s.rejectOutcome(fmt.Errorf("scheduler outcome timestamp is invalid"))
	}
	sample, err := residualFromOutcome(prediction, outcome)
	if err != nil {
		return s.rejectOutcome(err)
	}
	key := s.featureCell(prediction.Features)
	s.mu.Lock()
	values := append(s.cells[key], sample)
	if excess := len(values) - s.config.MaximumSamplesPerCell; excess > 0 {
		values = append([]residualSample(nil), values[excess:]...)
	}
	s.cells[key] = values
	s.samplesAccepted++
	s.mu.Unlock()
	return nil
}

func (s *LearnedScheduler) Snapshot() LearnedSchedulerSnapshot {
	if s == nil {
		return LearnedSchedulerSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return LearnedSchedulerSnapshot{
		SamplesAccepted: s.samplesAccepted,
		SamplesRejected: s.samplesRejected,
		Cells:           len(s.cells),
	}
}

func (s *LearnedScheduler) rejectOutcome(err error) error {
	s.mu.Lock()
	s.samplesRejected++
	s.mu.Unlock()
	return err
}

func (s *LearnedScheduler) staticEstimate(features SchedulerFeatures) domain.SchedulerEstimate {
	decodeSequences := features.DecodeSequences
	if decodeSequences < 1 {
		decodeSequences = 1
	}
	postJoinSequences := features.DecodeSequences + 1
	if postJoinSequences < 1 {
		postJoinSequences = 1
	}
	prefillPenalty := s.profile.PrefillTPSPenaltyPerKToken * float64(features.UncachedPrefillTokens) / 1_000
	prefillCapacity := s.profile.BaseCompletionTPS - prefillPenalty
	if prefillCapacity < 0 {
		prefillCapacity = 0
	}
	return domain.SchedulerEstimate{
		ExistingUserTPSLower: prefillCapacity / float64(decodeSequences),
		AllUserTPSLower:      s.profile.BaseCompletionTPS / float64(postJoinSequences),
		TTFTUpper:            addDurationSaturating(s.profile.BaseTTFT, multiplyDurationSaturating(s.profile.TTFTPerUncachedPrefillToken, features.UncachedPrefillTokens)),
		TPOTUpper:            addDurationSaturating(s.profile.BaseTPOT, multiplyDurationSaturating(s.profile.TPOTPerExistingDecodeSequence, int64(features.DecodeSequences))),
		WorkspaceRiskUpper:   s.profile.WorkspaceRiskUpper,
		PreemptionRiskUpper:  s.profile.PreemptionRiskUpper,
	}
}

func (s *LearnedScheduler) featureCell(features SchedulerFeatures) featureCell {
	return featureCell{
		DecodeSequences:       bucketInt(features.DecodeSequences, s.config.DecodeSequenceBucket),
		ActiveContextTokens:   bucketInt64(features.ActiveContextTokens, s.config.ContextTokenBucket),
		UncachedPrefillTokens: bucketInt64(features.UncachedPrefillTokens, s.config.PrefillTokenBucket),
		CachedPrefillExpected: bucketInt64(features.CachedPrefillExpected, s.config.PrefillTokenBucket),
		PhysicalKVUpper:       bucketInt64(features.PhysicalKVUpper, s.config.KVTokenBucket),
		ActiveKVUpper:         bucketInt64(features.ActiveKVUpper, s.config.KVTokenBucket),
		DecodeHorizonUpper:    bucketInt64(features.DecodeHorizonUpper, s.config.PrefillTokenBucket),
	}
}

func schedulerFeatures(state domain.VirtualState, request domain.RequestCost) SchedulerFeatures {
	return SchedulerFeatures{
		DecodeSequences:       nonNegativeInt(state.DecodeSequences),
		ActiveContextTokens:   nonNegativeInt64(state.ActiveContextTokens),
		UncachedPrefillTokens: addInt64Saturating(nonNegativeInt64(state.UncachedPrefillTokens), nonNegativeInt64(request.UncachedPrefillUpper)),
		CachedPrefillExpected: nonNegativeInt64(request.CachedPrefillExpected),
		PhysicalKVUpper:       addInt64Saturating(nonNegativeInt64(state.PhysicalKVUpper), nonNegativeInt64(request.KV.PhysicalKVUpper)),
		ActiveKVUpper:         addInt64Saturating(nonNegativeInt64(state.ActiveKVUpper), nonNegativeInt64(request.KV.ActiveKVUpper)),
		DecodeHorizonUpper:    nonNegativeInt64(request.DecodeHorizonUpper),
	}
}

func residualFromOutcome(prediction SchedulerPrediction, outcome SchedulerOutcome) (residualSample, error) {
	sample := residualSample{ObservedAt: outcome.ObservedAt}
	valid := 0
	if outcome.ExistingUserTPSValid {
		ratio, err := positiveRatio(outcome.ExistingUserTPS, prediction.Prior.ExistingUserTPSLower, "existing-user TPS")
		if err != nil {
			return residualSample{}, err
		}
		sample.ExistingTPSRatio = ratio
		sample.ExistingTPSValid = true
		valid++
	}
	if outcome.AllUserTPSValid {
		ratio, err := positiveRatio(outcome.AllUserTPS, prediction.Prior.AllUserTPSLower, "all-user TPS")
		if err != nil {
			return residualSample{}, err
		}
		sample.AllTPSRatio = ratio
		sample.AllTPSValid = true
		valid++
	}
	if outcome.TTFTValid {
		ratio, err := positiveRatio(float64(outcome.TTFT), float64(prediction.Prior.TTFTUpper), "TTFT")
		if err != nil {
			return residualSample{}, err
		}
		sample.TTFTRatio = ratio
		sample.TTFTValid = true
		valid++
	}
	if outcome.TPOTValid {
		ratio, err := positiveRatio(float64(outcome.TPOT), float64(prediction.Prior.TPOTUpper), "TPOT")
		if err != nil {
			return residualSample{}, err
		}
		sample.TPOTRatio = ratio
		sample.TPOTValid = true
		valid++
	}
	if valid == 0 {
		return residualSample{}, fmt.Errorf("scheduler outcome has no valid target")
	}
	return sample, nil
}

func freshResiduals(samples []residualSample, now time.Time, maxAge time.Duration) []residualSample {
	result := make([]residualSample, 0, len(samples))
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > maxAge {
			continue
		}
		result = append(result, sample)
	}
	return result
}

func residualRatios(samples []residualSample) (existing, all, ttft, tpot []float64) {
	for _, sample := range samples {
		if sample.ExistingTPSValid {
			existing = append(existing, sample.ExistingTPSRatio)
		}
		if sample.AllTPSValid {
			all = append(all, sample.AllTPSRatio)
		}
		if sample.TTFTValid {
			ttft = append(ttft, sample.TTFTRatio)
		}
		if sample.TPOTValid {
			tpot = append(tpot, sample.TPOTRatio)
		}
	}
	return existing, all, ttft, tpot
}

func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 1
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Ceil(q*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}

func positiveRatio(observed, predicted float64, name string) (float64, error) {
	if !positiveFinite(observed) || !positiveFinite(predicted) {
		return 0, fmt.Errorf("scheduler %s observation or prior is invalid", name)
	}
	ratio := observed / predicted
	if !positiveFinite(ratio) {
		return 0, fmt.Errorf("scheduler %s residual is invalid", name)
	}
	return ratio, nil
}

func scaleDuration(value time.Duration, multiplier float64) time.Duration {
	if value <= 0 || !positiveFinite(multiplier) {
		return value
	}
	scaled := float64(value) * multiplier
	if scaled >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if scaled < 1 {
		return time.Nanosecond
	}
	return time.Duration(math.Ceil(scaled))
}

func multiplyDurationSaturating(value time.Duration, count int64) time.Duration {
	if value <= 0 || count <= 0 {
		return 0
	}
	if count > math.MaxInt64/int64(value) {
		return time.Duration(math.MaxInt64)
	}
	return value * time.Duration(count)
}

func addDurationSaturating(left, right time.Duration) time.Duration {
	if right > 0 && left > time.Duration(math.MaxInt64)-right {
		return time.Duration(math.MaxInt64)
	}
	return left + right
}

func addInt64Saturating(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func bucketInt(value, size int) int {
	value = nonNegativeInt(value)
	return value / size
}

func bucketInt64(value, size int64) int64 {
	value = nonNegativeInt64(value)
	return value / size
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func minimumInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
