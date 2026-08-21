package request

import "bytes"

const (
	maximumRetainedBodyBufferBytes int64 = 32 * 1024 * 1024
	maximumOutstandingBodyBytes    int64 = 32 * 1024 * 1024
)

func New(cfg Config) *Classifier {
	classifier := &Classifier{
		cfg:                      cfg,
		maximumReservedBodyBytes: outstandingBodyBudget(cfg),
	}
	retainedBuffers := retainedBodyBufferCount(cfg)
	classifier.bodyPool = make(chan *bytes.Buffer, retainedBuffers)
	if cfg.MaximumConcurrent > 0 {
		classifier.tokens = make(chan struct{}, cfg.MaximumConcurrent)
	}
	return classifier
}

func outstandingBodyBudget(cfg Config) int64 {
	if cfg.MaximumBodyBytes <= 0 {
		return 0
	}
	maximum := maximumOutstandingBodyBytes
	if cfg.MaximumBodyBytes > maximum {
		maximum = cfg.MaximumBodyBytes
	}
	if cfg.MaximumConcurrent <= 0 {
		return maximum
	}
	maximumConcurrent := int64(cfg.MaximumConcurrent)
	if cfg.MaximumBodyBytes > maximum/maximumConcurrent {
		return maximum
	}
	configuredMaximum := cfg.MaximumBodyBytes * maximumConcurrent
	if configuredMaximum < maximum {
		return configuredMaximum
	}
	return maximum
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

func (c *Classifier) ReservedBodyBytes() int64 {
	return c.reservedBodyBytes.Load()
}

func (c *Classifier) Rejected() uint64 {
	return c.rejected.Load()
}

func (c *Classifier) acquire(contentLength int64) (int64, bool) {
	if c == nil {
		return 0, false
	}
	tokenAcquired := false
	if c.tokens != nil {
		select {
		case c.tokens <- struct{}{}:
			tokenAcquired = true
		default:
			c.rejected.Add(1)
			return 0, false
		}
	}
	reservedBytes := c.bodyReservationBytes(contentLength)
	if !c.reserveBodyBytes(reservedBytes) {
		if tokenAcquired {
			<-c.tokens
		}
		c.rejected.Add(1)
		return 0, false
	}
	c.inflight.Add(1)
	return reservedBytes, true
}

func (c *Classifier) releaseScanner() {
	if c == nil {
		return
	}
	if c.tokens != nil {
		<-c.tokens
	}
	c.inflight.Add(-1)
}

func (c *Classifier) bodyReservationBytes(contentLength int64) int64 {
	if c == nil || c.cfg.MaximumBodyBytes <= 0 {
		return 0
	}
	if contentLength <= 0 || contentLength > c.cfg.MaximumBodyBytes {
		return c.cfg.MaximumBodyBytes
	}
	return contentLength
}

func (c *Classifier) reserveBodyBytes(bytes int64) bool {
	if c == nil || bytes < 0 {
		return false
	}
	if bytes == 0 || c.maximumReservedBodyBytes <= 0 {
		return true
	}
	for {
		current := c.reservedBodyBytes.Load()
		if current < 0 || current > c.maximumReservedBodyBytes ||
			bytes > c.maximumReservedBodyBytes-current {
			return false
		}
		if c.reservedBodyBytes.CompareAndSwap(current, current+bytes) {
			return true
		}
	}
}

func (c *Classifier) releaseBodyBytes(bytes int64) bool {
	if c == nil || bytes < 0 {
		return false
	}
	if bytes == 0 || c.maximumReservedBodyBytes <= 0 {
		return true
	}
	for {
		current := c.reservedBodyBytes.Load()
		if current < bytes {
			return false
		}
		if c.reservedBodyBytes.CompareAndSwap(current, current-bytes) {
			return true
		}
	}
}
