package server

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
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
	now := time.Now()
	input, snapshot := s.predictiveAdmissionMetricsInput(now)
	projection := projectAdmissionCapacity(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, snapshot.Report)
	compatibility := projectRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, projection)
	applyAdmissionRouterMetrics(&input, projection, compatibility)
	fmt.Fprintf(w, "pig_info{version=%q} 1\n", version)
	fmt.Fprintf(w, "pig_uptime_seconds %.6f\n", time.Since(s.started).Seconds())
	fmt.Fprintf(w, "pig_rejected_total %d\n", s.total429.Load())
	fmt.Fprintf(w, "pig_client_protocol_errors_total{reason=%q} %d\n", "invalid_json", s.clientProtocolInvalidJSON.Load())
	fmt.Fprintf(w, "pig_predictive_scanner_inflight %d\n", s.requestClassifier.Inflight())
	fmt.Fprintf(w, "pig_predictive_scanner_saturated_total %d\n", s.requestClassifier.Rejected())
	metrics.WriteBackends(w, s.backendMetricsInput(snapshot, now))
	metrics.WritePredictiveAdmission(w, input)
	metrics.WriteRouterCapacityCompatibility(w, compatibility)
}

func (s *proxyServer) admissionTelemetry(now time.Time) (snapshot admissionTelemetrySnapshot) {
	if s == nil || s.admission == nil {
		return admissionTelemetrySnapshot{}
	}
	defer func() {
		if recover() != nil {
			snapshot = admissionTelemetrySnapshot{}
		}
	}()
	return s.admission.Snapshot(now)
}

func (s *proxyServer) predictiveAdmissionMetricsInput(
	now time.Time,
) (metrics.PredictiveAdmissionInput, admissionTelemetrySnapshot) {
	input := metrics.PredictiveAdmissionInput{
		Mode:               s.cfg.PredictiveAdmissionMode,
		EnforcedRejects:    s.predictiveEnforcedRejects.Load(),
		FailureClose:       s.admissionFailures.close.Load(),
		FailureDecide:      s.admissionFailures.decide.Load(),
		FailureForward:     s.admissionFailures.forward.Load(),
		FailureFirstByte:   s.admissionFailures.firstByte.Load(),
		FailureTerminal:    s.admissionFailures.terminal.Load(),
		BodyReadDuration:   &s.bodyReadDuration,
		EstimatorDuration:  &s.estimatorDuration,
		PreForwardDuration: &s.decisionDuration,
	}
	snapshot := s.admissionTelemetry(now)
	profile := snapshot.CapabilityProfile
	input.CapabilityProfileSource = string(profile.Source)
	input.CapabilityProfileSchema = profile.SchemaVersion
	input.CapabilityInitializationReason = snapshot.CapabilityReason
	input.CapabilityKVCapacityTokens = profile.KVCapacityTokens
	input.CapabilityKVBlockSize = profile.KVBlockSize
	input.CapabilityKVHardLimitTokens = profile.KVHardLimitTokens
	input.CapabilityMaxModelLenTokens = profile.MaxModelLenTokens
	input.CapabilityMaximumAdmissibleInputTokens = profile.MaximumAdmissibleInputTokens
	input.CapabilityPrefillRegularTokens = profile.PrefillRegularTokens
	input.CapabilityPrefillExclusiveTokens = profile.PrefillExclusiveTokens
	input.CapabilityPrefillQuiescentTokens = profile.PrefillQuiescentTokens
	input.CapabilityPrefillContendedBudgetTokens = profile.PrefillContendedBudgetTokens
	input.CapabilityPrefillAggregateBudgetTokens = profile.PrefillAggregateBudgetTokens
	report := snapshot.Report
	input.Attempts = report.Attempts
	input.Fits = report.Admitted
	input.Risks = report.RequestProtected + report.LoadProtected
	input.Unknown = report.AvailabilityProtected
	input.IntakeOpen = snapshot.Capacity.IntakeOpen
	input.Reservations = nonnegativeInt(snapshot.Capacity.State.LiveReservations)
	input.VirtualDecodeSequences = projectedDecodeSequences(snapshot.Capacity.State)
	input.ForwardedPendingPrefills = nonnegativeInt(snapshot.Capacity.State.PendingPrefillSequences)
	input.ForwardedPendingPrefillTokens = snapshot.Capacity.State.PendingPrefillTokens
	input.PredictionDuration = snapshot.PredictionDuration
	applyTPSCapacityMetrics(&input, snapshot.Capacity)
	if report.HasLastDecision {
		applyAdmissionDecisionMetrics(&input, report.LastDecision)
		input.LastReason = string(report.LastDecision.Reason)
		input.LastSource = admissionDecisionSource(report.LastDecision)
	}
	if report.HasLastReject {
		input.LastRejectReason = string(report.LastReject.Reason)
		input.LastRejectSource = admissionDecisionSource(report.LastReject)
		input.LastRejectScope = string(report.LastReject.Scope)
		input.LastRejectAt = report.LastRejectAt
	}
	return input, snapshot
}

