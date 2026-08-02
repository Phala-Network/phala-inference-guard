package predictive

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const maximumGlobalResidualSamples = 1_024

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
	MaximumCells             int
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
	if c.MaximumCells <= 0 {
		return fmt.Errorf("scheduler residual global cell bound must be positive")
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
	PredictionSourceStatic      PredictionSource = "static"
	PredictionSourceCalibrated  PredictionSource = "calibrated"
	PredictionSourceUnavailable PredictionSource = "unavailable"
)

type SchedulerFeatures struct {
	ExistingDecodeSequences         int
	DecodeSequences                 int
	ExistingPendingPrefillSequences int
	PendingPrefillSequences         int
	ExistingActiveContextTokens     int64
	ExistingUncachedPrefill         int64
	ExistingPhysicalKVUpper         int64
	ExistingActiveKVUpper           int64
	RequestComplexityTokensUpper    int64
	ActiveContextTokens             int64
	UncachedPrefillTokens           int64
	AccruedLocalAdmissionLatency    time.Duration
	PhysicalKVUpper                 int64
	ActiveKVUpper                   int64
	DecodeHorizonUpper              int64
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
	Censored             bool
	ExistingUserTPS      float64
	ExistingUserTPSValid bool
	UserTPS              float64
	UserTPSValid         bool
	TTFT                 time.Duration
	TTFTValid            bool
	TPOT                 time.Duration
	TPOTValid            bool
}

type ExistingPrefillOutcome struct {
	Identity                ModelIdentity
	StartedAt               time.Time
	ObservedAt              time.Time
	Features                SchedulerFeatures
	ExistingDecodeSequences int
	PendingPrefillSequences int
	PendingPrefillTokens    int64
	ExistingUserTPS         float64
}

type LearnedSchedulerSnapshot struct {
	SamplesAccepted        uint64
	SamplesRejected        uint64
	ExistingUserTPSSamples uint64
	NewUserTPSSamples      uint64
	Invalidations          uint64
	Cells                  int
	GlobalSamples          int
}

type featureCell struct {
	ExistingDecodeSequences         int
	DecodeSequences                 int
	ExistingPendingPrefillSequences int
	PendingPrefillSequences         int
	ExistingActiveContextTokens     int64
	ExistingUncachedPrefill         int64
	ExistingPhysicalKVUpper         int64
	ExistingActiveKVUpper           int64
	RequestComplexityTokensUpper    int64
	DecodeHorizonUpper              int64
}

type residualSample struct {
	ObservedAt           time.Time
	Features             SchedulerFeatures
	Censored             bool
	ExistingUserTPSRatio float64
	UserTPSRatio         float64
	TTFTRatio            float64
	TPOTRatio            float64
	ExistingUserTPSValid bool
	UserTPSValid         bool
	TTFTValid            bool
	TPOTValid            bool
}

type residualCell struct {
	CreatedSequence uint64
	Samples         []residualSample
}

type LearnedScheduler struct {
	mu                     sync.Mutex
	profile                StaticSchedulerProfile
	config                 ResidualCalibratorConfig
	cells                  map[featureCell]*residualCell
	globalSamples          []residualSample
	globalCounts           map[featureCell]int
	globalLimit            int
	cellSequence           uint64
	samplesAccepted        uint64
	samplesRejected        uint64
	existingUserTPSSamples uint64
	newUserTPSSamples      uint64
	invalidations          uint64
}

func (s *LearnedScheduler) InvalidateLearning() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.cells)
	s.globalSamples = nil
	clear(s.globalCounts)
	s.invalidations++
	s.mu.Unlock()
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
		profile:      profile,
		config:       config,
		cells:        make(map[featureCell]*residualCell),
		globalCounts: make(map[featureCell]int),
		globalLimit:  globalResidualSampleLimit(config),
	}, nil
}

