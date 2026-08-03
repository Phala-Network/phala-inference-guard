package predictive

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const (
	maximumGlobalResidualSamples           = 1_024
	defaultAdverseEvidenceMaxAge           = 10 * time.Second
	aggregateThroughputQuantile            = 0.50
	aggregateThroughputMinimumRelativeGain = 0.01
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
	AdverseEvidenceMaxAge    time.Duration
	HardUserTPSTarget        float64
	HardTPOTSLO              time.Duration
	ExplorationUserTPSTarget float64
	ExplorationTPOTSLO       time.Duration
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
	if c.AdverseEvidenceMaxAge <= 0 || c.AdverseEvidenceMaxAge > c.MaxAge {
		return fmt.Errorf("scheduler adverse evidence maximum age is invalid")
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
	if !nonNegativeFinite(c.HardUserTPSTarget) || c.HardTPOTSLO < 0 ||
		(c.HardUserTPSTarget == 0) != (c.HardTPOTSLO == 0) {
		return fmt.Errorf("scheduler hard TPS and TPOT targets are invalid")
	}
	if (c.ExplorationUserTPSTarget == 0) != (c.ExplorationTPOTSLO == 0) {
		return fmt.Errorf("scheduler exploration TPS and TPOT targets must be configured together")
	}
	if c.ExplorationUserTPSTarget != 0 && (!positiveFinite(c.ExplorationUserTPSTarget) || c.ExplorationTPOTSLO <= 0) {
		return fmt.Errorf("scheduler exploration target band is invalid")
	}
	if c.ExplorationUserTPSTarget > 0 &&
		(c.HardUserTPSTarget <= 0 || c.HardUserTPSTarget > c.ExplorationUserTPSTarget ||
			c.HardTPOTSLO < c.ExplorationTPOTSLO) {
		return fmt.Errorf("scheduler hard target must contain the soft exploration band")
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
	Exploratory bool
}

type LoadPressureKind string

const (
	LoadPressureWaiting    LoadPressureKind = "waiting"
	LoadPressurePreemption LoadPressureKind = "preemption"
)

func (k LoadPressureKind) Validate() error {
	switch k {
	case LoadPressureWaiting, LoadPressurePreemption:
		return nil
	default:
		return fmt.Errorf("scheduler load pressure kind %q is invalid", k)
	}
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

// AggregateThroughputOutcome is an anonymous stable backend window. It is kept
// separate from per-request SchedulerOutcome because a completion does not
// observe every concurrently decoding user's TPS in the same causal window.
type AggregateThroughputOutcome struct {
	Identity               ModelIdentity
	StartedAt              time.Time
	ObservedAt             time.Time
	DecodeSequences        int
	AggregateCompletionTPS float64
}

type LearnedSchedulerSnapshot struct {
	SamplesAccepted            uint64
	SamplesRejected            uint64
	ExistingUserTPSSamples     uint64
	NewUserTPSSamples          uint64
	AggregateThroughputSamples uint64
	Invalidations              uint64
	Cells                      int
	GlobalSamples              int
	AggregateThroughputCells   int
	AdverseEvidenceMaxAge      time.Duration
	ExplorationBlockedUntil    time.Time
	LastLoadPressureAt         time.Time
	AdverseEvidenceEvents      uint64
	SoftExistingTPSMisses      uint64
	SoftNewTPSMisses           uint64
	SoftTPOTMisses             uint64
	ExploratoryPredictions     uint64
	ExploratorySamples         uint64
	WaitingPressureEvents      uint64
	PreemptionPressureEvents   uint64
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
	ObservedAt             time.Time
	Features               SchedulerFeatures
	Censored               bool
	ExistingUserTPSRatio   float64
	UserTPSRatio           float64
	TTFTRatio              float64
	TPOTRatio              float64
	ExistingUserTPSValid   bool
	UserTPSValid           bool
	TTFTValid              bool
	TPOTValid              bool
	ExistingUserTPSAdverse bool
	UserTPSAdverse         bool
	TPOTAdverse            bool
}

type throughputFrontierEvidence struct {
	AggregateCompletionTPSEstimate         float64
	PreviousAggregateCompletionTPSEstimate float64
	Samples                                int
	ValidUntil                             time.Time
	Ready                                  bool
	Reached                                bool
}

type residualCell struct {
	CreatedSequence uint64
	Samples         []residualSample
}

type aggregateThroughputSample struct {
	ObservedAt time.Time
	TPS        float64
}

type aggregateThroughputCell struct {
	CreatedSequence uint64
	Samples         []aggregateThroughputSample
	Estimate        float64
	SampleCount     int
	ValidUntil      time.Time
	Ready           bool
}

type LearnedScheduler struct {
	mu                              sync.Mutex
	profile                         StaticSchedulerProfile
	config                          ResidualCalibratorConfig
	cells                           map[featureCell]*residualCell
	globalSamples                   []residualSample
	globalCounts                    map[featureCell]int
	globalLimit                     int
	aggregateThroughputCells        map[int]*aggregateThroughputCell
	aggregateThroughputScratch      []float64
	cellSequence                    uint64
	aggregateThroughputCellSequence uint64
	samplesAccepted                 uint64
	samplesRejected                 uint64
	existingUserTPSSamples          uint64
	newUserTPSSamples               uint64
	aggregateThroughputSamples      uint64
	invalidations                   uint64
	explorationBlockedUntil         time.Time
	lastLoadPressureAt              time.Time
	adverseEvidenceEvents           uint64
	softExistingTPSMisses           uint64
	softNewTPSMisses                uint64
	softTPOTMisses                  uint64
	exploratoryPredictions          uint64
	exploratorySamples              uint64
	waitingPressureEvents           uint64
	preemptionPressureEvents        uint64
}

func (s *LearnedScheduler) InvalidateLearning() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.cells)
	s.globalSamples = nil
	clear(s.globalCounts)
	clear(s.aggregateThroughputCells)
	s.aggregateThroughputScratch = s.aggregateThroughputScratch[:0]
	s.explorationBlockedUntil = time.Time{}
	s.lastLoadPressureAt = time.Time{}
	s.invalidations++
	s.mu.Unlock()
}

func (s *LearnedScheduler) ObserveLoadPressure(now time.Time, kind LoadPressureKind) bool {
	if s == nil || now.IsZero() || kind.Validate() != nil {
		return false
	}
	until := now.Add(s.config.AdverseEvidenceMaxAge)
	s.mu.Lock()
	if until.After(s.explorationBlockedUntil) {
		s.explorationBlockedUntil = until
	}
	if now.After(s.lastLoadPressureAt) {
		s.lastLoadPressureAt = now
	}
	switch kind {
	case LoadPressureWaiting:
		s.waitingPressureEvents++
	case LoadPressurePreemption:
		s.preemptionPressureEvents++
	}
	s.mu.Unlock()
	return true
}

func NewLearnedScheduler(profile StaticSchedulerProfile, config ResidualCalibratorConfig) (*LearnedScheduler, error) {
	config = normalizeResidualCalibratorConfig(config)
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
		profile:                  profile,
		config:                   config,
		cells:                    make(map[featureCell]*residualCell),
		globalCounts:             make(map[featureCell]int),
		globalLimit:              globalResidualSampleLimit(config),
		aggregateThroughputCells: make(map[int]*aggregateThroughputCell),
	}, nil
}

