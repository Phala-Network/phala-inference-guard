package predictive

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
	ManifestID               string
	InputTokens              int64
	KV                       KVIncrement
	FutureKV                 KVIncrement
	UncachedPrefillUpper     int64
	DecodeHorizonUpper       int64
	DecodeSequencesUpper     int
	ActiveContextTokensUpper int64
	FutureContextTokensUpper int64
	Confidence               float64
}

type Reason string

const (
	ReasonFit                     Reason = "fit"
	ReasonKVOverBudget            Reason = "kv_over_budget"
	ReasonRequestSizeUnknown      Reason = "request_size_unknown"
	ReasonRequestSizeAtPressure   Reason = "request_size_at_pressure"
	ReasonPredictorProfileUnknown Reason = "predictor_profile_unknown"
	ReasonMetricsStale            Reason = "metrics_stale"
	ReasonPreemptionCooldown      Reason = "preemption_cooldown"
	ReasonDuplicateRequest        Reason = "duplicate_request"
)
