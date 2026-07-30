package kvadmission

import (
	"testing"
	"time"
)

func testCost(high int64) Cost {
	return Cost{Supported: true, EstimatedInputLow: high / 2, EstimatedInputHigh: high, BoundedDecodeTokens: 64}
}

func testBackend(now time.Time, name string, used int64) BackendSnapshot {
	return BackendSnapshot{
		Name:              name,
		Kind:              BackendVLLM,
		CapacityTokens:    100000,
		UsedTokens:        used,
		Updated:           now,
		TokenMetricsValid: true,
	}
}

func TestEvaluateFitsBelowHardBudget(t *testing.T) {
	now := time.Unix(100, 0)
	policy := DefaultPolicy()
	policy.DecodeDriftTokens = 100
	decision := Evaluate(now, testCost(1000), []BackendSnapshot{testBackend(now, "a", 80000)}, nil, policy)
	if decision.Reason != ReasonFit || decision.Backend != "a" {
		t.Fatalf("decision=%#v want fit/a", decision)
	}
	if decision.ProjectedHighTokens > decision.HardBudgetTokens {
		t.Fatalf("fit crossed hard budget: %#v", decision)
	}
}

func TestEvaluateNeverFitsAboveHardBudget(t *testing.T) {
	now := time.Unix(100, 0)
	policy := DefaultPolicy()
	decision := Evaluate(now, testCost(10000), []BackendSnapshot{testBackend(now, "a", 82000)}, nil, policy)
	if decision.Reason != ReasonOverBudget {
		t.Fatalf("decision=%#v want over_budget", decision)
	}
	if decision.Backend != "a" || decision.CapacityTokens != 100000 || decision.HardBudgetTokens == 0 || decision.ProjectedHighTokens == 0 {
		t.Fatalf("over-budget decision lost backend evidence: %#v", decision)
	}
}

func TestEvaluateEmergencyPreservesDeterministicBackendEvidence(t *testing.T) {
	now := time.Unix(100, 0)
	b := testBackend(now, "b", 95000)
	a := testBackend(now, "a", 96000)
	decision := Evaluate(now, testCost(100), []BackendSnapshot{b, a}, nil, DefaultPolicy())
	if decision.Reason != ReasonEmergencyRed || decision.Backend != "a" {
		t.Fatalf("decision=%#v want emergency_red/a", decision)
	}
	if decision.ObservedTokens != 96000 || decision.CapacityTokens != 100000 || decision.EmergencyTokens != 90000 {
		t.Fatalf("emergency decision lost token evidence: %#v", decision)
	}
}

func TestEvaluateTreatsStaleAndUnknownAsNotFit(t *testing.T) {
	now := time.Unix(100, 0)
	policy := DefaultPolicy()
	stale := testBackend(now.Add(-policy.MaxMetricsAge-time.Millisecond), "stale", 1000)
	if got := Evaluate(now, testCost(100), []BackendSnapshot{stale}, nil, policy).Reason; got != ReasonStaleMetrics {
		t.Fatalf("reason=%s want stale_metrics", got)
	}
	unknown := testBackend(now, "unknown", 1000)
	unknown.TokenMetricsValid = false
	if got := Evaluate(now, testCost(100), []BackendSnapshot{unknown}, nil, policy).Reason; got != ReasonCapacityUnknown {
		t.Fatalf("reason=%s want capacity_unknown", got)
	}
}

func TestEvaluateClosesWaitingAndCooldown(t *testing.T) {
	now := time.Unix(100, 0)
	backend := testBackend(now, "a", 1000)
	backend.Waiting = 1
	if got := Evaluate(now, testCost(100), []BackendSnapshot{backend}, nil, DefaultPolicy()).Reason; got != ReasonBackendWaiting {
		t.Fatalf("reason=%s want backend_waiting", got)
	}
	backend.Waiting = 0
	backend.PreemptionCooldown = true
	if got := Evaluate(now, testCost(100), []BackendSnapshot{backend}, nil, DefaultPolicy()).Reason; got != ReasonPreemptionCooldown {
		t.Fatalf("reason=%s want preemption_cooldown", got)
	}
}

func TestEvaluateSelectsPerBackendFit(t *testing.T) {
	now := time.Unix(100, 0)
	a := testBackend(now, "a", 85000)
	b := testBackend(now, "b", 50000)
	decision := Evaluate(now, testCost(1000), []BackendSnapshot{a, b}, nil, DefaultPolicy())
	if decision.Reason != ReasonFit || decision.Backend != "b" {
		t.Fatalf("decision=%#v want fit/b", decision)
	}
}

func BenchmarkDecision(b *testing.B) {
	now := time.Unix(100, 0)
	backends := []BackendSnapshot{testBackend(now, "a", 50000), testBackend(now, "b", 60000)}
	cost := testCost(1000)
	policy := DefaultPolicy()
	reserved := map[string]int64{"a": 2000, "b": 1000}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if Evaluate(now, cost, backends, reserved, policy).Reason != ReasonFit {
			b.Fatal("not fit")
		}
	}
}
