package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type predictiveBackendStartupProbeConfig struct {
	MetricsURL     string
	MetricsURLs    []string
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	RetryInterval  time.Duration
}

type predictiveBackendStartup struct {
	BackendKind          string
	PDDecode             bool
	modelName            string
	ModelIdentitySHA256  string
	Running              int
	Waiting              int
	DecodePending        int
	Preemptions          uint64
	Generation           uint64
	RuntimeStartTime     float64
	RuntimeEpochIdentity string
	ObservedAt           time.Time
}

func validatePredictiveBackendStartupProbe(config predictiveBackendStartupProbeConfig) error {
	if _, err := predictiveMetricsURLSet(config.MetricsURL, config.MetricsURLs); err != nil {
		return fmt.Errorf("predictive backend startup probe configuration is invalid")
	}
	if config.StartupTimeout <= 0 || config.RequestTimeout <= 0 ||
		config.RequestTimeout > config.StartupTimeout || config.RetryInterval <= 0 {
		return fmt.Errorf("predictive backend startup probe configuration is invalid")
	}
	return nil
}

const maximumPredictiveMetricsURLCount = 16

func predictiveMetricsURLSet(single string, multiple []string) ([]string, error) {
	metricsURLs := multiple
	if len(metricsURLs) == 0 && strings.TrimSpace(single) != "" {
		metricsURLs = []string{single}
	}
	if len(metricsURLs) == 0 || len(metricsURLs) > maximumPredictiveMetricsURLCount {
		return nil, fmt.Errorf("predictive metrics URL set is invalid")
	}
	seen := make(map[string]struct{}, len(metricsURLs))
	validated := make([]string, len(metricsURLs))
	for i, raw := range metricsURLs {
		value := strings.TrimSpace(raw)
		endpoint, err := url.Parse(value)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
			endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return nil, fmt.Errorf("predictive backend metrics URL is invalid")
		}
		key := strings.ToLower(endpoint.Scheme+"://"+endpoint.Host) + endpoint.EscapedPath()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("predictive metrics URL set contains duplicate endpoints")
		}
		seen[key] = struct{}{}
		validated[i] = value
	}
	return validated, nil
}

// StartupTimeout bounds one diagnostic attempt, not the backend's loading time.
// Only coherent metrics or caller cancellation ends the overall startup wait.
func waitPredictiveBackendStartup(ctx context.Context, config predictiveBackendStartupProbeConfig) (predictiveBackendStartup, error) {
	if err := validatePredictiveBackendStartupProbe(config); err != nil {
		return predictiveBackendStartup{}, err
	}
	started := time.Now()
	var lastLog time.Time
	for {
		if err := ctx.Err(); err != nil {
			return predictiveBackendStartup{}, err
		}
		startup, err := probePredictiveBackendStartup(ctx, config)
		if ctx.Err() != nil {
			return predictiveBackendStartup{}, ctx.Err()
		}
		if err == nil {
			log.Printf("level=info component=runtime event=upstream_ready backend_kind=%s waited_ms=%d", startup.BackendKind, time.Since(started).Milliseconds())
			return startup, nil
		}
		// Fetch errors can contain credentials, URLs and upstream response text.
		// Never include them in persistent operational logs.
		if lastLog.IsZero() || time.Since(lastLog) >= 30*time.Second {
			log.Printf("level=warn component=runtime event=upstream_waiting reason=coherent_metrics_unavailable waited_ms=%d retry=true", time.Since(started).Milliseconds())
			lastLog = time.Now()
		}
		if err := waitStartupRetry(ctx, min(config.RetryInterval, 250*time.Millisecond)); err != nil {
			return predictiveBackendStartup{}, err
		}
	}
}

