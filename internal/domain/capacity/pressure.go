package capacity

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PressureLimitResult struct {
	Limit        int
	Reason       string
	TargetReason string
}

type pressureTargetResult struct {
	Target  int
	Limited bool
	Reason  string
}

func SeverePressure(cfg Config, waiting int, kvValue float64, preemptionDelta uint64) bool {
	if preemptionDelta > 0 {
		return true
	}
	if cfg.WaitingRed > 0 && waiting >= cfg.WaitingRed {
		return true
	}
	return cfg.KVRed > 0 && kvValue >= cfg.KVRed
}

func OverloadPressureTarget(cfg Config, baseLimit, running, decodeRunning, waiting int, kvValue float64, preemptionDelta uint64, userTPS float64, qosRedReady bool) (int, bool) {
	result := overloadPressureTarget(cfg, baseLimit, running, decodeRunning, waiting, kvValue, preemptionDelta, userTPS, qosRedReady)
	return result.Target, result.Limited
}

func overloadPressureTarget(cfg Config, baseLimit, running, decodeRunning, waiting int, kvValue float64, preemptionDelta uint64, userTPS float64, qosRedReady bool) pressureTargetResult {
	if baseLimit <= 0 || running <= 0 {
		return pressureTargetResult{Target: baseLimit, Reason: "no_running"}
	}
	headroom := cfg.PressureHeadroom
	if headroom < 1 {
		headroom = 1
	}
	minLimit := cfg.PressureMinLimit
	if minLimit < 1 {
		minLimit = 1
	}
	target := baseLimit
	limited := false
	reason := "base_limit"
	apply := func(value int, candidateReason string) {
		value = num.ClampInt(value, minLimit, baseLimit)
		if value < target {
			target = value
			limited = true
			reason = candidateReason
		}
	}
	if waiting > 0 {
		waitDecay := int(math.Ceil(float64(waiting) * overloadWaitingDecayRatio))
		if waitDecay < 1 {
			waitDecay = 1
		}
		apply(running-waitDecay-headroom, "backend_waiting")
	}
	if preemptionDelta > 0 {
		apply(running-num.MaxInt(2*headroom, headroom+1), "preemption")
	}
	if cfg.KVRed > 0 && kvValue >= cfg.KVRed {
		apply(running-num.MaxInt(2*headroom, headroom+1), "kv_red")
	} else if cfg.KVYellow > 0 && kvValue >= cfg.KVYellow {
		apply(running-headroom, "kv_yellow")
	}
	if qosRedReady && cfg.UserTPSRed > 0 && userTPS > 0 && userTPS < cfg.UserTPSRed {
		apply(decodeRunning-num.MaxInt(2*headroom, headroom+1), "pig_red")
	}
	if !limited {
		return pressureTargetResult{Target: baseLimit, Reason: "base_limit"}
	}
	return pressureTargetResult{Target: target, Limited: true, Reason: reason}
}

func primaryPressureReason(cfg Config, waiting int, kvValue float64, preemptionDelta uint64, userTPS float64, qosRedReady bool) string {
	if preemptionDelta > 0 {
		return "preemption"
	}
	if waiting > 0 {
		return "backend_waiting"
	}
	if cfg.KVRed > 0 && kvValue >= cfg.KVRed {
		return "kv_red"
	}
	if cfg.KVYellow > 0 && kvValue >= cfg.KVYellow {
		return "kv_yellow"
	}
	if qosRedReady && cfg.UserTPSRed > 0 && userTPS > 0 && userTPS < cfg.UserTPSRed {
		return "pig_red"
	}
	return "base_limit"
}

func RecoverPressureCap(cap *PressureCap, cfg Config, baseLimit, running, waiting, decodeRunning int, generationTPS float64, generationTPSValid bool, demandPressure bool) {
	if !cfg.PressureEnabled || !cfg.UserTPSEnabled || baseLimit <= 0 || running <= 0 || waiting > 0 || !demandPressure {
		return
	}
	current := int(cap.Load())
	if current <= 0 || current >= baseLimit {
		return
	}
	if !generationTPSValid || decodeRunning < cfg.UserTPSMinRun || decodeRunning <= 0 || cfg.UserTPSYellow <= 0 {
		return
	}
	userTPS := generationTPS / float64(decodeRunning)
	if userTPS < cfg.UserTPSYellow {
		return
	}
	step := cfg.PressureHeadroom + 1
	if step < 1 {
		step = 1
	}
	target := current + step
	if running >= current && running+step > target {
		target = running + step
	}
	if target > baseLimit {
		target = baseLimit
	}
	for {
		observed := cap.Load()
		if observed <= 0 || int(observed) >= target {
			return
		}
		if cap.compareAndSwap(observed, int64(target)) {
			return
		}
	}
}

