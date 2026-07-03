package metrics

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
)

func TestWriteRuntimeExposesClientDisconnectMetrics(t *testing.T) {
	var out bytes.Buffer
	decision := histogram.NewDurationHistogram()
	proxyTTFB := histogram.NewDurationHistogram()
	requestSemanticTTFT := histogram.NewDurationHistogram()
	proxyTotal := histogram.NewDurationHistogram()
	internalOverhead := histogram.NewDurationHistogram()
	WriteRuntime(&out, RuntimeInput{
		Errors: ErrorSnapshot{
			ClientDisconnectQueue:    1,
			ClientDisconnectUpstream: 2,
			ClientDisconnectResponse: 3,
			ClientDisconnectCancel:   4,
		},
		Histograms: RuntimeHistograms{
			DecisionDuration:    &decision,
			ProxyTTFB:           &proxyTTFB,
			RequestSemanticTTFT: &requestSemanticTTFT,
			ProxyTotal:          &proxyTotal,
			InternalOverhead:    &internalOverhead,
		},
	})

	got := out.String()
	for _, want := range []string{
		`pig_client_disconnects_total{phase="queue"} 1`,
		`pig_client_disconnects_total{phase="upstream"} 2`,
		`pig_client_disconnects_total{phase="response"} 3`,
		"pig_client_disconnect_upstream_cancellations_total 4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}
