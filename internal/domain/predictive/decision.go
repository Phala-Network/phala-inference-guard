package predictive

func Evaluate(input EvaluationInput) Decision {
	decision := Decision{
		Reason:     ReasonFit,
		Projection: input.Projection,
		Scheduler:  input.Scheduler,
		Confidence: input.Confidence,
	}
	cfg := input.Constraints
	switch {
	case cfg.PhysicalKVHard > 0 && input.Projection.PhysicalKVUpper > cfg.PhysicalKVHard:
		decision.Reason = ReasonKVOverBudget
	case cfg.ActiveKVHard > 0 && input.Projection.ActiveKVUpper > cfg.ActiveKVHard:
		decision.Reason = ReasonActiveKVOverBudget
	case cfg.UserTPSTarget > 0 && !input.Scheduler.ExistingUserTPSNotApplicable && input.Scheduler.ExistingUserTPSLower < cfg.UserTPSTarget:
		decision.Reason = ReasonExistingTPSAtRisk
	case cfg.UserTPSTarget > 0 && input.Scheduler.NewUserTPSLower < cfg.UserTPSTarget:
		decision.Reason = ReasonNewTPSAtRisk
	case cfg.TTFTSLO > 0 && input.Scheduler.TTFTUpper > cfg.TTFTSLO:
		decision.Reason = ReasonTTFTAtRisk
	case cfg.TPOTSLO > 0 && input.Scheduler.TPOTUpper > cfg.TPOTSLO:
		decision.Reason = ReasonTPOTAtRisk
	case input.Scheduler.WorkspaceRiskUpper > cfg.WorkspaceRiskBudget:
		decision.Reason = ReasonWorkspaceAtRisk
	case input.Scheduler.PreemptionRiskUpper > cfg.PreemptionRiskBudget:
		decision.Reason = ReasonPreemptionAtRisk
	case input.Confidence < cfg.MinimumConfidence:
		decision.Reason = ReasonPredictorProfileUnknown
	}
	return decision
}
