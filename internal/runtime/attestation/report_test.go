package attestation

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type mockDstack struct {
	reportData []byte
}

func (m *mockDstack) GetQuote(_ context.Context, reportData []byte) (QuoteResponse, error) {
	m.reportData = append([]byte(nil), reportData...)
	return QuoteResponse{
		Quote:    "mock_quote",
		EventLog: `[{"imr":0,"digest":"aa"}]`,
		VMConfig: "mock_vm_config",
	}, nil
}

func (m *mockDstack) Info(context.Context) (map[string]any, error) {
	return map[string]any{
		"compose_hash": "mock_compose_hash",
		"tcb_info":     map[string]any{"app_compose": "compose"},
	}, nil
}

func TestBuildReportDataLayout(t *testing.T) {
	identifier := bytes.Repeat([]byte{0x01}, 20)
	nonce := bytes.Repeat([]byte{0x02}, 32)
	reportData, err := buildReportData(identifier, nonce, nil)
	if err != nil {
		t.Fatalf("buildReportData: %v", err)
	}
	if len(reportData) != 64 {
		t.Fatalf("len=%d want 64", len(reportData))
	}
	if !bytes.Equal(reportData[:20], identifier) {
		t.Fatalf("identifier not copied")
	}
	if !bytes.Equal(reportData[20:32], make([]byte, 12)) {
		t.Fatalf("identifier not zero padded")
	}
	if !bytes.Equal(reportData[32:], nonce) {
		t.Fatalf("nonce not copied")
	}
}

func TestBuildReportDataFingerprintMode(t *testing.T) {
	identifier := bytes.Repeat([]byte{0x01}, 20)
	nonce := bytes.Repeat([]byte{0x02}, 32)
	fingerprint := bytes.Repeat([]byte{0xab}, 32)
	reportData, err := buildReportData(identifier, nonce, fingerprint)
	if err != nil {
		t.Fatalf("buildReportData: %v", err)
	}
	expected := sha256.Sum256(append(append([]byte(nil), identifier...), fingerprint...))
	if !bytes.Equal(reportData[:32], expected[:]) {
		t.Fatalf("fingerprint digest mismatch")
	}
	if !bytes.Equal(reportData[32:], nonce) {
		t.Fatalf("nonce mismatch")
	}
}

