package predictive

import "testing"

func TestCapabilityProfileDerivesFrozenKVAndCalibratedPrefillLimits(t *testing.T) {
	profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
		ModelIdentitySHA256:             "identity",
		KVCapacityTokens:                1_000_000,
		KVBlockSize:                     64,
		KVHardRatio:                     0.88,
		ObservedColdPrefillTokensPerSec: 10_000,
		Source:                          CapabilityProfileCalibrated,
	})
	if err != nil {
		t.Fatalf("derive calibrated profile: %v", err)
	}
	if profile.KVHardLimitTokens != 880_000 {
		t.Fatalf("hard KV limit = %d", profile.KVHardLimitTokens)
	}
	if profile.SafeColdPrefillTokensPerSec != 8_000 || profile.PrefillRegularTokens != 40_000 ||
		profile.PrefillExclusiveTokens != 160_000 || profile.PrefillQuiescentTokens != 320_000 ||
		profile.PrefillAggregateBudgetTokens != 160_000 {
		t.Fatalf("calibrated Prefill profile = %+v", profile)
	}
}

func TestCapabilityProfileAlignsStaticFallbackWithoutInventingRate(t *testing.T) {
	profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
		ModelIdentitySHA256: "identity",
		KVCapacityTokens:    1_003_000,
		KVBlockSize:         64,
		KVHardRatio:         0.88,
		Prefill: PrefillTokenBounds{
			Regular: 64*1024 + 1, Exclusive: 256*1024 + 1,
			Quiescent: 512*1024 + 1, Aggregate: 256*1024 + 1,
		},
		Source: CapabilityProfileFallback,
	})
	if err != nil {
		t.Fatalf("derive fallback profile: %v", err)
	}
	if profile.SafeColdPrefillTokensPerSec != 0 || profile.PrefillRegularTokens != 64*1024 ||
		profile.PrefillExclusiveTokens != 256*1024 || profile.PrefillQuiescentTokens != 512*1024 ||
		profile.PrefillAggregateBudgetTokens != 256*1024 {
		t.Fatalf("fallback profile = %+v", profile)
	}
}

func TestCapabilityProfileDoesNotWidenPrefillFromIdleProbeAlone(t *testing.T) {
	profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
		ModelIdentitySHA256:             "identity",
		KVCapacityTokens:                4 * 1024 * 1024,
		KVBlockSize:                     64,
		KVHardRatio:                     0.88,
		ObservedColdPrefillTokensPerSec: 40_000,
		Source:                          CapabilityProfileCalibrated,
	})
	if err != nil {
		t.Fatalf("derive fast calibrated profile: %v", err)
	}
	if profile.SafeColdPrefillTokensPerSec != 32_000 ||
		profile.PrefillRegularTokens != 64*1024 ||
		profile.PrefillExclusiveTokens != 256*1024 ||
		profile.PrefillQuiescentTokens != 512*1024 ||
		profile.PrefillAggregateBudgetTokens != 256*1024 {
		t.Fatalf("fast idle probe widened Prefill policy: %+v", profile)
	}
}

func TestCapabilityProfileRejectsInvalidGeometryRateAndOrdering(t *testing.T) {
	base := CapabilityProfileInput{
		ModelIdentitySHA256: "identity", KVCapacityTokens: 1_000_000, KVBlockSize: 64,
		KVHardRatio:                     0.88,
		ObservedColdPrefillTokensPerSec: 10_000, Source: CapabilityProfileCalibrated,
	}
	tests := []struct {
		name   string
		mutate func(*CapabilityProfileInput)
	}{
		{name: "identity", mutate: func(input *CapabilityProfileInput) { input.ModelIdentitySHA256 = "" }},
		{name: "capacity", mutate: func(input *CapabilityProfileInput) { input.KVCapacityTokens = 0 }},
		{name: "block", mutate: func(input *CapabilityProfileInput) { input.KVBlockSize = 0 }},
		{name: "hard ratio", mutate: func(input *CapabilityProfileInput) { input.KVHardRatio = 1 }},
		{name: "rate", mutate: func(input *CapabilityProfileInput) { input.ObservedColdPrefillTokensPerSec = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := NewBackendCapabilityProfile(input); err == nil {
				t.Fatal("invalid capability profile was accepted")
			}
		})
	}
}

func TestCapabilityProfileValidationRejectsForgedRuntimeProfile(t *testing.T) {
	profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
		ModelIdentitySHA256: "model-hash", KVCapacityTokens: 1_000_000, KVBlockSize: 64,
		KVHardRatio: 0.88,
		Prefill: PrefillTokenBounds{
			Regular: 64 * 1024, Exclusive: 256 * 1024,
			Quiescent: 512 * 1024, Aggregate: 256 * 1024,
		},
		Source: CapabilityProfileFallback,
	})
	if err != nil {
		t.Fatalf("construct valid profile: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*BackendCapabilityProfile)
	}{
		{name: "schema", mutate: func(value *BackendCapabilityProfile) { value.SchemaVersion = "other" }},
		{name: "hard limit", mutate: func(value *BackendCapabilityProfile) { value.KVHardLimitTokens = value.KVCapacityTokens }},
		{name: "prefill", mutate: func(value *BackendCapabilityProfile) { value.PrefillExclusiveTokens = value.PrefillRegularTokens }},
		{name: "unmeasured rate", mutate: func(value *BackendCapabilityProfile) { value.SafeColdPrefillTokensPerSec = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := profile
			test.mutate(&forged)
			if err := forged.Validate(); err == nil {
				t.Fatal("forged capability profile passed validation")
			}
		})
	}
}
