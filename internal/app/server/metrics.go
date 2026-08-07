package server

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
)

func (s *proxyServer) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+s.cfg.Token)) == 1
}

func (s *proxyServer) metrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.writeLocalMetrics(w)
}

func (s *proxyServer) combinedMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.writeLocalMetrics(w)
	_, _ = io.WriteString(w, "\n# --- Backend Metrics ---\n")
	s.writeBackendMetricsRaw(w)
}

func (s *proxyServer) writeLocalMetrics(w io.Writer) {
	predictiveInput, predictiveSnapshot := s.predictiveAdmissionMetricsInput()
	compatibility := predictiveRouterCompatibility(s.cfg.PredictiveAdmissionMode, predictiveSnapshot)
	applyPredictiveRouterCompatibility(&predictiveInput, predictiveSnapshot, compatibility)
	fmt.Fprintf(w, "pig_info{version=%q} 1\n", version)
	fmt.Fprintf(w, "pig_uptime_seconds %.6f\n", time.Since(s.started).Seconds())
	fmt.Fprintf(w, "pig_rejected_total %d\n", s.total429.Load())
	fmt.Fprintf(w, "pig_client_protocol_errors_total{reason=%q} %d\n", "invalid_json", s.clientProtocolInvalidJSON.Load())
	fmt.Fprintf(w, "pig_predictive_scanner_inflight %d\n", s.requestClassifier.Inflight())
	fmt.Fprintf(w, "pig_predictive_scanner_saturated_total %d\n", s.requestClassifier.Rejected())
	metrics.WriteBackends(w, s.backendMetricsInput(predictiveSnapshot.Observer))
	metrics.WritePredictiveAdmission(w, predictiveInput)
	metrics.WriteRouterCapacityCompatibility(w, compatibility)
}