func TestGenerateAttestationResponseShape(t *testing.T) {
	dstack := &mockDstack{}
	service, err := NewService(Config{GPUArch: "HOPPER"}, dstack)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	nonceHex := "aa" + stringsRepeat("00", 31)
	report, err := service.Generate(context.Background(), ReportRequest{
		SigningAlgo: AlgoEd25519,
		NonceHex:    nonceHex,
		Version:     1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, key := range []string{"signing_address", "signing_algo", "signing_public_key", "request_nonce", "intel_quote", "nvidia_payload", "info", "quote", "event_log", "vm_config", "version", "all_attestations"} {
		if _, ok := report[key]; !ok {
			t.Fatalf("missing key %s", key)
		}
	}
	if report["request_nonce"] != nonceHex {
		t.Fatalf("request_nonce=%v want %s", report["request_nonce"], nonceHex)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(report["nvidia_payload"].(string)), &payload); err != nil {
		t.Fatalf("nvidia_payload json: %v", err)
	}
	if payload["nonce"] != nonceHex {
		t.Fatalf("nvidia nonce=%v want %s", payload["nonce"], nonceHex)
	}
}

func TestGenerateAttestationFiltersCloudInfoFields(t *testing.T) {
	service, err := NewService(Config{GPUArch: "HOPPER"}, &mockDstackWithCloudInfo{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	report, err := service.Generate(context.Background(), ReportRequest{
		SigningAlgo: AlgoECDSA,
		NonceHex:    stringsRepeat("ab", 32),
		Version:     1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	info := report["info"].(map[string]any)
	if _, ok := info["cloud_product"]; ok {
		t.Fatalf("cloud_product leaked into report info")
	}
	if _, ok := info["cloud_vendor"]; ok {
		t.Fatalf("cloud_vendor leaked into report info")
	}
	attestations := report["all_attestations"].([]map[string]any)
	nestedInfo := attestations[0]["info"].(map[string]any)
	if _, ok := nestedInfo["cloud_product"]; ok {
		t.Fatalf("cloud_product leaked into nested attestation info")
	}
	if _, ok := nestedInfo["cloud_vendor"]; ok {
		t.Fatalf("cloud_vendor leaked into nested attestation info")
	}
}

func TestGenerateAttestationV2BindsTLSFingerprint(t *testing.T) {
	certPath, expectedFingerprint := writeTestCert(t)
	dstack := &mockDstack{}
	service, err := NewService(Config{TLSCertPath: certPath}, dstack)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	nonceHex := stringsRepeat("bb", 32)
	report, err := service.Generate(context.Background(), ReportRequest{
		SigningAlgo: AlgoECDSA,
		NonceHex:    nonceHex,
		Version:     2,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if report["tls_cert_fingerprint"] != hex.EncodeToString(expectedFingerprint) {
		t.Fatalf("tls fingerprint mismatch")
	}
	if len(dstack.reportData) != 64 {
		t.Fatalf("reportData len=%d want 64", len(dstack.reportData))
	}
	if hex.EncodeToString(dstack.reportData[32:]) != nonceHex {
		t.Fatalf("nonce not bound in report data")
	}
}

func TestNormalizeNVIDIAPayloadFromNVAttestShape(t *testing.T) {
	raw := `{"arch":"HOPPER","evidences":[{"evidence":"abc","certificate":"cert"}]}`
	normalized, err := normalizeNVIDIAPayload(raw, stringsRepeat("cc", 32), "HOPPER")
	if err != nil {
		t.Fatalf("normalizeNVIDIAPayload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		t.Fatalf("normalized json: %v", err)
	}
	if payload["nonce"] != stringsRepeat("cc", 32) {
		t.Fatalf("nonce mismatch")
	}
	if _, ok := payload["evidence_list"]; !ok {
		t.Fatalf("missing evidence_list: %s", normalized)
	}
}

func TestRequiredNVIDIAEvidenceRejectsEmptyEvidenceList(t *testing.T) {
	service, err := NewService(Config{
		GPUArch:               "HOPPER",
		NVIDIAPayload:         `{"evidence_list":[],"nonce":"${nonce}","arch":"HOPPER"}`,
		RequireNVIDIAEvidence: true,
	}, &mockDstack{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.nvidiaPayload(context.Background(), stringsRepeat("dd", 32))

	if err == nil {
		t.Fatalf("nvidiaPayload accepted empty evidence_list")
	}
	if !strings.Contains(err.Error(), "evidence_list must not be empty") {
		t.Fatalf("error=%q, want empty evidence_list rejection", err)
	}
}

func TestRequiredNVIDIAEvidenceRejectsEmptyNVAttestEvidences(t *testing.T) {
	service, err := NewService(Config{
		GPUArch:               "HOPPER",
		NVIDIAPayload:         `{"evidences":[],"arch":"HOPPER"}`,
		RequireNVIDIAEvidence: true,
	}, &mockDstack{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.nvidiaPayload(context.Background(), stringsRepeat("ee", 32))

	if err == nil {
		t.Fatalf("nvidiaPayload accepted empty evidences")
	}
	if !strings.Contains(err.Error(), "evidence_list must not be empty") {
		t.Fatalf("error=%q, want empty evidence_list rejection", err)
	}
}

func TestRequiredNVIDIAEvidenceAcceptsNonEmptyEvidenceList(t *testing.T) {
	nonceHex := stringsRepeat("ff", 32)
	service, err := NewService(Config{
		GPUArch:               "HOPPER",
		NVIDIAPayload:         `{"evidence_list":[{"evidence":"abc"}],"nonce":"${nonce}","arch":"HOPPER"}`,
		RequireNVIDIAEvidence: true,
	}, &mockDstack{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	normalized, err := service.nvidiaPayload(context.Background(), nonceHex)

	if err != nil {
		t.Fatalf("nvidiaPayload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		t.Fatalf("normalized json: %v", err)
	}
	if payload["nonce"] != nonceHex {
		t.Fatalf("nonce=%v want %s", payload["nonce"], nonceHex)
	}
	evidences := payload["evidence_list"].([]any)
	if len(evidences) != 1 {
		t.Fatalf("evidence_list len=%d want 1", len(evidences))
	}
}

func TestNVIDIAPayloadURLExtractsPayloadFromAttestationReport(t *testing.T) {
	nonceHex := stringsRepeat("12", 32)
	var observedNonce string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedNonce = r.URL.Query().Get("nonce")
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization=%q want Bearer test-token", got)
		}
		payload := map[string]any{
			"nonce": observedNonce,
			"evidence_list": []map[string]any{{
				"arch":        "HOPPER",
				"certificate": "cert",
				"evidence":    "evidence",
			}},
			"arch": "HOPPER",
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nvidia_payload": string(payloadJSON)})
	}))
	defer collector.Close()
	service, err := NewService(Config{
		GPUArch:               "HOPPER",
		NVIDIAPayloadURL:      collector.URL + "/v1/attestation/report?signing_algo=ecdsa",
		NVIDIAPayloadAuth:     "Bearer test-token",
		NVIDIACommandTimeout:  time.Second,
		RequireNVIDIAEvidence: true,
	}, &mockDstack{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	normalized, err := service.nvidiaPayload(context.Background(), nonceHex)

	if err != nil {
		t.Fatalf("nvidiaPayload: %v", err)
	}
	if observedNonce != nonceHex {
		t.Fatalf("collector nonce=%q want %q", observedNonce, nonceHex)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		t.Fatalf("normalized json: %v", err)
	}
	if payload["nonce"] != nonceHex {
		t.Fatalf("nonce=%v want %s", payload["nonce"], nonceHex)
	}
	evidences := payload["evidence_list"].([]any)
	if len(evidences) != 1 {
		t.Fatalf("evidence_list len=%d want 1", len(evidences))
	}
}

func writeTestCert(t *testing.T) (string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(4102444800, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal spki: %v", err)
	}
	digest := sha256.Sum256(spki)
	file, err := os.CreateTemp(t.TempDir(), "cert-*.pem")
	if err != nil {
		t.Fatalf("temp cert: %v", err)
	}
	if err := pem.Encode(file, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close cert: %v", err)
	}
	return file.Name(), digest[:]
}

func stringsRepeat(value string, count int) string {
	var buffer bytes.Buffer
	for i := 0; i < count; i++ {
		buffer.WriteString(value)
	}
	return buffer.String()
}

type mockDstackWithCloudInfo struct {
	mockDstack
}

func (m *mockDstackWithCloudInfo) Info(context.Context) (map[string]any, error) {
	return map[string]any{
		"app_id":        "mock_app",
		"cloud_product": "h200.small",
		"cloud_vendor":  "mock_cloud",
		"compose_hash":  "mock_compose_hash",
		"tcb_info":      map[string]any{"app_compose": "compose"},
	}, nil
}
