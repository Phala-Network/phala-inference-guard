package decision

type SignalConfig struct {
	KVYellow       float64
	KVRed          float64
	RunningYellow  int
	RunningRed     int
	WaitingYellow  int
	WaitingRed     int
	PreemptRed     uint64
	UserTPSEnabled bool
	UserTPSYellow  float64
	UserTPSRed     float64
	UserTPSMinRun  int
	TTFTEnabled    bool
}

type SignalInput struct {
	Running                   int
	Waiting                   int
	DecodeRunning             int
	KVCacheUsage              float64
	PreemptionDelta           uint64
	UserTPS                   float64
	UserTPSValid              bool
	UserTPSYellowReady        bool
	UserTPSRedReady           bool
	RepresentativeUserTPSLoad bool
	PrefillTransition         bool
	TTFTYellowReady           bool
	TTFTRedReady              bool
}

type SignalResult struct {
	State         string
	YellowReasons []string
	RedReasons    []string
}

func EvaluateSignals(cfg SignalConfig, input SignalInput) SignalResult {
	yellowReasons := []string{}
	redReasons := []string{}

	if cfg.KVYellow > 0 && input.KVCacheUsage >= cfg.KVYellow {
		yellowReasons = append(yellowReasons, "kv_cache")
	}
	if cfg.RunningYellow > 0 && input.Running >= cfg.RunningYellow {
		yellowReasons = append(yellowReasons, "running")
	}
	if cfg.WaitingYellow > 0 && input.Waiting >= cfg.WaitingYellow {
		yellowReasons = append(yellowReasons, "waiting")
	}
	if cfg.KVRed > 0 && input.KVCacheUsage >= cfg.KVRed {
		redReasons = append(redReasons, "kv_cache")
	}
	if cfg.RunningRed > 0 && input.Running >= cfg.RunningRed {
		redReasons = append(redReasons, "running")
	}
	if cfg.WaitingRed > 0 && input.Waiting >= cfg.WaitingRed {
		redReasons = append(redReasons, "waiting")
	}
	if cfg.PreemptRed > 0 && input.PreemptionDelta >= cfg.PreemptRed {
		redReasons = append(redReasons, "preemptions")
	}

	userTPSSignalReady := input.RepresentativeUserTPSLoad &&
		cfg.UserTPSEnabled &&
		!input.PrefillTransition &&
		input.UserTPSValid &&
		input.DecodeRunning >= cfg.UserTPSMinRun &&
		input.DecodeRunning > 0
	if userTPSSignalReady && cfg.UserTPSYellow > 0 && input.UserTPS < cfg.UserTPSYellow && input.UserTPSYellowReady {
		yellowReasons = append(yellowReasons, "single_user_tps")
	}
	if userTPSSignalReady && cfg.UserTPSRed > 0 && input.UserTPS < cfg.UserTPSRed && input.UserTPSRedReady {
		redReasons = append(redReasons, "single_user_tps")
	}
	if cfg.TTFTEnabled && input.TTFTRedReady {
		redReasons = append(redReasons, "ttft_latency")
	} else if cfg.TTFTEnabled && input.TTFTYellowReady {
		yellowReasons = append(yellowReasons, "ttft_latency")
	}

	return SignalResult{
		State:         StateFromReasons(yellowReasons, redReasons),
		YellowReasons: yellowReasons,
		RedReasons:    redReasons,
	}
}
