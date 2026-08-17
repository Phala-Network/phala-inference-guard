package kvadmission

import (
	"bytes"
	"math"
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

func TestEstimateValidatedJSONMatchesStrictEstimate(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello world"}],"max_tokens":64}`)
	cfg := DefaultEstimatorConfig()
	strict := EstimateJSON(body, 64, true, cfg)
	validated := EstimateValidatedJSON(body, 64, true, cfg)
	if strict != validated || !validated.Supported {
		t.Fatalf("validated estimate=%+v want strict=%+v", validated, strict)
	}
}

func TestEstimateJSONBuildsIndependentKVReservationEstimate(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 64*1024) + `"}]}`)
	cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig())
	selection, known := cost.ApproximatePrefillTokenHint()
	if !cost.Supported || !known || selection <= 0 {
		t.Fatalf("cost=%+v selection=%d/%t", cost, selection, known)
	}
	estimate, estimateKnown := cost.PredictiveEstimate()
	if !estimateKnown || estimate.SelectionInputTokens != selection ||
		estimate.DecodeHorizonTokens != int64(DefaultEstimatorConfig().BlindOutputTokens) {
		t.Fatalf("estimate=%+v/%t selection=%d", estimate, estimateKnown, selection)
	}
	wantReservation := (selection*fixedKVReservationMarginNumerator +
		fixedKVReservationMarginDenominator - 1) / fixedKVReservationMarginDenominator
	if estimate.KVReservationInputTokens != wantReservation ||
		estimate.KVReservationInputTokens >= cost.EstimatedInputHigh {
		t.Fatalf("KV reservation=%d want=%d high=%d", estimate.KVReservationInputTokens, wantReservation, cost.EstimatedInputHigh)
	}
}

func TestEstimateJSONUsesConservativeKVReservationForMultimodal(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,x"}}]}]}`)
	cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig())
	estimate, known := cost.PredictiveEstimate()
	if !cost.Supported || !known || cost.ModalityCount == 0 ||
		estimate.SelectionInputTokens != cost.EstimatedInputHigh ||
		estimate.KVReservationInputTokens != cost.EstimatedInputHigh {
		t.Fatalf("multimodal cost=%+v estimate=%+v/%t", cost, estimate, known)
	}
}

func TestEstimateJSONUsesConservativeGenericJSONEstimate(t *testing.T) {
	for _, body := range [][]byte{[]byte(`null`), []byte(`[]`), []byte(`"value"`), []byte(`42`)} {
		cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig())
		estimate, known := cost.PredictiveEstimate()
		if !cost.Supported || !known || estimate.SelectionInputTokens != cost.EstimatedInputHigh ||
			estimate.KVReservationInputTokens != cost.EstimatedInputHigh {
			t.Fatalf("generic JSON %q cost=%+v estimate=%+v/%t", body, cost, estimate, known)
		}
	}
}

func TestFixedMarginTokensRejectsInvalidAndOverflowingInputs(t *testing.T) {
	for _, test := range []struct {
		tokens      int64
		numerator   int64
		denominator int64
	}{
		{},
		{tokens: 1, numerator: 1, denominator: 2},
		{tokens: 1, numerator: 3},
		{tokens: math.MaxInt64, numerator: 3, denominator: 2},
	} {
		if value, ok := fixedMarginTokens(test.tokens, test.numerator, test.denominator); ok {
			t.Fatalf("accepted invalid margin %+v => %d", test, value)
		}
	}
	if value, ok := fixedMarginTokens(5, 3, 2); !ok || value != 8 {
		t.Fatalf("margin 5*3/2=%d/%t want 8/true", value, ok)
	}
}

