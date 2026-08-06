package predictive

import "time"

const (
	DefaultPrefillRegularTokens         int64 = 64 * 1024
	DefaultPrefillExclusiveTokens       int64 = 256 * 1024
	DefaultPrefillQuiescentTokens       int64 = 512 * 1024
	DefaultPrefillAggregateBudgetTokens int64 = 256 * 1024
)

type KVIncrement struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type Projection struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type VirtualState struct {
	PhysicalKVUpper         int64
	ActiveKVUpper           int64
	DecodeSequences         int
	PendingPrefillSequences int
	ActiveContextTokens     int64
	UncachedPrefillTokens   int64
}

type VirtualStateInterval struct {
	Lower VirtualState
	Upper VirtualState
}

type RequestCost struct {
	ManifestID                   string
	InputTokens                  int64
	RequestComplexityTokensUpper int64
	AccruedLocalAdmissionLatency time.Duration
	KV                           KVIncrement
	FutureKV                     KVIncrement
	UncachedPrefillUpper         int64
	DecodeHorizonUpper           int64
	DecodeSequencesUpper         int
	ActiveContextTokensUpper     int64
	FutureContextTokensUpper     int64
	Confidence                   float64
}

type SchedulerEstimate struct {
	ExistingUserTPSLower                   float64
	ExistingUserTPSNotApplicable           bool
	NewUserTPSLower                        float64
	AggregateCompletionTPSEstimate         float64
	PreviousAggregateCompletionTPSEstimate float64
	ThroughputFrontierReached              bool
	TTFTUpper                              time.Duration
	TPOTUpper                              time.Duration
	WorkspaceRiskUpper                     float64
	PreemptionRiskUpper                    float64
}

type Constraints struct {
	PhysicalKVHard       int64
	ActiveKVHard         int64
	UserTPSTarget        float64
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
	ReasonTPOTAtRisk              Reason = "tpot_at_risk"
	ReasonThroughputFrontier      Reason = "throughput_frontier_reached"
	ReasonWorkspaceAtRisk         Reason = "workspace_at_risk"
	ReasonPreemptionAtRisk        Reason = "preemption_at_risk"
	ReasonTokenizerProfileUnknown Reason = "tokenizer_profile_unknown"
	ReasonRequestSizeUnknown      Reason = "request_size_unknown"
	ReasonRequestSizeAtPressure   Reason = "request_size_at_pressure"
	ReasonPredictorProfileUnknown Reason = "predictor_profile_unknown"
	ReasonMetricsStale            Reason = "metrics_stale"
	ReasonPreemptionCooldown      Reason = "preemption_cooldown"
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
