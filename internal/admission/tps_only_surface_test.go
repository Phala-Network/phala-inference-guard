package admission

import (
	"reflect"
	"testing"
)

func TestTPSOnlyAdmissionTypesDoNotOwnRetiredResourceFields(t *testing.T) {
	tests := []struct {
		name    string
		typeOf  reflect.Type
		retired []string
	}{
		{
			name:   "ControllerConfig",
			typeOf: reflect.TypeOf(ControllerConfig{}),
			retired: []string{
				"Capability", "WorkProfile",
			},
		},
		{
			name:   "TPSRequestDemand",
			typeOf: reflect.TypeOf(TPSRequestDemand{}),
			retired: []string{
				"OutputLimitTokens", "OutputLimitKnown",
			},
		},
		{
			name:   "BackendObservation",
			typeOf: reflect.TypeOf(BackendObservation{}),
			retired: []string{
				"MaxModelLenTokens", "KVCapacityTokens", "KVBlockSize", "UsedKVTokens",
				"CacheQueryTokensTotal", "CacheHitTokensTotal", "CacheCountersValid",
			},
		},
		{
			name:   "ProjectedState",
			typeOf: reflect.TypeOf(ProjectedState{}),
			retired: []string{
				"ObservedKVTokens", "ReservationKVTokens", "EffectiveKVTokens",
				"PendingPrefillInputTokens", "PendingPrefillTokens",
				"PendingCacheCreditTokens", "PendingPrefillSequences",
				"PendingExclusiveSequences", "PendingQuiescentSequences",
				"LocalActiveDecode", "CacheObservationValid", "CacheHitFraction",
				"CacheCreditFraction", "CacheEvidenceTokens", "CacheCreditBudgetTokens",
				"CacheCreditSpentTokens",
			},
		},
		{
			name:   "DecisionRecord",
			typeOf: reflect.TypeOf(DecisionRecord{}),
			retired: []string{
				"Estimate", "PrefillClass", "Work", "PostAdmitKVTokens", "HardKVLimitTokens",
				"RemainingKVTokens", "PendingPrefillTokensBefore",
				"PendingPrefillTokensAfter",
			},
		},
		{
			name:   "reservationOverlay",
			typeOf: reflect.TypeOf(reservationOverlay{}),
			retired: []string{
				"kvTokens", "pendingPrefillInputTokens", "pendingPrefillTokens",
				"pendingPrefillSequences", "pendingExclusiveSequences",
				"pendingQuiescentSequences", "localActiveDecode",
			},
		},
	}

	for _, test := range tests {
		for _, field := range test.retired {
			if _, exists := test.typeOf.FieldByName(field); exists {
				t.Errorf("%s retained TPS-external field %s", test.name, field)
			}
		}
	}
}
