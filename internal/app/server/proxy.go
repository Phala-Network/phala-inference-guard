package server

import (
	"net/http"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
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
	if r.URL.Path == predictivePolicyAPIPath {
		s.predictivePolicyAPI(w, r)
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
	s.requestEvidence.Record(classification)
	responseEvidence := s.responseUsageEvidence.Begin(classification, r.URL.Path, s.cfg.PathSuffixMatch)
	defer responseEvidence.Censor()
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
	estimate, estimateKnown := classification.Cost.PredictiveEstimate()
	if !estimateKnown {
		// Unsupported or temporarily unclassifiable inputs are request-scoped
		// protection, not evidence that the whole upstream is unavailable. The
		// Controller turns this invalid estimate into an observable, non-reserving
		// decision while its canonical capacity probe remains independent.
		estimate = domainpredictive.RequestEstimate{}
	}
	decision := s.decideAdmission(r.Context(), estimate)
	reservation := decision.Reservation
	if s.cfg.PredictiveAdmissionMode == "enforce" && !decision.Record.Admitted() {
		s.predictiveEnforcedRejects.Add(1)
		s.total429.Add(1)
		s.decisionDuration.Observe(time.Since(decisionStart))
		openai.WriteTooManyRequests(w)
		return
	}
	terminal := coreadmission.TerminalCancel
	if reservation != nil {
		defer func() { reservation.Terminate(terminal) }()
		if !reservation.MarkForwarded() {
			terminal = coreadmission.TerminalError
			s.admissionFailures.forward.Add(1)
			if s.cfg.PredictiveAdmissionMode == "enforce" {
				s.predictiveEnforcedRejects.Add(1)
			}
			s.total429.Add(1)
			s.decisionDuration.Observe(time.Since(decisionStart))
			openai.WriteTooManyRequests(w)
			return
		}
		r = r.WithContext(attachAdmissionReservation(r.Context(), reservation))
	}
	r = r.WithContext(attachResponseUsageRequestEvidence(r.Context(), responseEvidence))
	s.decisionDuration.Observe(time.Since(decisionStart))
	result := s.proxyRequest(s.backend, w, r)
	responseEvidence.Complete(result)
	terminal = admissionTerminalCause(result)
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

func admissionTerminalCause(result proxyResult) coreadmission.TerminalCause {
	switch {
	case result.timedOut:
		return coreadmission.TerminalTimeout
	case result.status == clientClosedRequestStatus:
		return coreadmission.TerminalDisconnect
	case result.proxyFailed:
		return coreadmission.TerminalError
	case result.status >= http.StatusOK && result.status < http.StatusMultipleChoices:
		return coreadmission.TerminalSuccess
	default:
		return coreadmission.TerminalError
	}
}