func TestAddApproximateRoundedRunRejectsInvalidAndOverflowingInputs(t *testing.T) {
	for _, test := range []struct {
		name          string
		total         *int64
		runBytes      int64
		bytesPerToken int64
	}{
		{name: "nil total", runBytes: 1, bytesPerToken: 4},
		{name: "negative run", total: new(int64), runBytes: -1, bytesPerToken: 4},
		{name: "zero divisor", total: new(int64), runBytes: 1},
		{name: "rounding overflow", total: new(int64), runBytes: math.MaxInt64, bytesPerToken: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			if addApproximateRoundedRun(test.total, test.runBytes, test.bytesPerToken) {
				t.Fatalf("accepted invalid rounded run: %+v", test)
			}
		})
	}

	total := int64(math.MaxInt64 - 3)
	if addApproximateRoundedRun(&total, 1, 4) {
		t.Fatal("accepted rounded run whose quarter-token sum overflows")
	}
	total = 0
	if !addApproximateRoundedRun(&total, 5, 4) || total != 8 {
		t.Fatalf("rounded five-byte run=%d want 8 quarter-token units", total)
	}
}

func TestApproximateJSONStringTokensSmallPrefixDoesNotDominate(t *testing.T) {
	plain := []byte(strings.Repeat("word ", 8*1024))
	prefixed := append([]byte(nil), plain...)
	copy(prefixed[:16], []byte("!@#$%^&*()[]{}:;"))

	plainTokens, plainKnown := approximateJSONStringTokens(plain)
	prefixedTokens, prefixedKnown := approximateJSONStringTokens(prefixed)
	if !plainKnown || !prefixedKnown || plainTokens <= 0 || prefixedTokens <= 0 {
		t.Fatalf("plain=%d/%t prefixed=%d/%t", plainTokens, plainKnown, prefixedTokens, prefixedKnown)
	}
	difference := prefixedTokens - plainTokens
	if difference < 0 {
		difference = -difference
	}
	if difference > plainTokens/10 {
		t.Fatalf("one 16-byte prefix outlier changed long-string estimate: plain=%d prefixed=%d", plainTokens, prefixedTokens)
	}
}

func TestApproximateJSONStringTokensChargesEachShortLexicalRun(t *testing.T) {
	const lexicalRuns = 60_000
	raw := []byte(strings.Repeat("x ", lexicalRuns))
	want := int64(lexicalRuns + ceilDiv(lexicalRuns, approximateSpaceBytesPerToken))

	tokens, known := approximateJSONStringTokens(raw)
	if !known || tokens != want {
		t.Fatalf("repeated short lexical runs=%d/%t want %d/true", tokens, known, want)
	}
}

func TestApproximateJSONStringTokensChargesWhitespaceInsideTheString(t *testing.T) {
	const spaces = 64 * 1024
	tokens, known := approximateJSONStringTokens([]byte(strings.Repeat(" ", spaces)))
	want := int64(ceilDiv(spaces, approximateSpaceBytesPerToken))
	if !known || tokens != want {
		t.Fatalf("string whitespace=%d/%t want %d/true", tokens, known, want)
	}
}

func TestApproximateJSONStringTokensChargesEveryDigit(t *testing.T) {
	const pairs = 64 * 1024
	raw := []byte(strings.Repeat("01", pairs))
	want := int64(len(raw))
	tokens, known := approximateJSONStringTokens(raw)
	if !known || tokens != want {
		t.Fatalf("dense digits=%d/%t want %d/true", tokens, known, want)
	}
}

func TestApproximateJSONStringTokensDistinguishesHighEntropyUnbrokenASCII(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	highEntropy := make([]byte, 16*1024)
	for index := range highEntropy {
		highEntropy[index] = alphabet[(index*29+index/7)%len(alphabet)]
	}
	repeated := bytes.Repeat([]byte("a"), len(highEntropy))

	highTokens, highKnown := approximateJSONStringTokens(highEntropy)
	repeatedTokens, repeatedKnown := approximateJSONStringTokens(repeated)
	if !highKnown || !repeatedKnown {
		t.Fatalf("high/repeated known=%t/%t", highKnown, repeatedKnown)
	}
	if highTokens < int64(len(highEntropy))/2 {
		t.Fatalf("high-entropy estimate=%d want at least %d", highTokens, len(highEntropy)/2)
	}
	if repeatedTokens > int64(len(repeated))/3 {
		t.Fatalf("low-entropy repeated estimate=%d want at most %d", repeatedTokens, len(repeated)/3)
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
	for _, body := range [][]byte{
		[]byte(`{"messages":`),
		[]byte(`{"prompt":"x"} {}`),
		[]byte(`{"a":"b" "c":"d"}`),
		[]byte(`{"a":tru}`),
		[]byte(`{"a":01}`),
		[]byte(`{"a":"\x"}`),
	} {
		if cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig()); cost.Supported || cost.UnsupportedReason != "invalid_json" {
			t.Fatalf("invalid body %q cost=%#v", body, cost)
		}
	}
}

