package kvadmission

import (
	"bytes"
	"testing"
)

func TestEstimateJSONBuildsConservativeInterval(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello world"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"max_tokens":1024}`)
	cost := EstimateJSON(body, 1024, true, DefaultEstimatorConfig())
	if !cost.Supported {
		t.Fatalf("cost unsupported: %#v", cost)
	}
	if cost.MessageCount != 1 || cost.ToolCount != 1 || cost.ToolSchemaBytes == 0 || cost.TextBytes == 0 {
		t.Fatalf("unexpected features: %#v", cost)
	}
	if cost.EstimatedInputLow <= 0 || cost.EstimatedInputHigh < cost.EstimatedInputLow {
		t.Fatalf("invalid interval: %#v", cost)
	}
	if cost.BoundedDecodeTokens != 256 {
		t.Fatalf("decode allowance=%d want 256", cost.BoundedDecodeTokens)
	}
	minimumWholeBodyHigh := int64(ceilDiv(len(body), DefaultEstimatorConfig().MinBytesPerToken))
	if cost.EstimatedInputHigh < minimumWholeBodyHigh {
		t.Fatalf("high=%d below whole-body bound %d", cost.EstimatedInputHigh, minimumWholeBodyHigh)
	}
}

func TestEstimateJSONBoundsDecodeByRequestedMaximum(t *testing.T) {
	cost := EstimateJSON([]byte(`{"prompt":"hello"}`), 64, true, DefaultEstimatorConfig())
	if !cost.Supported || cost.BoundedDecodeTokens != 64 {
		t.Fatalf("cost=%#v want supported decode=64", cost)
	}
}

func TestEstimateJSONCountsMultimodalMarkers(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"input_text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,x"}}]}]}`)
	cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig())
	if !cost.Supported || cost.ModalityCount < 1 {
		t.Fatalf("cost=%#v want modality", cost)
	}
	if cost.EstimatedInputHigh < int64(DefaultEstimatorConfig().ModalityTokensHigh) {
		t.Fatalf("high=%d missing modality allowance", cost.EstimatedInputHigh)
	}
}

func TestEstimateJSONRejectsMalformedOrTrailingData(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"messages":`), []byte(`{"prompt":"x"} {}`)} {
		if cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig()); cost.Supported {
			t.Fatalf("accepted invalid body %q: %#v", body, cost)
		}
	}
}

var benchmarkEstimatorCost Cost

func BenchmarkEstimator1KiB(b *testing.B) {
	benchmarkEstimator(b, 1*1024)
}

func BenchmarkEstimator16KiB(b *testing.B) {
	benchmarkEstimator(b, 16*1024)
}

func BenchmarkEstimator64KiB(b *testing.B) {
	benchmarkEstimator(b, 64*1024)
}

func BenchmarkEstimator1MiB(b *testing.B) {
	benchmarkEstimator(b, 1*1024*1024)
}

func BenchmarkEstimator2MiB(b *testing.B) {
	benchmarkEstimator(b, 2*1024*1024)
}

func benchmarkEstimator(b *testing.B, targetBytes int) {
	b.Helper()
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	payloadBytes := targetBytes - len(prefix) - len(suffix)
	if payloadBytes <= 0 {
		b.Fatalf("target body bytes %d are too small", targetBytes)
	}
	body := make([]byte, 0, targetBytes)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte{'a'}, payloadBytes)...)
	body = append(body, suffix...)
	cfg := DefaultEstimatorConfig()
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkEstimatorCost = EstimateJSON(body, 256, true, cfg)
		if !benchmarkEstimatorCost.Supported {
			b.Fatal("unsupported")
		}
	}
}
