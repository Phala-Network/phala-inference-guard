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
	s.writeRouterMetricsContract(w)
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

func (s *proxyServer) writeRouterMetricsContract(w io.Writer) {
	snapshot := s.admissionTelemetry(time.Now())
	projection := projectAdmissionCapacity(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, snapshot.Report)
	compatibility := projectRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, projection)
	applied := projection.Active && compatibility.GlobalLimit > 0

	fmt.Fprintf(w, "pig_dynamic_observed_running %d\n", compatibility.ObservedRunning)
	fmt.Fprintf(w, "pig_dynamic_observed_waiting %d\n", compatibility.ObservedWaiting)
	fmt.Fprintf(w, "pig_dynamic_global_limit %d\n", compatibility.GlobalLimit)
	fmt.Fprintf(w, "pig_predictive_admission_enforce %d\n", boolMetric(s.cfg.PredictiveAdmissionMode == "enforce"))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_applied %d\n", boolMetric(applied))
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
	fmt.Fprintf(w, "pig_route_not_allowed_total %d\n", s.routeNotAllowed.Load())
	policy := snapshot.Capacity.Policy
	fmt.Fprintf(w, "pig_predictive_policy_revision %d\n", policy.Revision)
	fmt.Fprintf(w, "pig_predictive_backend_runtime_epoch %d\n", snapshot.Capacity.RuntimeEpoch)
	fmt.Fprintf(w, "pig_predictive_policy_last_updated_at_seconds %.6f\n", predictivePolicyUnixSeconds(policy.UpdatedAt))
	fmt.Fprintf(w, "pig_predictive_policy_updates_total{result=%q} %d\n", "applied", s.policyUpdates.applied.Load())
	fmt.Fprintf(w, "pig_predictive_policy_updates_total{result=%q} %d\n", "invalid", s.policyUpdates.invalid.Load())
	fmt.Fprintf(w, "pig_predictive_policy_updates_total{result=%q} %d\n", "conflict", s.policyUpdates.conflict.Load())
	fmt.Fprintf(w, "pig_predictive_policy_updates_total{result=%q} %d\n", "failed", s.policyUpdates.failed.Load())
	fmt.Fprintf(w, "pig_client_protocol_errors_total{reason=%q} %d\n", "invalid_json", s.clientProtocolInvalidJSON.Load())
	fmt.Fprintf(w, "pig_predictive_scanner_inflight %d\n", s.requestClassifier.Inflight())
	fmt.Fprintf(w, "pig_predictive_scanner_reserved_body_bytes %d\n", s.requestClassifier.ReservedBodyBytes())
	fmt.Fprintf(w, "pig_predictive_scanner_saturated_total %d\n", s.requestClassifier.Rejected())
	writeRequestEvidenceMetrics(w, s.requestEvidence.Snapshot())
	writeResponseUsageEvidenceMetrics(w, s.responseUsageEvidence.Snapshot())
	metrics.WriteBackends(w, s.backendMetricsInput(snapshot, now))
	metrics.WritePredictiveAdmission(w, input)
	writeAdmissionEvidenceMetrics(w, snapshot.Report.Evidence)
	writeTPSDecisionEvidenceMetrics(w, snapshot.Report.TPSEvidence)
	writeTPSDenominatorEvidenceMetrics(w, snapshot.Capacity.State.TPS.Denominator)
	writeWindowConcurrencyHistogram(w, snapshot.Capacity.WindowConcurrencyHistogram)
	metrics.WriteRouterCapacityCompatibility(w, compatibility)
}

func predictivePolicyUnixSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.UnixNano()) / float64(time.Second)
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
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
		ShapeScanDuration:  &s.shapeScanDuration,
		PreForwardDuration: &s.decisionDuration,
	}
	snapshot := s.admissionTelemetry(now)
	report := snapshot.Report
	input.Attempts = report.Attempts
	input.Fits = report.Admitted
	input.Risks = report.RequestProtected + report.LoadProtected
	input.Unknown = report.AvailabilityProtected
	input.IntakeOpen = snapshot.Capacity.IntakeOpen
	input.Reservations = nonnegativeInt(snapshot.Capacity.State.LiveReservations)
	input.VirtualDecodeSequences = projectedDecodeSequences(snapshot.Capacity.State)
	input.SequenceLiabilities = snapshot.Capacity.State.SequenceLiabilities
	input.ResidualDebts = snapshot.Capacity.State.ResidualDebts
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
	input.AdmissionDemandSource = string(decision.Demand.Source)
	input.AdmissionDecodeSequences = decision.Demand.DecodeSequences
	input.TPSDecisionResult = decision.TPSDecisionResult.String()
	input.TPSDecisionSubreason = decision.TPSDecisionSubreason.String()
	input.AdmissionProjectedRunning = decision.ProjectedRunning
	input.AdmissionProjectedWindowSequences = decision.ProjectedWindowSequences
	input.AdmissionRunning = nonnegativeInt(decision.State.RawRunning)
	input.AdmissionWaiting = nonnegativeInt(decision.State.RawWaiting)
	input.AdmissionEffectiveSequences = projectedDecodeSequences(decision.State)
	input.AdmissionGenerationDelta = decision.State.GenerationDelta
	input.AdmissionPreemptionDelta = decision.State.PreemptionDelta
	input.AdmissionAggregateTPS, input.AdmissionMeanActiveTPS, input.AdmissionMeanActiveTPSValid =
		admissionGenerationTPS(decision.State)
}

