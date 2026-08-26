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
)

const (
	predictivePolicyAPIPath          = "/admin/v1/predictive-policy"
	predictivePolicyAPISchema        = "pig.predictive-policy.v1"
	predictivePolicyMaximumBodyBytes = 4096
	policyPersistenceContract        = "restart_restores_startup"
)

type predictivePolicyPatch struct {
	ExpectedRevision  *uint64  `json:"expected_revision"`
	TPSReference      *float64 `json:"tps_reference"`
	WindowConcurrency *int64   `json:"window_concurrency"`
	RunningLimit      *int64   `json:"running_limit"`
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
	TPSReference      float64 `json:"tps_reference"`
	WindowConcurrency int64   `json:"window_concurrency"`
	RunningLimit      int64   `json:"running_limit"`
}

type predictivePolicyEffective struct {
	AdmissionMode             string `json:"admission_mode"`
	ObservationPollIntervalMS int64  `json:"observation_poll_interval_ms"`
	MaximumMetricsAgeMS       int64  `json:"maximum_metrics_age_ms"`
	RunningLimitSource        string `json:"running_limit_source"`
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
	writePredictivePolicyDocument(w, predictivePolicyDocumentFrom(s.cfg, telemetry.Capacity.Policy))
}

func (s *proxyServer) patchPredictivePolicy(w http.ResponseWriter, r *http.Request) {
	patch, status, err := decodePredictivePolicyPatch(w, r)
	if err != nil {
		s.policyUpdates.invalid.Add(1)
		logPredictivePolicyUpdate("invalid", 0, coreadmission.PolicySnapshot{}, false)
		writePredictivePolicyError(w, status, "invalid_request", err.Error())
		return
	}
	service, ok := s.admission.(admissionPolicyService)
	if !ok {
		s.policyUpdates.failed.Add(1)
		logPredictivePolicyUpdate("failed", *patch.ExpectedRevision, coreadmission.PolicySnapshot{}, false)
		writePredictivePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", "predictive policy is unavailable")
		return
	}
	result, updateErr := updateAdmissionPolicy(service, coreadmission.PolicyUpdate{
		ExpectedRevision:  *patch.ExpectedRevision,
		TPSReference:      patch.TPSReference,
		WindowConcurrency: patch.WindowConcurrency,
		RunningLimit:      patch.RunningLimit,
		UpdatedAt:         time.Now(),
	})
	if updateErr != nil {
		s.writePredictivePolicyUpdateError(w, patch, result, updateErr)
		return
	}
	s.policyUpdates.applied.Add(1)
	logPredictivePolicyUpdate(
		"applied",
		*patch.ExpectedRevision,
		result.Policy,
		result.TPSWindowReset,
	)
	writePredictivePolicyDocument(w, predictivePolicyDocumentFrom(s.cfg, result.Policy))
}

func updateAdmissionPolicy(
	service admissionPolicyService,
	update coreadmission.PolicyUpdate,
) (result coreadmission.PolicyUpdateResult, err error) {
	if service == nil {
		return result, coreadmission.ErrPolicyUnavailable
	}
	defer func() {
		if recover() != nil {
			result = coreadmission.PolicyUpdateResult{}
			err = coreadmission.ErrPolicyUnavailable
		}
	}()
	return service.UpdatePolicy(update)
}

func (s *proxyServer) writePredictivePolicyUpdateError(
	w http.ResponseWriter,
	patch predictivePolicyPatch,
	result coreadmission.PolicyUpdateResult,
	updateErr error,
) {
	if errors.Is(updateErr, coreadmission.ErrPolicyRevisionConflict) {
		s.policyUpdates.conflict.Add(1)
		logPredictivePolicyUpdate("conflict", *patch.ExpectedRevision, result.Policy, false)
		writePredictivePolicyError(w, http.StatusConflict, "revision_conflict", "expected_revision is stale")
		return
	}
	if errors.Is(updateErr, coreadmission.ErrPolicyInvalid) {
		s.policyUpdates.invalid.Add(1)
		logPredictivePolicyUpdate("invalid", *patch.ExpectedRevision, result.Policy, false)
		writePredictivePolicyError(w, http.StatusBadRequest, "invalid_request", "predictive policy update is invalid")
		return
	}
	s.policyUpdates.failed.Add(1)
	logPredictivePolicyUpdate("failed", *patch.ExpectedRevision, result.Policy, false)
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
	if patch.ExpectedRevision == nil || *patch.ExpectedRevision == 0 ||
		(patch.TPSReference == nil && patch.WindowConcurrency == nil && patch.RunningLimit == nil) {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("expected_revision and at least one mutable policy field are required")
	}
	if patch.TPSReference != nil && (math.IsNaN(*patch.TPSReference) || math.IsInf(*patch.TPSReference, 0) ||
		*patch.TPSReference < 0 || *patch.TPSReference > 1_000_000) {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("tps_reference must be finite and in [0, 1000000]")
	}
	if patch.WindowConcurrency != nil && (*patch.WindowConcurrency <= 0 || *patch.WindowConcurrency > maximumPredictiveSequenceBound) {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("window_concurrency must be in [1, %d]", maximumPredictiveSequenceBound)
	}
	if patch.RunningLimit != nil && (*patch.RunningLimit < 0 || *patch.RunningLimit > maximumPredictiveSequenceBound) {
		return predictivePolicyPatch{}, http.StatusBadRequest, fmt.Errorf("running_limit must be in [0, %d]", maximumPredictiveSequenceBound)
	}
	return patch, 0, nil
}

func predictivePolicyDocumentFrom(
	cfg config,
	policy coreadmission.PolicySnapshot,
) predictivePolicyDocument {
	document := predictivePolicyDocument{
		SchemaVersion: predictivePolicyAPISchema,
		Revision:      policy.Revision,
		Source:        predictivePolicySource(policy),
		Persistence:   policyPersistenceContract,
		Mutable: predictivePolicyMutable{
			TPSReference:      policy.TPSReference,
			WindowConcurrency: policy.WindowConcurrency,
			RunningLimit:      policy.RunningLimit,
		},
		Effective: predictivePolicyEffective{
			AdmissionMode:             cfg.PredictiveAdmissionMode,
			ObservationPollIntervalMS: cfg.PredictiveObservationPollInterval.Milliseconds(),
			MaximumMetricsAgeMS:       cfg.PredictiveMaximumMetricsAge.Milliseconds(),
			RunningLimitSource:        string(policy.RunningLimitSource),
		},
	}
	if !policy.UpdatedAt.IsZero() {
		updatedAt := policy.UpdatedAt.UTC()
		document.UpdatedAt = &updatedAt
	}
	return document
}

func predictivePolicySource(policy coreadmission.PolicySnapshot) string {
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
	policy coreadmission.PolicySnapshot,
	windowReset bool,
) {
	log.Printf(
		"level=info component=policy event=update result=%s expected_revision=%d revision=%d tps_reference=%.6f window_concurrency=%d running_limit=%d running_limit_source=%s tps_window_reset=%t",
		result,
		expectedRevision,
		policy.Revision,
		policy.TPSReference,
		policy.WindowConcurrency,
		policy.RunningLimit,
		policy.RunningLimitSource,
		windowReset,
	)
}
