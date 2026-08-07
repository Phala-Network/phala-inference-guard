package request

func New(cfg Config) *Classifier {
	classifier := &Classifier{cfg: cfg}
	if cfg.MaximumConcurrent > 0 {
		classifier.tokens = make(chan struct{}, cfg.MaximumConcurrent)
	}
	return classifier
}

func (c *Classifier) Inflight() int64 {
	return c.inflight.Load()
}

func (c *Classifier) Rejected() uint64 {
	return c.rejected.Load()
}

func (c *Classifier) acquire() bool {
	if c.tokens == nil {
		return true
	}
	select {
	case c.tokens <- struct{}{}:
		c.inflight.Add(1)
		return true
	default:
		c.rejected.Add(1)
		return false
	}
}

func (c *Classifier) release() {
	if c.tokens == nil {
		return
	}
	select {
	case <-c.tokens:
		c.inflight.Add(-1)
	default:
	}
}
