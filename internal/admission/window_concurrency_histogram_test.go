package admission

import (
	"testing"
	"time"
)

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

func TestWindowConcurrencyHistogramKeepsAbove64InOverflowOnly(t *testing.T) {
	var histogram windowConcurrencyHistogram
	histogram.observe(65)
	snapshot := histogram.snapshot()
	if snapshot.Count != 1 || snapshot.Sum != 65 {
		t.Fatalf("histogram count/sum=%d/%d", snapshot.Count, snapshot.Sum)
	}
	for _, bucket := range snapshot.Buckets {
		if bucket.CumulativeCount != 0 {
			t.Fatalf("above-64 observation entered finite bucket le=%d", bucket.UpperBound)
		}
	}
}
