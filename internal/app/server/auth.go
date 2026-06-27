package server

import (
	"net/http"
	"strings"

	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

func (s *proxyServer) requiresAPIAuth(r *http.Request) bool {
	if !s.cfg.APIAuthEnabled {
		return false
	}
	return requestclass.AdmittedPath(r, requestclass.PathConfig{
		Paths:       s.cfg.APIAuthPaths,
		SuffixMatch: s.cfg.PathSuffixMatch,
	})
}

func attestationReportPath(path string) bool {
	return path == "/v1/attestation/report" || path == "/attestation/report"
}

func signaturePath(path string) bool {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 2 && parts[0] == "signature" && parts[1] != "" {
		return true
	}
	return len(parts) == 3 && parts[0] == "v1" && parts[1] == "signature" && parts[2] != ""
}
