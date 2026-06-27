package server

import (
	"net/http"

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
	return path == "/v1/attestation/report"
}
