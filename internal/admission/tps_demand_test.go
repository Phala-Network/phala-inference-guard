package admission

import "testing"

func TestTPSRequestDemandTierDefaultsToDefaultAndNormalizesUnknown(t *testing.T) {
	demand := NewTPSRequestDemand(2)
	if demand.Tier != TierDefault || !demand.valid() {
		t.Fatalf("default demand=%+v", demand)
	}
	if got := demand.WithTier(RequestTier(99)); got.Tier != TierDefault || !got.valid() {
		t.Fatalf("unknown tier was not normalized: %+v", got)
	}
}

func TestTPSRequestDemandPriorityIsValid(t *testing.T) {
	demand := NewTPSRequestDemand(1).WithTier(TierPriority)
	if !demand.valid() || demand.Tier.String() != "priority" {
		t.Fatalf("priority demand=%+v", demand)
	}
}

func TestParseRequestTier(t *testing.T) {
	tests := map[string]RequestTier{
		"priority":   TierPriority,
		" PRIORITY ": TierPriority,
		"flex":       TierFlex,
		" Flex ":     TierFlex,
		"default":    TierDefault,
		"":           TierDefault,
		"premium":    TierDefault,
		"basic":      TierDefault,
		"priority2":  TierDefault,
	}
	for value, want := range tests {
		if got := ParseRequestTier(value); got != want {
			t.Errorf("ParseRequestTier(%q)=%s want %s", value, got, want)
		}
	}
}

func TestRequestTierRankOrdersByRejectionCost(t *testing.T) {
	if !(TierPriority.Rank() > TierDefault.Rank() && TierDefault.Rank() > TierFlex.Rank()) {
		t.Fatalf("ranks priority=%d default=%d flex=%d", TierPriority.Rank(), TierDefault.Rank(), TierFlex.Rank())
	}
	var zero RequestTier
	if zero != TierDefault {
		t.Fatalf("zero tier=%s want default", zero)
	}
}

func TestRequestTierOnlyPriorityIsNotSheddable(t *testing.T) {
	for tier, want := range map[RequestTier]bool{TierPriority: false, TierDefault: true, TierFlex: true, RequestTier(99): false} {
		if got := tier.Sheddable(); got != want {
			t.Errorf("%s.Sheddable()=%t want %t", tier, got, want)
		}
	}
}
