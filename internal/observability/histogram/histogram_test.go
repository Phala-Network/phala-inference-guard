package histogram

import (
	"math"
	"testing"
	"time"
)

func TestDurationHistogramWithBucketsValidatesAndOwnsBounds(t *testing.T) {
	bounds := []float64{0.001, 0.002}
	histogram, err := NewDurationHistogramWithBuckets(bounds)
	if err != nil {
		t.Fatalf("new duration histogram: %v", err)
	}
	bounds[0] = 100
	histogram.Observe(1500 * time.Microsecond)
	sample := histogram.Sample()
	if len(sample.Buckets) != 2 || sample.Buckets[0].UpperBound != 0.001 || sample.Buckets[0].Count != 0 || sample.Buckets[1].UpperBound != 0.002 || sample.Buckets[1].Count != 1 {
		t.Fatalf("histogram did not retain immutable instance bounds: %+v", sample)
	}

	for _, test := range []struct {
		name   string
		bounds []float64
	}{
		{name: "empty"},
		{name: "zero", bounds: []float64{0}},
		{name: "negative", bounds: []float64{-1}},
		{name: "nan", bounds: []float64{math.NaN()}},
		{name: "infinite", bounds: []float64{math.Inf(1)}},
		{name: "duplicate", bounds: []float64{0.001, 0.001}},
		{name: "descending", bounds: []float64{0.002, 0.001}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewDurationHistogramWithBuckets(test.bounds); err == nil {
				t.Fatal("invalid histogram bounds were accepted")
			}
		})
	}
}

func TestPredictiveDurationHistogramIncludesLiveLatencyGates(t *testing.T) {
	histogram := NewPredictiveDurationHistogram()
	sample := histogram.Sample()
	for _, required := range []float64{0.00025, 0.001} {
		found := false
		for _, bucket := range sample.Buckets {
			if bucket.UpperBound == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("predictive histogram lacks live latency bucket %g: %+v", required, sample.Buckets)
		}
	}
	general := NewDurationHistogram()
	generalSample := general.Sample()
	if len(generalSample.Buckets) == 0 || generalSample.Buckets[0].UpperBound != 0.1 {
		t.Fatalf("general service-duration buckets changed unexpectedly: %+v", generalSample.Buckets)
	}
}
