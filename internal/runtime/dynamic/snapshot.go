package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Snapshot struct {
	Enabled                    bool
	Enforce                    bool
	Decision                   decision.Decision
	State                      string
	Source                     string
	Error                      string
	Updated                    time.Time
	Running                    int
	Waiting                    int
	KVCacheUsage               float64
	Preemptions                uint64
	Generation                 uint64
	GenerationTPS              float64
	GenerationTPSValid         bool
	TTFTCumulative             HistogramSample
	SemanticTTFTCumulative     HistogramSample
	TTFTSource                 string
	TTFTWindowCount            uint64
	TTFTWindowAvg              float64
	TTFTWindowP95              float64
	TTFTWindowP99              float64
	TTFTWindowValid            bool
	TTFTSmoothedAvg            float64
	TTFTSmoothedP95            float64
	TTFTSmoothedP99            float64
	TTFTHighCount              int
	TTFTP99HighCount           int
	TTFTHealthyCount           int
	TTFTLearnState             string
	TTFTLearnReason            string
	TTFTTargetReason           string
	TTFTLearnedLimit           int
	TTFTTargetLimit            int
	TTFTLimit                  int
	CapacityTPS                float64
	CapacityRatio              float64
	CapacityRatioHealthyCount  int
	CapacityRawLimit           int
	CapacitySafeLimit          int
	CapacityLowConfidenceLimit int
	CapacityEstimateConfidence string
	CapacityRepresentativeLoad bool
	CapacityLearnMode          string
	CapacityLearnState         string
	CapacityLearnReason        string
	CapacityTargetReason       string
	CapacityProjectedLimit     int
	CapacityLearnedLimit       int
	CapacityTargetLimit        int
	CapacityLimit              int
	HardGlobalLimit            int
	StateLimit                 int
	ThroughputLimit            int
	AvailabilityLimit          int
	UserTPS                    float64
	UserTPSYellowCount         int
	UserTPSRedCount            int
	PrefillProtected           int
	PrefillTransition          bool
	PrefillSettling            bool
	PrefillLimit               int
	PrefillReason              string
	PrefillTargetReason        string
	DecodeRunning              int
	QOSLimit                   int
	RepresentativeUserTPSLoad  bool
	PressureLimit              int
	PressureReason             string
	PressureTargetReason       string
	BackendCount               int
	BackendFailed              int
	DynamicRejected            uint64
	DynamicRejectedDelta       uint64
	TierBasicRejected          uint64
	TierPremiumRejected        uint64
	TierBasicRejectedDelta     uint64
	TierPremiumRejectedDelta   uint64
	TierBasicWaiting           int64
	TierPremiumWaiting         int64
	TierDemandPressure         bool
	CapacityDemandPressure     bool
	GlobalLimit                int
	FinalLimitReason           string
	YellowReasons              []string
	RedReasons                 []string
}

type MetricSample = telemetry.Sample
type HistogramBucketSample = telemetry.HistogramBucketSample
type HistogramSample = telemetry.HistogramSample

func (snapshot Snapshot) DecisionState() string {
	if snapshot.Decision.State != "" {
		return snapshot.Decision.State
	}
	return snapshot.State
}

func (snapshot Snapshot) DecisionYellowReasons() []string {
	if len(snapshot.Decision.YellowReasons) > 0 {
		return snapshot.Decision.YellowReasons
	}
	return snapshot.YellowReasons
}

func (snapshot Snapshot) DecisionRedReasons() []string {
	if len(snapshot.Decision.RedReasons) > 0 {
		return snapshot.Decision.RedReasons
	}
	return snapshot.RedReasons
}

func (snapshot Snapshot) DecisionPrimaryReasons() []string {
	if reasons := snapshot.DecisionRedReasons(); len(reasons) > 0 {
		return reasons
	}
	return snapshot.DecisionYellowReasons()
}
