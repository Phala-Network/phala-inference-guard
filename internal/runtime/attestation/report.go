package attestation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Config struct {
	TLSCertPath           string
	GPUArch               string
	NVIDIAPayload         string
	NVIDIAPayloadFile     string
	NVIDIACommand         string
	NVIDIACommandArgs     []string
	NVIDIACommandTimeout  time.Duration
	RequireNVIDIAEvidence bool
}

type Service struct {
	cfg     Config
	signers Signers
	dstack  Dstack
}

type ReportRequest struct {
	SigningAlgo string
	NonceHex    string
	Version     int
}

func NewService(cfg Config, dstack Dstack) (*Service, error) {
	signers, err := NewSigners()
	if err != nil {
		return nil, err
	}
	if cfg.GPUArch == "" {
		cfg.GPUArch = "HOPPER"
	}
	if cfg.NVIDIACommandTimeout <= 0 {
		cfg.NVIDIACommandTimeout = 30 * time.Second
	}
	return &Service{cfg: cfg, signers: signers, dstack: dstack}, nil
}

func (s *Service) Generate(ctx context.Context, req ReportRequest) (map[string]any, error) {
	if req.Version == 0 {
		req.Version = 1
	}
	if req.Version != 1 && req.Version != 2 {
		return nil, badRequestError(fmt.Sprintf("Unsupported attestation report version: %d", req.Version))
	}
	signingContext, ok := s.signers.Context(strings.ToLower(strings.TrimSpace(req.SigningAlgo)))
	if !ok {
		return nil, badRequestError("Unsupported signing algorithm")
	}
	nonce, err := parseNonce(req.NonceHex)
	if err != nil {
		return nil, badRequestError(err.Error())
	}
	var certFingerprint []byte
	if req.Version >= 2 {
		certFingerprint, err = ResolveSPKIFingerprint(s.cfg.TLSCertPath)
		if err != nil {
			return nil, badRequestError("attestation version 2 requires a TLS certificate (set TLS_CERT_PATH)")
		}
	}
	reportData, err := buildReportData(signingContext.AddressBytes, nonce, certFingerprint)
	if err != nil {
		return nil, badRequestError(err.Error())
	}
	quote, err := s.dstack.GetQuote(ctx, reportData)
	if err != nil {
		return nil, fmt.Errorf("get dstack quote: %w", err)
	}
	info, err := s.dstack.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("get dstack info: %w", err)
	}
	requestNonceHex := hex.EncodeToString(nonce)
	nvidiaPayload, err := s.nvidiaPayload(ctx, requestNonceHex)
	if err != nil {
		return nil, err
	}
	attestation := map[string]any{
		"signing_address":    signingContext.Address,
		"signing_algo":       signingContext.Algo,
		"signing_public_key": signingContext.PublicKeyHex,
		"request_nonce":      requestNonceHex,
		"intel_quote":        quote.Quote,
		"nvidia_payload":     nvidiaPayload,
		"info":               info,
		"quote":              quote.Quote,
		"event_log":          quote.EventLog,
		"vm_config":          quote.VMConfig,
		"version":            req.Version,
	}
	if certFingerprint != nil {
		attestation["tls_cert_fingerprint"] = hex.EncodeToString(certFingerprint)
	}
	response := cloneMap(attestation)
	response["all_attestations"] = []map[string]any{attestation}
	return response, nil
}

type HTTPError struct {
	Status  int
	Message string
}

func (e HTTPError) Error() string {
	return e.Message
}

func badRequestError(message string) HTTPError {
	return HTTPError{Status: 400, Message: message}
}

func parseNonce(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		return nonce, nil
	}
	nonce, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("Nonce must be hex-encoded")
	}
	if len(nonce) != 32 {
		return nil, fmt.Errorf("Nonce must be 32 bytes")
	}
	return nonce, nil
}

func buildReportData(signingAddressBytes []byte, nonce []byte, certFingerprint []byte) ([]byte, error) {
	if len(signingAddressBytes) == 0 {
		return nil, fmt.Errorf("Signing address must be provided")
	}
	if len(signingAddressBytes) > 32 {
		return nil, fmt.Errorf("Signing address exceeds 32 bytes")
	}
	if len(nonce) != 32 {
		return nil, fmt.Errorf("Nonce must be 32 bytes")
	}
	reportData := make([]byte, 64)
	if certFingerprint != nil {
		digest := sha256.Sum256(append(append([]byte(nil), signingAddressBytes...), certFingerprint...))
		copy(reportData[:32], digest[:])
	} else {
		copy(reportData[:32], signingAddressBytes)
	}
	copy(reportData[32:], nonce)
	return reportData, nil
}

