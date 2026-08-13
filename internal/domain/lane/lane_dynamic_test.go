package lane

import (
	"sync"
	"testing"
)

func TestSetLimitResizesWithoutDroppingInflight(t *testing.T) {
	l := New("global", 2, Buckets{})
	if !l.TryAcquire() || !l.TryAcquire() || l.TryAcquire() {
		t.Fatal("initial limit was not enforced")
	}
	l.SetLimit(1)
	if l.TryAcquire() {
		t.Fatal("lowered limit admitted while above cap")
	}
	l.Release()
	if l.TryAcquire() {
		t.Fatal("lowered limit admitted while at cap")
	}
	l.Release()
	if !l.TryAcquire() {
		t.Fatal("lowered limit did not admit after drain")
	}
	l.SetLimit(3)
	if !l.TryAcquire() || !l.TryAcquire() || l.TryAcquire() {
		t.Fatal("raised limit was not applied")
	}
}

func TestSetLimitZeroIsEmergencyStop(t *testing.T) {
	l := New("global", 1, Buckets{})
	if !l.TryAcquire() {
		t.Fatal("initial acquire failed")
	}
	l.SetLimit(0)
	if l.TryAcquire() {
		t.Fatal("zero limit admitted new work")
	}
	l.Release()
	if l.TryAcquire() {
		t.Fatal("zero limit admitted after drain")
	}
}

func TestSetLimitIsLinearizableWithAcquire(t *testing.T) {
	l := New("global", 100, Buckets{})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.TryAcquire() {
				l.Release()
			}
		}()
	}
	close(start)
	l.SetLimit(1)
	wg.Wait()
	if got := l.Inflight(); got != 0 {
		t.Fatalf("inflight=%d after releases", got)
	}
	if !l.TryAcquire() || l.TryAcquire() {
		t.Fatal("final limit was not exactly one")
	}
}
