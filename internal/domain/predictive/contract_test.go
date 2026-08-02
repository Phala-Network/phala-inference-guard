package predictive

import (
	"testing"
	"time"
)

func TestEvaluateProtectsExistingUsersBeforeOtherSoftConstraints(t *testing.T) {
	decision := Evaluate(EvaluationInput{
		Projection: Projection{
			PhysicalKVUpper: 75_000,
			ActiveKVUpper:   75_000,
		},
		Scheduler: SchedulerEstimate{
			ExistingUserTPSLower: 24.9,
			NewUserTPSLower:      30,
			TTFTUpper:            200 * time.Millisecond,
			TPOTUpper:            30 * time.Millisecond,
		},
		Constraints: Constraints{
			PhysicalKVHard:    88_000,
			ActiveKVHard:      88_000,
			UserTPSTarget:     25,
			TPOTSLO:           50 * time.Millisecond,
			MinimumConfidence: 0.90,
		},
		Confidence: 0.99,
	})
	if decision.Reason != ReasonExistingTPSAtRisk {
		t.Fatalf("reason = %s, want %s", decision.Reason, ReasonExistingTPSAtRisk)
	}
}

func TestEvaluateFitsOnlyWhenEveryBoundPasses(t *testing.T) {
	decision := Evaluate(EvaluationInput{
		Projection: Projection{
			PhysicalKVUpper: 75_000,
			ActiveKVUpper:   76_000,
		},
		Scheduler: SchedulerEstimate{
			ExistingUserTPSLower: 27,
			NewUserTPSLower:      26,
			TTFTUpper:            500 * time.Millisecond,
			TPOTUpper:            35 * time.Millisecond,
			WorkspaceRiskUpper:   0.01,
			PreemptionRiskUpper:  0.001,
		},
		Constraints: Constraints{
			PhysicalKVHard:       88_000,
			ActiveKVHard:         84_000,
			UserTPSTarget:        25,
			TPOTSLO:              40 * time.Millisecond,
			WorkspaceRiskBudget:  0.02,
			PreemptionRiskBudget: 0.002,
			MinimumConfidence:    0.95,
		},
		Confidence: 0.99,
	})
	if decision.Reason != ReasonFit {
		t.Fatalf("reason = %s, want %s", decision.Reason, ReasonFit)
	}
}

func TestEvaluateKeepsTTFTObservationalInsteadOfRejecting(t *testing.T) {
	decision := Evaluate(EvaluationInput{
		Scheduler: SchedulerEstimate{
			ExistingUserTPSLower:         30,
			ExistingUserTPSNotApplicable: true,
			NewUserTPSLower:              30,
			TTFTUpper:                    time.Hour,
			TPOTUpper:                    20 * time.Millisecond,
		},
		Constraints: Constraints{
			UserTPSTarget:     25,
			TPOTSLO:           40 * time.Millisecond,
			MinimumConfidence: 0.9,
		},
		Confidence: 1,
	})
	if decision.Reason != ReasonFit {
		t.Fatalf("adverse TTFT rejected request: reason=%s want=%s", decision.Reason, ReasonFit)
	}
}

func TestEvaluateTreatsZeroRiskBudgetAsZeroTolerance(t *testing.T) {
	for name, test := range map[string]struct {
		estimate SchedulerEstimate
		want     Reason
	}{
		"workspace": {
			estimate: SchedulerEstimate{WorkspaceRiskUpper: 0.001},
			want:     ReasonWorkspaceAtRisk,
		},
		"preemption": {
			estimate: SchedulerEstimate{PreemptionRiskUpper: 0.001},
			want:     ReasonPreemptionAtRisk,
		},
	} {
		t.Run(name, func(t *testing.T) {
			decision := Evaluate(EvaluationInput{
				Scheduler:   test.estimate,
				Constraints: Constraints{},
				Confidence:  1,
			})
			if decision.Reason != test.want {
				t.Fatalf("reason = %s, want %s for a non-zero risk with zero budget", decision.Reason, test.want)
			}
		})
	}
	if decision := Evaluate(EvaluationInput{Confidence: 1}); decision.Reason != ReasonFit {
		t.Fatalf("zero risks with zero budgets = %s, want %s", decision.Reason, ReasonFit)
	}
}
