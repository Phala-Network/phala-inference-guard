package goodput

import (
	"testing"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestTokenizerFirstPredictiveAdmissionMeetsCompletionGoodputGate(t *testing.T) {
	suite, err := RunAcceptanceSuite()
	if err != nil {
		t.Fatalf("run acceptance suite: %v", err)
	}
	requiredScenarios := []string{
		"same_poll_short_burst_near_kv",
		"mixed_short_64k_128k",
		"long_prompt_short_decode",
		"short_prompt_long_decode",
		"progressive_kv_arrival_after_scrapes",
		"many_decode_sequences_low_kv",
		"completion_before_poll",
		"stale_waiting_after_owned_completion",
		"cancel_during_prefill_and_decode",
		"local_qos_reject_after_reservation",
		"timeout_and_upstream_failure",
		"stale_or_reset_backend_epoch",
		"tokenizer_template_mismatch",
		"unsupported_tools_or_multimodal",
		"near_capacity_atomic_burst",
		"repeated_prefixes_charged_cold",
		"high_kv_headroom_low_tps",
		"low_kv_excessive_ttft",
		"calibration_error_distribution_shift",
	}
	seen := make(map[string]bool, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		seen[scenario.Name] = true
		for _, policy := range []PolicyName{PolicyCurrentThreshold, PolicyV090KVOnly, PolicyExactKVOnly, PolicyPredictiveQoS} {
			if _, ok := scenario.Policies[policy]; !ok {
				t.Fatalf("scenario %s has no %s result", scenario.Name, policy)
			}
		}
	}
	for _, name := range requiredScenarios {
		if !seen[name] {
			t.Fatalf("acceptance suite is missing scenario %s", name)
		}
	}

	full := suite.Aggregate(PolicyPredictiveQoS)
	current := suite.Aggregate(PolicyCurrentThreshold)
	kvOnly := suite.Aggregate(PolicyV090KVOnly)
	t.Logf("aggregate current=%+v", current)
	t.Logf("aggregate v0.9.0-kv-only=%+v", kvOnly)
	t.Logf("aggregate predictive-qos=%+v", full)
	for _, scenario := range suite.Scenarios {
		t.Logf("scenario=%s current_goodput=%d kv_only_goodput=%d exact_kv_goodput=%d predictive_goodput=%d predictive_safety=%d",
			scenario.Name,
			scenario.Policies[PolicyCurrentThreshold].CompletionTokenGoodput,
			scenario.Policies[PolicyV090KVOnly].CompletionTokenGoodput,
			scenario.Policies[PolicyExactKVOnly].CompletionTokenGoodput,
			scenario.Policies[PolicyPredictiveQoS].CompletionTokenGoodput,
			scenario.Policies[PolicyPredictiveQoS].SafetyViolations(),
		)
	}
	if full.ReservationLeaks != 0 {
		t.Fatalf("predictive QoS reservation leaks = %d, want 0", full.ReservationLeaks)
	}
	if full.FalseAccepts != 0 {
		t.Fatalf("predictive QoS false accepts = %d, want 0", full.FalseAccepts)
	}
	if full.SafetyViolations() > current.SafetyViolations() || full.SafetyViolations() > kvOnly.SafetyViolations() {
		t.Fatalf("predictive QoS safety violations=%d exceed current=%d or KV-only=%d", full.SafetyViolations(), current.SafetyViolations(), kvOnly.SafetyViolations())
	}
	if got := improvementPercent(full.CompletionTokenGoodput, current.CompletionTokenGoodput); got < 5 {
		t.Fatalf("predictive QoS completion-token goodput improvement vs current = %.2f%%, want >= 5%%", got)
	}
	if got := improvementPercent(full.CompletionTokenGoodput, kvOnly.CompletionTokenGoodput); got < 5 {
		t.Fatalf("predictive QoS completion-token goodput improvement vs KV-only = %.2f%%, want >= 5%%", got)
	}

	strictImprovements := 0
	var fullLong, bestBaselineLong int64
	for _, scenario := range suite.Scenarios {
		candidate := scenario.Policies[PolicyPredictiveQoS].CompletionTokenGoodput
		currentGoodput := scenario.Policies[PolicyCurrentThreshold].CompletionTokenGoodput
		kvGoodput := scenario.Policies[PolicyV090KVOnly].CompletionTokenGoodput
		if candidate > currentGoodput && candidate > kvGoodput {
			strictImprovements++
		}
		if scenario.LongPromptSuite {
			fullLong += candidate
			if currentGoodput > kvGoodput {
				bestBaselineLong += currentGoodput
			} else {
				bestBaselineLong += kvGoodput
			}
		}
	}
	if strictImprovements < 3 {
		t.Fatalf("strict predictive QoS goodput improvements = %d scenarios, want >= 3", strictImprovements)
	}
	if got := improvementPercent(fullLong, bestBaselineLong); got < -1 {
		t.Fatalf("cache-cold long-prompt goodput regression = %.2f%%, want no worse than -1%%", got)
	}

	repeated, ok := scenarioByName(suite, "repeated_prefixes_charged_cold")
	if !ok {
		t.Fatal("repeated-prefix scenario disappeared after required-scenario validation")
	}
	wantRepeatedPeak := int64(4) * roundUpForTest(3_074+256, simulationBlock)
	if got := repeated.Policies[PolicyPredictiveQoS].PeakReservedKVTokens; got != wantRepeatedPeak {
		t.Fatalf("repeated-prefix predictive peak reserved KV = %d, want four full cache-cold costs = %d", got, wantRepeatedPeak)
	}
}

func TestTokenizerLatencyEvidenceIsChargedToTTFT(t *testing.T) {
	for _, test := range []struct {
		tokens int64
		want   time.Duration
	}{
		{tokens: 49, want: 52_539 * time.Nanosecond},
		{tokens: 3_074, want: 9 * time.Millisecond},
		{tokens: 24_578, want: 132_639 * time.Microsecond},
		{tokens: 65_538, want: 650 * time.Millisecond},
		{tokens: 131_074, want: 1_516 * time.Millisecond},
	} {
		if got := tokenizerP95(test.tokens); got != test.want {
			t.Fatalf("tokenizer p95 at %d tokens = %s, want %s", test.tokens, got, test.want)
		}
	}
	profile := (scenarioSpec{}).serviceProfile()
	state := &actualState{active: make(map[string]*activeRequest)}
	request := request("latency-128k", 0, 131_074, 64, 64)
	withoutTokenizer := state.evaluate(profile, false, request, 0)
	withTokenizer := state.evaluate(profile, true, request, 0)
	if delta := withTokenizer.ttft - withoutTokenizer.ttft; delta != 1_516*time.Millisecond {
		t.Fatalf("128k tokenizer TTFT charge = %s, want 1.516s", delta)
	}
}

func TestObservedKVGrowsWithActualDecodeProgress(t *testing.T) {
	active := &activeRequest{
		spec:          request("progressive", 0, 49, 100_000, 1_000),
		admittedAt:    0,
		terminalAt:    10_100 * time.Millisecond,
		terminalCause: runtimepredictive.TerminalCompleted,
		ttft:          100 * time.Millisecond,
		tps:           100,
	}
	state := &actualState{active: map[string]*activeRequest{"progressive": active}}
	if got := state.currentKV(0); got != 64 {
		t.Fatalf("KV at admission = %d, want one rounded 49-token input block", got)
	}
	if got := state.currentKV(5_100 * time.Millisecond); got != 576 {
		t.Fatalf("KV after 500 generated tokens = %d, want rounded 549-token context", got)
	}
	if got := state.currentKV(active.terminalAt); got != 1_088 {
		t.Fatalf("KV at completion = %d, want rounded 1,049-token actual context", got)
	}
}

func scenarioByName(suite SuiteResult, name string) (ScenarioResult, bool) {
	for _, scenario := range suite.Scenarios {
		if scenario.Name == name {
			return scenario, true
		}
	}
	return ScenarioResult{}, false
}

func roundUpForTest(value, block int64) int64 {
	return ((value + block - 1) / block) * block
}
