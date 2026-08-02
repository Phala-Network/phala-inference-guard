package pigconfig

import (
	"strings"
	"testing"
	"time"
)

func TestLoadPredictiveAdmissionDefaultsOff(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveAdmissionMode != "off" {
		t.Fatalf("predictive default mode = %q, want off", cfg.PredictiveAdmissionMode)
	}
	if cfg.PredictiveLatencyMinimumMultiplier != 0.10 {
		t.Fatalf("predictive minimum learned latency multiplier = %.3f, want 0.10", cfg.PredictiveLatencyMinimumMultiplier)
	}
	if cfg.PredictiveRouterBackpressureHold != 5*time.Second {
		t.Fatalf("predictive Router backpressure hold = %s, want 5s", cfg.PredictiveRouterBackpressureHold)
	}
}

func TestLoadPredictiveRouterBackpressureHoldIsIndependentOfDynamicPoll(t *testing.T) {
	t.Setenv("DYNAMIC_POLL_INTERVAL_MS", "100")
	t.Setenv("PREDICTIVE_ROUTER_BACKPRESSURE_HOLD", "7s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveRouterBackpressureHold != 7*time.Second {
		t.Fatalf("predictive Router backpressure hold = %s, want 7s", cfg.PredictiveRouterBackpressureHold)
	}
}

func TestLoadPredictiveRouterBackpressureHoldRejectsInvalidOrUnboundedValues(t *testing.T) {
	for _, value := range []string{"not-a-duration", "0", "1s", "31s", "-2s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PREDICTIVE_ROUTER_BACKPRESSURE_HOLD", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PREDICTIVE_ROUTER_BACKPRESSURE_HOLD") {
				t.Fatalf("Load error = %v, want Router backpressure hold rejection", err)
			}
		})
	}
}

func TestValidateAcceptsPredictiveAdmissionEnforce(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate enforce mode: %v", err)
	}
}

func TestValidatePredictiveAdmissionRejectsDynamicTTFTProtection(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PREDICTIVE_ADMISSION_MODE", mode)
			t.Setenv("DYNAMIC_TTFT_ENABLED", "true")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			err = Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "TTFT is observation-only") {
				t.Fatalf("Validate error = %v, want predictive TTFT protection rejection", err)
			}
		})
	}
}

func TestValidatePredictiveAdmissionRequiresBoundedJSONBody(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PREDICTIVE_ADMISSION_MODE", mode)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			cfg.JSONClassifyBodyBytes = 0
			err = Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "JSON_CLASSIFY_BODY_BYTES") {
				t.Fatalf("Validate error = %v, want bounded JSON body requirement", err)
			}
		})
	}
}

func TestValidatePredictiveAdmissionRequiresBoundedJSONConcurrency(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PREDICTIVE_ADMISSION_MODE", mode)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			cfg.ClassifyOutputTokens = false
			cfg.JSONClassifyLimit = 0
			err = Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "JSON_CLASSIFY_LIMIT") {
				t.Fatalf("Validate error = %v, want bounded JSON classifier concurrency requirement", err)
			}
		})
	}
}

func TestValidatePredictiveAdmissionRequiresBoundedShadowObservations(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "shadow")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, invalid := range []int{0, 4_097} {
		cfg.PredictiveShadowObservationLimit = invalid
		err = Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "shadow observation bound") {
			t.Fatalf("Validate maximum observations %d error = %v", invalid, err)
		}
	}
}

func TestLoadPredictiveAdmissionRejectsExcessiveResourceBounds(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "startup timeout", env: "PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS", value: "300001"},
		{name: "metrics timeout", env: "PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS", value: "60001"},
		{name: "minimum samples", env: "PREDICTIVE_LEARNING_MINIMUM_SAMPLES", value: "257"},
		{name: "maximum samples", env: "PREDICTIVE_LEARNING_MAXIMUM_SAMPLES", value: "257"},
		{name: "maximum cells", env: "PREDICTIVE_LEARNING_MAXIMUM_CELLS", value: "257"},
		{name: "maximum age", env: "PREDICTIVE_LEARNING_MAX_AGE_SECONDS", value: "86401"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
			t.Setenv(test.env, test.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.env) {
				t.Fatalf("Load error = %v, want %s bound failure", err, test.env)
			}
		})
	}
}

func TestValidatePredictiveAdmissionRejectsExcessiveResourceBounds(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	base, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "startup timeout", mutate: func(cfg *Config) { cfg.PredictiveStartupProbeTimeout = 5*time.Minute + time.Millisecond }},
		{name: "metrics timeout", mutate: func(cfg *Config) {
			cfg.PredictiveStartupProbeTimeout = 5 * time.Minute
			cfg.PredictiveMetricsRequestTimeout = time.Minute + time.Millisecond
		}},
		{name: "minimum samples", mutate: func(cfg *Config) {
			cfg.PredictiveLearningMinimumSamples = 257
			cfg.PredictiveLearningMaximumSamples = 257
		}},
		{name: "maximum samples", mutate: func(cfg *Config) { cfg.PredictiveLearningMaximumSamples = 257 }},
		{name: "maximum cells", mutate: func(cfg *Config) { cfg.PredictiveLearningMaximumCells = 257 }},
		{name: "maximum age", mutate: func(cfg *Config) { cfg.PredictiveLearningMaxAge = 24*time.Hour + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "predictive") {
				t.Fatalf("Validate error = %v, want predictive resource bound failure", err)
			}
		})
	}
}