func normalizeResidualCalibratorConfig(config ResidualCalibratorConfig) ResidualCalibratorConfig {
	if config.AdverseEvidenceMaxAge == 0 {
		config.AdverseEvidenceMaxAge = defaultAdverseEvidenceMaxAge
		if config.MaxAge > 0 && config.MaxAge < config.AdverseEvidenceMaxAge {
			config.AdverseEvidenceMaxAge = config.MaxAge
		}
	}
	return config
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
		local = freshLocalResidualRatios(cell.Samples, now, s.config.MaxAge, s.config.AdverseEvidenceMaxAge, features, s.config.DecodeSequenceBucket)
	}
	throughput := s.aggregateThroughputFrontierLocked(features.DecodeSequences, now)
	explorationBlocked := now.Before(s.explorationBlockedUntil)
	global := residualRatios{}
	if explorationBlocked || requiresGlobalResidualFallback(local, s.config.MinimumSamples, readyExistingDecodeSequences(features) > 0) {
		global = freshCompatibleResidualRatios(s.globalSamples, now, s.config.MaxAge, s.config.AdverseEvidenceMaxAge, features, s.config.MinimumSamples, s.config.DecodeSequenceBucket)
	}
	s.mu.Unlock()

	userRatios, userExploratory := preferTPSRatiosWithExploration(local.UserTPS, global.UserTPS, s.config.MinimumSamples)
	existingUserRatios, existingExploratory := preferTPSRatiosWithExploration(local.ExistingUserTPS, global.ExistingUserTPS, s.config.MinimumSamples)
	ttftRatios := preferMatureRatios(local.TTFT, global.TTFT, s.config.MinimumSamples)
	tpotRatios, tpotExploratory := preferTPOTRatiosWithExploration(local.TPOT, global.TPOT, s.config.MinimumSamples)
	calibratedSamples := 0
	nonExploratorySamples := 0
	nonExploratoryEstimate := prior
	exploratory := false
	frontierReached := false
	frontierSamples := 0
	if len(userRatios) > 0 {
		userMultiplier := clampFloat(quantileInPlace(userRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
		candidate := prior.NewUserTPSLower * userMultiplier
		if userExploratory && explorationBlocked {
			frontierReached = true
			frontierSamples = minimumPositiveInt(frontierSamples, len(userRatios))
		} else {
			prediction.Estimate.NewUserTPSLower = candidate
			calibratedSamples = minimumPositiveInt(calibratedSamples, len(userRatios))
			if userExploratory {
				exploratory = true
			} else {
				nonExploratoryEstimate.NewUserTPSLower = candidate
				nonExploratorySamples = minimumPositiveInt(nonExploratorySamples, len(userRatios))
			}
		}
	}
	if len(existingUserRatios) > 0 && !prior.ExistingUserTPSNotApplicable {
		existingUserMultiplier := clampFloat(quantileInPlace(existingUserRatios, s.config.LowerQuantile), s.config.MinimumTPSMultiplier, s.config.MaximumTPSMultiplier)
		candidate := prior.ExistingUserTPSLower * existingUserMultiplier
		if existingExploratory && explorationBlocked {
			frontierReached = true
			frontierSamples = minimumPositiveInt(frontierSamples, len(existingUserRatios))
		} else {
			prediction.Estimate.ExistingUserTPSLower = candidate
			calibratedSamples = minimumPositiveInt(calibratedSamples, len(existingUserRatios))
			if existingExploratory {
				exploratory = true
			} else {
				nonExploratoryEstimate.ExistingUserTPSLower = candidate
				nonExploratorySamples = minimumPositiveInt(nonExploratorySamples, len(existingUserRatios))
			}
		}
	}
	if len(ttftRatios) >= s.config.MinimumSamples {
		ttftMultiplier := clampFloat(quantileInPlace(ttftRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
		prediction.Estimate.TTFTUpper = scaleDuration(prior.TTFTUpper, ttftMultiplier)
		nonExploratoryEstimate.TTFTUpper = prediction.Estimate.TTFTUpper
		calibratedSamples = minimumPositiveInt(calibratedSamples, len(ttftRatios))
		nonExploratorySamples = minimumPositiveInt(nonExploratorySamples, len(ttftRatios))
	}
	if len(tpotRatios) > 0 {
		tpotMultiplier := clampFloat(quantileInPlace(tpotRatios, s.config.UpperQuantile), s.config.MinimumLatencyMultiplier, s.config.MaximumLatencyMultiplier)
		candidate := scaleDuration(prior.TPOTUpper, tpotMultiplier)
		if tpotExploratory && explorationBlocked {
			frontierReached = true
			frontierSamples = minimumPositiveInt(frontierSamples, len(tpotRatios))
		} else {
			prediction.Estimate.TPOTUpper = candidate
			calibratedSamples = minimumPositiveInt(calibratedSamples, len(tpotRatios))
			if tpotExploratory {
				exploratory = true
			} else {
				nonExploratoryEstimate.TPOTUpper = candidate
				nonExploratorySamples = minimumPositiveInt(nonExploratorySamples, len(tpotRatios))
			}
		}
	}
	if frontierReached && exploratory {
		prediction.Estimate = nonExploratoryEstimate
		calibratedSamples = nonExploratorySamples
		exploratory = false
	}
	if features.ExistingDecodeSequences == 0 {
		if prediction.Estimate.NewUserTPSLower < prior.NewUserTPSLower {
			prediction.Estimate.NewUserTPSLower = prior.NewUserTPSLower
			nonExploratoryEstimate.NewUserTPSLower = prior.NewUserTPSLower
		}
		if prediction.Estimate.TPOTUpper > prior.TPOTUpper {
			prediction.Estimate.TPOTUpper = prior.TPOTUpper
			nonExploratoryEstimate.TPOTUpper = prior.TPOTUpper
		}
	}
	if throughput.Ready {
		prediction.Estimate.AggregateCompletionTPSEstimate = throughput.AggregateCompletionTPSEstimate
		prediction.Estimate.PreviousAggregateCompletionTPSEstimate = throughput.PreviousAggregateCompletionTPSEstimate
		prediction.Estimate.ThroughputFrontierReached = throughput.Reached
		calibratedSamples = minimumPositiveInt(calibratedSamples, throughput.Samples)
	}
	if frontierReached {
		prediction.Estimate.ThroughputFrontierReached = true
		calibratedSamples = minimumPositiveInt(calibratedSamples, frontierSamples)
	}
	if calibratedSamples == 0 {
		return prediction
	}
	if prediction.Estimate == prior {
		return prediction
	}
	prediction.Source = PredictionSourceCalibrated
	prediction.Samples = calibratedSamples
	prediction.Confidence = minimumConfidence(s.profile.Confidence, s.config.CalibratedConfidence)
	prediction.Exploratory = exploratory
	if exploratory {
		s.mu.Lock()
		s.exploratoryPredictions++
		s.mu.Unlock()
	}
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
	s.mu.Lock()
	lastLoadPressureAt := s.lastLoadPressureAt
	s.mu.Unlock()
	if lastLoadPressureAt.After(prediction.PredictedAt) && !lastLoadPressureAt.After(outcome.ObservedAt) {
		return s.rejectOutcome(fmt.Errorf("scheduler outcome crossed a load-pressure event"))
	}
	sample, err := residualFromOutcome(prediction, outcome)
	if err != nil {
		return s.rejectOutcome(err)
	}
	classifyAdverseEvidence(&sample, outcome, s.config)
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
	if recordAdverseEvidenceLocked(s, sample) {
		s.adverseEvidenceEvents++
	}
	if softTPSMiss(sample.ExistingUserTPSValid, sample.ExistingUserTPSAdverse, outcome.ExistingUserTPS, s.config.ExplorationUserTPSTarget) {
		s.softExistingTPSMisses++
	}
	if softTPSMiss(sample.UserTPSValid, sample.UserTPSAdverse, outcome.UserTPS, s.config.ExplorationUserTPSTarget) {
		s.softNewTPSMisses++
	}
	if softTPOTMiss(sample.TPOTValid, sample.TPOTAdverse, outcome.TPOT, s.config.ExplorationTPOTSLO) {
		s.softTPOTMisses++
	}
	if prediction.Exploratory {
		s.exploratorySamples++
	}
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

func (s *LearnedScheduler) ObserveAggregateThroughput(outcome AggregateThroughputOutcome) error {
	if s == nil {
		return fmt.Errorf("learned scheduler is nil")
	}
	if outcome.Identity != s.profile.Identity {
		return s.rejectOutcome(fmt.Errorf("aggregate throughput outcome identity mismatch"))
	}
	if outcome.StartedAt.IsZero() || outcome.ObservedAt.IsZero() || !outcome.ObservedAt.After(outcome.StartedAt) {
		return s.rejectOutcome(fmt.Errorf("aggregate throughput outcome window is invalid"))
	}
	if outcome.DecodeSequences <= 0 || !positiveFinite(outcome.AggregateCompletionTPS) {
		return s.rejectOutcome(fmt.Errorf("aggregate throughput outcome is invalid"))
	}

	bucket := bucketInt(outcome.DecodeSequences, s.config.DecodeSequenceBucket)
	if bucket <= 0 {
		return s.rejectOutcome(fmt.Errorf("aggregate throughput bucket is invalid"))
	}
	s.mu.Lock()
	cell := s.aggregateThroughputCells[bucket]
	if cell == nil {
		if len(s.aggregateThroughputCells) >= s.config.MaximumCells {
			s.evictOldestAggregateThroughputCellLocked()
		}
		s.aggregateThroughputCellSequence++
		cell = &aggregateThroughputCell{CreatedSequence: s.aggregateThroughputCellSequence}
		s.aggregateThroughputCells[bucket] = cell
	}
	cell.Samples = append(cell.Samples, aggregateThroughputSample{
		ObservedAt: outcome.ObservedAt,
		TPS:        outcome.AggregateCompletionTPS,
	})
	if excess := len(cell.Samples) - s.config.MaximumSamplesPerCell; excess > 0 {
		copy(cell.Samples, cell.Samples[excess:])
		cell.Samples = cell.Samples[:s.config.MaximumSamplesPerCell]
	}
	s.refreshAggregateThroughputCellLocked(cell, outcome.ObservedAt)
	s.aggregateThroughputSamples++
	s.mu.Unlock()
	return nil
}

func (s *LearnedScheduler) refreshAggregateThroughputCellLocked(cell *aggregateThroughputCell, now time.Time) {
	cell.Estimate = 0
	cell.SampleCount = 0
	cell.ValidUntil = time.Time{}
	cell.Ready = false
	values := s.aggregateThroughputScratch[:0]
	validUntil := time.Time{}
	for _, sample := range cell.Samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > s.config.MaxAge || !positiveFinite(sample.TPS) {
			continue
		}
		values = append(values, sample.TPS)
		expires := sample.ObservedAt.Add(s.config.MaxAge)
		if validUntil.IsZero() || expires.Before(validUntil) {
			validUntil = expires
		}
	}
	s.aggregateThroughputScratch = values
	if len(values) < s.config.MinimumSamples || validUntil.IsZero() {
		return
	}
	estimate := quantileInPlace(values, aggregateThroughputQuantile)
	if !positiveFinite(estimate) {
		return
	}
	cell.Estimate = estimate
	cell.SampleCount = len(values)
	cell.ValidUntil = validUntil
	cell.Ready = true
}

func (s *LearnedScheduler) evictOldestAggregateThroughputCellLocked() {
	oldestBucket := 0
	var oldestSequence uint64
	found := false
	for bucket, cell := range s.aggregateThroughputCells {
		if !found || cell.CreatedSequence < oldestSequence {
			oldestBucket = bucket
			oldestSequence = cell.CreatedSequence
			found = true
		}
	}
	if found {
		delete(s.aggregateThroughputCells, oldestBucket)
	}
}

func requiresGlobalResidualFallback(local residualRatios, minimum int, existingTPSApplicable bool) bool {
	// TTFT is observation-only. Preserve local TTFT calibration and collect a
	// compatible global TTFT fallback opportunistically when a protected TPS or
	// TPOT dimension needs the scan, but never scan the global store only for
	// TTFT diagnostics.
	return len(local.UserTPS.Standard) < minimum || len(local.TPOT.Standard) < minimum ||
		(existingTPSApplicable && len(local.ExistingUserTPS.Standard) < minimum)
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
		SamplesAccepted:            s.samplesAccepted,
		SamplesRejected:            s.samplesRejected,
		ExistingUserTPSSamples:     s.existingUserTPSSamples,
		NewUserTPSSamples:          s.newUserTPSSamples,
		AggregateThroughputSamples: s.aggregateThroughputSamples,
		Invalidations:              s.invalidations,
		Cells:                      len(s.cells),
		GlobalSamples:              len(s.globalSamples),
		AggregateThroughputCells:   len(s.aggregateThroughputCells),
		AdverseEvidenceMaxAge:      s.config.AdverseEvidenceMaxAge,
		ExplorationBlockedUntil:    s.explorationBlockedUntil,
		LastLoadPressureAt:         s.lastLoadPressureAt,
		AdverseEvidenceEvents:      s.adverseEvidenceEvents,
		SoftExistingTPSMisses:      s.softExistingTPSMisses,
		SoftNewTPSMisses:           s.softNewTPSMisses,
		SoftTPOTMisses:             s.softTPOTMisses,
		ExploratoryPredictions:     s.exploratoryPredictions,
		ExploratorySamples:         s.exploratorySamples,
		WaitingPressureEvents:      s.waitingPressureEvents,
		PreemptionPressureEvents:   s.preemptionPressureEvents,
	}
}

type residualDimension struct {
	Standard      []float64
	Exploratory   []float64
	Adverse       []float64
	LastAdverseAt time.Time
}

type residualRatios struct {
	ExistingUserTPS residualDimension
	UserTPS         residualDimension
	TTFT            []float64
	TPOT            residualDimension
}

func freshLocalResidualRatios(samples []residualSample, now time.Time, maxAge, adverseMaxAge time.Duration, query SchedulerFeatures, decodeBucket int) residualRatios {
	cutoffs := latestCompatibleAdverseEvidence(samples, now, maxAge, query)
	ratios := residualRatios{
		ExistingUserTPS: residualDimension{LastAdverseAt: cutoffs.ExistingUserTPS},
		UserTPS:         residualDimension{LastAdverseAt: cutoffs.UserTPS},
		TPOT:            residualDimension{LastAdverseAt: cutoffs.TPOT},
	}
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > maxAge || sample.Censored {
			continue
		}
		if sample.ExistingUserTPSValid {
			ratio := sample.ExistingUserTPSRatio
			switch {
			case sample.ExistingUserTPSAdverse && age <= adverseMaxAge && existingTPSAdverseCompatible(sample.Features, query):
				ratios.ExistingUserTPS.Adverse = appendResidualRatio(ratios.ExistingUserTPS.Adverse, ratio, len(samples))
			case !sample.ExistingUserTPSAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.ExistingUserTPS):
				appendExistingTPSRatio(&ratios.ExistingUserTPS, sample, query, decodeBucket, len(samples))
			}
		}
		if sample.UserTPSValid {
			ratio := sample.UserTPSRatio
			switch {
			case sample.UserTPSAdverse && age <= adverseMaxAge && userTPSAdverseCompatible(sample.Features, query):
				ratios.UserTPS.Adverse = appendResidualRatio(ratios.UserTPS.Adverse, ratio, len(samples))
			case !sample.UserTPSAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.UserTPS):
				appendUserTPSRatio(&ratios.UserTPS, sample, query, decodeBucket, len(samples))
			}
		}
		if sample.TTFTValid && ttftResidualCompatible(sample.Features, query, sample.TTFTRatio) {
			ratios.TTFT = appendResidualRatio(ratios.TTFT, sample.TTFTRatio, len(samples))
		}
		if sample.TPOTValid {
			ratio := sample.TPOTRatio
			switch {
			case sample.TPOTAdverse && age <= adverseMaxAge && tpotAdverseCompatible(sample.Features, query):
				ratios.TPOT.Adverse = appendResidualRatio(ratios.TPOT.Adverse, ratio, len(samples))
			case !sample.TPOTAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.TPOT):
				appendTPOTRatio(&ratios.TPOT, sample, query, decodeBucket, len(samples))
			}
		}
	}
	return ratios
}