func TestEstimateJSONSupportsValidNonObjectValuesConservatively(t *testing.T) {
	for _, body := range [][]byte{[]byte(`null`), []byte(`[]`), []byte(`"value"`), []byte(`42`)} {
		cost := EstimateJSON(body, 0, false, DefaultEstimatorConfig())
		if !cost.Supported || cost.EstimatedInputLow <= 0 || cost.EstimatedInputHigh < cost.EstimatedInputLow ||
			cost.BoundedDecodeTokens != int64(DefaultEstimatorConfig().BlindOutputTokens) {
			t.Fatalf("valid non-object JSON %q cost=%+v, want bounded generic estimate", body, cost)
		}
		if _, known := cost.ApproximateInputTokenHint(); !known {
			t.Fatalf("valid non-object JSON %q has no approximate input hint: %+v", body, cost)
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

func TestApproximateInputTokenHintPreservesShapeAfterEarlierStrings(t *testing.T) {
	prefixValue := strings.Repeat("a", 64)
	prefix := `{"a":"` + prefixValue + `","b":"` + prefixValue + `","c":"` + prefixValue + `","d":"` + prefixValue + `","prompt":"`
	const payloadBytes = 3 * 1024
	asciiBody := []byte(prefix + strings.Repeat("a", payloadBytes) + `"}`)
	cjkBody := []byte(prefix + strings.Repeat("\xe4\xb8\xad", payloadBytes/3) + `"}`)
	if len(asciiBody) != len(cjkBody) {
		t.Fatalf("fixture body lengths differ: ascii=%d cjk=%d", len(asciiBody), len(cjkBody))
	}

	asciiCost := EstimateJSON(asciiBody, 64, true, DefaultEstimatorConfig())
	cjkCost := EstimateJSON(cjkBody, 64, true, DefaultEstimatorConfig())
	asciiHint, asciiKnown := asciiCost.ApproximateInputTokenHint()
	cjkHint, cjkKnown := cjkCost.ApproximateInputTokenHint()
	if !asciiCost.Supported || !cjkCost.Supported || !asciiKnown || !cjkKnown || cjkHint <= asciiHint {
		t.Fatalf("late string shape was lost: ascii=%d/%t cjk=%d/%t", asciiHint, asciiKnown, cjkHint, cjkKnown)
	}
}

func TestApproximateInputTokenHintKeepsLate650KCJKQuiescentSized(t *testing.T) {
	prefixValue := strings.Repeat("a", 64)
	prefix := `{"a":"` + prefixValue + `","b":"` + prefixValue + `","c":"` + prefixValue + `","d":"` + prefixValue + `","prompt":"`
	body := []byte(prefix + strings.Repeat("\xe4\xb8\xad", 650*1024) + `"}`)

	cost := EstimateJSON(body, 64, true, DefaultEstimatorConfig())
	hint, known := cost.ApproximateInputTokenHint()
	if !cost.Supported || !known || hint < 512*1024 {
		t.Fatalf("late 650K CJK hint=%d/%t supported=%t, want >=512K quiescent boundary", hint, known, cost.Supported)
	}
}

func TestApproximateJSONStringTokensShortValueFastPath(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int64
	}{
		{raw: "", want: 0},
		{raw: "a", want: 1},
		{raw: "abc", want: 1},
		{raw: "\xe4\xb8\xad", want: 1},
	} {
		got, known := approximateJSONStringTokens([]byte(test.raw))
		if !known || got != test.want {
			t.Fatalf("short lexical value %q = %d/%t want %d/true", test.raw, got, known, test.want)
		}
	}
}

func TestApproximateInputTokenHintModelNeutralShapeCorpus(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "natural-text", body: `{"prompt":"Explain why a quiet lake reflects the evening sky."}`},
		{name: "code", body: `{"prompt":"func add(a, b int) int { return a + b }"}`},
		{name: "multilingual", body: `{"prompt":"中文输入 English 日本語 한국어 العربية"}`},
		{name: "escape-heavy", body: `{"prompt":"line\\nquote\\\"slash\\\\tab\\tunicode\\u4e2d"}`},
		{name: "schema", body: `{"messages":[{"role":"user","content":"lookup"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]}`},
		{name: "high-entropy", body: `{"prompt":"A9+/zQ7!f0_kL=2@xV#8mN$4pR%1sT^6wY&3"}`},
		{name: "multimodal-marker", body: `{"messages":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,x"}}]}]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			body := []byte(fixture.body)
			original := bytes.Clone(body)
			cost := EstimateJSON(body, 64, true, DefaultEstimatorConfig())
			replayed := EstimateJSON(body, 64, true, DefaultEstimatorConfig())
			hint, known := cost.ApproximateInputTokenHint()
			if !bytes.Equal(body, original) {
				t.Fatal("shape corpus estimator modified the request body")
			}
			if cost != replayed {
				t.Fatalf("shape corpus estimate is not deterministic: first=%+v replay=%+v", cost, replayed)
			}
			if !cost.Supported || !known || hint <= 0 || cost.EstimatedInputLow <= 0 ||
				cost.EstimatedInputHigh < cost.EstimatedInputLow {
				t.Fatalf("shape corpus cost=%+v hint=%d/%t", cost, hint, known)
			}
			safety := cost.EstimatedInputHigh
			if hint > safety {
				safety = hint
			}
			t.Logf(
				"fixture=%s body_bytes=%d point_tokens=%d safety_tokens=%d raw_interval=%d..%d",
				fixture.name, len(body), hint, safety, cost.EstimatedInputLow, cost.EstimatedInputHigh,
			)
		})
	}

	ascii, asciiKnown := approximateJSONStringTokens([]byte("aaaaaaaaaaaaaaaa"))
	punctuation, punctuationKnown := approximateJSONStringTokens([]byte("!!!!!!!!!!!!!!!!"))
	if !asciiKnown || !punctuationKnown || punctuation <= ascii {
		t.Fatalf("equal-byte lexical differentiation ascii=%d/%t punctuation=%d/%t", ascii, asciiKnown, punctuation, punctuationKnown)
	}
}

func TestApproximateInputTokenHintMaximumBodyFixtureIsDeterministic(t *testing.T) {
	const maximumBodyBytes = 4 * 1024 * 1024
	prefix := `{"messages":[{"role":"user","content":"`
	suffix := `"}],"max_tokens":256}`
	payloadBytes := maximumBodyBytes - len(prefix) - len(suffix)
	if payloadBytes <= 0 {
		t.Fatal("maximum-body fixture has no payload")
	}
	body := []byte(prefix + strings.Repeat("a", payloadBytes) + suffix)
	if len(body) != maximumBodyBytes {
		t.Fatalf("maximum-body fixture bytes=%d want=%d", len(body), maximumBodyBytes)
	}
	original := bytes.Clone(body)

	first := EstimateJSON(body, 256, true, DefaultEstimatorConfig())
	second := EstimateJSON(body, 256, true, DefaultEstimatorConfig())
	hint, known := first.ApproximateInputTokenHint()
	if !bytes.Equal(body, original) {
		t.Fatal("maximum-body estimator modified the request body")
	}
	if first != second || !first.Supported || !known || hint <= 0 ||
		first.EstimatedInputLow <= 0 || first.EstimatedInputHigh < first.EstimatedInputLow {
		t.Fatalf("maximum-body estimate first=%+v second=%+v hint=%d/%t", first, second, hint, known)
	}
	safety := first.EstimatedInputHigh
	if hint > safety {
		safety = hint
	}
	t.Logf(
		"fixture=maximum-body body_bytes=%d point_tokens=%d safety_tokens=%d raw_interval=%d..%d",
		len(body), hint, safety, first.EstimatedInputLow, first.EstimatedInputHigh,
	)
}

func TestApproximateInputTokenHintInspectsTheCompleteString(t *testing.T) {
	const size = 256 * 1024
	raw := []byte(exactEntropyText(size))
	for _, start := range []int{0, size/3 - 32, 2*size/3 - 32, size - 64} {
		copy(raw[start:start+64], bytes.Repeat([]byte{'a'}, 64))
	}
	tokens, known := approximateJSONStringTokens(raw)
	if !known || tokens < size/2 {
		t.Fatalf("complete lexical estimate=%d/%t want at least %d", tokens, known, size/2)
	}
}

func TestApproximateInputTokenHintExcludesTrailingJSONWhitespace(t *testing.T) {
	trimmedBody := []byte(`{"model":"google/gemma-4-31B-it","prompt":"Return exactly OK.","max_tokens":8,"temperature":0}`)
	body := append(bytes.Clone(trimmedBody), bytes.Repeat([]byte(" "), 1_600_000)...)
	cost := EstimateJSON(body, 8, true, DefaultEstimatorConfig())
	trimmedCost := EstimateJSON(trimmedBody, 8, true, DefaultEstimatorConfig())
	if !cost.Supported {
		t.Fatalf("trailing-whitespace fixture unsupported: %+v", cost)
	}
	if cost.EstimatedInputHigh != 800_047 {
		t.Fatalf("raw conservative high=%d want 800047", cost.EstimatedInputHigh)
	}
	hint, known := cost.ApproximateInputTokenHint()
	trimmedHint, trimmedKnown := trimmedCost.ApproximateInputTokenHint()
	if !known || !trimmedKnown || hint != trimmedHint {
		t.Fatalf(
			"trailing JSON whitespace changed lexical hint: padded=%d/%t trimmed=%d/%t",
			hint, known, trimmedHint, trimmedKnown,
		)
	}
}

func TestApproximatePrefillTokenHintChargesRecognizedMultimodalExpansion(t *testing.T) {
	text := Cost{
		EstimatedInputHigh:          8 * 1024,
		ApproximateInputTokens:      256,
		ApproximateInputTokensKnown: true,
	}
	if hint, known := text.ApproximatePrefillTokenHint(); !known || hint != 256 {
		t.Fatalf("text prefill hint=%d/%t want bounded lexical 256/true", hint, known)
	}

	multimodal := text
	multimodal.ModalityCount = 1
	if hint, known := multimodal.ApproximatePrefillTokenHint(); !known || hint != 8*1024 {
		t.Fatalf("multimodal prefill hint=%d/%t want safety input 8K/true", hint, known)
	}

	unknown := text
	unknown.ApproximateInputTokens = 0
	unknown.ApproximateInputTokensKnown = false
	if hint, known := unknown.ApproximatePrefillTokenHint(); known || hint != 0 {
		t.Fatalf("unknown text prefill hint=%d/%t want 0/false for adapter fallback", hint, known)
	}
}

var benchmarkEstimatorCost Cost
var benchmarkApproximateHintTokens int64
var benchmarkApproximateHintKnown bool

func BenchmarkCostApproximatePrefillTokenHint(b *testing.B) {
	for _, fixture := range []struct {
		name string
		cost Cost
	}{
		{name: "text", cost: Cost{
			EstimatedInputHigh:          8 * 1024,
			ApproximateInputTokens:      256,
			ApproximateInputTokensKnown: true,
		}},
		{name: "multimodal", cost: Cost{
			EstimatedInputHigh:          8 * 1024,
			ApproximateInputTokens:      256,
			ApproximateInputTokensKnown: true,
			ModalityCount:               1,
		}},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				benchmarkApproximateHintTokens, benchmarkApproximateHintKnown = fixture.cost.ApproximatePrefillTokenHint()
			}
		})
	}
}

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

func BenchmarkEstimator4MiB(b *testing.B) {
	benchmarkEstimator(b, 4*1024*1024)
}

func BenchmarkEstimatorManyStrings4MiB(b *testing.B) {
	const targetBytes = 4 * 1024 * 1024
	prefix := []byte(`{"prompt":[`)
	item := []byte(`"a",`)
	suffix := []byte(`"a"]}`)
	itemCount := (targetBytes - len(prefix) - len(suffix)) / len(item)
	body := make([]byte, 0, targetBytes)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat(item, itemCount)...)
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
