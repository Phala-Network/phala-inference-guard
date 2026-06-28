package latency

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

const TargetSeconds = 1.0
const RedSeconds = 3.0
const P99TargetSeconds = 3.0
const P99RedSeconds = 8.0
const P99SignalWeight = 0.50
const SmoothRatio = 0.70
const HighConsecutive = 2
const P99HighConsecutive = 2
const HealthyConsecutive = 3
const MinRunning = 4
const MinWindowCount = 2
const P99MinWindowCount = 10
const MinLimit = 1
const HealthyMaxKVCacheUsage = 0.70
const RecoveryStepRatio = 0.05
const FastRecoverySignalRatio = 0.25
const FastRecoveryStepRatio = 0.10
const ProbeLoadRatio = 0.50

type Policy struct {
	TargetSeconds    float64
	RedSeconds       float64
	P99TargetSeconds float64
	P99RedSeconds    float64
	P99SignalWeight  float64
}

func DefaultPolicy() Policy {
	return Policy{
		TargetSeconds:    TargetSeconds,
		RedSeconds:       RedSeconds,
		P99TargetSeconds: P99TargetSeconds,
		P99RedSeconds:    P99RedSeconds,
		P99SignalWeight:  P99SignalWeight,
	}
}

func (p Policy) Normalize() Policy {
	defaults := DefaultPolicy()
	if p.TargetSeconds <= 0 {
		p.TargetSeconds = defaults.TargetSeconds
	}
	if p.RedSeconds <= 0 {
		p.RedSeconds = defaults.RedSeconds
	}
	if p.P99TargetSeconds <= 0 {
		p.P99TargetSeconds = defaults.P99TargetSeconds
	}
	if p.P99RedSeconds <= 0 {
		p.P99RedSeconds = defaults.P99RedSeconds
	}
	if p.P99SignalWeight <= 0 {
		p.P99SignalWeight = defaults.P99SignalWeight
	}
	return p
}

type Observation struct {
	Valid       bool
	Count       uint64
	Avg         float64
	P95         float64
	P99         float64
	SmoothedAvg float64
	SmoothedP95 float64
	SmoothedP99 float64
}

type Assessment struct {
	High          bool
	TailHigh      bool
	YellowReady   bool
	RedReady      bool
	Healthy       bool
	HighCount     int
	TailHighCount int
	HealthyCount  int
	Signal        float64
	P95Signal     float64
	P99Signal     float64
}

type WindowState struct {
	Source        string
	Cumulative    telemetry.HistogramSample
	SmoothedAvg   float64
	SmoothedP95   float64
	SmoothedP99   float64
	HighCount     int
	TailHighCount int
	HealthyCount  int
}

type WindowOptions struct {
	PreserveSmoothing bool
	AllowZeroPrevious bool
}

func ObserveWindow(current telemetry.HistogramSample, previous WindowState, options WindowOptions) Observation {
	observation := Observation{}
	if options.PreserveSmoothing {
		observation.SmoothedAvg = previous.SmoothedAvg
		observation.SmoothedP95 = previous.SmoothedP95
		observation.SmoothedP99 = previous.SmoothedP99
	}
	if previous.Source != "metrics" && !(options.AllowZeroPrevious && previous.Cumulative.Count == 0) {
		return observation
	}
	delta, ok := telemetry.HistogramDelta(current, previous.Cumulative)
	if !ok && options.AllowZeroPrevious && previous.Cumulative.Count == 0 && current.Count > 0 {
		delta = current
		ok = true
	}
	if !ok {
		return observation
	}
	avg, avgOK := telemetry.HistogramAverage(delta)
	p95, p95OK := telemetry.HistogramQuantileUpperBound(delta, 0.95)
	p99, p99OK := telemetry.HistogramQuantileUpperBound(delta, 0.99)
	if !avgOK {
		return observation
	}
	if !p95OK {
		p95 = avg
	}
	if !p99OK {
		p99 = p95
	}
	observation.Valid = true
	observation.Count = delta.Count
	observation.Avg = avg
	observation.P95 = p95
	observation.P99 = p99
	if previous.SmoothedAvg > 0 {
		observation.SmoothedAvg = previous.SmoothedAvg*SmoothRatio + avg*(1-SmoothRatio)
	} else {
		observation.SmoothedAvg = avg
	}
	if previous.SmoothedP95 > 0 {
		observation.SmoothedP95 = previous.SmoothedP95*SmoothRatio + p95*(1-SmoothRatio)
	} else {
		observation.SmoothedP95 = p95
	}
	if previous.SmoothedP99 > 0 {
		observation.SmoothedP99 = previous.SmoothedP99*SmoothRatio + p99*(1-SmoothRatio)
	} else {
		observation.SmoothedP99 = p99
	}
	return observation
}

type AssessInput struct {
	Previous           WindowState
	Policy             Policy
	Running            int
	Waiting            int
	KVCacheUsage       float64
	Preemptions        uint64
	RecoveryLoadLimit  int
	RequireLoadSignal  bool
	RepresentativeLoad bool
	DemandPressure     bool
}

