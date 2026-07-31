package predictive

import "time"

type KVIncrement struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type Projection struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type VirtualState struct {
	PhysicalKVUpper       int64
	ActiveKVUpper         int64
	DecodeSequences       int
	ActiveContextTokens   int64
	UncachedPrefillTokens int64
}

type VirtualStateInterval struct {
	Lower VirtualState
	Upper VirtualState
}

type RequestCost struct {
	ManifestID               string
	InputTokens              int64
	KV                       KVIncrement
	UncachedPrefillUpper     int64
	DecodeHorizonUpper       int64
	DecodeSequencesUpper     int
	ActiveContextTokensUpper int64
	Confidence               float64
}

type SchedulerEstimate struct {
	ExistingUserTPSLower         float64
	ExistingUserTPSNotApplicable bool
	AllUserTPSLower              float64
	TTFTUpper                    time.Duration
	TPOTUpper                    time.Duration
	WorkspaceRiskUpper           float64
	PreemptionRiskUpper          float64
}

type Constraints struct {
	PhysicalKVHard       int64
	ActiveKVHard         int64
	UserTPSTarget        float64
	TTFTSLO              time.Duration
	TPOTSLO              time.Duration
	WorkspaceRiskBudget  float64
	PreemptionRiskBudget float64
	MinimumConfidence    float64
}

type Reason string

const (
	ReasonFit                     Reason = "fit"
	ReasonKVOverBudget            Reason = "kv_over_budget"
	ReasonActiveKVOverBudget      Reason = "active_kv_over_budget"
	ReasonExistingTPSAtRisk       Reason = "existing_tps_at_risk"
	ReasonNewTPSAtRisk            Reason = "new_tps_at_risk"
	ReasonTTFTAtRisk              Reason = "ttft_at_risk"
	ReasonTPOTAtRisk              Reason = "tpot_at_risk"
	ReasonWorkspaceAtRisk         Reason = "workspace_at_risk"
	ReasonPreemptionAtRisk        Reason = "preemption_at_risk"
	ReasonTokenizerProfileUnknown Reason = "tokenizer_profile_unknown"
	ReasonPredictorProfileUnknown Reason = "predictor_profile_unknown"
	ReasonDuplicateRequest        Reason = "duplicate_request"
)

type EvaluationInput struct {
	Projection  Projection
	Scheduler   SchedulerEstimate
	Constraints Constraints
	Confidence  float64
}

type Decision struct {
	Reason     Reason
	Projection Projection
	Scheduler  SchedulerEstimate
	Confidence float64
}
