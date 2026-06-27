package dynamic

import (
	"errors"
	"testing"
	"time"

	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestNormalizeStaticMetricPollsUsesPerURLCounterDeltas(t *testing.T) {
	now := time.Unix(100, 0)
	keyA := staticMetricKey(0, "http://backend-a/metrics")
	keyB := staticMetricKey(1, "http://backend-b/metrics")
	previous := staticMetricState{
		keyA: {
			Name:        keyA,
			Generation:  1000,
			Preemptions: 5,
			Updated:     now.Add(-time.Second),
		},
		keyB: {
			Name:        keyB,
			Generation:  2000,
			Preemptions: 10,
			Updated:     now.Add(-time.Second),
		},
	}

	samples, next, failed := normalizeStaticMetricPolls(previous, []staticMetricPoll{
		{
			Key: keyA,
			Sample: telemetry.Sample{
				Running:     4,
				Generation:  1250,
				Preemptions: 7,
			},
		},
		{
			Key: keyB,
			Sample: telemetry.Sample{
				Running:     5,
				Generation:  2600,
				Preemptions: 13,
			},
		},
	}, now)

	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	if !samples[0].GenerationTPSDirect || samples[0].GenerationTPS != 250 {
		t.Fatalf("sample A generation TPS direct/value = %t/%.1f, want true/250", samples[0].GenerationTPSDirect, samples[0].GenerationTPS)
	}
	if !samples[1].GenerationTPSDirect || samples[1].GenerationTPS != 600 {
		t.Fatalf("sample B generation TPS direct/value = %t/%.1f, want true/600", samples[1].GenerationTPSDirect, samples[1].GenerationTPS)
	}
	if !samples[0].PreemptionDeltaDirect || samples[0].PreemptionDelta != 2 {
		t.Fatalf("sample A preemption delta direct/value = %t/%d, want true/2", samples[0].PreemptionDeltaDirect, samples[0].PreemptionDelta)
	}
	if !samples[1].PreemptionDeltaDirect || samples[1].PreemptionDelta != 3 {
		t.Fatalf("sample B preemption delta direct/value = %t/%d, want true/3", samples[1].PreemptionDeltaDirect, samples[1].PreemptionDelta)
	}
	if next[keyA].Generation != 1250 || next[keyB].Generation != 2600 {
		t.Fatalf("next generation A/B = %d/%d, want 1250/2600", next[keyA].Generation, next[keyB].Generation)
	}
}

func TestNormalizeStaticMetricPollsRejectsSingleURLCounterResetWithoutPoisoningOthers(t *testing.T) {
	now := time.Unix(100, 0)
	keyA := staticMetricKey(0, "http://backend-a/metrics")
	keyB := staticMetricKey(1, "http://backend-b/metrics")
	previous := staticMetricState{
		keyA: {
			Name:        keyA,
			Generation:  1000,
			Preemptions: 5,
			Updated:     now.Add(-time.Second),
		},
		keyB: {
			Name:        keyB,
			Generation:  2000,
			Preemptions: 10,
			Updated:     now.Add(-time.Second),
		},
	}

	samples, _, failed := normalizeStaticMetricPolls(previous, []staticMetricPoll{
		{
			Key: keyA,
			Sample: telemetry.Sample{
				Running:     4,
				Generation:  10,
				Preemptions: 1,
			},
		},
		{
			Key: keyB,
			Sample: telemetry.Sample{
				Running:     5,
				Generation:  2600,
				Preemptions: 13,
			},
		},
	}, now)

	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	if samples[0].GenerationTPSDirect || samples[0].GenerationTPS != 0 {
		t.Fatalf("reset sample generation TPS direct/value = %t/%.1f, want false/0", samples[0].GenerationTPSDirect, samples[0].GenerationTPS)
	}
	if samples[0].PreemptionDeltaDirect || samples[0].PreemptionDelta != 0 {
		t.Fatalf("reset sample preemption delta direct/value = %t/%d, want false/0", samples[0].PreemptionDeltaDirect, samples[0].PreemptionDelta)
	}
	if !samples[1].GenerationTPSDirect || samples[1].GenerationTPS != 600 {
		t.Fatalf("healthy sample generation TPS direct/value = %t/%.1f, want true/600", samples[1].GenerationTPSDirect, samples[1].GenerationTPS)
	}
	if !samples[1].PreemptionDeltaDirect || samples[1].PreemptionDelta != 3 {
		t.Fatalf("healthy sample preemption delta direct/value = %t/%d, want true/3", samples[1].PreemptionDeltaDirect, samples[1].PreemptionDelta)
	}
}

func TestNormalizeStaticMetricPollsKeepsHealthyURLsWhenAnotherURLFails(t *testing.T) {
	now := time.Unix(100, 0)
	keyA := staticMetricKey(0, "http://backend-a/metrics")
	keyB := staticMetricKey(1, "http://backend-b/metrics")
	previous := staticMetricState{
		keyA: {
			Name:        keyA,
			Generation:  1000,
			Preemptions: 5,
			Updated:     now.Add(-time.Second),
		},
		keyB: {
			Name:        keyB,
			Generation:  2000,
			Preemptions: 10,
			Updated:     now.Add(-time.Second),
		},
	}

	samples, next, failed := normalizeStaticMetricPolls(previous, []staticMetricPoll{
		{
			Key: keyA,
			Sample: telemetry.Sample{
				Running:     4,
				Generation:  1250,
				Preemptions: 7,
			},
		},
		{
			Key: keyB,
			Err: errors.New("metrics status 500"),
		},
	}, now)

	if failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	if !samples[0].GenerationTPSDirect || samples[0].GenerationTPS != 250 {
		t.Fatalf("healthy sample generation TPS direct/value = %t/%.1f, want true/250", samples[0].GenerationTPSDirect, samples[0].GenerationTPS)
	}
	if !next[keyB].Failed || next[keyB].Error == "" {
		t.Fatalf("failed URL status = %#v, want failed status with error", next[keyB])
	}
}

func TestPreviousStaticMetricStateReturnsCopy(t *testing.T) {
	key := staticMetricKey(0, "http://backend-a/metrics")
	controller := New(testDynamicConfig(), Dependencies{})
	controller.storeStaticMetricState(staticMetricState{
		key: {
			Name:       key,
			Generation: 100,
		},
	})

	previous := controller.previousStaticMetricState()
	previous[key] = runtimebackend.Runtime{Name: key, Generation: 1}
	again := controller.previousStaticMetricState()

	if again[key].Generation != 100 {
		t.Fatalf("stored generation = %d, want original 100", again[key].Generation)
	}
}
