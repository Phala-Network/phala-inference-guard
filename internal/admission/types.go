package admission

import (
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type Action string

const (
	ActionAdmit   Action = "admit"
	ActionProtect Action = "protect"
)

type Reason string

const (
	ReasonOpen                  Reason = "open"
	ReasonControllerUnavailable Reason = "controller_unavailable"
	ReasonObservationMissing    Reason = "observation_missing"
	ReasonObservationInvalid    Reason = "observation_invalid"
	ReasonObservationStale      Reason = "observation_stale"
	ReasonInvalidRequest        Reason = "invalid_request"
	ReasonInputLimit            Reason = "input_limit"
	ReasonKVCapacity            Reason = "kv_capacity"
	ReasonPrefillContention     Reason = "prefill_contention"
	ReasonPrefillBudget         Reason = "prefill_budget"
	ReasonPrefillExclusive      Reason = "prefill_exclusive"
	ReasonPrefillQuiescent      Reason = "prefill_quiescent"
	ReasonTPSReference          Reason = "tps_reference"
	ReasonCapabilityDrift       Reason = "capability_drift"
	ReasonResourceExhausted     Reason = "resource_exhausted"
	ReasonCounterOverflow       Reason = "counter_overflow"
	ReasonClosed                Reason = "closed"
)

type ProtectionScope string

const (
	ProtectionNone         ProtectionScope = ""
	ProtectionRequest      ProtectionScope = "request"
	ProtectionLoad         ProtectionScope = "load"
	ProtectionAvailability ProtectionScope = "availability"
)

type PrefillClass string

const (
	PrefillRegular   PrefillClass = "regular"
	PrefillWeighted  PrefillClass = "weighted"
	PrefillExclusive PrefillClass = "exclusive"
	PrefillQuiescent PrefillClass = "quiescent"
)

type ProjectedState struct {
	ObservedKVTokens          int64
	ReservationKVTokens       int64
	EffectiveKVTokens         int64
	PendingPrefillInputTokens int64
	PendingPrefillTokens      int64
	PendingCacheCreditTokens  int64
	PendingPrefillSequences   int64
	PendingExclusiveSequences int64
	PendingQuiescentSequences int64
	LocalActiveDecode         int64
	UnobservedSequences       int64
	SequenceLiabilities       int64
	QoSBudgetLeases           int64
	LiveReservations          int64
	ResidualDebts             int64
	RawRunning                int64
	RawWaiting                int64
	PreviousRawRunning        int64
	GenerationDelta           uint64
	PreemptionDelta           uint64
	ObservationInterval       time.Duration
	ObservationIntervalValid  bool
	CacheObservationValid     bool
	CacheHitFraction          float64
	CacheCreditFraction       float64
	CacheEvidenceTokens       uint64
	CacheCreditBudgetTokens   int64
	CacheCreditSpentTokens    int64
	TPS                       TPSSnapshot
}

type TPSSnapshot struct {
	Enabled                  bool
	Ready                    bool
	Reference                float64
	QualifiedSamples         uint64
	QualifiedTokens          float64
	QualifiedActiveSeconds   float64
	QualifiedSequenceSamples uint64
	QualifiedSequenceTokens  float64
	QualifiedSequenceSeconds float64
	AggregateTPS             float64
	MeanActiveTPS            float64
	Denominator              TPSDenominatorEvidence
}

type TPSDenominatorEvidence struct {
	EndpointSelections          uint64
	LocalForwardedSelections    uint64
	LocalResponseSelections     uint64
	FallbackLiabilitySelections uint64
	TieSelections               uint64
	NoneSelections              uint64
	EndpointSequenceSeconds     float64
	LocalForwardedSeconds       float64
	LocalResponseSeconds        float64
	FallbackLiabilitySeconds    float64
	SelectedSequenceSeconds     float64
}

type TPSDecisionResult uint8

const (
	TPSDecisionResultUnknown TPSDecisionResult = iota
	TPSDecisionResultDisabled
	TPSDecisionResultAdmit
	TPSDecisionResultProtect
	TPSDecisionResultInvalid
)

type TPSDecisionSubreason uint8

const (
	TPSDecisionSubreasonUnknown TPSDecisionSubreason = iota
	TPSDecisionSubreasonDisabled
	TPSDecisionSubreasonInvalidState
	TPSDecisionSubreasonWaiting
	TPSDecisionSubreasonPreemption
	TPSDecisionSubreasonWarming
	TPSDecisionSubreasonIdle
	TPSDecisionSubreasonBaseRate
	TPSDecisionSubreasonCurrentRate
	TPSDecisionSubreasonQoSBudgetGranted
	TPSDecisionSubreasonQoSBudgetOutputUnknown
	TPSDecisionSubreasonQoSBudgetMultiSequence
	TPSDecisionSubreasonQoSBudgetUnobserved
	TPSDecisionSubreasonQoSBudgetActiveLease
	TPSDecisionSubreasonQoSBudgetWaveLimit
	TPSDecisionSubreasonQoSBudgetNoSurplus
	TPSDecisionSubreasonQoSBudgetInvalidRate
	TPSDecisionSubreasonQoSBudgetLifetime
	TPSDecisionSubreasonQoSBudgetIneligible
)

type TPSPolicyConfig struct {
	Reference float64
}

type ControllerConfig struct {
	Capability  Capability
	WorkProfile predictive.BackendExecutionProfile
	TPS         TPSPolicyConfig
	Now         func() time.Time
}

type DecisionRecord struct {
	Action                     Action
	Reason                     Reason
	Scope                      ProtectionScope
	PrefillClass               PrefillClass
	Estimate                   predictive.RequestEstimate
	Work                       predictive.RequestWork
	State                      ProjectedState
	PostAdmitKVTokens          int64
	HardKVLimitTokens          int64
	RemainingKVTokens          int64
	PendingPrefillTokensBefore int64
	PendingPrefillTokensAfter  int64
	TPSSequenceLimit           int64
	TPSCurrentSequences        int64
	TPSPostAdmitSequences      int64
	TPSQoSBudgeted             bool
	TPSDecisionResult          TPSDecisionResult
	TPSDecisionSubreason       TPSDecisionSubreason
	ObservationSequence        uint64
	ControllerSequence         uint64
	RuntimeEpoch               uint64
	PolicyRevision             uint64
	ReservationID              uint64
}

func (d DecisionRecord) Admitted() bool {
	return d.Action == ActionAdmit
}

type BackendObservation struct {
	CapabilityFingerprint string
	MaxModelLenTokens     int64
	KVCapacityTokens      int64
	KVBlockSize           int64
	ObservedAt            time.Time
	MaximumAge            time.Duration
	UsedKVTokens          int64
	Running               int64
	Waiting               int64
	GenerationTokensTotal uint64
	PreemptionsTotal      uint64
	CacheQueryTokensTotal uint64
	CacheHitTokensTotal   uint64
	CacheCountersValid    bool
	RuntimeStartTime      float64
}

type AdmissionResult struct {
	Decision DecisionRecord
	Handle   ReservationHandle
}

type CapacitySnapshot struct {
	IntakeOpen          bool
	HasObservation      bool
	Available           bool
	MinimumDecision     DecisionRecord
	State               ProjectedState
	Observation         BackendObservation
	ObservationSequence uint64
	ControllerSequence  uint64
	RuntimeEpoch        uint64
	Policy              TPSPolicySnapshot
}

type PublicationResult struct {
	Accepted            bool
	RuntimeReset        bool
	CapabilityDrift     bool
	Reason              Reason
	ObservationSequence uint64
	RuntimeEpoch        uint64
}

type TerminalCause string

const (
	TerminalSuccess    TerminalCause = "success"
	TerminalError      TerminalCause = "error"
	TerminalCancel     TerminalCause = "cancel"
	TerminalDisconnect TerminalCause = "disconnect"
	TerminalTimeout    TerminalCause = "timeout"
	TerminalShutdown   TerminalCause = "shutdown"
)

func (c TerminalCause) valid() bool {
	switch c {
	case TerminalSuccess, TerminalError, TerminalCancel, TerminalDisconnect, TerminalTimeout, TerminalShutdown:
		return true
	default:
		return false
	}
}
