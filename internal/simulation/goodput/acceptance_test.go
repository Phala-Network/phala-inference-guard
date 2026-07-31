package goodput

import "testing"

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
}
