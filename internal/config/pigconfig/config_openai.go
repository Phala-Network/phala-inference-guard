package pigconfig

import (
	"os"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

const defaultOpenAICompatBodyBytes = 32 * 1024 * 1024
const defaultNVIDIACommandTimeoutSeconds = 30

func loadOpenAIConfig(cfg *Config) error {
	apiAuthEnabled, err := env.Bool("API_AUTH_ENABLED", cfg.Token != "")
	if err != nil {
		return err
	}
	upstreamErrorClassificationEnabled, err := env.Bool("UPSTREAM_ERROR_CLASSIFICATION_ENABLED", true)
	if err != nil {
		return err
	}
	stripEmptyToolCalls, err := env.Bool("OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS", true)
	if err != nil {
		return err
	}
	compatBodyBytes, err := env.Int("OPENAI_COMPAT_BODY_BYTES", defaultOpenAICompatBodyBytes)
	if err != nil {
		return err
	}
	compatFailOpen, err := env.Bool("OPENAI_COMPAT_FAIL_OPEN", true)
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
	requireNVIDIAEvidence, err := env.Bool("ATTESTATION_REQUIRE_NVIDIA_EVIDENCE", true)
	if err != nil {
		return err
	}

	cfg.APIAuthEnabled = apiAuthEnabled
	cfg.APIAuthPaths = env.CSV("API_AUTH_PATHS", strings.Join(cfg.QoSPaths, ","))
	cfg.UpstreamErrorClassificationEnabled = upstreamErrorClassificationEnabled
	cfg.OpenAICompatStripEmptyToolCalls = stripEmptyToolCalls
	cfg.OpenAICompatBodyBytes = int64(compatBodyBytes)
	cfg.OpenAICompatFailOpen = compatFailOpen
	cfg.AttestationEnabled = attestationEnabled
	cfg.AttestationDstackEndpoint = strings.TrimSpace(env.String("ATTESTATION_DSTACK_ENDPOINT", ""))
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
