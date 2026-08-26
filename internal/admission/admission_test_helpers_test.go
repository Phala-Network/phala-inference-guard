package admission

func testCapability() Capability {
	return Capability{
		Fingerprint:                  "test-capability",
		MaxModelLenTokens:            1_048_576,
		KVCapacityTokens:             10_000_000,
		KVBlockSize:                  64,
		KVHardLimitTokens:            8_000_000,
		MaximumInputTokens:           800_000,
		MinimumDecodeHorizonTokens:   256,
		PrefillRegularTokens:         64 * 1024,
		PrefillExclusiveTokens:       256 * 1024,
		PrefillQuiescentTokens:       512 * 1024,
		PrefillContendedBudgetTokens: 64 * 1024,
		PrefillAggregateBudgetTokens: 256 * 1024,
	}
}
