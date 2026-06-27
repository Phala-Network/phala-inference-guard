package decision

import "math"

type CapacityLimitInput struct {
	CurrentLimit           int
	PreviousLimit          int
	LearnedLimit           int
	TargetLimit            int
	LearnState             string
	DemandPressure         bool
	PrefillTransition      bool
	ProvisionalGrowthRatio float64
}

func ApplyCapacityLimit(input CapacityLimitInput) int {
	if input.CurrentLimit <= 0 {
		return input.CurrentLimit
	}
	if input.LearnState == "disabled" {
		return input.CurrentLimit
	}

	active := input.PreviousLimit
	if active <= 0 {
		active = input.LearnedLimit
	}
	if active <= 0 {
		active = input.CurrentLimit
	}
	active = clampInt(active, 1, input.CurrentLimit)

	if input.PrefillTransition {
		return active
	}

	target := input.TargetLimit
	if target <= 0 {
		target = input.LearnedLimit
	}
	if target <= 0 {
		target = active
	}
	target = clampInt(target, 1, input.CurrentLimit)

	switch input.LearnState {
	case "pig_down", "pressure_down", "learn_down":
		if input.LearnedLimit > 0 && input.LearnedLimit < target {
			target = input.LearnedLimit
		}
		return clampInt(target, 1, input.CurrentLimit)
	}

	if !input.DemandPressure {
		return active
	}

	recoveryTarget := target
	if input.LearnedLimit > recoveryTarget {
		recoveryTarget = input.LearnedLimit
	}
	recoveryTarget = clampInt(recoveryTarget, 1, input.CurrentLimit)
	if recoveryTarget <= active {
		return active
	}
	return growCapacityLimit(active, recoveryTarget, input.ProvisionalGrowthRatio, input.CurrentLimit)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func growCapacityLimit(active, target int, ratio float64, maxLimit int) int {
	if target <= active {
		return active
	}
	window := int(math.Ceil(float64(active) * ratio))
	if window < 8 {
		window = 8
	}
	next := active + window
	if next > target {
		next = target
	}
	return clampInt(next, 1, maxLimit)
}
