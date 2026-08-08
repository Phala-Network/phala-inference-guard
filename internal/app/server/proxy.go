package server

import (
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func (s *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	if r.URL.Path == "/pig/metrics" {
		s.metrics(w, r)
		return
	}
	if r.URL.Path == "/v1/metrics" {
		s.combinedMetrics(w, r)
		return
	}
	if r.URL.Path == "/v1/upstream-status" {
		s.upstreamStatus(w, r)
		return
	}
	if attestationReportPath(r.URL.Path) {
		s.attestationReport(w, r)
		return
	}
	if s.requiresAPIAuth(r) && !s.authorized(r) {
		openai.WriteUnauthorized(w)
		return
	}
	r = r.WithContext(attachClientContext(r.Context(), r.Context()))
	requestStart := time.Now()
	if !s.requestClassifier.AdmittedPath(r) {
		s.forwardWithoutAdmission(w, r, requestStart)
		return
	}

	decisionStart := time.Now()
	classification, protocolError := s.requestClassifier.ClassifyRequest(r)
	if classification.Timing.BodyReadMeasured {
		s.bodyReadDuration.Observe(classification.Timing.BodyRead)
	}
	if classification.Timing.EstimatorMeasured {
		s.estimatorDuration.Observe(classification.Timing.Estimator)
	}
	if protocolError != nil {
		s.decisionDuration.Observe(time.Since(decisionStart))
		if protocolError.Reason == "invalid_json" {
			s.clientProtocolInvalidJSON.Add(1)
			openai.WriteInvalidJSON(w)
			return
		}
		openai.WriteInvalidJSON(w)
		return
	}
	decision := s.decidePredictiveShadow(r.Context(), predictiveShadowInput{
		Cost: classification.Cost,
	})
	reservation := decision.Reservation
	if s.cfg.PredictiveAdmissionMode == "enforce" && !decision.validEnforceResult() {
		if reservation != nil {
			reservation.Terminate(runtimepredictive.TerminalLocalQoSReject)
		}
		s.predictiveShadowFailures.decide.Add(1)
		s.predictiveEnforcedRejects.Add(1)
		s.total429.Add(1)
		s.decisionDuration.Observe(time.Since(decisionStart))
		openai.WriteTooManyRequests(w)
		return
	}
	if s.cfg.PredictiveAdmissionMode == "enforce" && decision.rejectsForward() {
		s.predictiveEnforcedRejects.Add(1)
		s.total429.Add(1)
		s.decisionDuration.Observe(time.Since(decisionStart))
		openai.WriteTooManyRequests(w)
		return
	}
	terminal := runtimepredictive.TerminalClientCancelled
	if reservation != nil {
		defer func() { reservation.Terminate(terminal) }()
		if !reservation.MarkForwarded() {
			terminal = runtimepredictive.TerminalLocalQoSReject
			s.predictiveShadowFailures.forward.Add(1)
			s.predictiveEnforcedRejects.Add(1)
			s.total429.Add(1)
			s.decisionDuration.Observe(time.Since(decisionStart))
			openai.WriteTooManyRequests(w)
			return
		}
		r = r.WithContext(attachPredictiveReservation(r.Context(), reservation))
	}
	s.decisionDuration.Observe(time.Since(decisionStart))
	result := s.proxyRequest(s.backend, w, r)
	terminal = predictiveTerminalCause(result)
	s.observeProxyResult(result)
	s.observeInternalOverhead(time.Since(requestStart), 0, result.total)
}

func (s *proxyServer) forwardWithoutAdmission(w http.ResponseWriter, r *http.Request, started time.Time) {
	if s.backend == nil {
		s.backendUnavailable.Add(1)
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	result := s.proxyRequest(s.backend, w, r)
	s.observeProxyResult(result)
	s.observeInternalOverhead(time.Since(started), 0, result.total)
}

func predictiveTerminalCause(result proxyResult) runtimepredictive.TerminalCause {
	switch {
	case result.timedOut:
		return runtimepredictive.TerminalTimeout
	case result.status == clientClosedRequestStatus:
		return runtimepredictive.TerminalClientDisconnected
	case result.proxyFailed:
		return runtimepredictive.TerminalUpstreamFailure
	case result.status >= http.StatusOK && result.status < http.StatusMultipleChoices:
		return runtimepredictive.TerminalCompleted
	default:
		return runtimepredictive.TerminalUpstreamFailure
	}
}
