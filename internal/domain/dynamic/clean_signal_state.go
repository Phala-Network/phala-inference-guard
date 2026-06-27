package dynamic

import "github.com/Phala-Network/phala-inference-guard/internal/domain/decision"

func evaluateCleanSignalState(cfg Config, signals cleanSignals, ttft cleanTTFTStage) decision.SignalResult {
	return decision.EvaluateSignals(decision.SignalConfig{
		KVYellow:       cfg.KVYellow,
		KVRed:          cfg.KVRed,
		RunningYellow:  cfg.RunningYellow,
		RunningRed:     cfg.RunningRed,
		WaitingYellow:  cfg.WaitingYellow,
		WaitingRed:     cfg.WaitingRed,
		PreemptRed:     cfg.PreemptRed,
		UserTPSEnabled: cfg.UserTPSEnabled,
		UserTPSYellow:  cfg.UserTPSYellow,
		UserTPSRed:     cfg.UserTPSRed,
		UserTPSMinRun:  cfg.UserTPSMinRun,
		TTFTEnabled:    cfg.TTFTEnabled,
	}, decision.SignalInput{
		Running:                   signals.Running,
		Waiting:                   signals.Waiting,
		DecodeRunning:             signals.DecodeRunning,
		KVCacheUsage:              signals.KVCacheUsage,
		PreemptionDelta:           signals.PreemptionDelta,
		UserTPS:                   signals.UserTPS,
		UserTPSValid:              signals.QOSTPSValid,
		UserTPSYellowReady:        signals.UserTPSYellowReady,
		UserTPSRedReady:           signals.UserTPSRedReady,
		RepresentativeUserTPSLoad: signals.RepresentativeUserTPSLoad,
		PrefillTransition:         signals.PrefillFreeze,
		TTFTYellowReady:           ttft.Assessment.YellowReady,
		TTFTRedReady:              ttft.Assessment.RedReady,
	})
}

func cleanEnforceQOSLimit(cfg Config, signals cleanSignals) bool {
	if !signals.RepresentativeUserTPSLoad || !cfg.UserTPSEnabled || signals.PrefillFreeze || !signals.QOSTPSValid || signals.DecodeRunning < cfg.UserTPSMinRun || signals.DecodeRunning <= 0 {
		return false
	}
	if signals.UserTPS < cfg.UserTPSRed {
		return signals.UserTPSRedReady
	}
	if signals.UserTPS < cfg.UserTPSYellow {
		return signals.UserTPSYellowReady
	}
	return false
}
