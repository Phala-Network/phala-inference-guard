package metrics

import "testing"

func TestV0126MetricsPreserveDecodeInterferenceReason(t *testing.T) {
	if got := normalizeRequestAwareReason("decode_interference"); got != "decode_interference" {
		t.Fatalf("normalized Decode interference reason=%q, want bounded label preserved", got)
	}
	if got := normalizeRequestAwarePressureSource("decode"); got != "decode" {
		t.Fatalf("normalized Decode pressure source=%q, want decode", got)
	}
}
