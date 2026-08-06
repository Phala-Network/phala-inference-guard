package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	requesttier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
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
	admitted := s.admittedPath(r)
	if !admitted {
		backend := s.chooseBackend()
		if backend == nil {
			s.unavailable(w, "backend_unavailable")
			return
		}
		result := s.proxyRequest(backend, w, r)
		s.observeProxyResult(result)
		s.observeInternalOverhead(time.Since(requestStart), 0, result.total)
		return
	}
	decisionStart := time.Now()
	estimatorStart := time.Now()
	classification := s.classifyRequest(r)
	if s.kvShadow != nil || predictiveAdmissionEnabled(s.cfg.PredictiveAdmissionMode) {
		s.kvEstimatorDuration.Observe(time.Since(estimatorStart))
	}
	predictiveDecision := s.decidePredictiveShadow(r.Context(), predictiveShadowInput{
		Path:             r.URL.Path,
		Cost:             classification.KVCost,
		RequestStartedAt: requestStart,
		OutputTokens:     classification.PredictiveOutputTokens,
		HasOutputTokens:  classification.PredictiveHasOutputTokens,
		Streaming:        classification.Streaming,
	})
	predictiveReservation := predictiveDecision.Reservation
	if s.cfg.PredictiveAdmissionMode == "enforce" && !predictiveDecision.validEnforceResult() {
		s.decisionDuration.Observe(time.Since(decisionStart))
		s.predictiveShadowFailures.decide.Add(1)
		s.logPredictiveFailureReject("invalid_decision_result")
		if predictiveReservation != nil {
			predictiveReservation.Terminate(runtimepredictive.TerminalLocalQoSReject)
		}
		s.predictiveEnforcedRejects.Add(1)
		s.reject(w, s.globalLn, "predictive_admission")
		return
	}
	if s.cfg.PredictiveAdmissionMode == "enforce" && predictiveDecision.rejectsForward() {
		s.decisionDuration.Observe(time.Since(decisionStart))
		s.predictiveEnforcedRejects.Add(1)
		s.reject(w, s.globalLn, "predictive_admission")
		return
	}
	predictiveCause := runtimepredictive.TerminalClientCancelled
	if predictiveReservation != nil {
		defer func() { predictiveReservation.Terminate(predictiveCause) }()
	}
	releaseKVShadow := s.shadowKVRequest(classification.KVCost)
	defer releaseKVShadow()
	ln := classification.Lane
	outputTokens := classification.OutputTokens
	hasOutputTokens := classification.HasOutputTokens
	legacyQoS := s.cfg.PredictiveAdmissionMode != "enforce"
	tier := requesttier.Basic
	if legacyQoS {
		tier = requesttier.FromHeader(r)
	}
	ln.ObserveBody(r.ContentLength)
	s.globalLn.ObserveBody(r.ContentLength)
	var releaseQoS func()
	var queueWait time.Duration
	if legacyQoS {
		var qosReject string
		releaseQoS, qosReject, queueWait = s.qosGate.WaitAcquire(r.Context(), ln, tier)
		if releaseQoS == nil {
			if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseQueue, false) {
				predictiveCause = runtimepredictive.TerminalClientDisconnected
				return
			}
			if qosReject == "backend_unavailable" {
				predictiveCause = runtimepredictive.TerminalUpstreamFailure
				s.unavailable(w, qosReject)
				return
			}
			predictiveCause = runtimepredictive.TerminalLocalQoSReject
			s.qosGate.ObserveReject(ln, tier, qosReject)
			rejectLane := ln
			if strings.HasPrefix(qosReject, "global_") {
				rejectLane = s.globalLn
			}
			s.reject(w, rejectLane, qosReject)
			return
		}
	}
	decisionElapsed := time.Since(decisionStart) - queueWait
	if decisionElapsed < 0 {
		decisionElapsed = 0
	}
	s.decisionDuration.Observe(decisionElapsed)
	if releaseQoS != nil {
		defer releaseQoS()
	}
	backend := s.chooseBackend()
	if backend == nil {
		predictiveCause = runtimepredictive.TerminalUpstreamFailure
		s.unavailable(w, "backend_unavailable")
		return
	}
	if !s.priorityInjector.Inject(r, tier) {
		predictiveCause = runtimepredictive.TerminalLocalQoSReject
		rejectCode := "request_compat_rewrite"
		if legacyQoS {
			rejectCode = "backend_priority_injection"
			s.qosGate.ObserveReject(ln, tier, rejectCode)
		}
		s.reject(w, ln, rejectCode)
		return
	}
	predictiveForwarded := false
	if predictiveReservation != nil {
		predictiveForwarded = predictiveReservation.MarkForwarded()
		if !predictiveForwarded && s.cfg.PredictiveAdmissionMode == "enforce" {
			predictiveCause = runtimepredictive.TerminalLocalQoSReject
			s.predictiveShadowFailures.forwardRejected.Add(1)
			s.logPredictiveFailureReject("forward_commit")
			s.predictiveEnforcedRejects.Add(1)
			s.reject(w, s.globalLn, "predictive_admission")
			return
		}
	}
	if predictiveForwarded {
		r = attachPredictiveResponseObserver(r, predictiveReservation, requestStart, classification.Streaming, &s.predictiveCompletionObserver)
	}
	s.globalLn.ObserveAccepted()
	ln.ObserveAccepted()
	if legacyQoS {
		s.qosGate.ObserveAccepted(tier)
	}
	prefillGrace := s.prefillGraceDuration(r, classification.Streaming)
	markDecode, doneActive := s.trackActiveRequest(prefillGrace)
	defer doneActive()
	semanticCallbacks := semanticResponseCallbacks{observed: markDecode}
	if predictiveForwarded {
		semanticCallbacks.observed = func() {
			markDecode()
			predictiveReservation.MarkPrefillComplete()
		}
		semanticCallbacks.delivered = func(ttft time.Duration) {
			observePredictiveSemanticTTFT(predictiveReservation, ttft)
		}
	}
	if legacyQoS {
		r.Header.Set("X-PIG-Lane", ln.Name())
		r.Header.Set("X-PIG-Tier", tier.String())
	} else {
		r.Header.Del("X-PIG-Lane")
		r.Header.Del("X-PIG-Tier")
	}
	if hasOutputTokens {
		r.Header.Set("X-PIG-Output-Tokens", strconv.Itoa(outputTokens))
	}
	started := time.Now()
	var result proxyResult
	if classification.Streaming {
		allowEarlyBridge := s.cfg.SSEEarlyBridgeEnabled && s.safeForEarlySSEBridge(r, outputTokens, hasOutputTokens)
		result = s.proxyStreamingRequest(backend, w, r, allowEarlyBridge, requestStart, semanticCallbacks)
	} else {
		result = s.proxyRequest(backend, w, r)
	}
	if result.timedOut {
		predictiveCause = runtimepredictive.TerminalTimeout
	} else if result.status == clientClosedRequestStatus {
		predictiveCause = runtimepredictive.TerminalClientDisconnected
	} else if result.proxyFailed {
		predictiveCause = runtimepredictive.TerminalUpstreamFailure
	} else if result.status >= http.StatusOK && result.status < http.StatusMultipleChoices {
		predictiveCause = runtimepredictive.TerminalCompleted
	} else {
		predictiveCause = runtimepredictive.TerminalUpstreamFailure
	}
	elapsed := time.Since(started)
	s.observeProxyResult(result)
	s.observeInternalOverhead(time.Since(requestStart), queueWait, result.total)
	ln.ObserveComplete(result.status, elapsed)
	s.globalLn.ObserveComplete(result.status, elapsed)
}

func (s *proxyServer) reject(w http.ResponseWriter, ln *qosLane, code string) {
	s.total429.Add(1)
	openai.WriteTooManyRequests(w)
}
