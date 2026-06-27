package dynamic

type cleanIntakeGuardStage struct {
	Pressure                 cleanPressureStage
	Prefill                  cleanPrefillStage
	AvailabilityLimit        int
	FinalLimitReasonOverride string
	YellowReasons            []string
}

func evaluateCleanIntakeGuard(signals cleanSignals, currentState string, backendFailed int, pressure cleanPressureStage, prefill cleanPrefillStage, stateLimit int) cleanIntakeGuardStage {
	stage := cleanIntakeGuardStage{
		Pressure:          pressure,
		Prefill:           prefill,
		AvailabilityLimit: stateLimit,
	}
	if signals.Waiting > 0 {
		stage.Pressure = cleanPressureStage{Limit: 0, Reason: "backend_waiting", TargetReason: "backend_waiting"}
		stage.Prefill = cleanPrefillStage{Limit: 0, Reason: "backend_waiting", TargetReason: "backend_waiting"}
		stage.FinalLimitReasonOverride = "backend_waiting"
		if currentState != "red" {
			stage.YellowReasons = append(stage.YellowReasons, "backend_waiting_queue")
		}
	}
	if signals.BackendCount > 0 && backendFailed >= signals.BackendCount {
		stage.AvailabilityLimit = 0
		stage.Pressure = cleanPressureStage{Limit: 0, Reason: "backend_unavailable", TargetReason: "backend_unavailable"}
		stage.Prefill = cleanPrefillStage{Limit: 0, Reason: "backend_unavailable", TargetReason: "backend_unavailable"}
		stage.FinalLimitReasonOverride = "backend_unavailable"
		stage.YellowReasons = append(stage.YellowReasons, "backend_unavailable")
	}
	return stage
}
