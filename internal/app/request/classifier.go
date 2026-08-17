package request

import "bytes"

const maximumRetainedBodyBufferBytes int64 = 32 * 1024 * 1024

func New(cfg Config) *Classifier {
	classifier := &Classifier{cfg: cfg}
	retainedBuffers := retainedBodyBufferCount(cfg)
	classifier.bodyPool = make(chan *bytes.Buffer, retainedBuffers)
	if cfg.MaximumConcurrent > 0 {
		classifier.tokens = make(chan struct{}, cfg.MaximumConcurrent)
	}
	return classifier
}

func retainedBodyBufferCount(cfg Config) int {
	if cfg.MaximumBodyBytes <= 0 ||
		cfg.MaximumBodyBytes > maximumRetainedBodyBufferBytes-int64(bytes.MinRead) {
		return 0
	}
	maximumConcurrent := cfg.MaximumConcurrent
	if maximumConcurrent < 1 {
		maximumConcurrent = 1
	}
	perBufferBytes := cfg.MaximumBodyBytes + int64(bytes.MinRead)
	retainedBuffers := int(maximumRetainedBodyBufferBytes / perBufferBytes)
	if retainedBuffers > maximumConcurrent {
		return maximumConcurrent
	}
	return retainedBuffers
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
