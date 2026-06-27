package token

import (
	"math"
	"sort"
	"sync"
)

type Window struct {
	mu     sync.Mutex
	values []int
	next   int
}

func New(size int) *Window {
	if size <= 0 {
		return nil
	}
	return &Window{values: make([]int, 0, size)}
}

func (w *Window) Observe(tokens int) {
	if w == nil || tokens <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.values) < cap(w.values) {
		w.values = append(w.values, tokens)
		return
	}
	w.values[w.next] = tokens
	w.next = (w.next + 1) % len(w.values)
}

func (w *Window) SortedSnapshot() []int {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	values := append([]int(nil), w.values...)
	sort.Ints(values)
	return values
}

func (w *Window) Count() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.values)
}

func QuantileSorted(sorted []int, q float64) int {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(math.Ceil(q*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
