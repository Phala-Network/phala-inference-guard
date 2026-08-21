package lexical

import (
	"math"
	"strings"
	"testing"
)

func TestAddRoundedRunRejectsInvalidAndOverflowingInputs(t *testing.T) {
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
			if addRoundedRun(test.total, test.runBytes, test.bytesPerToken) {
				t.Fatalf("accepted invalid rounded run: %+v", test)
			}
		})
	}

	total := int64(math.MaxInt64 - 3)
	if addRoundedRun(&total, 1, 4) {
		t.Fatal("accepted rounded run whose quarter-token sum overflows")
	}
	total = 0
	if !addRoundedRun(&total, 5, 4) || total != 8 {
		t.Fatalf("rounded five-byte run=%d want 8 quarter-token units", total)
	}
}

func TestV01218DecodedJSONStringEstimateMatchesLiteralUTF8(t *testing.T) {
	tests := []struct {
		name    string
		literal string
		escaped string
	}{
		{name: "plain", literal: "plain text", escaped: "plain text"},
		{name: "CJK", literal: "中文\nquote\"", escaped: `\u4e2d\u6587\nquote\"`},
		{name: "surrogate pair", literal: "😀", escaped: `\ud83d\ude00`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			literalTokens, literalBytes, literalConservative, literalValid :=
				EstimateDecodedJSONStringTokensWithRisk([]byte(test.literal))
			escapedTokens, escapedBytes, escapedConservative, escapedValid :=
				EstimateDecodedJSONStringTokensWithRisk([]byte(test.escaped))
			if !literalValid || !escapedValid || literalBytes != int64(len(test.literal)) ||
				escapedBytes != literalBytes || escapedTokens != literalTokens ||
				escapedConservative != literalConservative {
				t.Fatalf("decoded estimate mismatch: literal=%d/%d/%t/%t escaped=%d/%d/%t/%t",
					literalTokens, literalBytes, literalConservative, literalValid,
					escapedTokens, escapedBytes, escapedConservative, escapedValid)
			}
		})
	}
}

func TestV01218DecodedJSONStringEstimateRejectsInvalidEscapes(t *testing.T) {
	for _, raw := range []string{`\x`, `\u12`, `\ud83d`, `\ude00`, `\ud83d\u0041`} {
		if _, _, _, valid := EstimateDecodedJSONStringTokensWithRisk([]byte(raw)); valid {
			t.Fatalf("accepted invalid decoded JSON string %q", raw)
		}
	}
}

func TestV01218DecodedJSONStringEstimateDoesNotAllocate(t *testing.T) {
	raw := []byte(strings.Repeat(`\u4e2d`, 16*1024))
	var tokens, decodedBytes int64
	var conservative, valid bool
	allocations := testing.AllocsPerRun(10, func() {
		tokens, decodedBytes, conservative, valid = EstimateDecodedJSONStringTokensWithRisk(raw)
	})
	if !valid || !conservative || tokens <= 0 || decodedBytes != 3*16*1024 {
		t.Fatalf("decoded allocation fixture=%d/%d/%t/%t", tokens, decodedBytes, conservative, valid)
	}
	if allocations != 0 {
		t.Fatalf("decoded JSON string allocations=%g want 0", allocations)
	}
}
