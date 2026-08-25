package server

import (
	"fmt"
	"log"
	"net/http"
	pathpkg "path"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
)

const (
	chatCompletionsPath = "/v1/chat/completions"
	completionsPath     = "/v1/completions"
	responsesPath       = "/v1/responses"
	modelsPath          = "/v1/models"
)

type routeRejectionClass string

const (
	routeRejectionNonCanonical routeRejectionClass = "noncanonical_path"
	routeRejectionMethod       routeRejectionClass = "method_mismatch"
	routeRejectionUnknown      routeRejectionClass = "unknown_path"
)

// PublicRoutePolicy owns the complete public forwarding surface.
type PublicRoutePolicy struct{}

func (PublicRoutePolicy) Allows(r *http.Request) bool {
	path, ok := canonicalRoutePath(r)
	if !ok {
		return false
	}
	switch r.Method {
	case http.MethodPost:
		return generationPath(path)
	case http.MethodGet:
		return path == modelsPath
	default:
		return false
	}
}

func (PublicRoutePolicy) RejectionClass(r *http.Request) routeRejectionClass {
	path, ok := canonicalRoutePath(r)
	if !ok {
		return routeRejectionNonCanonical
	}
	if generationPath(path) || path == modelsPath {
		return routeRejectionMethod
	}
	return routeRejectionUnknown
}

// AdmissionRoutePolicy owns admission for already-allowed public routes.
type AdmissionRoutePolicy struct{}

func (AdmissionRoutePolicy) RequiresAdmission(r *http.Request) bool {
	path, ok := canonicalRoutePath(r)
	return ok && r.Method == http.MethodPost && generationPath(path)
}

// AuthenticationPolicy owns authentication for already-allowed public routes.
type AuthenticationPolicy struct {
	enabled bool
}

func (p AuthenticationPolicy) RequiresPublicAuthentication() bool {
	return p.enabled
}

type localManagementHandler uint8

const (
	localManagementHealth localManagementHandler = iota + 1
	localManagementPIGMetrics
	localManagementCombinedMetrics
	localManagementUpstreamStatus
	localManagementPredictivePolicy
	localManagementAttestation
)

// LocalManagementRoutePolicy owns every endpoint handled by PIG itself.
type LocalManagementRoutePolicy struct{}

func (LocalManagementRoutePolicy) Match(r *http.Request) (localManagementHandler, bool) {
	path, ok := canonicalRoutePath(r)
	if !ok {
		return 0, false
	}
	switch path {
	case "/healthz":
		return localManagementHealth, true
	case "/pig/metrics":
		return localManagementPIGMetrics, true
	case "/v1/metrics":
		return localManagementCombinedMetrics, true
	case "/v1/upstream-status":
		return localManagementUpstreamStatus, true
	case predictivePolicyAPIPath:
		return localManagementPredictivePolicy, true
	case "/v1/attestation/report":
		return localManagementAttestation, true
	default:
		return 0, false
	}
}

func (s *proxyServer) serveLocalManagement(
	handler localManagementHandler,
	w http.ResponseWriter,
	r *http.Request,
) {
	switch handler {
	case localManagementHealth:
		_, _ = w.Write([]byte("ok\n"))
	case localManagementPIGMetrics:
		s.metrics(w, r)
	case localManagementCombinedMetrics:
		s.combinedMetrics(w, r)
	case localManagementUpstreamStatus:
		s.upstreamStatus(w, r)
	case localManagementPredictivePolicy:
		s.predictivePolicyAPI(w, r)
	case localManagementAttestation:
		s.attestationReport(w, r)
	default:
		s.rejectPublicRoute(w, r)
	}
}

func (s *proxyServer) rejectPublicRoute(w http.ResponseWriter, r *http.Request) {
	class := s.publicRoutes.RejectionClass(r)
	s.routeNotAllowed.Add(1)
	log.Print(routeRejectionLogLine(class, r))
	openai.WriteNotFound(w)
}

func routeRejectionLogLine(class routeRejectionClass, r *http.Request) string {
	return fmt.Sprintf(
		"level=warn component=route event=rejected reason=route_not_allowed class=%s method_class=%s",
		class,
		routeMethodClass(r),
	)
}

func canonicalRoutePath(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil || r.URL.Scheme != "" || r.URL.Host != "" ||
		r.URL.Opaque != "" || r.URL.Fragment != "" || r.URL.RawPath != "" {
		return "", false
	}
	path := r.URL.Path
	if path == "" || !strings.HasPrefix(path, "/") || r.URL.EscapedPath() != path ||
		pathpkg.Clean(path) != path {
		return "", false
	}
	if r.RequestURI != "" {
		rawTargetPath := r.RequestURI
		if query := strings.IndexByte(rawTargetPath, '?'); query >= 0 {
			rawTargetPath = rawTargetPath[:query]
		}
		if rawTargetPath != path {
			return "", false
		}
	}
	return path, true
}

func generationPath(path string) bool {
	switch path {
	case chatCompletionsPath, completionsPath, responsesPath:
		return true
	default:
		return false
	}
}

func routeMethodClass(r *http.Request) string {
	if r == nil {
		return "other"
	}
	switch r.Method {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "post"
	default:
		return "other"
	}
}
