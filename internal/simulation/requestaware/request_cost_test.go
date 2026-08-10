package requestaware

import "testing"

func TestSimulationRequestCostUsesProductionRollingHorizonContract(t *testing.T) {
	request := requestSpec{
		id:             "live-shape",
		selectionInput: 1_298,
		safetyInput:    2_501,
		decodeHorizon:  256,
	}
	cost := simulationRequestCost(request)
	if cost.InputTokens != 2_501 || cost.UncachedPrefillUpper != 2_501 ||
		cost.DecodeHorizonUpper != 256 || cost.ActiveContextTokensUpper != 2_757 ||
		cost.FutureContextTokensUpper != 256 || cost.KV.PhysicalKVUpper != 2_816 ||
		cost.FutureKV.PhysicalKVUpper != 256 || simulationReservedTokens(request) != 2_816 {
		t.Fatalf("simulation request cost=%+v reserved=%d", cost, simulationReservedTokens(request))
	}
}
