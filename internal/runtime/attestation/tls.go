package attestation

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func SPKISHA256FromPEM(pemBytes []byte) ([]byte, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM certificate found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return digest[:], nil
}

func ResolveSPKIFingerprint(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("TLS_CERT_PATH is not set")
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return SPKISHA256FromPEM(pemBytes)
}
