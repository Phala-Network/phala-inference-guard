package kvadmission

import (
	"bytes"
	"strings"
	"testing"
)

type approximateInputTokenHintProvider interface {
	ApproximateInputTokenHint() (int64, bool)
}

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

func TestApproximateInputTokenHintSeparatesEqualByteLanguageShapes(t *testing.T) {
	const textBytes = 96
	asciiBody := []byte(`{"prompt":"` + strings.Repeat("a", textBytes) + `"}`)
	cjkBody := []byte(`{"prompt":"` + strings.Repeat("中", textBytes/len("中")) + `"}`)
	if len(asciiBody) != len(cjkBody) {
		t.Fatalf("fixture body lengths differ: ascii=%d cjk=%d", len(asciiBody), len(cjkBody))
	}

	asciiCost := EstimateJSON(asciiBody, 64, true, DefaultEstimatorConfig())
	cjkCost := EstimateJSON(cjkBody, 64, true, DefaultEstimatorConfig())
	if !asciiCost.Supported || !cjkCost.Supported || asciiCost.EstimatedInputHigh != cjkCost.EstimatedInputHigh {
		t.Fatalf("fixture must hold the byte estimator constant: ascii=%+v cjk=%+v", asciiCost, cjkCost)
	}

	asciiProvider, asciiOK := any(asciiCost).(approximateInputTokenHintProvider)
	cjkProvider, cjkOK := any(cjkCost).(approximateInputTokenHintProvider)
	if !asciiOK || !cjkOK {
		t.Fatal("supported JSON cost has no optional approximate input-token hint")
	}
	asciiHint, asciiKnown := asciiProvider.ApproximateInputTokenHint()
	cjkHint, cjkKnown := cjkProvider.ApproximateInputTokenHint()
	if !asciiKnown || !cjkKnown || asciiHint <= 0 || cjkHint <= asciiHint {
		t.Fatalf("approximate hint did not distinguish equal-byte language shapes: ascii=%d/%t cjk=%d/%t", asciiHint, asciiKnown, cjkHint, cjkKnown)
	}
}

func TestApproximateInputTokenHintUsesBoundedSampling(t *testing.T) {
	raw := bytes.Repeat([]byte("bounded lexical sample "), 128*1024)
	tokens, sampled, known := approximateJSONStringTokensWithBudget(raw, approximateLexicalRequestBudget)
	if !known || tokens <= 0 {
		t.Fatalf("bounded lexical hint unavailable: tokens=%d known=%t", tokens, known)
	}
	if sampled != approximateLexicalPerStringLimit || sampled > approximateLexicalRequestBudget {
		t.Fatalf("sampled bytes=%d want per-string bound=%d request bound<=%d", sampled, approximateLexicalPerStringLimit, approximateLexicalRequestBudget)
	}
}

func TestApproximateInputTokenHintExcludesTrailingJSONWhitespace(t *testing.T) {
	body := []byte(`{"model":"google/gemma-4-31B-it","prompt":"Return exactly OK.","max_tokens":8,"temperature":0}` + strings.Repeat(" ", 1_600_000))
	cost := EstimateJSON(body, 8, true, DefaultEstimatorConfig())
	if !cost.Supported {
		t.Fatalf("trailing-whitespace fixture unsupported: %+v", cost)
	}
	if cost.EstimatedInputHigh != 800_047 {
		t.Fatalf("raw conservative high=%d want 800047", cost.EstimatedInputHigh)
	}
	hint, known := cost.ApproximateInputTokenHint()
	if !known || hint != 14 {
		t.Fatalf("lexical hint=%d/%t want 14/true without trailing JSON whitespace", hint, known)
	}
	// The matching integration fixture reports four prompt tokens. The raw
	// ratio is intentionally too small to learn, while 4/14 remains inside the
	// calibrator's [0.25, 8] qualification band.
	if rawRatio, hintRatio := 4/float64(cost.EstimatedInputHigh), 4/float64(hint); rawRatio >= 0.25 || hintRatio < 0.25 || hintRatio > 8 {
		t.Fatalf("fixture ratios raw=%g hint=%g do not isolate the hint channel", rawRatio, hintRatio)
	}
}

var benchmarkEstimatorCost Cost
var benchmarkApproximateHintTokens int64
var benchmarkApproximateHintKnown bool

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

func BenchmarkApproximateJSONStringTokens1KiB(b *testing.B) {
	benchmarkApproximateJSONStringTokens(b, 1*1024)
}

func BenchmarkApproximateJSONStringTokens64KiB(b *testing.B) {
	benchmarkApproximateJSONStringTokens(b, 64*1024)
}

func BenchmarkApproximateJSONStringTokens2MiB(b *testing.B) {
	benchmarkApproximateJSONStringTokens(b, 2*1024*1024)
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

func benchmarkApproximateJSONStringTokens(b *testing.B, textBytes int) {
	b.Helper()
	raw := bytes.Repeat([]byte("simple lexical input "), ceilDiv(textBytes, len("simple lexical input ")))
	raw = raw[:textBytes]
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkApproximateHintTokens, benchmarkApproximateHintKnown = approximateJSONStringTokens(raw)
	}
	if !benchmarkApproximateHintKnown || benchmarkApproximateHintTokens <= 0 {
		b.Fatal("approximate tokenizer hint became unavailable")
	}
}
