package pigconfig

import "testing"

func TestSGLangMetricsTopologyPreservesAdmissionPolicy(t *testing.T) {
	t.Setenv("SGLANG_METRICS_TP_SIZE", "8")
	t.Setenv("SGLANG_METRICS_DP_SIZE", "8")
	t.Setenv("PREDICTIVE_TPS_REFERENCE", "50")
	t.Setenv("PREDICTIVE_RUNNING_LIMIT", "128")
	t.Setenv("PREDICTIVE_WINDOW_CONCURRENCY", "32")
	cfg, err := Load()
	if err != nil || cfg.SGLangMetricsTPSize != 8 || cfg.SGLangMetricsDPSize != 8 ||
		cfg.PredictiveTPSReference != 50 || cfg.PredictiveRunningLimit != 128 || cfg.PredictiveWindowConcurrency != 32 {
		t.Fatalf("topology altered policy or failed to load: cfg=%#v err=%v", cfg, err)
	}
}

func TestSGLangMetricsTopologyRejectsInvalidGeometry(t *testing.T) {
	for _, pair := range [][2]string{{"8", "3"}, {"8", "0"}, {"0", "8"}, {"-1", "1"}, {"invalid", "8"}, {"4097", "1"}} {
		t.Run(pair[0]+"_"+pair[1], func(t *testing.T) {
			t.Setenv("SGLANG_METRICS_TP_SIZE", pair[0])
			t.Setenv("SGLANG_METRICS_DP_SIZE", pair[1])
			if _, err := Load(); err == nil {
				t.Fatal("invalid topology accepted")
			}
		})
	}
}
