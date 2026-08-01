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

type predictiveVLLMStartupProbeConfig struct {
	MetricsURL     string
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	RetryInterval  time.Duration
}

type predictiveVLLMStartup struct {
	ModelIdentitySHA256 string
	CapacityTokens      int64
	BlockSize           int
	UsedTokens          int64
	Running             int
	Waiting             int
	Preemptions         uint64
	Generation          uint64
	ObservedAt          time.Time
}

func probePredictiveVLLMStartup(config predictiveVLLMStartupProbeConfig) (predictiveVLLMStartup, error) {
	if strings.TrimSpace(config.MetricsURL) == "" || config.StartupTimeout <= 0 || config.RequestTimeout <= 0 || config.RequestTimeout > config.StartupTimeout || config.RetryInterval <= 0 {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive vLLM startup probe configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.StartupTimeout)
	defer cancel()
	client := &http.Client{Timeout: config.RequestTimeout}
	retry := config.RetryInterval
	if retry > 250*time.Millisecond {
		retry = 250 * time.Millisecond
	}
	var lastErr error
	for {
		sample, err := prometheus.FetchSampleContext(ctx, client, config.MetricsURL)
		if err == nil {
			startup, validateErr := predictiveVLLMStartupFromSample(sample, time.Now())
			if validateErr == nil {
				return startup, nil
			}
			err = validateErr
		}
		lastErr = err
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return predictiveVLLMStartup{}, fmt.Errorf("predictive vLLM startup probe did not obtain coherent metrics: %w", lastErr)
		case <-timer.C:
		}
	}
}

func predictiveVLLMStartupFromSample(sample telemetry.Sample, observedAt time.Time) (predictiveVLLMStartup, error) {
	maximumInt := int(^uint(0) >> 1)
	if sample.BackendKind != "vllm" {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive startup metrics backend is not vLLM")
	}
	if !sample.ModelNameValid || strings.TrimSpace(sample.ModelName) == "" {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive startup model identity is missing or ambiguous")
	}
	if !sample.KVTokenMetricsValid || sample.KVCapacityTokens <= 0 || sample.KVUsedTokens < 0 || sample.KVUsedTokens > sample.KVCapacityTokens || !sample.KVBlockSizeValid || sample.KVBlockSize <= 0 {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive startup KV capacity or block size is invalid")
	}
	if !sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid || sample.Running < 0 || sample.Waiting < 0 || sample.Running > maximumInt-sample.Waiting {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive startup request or generation counters are invalid")
	}
	if observedAt.IsZero() {
		return predictiveVLLMStartup{}, fmt.Errorf("predictive startup observation time is invalid")
	}
	return predictiveVLLMStartup{
		ModelIdentitySHA256: predictiveModelIdentitySHA256(sample.ModelName),
		CapacityTokens:      sample.KVCapacityTokens,
		BlockSize:           sample.KVBlockSize,
		UsedTokens:          sample.KVUsedTokens,
		Running:             sample.Running,
		Waiting:             sample.Waiting,
		Preemptions:         sample.Preemptions,
		Generation:          sample.Generation,
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
