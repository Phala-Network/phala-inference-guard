package pigconfig

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

// Config is the typed runtime configuration. Tests may inject policy values
// directly, while production Compose files should set only infrastructure,
// credentials, and genuine deployment-specific overrides.
type Config struct {
	Listen                             string
	Upstream                           string
	PredictiveMetricsURL               string
	Token                              string
	QoSPaths                           []string
	APIAuthEnabled                     bool
	APIAuthPaths                       []string
	PathSuffixMatch                    bool
	ProxyTimeout                       time.Duration
	StatusLogInterval                  time.Duration
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

	PredictiveAdmissionMode                string
	PredictiveScannerBodyBytes             int64
	PredictiveScannerConcurrency           int
	OutputTokenFields                      []string
	PredictiveEstimator                    kvadmission.EstimatorConfig
	PredictiveStartupProbeTimeout          time.Duration
	PredictiveMetricsRequestTimeout        time.Duration
	PredictiveObservationPollInterval      time.Duration
	PredictiveMaximumMetricsAge            time.Duration
	PredictivePreemptionCooldown           time.Duration
	PredictiveKVTargetRatio                float64
	PredictiveKVHardRatio                  float64
	PredictiveTPSTarget                    float64
	PredictiveTPSFloor                     float64
	PredictivePrefillRegularTokens         int64
	PredictivePrefillExclusiveTokens       int64
	PredictivePrefillQuiescentTokens       int64
	PredictivePrefillAggregateBudgetTokens int64
}