func (s *LearnedScheduler) Identity() ModelIdentity {
	if s == nil {
		return ModelIdentity{}
	}
	return s.profile.Identity
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
	cell := s.cells[key]
	local := residualRatios{}
	if cell != nil {
		local = freshLocalResidualRatios(cell.Samples, now, s.config.MaxAge, features)
	}
	global := residualRatios{}
	if requiresGlobalResidualFallback(local, s.config.MinimumSamples, readyExistingDecodeSequences(features) > 0) {
		global = freshCompatibleResidualRatios(s.globalSamples, now, s.config.MaxAge, features, s.config.MinimumSamples)
	}
	s.mu.Unlock()

	userRatios := preferMatureRatios(local.UserTPS, global.UserTPS, s.config.MinimumSamples)
	existingUserRatios := preferExistingTPSRatios(local.ExistingUserTPS, global.ExistingUserTPS, s.config.MinimumSamples)
	ttftRatios := preferMatureRatios(local.TTFT, global.TTFT, s.config.MinimumSamples)
	tpotRatios := preferMatureRatios(local.TPOT, global.TPOT, s.config.MinimumSamples)
	calibratedSamples := 0
	if len(userRatios) >= s.config.MinimumSamples {
		userMultiplier := clampFloat(quantileInPlace(userRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
		prediction.Estimate.NewUserTPSLower = prior.NewUserTPSLower * userMultiplier
		calibratedSamples = minimumPositiveInt(calibratedSamples, len(userRatios))
	}
	if len(existingUserRatios) > 0 && !prior.ExistingUserTPSNotApplicable {
		existingUserMultiplier := clampFloat(quantileInPlace(existingUserRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
		prediction.Estimate.ExistingUserTPSLower = prior.ExistingUserTPSLower * existingUserMultiplier
		calibratedSamples = minimumPositiveInt(calibratedSamples, len(existingUserRatios))
	}
	if len(ttftRatios) >= s.config.MinimumSamples {
		ttftMultiplier := clampFloat(quantileInPlace(ttftRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
		prediction.Estimate.TTFTUpper = scaleDuration(prior.TTFTUpper, ttftMultiplier)
		calibratedSamples = minimumPositiveInt(calibratedSamples, len(ttftRatios))
	}
	if len(tpotRatios) >= s.config.MinimumSamples {
		tpotMultiplier := clampFloat(quantileInPlace(tpotRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
		prediction.Estimate.TPOTUpper = scaleDuration(prior.TPOTUpper, tpotMultiplier)
		calibratedSamples = minimumPositiveInt(calibratedSamples, len(tpotRatios))
	}
	if calibratedSamples == 0 {
		return prediction
	}
	if features.ExistingDecodeSequences == 0 {
		if prediction.Estimate.NewUserTPSLower < prior.NewUserTPSLower {
			prediction.Estimate.NewUserTPSLower = prior.NewUserTPSLower
		}
		if prediction.Estimate.TPOTUpper > prior.TPOTUpper {
			prediction.Estimate.TPOTUpper = prior.TPOTUpper
		}
	}
	if prediction.Estimate == prior {
		return prediction
	}
	prediction.Source = PredictionSourceCalibrated
	prediction.Samples = calibratedSamples
	prediction.Confidence = minimumConfidence(s.profile.Confidence, s.config.CalibratedConfidence)
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
	if outcome.Censored {
		return s.rejectOutcome(fmt.Errorf("scheduler outcome is censored"))
	}
	sample, err := residualFromOutcome(prediction, outcome)
	if err != nil {
		return s.rejectOutcome(err)
	}
	key := s.featureCell(prediction.Features)
	s.mu.Lock()
	cell := s.cells[key]
	if cell == nil {
		if len(s.cells) >= s.config.MaximumCells {
			s.evictOldestCellLocked()
		}
		s.cellSequence++
		cell = &residualCell{CreatedSequence: s.cellSequence}
		s.cells[key] = cell
	}
	cell.Samples = append(cell.Samples, sample)
	if excess := len(cell.Samples) - s.config.MaximumSamplesPerCell; excess > 0 {
		copy(cell.Samples, cell.Samples[excess:])
		cell.Samples = cell.Samples[:s.config.MaximumSamplesPerCell]
	}
	s.appendGlobalSampleLocked(sample)
	s.samplesAccepted++
	if sample.ExistingUserTPSValid {
		s.existingUserTPSSamples++
	}
	if sample.UserTPSValid {
		s.newUserTPSSamples++
	}
	s.mu.Unlock()
	return nil
}

func (s *LearnedScheduler) ObserveExistingPrefill(outcome ExistingPrefillOutcome) error {
	if s == nil {
		return fmt.Errorf("learned scheduler is nil")
	}
	if outcome.Identity != s.profile.Identity {
		return s.rejectOutcome(fmt.Errorf("existing-user prefill outcome identity mismatch"))
	}
	if outcome.StartedAt.IsZero() || outcome.ObservedAt.IsZero() || !outcome.ObservedAt.After(outcome.StartedAt) {
		return s.rejectOutcome(fmt.Errorf("existing-user prefill outcome window is invalid"))
	}
	features := outcome.Features
	if outcome.ExistingDecodeSequences <= 0 || outcome.PendingPrefillSequences != 1 || outcome.PendingPrefillTokens <= 0 ||
		readyExistingDecodeSequences(features) != outcome.ExistingDecodeSequences ||
		features.ExistingPendingPrefillSequences != 0 ||
		features.PendingPrefillSequences != outcome.PendingPrefillSequences ||
		features.DecodeSequences != features.ExistingDecodeSequences+outcome.PendingPrefillSequences ||
		features.ExistingActiveContextTokens < 0 || features.ExistingUncachedPrefill < 0 ||
		features.ExistingPhysicalKVUpper < 0 || features.ExistingActiveKVUpper < 0 ||
		features.ActiveContextTokens < features.ExistingActiveContextTokens ||
		features.UncachedPrefillTokens < features.ExistingUncachedPrefill ||
		features.UncachedPrefillTokens-features.ExistingUncachedPrefill != outcome.PendingPrefillTokens ||
		features.PhysicalKVUpper < features.ExistingPhysicalKVUpper || features.ActiveKVUpper < features.ExistingActiveKVUpper ||
		features.RequestComplexityTokensUpper < outcome.PendingPrefillTokens ||
		!nonNegativeFinite(outcome.ExistingUserTPS) {
		return s.rejectOutcome(fmt.Errorf("existing-user prefill outcome state is invalid"))
	}
	prior := s.staticEstimate(features)
	prediction := SchedulerPrediction{
		Identity:    s.profile.Identity,
		PredictedAt: outcome.StartedAt,
		Features:    features,
		Prior:       prior,
		Estimate:    prior,
		Source:      PredictionSourceStatic,
		Confidence:  s.profile.Confidence,
	}
	return s.Observe(prediction, SchedulerOutcome{
		Identity:             outcome.Identity,
		ObservedAt:           outcome.ObservedAt,
		Attributed:           true,
		ExistingUserTPS:      outcome.ExistingUserTPS,
		ExistingUserTPSValid: true,
	})
}

func requiresGlobalResidualFallback(local residualRatios, minimum int, existingTPSApplicable bool) bool {
	// TTFT is observation-only. Preserve local TTFT calibration and collect a
	// compatible global TTFT fallback opportunistically when a protected TPS or
	// TPOT dimension needs the scan, but never scan the global store only for
	// TTFT diagnostics.
	return len(local.UserTPS) < minimum || len(local.TPOT) < minimum ||
		(existingTPSApplicable && len(local.ExistingUserTPS) < minimum)
}

func globalResidualSampleLimit(config ResidualCalibratorConfig) int {
	limit := config.MaximumSamplesPerCell
	if config.MaximumCells <= maximumGlobalResidualSamples/config.MinimumSamples {
		matureCoverage := config.MaximumCells * config.MinimumSamples
		if matureCoverage > limit {
			limit = matureCoverage
		}
	} else {
		limit = maximumGlobalResidualSamples
	}
	if limit > maximumGlobalResidualSamples {
		return maximumGlobalResidualSamples
	}
	return limit
}

// appendGlobalSampleLocked keeps global fallback evidence independently
// bounded from the per-cell history. A dominant request shape is trimmed before
// a minority shape that has only the minimum samples needed for a mature
// prediction. This prevents ordinary traffic skew from erasing all transferable
// evidence for less frequent request sizes while keeping prediction scans under
// a hard operational bound.
func (s *LearnedScheduler) appendGlobalSampleLocked(sample residualSample) {
	s.globalSamples = append(s.globalSamples, sample)
	key := s.featureCell(sample.Features)
	s.globalCounts[key]++
	if len(s.globalSamples) <= s.globalLimit {
		return
	}

	evictionCount := 0
	for _, count := range s.globalCounts {
		if count > s.config.MinimumSamples && count > evictionCount {
			evictionCount = count
		}
	}
	if evictionCount == 0 {
		for _, count := range s.globalCounts {
			if count > evictionCount {
				evictionCount = count
			}
		}
	}

	evictionIndex := 0
	for index, candidate := range s.globalSamples {
		if s.globalCounts[s.featureCell(candidate.Features)] == evictionCount {
			evictionIndex = index
			break
		}
	}
	evictionKey := s.featureCell(s.globalSamples[evictionIndex].Features)
	s.globalCounts[evictionKey]--
	if s.globalCounts[evictionKey] == 0 {
		delete(s.globalCounts, evictionKey)
	}
	copy(s.globalSamples[evictionIndex:], s.globalSamples[evictionIndex+1:])
	s.globalSamples[len(s.globalSamples)-1] = residualSample{}
	s.globalSamples = s.globalSamples[:len(s.globalSamples)-1]
}

func (s *LearnedScheduler) evictOldestCellLocked() {
	var oldestKey featureCell
	var oldestSequence uint64
	found := false
	for key, cell := range s.cells {
		if cell == nil {
			delete(s.cells, key)
			continue
		}
		if !found || cell.CreatedSequence < oldestSequence {
			oldestKey = key
			oldestSequence = cell.CreatedSequence
			found = true
		}
	}
	if found {
		delete(s.cells, oldestKey)
	}
}

func (s *LearnedScheduler) Snapshot() LearnedSchedulerSnapshot {
	if s == nil {
		return LearnedSchedulerSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return LearnedSchedulerSnapshot{
		SamplesAccepted:        s.samplesAccepted,
		SamplesRejected:        s.samplesRejected,
		ExistingUserTPSSamples: s.existingUserTPSSamples,
		NewUserTPSSamples:      s.newUserTPSSamples,
		Invalidations:          s.invalidations,
		Cells:                  len(s.cells),
		GlobalSamples:          len(s.globalSamples),
	}
}

type residualRatios struct {
	ExistingUserTPS []float64
	UserTPS         []float64
	TTFT            []float64
	TPOT            []float64
}

func freshLocalResidualRatios(samples []residualSample, now time.Time, maxAge time.Duration, query SchedulerFeatures) residualRatios {
	ratios := residualRatios{}
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > maxAge || sample.Censored {
			continue
		}
		if sample.ExistingUserTPSValid && existingTPSResidualCompatible(sample.Features, query, sample.ExistingUserTPSRatio) {
			ratios.ExistingUserTPS = appendResidualRatio(ratios.ExistingUserTPS, sample.ExistingUserTPSRatio, len(samples))
		}
		if sample.UserTPSValid && tpsResidualCompatible(sample.Features, query, sample.UserTPSRatio) {
			ratios.UserTPS = appendResidualRatio(ratios.UserTPS, sample.UserTPSRatio, len(samples))
		}
		if sample.TTFTValid && ttftResidualCompatible(sample.Features, query, sample.TTFTRatio) {
			ratios.TTFT = appendResidualRatio(ratios.TTFT, sample.TTFTRatio, len(samples))
		}
		if sample.TPOTValid && latencyResidualCompatible(sample.Features, query, sample.TPOTRatio) {
			ratios.TPOT = appendResidualRatio(ratios.TPOT, sample.TPOTRatio, len(samples))
		}
	}
	return ratios
}

func freshCompatibleResidualRatios(samples []residualSample, now time.Time, maxAge time.Duration, query SchedulerFeatures, minimum int) residualRatios {
	if minimum <= 0 {
		return residualRatios{}
	}
	groups := make(map[int]*residualRatios)
	existingAdverse := make([]float64, 0, minimum)
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > maxAge || sample.Censored {
			continue
		}
		sequence := sample.Features.DecodeSequences
		group := groups[sequence]
		if group == nil {
			group = &residualRatios{}
			groups[sequence] = group
		}
		if sample.ExistingUserTPSValid && existingTPSResidualCompatible(sample.Features, query, sample.ExistingUserTPSRatio) {
			group.ExistingUserTPS = append(group.ExistingUserTPS, sample.ExistingUserTPSRatio)
			if sample.ExistingUserTPSRatio < 1 {
				existingAdverse = append(existingAdverse, sample.ExistingUserTPSRatio)
			}
		}
		if sample.UserTPSValid && tpsResidualCompatible(sample.Features, query, sample.UserTPSRatio) {
			group.UserTPS = append(group.UserTPS, sample.UserTPSRatio)
		}
		if sample.TTFTValid && ttftResidualCompatible(sample.Features, query, sample.TTFTRatio) {
			group.TTFT = append(group.TTFT, sample.TTFTRatio)
		}
		if sample.TPOTValid && latencyResidualCompatible(sample.Features, query, sample.TPOTRatio) {
			group.TPOT = append(group.TPOT, sample.TPOTRatio)
		}
	}

	result := residualRatios{}
	existingSequence := 0
	userSequence := 0
	ttftSequence := 0
	tpotSequence := 0
	for sequence, group := range groups {
		if len(existingAdverse) == 0 && len(group.ExistingUserTPS) >= minimum && sequence > existingSequence {
			existingSequence = sequence
			result.ExistingUserTPS = group.ExistingUserTPS
		}
		if len(group.UserTPS) >= minimum && sequence > userSequence {
			userSequence = sequence
			result.UserTPS = group.UserTPS
		}
		if len(group.TTFT) >= minimum && sequence > ttftSequence {
			ttftSequence = sequence
			result.TTFT = group.TTFT
		}
		if len(group.TPOT) >= minimum && sequence > tpotSequence {
			tpotSequence = sequence
			result.TPOT = group.TPOT
		}
	}
	if len(existingAdverse) > 0 {
		result.ExistingUserTPS = existingAdverse
	}
	return result
}

func decodePressureAtLeast(left, right SchedulerFeatures) bool {
	if left.DecodeSequences <= 0 || right.DecodeSequences <= 0 {
		return false
	}
	return normalizedPressure(left.ActiveContextTokens, left.DecodeSequences) >= normalizedPressure(right.ActiveContextTokens, right.DecodeSequences) &&
		normalizedPressure(left.PhysicalKVUpper, left.DecodeSequences) >= normalizedPressure(right.PhysicalKVUpper, right.DecodeSequences) &&
		normalizedPressure(left.ActiveKVUpper, left.DecodeSequences) >= normalizedPressure(right.ActiveKVUpper, right.DecodeSequences)
}

func prefillPressureAtLeast(left, right SchedulerFeatures) bool {
	return left.PendingPrefillSequences >= right.PendingPrefillSequences &&
		normalizedPressure(left.UncachedPrefillTokens, left.DecodeSequences) >= normalizedPressure(right.UncachedPrefillTokens, right.DecodeSequences) &&
		decodePressureAtLeast(left, right)
}

func concurrencyAndPrefillPressureAtLeast(left, right SchedulerFeatures) bool {
	return left.DecodeSequences >= right.DecodeSequences && prefillPressureAtLeast(left, right)
}

func existingTPSResidualCompatible(sample, query SchedulerFeatures, ratio float64) bool {
	if ratio < 1 {
		return concurrencyAndPrefillPressureAtLeast(query, sample)
	}
	return concurrencyAndPrefillPressureAtLeast(sample, query)
}

func tpsResidualCompatible(sample, query SchedulerFeatures, ratio float64) bool {
	if ratio < 1 {
		// A post-prefill completion below its prior is conservative evidence of
		// insufficient aggregate decode capacity. Keep it across request-size
		// calibration and lower decode pressure; otherwise a slightly smaller
		// approximate token estimate can erase known TPS risk. Optimistic evidence
		// remains dominance-gated below and still requires mature samples.
		return true
	}
	return decodePressureAtLeast(sample, query)
}

func latencyResidualCompatible(sample, query SchedulerFeatures, ratio float64) bool {
	if ratio > 1 {
		return decodePressureAtLeast(query, sample)
	}
	return decodePressureAtLeast(sample, query)
}

func ttftResidualCompatible(sample, query SchedulerFeatures, ratio float64) bool {
	if sample.DecodeSequences <= 0 || query.DecodeSequences <= 0 {
		return false
	}
	samplePressure := normalizedPressure(sample.RequestComplexityTokensUpper, sample.DecodeSequences)
	queryPressure := normalizedPressure(query.RequestComplexityTokensUpper, query.DecodeSequences)
	if ratio > 1 {
		return queryPressure >= samplePressure
	}
	return samplePressure >= queryPressure
}

func normalizedPressure(value int64, sequences int) int64 {
	if value <= 0 {
		return 0
	}
	if sequences <= 1 {
		return value
	}
	divisor := int64(sequences)
	return value/divisor + boolInt64(value%divisor != 0)
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func preferMatureRatios(local, global []float64, minimum int) []float64 {
	if len(local) >= minimum {
		return local
	}
	if len(global) >= minimum {
		return global
	}
	return nil
}

func preferExistingTPSRatios(local, global []float64, minimum int) []float64 {
	if adverse, ok := minimumAdverseRatio(local, global); ok {
		return []float64{adverse}
	}
	return preferMatureRatios(local, global, minimum)
}

func minimumAdverseRatio(groups ...[]float64) (float64, bool) {
	minimum := 1.0
	found := false
	for _, ratios := range groups {
		for _, ratio := range ratios {
			if ratio >= 0 && ratio < minimum {
				minimum = ratio
				found = true
			}
		}
	}
	return minimum, found
}

func (s *LearnedScheduler) rejectOutcome(err error) error {
	s.mu.Lock()
	s.samplesRejected++
	s.mu.Unlock()
	return err
}

func (s *LearnedScheduler) staticEstimate(features SchedulerFeatures) domain.SchedulerEstimate {
	prefillPenalty := s.profile.PrefillTPSPenaltyPerKToken * float64(features.UncachedPrefillTokens) / 1_000
	existingCapacity := s.profile.BaseCompletionTPS - prefillPenalty
	if existingCapacity < 0 {
		existingCapacity = 0
	}
	readyExisting := readyExistingDecodeSequences(features)
	existingTPSNotApplicable := readyExisting == 0
	postJoinSequences := features.DecodeSequences
	if postJoinSequences < 1 {
		postJoinSequences = 1
	}
	postJoinTPS := s.profile.BaseCompletionTPS / float64(postJoinSequences)
	existingTPS := 0.0
	if existingTPSNotApplicable {
		existingTPS = 0
	} else {
		existingTPS = existingCapacity / float64(readyExisting)
	}
	return domain.SchedulerEstimate{
		ExistingUserTPSLower:         existingTPS,
		ExistingUserTPSNotApplicable: existingTPSNotApplicable,
		NewUserTPSLower:              postJoinTPS,
		TTFTUpper:                    addDurationSaturating(addDurationSaturating(s.profile.BaseTTFT, multiplyDurationSaturating(s.profile.TTFTPerUncachedPrefillToken, features.UncachedPrefillTokens)), features.AccruedLocalAdmissionLatency),
		TPOTUpper:                    addDurationSaturating(s.profile.BaseTPOT, multiplyDurationSaturating(s.profile.TPOTPerExistingDecodeSequence, int64(features.ExistingDecodeSequences))),
		WorkspaceRiskUpper:           s.profile.WorkspaceRiskUpper,
		PreemptionRiskUpper:          s.profile.PreemptionRiskUpper,
	}
}

func (s *LearnedScheduler) featureCell(features SchedulerFeatures) featureCell {
	return featureCell{
		ExistingDecodeSequences:         bucketInt(features.ExistingDecodeSequences, s.config.DecodeSequenceBucket),
		DecodeSequences:                 bucketInt(features.DecodeSequences, s.config.DecodeSequenceBucket),
		ExistingPendingPrefillSequences: bucketInt(features.ExistingPendingPrefillSequences, s.config.DecodeSequenceBucket),
		PendingPrefillSequences:         bucketInt(features.PendingPrefillSequences, s.config.DecodeSequenceBucket),
		ExistingActiveContextTokens:     bucketInt64(features.ExistingActiveContextTokens, s.config.ContextTokenBucket),
		ExistingUncachedPrefill:         bucketInt64(features.ExistingUncachedPrefill, s.config.PrefillTokenBucket),
		ExistingPhysicalKVUpper:         bucketInt64(features.ExistingPhysicalKVUpper, s.config.KVTokenBucket),
		ExistingActiveKVUpper:           bucketInt64(features.ExistingActiveKVUpper, s.config.KVTokenBucket),
		RequestComplexityTokensUpper:    bucketInt64(features.RequestComplexityTokensUpper, s.config.PrefillTokenBucket),
		DecodeHorizonUpper:              bucketInt64(features.DecodeHorizonUpper, s.config.PrefillTokenBucket),
	}
}

func schedulerFeatures(state domain.VirtualState, request domain.RequestCost) SchedulerFeatures {
	requestSequences := nonNegativeInt(request.DecodeSequencesUpper)
	if requestSequences == 0 {
		requestSequences = 1
	}
	existingSequences := nonNegativeInt(state.DecodeSequences)
	existingPending := nonNegativeInt(state.PendingPrefillSequences)
	if existingPending > existingSequences {
		existingPending = existingSequences
	}
	requestComplexity := request.RequestComplexityTokensUpper
	if requestComplexity < request.InputTokens {
		requestComplexity = request.InputTokens
	}
	return SchedulerFeatures{
		ExistingDecodeSequences:         existingSequences,
		DecodeSequences:                 addIntSaturating(existingSequences, requestSequences),
		ExistingPendingPrefillSequences: existingPending,
		PendingPrefillSequences:         addIntSaturating(existingPending, requestSequences),
		ExistingActiveContextTokens:     nonNegativeInt64(state.ActiveContextTokens),
		ExistingUncachedPrefill:         nonNegativeInt64(state.UncachedPrefillTokens),
		ExistingPhysicalKVUpper:         nonNegativeInt64(state.PhysicalKVUpper),
		ExistingActiveKVUpper:           nonNegativeInt64(state.ActiveKVUpper),
		RequestComplexityTokensUpper:    nonNegativeInt64(requestComplexity),
		ActiveContextTokens:             addInt64Saturating(nonNegativeInt64(state.ActiveContextTokens), nonNegativeInt64(request.ActiveContextTokensUpper)),
		UncachedPrefillTokens:           addInt64Saturating(nonNegativeInt64(state.UncachedPrefillTokens), nonNegativeInt64(request.UncachedPrefillUpper)),
		AccruedLocalAdmissionLatency:    request.AccruedLocalAdmissionLatency,
		PhysicalKVUpper:                 addInt64Saturating(nonNegativeInt64(state.PhysicalKVUpper), nonNegativeInt64(request.KV.PhysicalKVUpper)),
		ActiveKVUpper:                   addInt64Saturating(nonNegativeInt64(state.ActiveKVUpper), nonNegativeInt64(request.KV.ActiveKVUpper)),
		DecodeHorizonUpper:              nonNegativeInt64(request.DecodeHorizonUpper),
	}
}

func readyExistingDecodeSequences(features SchedulerFeatures) int {
	ready := features.ExistingDecodeSequences - features.ExistingPendingPrefillSequences
	if ready < 0 {
		return 0
	}
	return ready
}

func residualFromOutcome(prediction SchedulerPrediction, outcome SchedulerOutcome) (residualSample, error) {
	sample := residualSample{ObservedAt: outcome.ObservedAt, Features: prediction.Features}
	valid := 0
	if outcome.ExistingUserTPSValid {
		if prediction.Prior.ExistingUserTPSNotApplicable {
			return residualSample{}, fmt.Errorf("existing-user TPS outcome is not applicable")
		}
		ratio, err := nonNegativeRatio(outcome.ExistingUserTPS, prediction.Prior.ExistingUserTPSLower, "existing-user TPS")
		if err != nil {
			return residualSample{}, err
		}
		sample.ExistingUserTPSRatio = ratio
		sample.ExistingUserTPSValid = true
		valid++
	}
	if outcome.UserTPSValid {
		ratio, err := positiveRatio(outcome.UserTPS, prediction.Prior.NewUserTPSLower, "per-user TPS")
		if err != nil {
			return residualSample{}, err
		}
		sample.UserTPSRatio = ratio
		sample.UserTPSValid = true
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

func nonNegativeRatio(observed, predicted float64, name string) (float64, error) {
	if !nonNegativeFinite(observed) || !positiveFinite(predicted) {
		return 0, fmt.Errorf("scheduler %s observation or prior is invalid", name)
	}
	ratio := observed / predicted
	if !nonNegativeFinite(ratio) {
		return 0, fmt.Errorf("scheduler %s residual is invalid", name)
	}
	return ratio, nil
}

func quantileInPlace(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 1
	}
	sort.Float64s(values)
	index := int(math.Ceil(q*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func appendResidualRatio(values []float64, value float64, capacityHint int) []float64 {
	if values == nil {
		values = make([]float64, 0, capacityHint)
	}
	return append(values, value)
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

func addIntSaturating(left, right int) int {
	maximum := int(^uint(0) >> 1)
	if right > 0 && left > maximum-right {
		return maximum
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

func minimumPositiveInt(current, candidate int) int {
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
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
