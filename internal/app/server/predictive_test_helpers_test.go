package server

import (
	"sync"
	"time"
)

type manualTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualTestClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	c.mu.Unlock()
}
