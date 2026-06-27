package request

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/output"
	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

func (c *Classifier) EffectiveOutputThresholds() output.Thresholds {
	return c.effectiveOutputThresholds()
}

func (c *Classifier) outputLane(tokens int) *lane.Lane {
	return requestclass.OutputLane(tokens, requestclass.OutputLanes{
		Default:  c.lanes.Default,
		Medium:   c.lanes.MediumOutput,
		Long:     c.lanes.LongOutput,
		VeryLong: c.lanes.VeryLongOutput,
	}, c.effectiveOutputThresholds())
}

func (c *Classifier) effectiveOutputThresholds() output.Thresholds {
	base := output.Normalize(c.staticOutputThresholds())
	if !c.cfg.AdaptiveOutput || c.outputs == nil {
		return base
	}
	values := c.outputs.SortedSnapshot()
	if len(values) < c.cfg.AdaptiveOutputMin {
		return base
	}
	learned := output.Learned(c.staticOutputThresholds(), values, output.LearningConfig{
		MediumQuantile:   c.cfg.AdaptiveOutputMediumQ,
		LongQuantile:     c.cfg.AdaptiveOutputLongQ,
		VeryLongQuantile: c.cfg.AdaptiveOutputVeryQ,
	})
	factor := output.RelaxFactor(c.currentDynamicState(), c.cfg.AdaptiveOutputGreen, c.cfg.AdaptiveOutputYellow, c.cfg.AdaptiveOutputRed)
	return output.Relaxed(base, learned, factor)
}

func (c *Classifier) staticOutputThresholds() output.Thresholds {
	return output.Thresholds{
		Medium:   c.cfg.MediumOutputTokens,
		Long:     c.cfg.LongOutputTokens,
		VeryLong: c.cfg.VeryLongOutputTokens,
	}
}

func (c *Classifier) currentDynamicState() string {
	if !c.cfg.DynamicEnabled {
		return "green"
	}
	if c.stateFunc == nil {
		return c.cfg.DynamicFailsafeState
	}
	state := c.stateFunc()
	if !decision.ValidState(state) {
		return c.cfg.DynamicFailsafeState
	}
	return state
}

func (c *Classifier) observeOutputTokens(tokens int) {
	if c.outputs != nil {
		c.outputs.Observe(tokens)
	}
}
