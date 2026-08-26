package admission

import (
	"time"
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
	ReasonTPSReference          Reason = "tps_reference"
	ReasonRuntimeIdentityDrift  Reason = "runtime_identity_drift"
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

type ProjectedState struct {
	UnobservedSequences      int64
	SequenceLiabilities      int64
	QoSBudgetLeases          int64
	LiveReservations         int64
	ResidualDebts            int64
	RawRunning               int64
	RawWaiting               int64
	PreviousRawRunning       int64
	GenerationDelta          uint64
	PreemptionDelta          uint64
	ObservationInterval      time.Duration
	ObservationIntervalValid bool
	TPS                      TPSSnapshot
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

func (r TPSDecisionResult) String() string {
	switch r {
	case TPSDecisionResultDisabled:
		return "disabled"
	case TPSDecisionResultAdmit:
		return "admit"
	case TPSDecisionResultProtect:
		return "protect"
	case TPSDecisionResultInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

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
	TPSDecisionSubreasonQoSBudgetMultiSequence
	TPSDecisionSubreasonQoSBudgetUnobserved
	TPSDecisionSubreasonQoSBudgetActiveLease
	TPSDecisionSubreasonQoSBudgetWaveLimit
	TPSDecisionSubreasonQoSBudgetNoSurplus
	TPSDecisionSubreasonQoSBudgetCurrentRate
	TPSDecisionSubreasonQoSBudgetIneligible
)

func (r TPSDecisionSubreason) String() string {
	switch r {
	case TPSDecisionSubreasonDisabled:
		return "disabled"
	case TPSDecisionSubreasonInvalidState:
		return "invalid_state"
	case TPSDecisionSubreasonWaiting:
		return "waiting"
	case TPSDecisionSubreasonPreemption:
		return "preemption"
	case TPSDecisionSubreasonWarming:
		return "warming"
	case TPSDecisionSubreasonIdle:
		return "idle"
	case TPSDecisionSubreasonBaseRate:
		return "base_rate"
	case TPSDecisionSubreasonCurrentRate:
		return "current_rate"
	case TPSDecisionSubreasonQoSBudgetGranted:
		return "qos_budget_granted"
	case TPSDecisionSubreasonQoSBudgetMultiSequence:
		return "qos_budget_multi_sequence"
	case TPSDecisionSubreasonQoSBudgetUnobserved:
		return "qos_budget_unobserved"
	case TPSDecisionSubreasonQoSBudgetActiveLease:
		return "qos_budget_active_lease"
	case TPSDecisionSubreasonQoSBudgetWaveLimit:
		return "qos_budget_wave_limit"
	case TPSDecisionSubreasonQoSBudgetNoSurplus:
		return "qos_budget_no_surplus"
	case TPSDecisionSubreasonQoSBudgetCurrentRate:
		return "qos_budget_current_rate"
	case TPSDecisionSubreasonQoSBudgetIneligible:
		return "qos_budget_ineligible"
	default:
		return "unknown"
	}
}

type TPSPolicyConfig struct {
	Reference float64
}

type ControllerConfig struct {
	RuntimeIdentity string
	TPS             TPSPolicyConfig
	Now             func() time.Time
}

type DecisionRecord struct {
	Action                Action
	Reason                Reason
	Scope                 ProtectionScope
	Demand                TPSRequestDemand
	State                 ProjectedState
	TPSSequenceLimit      int64
	TPSCurrentSequences   int64
	TPSPostAdmitSequences int64
	TPSQoSBudgeted        bool
	TPSDecisionResult     TPSDecisionResult
	TPSDecisionSubreason  TPSDecisionSubreason
	ObservationSequence   uint64
	ControllerSequence    uint64
	RuntimeEpoch          uint64
	PolicyRevision        uint64
	ReservationID         uint64
}

func (d DecisionRecord) Admitted() bool {
	return d.Action == ActionAdmit
}

type BackendObservation struct {
	RuntimeIdentity       string
	ObservedAt            time.Time
	MaximumAge            time.Duration
	Running               int64
	Waiting               int64
	GenerationTokensTotal uint64
	PreemptionsTotal      uint64
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
	Accepted             bool
	RuntimeReset         bool
	RuntimeIdentityDrift bool
	Reason               Reason
	ObservationSequence  uint64
	RuntimeEpoch         uint64
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