func (s *LearnedScheduler) aggregateThroughputFrontierLocked(decodeSequences int, now time.Time) throughputFrontierEvidence {
	if s == nil || now.IsZero() || decodeSequences <= 1 {
		return throughputFrontierEvidence{}
	}
	currentBucket := bucketInt(decodeSequences, s.config.DecodeSequenceBucket)
	if currentBucket <= 0 {
		return throughputFrontierEvidence{}
	}
	current, currentReady := freshAggregateThroughputCell(s.aggregateThroughputCells[currentBucket], now)
	previous, previousReady := freshAggregateThroughputCell(s.aggregateThroughputCells[currentBucket-1], now)
	if !currentReady || !previousReady {
		return throughputFrontierEvidence{}
	}
	return throughputFrontierEvidence{
		AggregateCompletionTPSEstimate:         current.Estimate,
		PreviousAggregateCompletionTPSEstimate: previous.Estimate,
		Samples:                                minimumPositiveInt(current.SampleCount, previous.SampleCount),
		ValidUntil:                             earlierTime(current.ValidUntil, previous.ValidUntil),
		Ready:                                  true,
		Reached:                                aggregateThroughputGainIsEquivalent(current.Estimate, previous.Estimate),
	}
}

func aggregateThroughputGainIsEquivalent(current, previous float64) bool {
	if !positiveFinite(current) || !positiveFinite(previous) {
		return false
	}
	return current/previous <= 1+aggregateThroughputMinimumRelativeGain
}

