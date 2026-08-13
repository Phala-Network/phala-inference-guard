//go:build !race

package kvadmission

import (
	"bytes"
	"sort"
	"testing"
	"time"
)

// Race instrumentation deliberately changes latency by more than an order of
// magnitude. Correctness tests run in both builds; this native-speed acceptance
// gate runs only in an ordinary c21 build.
func TestEstimatorMaximumBodyLatencyAndAllocations(t *testing.T) {
	const maximumBodyBytes = 4 * 1024 * 1024
	longString := maximumBodyEstimatorFixture(t, maximumBodyBytes)
	shortLexemes := repeatedLexemeEstimatorFixture(t, maximumBodyBytes, []byte("x "))
	shortQZLexemes := repeatedLexemeEstimatorFixture(t, maximumBodyBytes, []byte("qz "))
	denseDigits := repeatedLexemeEstimatorFixture(t, maximumBodyBytes, []byte("01"))
	manyStrings := manyStringEstimatorFixture(t, maximumBodyBytes)
	for _, fixture := range []struct {
		name string
		body []byte
	}{
		{name: "long_string", body: longString},
		{name: "short_lexemes", body: shortLexemes},
		{name: "short_qz_lexemes", body: shortQZLexemes},
		{name: "dense_digits", body: denseDigits},
		{name: "many_strings", body: manyStrings},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cfg := DefaultEstimatorConfig()
			allocations := testing.AllocsPerRun(100, func() {
				if cost := EstimateJSON(fixture.body, 256, true, cfg); !cost.Supported {
					t.Fatalf("maximum-body cost=%+v", cost)
				}
			})
			if allocations != 0 {
				t.Fatalf("maximum-body allocations=%g want 0", allocations)
			}

			const runs = 101
			durations := make([]time.Duration, runs)
			for index := range durations {
				started := time.Now()
				cost := EstimateJSON(fixture.body, 256, true, cfg)
				durations[index] = time.Since(started)
				if !cost.Supported {
					t.Fatalf("maximum-body cost=%+v", cost)
				}
			}
			sort.Slice(durations, func(left, right int) bool {
				return durations[left] < durations[right]
			})
			p50 := durations[len(durations)/2]
			p99 := durations[(len(durations)*99)/100]
			t.Logf("body_bytes=%d p50=%s p99=%s allocations=%g", len(fixture.body), p50, p99, allocations)
			if p99 >= 100*time.Millisecond {
				t.Fatalf("maximum-body p99=%s exceeds 100ms", p99)
			}
		})
	}
}

func repeatedLexemeEstimatorFixture(t *testing.T, targetBytes int, unit []byte) []byte {
	t.Helper()
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	payloadBytes := targetBytes - len(prefix) - len(suffix)
	if payloadBytes <= 0 {
		t.Fatal("short-lexeme fixture has no payload")
	}
	if len(unit) == 0 {
		t.Fatal("short-lexeme fixture has an empty unit")
	}
	lexemes := bytes.Repeat(unit, ceilDiv(payloadBytes, len(unit)))
	body := make([]byte, 0, targetBytes)
	body = append(body, prefix...)
	body = append(body, lexemes[:payloadBytes]...)
	body = append(body, suffix...)
	return body
}

func maximumBodyEstimatorFixture(t *testing.T, targetBytes int) []byte {
	t.Helper()
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	payloadBytes := targetBytes - len(prefix) - len(suffix)
	if payloadBytes <= 0 {
		t.Fatal("maximum-body fixture has no payload")
	}
	body := make([]byte, 0, targetBytes)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte{'a'}, payloadBytes)...)
	body = append(body, suffix...)
	return body
}

func manyStringEstimatorFixture(t *testing.T, targetBytes int) []byte {
	t.Helper()
	prefix := []byte(`{"prompt":[`)
	item := []byte(`"a",`)
	suffix := []byte(`"a"]}`)
	itemCount := (targetBytes - len(prefix) - len(suffix)) / len(item)
	if itemCount <= 0 {
		t.Fatal("many-string fixture has no items")
	}
	body := make([]byte, 0, targetBytes)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat(item, itemCount)...)
	body = append(body, suffix...)
	return body
}
