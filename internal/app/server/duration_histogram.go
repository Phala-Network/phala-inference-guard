package server

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
)

func newDurationHistogram() durationHistogram {
	return histogram.NewDurationHistogram()
}

func newPredictiveDurationHistogram() durationHistogram {
	return histogram.NewPredictiveDurationHistogram()
}

func (s *proxyServer) observeProxyResult(result proxyResult) {
	s.proxyTotal.Observe(result.total)
	if result.firstByteOK {
		s.proxyTTFB.Observe(result.firstByte)
	}
}

func (s *proxyServer) observeInternalOverhead(total, queueWait, proxyTotal time.Duration) {
	overhead := total - queueWait - proxyTotal
	if overhead < 0 {
		overhead = 0
	}
	s.internalOverhead.Observe(overhead)
}
