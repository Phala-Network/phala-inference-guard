package requestaware

import "testing"

func TestSimulationRequestWorkUsesProductionRollingHorizonContract(t *testing.T) {
	request := requestSpec{
		id:             "live-shape",
		selectionInput: 1_298,
		safetyInput:    2_501,
		decodeHorizon:  256,
	}
	work := simulationRequestWork(request)
	if work.Estimate.SelectionInputTokens != 1_298 ||
		work.Estimate.KVReservationInputTokens != 2_501 ||
		work.Estimate.DecodeHorizonTokens != 256 ||
		work.InputKVTokens != 2_560 || work.TotalKVTokens != 2_816 ||
		work.FutureKVTokens != 256 || simulationReservedTokens(request) != 2_816 {
		t.Fatalf("simulation request work=%+v reserved=%d", work, simulationReservedTokens(request))
	}
}
