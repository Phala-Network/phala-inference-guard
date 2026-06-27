package server

import (
	requesttier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	if attestationReportPath(r.URL.Path) {
		s.attestationReport(w, r)
		return
	}
	if s.requiresAPIAuth(r) && !s.authorized(r) {
		openai.WriteUnauthorized(w)
		return
	}
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
	ln, outputTokens, hasOutputTokens := s.classifyRequest(r)
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
		if qosReject == "backend_unavailable" {
			s.unavailable(w, qosReject)
			return
		}
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
		s.unavailable(w, "backend_unavailable")
		return
	}
	if !s.priorityInjector.Inject(r, tier) {
		s.qosGate.ObserveReject(ln, tier, "backend_priority_injection")
		s.reject(w, ln, "backend_priority_injection")
		return
	}
	s.globalLn.ObserveAccepted()
	ln.ObserveAccepted()
	s.qosGate.ObserveAccepted(tier)
	prefillGrace := s.prefillGraceDuration(r)
	doneActive := s.trackActiveRequest(prefillGrace)
	defer doneActive()
	r.Header.Set("X-PIG-Lane", ln.Name())
	r.Header.Set("X-PIG-Tier", tier.String())
	if hasOutputTokens {
		r.Header.Set("X-PIG-Output-Tokens", strconv.Itoa(outputTokens))
	}
	started := time.Now()
	var result proxyResult
	if s.wantsStreamingResponse(r) {
		allowEarlyBridge := s.cfg.SSEEarlyBridgeEnabled && s.safeForEarlySSEBridge(r, outputTokens, hasOutputTokens)
		result = s.proxyStreamingRequest(backend, w, r, allowEarlyBridge, requestStart)
	} else {
		result = s.proxyRequest(backend, w, r)
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