func freshAggregateThroughputCell(cell *aggregateThroughputCell, now time.Time) (*aggregateThroughputCell, bool) {
	if cell == nil || !cell.Ready || cell.ValidUntil.IsZero() || now.After(cell.ValidUntil) {
		return nil, false
	}
	return cell, true
}

func freshCompatibleResidualRatios(samples []residualSample, now time.Time, maxAge, adverseMaxAge time.Duration, query SchedulerFeatures, minimum, decodeBucket int) residualRatios {
	if minimum <= 0 {
		return residualRatios{}
	}
	cutoffs := latestCompatibleAdverseEvidence(samples, now, maxAge, query)
	groups := make(map[int]*residualRatios)
	result := residualRatios{
		ExistingUserTPS: residualDimension{LastAdverseAt: cutoffs.ExistingUserTPS},
		UserTPS:         residualDimension{LastAdverseAt: cutoffs.UserTPS},
		TPOT:            residualDimension{LastAdverseAt: cutoffs.TPOT},
	}
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
		if sample.ExistingUserTPSValid {
			ratio := sample.ExistingUserTPSRatio
			switch {
			case sample.ExistingUserTPSAdverse && age <= adverseMaxAge && existingTPSAdverseCompatible(sample.Features, query):
				result.ExistingUserTPS.Adverse = append(result.ExistingUserTPS.Adverse, ratio)
			case !sample.ExistingUserTPSAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.ExistingUserTPS):
				appendExistingTPSRatio(&group.ExistingUserTPS, sample, query, decodeBucket, 0)
			}
		}
		if sample.UserTPSValid {
			ratio := sample.UserTPSRatio
			switch {
			case sample.UserTPSAdverse && age <= adverseMaxAge && userTPSAdverseCompatible(sample.Features, query):
				result.UserTPS.Adverse = append(result.UserTPS.Adverse, ratio)
			case !sample.UserTPSAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.UserTPS):
				appendUserTPSRatio(&group.UserTPS, sample, query, decodeBucket, 0)
			}
		}
		if sample.TTFTValid && ttftResidualCompatible(sample.Features, query, sample.TTFTRatio) {
			group.TTFT = append(group.TTFT, sample.TTFTRatio)
		}
		if sample.TPOTValid {
			ratio := sample.TPOTRatio
			switch {
			case sample.TPOTAdverse && age <= adverseMaxAge && tpotAdverseCompatible(sample.Features, query):
				result.TPOT.Adverse = append(result.TPOT.Adverse, ratio)
			case !sample.TPOTAdverse && newerThanAdverse(sample.ObservedAt, cutoffs.TPOT):
				appendTPOTRatio(&group.TPOT, sample, query, decodeBucket, 0)
			}
		}
	}

	existingSequence := -1
	existingExplorationSequence := -1
	userSequence := -1
	userExplorationSequence := -1
	ttftSequence := -1
	tpotSequence := -1
	tpotExplorationSequence := -1
	existingAdverse := len(result.ExistingUserTPS.Adverse) > 0
	userAdverse := len(result.UserTPS.Adverse) > 0
	tpotAdverse := len(result.TPOT.Adverse) > 0
	for sequence, group := range groups {
		if !existingAdverse && len(group.ExistingUserTPS.Standard) >= minimum && sequence > existingSequence {
			existingSequence = sequence
			result.ExistingUserTPS.Standard = group.ExistingUserTPS.Standard
		}
		if len(group.ExistingUserTPS.Exploratory) >= minimum && sequence > existingExplorationSequence {
			existingExplorationSequence = sequence
			result.ExistingUserTPS.Exploratory = group.ExistingUserTPS.Exploratory
		}
		if !userAdverse && len(group.UserTPS.Standard) >= minimum && sequence > userSequence {
			userSequence = sequence
			result.UserTPS.Standard = group.UserTPS.Standard
		}
		if len(group.UserTPS.Exploratory) >= minimum && sequence > userExplorationSequence {
			userExplorationSequence = sequence
			result.UserTPS.Exploratory = group.UserTPS.Exploratory
		}
		if len(group.TTFT) >= minimum && sequence > ttftSequence {
			ttftSequence = sequence
			result.TTFT = group.TTFT
		}
		if !tpotAdverse && len(group.TPOT.Standard) >= minimum && sequence > tpotSequence {
			tpotSequence = sequence
			result.TPOT.Standard = group.TPOT.Standard
		}
		if len(group.TPOT.Exploratory) >= minimum && sequence > tpotExplorationSequence {
			tpotExplorationSequence = sequence
			result.TPOT.Exploratory = group.TPOT.Exploratory
		}
	}
	return result
}

