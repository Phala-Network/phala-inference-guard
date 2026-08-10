package predictive

import (
	"math"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const (
	decodeInterferenceReason = RequestAwareReason("decode_interference")
	decodePressureSource     = RequestAwarePressureSource("decode")
)

func TestV0127DecodeEnvelopeValidatesConfigurationAndCapability(t *testing.T) {
	if _, err := NewDecodeEnvelope(DecodeEnvelopeConfig{}); err == nil {
		t.Fatal("zero Decode envelope budget was accepted")
	}
	envelope, err := NewDecodeEnvelope(DecodeEnvelopeConfig{InterferenceBudgetTokens: 64 * 1024})
	if err != nil {
		t.Fatalf("NewDecodeEnvelope: %v", err)
	}
	profile := BackendCapabilityProfile{PrefillRegularTokens: 64 * 1024}
	if !envelope.MatchesCapability(profile) {
		t.Fatal("Decode envelope did not match its immutable regular-token budget")
	}
	profile.PrefillRegularTokens++
	if envelope.MatchesCapability(profile) {
		t.Fatal("Decode envelope matched a different regular-token budget")
	}
	var missing *DecodeEnvelope
	if missing.MatchesCapability(profile) {
		t.Fatal("nil Decode envelope matched a capability profile")
	}
}

func TestV0127DecodeEnvelopeRejectsInvalidInput(t *testing.T) {
	envelope, err := NewDecodeEnvelope(DecodeEnvelopeConfig{InterferenceBudgetTokens: 64 * 1024})
	if err != nil {
		t.Fatalf("NewDecodeEnvelope: %v", err)
	}
	for _, input := range []DecodeEnvelopeInput{
		{},
		{PostAdmitPrefillTokens: 1, ActiveDecodeSequences: -1},
	} {
		result := envelope.Evaluate(input)
		if result.Admit || !result.HardProtection || result.Reason != RequestAwareReasonInvalid {
			t.Fatalf("invalid input=%+v result=%+v, want hard invalid protection", input, result)
		}
	}
	var missing *DecodeEnvelope
	if result := missing.Evaluate(DecodeEnvelopeInput{PostAdmitPrefillTokens: 1}); result.Admit || !result.HardProtection || result.Reason != RequestAwareReasonInvalid {
		t.Fatalf("nil envelope result=%+v, want hard invalid protection", result)
	}
}

func TestV0127PolicyBoundsDecodeInterferenceBeforeForward(t *testing.T) {
	const (
		kib         = int64(1024)
		regular     = 64 * kib
		decodeUsers = 4
	)
	policy := newLargeRequestAwareTestPolicy(t)

	t.Run("equality admits", func(t *testing.T) {
		const candidate = regular / decodeUsers
		decision := policy.Evaluate(v0126DecodeEnvelopeInput(candidate, decodeUsers))
		if decision.Action != RequestAwareAdmit || decision.Reason != RequestAwareReasonOpen ||
			decision.PostAdmitPendingPrefillTokens != candidate || decision.Pressure != 0 {
			t.Fatalf("boundary decision=%+v, want equality admitted", decision)
		}
	})

	t.Run("one token over protects", func(t *testing.T) {
		const candidate = regular/decodeUsers + 1
		decision := policy.Evaluate(v0126DecodeEnvelopeInput(candidate, decodeUsers))
		wantPressure := float64(candidate*decodeUsers) / float64(regular)
		if decision.Action != RequestAwareSizeProtect || decision.Reason != decodeInterferenceReason ||
			decision.PressureSource != decodePressureSource || decision.PostAdmitPendingPrefillTokens != candidate ||
			math.Abs(decision.Pressure-wantPressure) > 1e-12 || decision.AllowanceTokens != 0 {
			t.Fatalf("over-boundary decision=%+v, want decode protection pressure=%f", decision, wantPressure)
		}
	})

	t.Run("zero decode remains work conserving", func(t *testing.T) {
		const candidate = 600 * kib
		decision := policy.Evaluate(v0126DecodeEnvelopeInput(candidate, 0))
		if decision.Action != RequestAwareAdmit || decision.Reason != RequestAwareReasonOpen ||
			decision.PrefillClass != RequestAwarePrefillQuiescent {
			t.Fatalf("zero-decode decision=%+v, want large Prefill admitted", decision)
		}
	})

	t.Run("quiescent decode ownership", func(t *testing.T) {
		const candidate = 600 * kib
		decision := policy.Evaluate(v0126DecodeEnvelopeInput(candidate, 1))
		if decision.Action != RequestAwareSizeProtect || decision.Reason != decodeInterferenceReason ||
			decision.PressureSource != decodePressureSource || decision.PrefillClass != RequestAwarePrefillQuiescent {
			t.Fatalf("quiescent-with-decode decision=%+v, want DecodeEnvelope ownership", decision)
		}
	})

	t.Run("multiplication overflow fails closed", func(t *testing.T) {
		maxInt := int(^uint(0) >> 1)
		if int64(maxInt) <= math.MaxInt64/2 {
			t.Skip("test requires a 64-bit Go target")
		}
		decision := policy.Evaluate(v0126DecodeEnvelopeInput(2, maxInt))
		if decision.Action != RequestAwareHardProtect || decision.Reason != RequestAwareReasonInvalid ||
			decision.PressureSource != RequestAwarePressureNone {
			t.Fatalf("overflow decision=%+v, want fail-closed invalid protection", decision)
		}
	})

	t.Run("resource protection keeps first precedence", func(t *testing.T) {
		input := v0126DecodeEnvelopeInput(32*kib, decodeUsers)
		input.UsedTokens = 3_760_000
		input.RequestReservedTokens = 32 * kib
		decision := policy.Evaluate(input)
		if decision.Action != RequestAwareHardProtect || decision.Reason != RequestAwareReasonKV ||
			decision.PressureSource != RequestAwarePressureNone {
			t.Fatalf("resource precedence decision=%+v, want hard KV protection", decision)
		}
	})

	t.Run("prefill interference keeps second precedence", func(t *testing.T) {
		input := v0126DecodeEnvelopeInput(32*kib, decodeUsers)
		input.PendingPrefillSequences = 1
		input.PendingPrefillTokens = 250 * kib
		decision := policy.Evaluate(input)
		if decision.Action != RequestAwareSizeProtect || decision.Reason != RequestAwareReasonPrefillBudget ||
			decision.PressureSource != RequestAwarePressurePrefill {
			t.Fatalf("Prefill precedence decision=%+v, want aggregate Prefill protection", decision)
		}
	})
}

func TestV0127InterferenceGateDoesNotOwnDecodeActivity(t *testing.T) {
	gate, err := NewInterferenceGate(InterferenceGateConfig{
		PrefillRegularTokens:         64 * 1024,
		PrefillExclusiveTokens:       256 * 1024,
		PrefillQuiescentTokens:       512 * 1024,
		PrefillAggregateBudgetTokens: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("NewInterferenceGate: %v", err)
	}
	result := gate.Evaluate(InterferenceGateInput{
		EstimatedPrefillTokens: 600 * 1024,
		Running:                4,
		EffectiveSequences:     4,
	})
	if !result.Admit || result.Reason != RequestAwareReasonOpen || result.PrefillClass != RequestAwarePrefillQuiescent {
		t.Fatalf("quiescent InterferenceGate result=%+v, want only Prefill-vs-Prefill ownership", result)
	}
}

func TestV0128InterferenceGateAcceptsReconciledDecodeBelowRawRunning(t *testing.T) {
	gate, err := NewInterferenceGate(InterferenceGateConfig{
		PrefillRegularTokens:         64 * 1024,
		PrefillExclusiveTokens:       256 * 1024,
		PrefillQuiescentTokens:       512 * 1024,
		PrefillAggregateBudgetTokens: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("NewInterferenceGate: %v", err)
	}
	result := gate.Evaluate(InterferenceGateInput{
		EstimatedPrefillTokens: 4 * 1024,
		Running:                12,
		EffectiveSequences:     0,
	})
	if !result.Admit || result.HardProtection || result.Reason != RequestAwareReasonOpen {
		t.Fatalf("reconciled InterferenceGate result=%+v, want raw running independent of active Decode", result)
	}
}

func TestV0127ManagerSumsPendingPrefillWithoutInventingDecodeUsers(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{DecodeSequences: 4})
	input := RequestAwareInput{
		MetricsFresh:       true,
		IdentityValid:      true,
		CapacityTokens:     4 * 1024 * 1024,
		Running:            4,
		EffectiveSequences: 4,
	}

	for index := 1; index <= 4; index++ {
		requestID := "same-snapshot-" + string(rune('0'+index))
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), requestID, requestAwareManagerCost(4*kib, 0), 4*kib, policy, input,
		)
		if !result.Reserved || result.Decision.Action != RequestAwareAdmit {
			t.Fatalf("same-snapshot reservation %d=%+v, want admitted", index, result)
		}
	}

	blocked := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "same-snapshot-5", requestAwareManagerCost(4*kib, 0), 4*kib, policy, input,
	)
	if blocked.Reserved || blocked.Decision.Action != RequestAwareSizeProtect ||
		blocked.Decision.Reason != decodeInterferenceReason || blocked.Decision.PressureSource != decodePressureSource ||
		blocked.Decision.PendingPrefillTokens != 16*kib || blocked.Decision.PostAdmitPendingPrefillTokens != 20*kib ||
		blocked.Decision.EffectiveSequences != 4 {
		t.Fatalf("same-snapshot fifth decision=%+v, want phase-correct Decode envelope protection", blocked)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 4 {
		t.Fatalf("same-snapshot manager=%+v, want exactly four reservations", snapshot)
	}
	for _, requestID := range []string{"same-snapshot-1", "same-snapshot-2", "same-snapshot-3", "same-snapshot-4"} {
		if !manager.Terminate(requestID, TerminalExpired) {
			t.Fatalf("terminate %s failed", requestID)
		}
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.Virtual.Upper.PendingPrefillSequences != 0 {
		t.Fatalf("same-snapshot reservations leaked: %+v", snapshot)
	}
}

func v0126DecodeEnvelopeInput(prefillTokens int64, decodeSequences int) RequestAwareInput {
	return RequestAwareInput{
		MetricsFresh:           true,
		IdentityValid:          true,
		CapacityTokens:         4 * 1024 * 1024,
		RequestReservedTokens:  prefillTokens,
		SelectionInputTokens:   prefillTokens,
		EstimatedPrefillTokens: prefillTokens,
		Running:                decodeSequences,
		EffectiveSequences:     decodeSequences,
	}
}