func (s *proxyServer) predictiveAdmissionMetricsInput() (metrics.PredictiveAdmissionInput, predictiveAdmissionTelemetrySnapshot) {
	input := metrics.PredictiveAdmissionInput{
		Mode:              s.cfg.PredictiveAdmissionMode,
		EnforcedRejects:   s.predictiveEnforcedRejects.Load(),
		FailureClose:      s.predictiveShadowFailures.close.Load(),
		FailureDecide:     s.predictiveShadowFailures.decide.Load(),
		FailureForward:    s.predictiveShadowFailures.forward.Load(),
		FailurePrefill:    s.predictiveShadowFailures.prefill.Load(),
		FailureTerminal:   s.predictiveShadowFailures.terminal.Load(),
		EstimatorDuration: &s.estimatorDuration,
	}
	provider, ok := s.predictiveShadow.(predictiveAdmissionTelemetryProvider)
	if !ok {
		return input, predictiveAdmissionTelemetrySnapshot{}
	}
	snapshot := provider.PredictiveAdmissionTelemetry()
	input.CapabilityProfileSource = string(snapshot.CapabilityProfile.Source)
	input.CapabilityProfileSchema = snapshot.CapabilityProfile.SchemaVersion
	input.CapabilityInitializationReason = snapshot.CapabilityReason
	input.CapabilityKVCapacityTokens = snapshot.CapabilityProfile.KVCapacityTokens
	input.CapabilityKVBlockSize = snapshot.CapabilityProfile.KVBlockSize
	input.CapabilityKVSoftLimitTokens = snapshot.CapabilityProfile.KVSoftLimitTokens
	input.CapabilityKVHardLimitTokens = snapshot.CapabilityProfile.KVHardLimitTokens
	input.CapabilitySafeColdPrefillTokensPerSecond = snapshot.CapabilityProfile.SafeColdPrefillTokensPerSec
	input.CapabilityPrefillRegularTokens = snapshot.CapabilityProfile.PrefillRegularTokens
	input.CapabilityPrefillExclusiveTokens = snapshot.CapabilityProfile.PrefillExclusiveTokens
	input.CapabilityPrefillQuiescentTokens = snapshot.CapabilityProfile.PrefillQuiescentTokens
	input.CapabilityPrefillAggregateBudgetTokens = snapshot.CapabilityProfile.PrefillAggregateBudgetTokens
	input.Attempts = snapshot.Attempts.Attempts
	input.Fits = snapshot.Attempts.Fits
	input.Risks = snapshot.Attempts.Risks
	input.Unknown = snapshot.Attempts.Unknown
	input.LastReason = string(snapshot.Attempts.LastReason)
	input.LastSource = string(snapshot.Attempts.LastSource)
	input.LastRejectReason = string(snapshot.Attempts.LastRejectReason)
	input.LastRejectSource = string(snapshot.Attempts.LastRejectSource)
	input.LastRejectScope = string(snapshot.Attempts.LastRejectScope)
	input.LastRejectAt = snapshot.Attempts.LastRejectAt
	input.IntakeOpen = snapshot.Manager.IntakeOpen
	input.Reservations = snapshot.Manager.Reservations
	input.VirtualDecodeSequences = snapshot.Manager.Virtual.Upper.DecodeSequences
	input.ForwardedPendingPrefills = snapshot.Manager.ForwardedPendingPrefills
	input.ForwardedPendingPrefillTokens = snapshot.Manager.ForwardedPendingPrefillTokens
	input.RetiredReservations = snapshot.Manager.RetiredReservations
	input.RetiredEvictions = snapshot.Manager.RetiredEvictions
	input.RequestAwareAction = string(snapshot.RequestAware.Action)
	input.RequestAwareReason = string(snapshot.RequestAware.Reason)
	input.RequestAwarePressureSource = string(snapshot.RequestAware.PressureSource)
	input.RequestAwarePressure = snapshot.RequestAware.Pressure
	input.RequestAwareSelectionInputTokens = snapshot.RequestAware.SelectionInputTokens
	input.RequestAwareReservedTokens = snapshot.RequestAware.ReservedTokens
	input.RequestAwareAllowanceTokens = snapshot.RequestAware.AllowanceTokens
	input.RequestAwareEffectiveKV = snapshot.RequestAware.EffectiveKV
	input.RequestAwarePostAdmitKV = snapshot.RequestAware.PostAdmitKV
	input.RequestAwareRemainingKV = snapshot.RequestAware.RemainingKV
	input.RequestAwareRunning = snapshot.RequestAware.Running
	input.RequestAwareWaiting = snapshot.RequestAware.Waiting
	input.RequestAwareEffectiveSequences = snapshot.RequestAware.EffectiveSequences
	input.RequestAwareAggregateTPSProxy = snapshot.RequestAware.AggregateTPSProxy
	input.RequestAwareMeanActiveTPSProxy = snapshot.RequestAware.MeanActiveTPSProxy
	input.RequestAwareProjectedTPSProxy = snapshot.RequestAware.ProjectedTPSProxy
	input.RequestAwareTPSForecastValid = snapshot.RequestAware.TPSForecastValid
	input.RequestAwarePrefillClass = string(snapshot.RequestAware.PrefillClass)
	input.RequestAwareEstimatedPrefillTokens = snapshot.RequestAware.EstimatedPrefillTokens
	input.RequestAwarePendingPrefillSequences = snapshot.RequestAware.PendingPrefillSequences
	input.RequestAwarePendingPrefillTokens = snapshot.RequestAware.PendingPrefillTokens
	input.RequestAwarePostAdmitPendingPrefillTokens = snapshot.RequestAware.PostAdmitPendingPrefillTokens
	input.RequestAwarePendingLongPrefillSequences = snapshot.RequestAware.PendingLongPrefillSequences
	input.RequestAwarePendingQuiescentPrefillSequences = snapshot.RequestAware.PendingQuiescentPrefillSequences
	input.RequestAwareLastDecisionPendingPrefillSequences = snapshot.RequestAware.LastDecisionPendingPrefillSequences
	input.RequestAwareLastDecisionPendingPrefillTokens = snapshot.RequestAware.LastDecisionPendingPrefillTokens
	input.RequestAwareLastDecisionPostAdmitPendingPrefillTokens = snapshot.RequestAware.LastDecisionPostAdmitPendingPrefillTokens
	input.RequestAwareLastDecisionPendingLongPrefillSequences = snapshot.RequestAware.LastDecisionPendingLongPrefillSequences
	input.RequestAwareLastDecisionPendingQuiescentPrefillSequences = snapshot.RequestAware.LastDecisionPendingQuiescentPrefillSequences
	input.RouterBackpressure = metrics.PredictiveRouterBackpressureInput{
		Active: snapshot.RouterBackpressure.Active, Activation: snapshot.RouterBackpressure.Activation,
		Scope: string(snapshot.RouterBackpressure.Scope), MinimumRunning: snapshot.RouterBackpressure.MinimumRunning,
		InspectCapacity: snapshot.RouterBackpressure.InspectCapacity, Reason: string(snapshot.RouterBackpressure.Reason),
		Source: string(snapshot.RouterBackpressure.Source), ActivatedAt: snapshot.RouterBackpressure.ActivatedAt,
		Activations: snapshot.RouterBackpressure.Activations, LatestRejectAt: snapshot.RouterBackpressure.LatestRejectAt,
	}
	input.PredictionDuration = snapshot.PredictionDuration
	return input, snapshot
}