func applyAdmissionDecisionMetrics(
	input *metrics.PredictiveAdmissionInput,
	decision coreadmission.DecisionRecord,
) {
	if input == nil {
		return
	}
	input.AdmissionAction = admissionMetricAction(decision)
	input.AdmissionReason = string(decision.Reason)
	input.AdmissionPressureSource = admissionPressureSource(decision.Reason)
	input.AdmissionSelectionInputTokens = decision.Estimate.SelectionInputTokens
	input.AdmissionReservedTokens = decision.Work.TotalKVTokens
	if decision.HardKVLimitTokens >= decision.State.EffectiveKVTokens {
		input.AdmissionAllowanceTokens = decision.HardKVLimitTokens - decision.State.EffectiveKVTokens
	}
	input.AdmissionEffectiveKV = decision.State.EffectiveKVTokens
	input.AdmissionPostAdmitKV = decision.PostAdmitKVTokens
	input.AdmissionRemainingKV = decision.RemainingKVTokens
	input.AdmissionRunning = nonnegativeInt(decision.State.RawRunning)
	input.AdmissionWaiting = nonnegativeInt(decision.State.RawWaiting)
	input.AdmissionEffectiveSequences = projectedDecodeSequences(decision.State)
	input.AdmissionAggregateTPS, input.AdmissionMeanActiveTPS = admissionGenerationTPS(decision.State)
	input.AdmissionPrefillClass = string(decision.PrefillClass)
	input.AdmissionEstimatedPrefillTokens = decision.Estimate.SelectionInputTokens
	input.AdmissionPendingPrefillSequences = nonnegativeInt(decision.State.PendingPrefillSequences)
	input.AdmissionPendingPrefillTokens = decision.PendingPrefillTokensBefore
	input.AdmissionPostAdmitPendingPrefillTokens = decision.PendingPrefillTokensAfter
	input.AdmissionPendingExclusiveSequences = nonnegativeInt(decision.State.PendingExclusiveSequences)
	input.AdmissionPendingQuiescentSequences = nonnegativeInt(decision.State.PendingQuiescentSequences)
}

func admissionMetricAction(decision coreadmission.DecisionRecord) string {
	if decision.Admitted() {
		return "admit"
	}
	if decision.Scope == coreadmission.ProtectionRequest {
		return "size_protect"
	}
	return "hard_protect"
}

func admissionPressureSource(reason coreadmission.Reason) string {
	switch reason {
	case coreadmission.ReasonPrefillContention,
		coreadmission.ReasonPrefillBudget,
		coreadmission.ReasonPrefillExclusive,
		coreadmission.ReasonPrefillQuiescent:
		return "prefill"
	case coreadmission.ReasonTPSReference:
		return "tps"
	default:
		return "none"
	}
}

func admissionGenerationTPS(state coreadmission.ProjectedState) (aggregate, mean float64) {
	if state.GenerationDelta == 0 || state.PreemptionDelta > 0 || state.ObservationInterval <= 0 {
		return 0, 0
	}
	aggregate = float64(state.GenerationDelta) / state.ObservationInterval.Seconds()
	denominator := state.RawRunning
	if state.PreviousRawRunning > denominator {
		denominator = state.PreviousRawRunning
	}
	if denominator < 1 {
		denominator = 1
	}
	return aggregate, aggregate / float64(denominator)
}

func projectedDecodeSequences(state coreadmission.ProjectedState) int {
	tracked, ok := addNonnegativeForMetrics(state.PendingPrefillSequences, state.LocalActiveDecode)
	if !ok {
		return int(^uint(0) >> 1)
	}
	if state.RawRunning > tracked {
		tracked = state.RawRunning
	}
	return nonnegativeInt(tracked)
}