func (s *Service) nvidiaPayload(ctx context.Context, nonceHex string) (string, error) {
	payload := s.cfg.NVIDIAPayload
	if payload == "" && s.cfg.NVIDIAPayloadFile != "" {
		if body, err := os.ReadFile(s.cfg.NVIDIAPayloadFile); err == nil {
			payload = strings.TrimSpace(string(body))
		}
	}
	if payload != "" {
		return s.normalizeNVIDIAPayload(strings.ReplaceAll(payload, "${nonce}", nonceHex), nonceHex)
	}
	if s.cfg.NVIDIACommand != "" {
		output, err := s.runNVIDIACommand(ctx, nonceHex)
		if err != nil {
			return "", err
		}
		return s.normalizeNVIDIAPayload(output, nonceHex)
	}
	if s.cfg.RequireNVIDIAEvidence {
		return "", fmt.Errorf("nvidia evidence is required but no payload or command is configured")
	}
	body, _ := json.Marshal(map[string]any{
		"nonce":         nonceHex,
		"evidence_list": []any{},
		"arch":          s.cfg.GPUArch,
	})
	return string(body), nil
}

func (s *Service) normalizeNVIDIAPayload(raw string, nonceHex string) (string, error) {
	normalized, err := normalizeNVIDIAPayload(raw, nonceHex, s.cfg.GPUArch)
	if err != nil {
		return "", err
	}
	if s.cfg.RequireNVIDIAEvidence {
		if err := requireNonEmptyNVIDIAEvidence(normalized); err != nil {
			return "", err
		}
	}
	return normalized, nil
}

func (s *Service) runNVIDIACommand(ctx context.Context, nonceHex string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, s.cfg.NVIDIACommandTimeout)
	defer cancel()
	args := make([]string, 0, len(s.cfg.NVIDIACommandArgs))
	for _, arg := range s.cfg.NVIDIACommandArgs {
		args = append(args, strings.ReplaceAll(arg, "{nonce}", nonceHex))
	}
	command := exec.CommandContext(commandCtx, s.cfg.NVIDIACommand, args...)
	output, err := command.Output()
	if commandCtx.Err() != nil {
		return "", fmt.Errorf("nvidia evidence command timed out: %w", commandCtx.Err())
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("nvidia evidence command failed: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("nvidia evidence command failed: %w", err)
	}
	return string(output), nil
}

func normalizeNVIDIAPayload(raw string, nonceHex string, arch string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("nvidia payload is empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("nvidia payload must be JSON: %w", err)
	}
	if _, ok := payload["evidence_list"]; ok {
		if _, ok := payload["nonce"]; !ok {
			payload["nonce"] = nonceHex
		}
		if _, ok := payload["arch"]; !ok {
			payload["arch"] = arch
		}
		normalized, err := json.Marshal(payload)
		return string(normalized), err
	}
	if evidences, ok := payload["evidences"]; ok {
		if payloadArch, ok := payload["arch"].(string); ok && payloadArch != "" {
			arch = payloadArch
		}
		normalized, err := json.Marshal(map[string]any{
			"nonce":         nonceHex,
			"evidence_list": evidences,
			"arch":          arch,
		})
		return string(normalized), err
	}
	if s, ok := payload["evidence"].(string); ok && s != "" {
		normalized, err := json.Marshal(map[string]any{
			"nonce":         nonceHex,
			"evidence_list": []any{payload},
			"arch":          arch,
		})
		return string(normalized), err
	}
	return "", fmt.Errorf("nvidia payload JSON must contain evidence_list, evidences, or evidence")
}

func requireNonEmptyNVIDIAEvidence(normalized string) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		return fmt.Errorf("nvidia payload must be JSON: %w", err)
	}
	rawList, ok := payload["evidence_list"]
	if !ok {
		return fmt.Errorf("nvidia payload evidence_list is required when NVIDIA evidence is required")
	}
	var evidenceList []json.RawMessage
	if err := json.Unmarshal(rawList, &evidenceList); err != nil {
		return fmt.Errorf("nvidia payload evidence_list must be an array: %w", err)
	}
	if len(evidenceList) == 0 {
		return fmt.Errorf("nvidia payload evidence_list must not be empty when NVIDIA evidence is required")
	}
	for _, evidence := range evidenceList {
		trimmed := bytes.TrimSpace(evidence)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return fmt.Errorf("nvidia payload evidence_list must not contain empty evidence")
		}
	}
	return nil
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
