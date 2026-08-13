package kvadmission

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type exactTokenizerFixture struct {
	name         string
	body         []byte
	bodyBytes    int
	bodySHA256   string
	actualTokens int64
}

type exactTokenizerPayload struct {
	Model     string                  `json:"model"`
	Messages  []exactTokenizerMessage `json:"messages"`
	Tools     []exactTokenizerTool    `json:"tools,omitempty"`
	MaxTokens int                     `json:"max_tokens"`
}

type exactTokenizerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type exactTokenizerTool struct {
	Type     string                 `json:"type"`
	Function exactTokenizerFunction `json:"function"`
}

type exactTokenizerFunction struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Parameters  exactTokenizerParameters `json:"parameters"`
}

type exactTokenizerParameters struct {
	Type       string                            `json:"type"`
	Properties map[string]exactTokenizerProperty `json:"properties"`
}

type exactTokenizerProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type fixedMarginCandidate struct {
	numerator   int64
	denominator int64
}

var registeredFixedMarginCandidates = [...]fixedMarginCandidate{
	{numerator: 9, denominator: 8},
	{numerator: 5, denominator: 4},
	{numerator: 3, denominator: 2},
	{numerator: 2, denominator: 1},
}

const (
	// Require at least 10% oracle evidence headroom: actual/reservation <= 0.90.
	oracleHeadroomActualMultiplier      int64 = 10
	oracleHeadroomReservationMultiplier int64 = 9
	// Reject a candidate whose reservation exceeds 2.25x actual in any
	// registered text fixture. Multimodal fallback is evaluated separately.
	oracleMaximumOverreservationNumerator   int64 = 9
	oracleMaximumOverreservationDenominator int64 = 4
)

func TestEstimatorGemma4OracleEvidence(t *testing.T) {
	fixtures := exactTokenizerFixtures(t)
	selected := fixedMarginCandidate{}
	for _, candidate := range registeredFixedMarginCandidates {
		accepted := true
		for _, fixture := range fixtures {
			cost := EstimateJSON(fixture.body, 256, true, DefaultEstimatorConfig())
			selection, known := cost.ApproximatePrefillTokenHint()
			reservation, valid := fixedMarginTokens(selection, candidate.numerator, candidate.denominator)
			if !cost.Supported || !known || !valid || selection < fixture.actualTokens ||
				fixture.actualTokens*oracleHeadroomActualMultiplier >
					reservation*oracleHeadroomReservationMultiplier ||
				reservation*oracleMaximumOverreservationDenominator >
					fixture.actualTokens*oracleMaximumOverreservationNumerator {
				accepted = false
				break
			}
		}
		if accepted {
			selected = candidate
			break
		}
	}
	if selected.numerator != fixedKVReservationMarginNumerator ||
		selected.denominator != fixedKVReservationMarginDenominator {
		t.Fatalf(
			"selected fixed margin=%d/%d implementation=%d/%d",
			selected.numerator,
			selected.denominator,
			fixedKVReservationMarginNumerator,
			fixedKVReservationMarginDenominator,
		)
	}

	for _, fixture := range fixtures {
		cost := EstimateJSON(fixture.body, 256, true, DefaultEstimatorConfig())
		estimate, known := cost.PredictiveEstimate()
		if !cost.Supported || !known {
			t.Fatalf("fixture=%s cost=%+v estimate=%+v/%t", fixture.name, cost, estimate, known)
		}
		if estimate.SelectionInputTokens < fixture.actualTokens {
			t.Fatalf(
				"fixture=%s selection=%d underestimates actual=%d",
				fixture.name,
				estimate.SelectionInputTokens,
				fixture.actualTokens,
			)
		}
		if len(fixture.body) != fixture.bodyBytes {
			t.Fatalf("fixture=%s body bytes=%d want=%d", fixture.name, len(fixture.body), fixture.bodyBytes)
		}
		bodyDigest := fmt.Sprintf("%x", sha256.Sum256(fixture.body))
		if bodyDigest != fixture.bodySHA256 {
			t.Fatalf("fixture=%s body SHA-256=%s want=%s", fixture.name, bodyDigest, fixture.bodySHA256)
		}
		t.Logf(
			"fixture=%s body_bytes=%d actual=%d selection=%d selection_error=%d reservation=%d reservation_over=%d high=%d",
			fixture.name,
			len(fixture.body),
			fixture.actualTokens,
			estimate.SelectionInputTokens,
			estimate.SelectionInputTokens-fixture.actualTokens,
			estimate.KVReservationInputTokens,
			estimate.KVReservationInputTokens-fixture.actualTokens,
			cost.EstimatedInputHigh,
		)
	}
}

