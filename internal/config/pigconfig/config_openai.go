package pigconfig

import (
	"os"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

const (
	defaultNVIDIACommandTimeoutSeconds = 30
	// The guest agent normally returns a quote in about one second; ten
	// seconds absorbs a slow quote service while still bounding a hung call.
	defaultDstackTimeoutSeconds = 10
)

func loadOpenAIConfig(cfg *Config) error {
	apiAuthEnabled, err := env.Bool("API_AUTH_ENABLED", cfg.Token != "")
	if err != nil {
		return err
	}
	upstreamErrorClassificationEnabled, err := env.Bool("UPSTREAM_ERROR_CLASSIFICATION_ENABLED", true)
	if err != nil {
		return err
	}
	attestationEnabled, err := env.Bool("ATTESTATION_ENABLED", true)
	if err != nil {
		return err
	}
	nvidiaCommandTimeoutSeconds, err := env.Int("ATTESTATION_NVIDIA_COMMAND_TIMEOUT_SECONDS", defaultNVIDIACommandTimeoutSeconds)
	if err != nil {
		return err
	}
	dstackTimeoutSeconds, err := env.Int("ATTESTATION_DSTACK_TIMEOUT_SECONDS", defaultDstackTimeoutSeconds)
	if err != nil {
		return err
	}
	requireNVIDIAEvidence, err := env.Bool("ATTESTATION_REQUIRE_NVIDIA_EVIDENCE", true)
	if err != nil {
		return err
	}

	cfg.APIAuthEnabled = apiAuthEnabled
	cfg.UpstreamErrorClassificationEnabled = upstreamErrorClassificationEnabled
	cfg.AttestationEnabled = attestationEnabled
	cfg.AttestationDstackEndpoint = strings.TrimSpace(env.String("ATTESTATION_DSTACK_ENDPOINT", ""))
	cfg.AttestationDstackTimeout = time.Duration(dstackTimeoutSeconds) * time.Second
	cfg.AttestationTLSCertPath = strings.TrimSpace(env.String("TLS_CERT_PATH", ""))
	cfg.AttestationGPUArch = strings.TrimSpace(env.String("ATTESTATION_GPU_ARCH", "HOPPER"))
	cfg.AttestationNVIDIAPayload = strings.TrimSpace(os.Getenv("ATTESTATION_NVIDIA_PAYLOAD"))
	cfg.AttestationNVIDIAPayloadFile = strings.TrimSpace(env.String("ATTESTATION_NVIDIA_PAYLOAD_FILE", ""))
	cfg.AttestationNVIDIAPayloadURL = strings.TrimSpace(env.String("ATTESTATION_NVIDIA_PAYLOAD_URL", ""))
	cfg.AttestationNVIDIAPayloadAuth = strings.TrimSpace(os.Getenv("ATTESTATION_NVIDIA_PAYLOAD_AUTHORIZATION"))
	cfg.AttestationNVIDIACommand = strings.TrimSpace(env.String("ATTESTATION_NVIDIA_COMMAND", ""))
	cfg.AttestationNVIDIACommandArgs = env.CSV("ATTESTATION_NVIDIA_COMMAND_ARGS", "--nonce,{nonce},--arch,"+cfg.AttestationGPUArch)
	cfg.AttestationNVIDIACommandTimeout = time.Duration(nvidiaCommandTimeoutSeconds) * time.Second
	cfg.AttestationRequireNVIDIAEvidence = requireNVIDIAEvidence
	return nil
}
