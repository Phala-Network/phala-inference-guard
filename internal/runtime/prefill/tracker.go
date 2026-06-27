package prefill

import (
	"sync"
	"time"
)

type Tracker struct {
	mu               sync.Mutex
	prefillUntilByID map[uint64]time.Time
}

func New() *Tracker {
	return &Tracker{prefillUntilByID: map[uint64]time.Time{}}
}

func (t *Tracker) Add(id uint64, prefillUntil time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prefillUntilByID[id] = prefillUntil
}

func (t *Tracker) Remove(id uint64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.prefillUntilByID, id)
}

func (t *Tracker) ProtectedCount(now time.Time) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for id, until := range t.prefillUntilByID {
		if until.IsZero() || now.Before(until) {
			count++
			continue
		}
		if now.Sub(until) > time.Hour {
			delete(t.prefillUntilByID, id)
		}
	}
	return count
}