func probePredictiveBackendStartup(parent context.Context, config predictiveBackendStartupProbeConfig) (predictiveBackendStartup, error) {
	if err := validatePredictiveBackendStartupProbe(config); err != nil {
		return predictiveBackendStartup{}, err
	}
	ctx, cancel := context.WithTimeout(parent, config.StartupTimeout)
	defer cancel()
	metricsURLs, err := predictiveMetricsURLSet(config.MetricsURL, config.MetricsURLs)
	if err != nil {
		return predictiveBackendStartup{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: config.RequestTimeout, Transport: transport}
	retry := config.RetryInterval
	if retry > 250*time.Millisecond {
		retry = 250 * time.Millisecond
	}
	var lastValidationErr error
	var lastFetchErr error
	for {
		if err := ctx.Err(); err != nil {
			return predictiveBackendStartup{}, predictiveBackendStartupProbeError(err, lastValidationErr, lastFetchErr)
		}
		sample, fetchErr := prometheus.FetchAggregateSampleContext(ctx, client, metricsURLs)
		if fetchErr == nil {
			startup, validateErr := predictiveBackendStartupFromSample(sample, time.Now())
			if validateErr == nil {
				return startup, nil
			}
			lastValidationErr = validateErr
		} else {
			lastFetchErr = fetchErr
		}
		if err := waitStartupRetry(ctx, retry); err != nil {
			return predictiveBackendStartup{}, predictiveBackendStartupProbeError(err, lastValidationErr, lastFetchErr)
		}
	}
}

func waitStartupRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func predictiveBackendStartupProbeError(contextErr, validationErr, fetchErr error) error {
	if validationErr != nil && fetchErr != nil {
		return fmt.Errorf("predictive backend startup probe did not obtain coherent metrics: last validation error: %v; last fetch error: %w", validationErr, fetchErr)
	}
	if validationErr != nil {
		return fmt.Errorf("predictive backend startup probe did not obtain coherent metrics: %w", validationErr)
	}
	if fetchErr != nil {
		return fmt.Errorf("predictive backend startup probe did not obtain coherent metrics: %w", fetchErr)
	}
	return fmt.Errorf("predictive backend startup probe did not obtain coherent metrics: %w", contextErr)
}

func predictiveBackendStartupFromSample(sample telemetry.Sample, observedAt time.Time) (predictiveBackendStartup, error) {
	if sample.BackendKind != "vllm" && sample.BackendKind != "sglang" {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup metrics backend is unsupported or ambiguous")
	}
	if !sample.ModelNameValid || strings.TrimSpace(sample.ModelName) == "" {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup model identity is missing or ambiguous")
	}
	if !validPredictiveSampleRequestCounts(sample) || !sample.PreemptionsValid || !sample.GenerationValid {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup request or generation counters are invalid")
	}
	if observedAt.IsZero() {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup observation time is invalid")
	}
	return predictiveBackendStartup{
		BackendKind:          sample.BackendKind,
		PDDecode:             sample.BackendKind == "sglang" && sample.BackendRole == "decode",
		modelName:            sample.ModelName,
		ModelIdentitySHA256:  predictiveSampleIdentitySHA256(sample),
		Running:              sample.Running,
		Waiting:              sample.Waiting,
		DecodePending:        sample.DecodePending,
		Preemptions:          sample.Preemptions,
		Generation:           sample.Generation,
		RuntimeStartTime:     sample.RuntimeStartTime,
		RuntimeEpochIdentity: sample.RuntimeEpochIdentity,
		ObservedAt:           observedAt,
	}, nil
}

func validPredictiveSampleRequestCounts(sample telemetry.Sample) bool {
	if !sample.RunningValid || !sample.WaitingValid || sample.Running < 0 || sample.Waiting < 0 ||
		sample.DecodePending < 0 || (sample.BackendRole == "decode" && !sample.DecodePendingValid) ||
		(sample.BackendRole != "decode" && sample.DecodePending != 0) {
		return false
	}
	maximumInt := int(^uint(0) >> 1)
	if sample.Running > maximumInt-sample.Waiting {
		return false
	}
	return sample.DecodePending <= maximumInt-sample.Running-sample.Waiting
}

// Preserve existing unified/vLLM identities. PD Decode has a distinct identity
// so a same-model role switch cannot reuse the original controller's history.
func predictiveSampleIdentitySHA256(sample telemetry.Sample) string {
	if sample.BackendKind == "sglang" && sample.BackendRole == "decode" {
		return predictiveModelIdentitySHA256(sample.ModelName + "\x00sglang-decode")
	}
	return predictiveModelIdentitySHA256(sample.ModelName)
}

func predictiveModelIdentitySHA256(model string) string {
	digest := sha256.Sum256([]byte(model))
	return hex.EncodeToString(digest[:])
}

func validPredictiveModelIdentitySHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
