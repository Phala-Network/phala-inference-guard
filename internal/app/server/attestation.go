package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
)

func (s *proxyServer) attestationReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		openai.WriteUnauthorized(w)
		return
	}
	if s.attestation == nil {
		http.NotFound(w, r)
		return
	}
	version := 1
	if rawVersion := r.URL.Query().Get("version"); rawVersion != "" {
		parsed, err := strconv.Atoi(rawVersion)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "Unsupported attestation report version: "+rawVersion)
			return
		}
		version = parsed
	}
	report, err := s.attestation.Generate(r.Context(), attestation.ReportRequest{
		SigningAlgo: r.URL.Query().Get("signing_algo"),
		NonceHex:    r.URL.Query().Get("nonce"),
		Version:     version,
	})
	if err != nil {
		var httpErr attestation.HTTPError
		if errors.As(err, &httpErr) {
			writeJSONError(w, httpErr.Status, httpErr.Message)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": message})
}
