package predictive

import (
	"reflect"
	"testing"
)

func TestCapabilityProfileRetiresSyntheticPerformanceRate(t *testing.T) {
	for _, value := range []struct {
		name   string
		typeOf reflect.Type
		field  string
	}{
		{name: "profile safe rate", typeOf: reflect.TypeOf(BackendCapabilityProfile{}), field: "SafeColdPrefillTokensPerSec"},
		{name: "input observed rate", typeOf: reflect.TypeOf(CapabilityProfileInput{}), field: "ObservedColdPrefillTokensPerSec"},
	} {
		if _, exists := value.typeOf.FieldByName(value.field); exists {
			t.Errorf("%s field %s remains in the immutable capability contract", value.name, value.field)
		}
	}
}

func TestCapabilityProfileDerivesDeterministicAutomaticBounds(t *testing.T) {
	tests := []struct {
		name          string
		maxModelLen   int64
		capacity      int64
		wantHard      int64
		wantMaxInput  int64
		wantRegular   int64
		wantExclusive int64
		wantQuiescent int64
		wantContended int64
		wantAggregate int64
	}{
		{name: "32K context", maxModelLen: 32 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 32*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 32*1024 - 256, wantAggregate: 32*1024 - 256},
		{name: "256K context", maxModelLen: 256 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 256*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256*1024 - 256},
		{name: "650K context", maxModelLen: 650 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 650*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256 * 1024},
		{name: "KV limited", maxModelLen: 650 * 1024, capacity: 300_000, wantHard: 264_000, wantMaxInput: 264_000 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256 * 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
				ModelIdentitySHA256: "identity",
				KVCapacityTokens:    test.capacity,
				KVBlockSize:         64,
				KVHardRatio:         0.88,
				MaxModelLen:         test.maxModelLen,
				Source:              CapabilityProfileAutomatic,
			})
			if err != nil {
				t.Fatalf("derive automatic profile: %v", err)
			}
			if profile.SchemaVersion != "request-aware-capability-v3" ||
				profile.KVHardLimitTokens != test.wantHard ||
				profile.MaxModelLenTokens != test.maxModelLen ||
				profile.MaximumAdmissibleInputTokens != test.wantMaxInput ||
				profile.PrefillRegularTokens != test.wantRegular ||
				profile.PrefillExclusiveTokens != test.wantExclusive ||
				profile.PrefillQuiescentTokens != test.wantQuiescent ||
				profile.PrefillContendedBudgetTokens != test.wantContended ||
				profile.PrefillAggregateBudgetTokens != test.wantAggregate {
				t.Fatalf("automatic capability profile = %+v", profile)
			}
		})
	}
}

func TestCapabilityProfileAlignsCompleteExplicitBounds(t *testing.T) {
	profile, err := NewBackendCapabilityProfile(CapabilityProfileInput{
		ModelIdentitySHA256: "identity",
		KVCapacityTokens:    1_003_000,
		KVBlockSize:         64,
		KVHardRatio:         0.88,
		MaxModelLen:         650 * 1024,
		Prefill: PrefillTokenBounds{
			Regular: 32*1024 + 1, Exclusive: 128*1024 + 1,
			Quiescent: 256*1024 + 1, Aggregate: 192*1024 + 1,
		},
		Source: CapabilityProfileExplicit,
	})
	if err != nil {
		t.Fatalf("derive explicit profile: %v", err)
	}
	if profile.PrefillRegularTokens != 32*1024 || profile.PrefillExclusiveTokens != 128*1024 ||
		profile.PrefillQuiescentTokens != 256*1024 || profile.PrefillContendedBudgetTokens != 32*1024 ||
		profile.PrefillAggregateBudgetTokens != 192*1024 {
		t.Fatalf("explicit capability profile = %+v", profile)
	}
}

func TestCapabilityProfileRejectsInvalidGeometryAndBounds(t *testing.T) {
	base := CapabilityProfileInput{
		ModelIdentitySHA256: "identity",
		KVCapacityTokens:    1_000_000,
		KVBlockSize:         64,
		KVHardRatio:         0.88,
		MaxModelLen:         256 * 1024,
		Source:              CapabilityProfileAutomatic,
	}
	tests := []struct {
		name   string
		mutate func(*CapabilityProfileInput)
	}{
		{name: "identity", mutate: func(input *CapabilityProfileInput) { input.ModelIdentitySHA256 = "" }},
		{name: "capacity", mutate: func(input *CapabilityProfileInput) { input.KVCapacityTokens = 0 }},
		{name: "block", mutate: func(input *CapabilityProfileInput) { input.KVBlockSize = 0 }},
		{name: "hard ratio", mutate: func(input *CapabilityProfileInput) { input.KVHardRatio = 1 }},
		{name: "model length", mutate: func(input *CapabilityProfileInput) { input.MaxModelLen = 0 }},
		{name: "tiny span", mutate: func(input *CapabilityProfileInput) { input.MaxModelLen = 128 }},
		{name: "unknown source", mutate: func(input *CapabilityProfileInput) { input.Source = "unknown" }},
		{name: "explicit ordering", mutate: func(input *CapabilityProfileInput) {
			input.Source = CapabilityProfileExplicit
			input.Prefill = PrefillTokenBounds{Regular: 64 * 1024, Exclusive: 32 * 1024, Quiescent: 128 * 1024, Aggregate: 64 * 1024}
		}},
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
		ModelIdentitySHA256: "model-hash",
		KVCapacityTokens:    1_000_000,
		KVBlockSize:         64,
		KVHardRatio:         0.88,
		MaxModelLen:         256 * 1024,
		Source:              CapabilityProfileAutomatic,
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
		{name: "model length", mutate: func(value *BackendCapabilityProfile) { value.MaxModelLenTokens = 0 }},
		{name: "maximum input", mutate: func(value *BackendCapabilityProfile) { value.MaximumAdmissibleInputTokens++ }},
		{name: "prefill", mutate: func(value *BackendCapabilityProfile) { value.PrefillExclusiveTokens = value.PrefillRegularTokens }},
		{name: "contended budget", mutate: func(value *BackendCapabilityProfile) { value.PrefillContendedBudgetTokens = 0 }},
		{name: "aggregate budget", mutate: func(value *BackendCapabilityProfile) {
			value.PrefillAggregateBudgetTokens = value.PrefillExclusiveTokens + value.KVBlockSize
		}},
		{name: "source", mutate: func(value *BackendCapabilityProfile) { value.Source = "unknown" }},
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