func exactTokenizerFixtures(t *testing.T) []exactTokenizerFixture {
	t.Helper()
	return []exactTokenizerFixture{
		exactTokenizerFixtureFor(t, "common_of_60k_words", "of ", 60_000, 180_092, "db99699ba2962eab171d5869bdbcdc324160a1d7592c5d93000513dc1e083d76", 60_013),
		exactTokenizerFixtureFor(t, "common_the_60k_words", "the ", 60_000, 240_092, "80b3b51f975392d27542ba0eb10faaeebdf4432177bac64a4f7a2e796e153af6", 60_013),
		exactTokenizerFixtureFor(t, "short_lexeme_60k_words", "x ", 60_000, 120_092, "7db92f83f6dccd47d345dfadbc12ade77dc38ff30ea251522c5d46032cddb118", 60_013),
		exactTokenizerFixtureFor(t, "numeric_64k_pairs", "01", 64*1024, 131_164, "a28247c1cfe136411423e94f596b951049823c299a147c6b4dea6c6e853b380f", 131_085),
		exactTokenizerFixtureFor(t, "natural_64k_words", "word ", 64*1024, 327_772, "c8b6e8f708dc91bec38bf8b2316c03051cb2498fcc6cc8dd534c6b2606cc52ae", 65_549),
		exactTokenizerFixtureFor(t, "natural_120k_words", "word ", 120*1024, 614_492, "0d8d864ced416362e5e7e6534d8db63b998ca01ea31c463aa7000658d819ef14", 122_893),
		exactTokenizerFixtureFor(t, "cjk_64k", "\u4e2d", 64*1024, 196_700, "d9c7e77d2c535849e9b441a543f58972d26668d42119ebe3b98bc0d05908cad9", 65_549),
		exactTokenizerFixtureFor(t, "code_64k_lines", "func add(a, b int) int { return a + b }\n", 8*1024, 335_964, "02d855bbf3a933e0a00486b55e9da664d817d87849aa0194646a78ebff9be28f", 131_084),
		exactTokenizerFixtureFor(t, "escape_64k", "line\nquote\"slash\\tab\tunicode\u4e2d", 4*1024, 143_452, "9db795ab5d9b72fa2222b558f059656b9f243fbbe6c11b20fd97738bd7adae0e", 40_973),
		exactTokenizerFixtureFor(t, "entropy_64k", exactEntropyText(64*1024), 1, 65_628, "406a50523bf125b5d2f9acb8748cc82185539b771c965d567cb805ba59430517", 47_410),
		exactSchemaTokenizerFixture(t, 2*1024, 149_770, "42209501c5eee7982106ab9f4bd8f59a282d0051a2cc24d317da587f9e98a5b0", 55_344),
	}
}

func exactTokenizerFixtureFor(
	t *testing.T,
	name string,
	unit string,
	repetitions int,
	bodyBytes int,
	bodySHA256 string,
	actualTokens int64,
) exactTokenizerFixture {
	t.Helper()
	payload := exactTokenizerPayload{
		Model: "google/gemma-4-31B-it",
		Messages: []exactTokenizerMessage{{
			Role:    "user",
			Content: strings.Repeat(unit, repetitions),
		}},
		MaxTokens: 256,
	}
	return exactTokenizerFixture{
		name:         name,
		body:         marshalExactTokenizerFixture(t, payload),
		bodyBytes:    bodyBytes,
		bodySHA256:   bodySHA256,
		actualTokens: actualTokens,
	}
}

func exactSchemaTokenizerFixture(
	t *testing.T,
	properties int,
	bodyBytes int,
	bodySHA256 string,
	actualTokens int64,
) exactTokenizerFixture {
	t.Helper()
	propertySchema := make(map[string]exactTokenizerProperty, properties)
	for index := 0; index < properties; index++ {
		propertySchema[fmt.Sprintf("field_%05d", index)] = exactTokenizerProperty{
			Type:        "string",
			Description: fmt.Sprintf("requested property %05d", index),
		}
	}
	payload := exactTokenizerPayload{
		Model: "google/gemma-4-31B-it",
		Messages: []exactTokenizerMessage{{
			Role:    "user",
			Content: "look up all requested fields",
		}},
		Tools: []exactTokenizerTool{{
			Type: "function",
			Function: exactTokenizerFunction{
				Name:        "lookup",
				Description: "Return requested values.",
				Parameters: exactTokenizerParameters{
					Type:       "object",
					Properties: propertySchema,
				},
			},
		}},
		MaxTokens: 256,
	}
	return exactTokenizerFixture{
		name:         "schema_2k_properties",
		body:         marshalExactTokenizerFixture(t, payload),
		bodyBytes:    bodyBytes,
		bodySHA256:   bodySHA256,
		actualTokens: actualTokens,
	}
}

func marshalExactTokenizerFixture(t *testing.T, payload any) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func exactEntropyText(size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var builder strings.Builder
	builder.Grow(size)
	for index := 0; index < size; index++ {
		builder.WriteByte(alphabet[(index*29+index/7)%len(alphabet)])
	}
	return builder.String()
}
