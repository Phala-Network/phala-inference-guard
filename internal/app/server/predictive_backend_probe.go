package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type predictiveBackendStartupProbeConfig struct {
	MetricsURL     string
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	RetryInterval  time.Duration
}

type predictiveBackendStartup struct {
	BackendKind         string
	modelName           string
	ModelIdentitySHA256 string
	CapacityTokens      int64
	BlockSize           int
	UsedTokens          int64
	Running             int
	Waiting             int
	Preemptions         uint64
	Generation          uint64
	RuntimeStartTime    float64
	PromptLocalCompute  uint64
	PromptLocalCacheHit uint64
	PrefillRequests     uint64
	PrefillSeconds      float64
	CapabilityMetricsOK bool
	ObservedAt          time.Time
}

func probePredictiveBackendStartup(config predictiveBackendStartupProbeConfig) (predictiveBackendStartup, error) {
	if strings.TrimSpace(config.MetricsURL) == "" || config.StartupTimeout <= 0 || config.RequestTimeout <= 0 ||
		config.RequestTimeout > config.StartupTimeout || config.RetryInterval <= 0 {
		return predictiveBackendStartup{}, fmt.Errorf("predictive backend startup probe configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.StartupTimeout)
	defer cancel()
	client := &http.Client{Timeout: config.RequestTimeout}
	retry := config.RetryInterval
	if retry > 250*time.Millisecond {
		retry = 250 * time.Millisecond
	}
	var lastValidationErr error
	var lastFetchErr error
	for {
		sample, fetchErr := prometheus.FetchSampleContext(ctx, client, config.MetricsURL)
		if fetchErr == nil {
			startup, validateErr := predictiveBackendStartupFromSample(sample, time.Now())
			if validateErr == nil {
				return startup, nil
			}
			lastValidationErr = validateErr
		} else {
			lastFetchErr = fetchErr
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return predictiveBackendStartup{}, predictiveBackendStartupProbeError(ctx.Err(), lastValidationErr, lastFetchErr)
		case <-timer.C:
		}
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
	maximumInt := int(^uint(0) >> 1)
	if sample.BackendKind != "vllm" && sample.BackendKind != "sglang" {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup metrics backend is unsupported or ambiguous")
	}
	if !sample.ModelNameValid || strings.TrimSpace(sample.ModelName) == "" {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup model identity is missing or ambiguous")
	}
	if !sample.KVTokenMetricsValid || sample.KVCapacityTokens <= 0 || sample.KVUsedTokens < 0 ||
		sample.KVUsedTokens > sample.KVCapacityTokens || !sample.KVBlockSizeValid || sample.KVBlockSize <= 0 {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup KV capacity or block size is invalid")
	}
	if !sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid ||
		sample.Running < 0 || sample.Waiting < 0 || sample.Running > maximumInt-sample.Waiting {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup request or generation counters are invalid")
	}
	if observedAt.IsZero() {
		return predictiveBackendStartup{}, fmt.Errorf("predictive startup observation time is invalid")
	}
	return predictiveBackendStartup{
		BackendKind:         sample.BackendKind,
		modelName:           sample.ModelName,
		ModelIdentitySHA256: predictiveModelIdentitySHA256(sample.ModelName),
		CapacityTokens:      sample.KVCapacityTokens,
		BlockSize:           sample.KVBlockSize,
		UsedTokens:          sample.KVUsedTokens,
		Running:             sample.Running,
		Waiting:             sample.Waiting,
		Preemptions:         sample.Preemptions,
		Generation:          sample.Generation,
		RuntimeStartTime:    sample.RuntimeStartTime,
		PromptLocalCompute:  sample.PromptLocalCompute,
		PromptLocalCacheHit: sample.PromptLocalCacheHit,
		PrefillRequests:     sample.PrefillRequests,
		PrefillSeconds:      sample.PrefillSeconds,
		CapabilityMetricsOK: sample.PromptLocalComputeOK && sample.PromptLocalCacheHitOK && sample.PrefillMetricsOK,
		ObservedAt:          observedAt,
	}, nil
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
