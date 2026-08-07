package predictive

// PredictionSource identifies whether an admission result came from the
// deterministic request-aware policy or from a fail-closed availability path.
type PredictionSource string

const (
	PredictionSourceDeterministic PredictionSource = "deterministic"
	PredictionSourceUnavailable   PredictionSource = "unavailable"
)
