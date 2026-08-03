package kvadmission

import "time"

type BackendKind string

const (
	BackendUnknown BackendKind = "unknown"
	BackendVLLM    BackendKind = "vllm"
	BackendSGLang  BackendKind = "sglang"
)

func ParseBackendKind(value string) BackendKind {
	switch BackendKind(value) {
	case BackendVLLM:
		return BackendVLLM
	case BackendSGLang:
		return BackendSGLang
	default:
		return BackendUnknown
	}
}

type Cost struct {
	Supported                   bool
	UnsupportedReason           string
	BodyBytes                   int
	TextBytes                   int
	ToolSchemaBytes             int
	MessageCount                int
	ToolCount                   int
	ModalityCount               int
	MaxOutputTokens             int
	HasMaxOutputTokens          bool
	EstimatedInputLow           int64
	EstimatedInputHigh          int64
	ApproximateInputTokens      int64
	ApproximateInputTokensKnown bool
	BoundedDecodeTokens         int64
}

// ApproximateInputTokenHint returns a model-neutral lexical-size hint. The
// value is optional evidence for a later combined forecast; it is neither an
// exact tokenizer result nor an admission decision.
func (c Cost) ApproximateInputTokenHint() (int64, bool) {
	return c.ApproximateInputTokens, c.ApproximateInputTokensKnown && c.ApproximateInputTokens > 0
}

func (c Cost) ProjectedHigh() int64 {
	return c.EstimatedInputHigh + c.BoundedDecodeTokens
}

type EstimatorConfig struct {
	MinBytesPerToken             int
	MaxBytesPerToken             int
	ToolMinBytesPerToken         int
	ToolMaxBytesPerToken         int
	TemplateTokensPerMessageLow  int
	TemplateTokensPerMessageHigh int
	ModalityTokensLow            int
	ModalityTokensHigh           int
	BlindOutputTokens            int
}

func DefaultEstimatorConfig() EstimatorConfig {
	return EstimatorConfig{
		MinBytesPerToken:             2,
		MaxBytesPerToken:             6,
		ToolMinBytesPerToken:         2,
		ToolMaxBytesPerToken:         6,
		TemplateTokensPerMessageLow:  3,
		TemplateTokensPerMessageHigh: 8,
		ModalityTokensLow:            256,
		ModalityTokensHigh:           4096,
		BlindOutputTokens:            256,
	}
}

type Budget struct {
	TargetRatio    float64
	HardRatio      float64
	EmergencyRatio float64
}

type Policy struct {
	VLLM               Budget
	SGLang             Budget
	MaxMetricsAge      time.Duration
	PreemptionCooldown time.Duration
	DecodeDriftTokens  int64
	ReservationTTL     time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		VLLM: Budget{
			TargetRatio:    0.84,
			HardRatio:      0.88,
			EmergencyRatio: 0.90,
		},
		SGLang: Budget{
			TargetRatio:    0.80,
			HardRatio:      0.84,
			EmergencyRatio: 0.85,
		},
		MaxMetricsAge:      3 * time.Second,
		PreemptionCooldown: 10 * time.Second,
		DecodeDriftTokens:  8192,
		ReservationTTL:     30 * time.Minute,
	}
}

func (p Policy) BudgetFor(kind BackendKind) (Budget, bool) {
	switch kind {
	case BackendVLLM:
		return p.VLLM, true
	case BackendSGLang:
		return p.SGLang, true
	default:
		return Budget{}, false
	}
}

type BackendSnapshot struct {
	Name                 string
	Kind                 BackendKind
	CapacityTokens       int64
	UsedTokens           int64
	AvailableTokens      int64
	EvictableTokens      int64
	Usage                float64
	Updated              time.Time
	GenerationTokens     uint64
	GenerationTPS        float64
	Waiting              int
	PreemptionDelta      uint64
	PreemptionDeltaValid bool
	PreemptionCooldown   bool
	Failed               bool
	TokenMetricsValid    bool
}

type Reason string

const (
	ReasonFit                Reason = "fit"
	ReasonOverBudget         Reason = "over_budget"
	ReasonEmergencyRed       Reason = "emergency_red"
	ReasonBackendWaiting     Reason = "backend_waiting"
	ReasonPreemptionCooldown Reason = "preemption_cooldown"
	ReasonStaleMetrics       Reason = "stale_metrics"
	ReasonCapacityUnknown    Reason = "capacity_unknown"
	ReasonUnsupportedRequest Reason = "unsupported_request"
)

type Decision struct {
	Reason                   Reason
	Backend                  string
	BackendKind              BackendKind
	EstimatedInputLow        int64
	EstimatedInputHigh       int64
	BoundedDecodeTokens      int64
	ObservedTokens           int64
	UnabsorbedReservedTokens int64
	DecodeDriftTokens        int64
	ProjectedHighTokens      int64
	TargetTokens             int64
	HardBudgetTokens         int64
	EmergencyTokens          int64
	CapacityTokens           int64
	ProjectedRatio           float64
	SampleAge                time.Duration
	SpillHeadroom            bool
}
