package server

import (
	"sync"
	"time"
)

type adapterTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *adapterTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *adapterTestClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	c.mu.Unlock()
}
