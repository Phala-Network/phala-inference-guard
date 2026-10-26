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
	ReasonRunningLimit          Reason = "running_limit"
	ReasonWindowConcurrency     Reason = "window_concurrency"
	ReasonRuntimeIdentityDrift  Reason = "runtime_identity_drift"
	ReasonResourceExhausted     Reason = "resource_exhausted"
	ReasonPriorityQueueFull     Reason = "priority_queue_full"
	ReasonPriorityQueueTimeout  Reason = "priority_queue_timeout"
	ReasonPriorityQueueCanceled Reason = "priority_queue_canceled"
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
	UnobservedSequences int64
	// ColdSheddablePending is the number of admitted default or flex requests
	// with a cold or unknown uncached Prompt still pending first byte.
	ColdSheddablePending     int64
	SequenceLiabilities      int64
	LiveReservations         int64
	ResidualDebts            int64
	RawRunning               int64
	RawWaiting               int64
	PreviousRawRunning       int64
	PreviousRawWaiting       int64
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
	Latest                   TPSIntervalSnapshot
	Denominator              TPSDenominatorEvidence
}

type TPSIntervalSnapshot struct {
	Qualified       bool
	Tokens          uint64
	DurationSeconds float64
	SequenceSeconds float64
	AggregateTPS    float64
	MeanActiveTPS   float64
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
	TPSDecisionSubreasonNoCurrentEvidence
	TPSDecisionSubreasonHealthyWindow
	TPSDecisionSubreasonRecoveredCurrent
	TPSDecisionSubreasonBelowReference
	TPSDecisionSubreasonBackendScheduled
	TPSDecisionSubreasonWarmPrompt
	TPSDecisionSubreasonColdPrompt
	TPSDecisionSubreasonColdPromptPending
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
	case TPSDecisionSubreasonNoCurrentEvidence:
		return "no_current_evidence"
	case TPSDecisionSubreasonHealthyWindow:
		return "healthy_window"
	case TPSDecisionSubreasonRecoveredCurrent:
		return "recovered_current"
	case TPSDecisionSubreasonBelowReference:
		return "below_reference"
	case TPSDecisionSubreasonBackendScheduled:
		return "backend_scheduled"
	case TPSDecisionSubreasonWarmPrompt:
		return "warm_prompt"
	case TPSDecisionSubreasonColdPrompt:
		return "cold_prompt"
	case TPSDecisionSubreasonColdPromptPending:
		return "cold_prompt_pending"
	default:
		return "unknown"
	}
}

type TPSPolicyConfig struct {
	Reference float64
}

const (
	DefaultWindowConcurrency int64 = 32
	DefaultWaitingAllowance  int64 = 3
)

type RunningLimitSource string

const (
	RunningLimitSourceUnknown          RunningLimitSource = "unknown"
	RunningLimitSourceEnvironment      RunningLimitSource = "environment"
	RunningLimitSourceSGLangServerInfo RunningLimitSource = "sglang_server_info"
	RunningLimitSourceAdmin            RunningLimitSource = "admin"
)

func (s RunningLimitSource) valid() bool {
	switch s {
	case RunningLimitSourceUnknown, RunningLimitSourceEnvironment,
		RunningLimitSourceSGLangServerInfo, RunningLimitSourceAdmin:
		return true
	default:
		return false
	}
}

type ControllerConfig struct {
	RuntimeIdentity               string
	TPS                           TPSPolicyConfig
	WindowConcurrency             int64
	RunningLimit                  int64
	RunningLimitSource            RunningLimitSource
	PendingFirstByteLeaseDuration time.Duration
	// BackendTierScheduling states that the backend orders its waiting queue
	// by the tier PIG writes into each request (SGLang with priority
	// scheduling), so priority Prefill runs ahead of every queued default and
	// flex request and the TPS reference stops protecting the priority tier.
	// It follows the backend kind discovered at startup; it is not
	// configuration.
	BackendTierScheduling bool
	// WarmPromptTokens, when positive, makes TPS health Prompt-aware for the
	// default and flex tiers while the window is below the reference. Set it
	// to the backend's chunked Prefill size. A request whose estimated
	// uncached Prompt fits one chunk passes on a recovered latest sample: the
	// backend interleaves Prefill with Decode one chunk at a time, so it costs
	// at most the one-step stall every chunk costs, and such requests are
	// conversations continuing on this node, which turned away come back
	// later as a cold Prefill. A larger or unknown Prompt waits for the
	// window mean and is admitted one at a time: a recovered sample is the
	// moment a chunk ended and says nothing about absorbing several more, and
	// cold Prompts admitted together all Prefill at once and each waits for
	// all of them. Zero keeps the plain reference gate.
	WarmPromptTokens int64
	Now              func() time.Time
}

type DecisionRecord struct {
	Action                   Action
	Reason                   Reason
	Scope                    ProtectionScope
	Demand                   TPSRequestDemand
	State                    ProjectedState
	ProjectedRunning         int64
	ProjectedWindowSequences int64
	RunningLimit             int64
	RunningLimitSource       RunningLimitSource
	WindowConcurrency        int64
	TPSDecisionResult        TPSDecisionResult
	TPSDecisionSubreason     TPSDecisionSubreason
	ObservationSequence      uint64
	ControllerSequence       uint64
	RuntimeEpoch             uint64
	PolicyRevision           uint64
	ReservationID            uint64
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
	// PrefillTokensTotal counts uncached Prompt tokens the backend computed.
	// It is optional; without it the Prefill budget stays unmeasured and open.
	PrefillTokensTotal uint64
	PrefillTokensValid bool
}

type AdmissionResult struct {
	Decision DecisionRecord
	Handle   ReservationHandle
}

type CapacitySnapshot struct {
	IntakeOpen                 bool
	HasObservation             bool
	Available                  bool
	MinimumDecision            DecisionRecord
	State                      ProjectedState
	Observation                BackendObservation
	ObservationSequence        uint64
	ControllerSequence         uint64
	RuntimeEpoch               uint64
	Policy                     PolicySnapshot
	WindowConcurrencyHistogram WindowConcurrencyHistogramSnapshot
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