type adverseEvidenceCutoffs struct {
	ExistingUserTPS time.Time
	UserTPS         time.Time
	TPOT            time.Time
}

func latestCompatibleAdverseEvidence(samples []residualSample, now time.Time, maxAge time.Duration, query SchedulerFeatures) adverseEvidenceCutoffs {
	cutoffs := adverseEvidenceCutoffs{}
	for _, sample := range samples {
		age := now.Sub(sample.ObservedAt)
		if age < 0 || age > maxAge || sample.Censored {
			continue
		}
		if sample.ExistingUserTPSValid && sample.ExistingUserTPSAdverse && existingTPSAdverseCompatible(sample.Features, query) {
			cutoffs.ExistingUserTPS = laterTime(cutoffs.ExistingUserTPS, sample.ObservedAt)
		}
		if sample.UserTPSValid && sample.UserTPSAdverse && userTPSAdverseCompatible(sample.Features, query) {
			cutoffs.UserTPS = laterTime(cutoffs.UserTPS, sample.ObservedAt)
		}
		if sample.TPOTValid && sample.TPOTAdverse && tpotAdverseCompatible(sample.Features, query) {
			cutoffs.TPOT = laterTime(cutoffs.TPOT, sample.ObservedAt)
		}
	}
	return cutoffs
}

