package admission

import "testing"

func TestTPSRequestDemandPriorityDefaultsToBasicAndNormalizesUnknown(t *testing.T) {
	demand := NewTPSRequestDemand(2)
	if demand.Priority != RequestPriorityBasic || !demand.valid() {
		t.Fatalf("default demand=%+v", demand)
	}
	if got := demand.WithPriority(RequestPriority(99)); got.Priority != RequestPriorityBasic || !got.valid() {
		t.Fatalf("unknown priority was not normalized: %+v", got)
	}
}

func TestTPSRequestDemandPremiumPriorityIsValid(t *testing.T) {
	demand := NewTPSRequestDemand(1).WithPriority(RequestPriorityPremium)
	if !demand.valid() || demand.Priority.String() != "premium" {
		t.Fatalf("premium demand=%+v", demand)
	}
}