func Assess(input AssessInput, observation Observation) Assessment {
	policy := input.Policy.Normalize()
	assessment := Assessment{
		HighCount:     input.Previous.HighCount,
		TailHighCount: input.Previous.TailHighCount,
		HealthyCount:  input.Previous.HealthyCount,
		P95Signal:     observation.SmoothedP95,
		P99Signal:     observation.SmoothedP99,
	}
	pressureFree := input.Waiting == 0 && input.Preemptions == 0 && (input.KVCacheUsage <= 0 || input.KVCacheUsage < HealthyMaxKVCacheUsage)
	p99Reliable := observation.Valid && observation.Count >= P99MinWindowCount
	p99Healthy := observation.SmoothedP99 <= 0 || observation.SmoothedP99 <= policy.P99TargetSeconds || (!p99Reliable && input.Previous.TailHighCount == 0)
	smoothedHealthy := pressureFree && observation.SmoothedP95 > 0 && observation.SmoothedP95 <= policy.TargetSeconds && observation.SmoothedAvg <= policy.TargetSeconds && p99Healthy
	highSignalQualified := !input.RequireLoadSignal || input.RepresentativeLoad || input.DemandPressure || input.Waiting > 0 || input.Preemptions > 0
	if input.Running < effectiveMinRunning(input.RecoveryLoadLimit) {
		if observation.Valid && observation.Count > 0 {
			fillSignals(&assessment, observation)
			if highSignalQualified && applyHighSignal(&assessment, p99Reliable, policy) {
				return assessment
			}
		}
		if observation.Valid && observation.Count > 0 && smoothedHealthy {
			assessment.Healthy = true
			assessment.HealthyCount++
			decayUnhealthyCounts(&assessment)
			return assessment
		}
		decayUnhealthyCounts(&assessment)
		assessment.HealthyCount = 0
		return assessment
	}
	if !observation.Valid || observation.Count < MinWindowCount {
		if observation.Valid && observation.Count > 0 {
			fillSignals(&assessment, observation)
			if highSignalQualified && applyHighSignal(&assessment, p99Reliable, policy) {
				return assessment
			}
		}
		if smoothedHealthy {
			assessment.Healthy = true
			if observation.Valid && observation.Count > 0 {
				assessment.HealthyCount++
			}
			decayUnhealthyCounts(&assessment)
			return assessment
		}
		decayUnhealthyCounts(&assessment)
		return assessment
	}
	fillSignals(&assessment, observation)
	if highSignalQualified && applyHighSignal(&assessment, p99Reliable, policy) {
		return assessment
	}
	assessment.Healthy = smoothedHealthy
	if assessment.Healthy {
		assessment.HealthyCount++
		decayUnhealthyCounts(&assessment)
		return assessment
	}
	assessment.HealthyCount = 0
	decayUnhealthyCounts(&assessment)
	assessment.Signal = math.Max(assessment.P95Signal, assessment.P99Signal*policy.P99SignalWeight)
	return assessment
}

func fillSignals(assessment *Assessment, observation Observation) {
	if assessment.P95Signal <= 0 {
		assessment.P95Signal = observation.P95
	}
	if assessment.P99Signal <= 0 {
		assessment.P99Signal = observation.P99
	}
}

func applyHighSignal(assessment *Assessment, p99Reliable bool, policy Policy) bool {
	assessment.High = assessment.P95Signal > policy.TargetSeconds
	assessment.TailHigh = p99Reliable && assessment.P99Signal > policy.P99TargetSeconds
	if !assessment.High && !assessment.TailHigh {
		return false
	}
	if assessment.High {
		assessment.HighCount++
	} else if assessment.HighCount > 0 {
		assessment.HighCount--
	}
	if assessment.TailHigh {
		assessment.TailHighCount++
	} else if assessment.TailHighCount > 0 {
		assessment.TailHighCount--
	}
	p95Ready := assessment.HighCount >= HighConsecutive
	tailReady := assessment.TailHighCount >= P99HighConsecutive
	assessment.YellowReady = p95Ready || tailReady
	assessment.RedReady = (p95Ready && assessment.P95Signal >= policy.RedSeconds) || (tailReady && assessment.P99Signal >= policy.P99RedSeconds)
	assessment.Signal = assessment.P95Signal
	tailSignal := assessment.P99Signal * policy.P99SignalWeight
	if assessment.RedReady && tailReady && assessment.P99Signal >= policy.P99RedSeconds {
		tailSignal = assessment.P99Signal
	}
	if tailSignal > assessment.Signal {
		assessment.Signal = tailSignal
	}
	assessment.HealthyCount = 0
	return true
}

func decayUnhealthyCounts(assessment *Assessment) {
	if assessment.HighCount > 0 {
		assessment.HighCount--
	}
	if assessment.TailHighCount > 0 {
		assessment.TailHighCount--
	}
}

func effectiveMinRunning(limit int) int {
	if limit <= 0 || limit >= MinRunning {
		return MinRunning
	}
	if limit < MinLimit {
		return MinLimit
	}
	return limit
}