func applyTPSCapacityMetrics(input *metrics.PredictiveAdmissionInput, capacity coreadmission.CapacitySnapshot) {
	if input == nil {
		return
	}
	snapshot := capacity.State.TPS
	input.TPSReference = snapshot.Reference
	input.TPSWindowReady = snapshot.Ready
	input.TPSWindowQualifiedSamples = snapshot.QualifiedSamples
	input.TPSWindowQualifiedSequenceSeconds = snapshot.QualifiedSequenceSeconds
	input.TPSWindowAggregate = snapshot.AggregateTPS
	input.TPSWindowMeanActive = snapshot.MeanActiveTPS
	input.TPSSequenceLimit = capacity.MinimumDecision.TPSSequenceLimit
	input.TPSCurrentSequences = capacity.MinimumDecision.TPSCurrentSequences
	input.TPSPostAdmitSequences = capacity.MinimumDecision.TPSPostAdmitSequences
}

func addNonnegativeForMetrics(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > int64(^uint64(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}

func (s *proxyServer) writeAdmissionAndRouterMetrics(w io.Writer) {
	now := time.Now()
	input, snapshot := s.predictiveAdmissionMetricsInput(now)
	projection := projectAdmissionCapacity(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, snapshot.Report)
	compatibility := projectRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, projection)
	applyAdmissionRouterMetrics(&input, projection, compatibility)
	metrics.WritePredictiveAdmission(w, input)
	metrics.WriteRouterCapacityCompatibility(w, compatibility)
}

func applyAdmissionRouterMetrics(
	input *metrics.PredictiveAdmissionInput,
	projection admissionCapacityProjection,
	compatibility metrics.RouterCapacityCompatibility,
) {
	if input == nil {
		return
	}
	input.RouterBackpressure = metrics.PredictiveRouterBackpressureInput{
		Active:               projection.Active,
		Scope:                string(projection.Scope),
		MinimumRunning:       nonnegativeInt(projection.MinimumRunning),
		InspectCapacity:      nonnegativeInt(projection.InspectCapacity),
		Applied:              compatibility.GlobalLimit > 0 && projection.Active,
		Reason:               string(projection.Reason),
		Source:               projection.Source,
		LatestRejectAt:       projection.LatestRejectAt,
		PredictiveRunning:    compatibility.ObservedRunning,
		RawRunning:           compatibility.ObservedRunningRaw,
		EffectiveRunning:     compatibility.ObservedRunning,
		RawGlobalLimit:       compatibility.GlobalLimitRaw,
		EffectiveGlobalLimit: compatibility.GlobalLimit,
	}
}

func (s *proxyServer) backendMetricsInput(
	snapshot admissionTelemetrySnapshot,
	now time.Time,
) []metrics.BackendSnapshot {
	if s.backend == nil {
		return nil
	}
	stats := s.backend.Stats()
	capacity := snapshot.Capacity
	observation := capacity.Observation
	fresh := capacity.IntakeOpen && capacity.HasObservation && !now.IsZero() &&
		!now.Before(observation.ObservedAt) && now.Sub(observation.ObservedAt) <= observation.MaximumAge
	aggregateTPS, _ := admissionGenerationTPS(capacity.State)
	availableKV := observation.KVCapacityTokens - observation.UsedKVTokens
	if availableKV < 0 {
		availableKV = 0
	}
	status := runtimebackend.Runtime{
		Name: "upstream", BackendKind: "vllm", KVCapacityTokens: observation.KVCapacityTokens,
		KVUsedTokens: observation.UsedKVTokens, KVAvailableTokens: availableKV,
		KVTokenMetricsValid: fresh, Running: nonnegativeInt(observation.Running),
		Waiting: nonnegativeInt(observation.Waiting), GenerationTPS: aggregateTPS,
		GenerationTPSValid: fresh && aggregateTPS > 0, Updated: observation.ObservedAt,
		Failed: !fresh,
	}
	if observation.KVCapacityTokens > 0 {
		status.KVCacheUsage = float64(observation.UsedKVTokens) / float64(observation.KVCapacityTokens)
	}
	return []metrics.BackendSnapshot{{
		Name: "upstream", Upstream: s.cfg.Upstream,
		Stats: metrics.BackendStats{
			Inflight: stats.Inflight, Accepted: stats.Accepted, Completed: stats.Completed,
			Failed: stats.Failed, ProxyErrs: stats.ProxyErrs, CopyErrs: stats.CopyErrs,
		},
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
