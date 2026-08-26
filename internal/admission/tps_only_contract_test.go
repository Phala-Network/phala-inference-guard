package admission

import (
	"testing"
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestTPSOnlyControllerDoesNotProtectOnInputSize(t *testing.T) {
	now := time.Unix(30_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	result := controller.Admit(
		now.Add(time.Millisecond),
		testEstimate(capability.MaximumInputTokens+1, capability.MaximumInputTokens+1, 256),
	)
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of input size: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("input-size invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerDoesNotProtectOnKVObservation(t *testing.T) {
	now := time.Unix(30_100, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, capability.KVHardLimitTokens-64, 0, 0, 1, 0),
	)

	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_024, 256))
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of KV observation: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("KV invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerDoesNotProtectOnPrefillLiability(t *testing.T) {
	now := time.Unix(30_200, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	first := controller.Admit(now.Add(time.Millisecond), testEstimate(200*1024, 200*1024, 256))
	if !first.Decision.Admitted() {
		t.Fatalf("TPS-only prefill invariant setup admission: %+v", first.Decision)
	}
	defer first.Handle.Terminate(TerminalCancel)

	second := controller.Admit(now.Add(2*time.Millisecond), testEstimate(100*1024, 100*1024, 256))
	if !second.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of Prefill liability: %+v", second.Decision)
	}
	if !second.Handle.Terminate(TerminalCancel) {
		t.Fatal("Prefill invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerFallsBackWhenDetailedTokenEstimateIsMissing(t *testing.T) {
	now := time.Unix(30_300, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	result := controller.Admit(now.Add(time.Millisecond), predictive.RequestEstimate{})
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission did not use the one-sequence fallback: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("fallback reservation rollback failed")
	}
}