func admissionMetricAction(decision coreadmission.DecisionRecord) string {
	if decision.Admitted() {
		return "admit"
	}
	switch decision.Scope {
	case coreadmission.ProtectionRequest:
		return "request_protect"
	case coreadmission.ProtectionLoad:
		return "load_protect"
	default:
		return "availability_protect"
	}
}

func admissionPressureSource(reason coreadmission.Reason) string {
	switch {
	case reason == coreadmission.ReasonTPSReference:
		return "tps"
	case reason == coreadmission.ReasonRunningLimit:
		return "running"
	case reason == coreadmission.ReasonWindowConcurrency:
		return "window"
	case reason == coreadmission.ReasonInvalidRequest:
		return "request"
	case reason != coreadmission.ReasonOpen:
		return "availability"
	default:
		return "none"
	}
}

func admissionGenerationTPS(state coreadmission.ProjectedState) (aggregate, mean float64, meanValid bool) {
	if state.GenerationDelta == 0 || state.PreemptionDelta > 0 || state.ObservationInterval <= 0 {
		return 0, 0, false
	}
	aggregate = float64(state.GenerationDelta) / state.ObservationInterval.Seconds()
	denominator := state.RawRunning
	if state.PreviousRawRunning > denominator {
		denominator = state.PreviousRawRunning
	}
	if denominator < 1 {
		return aggregate, 0, false
	}
	return aggregate, aggregate / float64(denominator), true
}

func projectedDecodeSequences(state coreadmission.ProjectedState) int {
	rawDemand, ok := addNonnegativeForMetrics(state.RawRunning, state.RawWaiting)
	if !ok {
		return int(^uint(0) >> 1)
	}
	rawDemand, ok = addNonnegativeForMetrics(rawDemand, state.UnobservedSequences)
	if !ok {
		return int(^uint(0) >> 1)
	}
	return nonnegativeInt(rawDemand)
}

func applyTPSCapacityMetrics(input *metrics.PredictiveAdmissionInput, capacity coreadmission.CapacitySnapshot) {
	if input == nil {
		return
	}
	snapshot := capacity.State.TPS
	input.TPSReference = snapshot.Reference
	input.TPSWindowReady = snapshot.Ready
	input.TPSWindowQualifiedSamples = snapshot.QualifiedSamples
	input.TPSWindowQualifiedSequenceSamples = snapshot.QualifiedSequenceSamples
	input.TPSWindowQualifiedSequenceSeconds = snapshot.QualifiedSequenceSeconds
	input.TPSWindowAggregate = snapshot.AggregateTPS
	input.TPSWindowMeanActive = snapshot.MeanActiveTPS
	input.TPSLatestQualified = snapshot.Latest.Qualified
	input.TPSLatestAggregate = snapshot.Latest.AggregateTPS
	input.TPSLatestMeanActive = snapshot.Latest.MeanActiveTPS
	input.TPSLatestSequenceSeconds = snapshot.Latest.SequenceSeconds
	input.TPSUnobservedSequences = capacity.State.UnobservedSequences
	input.CapacityProjectedRunning = capacity.MinimumDecision.ProjectedRunning
	input.CapacityProjectedWindowSequences = capacity.MinimumDecision.ProjectedWindowSequences
	input.AdmissionRunningLimit = capacity.Policy.RunningLimit
	input.AdmissionRunningLimitSource = string(capacity.Policy.RunningLimitSource)
	input.AdmissionWindowConcurrency = capacity.Policy.WindowConcurrency
}

func writeWindowConcurrencyHistogram(
	w io.Writer,
	snapshot coreadmission.WindowConcurrencyHistogramSnapshot,
) {
	for _, bucket := range snapshot.Buckets {
		fmt.Fprintf(w, "pig_predictive_window_concurrency_observed_bucket{le=%q} %d\n", fmt.Sprint(bucket.UpperBound), bucket.CumulativeCount)
	}
	fmt.Fprintf(w, "pig_predictive_window_concurrency_observed_bucket{le=%q} %d\n", "+Inf", snapshot.Count)
	fmt.Fprintf(w, "pig_predictive_window_concurrency_observed_count %d\n", snapshot.Count)
	fmt.Fprintf(w, "pig_predictive_window_concurrency_observed_sum %d\n", snapshot.Sum)
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
	aggregateTPS, _, _ := admissionGenerationTPS(capacity.State)
	status := runtimebackend.Runtime{
		Name: "upstream", BackendKind: snapshot.BackendKind,
		Running: nonnegativeInt(observation.Running),
		Waiting: nonnegativeInt(observation.Waiting), GenerationTPS: aggregateTPS,
		GenerationTPSValid: fresh && aggregateTPS > 0, Updated: observation.ObservedAt,
		Failed: !fresh,
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
