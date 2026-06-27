package output

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/token"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type Thresholds struct {
	Medium   int
	Long     int
	VeryLong int
}

type LearningConfig struct {
	MediumQuantile   float64
	LongQuantile     float64
	VeryLongQuantile float64
}

func Normalize(thresholds Thresholds) Thresholds {
	if thresholds.Medium < 1 {
		thresholds.Medium = 1
	}
	if thresholds.Long <= thresholds.Medium {
		thresholds.Long = thresholds.Medium + 1
	}
	if thresholds.VeryLong <= thresholds.Long {
		thresholds.VeryLong = thresholds.Long + 1
	}
	return thresholds
}

func Learned(base Thresholds, values []int, cfg LearningConfig) Thresholds {
	base = Normalize(base)
	if len(values) == 0 {
		return base
	}
	return Normalize(Thresholds{
		Medium:   num.MaxInt(base.Medium, token.QuantileSorted(values, cfg.MediumQuantile)),
		Long:     num.MaxInt(base.Long, token.QuantileSorted(values, cfg.LongQuantile)),
		VeryLong: num.MaxInt(base.VeryLong, token.QuantileSorted(values, cfg.VeryLongQuantile)+1),
	})
}

func RelaxFactor(state string, green, yellow, red float64) float64 {
	switch state {
	case "green":
		return green
	case "yellow":
		return yellow
	case "red":
		return red
	default:
		return 0
	}
}

func Relaxed(base, learned Thresholds, factor float64) Thresholds {
	base = Normalize(base)
	learned = Normalize(learned)
	return Normalize(Thresholds{
		Medium:   relaxValue(base.Medium, learned.Medium, factor),
		Long:     relaxValue(base.Long, learned.Long, factor),
		VeryLong: relaxValue(base.VeryLong, learned.VeryLong, factor),
	})
}

func relaxValue(staticValue, adaptiveValue int, factor float64) int {
	if adaptiveValue <= staticValue || factor <= 0 {
		return staticValue
	}
	if factor >= 1 {
		return adaptiveValue
	}
	return staticValue + int(math.Round(float64(adaptiveValue-staticValue)*factor))
}
