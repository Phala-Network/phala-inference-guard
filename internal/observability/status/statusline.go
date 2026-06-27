package status

import (
	"fmt"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type TierSnapshot struct {
	BasicInflight   int64
	BasicWaiting    int64
	BasicLimit      int
	PremiumInflight int64
	PremiumWaiting  int64
	PremiumReserved int
}

type BackendSnapshot struct {
	Name      string
	Running   int
	Waiting   int
	Inflight  int64
	TTFTValid bool
	TTFTP95   float64
	TTFTP99   float64
	Failed    bool
}

type Input struct {
	Version            string
	Snapshot           dynamic.Snapshot
	QueueCurrent       int64
	DynamicRejected    uint64
	BackendUnavailable uint64
	Tier               TierSnapshot
	Backends           []BackendSnapshot
}

func Format(input Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pig_status v=%s", input.Version)
	writeBackendSummary(&b, input.Snapshot)
	writePIG(&b, input)
	writeBackendDetail(&b, input.Backends)
	return b.String()
}

func writeBackendSummary(b *strings.Builder, snapshot dynamic.Snapshot) {
	state := snapshot.DecisionState()
	if state == "" {
		state = "unknown"
	}
	reasons := snapshot.DecisionPrimaryReasons()
	level := state
	if len(reasons) > 0 {
		level += ":" + strings.Join(reasons, ",")
	}
	fmt.Fprintf(b, " backend={state=%s backend=%d/%d running=%d waiting=%d prefill=%d decode=%d", level, snapshot.BackendCount-snapshot.BackendFailed, snapshot.BackendCount, snapshot.Running, snapshot.Waiting, snapshot.PrefillProtected, snapshot.DecodeRunning)
	if snapshot.PrefillTransition {
		fmt.Fprintf(b, " prefill_transition=1")
	}
	if snapshot.PrefillSettling {
		fmt.Fprintf(b, " prefill_settling=1")
	}
	fmt.Fprintf(b, " kv=%.3f", snapshot.KVCacheUsage)
	if snapshot.GenerationTPSValid {
		fmt.Fprintf(b, " gen_tps=%.1f user_tps=%.1f", snapshot.GenerationTPS, snapshot.UserTPS)
	} else {
		fmt.Fprintf(b, " gen_tps=na user_tps=na")
	}
	if snapshot.TTFTSmoothedP95 > 0 {
		fmt.Fprintf(b, " ttft_p95=%.2f", snapshot.TTFTSmoothedP95)
	}
	if snapshot.TTFTSmoothedP99 > 0 {
		fmt.Fprintf(b, " ttft_p99=%.2f", snapshot.TTFTSmoothedP99)
	}
	if snapshot.TTFTSource != "" && snapshot.TTFTSource != "disabled" {
		fmt.Fprintf(b, " ttft_src=%s", snapshot.TTFTSource)
	}
	fmt.Fprintf(b, " preempt=%d}", snapshot.Preemptions)
}

func writePIG(b *strings.Builder, input Input) {
	snapshot := input.Snapshot
	tier := input.Tier
	finalLimitReason := snapshot.FinalLimitReason
	if finalLimitReason == "" {
		finalLimitReason = "unknown"
	}
	capacityLearnReason := snapshot.CapacityLearnReason
	if capacityLearnReason == "" {
		capacityLearnReason = "unknown"
	}
	capacityTargetReason := snapshot.CapacityTargetReason
	if capacityTargetReason == "" {
		capacityTargetReason = "unknown"
	}
	pressureReason := snapshot.PressureReason
	if pressureReason == "" {
		pressureReason = "unknown"
	}
	pressureTargetReason := snapshot.PressureTargetReason
	if pressureTargetReason == "" {
		pressureTargetReason = "unknown"
	}
	prefillReason := snapshot.PrefillReason
	if prefillReason == "" {
		prefillReason = "unknown"
	}
	prefillTargetReason := snapshot.PrefillTargetReason
	if prefillTargetReason == "" {
		prefillTargetReason = "unknown"
	}
	ttftLearnReason := snapshot.TTFTLearnReason
	if ttftLearnReason == "" {
		ttftLearnReason = "unknown"
	}
	ttftTargetReason := snapshot.TTFTTargetReason
	if ttftTargetReason == "" {
		ttftTargetReason = "unknown"
	}
	fmt.Fprintf(b, " pig={limit=%d winner=%s state_limit=%d throughput=%d estimate=%s/%d projected=%d admit=%d cap=%d learned=%d target=%d target_reason=%s ttft_limit=%d ttft_learned=%d ttft_target=%d pressure=%d/%s/%s prefill_limit=%d/%s/%s avail=%d queue=%d reject=%d reject_delta=%d demand=%d tier_demand=%d tier_reject_delta=%d/%d backend_unavailable=%d tier_basic=%d/%d tier_premium=%d/%d tier_waiting=%d/%d learn=%s/%s ttft_learn=%s/%s/%s}", snapshot.GlobalLimit, finalLimitReason, snapshot.StateLimit, snapshot.ThroughputLimit, snapshot.CapacityEstimateConfidence, snapshot.CapacitySafeLimit, snapshot.CapacityProjectedLimit, snapshot.QOSLimit, snapshot.CapacityLimit, snapshot.CapacityLearnedLimit, snapshot.CapacityTargetLimit, capacityTargetReason, snapshot.TTFTLimit, snapshot.TTFTLearnedLimit, snapshot.TTFTTargetLimit, snapshot.PressureLimit, pressureReason, pressureTargetReason, snapshot.PrefillLimit, prefillReason, prefillTargetReason, snapshot.AvailabilityLimit, input.QueueCurrent, input.DynamicRejected, snapshot.DynamicRejectedDelta, num.BoolAsInt(snapshot.CapacityDemandPressure), num.BoolAsInt(snapshot.TierDemandPressure), snapshot.TierBasicRejectedDelta, snapshot.TierPremiumRejectedDelta, input.BackendUnavailable, tier.BasicInflight, tier.BasicLimit, tier.PremiumInflight, tier.PremiumReserved, tier.BasicWaiting, tier.PremiumWaiting, snapshot.CapacityLearnState, capacityLearnReason, snapshot.TTFTLearnState, ttftLearnReason, ttftTargetReason)
}

func writeBackendDetail(b *strings.Builder, backends []BackendSnapshot) {
	if len(backends) <= 1 {
		return
	}
	parts := make([]string, 0, len(backends))
	for _, backend := range backends {
		part := fmt.Sprintf("%s:%dr/%dw/%di", backend.Name, backend.Running, backend.Waiting, backend.Inflight)
		if backend.TTFTValid {
			part += fmt.Sprintf("/ttft%.2f/ttft99%.2f", backend.TTFTP95, backend.TTFTP99)
		}
		if backend.Failed {
			part += "/err"
		}
		parts = append(parts, part)
	}
	fmt.Fprintf(b, " backend_detail={%s}", strings.Join(parts, ";"))
}
