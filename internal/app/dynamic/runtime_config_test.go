package dynamic

import "testing"

func TestRuntimeConfigRejectsStaleGenerationAndDeepCopies(t *testing.T) {
	cfg := testDynamicConfig()
	cfg.BackendRouting = false
	cfg.MetricsURLs = []string{"http://old.example/metrics"}
	c := New(cfg, Dependencies{GlobalLimit: func() int { return 100 }})
	oldGeneration := c.configGeneration.Load()
	next := c.AdmissionConfig()
	next.MetricsURLs = []string{"http://new.example/metrics"}
	c.SetAdmissionConfig(next)
	next.MetricsURLs[0] = "mutated"
	before := c.Snapshot()
	c.updateFromMetricSamplesForGeneration(nil, 0, oldGeneration)
	after := c.Snapshot()
	if after.Source != "runtime_config" || after.Updated != before.Updated {
		t.Fatalf("stale poll overwrote snapshot: %+v", after)
	}
	got := c.AdmissionConfig()
	got.MetricsURLs[0] = "also-mutated"
	if c.AdmissionConfig().MetricsURLs[0] != "http://new.example/metrics" {
		t.Fatal("metrics URLs alias caller storage")
	}
}

func TestEveryRuntimeRevisionPublishesConservativeSnapshot(t *testing.T) {
	c := New(testDynamicConfig(), Dependencies{GlobalLimit: func() int { return 100 }})
	next := c.AdmissionConfig()
	next.Enforce = false
	next.FailsafeState = "red"
	c.SetAdmissionConfig(next)
	snapshot := c.Snapshot()
	if snapshot.Source != "runtime_config" || snapshot.State != "red" || snapshot.GlobalLimit != 0 || snapshot.Enforce {
		t.Fatalf("runtime snapshot=%+v", snapshot)
	}
}
