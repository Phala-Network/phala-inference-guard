package admission

import (
	"testing"
	"time"
)

func TestWindowConcurrencyHistogramUsesFineLowerBoundsAndNoFinite64PlusBound(t *testing.T) {
	want := [...]int64{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 60, 63,
	}
	if windowConcurrencyHistogramBounds != want {
		t.Fatalf("window concurrency bounds=%v, want=%v", windowConcurrencyHistogramBounds, want)
	}
}

func TestWindowConcurrencyHistogramRecordsEachSuccessfulObservationWindow(t *testing.T) {
	start := time.Unix(81_000, 0)
	controller := testControllerWithObservation(t, testObservation(start, 0, 0, 0, 0))
	admitted := controller.Admit(start.Add(time.Millisecond), testDemand(3))
	if !admitted.Decision.Admitted() {
		t.Fatalf("fixture admission=%+v", admitted.Decision)
	}
	publishObservation(t, controller, testObservation(start.Add(500*time.Millisecond), 0, 0, 0, 0))

	histogram := controller.Snapshot(start.Add(501 * time.Millisecond)).WindowConcurrencyHistogram
	if histogram.Count != 1 || histogram.Sum != 3 {
		t.Fatalf("histogram count/sum=%d/%d", histogram.Count, histogram.Sum)
	}
	for _, bucket := range histogram.Buckets {
		want := uint64(0)
		if bucket.UpperBound >= 3 {
			want = 1
		}
		if bucket.CumulativeCount != want {
			t.Fatalf("bucket le=%d count=%d want=%d", bucket.UpperBound, bucket.CumulativeCount, want)
		}
	}
}

func TestWindowConcurrencyHistogramCombines64AndAboveInOverflowBand(t *testing.T) {
	var histogram windowConcurrencyHistogram
	histogram.observe(63)
	histogram.observe(64)
	histogram.observe(65)
	snapshot := histogram.snapshot()
	if snapshot.Count != 3 || snapshot.Sum != 192 {
		t.Fatalf("histogram count/sum=%d/%d", snapshot.Count, snapshot.Sum)
	}
	for _, bucket := range snapshot.Buckets {
		want := uint64(0)
		if bucket.UpperBound == 63 {
			want = 1
		}
		if bucket.CumulativeCount != want {
			t.Fatalf("bucket le=%d count=%d, want=%d", bucket.UpperBound, bucket.CumulativeCount, want)
		}
	}
}