func PressureLimit(cap *PressureCap, cfg Config, baseLimit, running, waiting, decodeRunning int, kvValue float64, preemptionDelta uint64, userTPS float64, qosHealthy, qosRedReady, prefillTransition bool) int {
	return EvaluatePressureLimit(cap, cfg, baseLimit, running, waiting, decodeRunning, kvValue, preemptionDelta, userTPS, qosHealthy, qosRedReady, prefillTransition).Limit
}

func EvaluatePressureLimit(cap *PressureCap, cfg Config, baseLimit, running, waiting, decodeRunning int, kvValue float64, preemptionDelta uint64, userTPS float64, qosHealthy, qosRedReady, prefillTransition bool) PressureLimitResult {
	if !cfg.PressureEnabled || baseLimit <= 0 || running <= 0 {
		return PressureLimitResult{Limit: baseLimit, Reason: "disabled", TargetReason: "base_limit"}
	}
	if prefillTransition {
		return PressureLimitResult{Limit: baseLimit, Reason: "prefill_transition", TargetReason: "base_limit"}
	}
	limit := baseLimit
	reason := "base_limit"
	targetReason := "base_limit"
	headroom := cfg.PressureHeadroom
	if headroom < 0 {
		headroom = 0
	}
	minLimit := cfg.PressureMinLimit
	if minLimit < 0 {
		minLimit = 0
	}
	apply := func(target int, candidateReason, candidateTargetReason string) {
		if target < minLimit {
			target = minLimit
		}
		if target < limit {
			limit = target
			reason = candidateReason
			targetReason = candidateTargetReason
		}
	}
	learnPressureCap := func(candidateReason, candidateTargetReason string) {
		if running < cfg.PressureLearnMinRun {
			return
		}
		target := int(math.Floor(float64(running) * cfg.PressureLearnRatio))
		if target < minLimit {
			target = minLimit
		}
		for {
			current := cap.Load()
			if current > 0 && int64(target) >= current {
				break
			}
			if cap.compareAndSwap(current, int64(target)) {
				break
			}
		}
		apply(target, candidateReason, candidateTargetReason)
	}
	representativeSeverePressure := SeverePressure(cfg, waiting, kvValue, preemptionDelta) && (preemptionDelta > 0 || running >= cfg.PressureLearnMinRun)
	if representativeSeverePressure {
		overload := overloadPressureTarget(cfg, baseLimit, running, decodeRunning, waiting, kvValue, preemptionDelta, userTPS, qosRedReady)
		pressureReason := overload.Reason
		if pressureReason == "base_limit" {
			pressureReason = primaryPressureReason(cfg, waiting, kvValue, preemptionDelta, userTPS, qosRedReady)
		}
		if waiting > 0 || preemptionDelta > 0 || KVPressureActive(cfg, kvValue) {
			learnPressureCap("severe_pressure", pressureReason)
		}
		if overload.Limited {
			apply(overload.Target, "severe_pressure", overload.Reason)
		}
		if prefillTransition {
			return PressureLimitResult{Limit: limit, Reason: reason, TargetReason: targetReason}
		}
	}
	if learned := int(cap.Load()); learned > 0 && !prefillTransition {
		apply(learned, "learned_cap", "learned_pressure_cap")
	}
	if waiting >= cfg.WaitingYellow && waiting > 0 {
		if !prefillTransition {
			learnPressureCap("waiting_pressure", "backend_waiting")
			apply(running-waiting-headroom, "waiting_pressure", "waiting_yellow")
		}
	}
	if qosHealthy {
		if cfg.KVYellow > 0 && kvValue >= cfg.KVYellow {
			target := int(math.Floor(float64(running) * cfg.KVYellow / kvValue))
			apply(target-headroom, "healthy_kv_headroom", "kv_yellow")
		}
	} else {
		if cfg.KVRed > 0 && kvValue >= cfg.KVRed {
			learnPressureCap("kv_pressure", "kv_red")
			apply(running-num.MaxInt(2*headroom, headroom+1), "kv_pressure", "kv_red")
		} else if cfg.KVYellow > 0 && kvValue >= cfg.KVYellow {
			learnPressureCap("kv_pressure", "kv_yellow")
			apply(running-headroom, "kv_pressure", "kv_yellow")
		}
	}
	return PressureLimitResult{Limit: limit, Reason: reason, TargetReason: targetReason}
}
