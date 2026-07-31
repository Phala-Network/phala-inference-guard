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
	predictiveReservation := s.decidePredictiveShadow(r.Context(), predictiveShadowInput{
		Path:            r.URL.Path,
		Body:            classification.PredictiveBody,
		OutputTokens:    classification.PredictiveOutputTokens,
		HasOutputTokens: classification.PredictiveHasOutputTokens,
		Streaming:       classification.Streaming,
	})
	predictiveCause := runtimepredictive.TerminalClientCancelled
	if predictiveReservation != nil {
		defer func() { predictiveReservation.Terminate(predictiveCause) }()
	}
	if s.kvShadow != nil {
		s.kvEstimatorDuration.Observe(time.Since(estimatorStart))
	}
	releaseKVShadow := s.shadowKVRequest(classification.KVCost)
	defer releaseKVShadow()
	ln := classification.Lane
	outputTokens := classification.OutputTokens
	hasOutputTokens := classification.HasOutputTokens
	tier := requesttier.FromHeader(r)
	ln.ObserveBody(r.ContentLength)
	s.globalLn.ObserveBody(r.ContentLength)
	releaseQoS, qosReject, queueWait := s.qosGate.WaitAcquire(r.Context(), ln, tier)
	decisionElapsed := time.Since(decisionStart) - queueWait
	if decisionElapsed < 0 {
		decisionElapsed = 0
	}
	s.decisionDuration.Observe(decisionElapsed)
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
	defer releaseQoS()
	backend := s.chooseBackend()
	if backend == nil {
		predictiveCause = runtimepredictive.TerminalUpstreamFailure
		s.unavailable(w, "backend_unavailable")
		return
	}
	if !s.priorityInjector.Inject(r, tier) {
		predictiveCause = runtimepredictive.TerminalLocalQoSReject
		s.qosGate.ObserveReject(ln, tier, "backend_priority_injection")
		s.reject(w, ln, "backend_priority_injection")
		return
	}
	s.globalLn.ObserveAccepted()
	ln.ObserveAccepted()
	s.qosGate.ObserveAccepted(tier)
	prefillGrace := s.prefillGraceDuration(r, classification.Streaming)
	markDecode, doneActive := s.trackActiveRequest(prefillGrace)
	defer doneActive()
	markSemanticOutput := markDecode
	if predictiveReservation != nil {
		markSemanticOutput = func() {
			markDecode()
			predictiveReservation.MarkPrefillComplete()
		}
	}
	r.Header.Set("X-PIG-Lane", ln.Name())
	r.Header.Set("X-PIG-Tier", tier.String())
	if hasOutputTokens {
		r.Header.Set("X-PIG-Output-Tokens", strconv.Itoa(outputTokens))
	}
	started := time.Now()
	var result proxyResult
	if classification.Streaming {
		allowEarlyBridge := s.cfg.SSEEarlyBridgeEnabled && s.safeForEarlySSEBridge(r, outputTokens, hasOutputTokens)
		result = s.proxyStreamingRequest(backend, w, r, allowEarlyBridge, requestStart, markSemanticOutput)
	} else {
		result = s.proxyRequest(backend, w, r)
	}
	if result.timedOut {
		predictiveCause = runtimepredictive.TerminalTimeout
	} else if result.status == clientClosedRequestStatus {
		predictiveCause = runtimepredictive.TerminalClientDisconnected
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
