package request

import "github.com/Phala-Network/phala-inference-guard/internal/runtime/token"

func New(cfg Config, lanes Lanes, stateFunc func() string, enabledFunc ...func() bool) *Classifier {
	classifier := &Classifier{cfg: cfg, lanes: lanes, stateFunc: stateFunc}
	if len(enabledFunc) > 0 {
		classifier.enabledFunc = enabledFunc[0]
	}
	if cfg.AdaptiveOutput {
		classifier.outputs = token.New(cfg.AdaptiveOutputWindow)
	}
	if cfg.JSONClassifyLimit > 0 {
		classifier.tokens = make(chan struct{}, cfg.JSONClassifyLimit)
	}
	return classifier
}

func (c *Classifier) Inflight() int64 {
	return c.inflight.Load()
}

func (c *Classifier) Rejected() uint64 {
	return c.rejected.Load()
}

func (c *Classifier) OutputSampleCount() int {
	if c.outputs == nil {
		return 0
	}
	return c.outputs.Count()
}