func (s *proxyServer) writePredictiveAndDynamicMetrics(w io.Writer) {
	input, snapshot := s.predictiveAdmissionMetricsInput()
	compatibility := predictiveRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot)
	applyPredictiveRouterCompatibility(&input, snapshot, compatibility)
	metrics.WritePredictiveAdmission(w, input)
	metrics.WriteRouterCapacityCompatibility(w, compatibility)
}

func applyPredictiveRouterCompatibility(
	input *metrics.PredictiveAdmissionInput,
	snapshot predictiveAdmissionTelemetrySnapshot,
	compatibility metrics.RouterCapacityCompatibility,
) {
	if input == nil {
		return
	}
	input.RouterBackpressure.Applied = compatibility.GlobalLimit > 0 && snapshot.RouterBackpressure.Active
	input.RouterBackpressure.PredictiveRunning = compatibility.ObservedRunning
	input.RouterBackpressure.RawRunning = compatibility.ObservedRunningRaw
	input.RouterBackpressure.EffectiveRunning = compatibility.ObservedRunning
	input.RouterBackpressure.RawGlobalLimit = compatibility.GlobalLimitRaw
	input.RouterBackpressure.EffectiveGlobalLimit = compatibility.GlobalLimit
}

func predictiveRouterCompatibility(mode string, snapshot predictiveAdmissionTelemetrySnapshot) metrics.RouterCapacityCompatibility {
	observer := snapshot.Observer
	value := metrics.RouterCapacityCompatibility{
		ObservedRunningRaw: observer.Running,
		ObservedWaitingRaw: observer.Waiting,
		ObservedRunning:    observer.Running,
		ObservedWaiting:    observer.Waiting,
	}
	if mode != "enforce" {
		return value
	}
	value.ObservedWaiting = 0
	if !snapshot.RouterBackpressure.Active {
		return value
	}
	if value.ObservedRunning < snapshot.RouterBackpressure.MinimumRunning {
		value.ObservedRunning = snapshot.RouterBackpressure.MinimumRunning
	}
	if value.ObservedRunning < 1 {
		value.ObservedRunning = 1
	}
	inspect := snapshot.RouterBackpressure.InspectCapacity
	if inspect < 0 {
		inspect = 0
	}
	value.GlobalLimit = value.ObservedRunning + inspect
	return value
}

func (s *proxyServer) backendMetricsInput(observer predictiveObserverSnapshot) []metrics.BackendSnapshot {
	if s.backend == nil {
		return nil
	}
	stats := s.backend.Stats()
	status := runtimebackend.Runtime{
		Name: "upstream", BackendKind: "vllm", KVCapacityTokens: observer.CapacityTokens,
		KVUsedTokens: observer.UsedTokens, KVAvailableTokens: observer.CapacityTokens - observer.UsedTokens,
		KVTokenMetricsValid: observer.IdentityValid, Running: observer.Running, Waiting: observer.Waiting,
		GenerationTPS: observer.AggregateTPS, GenerationTPSValid: observer.TPSValid, Updated: observer.ObservedAt,
		Failed: !observer.MetricsFresh,
	}
	if observer.CapacityTokens > 0 {
		status.KVCacheUsage = float64(observer.UsedTokens) / float64(observer.CapacityTokens)
	}
	return []metrics.BackendSnapshot{{
		Name: "upstream", Upstream: s.cfg.Upstream,
		Stats:  metrics.BackendStats{Inflight: stats.Inflight, Accepted: stats.Accepted, Completed: stats.Completed, Failed: stats.Failed, ProxyErrs: stats.ProxyErrs, CopyErrs: stats.CopyErrs},
		Status: status,
	}}
}

func (s *proxyServer) writeBackendMetricsRaw(w io.Writer) {
	client := &http.Client{Timeout: s.cfg.PredictiveMetricsRequestTimeout}
	response, err := client.Get(s.cfg.PredictiveMetricsURL)
	if err != nil {
		_, _ = fmt.Fprintf(w, "# failed to fetch backend metrics: %v\n", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(w, "# backend metrics status %d\n", response.StatusCode)
		return
	}
	_, _ = io.Copy(w, io.LimitReader(response.Body, 16*1024*1024))
}
