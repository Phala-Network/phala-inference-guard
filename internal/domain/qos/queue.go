package qos

import (
	"math"
	"time"
)

type QueueWaitInput struct {
	Configured   time.Duration
	Max          time.Duration
	Severe       time.Duration
	Saturated    time.Duration
	Code         string
	State        string
	RedReasons   []string
	KVCacheUsage float64
	KVRed        float64
	GlobalLimit  int
	Running      int
	Waiting      int
	WaitingRed   int
}

func EffectiveQueueWait(input QueueWaitInput) time.Duration {
	if input.Configured <= 0 {
		return 0
	}
	if input.Code == "backend_unavailable" {
		return 0
	}
	wait := minDuration(input.Configured, input.Max)
	for _, reason := range input.RedReasons {
		switch reason {
		case "preemptions", "kv_cache":
			return minDuration(wait, input.Severe)
		case "waiting", "scheduler_pressure_capacity":
			wait = minDuration(wait, input.Saturated)
		}
	}
	if input.State == "red" {
		return minDuration(wait, input.Severe)
	}
	if input.KVRed > 0 && input.KVCacheUsage >= input.KVRed {
		return minDuration(wait, input.Severe)
	}
	if input.KVCacheUsage >= 0.95 {
		return minDuration(wait, input.Severe)
	}
	if input.GlobalLimit > 0 && input.Running >= int(math.Ceil(float64(input.GlobalLimit)*0.90)) && input.Waiting > 0 {
		wait = minDuration(wait, input.Saturated)
	}
	if input.WaitingRed > 0 && input.Waiting >= maxInt(1, input.WaitingRed/2) {
		wait = minDuration(wait, input.Saturated)
	}
	if input.State == "yellow" && input.Waiting > 0 && input.KVCacheUsage < 0.95 && len(input.RedReasons) == 0 && input.Configured >= input.Saturated {
		wait = minDuration(wait, input.Saturated)
	}
	return wait
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