func appendExistingTPSRatio(target *residualDimension, sample residualSample, query SchedulerFeatures, decodeBucket, capacityHint int) {
	if existingTPSResidualCompatible(sample.Features, query, sample.ExistingUserTPSRatio) {
		target.Standard = appendResidualRatio(target.Standard, sample.ExistingUserTPSRatio, capacityHint)
		return
	}
	if existingTPSExplorationCompatible(sample.Features, query, sample.ExistingUserTPSRatio, decodeBucket) {
		target.Exploratory = appendResidualRatio(target.Exploratory, sample.ExistingUserTPSRatio, capacityHint)
	}
}

func appendUserTPSRatio(target *residualDimension, sample residualSample, query SchedulerFeatures, decodeBucket, capacityHint int) {
	if tpsResidualCompatible(sample.Features, query, sample.UserTPSRatio) {
		target.Standard = appendResidualRatio(target.Standard, sample.UserTPSRatio, capacityHint)
		return
	}
	if tpsExplorationCompatible(sample.Features, query, sample.UserTPSRatio, decodeBucket) {
		target.Exploratory = appendResidualRatio(target.Exploratory, sample.UserTPSRatio, capacityHint)
	}
}

func appendTPOTRatio(target *residualDimension, sample residualSample, query SchedulerFeatures, decodeBucket, capacityHint int) {
	if latencyResidualCompatible(sample.Features, query, sample.TPOTRatio) {
		target.Standard = appendResidualRatio(target.Standard, sample.TPOTRatio, capacityHint)
		return
	}
	if tpotExplorationCompatible(sample.Features, query, sample.TPOTRatio, decodeBucket) {
		target.Exploratory = appendResidualRatio(target.Exploratory, sample.TPOTRatio, capacityHint)
	}
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

func existingTPSAdverseCompatible(sample, query SchedulerFeatures) bool {
	return concurrencyAndPrefillPressureAtLeast(query, sample)
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
	return sample.DecodeSequences >= query.DecodeSequences && decodePressureAtLeast(sample, query)
}

func userTPSAdverseCompatible(_ SchedulerFeatures, _ SchedulerFeatures) bool {
	// A qualified joining-user TPS violation is short-lived but intentionally
	// survives request-size recalibration. Otherwise a slightly smaller optional
	// input-size hint could erase the capacity failure that the hint is meant to
	// help predict.
	return true
}

func latencyResidualCompatible(sample, query SchedulerFeatures, ratio float64) bool {
	if ratio > 1 {
		return decodePressureAtLeast(query, sample)
	}
	return sample.DecodeSequences >= query.DecodeSequences && decodePressureAtLeast(sample, query)
}

func tpotAdverseCompatible(sample, query SchedulerFeatures) bool {
	return decodePressureAtLeast(query, sample)
}

func existingTPSExplorationCompatible(sample, query SchedulerFeatures, ratio float64, decodeBucket int) bool {
	return ratio >= 1 && exactlyOneDecodeBucketHigher(sample, query, decodeBucket) &&
		normalizedPrefillPressureAtLeast(sample, query)
}

func tpsExplorationCompatible(sample, query SchedulerFeatures, ratio float64, decodeBucket int) bool {
	return ratio >= 1 && exactlyOneDecodeBucketHigher(sample, query, decodeBucket) &&
		decodePressureAtLeast(sample, query)
}

func tpotExplorationCompatible(sample, query SchedulerFeatures, ratio float64, decodeBucket int) bool {
	return ratio <= 1 && exactlyOneDecodeBucketHigher(sample, query, decodeBucket) &&
		decodePressureAtLeast(sample, query)
}

func exactlyOneDecodeBucketHigher(sample, query SchedulerFeatures, decodeBucket int) bool {
	if decodeBucket <= 0 || sample.DecodeSequences <= 0 || query.DecodeSequences <= 0 {
		return false
	}
	return bucketInt(query.DecodeSequences, decodeBucket) == bucketInt(sample.DecodeSequences, decodeBucket)+1
}

func normalizedPrefillPressureAtLeast(sample, query SchedulerFeatures) bool {
	return normalizedPressure(sample.UncachedPrefillTokens, sample.DecodeSequences) >= normalizedPressure(query.UncachedPrefillTokens, query.DecodeSequences) &&
		normalizedPressure(sample.RequestComplexityTokensUpper, sample.DecodeSequences) >= normalizedPressure(query.RequestComplexityTokensUpper, query.DecodeSequences) &&
		decodePressureAtLeast(sample, query)
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

func preferTPSRatiosWithExploration(local, global residualDimension, minimum int) ([]float64, bool) {
	local, global = preferLatestAdverseGeneration(local, global)
	if adverse, ok := minimumAdverseRatio(local.Adverse, global.Adverse); ok {
		return []float64{adverse}, false
	}
	if ratios := preferMatureRatios(local.Standard, global.Standard, minimum); len(ratios) >= minimum {
		return ratios, false
	}
	if len(local.Exploratory) >= minimum {
		return local.Exploratory, true
	}
	if len(global.Exploratory) >= minimum {
		return global.Exploratory, true
	}
	return nil, false
}

func preferTPOTRatiosWithExploration(local, global residualDimension, minimum int) ([]float64, bool) {
	local, global = preferLatestAdverseGeneration(local, global)
	if adverse, ok := maximumAdverseRatio(local.Adverse, global.Adverse); ok {
		return []float64{adverse}, false
	}
	if ratios := preferMatureRatios(local.Standard, global.Standard, minimum); len(ratios) >= minimum {
		return ratios, false
	}
	if len(local.Exploratory) >= minimum {
		return local.Exploratory, true
	}
	if len(global.Exploratory) >= minimum {
		return global.Exploratory, true
	}
	return nil, false
}

func preferLatestAdverseGeneration(local, global residualDimension) (residualDimension, residualDimension) {
	switch {
	case local.LastAdverseAt.Before(global.LastAdverseAt):
		local = residualDimension{}
	case global.LastAdverseAt.Before(local.LastAdverseAt):
		global = residualDimension{}
	}
	return local, global
}

func minimumAdverseRatio(groups ...[]float64) (float64, bool) {
	minimum := math.MaxFloat64
	found := false
	for _, ratios := range groups {
		for _, ratio := range ratios {
			if nonNegativeFinite(ratio) && ratio < minimum {
				minimum = ratio
				found = true
			}
		}
	}
	return minimum, found
}

func maximumAdverseRatio(groups ...[]float64) (float64, bool) {
	maximum := 0.0
	found := false
	for _, ratios := range groups {
		for _, ratio := range ratios {
			if positiveFinite(ratio) && ratio > maximum {
				maximum = ratio
				found = true
			}
		}
	}
	return maximum, found
}

func newerThanAdverse(observedAt, adverseAt time.Time) bool {
	return adverseAt.IsZero() || observedAt.After(adverseAt)
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func earlierTime(left, right time.Time) time.Time {
	if left.IsZero() || (!right.IsZero() && right.Before(left)) {
		return right
	}
	return left
}

func recordAdverseEvidenceLocked(s *LearnedScheduler, sample residualSample) bool {
	if s == nil {
		return false
	}
	adverse := (sample.ExistingUserTPSValid && sample.ExistingUserTPSAdverse) ||
		(sample.UserTPSValid && sample.UserTPSAdverse) ||
		(sample.TPOTValid && sample.TPOTAdverse)
	if !adverse {
		return false
	}
	until := sample.ObservedAt.Add(s.config.AdverseEvidenceMaxAge)
	if until.After(s.explorationBlockedUntil) {
		s.explorationBlockedUntil = until
	}
	return true
}

func classifyAdverseEvidence(sample *residualSample, outcome SchedulerOutcome, config ResidualCalibratorConfig) {
	if sample == nil {
		return
	}
	existingTPSAdverse := sample.ExistingUserTPSRatio < 1
	userTPSAdverse := sample.UserTPSRatio < 1
	tpotAdverse := sample.TPOTRatio > 1
	if config.HardUserTPSTarget > 0 {
		existingTPSAdverse = outcome.ExistingUserTPS < config.HardUserTPSTarget
		userTPSAdverse = outcome.UserTPS < config.HardUserTPSTarget
		tpotAdverse = outcome.TPOT > config.HardTPOTSLO
	}
	sample.ExistingUserTPSAdverse = sample.ExistingUserTPSValid &&
		existingTPSAdverse
	sample.UserTPSAdverse = sample.UserTPSValid && userTPSAdverse
	sample.TPOTAdverse = sample.TPOTValid && tpotAdverse
}

func softTPSMiss(valid, hardAdverse bool, actual, target float64) bool {
	return valid && !hardAdverse && target > 0 && actual < target
}

func softTPOTMiss(valid, hardAdverse bool, actual, target time.Duration) bool {
	return valid && !hardAdverse && target > 0 && actual > target
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
