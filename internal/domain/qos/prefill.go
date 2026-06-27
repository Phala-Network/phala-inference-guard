package qos

import "time"

type PrefillGraceInput struct {
	Enabled        bool
	Min            time.Duration
	Max            time.Duration
	BodyBytes      int64
	BytesPerSec    float64
	BodyMultiplier float64
}

func PrefillGrace(input PrefillGraceInput) time.Duration {
	if !input.Enabled {
		return 0
	}
	grace := input.Min
	bodyBytes := input.BodyBytes
	if bodyBytes < 0 {
		bodyBytes = 0
	}
	if input.BytesPerSec > 0 && bodyBytes > 0 {
		estimatedSeconds := (float64(bodyBytes) / input.BytesPerSec) * input.BodyMultiplier
		estimated := time.Duration(estimatedSeconds * float64(time.Second))
		if estimated > grace {
			grace = estimated
		}
	}
	if grace > input.Max {
		grace = input.Max
	}
	return grace
}
