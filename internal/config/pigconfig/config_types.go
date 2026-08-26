package pigconfig

import (
	"time"
)

// Config is the typed runtime configuration. Tests may inject policy values
// directly, while production Compose files should set only infrastructure,
// credentials, and genuine deployment-specific overrides.
type Config struct {
	Listen                             string
	Upstream                           string
	PredictiveMetricsURL               string
	Token                              string
	APIAuthEnabled                     bool
	ProxyTimeout                       time.Duration
	StatusLogInterval                  time.Duration
	LogLevel                           string
	UpstreamErrorClassificationEnabled bool

	AttestationEnabled               bool
	AttestationDstackEndpoint        string
	AttestationTLSCertPath           string
	AttestationGPUArch               string
	AttestationNVIDIAPayload         string
	AttestationNVIDIAPayloadFile     string
	AttestationNVIDIAPayloadURL      string
	AttestationNVIDIAPayloadAuth     string
	AttestationNVIDIACommand         string
	AttestationNVIDIACommandArgs     []string
	AttestationNVIDIACommandTimeout  time.Duration
	AttestationRequireNVIDIAEvidence bool

	PredictiveAdmissionMode           string
	PredictiveScannerBodyBytes        int64
	PredictiveScannerConcurrency      int
	PredictiveStartupProbeTimeout     time.Duration
	PredictiveMetricsRequestTimeout   time.Duration
	PredictiveObservationPollInterval time.Duration
	PredictiveMaximumMetricsAge       time.Duration
	PredictiveTPSReference            float64
	PredictiveWindowConcurrency       int64
	PredictiveRunningLimit            int64
	PredictiveRunningLimitConfigured  bool
}
