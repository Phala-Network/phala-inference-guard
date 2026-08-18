package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"strconv"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	predictivePolicyAPIPath          = "/admin/v1/predictive-policy"
	predictivePolicyAPISchema        = "pig.predictive-policy.v1"
	predictivePolicyMaximumBodyBytes = 4096
	policyPersistenceContract        = "restart_restores_startup"
)

type predictivePolicyPatch struct {
	ExpectedRevision *uint64  `json:"expected_revision"`
	TPSReference     *float64 `json:"tps_reference"`
}

type predictivePolicyDocument struct {
	SchemaVersion string                    `json:"schema_version"`
	Revision      uint64                    `json:"revision"`
	Source        string                    `json:"source"`
	Persistence   string                    `json:"persistence"`
	UpdatedAt     *time.Time                `json:"updated_at"`
	Mutable       predictivePolicyMutable   `json:"mutable"`
	Effective     predictivePolicyEffective `json:"effective"`
}

type predictivePolicyMutable struct {
	TPSReference float64 `json:"tps_reference"`
}

type predictivePolicyEffective struct {
	AdmissionMode                string `json:"admission_mode"`
	ObservationPollIntervalMS    int64  `json:"observation_poll_interval_ms"`
	MaximumMetricsAgeMS          int64  `json:"maximum_metrics_age_ms"`
	KVCapacityTokens             int64  `json:"kv_capacity_tokens"`
	KVBlockSize                  int64  `json:"kv_block_size"`
	KVHardLimitTokens            int64  `json:"kv_hard_limit_tokens"`
	MaximumAdmissibleInputTokens int64  `json:"maximum_admissible_input_tokens"`
	PrefillRegularTokens         int64  `json:"prefill_regular_tokens"`
	PrefillExclusiveTokens       int64  `json:"prefill_exclusive_tokens"`
	PrefillQuiescentTokens       int64  `json:"prefill_quiescent_tokens"`
	PrefillAggregateBudgetTokens int64  `json:"prefill_aggregate_budget_tokens"`
	PrefillContendedBudgetTokens int64  `json:"prefill_contended_budget_tokens"`
}

type predictivePolicyErrorDocument struct {
	Error predictivePolicyError `json:"error"`
}

type predictivePolicyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *proxyServer) predictivePolicyAPI(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writePredictivePolicyError(w, http.StatusUnauthorized, "unauthorized", "valid bearer authentication is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getPredictivePolicy(w)
	case http.MethodPatch:
		s.patchPredictivePolicy(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writePredictivePolicyError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or PATCH")
	}
}

func (s *proxyServer) getPredictivePolicy(w http.ResponseWriter) {
	telemetry := s.admissionTelemetry(time.Now())
	if telemetry.Capacity.Policy.Revision == 0 {
		writePredictivePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", "predictive policy is unavailable")
		return
	}
	writePredictivePolicyDocument(w, predictivePolicyDocumentFrom(
		s.cfg,
		telemetry.Capacity.Policy,
		telemetry.CapabilityProfile,
	))
}

func (s *proxyServer) patchPredictivePolicy(w http.ResponseWriter, r *http.Request) {
	patch, status, err := decodePredictivePolicyPatch(w, r)
	if err != nil {
		s.policyUpdates.invalid.Add(1)
		logPredictivePolicyUpdate("invalid", 0, 0, 0, false)
		writePredictivePolicyError(w, status, "invalid_request", err.Error())
		return
	}
	service, ok := s.admission.(admissionPolicyService)
	if !ok {
		s.policyUpdates.failed.Add(1)
		logPredictivePolicyUpdate("failed", *patch.ExpectedRevision, 0, *patch.TPSReference, false)
		writePredictivePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", "predictive policy is unavailable")
		return
	}
	result, updateErr := updateAdmissionTPSPolicy(service, coreadmission.TPSPolicyUpdate{
		ExpectedRevision: *patch.ExpectedRevision,
		Reference:        *patch.TPSReference,
		UpdatedAt:        time.Now(),
	})
	if updateErr != nil {
		s.writePredictivePolicyUpdateError(w, patch, result, updateErr)
		return
	}
	s.policyUpdates.applied.Add(1)
	logPredictivePolicyUpdate(
		"applied",
		*patch.ExpectedRevision,
		result.Policy.Revision,
		result.Policy.Reference,
		result.WindowReset,
	)
	telemetry := s.admissionTelemetry(time.Now())
	writePredictivePolicyDocument(w, predictivePolicyDocumentFrom(
		s.cfg,
		result.Policy,
		telemetry.CapabilityProfile,
	))
}

func updateAdmissionTPSPolicy(
	service admissionPolicyService,
	update coreadmission.TPSPolicyUpdate,
) (result coreadmission.TPSPolicyUpdateResult, err error) {
	if service == nil {
		return result, coreadmission.ErrTPSPolicyUnavailable
	}
	defer func() {
		if recover() != nil {
			result = coreadmission.TPSPolicyUpdateResult{}
			err = coreadmission.ErrTPSPolicyUnavailable
		}
	}()
	return service.UpdateTPSPolicy(update)
}

func (s *proxyServer) writePredictivePolicyUpdateError(
	w http.ResponseWriter,
	patch predictivePolicyPatch,
	result coreadmission.TPSPolicyUpdateResult,
	updateErr error,
) {
	if errors.Is(updateErr, coreadmission.ErrTPSPolicyRevisionConflict) {
		s.policyUpdates.conflict.Add(1)
		logPredictivePolicyUpdate(
			"conflict", *patch.ExpectedRevision, result.Policy.Revision, result.Policy.Reference, false,
		)
		writePredictivePolicyError(w, http.StatusConflict, "revision_conflict", "expected_revision is stale")
		return
	}
	if errors.Is(updateErr, coreadmission.ErrTPSPolicyInvalid) {
		s.policyUpdates.invalid.Add(1)
		logPredictivePolicyUpdate("invalid", *patch.ExpectedRevision, result.Policy.Revision, *patch.TPSReference, false)
		writePredictivePolicyError(w, http.StatusBadRequest, "invalid_request", "TPS policy update is invalid")
		return
	}
	s.policyUpdates.failed.Add(1)
	logPredictivePolicyUpdate("failed", *patch.ExpectedRevision, result.Policy.Revision, *patch.TPSReference, false)
	writePredictivePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", "predictive policy is unavailable")
}

func decodePredictivePolicyPatch(w http.ResponseWriter, r *http.Request) (predictivePolicyPatch, int, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return predictivePolicyPatch{}, http.StatusUnsupportedMediaType, fmt.Errorf("Content-Type must be application/json")
	}
	if r.ContentLength > predictivePolicyMaximumBodyBytes {
		return predictivePolicyPatch{}, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds %d bytes", predictivePolicyMaximumBodyBytes)
	}
	body := http.MaxBytesReader(w, r.Body, predictivePolicyMaximumBodyBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var patch predictivePolicyPatch
	if err := decoder.Decode(&patch); err != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(err, &maximumBytesError) {
			return predictivePolicyPatch{}, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds %d bytes", predictivePolicyMaximumBodyBytes)
		}
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("request body must be one valid policy object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("request body must contain exactly one policy object")
	}
	if patch.ExpectedRevision == nil || *patch.ExpectedRevision == 0 || patch.TPSReference == nil ||
		math.IsNaN(*patch.TPSReference) || math.IsInf(*patch.TPSReference, 0) ||
		*patch.TPSReference < 0 || *patch.TPSReference > 1_000_000 {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("expected_revision and a finite tps_reference in [0, 1000000] are required")
	}
	return patch, 0, nil
}

func predictivePolicyDocumentFrom(
	cfg config,
	policy coreadmission.TPSPolicySnapshot,
	profile runtimepredictive.BackendCapabilityProfile,
) predictivePolicyDocument {
	document := predictivePolicyDocument{
		SchemaVersion: predictivePolicyAPISchema,
		Revision:      policy.Revision,
		Source:        predictivePolicySource(policy),
		Persistence:   policyPersistenceContract,
		Mutable: predictivePolicyMutable{
			TPSReference: policy.Reference,
		},
		Effective: predictivePolicyEffective{
			AdmissionMode:                cfg.PredictiveAdmissionMode,
			ObservationPollIntervalMS:    cfg.PredictiveObservationPollInterval.Milliseconds(),
			MaximumMetricsAgeMS:          cfg.PredictiveMaximumMetricsAge.Milliseconds(),
			KVCapacityTokens:             profile.KVCapacityTokens,
			KVBlockSize:                  profile.KVBlockSize,
			KVHardLimitTokens:            profile.KVHardLimitTokens,
			MaximumAdmissibleInputTokens: profile.MaximumAdmissibleInputTokens,
			PrefillRegularTokens:         profile.PrefillRegularTokens,
			PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
			PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
			PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
			PrefillContendedBudgetTokens: profile.PrefillContendedBudgetTokens,
		},
	}
	if !policy.UpdatedAt.IsZero() {
		updatedAt := policy.UpdatedAt.UTC()
		document.UpdatedAt = &updatedAt
	}
	return document
}

func predictivePolicySource(policy coreadmission.TPSPolicySnapshot) string {
	if policy.Revision <= 1 || policy.UpdatedAt.IsZero() {
		return "startup"
	}
	return "runtime_api"
}

func writePredictivePolicyDocument(w http.ResponseWriter, document predictivePolicyDocument) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(document.Revision, 10)))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(document)
}

func writePredictivePolicyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(predictivePolicyErrorDocument{
		Error: predictivePolicyError{Code: code, Message: message},
	})
}

func logPredictivePolicyUpdate(
	result string,
	expectedRevision uint64,
	currentRevision uint64,
	reference float64,
	windowReset bool,
) {
	log.Printf(
		"predictive_policy event=update result=%s expected_revision=%d revision=%d tps_reference=%.6f tps_window_reset=%t",
		result,
		expectedRevision,
		currentRevision,
		reference,
		windowReset,
	)
}
