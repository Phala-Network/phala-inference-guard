package decision

import "math"

type BackendScoreInput struct {
	Running            int
	Waiting            int
	Inflight           int64
	KVCacheUsage       float64
	GenerationTPS      float64
	GenerationTPSValid bool
	TargetTPS          float64
	CapacityRatio      float64
}

func BackendScore(input BackendScoreInput) float64 {
	inflight := float64(input.Inflight)
	load := float64(input.Running) + float64(input.Waiting*8) + inflight*0.25
	if input.KVCacheUsage > 0 {
		load += input.KVCacheUsage * 4
	}
	if input.GenerationTPSValid && input.GenerationTPS > 0 && input.TargetTPS > 0 && input.CapacityRatio > 0 {
		capacity := math.Floor(input.GenerationTPS * input.CapacityRatio / input.TargetTPS)
		if capacity < 1 {
			capacity = 1
		}
		headroom := capacity - float64(input.Running) - inflight
		return -headroom + float64(input.Waiting*4) + input.KVCacheUsage*2
	}
	return load
}
