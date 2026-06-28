package attestation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
)

type nvidiaCollector interface {
	Collect(ctx context.Context, nonceHex string, fallbackArch string) (string, error)
}

type nvidiaPayloadEnvelope struct {
	Nonce        string           `json:"nonce"`
	EvidenceList []nvidiaEvidence `json:"evidence_list"`
	Arch         string           `json:"arch"`
}

type nvidiaEvidence struct {
	Certificate string `json:"certificate"`
	Evidence    string `json:"evidence"`
	Arch        string `json:"arch"`
}

func emptyNVIDIAPayload(nonceHex string, arch string) nvidiaPayloadEnvelope {
	return nvidiaPayloadEnvelope{
		Nonce:        nonceHex,
		EvidenceList: []nvidiaEvidence{},
		Arch:         normalizeNVIDIAArchName(arch),
	}
}

func marshalNVIDIAPayload(nonceHex string, evidences []nvidiaEvidence, fallbackArch string) (string, error) {
	arch := normalizeNVIDIAArchName(fallbackArch)
	if len(evidences) > 0 && evidences[0].Arch != "" {
		arch = evidences[0].Arch
	}
	body, err := json.Marshal(nvidiaPayloadEnvelope{
		Nonce:        nonceHex,
		EvidenceList: evidences,
		Arch:         arch,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func normalizeNVIDIAArchName(arch string) string {
	arch = strings.TrimSpace(strings.ToUpper(arch))
	if arch == "" {
		return "HOPPER"
	}
	return arch
}

func nvidiaArchName(arch uint32) (string, bool) {
	switch arch {
	case 9:
		return "HOPPER", true
	case 10:
		return "BLACKWELL", true
	default:
		return "", false
	}
}

func encodeNVIDIACertificateChain(raw []byte) (string, error) {
	blocks, err := extractCertificatePEMBlocks(raw)
	if err != nil {
		return "", err
	}
	if len(blocks) == 0 {
		return "", fmt.Errorf("nvidia attestation certificate chain contained no certificates")
	}
	blocks = blocks[:len(blocks)-1]
	rootBlocks, err := extractCertificatePEMBlocks([]byte(nvidiaDeviceRootCertificatePEM))
	if err != nil {
		return "", fmt.Errorf("parse NVIDIA device root certificate: %w", err)
	}
	if len(rootBlocks) != 1 {
		return "", fmt.Errorf("NVIDIA device root certificate must contain exactly one certificate")
	}
	blocks = append(blocks, rootBlocks[0])

	var chain bytes.Buffer
	for _, block := range blocks {
		chain.Write(block)
	}
	return base64.StdEncoding.EncodeToString(chain.Bytes()), nil
}

func extractCertificatePEMBlocks(raw []byte) ([][]byte, error) {
	var blocks [][]byte
	rest := bytes.TrimSpace(raw)
	for len(rest) > 0 {
		block, remaining := pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("invalid PEM certificate chain")
		}
		if block.Type == "CERTIFICATE" {
			blocks = append(blocks, pem.EncodeToMemory(block))
		}
		rest = bytes.TrimSpace(remaining)
	}
	return blocks, nil
}
