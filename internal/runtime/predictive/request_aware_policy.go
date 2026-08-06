package predictive

import (
	"fmt"
	"math"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type RequestAwareAction string

const (
	RequestAwareAdmit       RequestAwareAction = "admit"
	RequestAwareSizeProtect RequestAwareAction = "size_protect"
	RequestAwareHardProtect RequestAwareAction = "hard_protect"
)

type RequestAwareReason string

const (
	RequestAwareReasonOpen               RequestAwareReason = "open"
	RequestAwareReasonWithinBudget       RequestAwareReason = "within_budget"
	RequestAwareReasonRequestSize        RequestAwareReason = "request_size"
	RequestAwareReasonStale              RequestAwareReason = "stale"
	RequestAwareReasonPreemption         RequestAwareReason = "preemption"
	RequestAwareReasonKV                 RequestAwareReason = "kv"
	RequestAwareReasonPrefillBudget      RequestAwareReason = "prefill_budget"
	RequestAwareReasonPrefillConcurrency RequestAwareReason = "prefill_concurrency"
	RequestAwareReasonPrefillExclusive   RequestAwareReason = "prefill_exclusive"
	RequestAwareReasonPrefillBusy        RequestAwareReason = "prefill_busy"
	RequestAwareReasonDuplicate          RequestAwareReason = "duplicate"
	RequestAwareReasonUnavailable        RequestAwareReason = "unavailable"
	RequestAwareReasonInvalid            RequestAwareReason = "invalid"
)

type RequestAwarePressureSource string

const (
	RequestAwarePressureNone    RequestAwarePressureSource = "none"
	RequestAwarePressureKV      RequestAwarePressureSource = "kv"
	RequestAwarePressureWaiting RequestAwarePressureSource = "waiting"
	RequestAwarePressureTPS     RequestAwarePressureSource = "tps"
	RequestAwarePressurePrefill RequestAwarePressureSource = "prefill"
)

type RequestAwarePrefillClass string

const (
	RequestAwarePrefillRegular   RequestAwarePrefillClass = "regular"
	RequestAwarePrefillWeighted  RequestAwarePrefillClass = "weighted"
	RequestAwarePrefillExclusive RequestAwarePrefillClass = "exclusive"
	RequestAwarePrefillQuiescent RequestAwarePrefillClass = "quiescent"

	DefaultRequestAwarePrefillRegularTokens         = domain.DefaultPrefillRegularTokens
	DefaultRequestAwarePrefillExclusiveTokens       = domain.DefaultPrefillExclusiveTokens
	DefaultRequestAwarePrefillQuiescentTokens       = domain.DefaultPrefillQuiescentTokens
	DefaultRequestAwarePrefillAggregateBudgetTokens = domain.DefaultPrefillAggregateBudgetTokens
)

type RequestAwareConfig struct {
	SoftKVRatio                  float64
	HardKVRatio                  float64
	TPSTarget                    float64
	TPSFloor                     float64
	BlockSize                    int64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillAggregateBudgetTokens int64
}

func (c RequestAwareConfig) Validate() error {
	if !requestAwareFinite(c.SoftKVRatio) || c.SoftKVRatio <= 0 ||
		!requestAwareFinite(c.HardKVRatio) || c.HardKVRatio <= c.SoftKVRatio || c.HardKVRatio >= 1 ||
		!requestAwareFinite(c.TPSTarget) || c.TPSTarget <= 0 ||
		!requestAwareFinite(c.TPSFloor) || c.TPSFloor <= 0 || c.TPSFloor >= c.TPSTarget ||
		c.BlockSize <= 0 ||
		c.PrefillRegularTokens <= 0 || c.PrefillExclusiveTokens <= c.PrefillRegularTokens ||
		c.PrefillQuiescentTokens <= c.PrefillExclusiveTokens ||
		c.PrefillAggregateBudgetTokens < c.PrefillExclusiveTokens ||
		c.PrefillAggregateBudgetTokens > c.PrefillQuiescentTokens {
		return fmt.Errorf("request-aware policy configuration is invalid")
	}
	return nil
}

func (c RequestAwareConfig) withPrefillDefaults() RequestAwareConfig {
	if c.PrefillRegularTokens == 0 {
		c.PrefillRegularTokens = DefaultRequestAwarePrefillRegularTokens
	}
	if c.PrefillExclusiveTokens == 0 {
		c.PrefillExclusiveTokens = DefaultRequestAwarePrefillExclusiveTokens
	}
	if c.PrefillQuiescentTokens == 0 {
		c.PrefillQuiescentTokens = DefaultRequestAwarePrefillQuiescentTokens
	}
	if c.PrefillAggregateBudgetTokens == 0 {
		c.PrefillAggregateBudgetTokens = DefaultRequestAwarePrefillAggregateBudgetTokens
	}
	return c
}

type RequestAwareInput struct {
	MetricsFresh                     bool
	IdentityValid                    bool
	CapacityTokens                   int64
	UsedTokens                       int64
	ReservedTokens                   int64
	RequestReservedTokens            int64
	SelectionInputTokens             int64
	Running                          int
	Waiting                          int
	EffectiveSequences               int
	AggregateTPSProxy                float64
	MeanActiveTPSProxy               float64
	TPSValid                         bool
	PreemptionCooldown               bool
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
}

type RequestAwareDecision struct {
	Action                           RequestAwareAction
	Reason                           RequestAwareReason
	PressureSource                   RequestAwarePressureSource
	Pressure                         float64
	AllowanceTokens                  int64
	EffectiveKV                      int64
	PostAdmitKV                      int64
	RemainingKV                      int64
	HardKVLimit                      int64
	EffectiveSequences               int
	ProjectedMeanActiveTPSProxy      float64
	TPSForecastValid                 bool
	PrefillClass                     RequestAwarePrefillClass
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PostAdmitPendingPrefillTokens    int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
}

type RequestAwarePolicy struct {
	config RequestAwareConfig
}

func NewRequestAwarePolicy(config RequestAwareConfig) (*RequestAwarePolicy, error) {
	config = config.withPrefillDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &RequestAwarePolicy{config: config}, nil
}

func (p *RequestAwarePolicy) Evaluate(input RequestAwareInput) RequestAwareDecision {
	estimatedPrefillTokens := input.EstimatedPrefillTokens
	if estimatedPrefillTokens == 0 {
		estimatedPrefillTokens = input.SelectionInputTokens
	}
	if p == nil || input.CapacityTokens <= 0 || input.CapacityTokens > 1<<53 ||
		input.UsedTokens < 0 || input.UsedTokens > input.CapacityTokens ||
		input.ReservedTokens < 0 || input.RequestReservedTokens <= 0 || input.SelectionInputTokens <= 0 ||
		input.Running < 0 || input.Waiting < 0 || input.EffectiveSequences < input.Running ||
		!requestAwareFinite(input.AggregateTPSProxy) || input.AggregateTPSProxy < 0 ||
		!requestAwareFinite(input.MeanActiveTPSProxy) || input.MeanActiveTPSProxy < 0 ||
		(input.TPSValid && (input.AggregateTPSProxy <= 0 || input.MeanActiveTPSProxy <= 0)) ||
		estimatedPrefillTokens <= 0 || input.PendingPrefillSequences < 0 || input.PendingPrefillTokens < 0 ||
		input.PendingLongPrefillSequences < 0 || input.PendingLongPrefillSequences > input.PendingPrefillSequences ||
		input.PendingQuiescentPrefillSequences < 0 || input.PendingQuiescentPrefillSequences > input.PendingLongPrefillSequences ||
		(input.PendingPrefillSequences == 0 && input.PendingPrefillTokens != 0) ||
		(input.PendingPrefillSequences > 0 && input.PendingPrefillTokens == 0) {
		return RequestAwareDecision{Action: RequestAwareHardProtect, Reason: RequestAwareReasonInvalid}
	}

	softKVLimit := requestAwareBlockRoundDown(
		int64(math.Floor(float64(input.CapacityTokens)*p.config.SoftKVRatio)),
		p.config.BlockSize,
	)
	hardKVLimit := requestAwareBlockRoundDown(
		int64(math.Floor(float64(input.CapacityTokens)*p.config.HardKVRatio)),
		p.config.BlockSize,
	)
	effectiveKV, ok := requestAwareAdd(input.UsedTokens, input.ReservedTokens)
	if !ok {
		return RequestAwareDecision{
			Action:      RequestAwareHardProtect,
			Reason:      RequestAwareReasonInvalid,
			HardKVLimit: hardKVLimit,
		}
	}
	postAdmitKV, ok := requestAwareAdd(effectiveKV, input.RequestReservedTokens)
	if !ok {
		return RequestAwareDecision{
			Action:             RequestAwareHardProtect,
			Reason:             RequestAwareReasonInvalid,
			EffectiveKV:        effectiveKV,
			HardKVLimit:        hardKVLimit,
			EffectiveSequences: input.EffectiveSequences,
		}
	}
	remainingKV := hardKVLimit - effectiveKV
	if remainingKV < 0 {
		remainingKV = 0
	}
	decision := RequestAwareDecision{
		Action:                           RequestAwareHardProtect,
		PressureSource:                   RequestAwarePressureNone,
		EffectiveKV:                      effectiveKV,
		PostAdmitKV:                      postAdmitKV,
		RemainingKV:                      remainingKV,
		HardKVLimit:                      hardKVLimit,
		EffectiveSequences:               input.EffectiveSequences,
		PrefillClass:                     p.prefillClass(estimatedPrefillTokens),
		EstimatedPrefillTokens:           estimatedPrefillTokens,
		PendingPrefillSequences:          input.PendingPrefillSequences,
		PendingPrefillTokens:             input.PendingPrefillTokens,
		PendingLongPrefillSequences:      input.PendingLongPrefillSequences,
		PendingQuiescentPrefillSequences: input.PendingQuiescentPrefillSequences,
	}
	postAdmitPendingPrefillTokens, pendingTokensValid := requestAwareAdd(input.PendingPrefillTokens, estimatedPrefillTokens)
	if !pendingTokensValid {
		decision.Reason = RequestAwareReasonInvalid
		return decision
	}
	decision.PostAdmitPendingPrefillTokens = postAdmitPendingPrefillTokens

	if !input.MetricsFresh || !input.IdentityValid {
		decision.Reason = RequestAwareReasonStale
		return decision
	}
	if input.PreemptionCooldown {
		decision.Reason = RequestAwareReasonPreemption
		return decision
	}
	selectiveWindowTokens := hardKVLimit - softKVLimit
	if hardKVLimit <= 0 || selectiveWindowTokens <= 0 {
		decision.Reason = RequestAwareReasonInvalid
		return decision
	}
	if effectiveKV > hardKVLimit || postAdmitKV > hardKVLimit {
		decision.Reason = RequestAwareReasonKV
		return decision
	}
	if reason, protect := p.prefillProtectionReason(input, decision.PrefillClass, postAdmitPendingPrefillTokens); protect {
		decision.Action = RequestAwareSizeProtect
		decision.Reason = reason
		decision.PressureSource = RequestAwarePressurePrefill
		decision.Pressure = 1
		decision.AllowanceTokens = 0
		return decision
	}
	tpsUsable := input.TPSValid && input.EffectiveSequences > 0
	kvPressure := requestAwareNormalizedPressure(
		float64(effectiveKV),
		float64(softKVLimit),
		float64(hardKVLimit),
	)
	waitingPressure := float64(input.Waiting) /
		(float64(input.Running) + float64(input.Waiting) + 1)
	tpsPressure := 0.0
	if tpsUsable {
		projectedTPS := input.MeanActiveTPSProxy
		if input.MeanActiveTPSProxy < p.config.TPSTarget || input.Waiting > 0 || input.EffectiveSequences > input.Running {
			if input.EffectiveSequences == int(^uint(0)>>1) {
				decision.Reason = RequestAwareReasonInvalid
				return decision
			}
			projectedTPS = input.AggregateTPSProxy / float64(input.EffectiveSequences+1)
		}
		if !requestAwareFinite(projectedTPS) || projectedTPS <= 0 {
			decision.Reason = RequestAwareReasonInvalid
			return decision
		}
		decision.ProjectedMeanActiveTPSProxy = projectedTPS
		decision.TPSForecastValid = true
		if projectedTPS < p.config.TPSTarget {
			tpsPressure = (p.config.TPSTarget - projectedTPS) /
				(p.config.TPSTarget - p.config.TPSFloor)
		}
	}

	pressure := kvPressure
	pressureSource := RequestAwarePressureKV
	if waitingPressure > pressure {
		pressure = waitingPressure
		pressureSource = RequestAwarePressureWaiting
	}
	if tpsPressure > pressure {
		pressure = tpsPressure
		pressureSource = RequestAwarePressureTPS
	}
	pressure = requestAwareClampUnit(pressure)
	if pressure == 0 {
		decision.Action = RequestAwareAdmit
		decision.Reason = RequestAwareReasonOpen
		decision.AllowanceTokens = remainingKV
		return decision
	}

	allowance := int64(math.Floor(float64(selectiveWindowTokens) * (1 - pressure)))
	if allowance < 0 {
		allowance = 0
	}
	if allowance > remainingKV {
		allowance = remainingKV
	}
	decision.PressureSource = pressureSource
	decision.Pressure = pressure
	decision.AllowanceTokens = allowance
	if input.SelectionInputTokens <= allowance {
		decision.Action = RequestAwareAdmit
		decision.Reason = RequestAwareReasonWithinBudget
		return decision
	}
	decision.Action = RequestAwareSizeProtect
	decision.Reason = RequestAwareReasonRequestSize
	return decision
}

func (p *RequestAwarePolicy) prefillClass(tokens int64) RequestAwarePrefillClass {
	switch {
	case p == nil || tokens <= 0:
		return ""
	case tokens < p.config.PrefillRegularTokens:
		return RequestAwarePrefillRegular
	case tokens < p.config.PrefillExclusiveTokens:
		return RequestAwarePrefillWeighted
	case tokens < p.config.PrefillQuiescentTokens:
		return RequestAwarePrefillExclusive
	default:
		return RequestAwarePrefillQuiescent
	}
}

func (p *RequestAwarePolicy) prefillProtectionReason(
	input RequestAwareInput,
	class RequestAwarePrefillClass,
	postAdmitTokens int64,
) (RequestAwareReason, bool) {
	if input.PendingQuiescentPrefillSequences > 0 {
		return RequestAwareReasonPrefillExclusive, true
	}
	switch class {
	case RequestAwarePrefillRegular:
		return RequestAwareReasonPrefillBudget,
			input.PendingLongPrefillSequences == 0 && postAdmitTokens > p.config.PrefillAggregateBudgetTokens
	case RequestAwarePrefillWeighted:
		return RequestAwareReasonPrefillBudget, postAdmitTokens > p.config.PrefillAggregateBudgetTokens
	case RequestAwarePrefillExclusive:
		return RequestAwareReasonPrefillConcurrency, input.PendingLongPrefillSequences > 0
	case RequestAwarePrefillQuiescent:
		return RequestAwareReasonPrefillBusy, input.Running > 0 || input.Waiting > 0 ||
			input.EffectiveSequences > 0 || input.PendingPrefillSequences > 0
	default:
		return "", false
	}
}

func requestAwareFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func requestAwareAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > (1<<63-1)-right {
		return 0, false
	}
	return left + right, true
}

func requestAwareBlockRoundDown(value, blockSize int64) int64 {
	if value <= 0 || blockSize <= 0 {
		return 0
	}
	return value - value%blockSize
}

func requestAwareNormalizedPressure(value, soft, hard float64) float64 {
	return requestAwareClampUnit((value - soft) / (hard - soft))
}

func requestAwareClampUnit(value float64) float64 {
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value
}
