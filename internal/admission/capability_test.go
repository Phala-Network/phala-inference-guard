package admission

import "testing"

func TestCapabilityRejectsInvalidGeometryAndPrefillProfiles(t *testing.T) {
	valid := testCapability()
	tests := []Capability{
		{},
		func() Capability { value := valid; value.Fingerprint = ""; return value }(),
		func() Capability { value := valid; value.KVBlockSize = 0; return value }(),
		func() Capability { value := valid; value.KVHardLimitTokens = value.KVCapacityTokens; return value }(),
		func() Capability { value := valid; value.KVHardLimitTokens++; return value }(),
		func() Capability { value := valid; value.MaximumInputTokens = value.MaxModelLenTokens; return value }(),
		func() Capability {
			value := valid
			value.PrefillExclusiveTokens = value.PrefillRegularTokens
			return value
		}(),
		func() Capability {
			value := valid
			value.PrefillContendedBudgetTokens = value.PrefillRegularTokens + value.KVBlockSize
			return value
		}(),
		func() Capability {
			value := valid
			value.PrefillAggregateBudgetTokens = value.PrefillQuiescentTokens + value.KVBlockSize
			return value
		}(),
	}
	for index, capability := range tests {
		if err := capability.Validate(); err == nil {
			t.Fatalf("case %d accepted invalid capability=%+v", index, capability)
		}
	}
}

func TestCapabilityKeepsPortableBandsFixedWhenContextMakesThemUnreachable(t *testing.T) {
	capability := testCapability()
	capability.MaxModelLenTokens = 128 * 1024
	capability.MaximumInputTokens = 120 * 1024
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	if capability.PrefillRegularTokens != 64*1024 ||
		capability.PrefillExclusiveTokens != 256*1024 ||
		capability.PrefillQuiescentTokens != 512*1024 {
		t.Fatalf("portable bands were context-rescaled: %+v", capability)
	}
}
