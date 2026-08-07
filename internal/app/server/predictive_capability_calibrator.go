package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	predictiveCalibrationDeadline          = 15 * time.Second
	predictiveCalibrationMaximumModelBody  = 64 * 1024
	predictiveCalibrationMaximumReplyBody  = 1024 * 1024
	predictiveCalibrationMinimumProbe      = int64(2 * 1024)
	predictiveCalibrationMaximumFirstProbe = int64(8 * 1024)
	predictiveCalibrationMaximumNextProbe  = int64(64 * 1024)
)

type predictiveCapabilityInitializationConfig struct {
	MetricsURL     string
	UpstreamURL    string
	RequestTimeout time.Duration
	RetryInterval  time.Duration
	KVTargetRatio  float64
	KVHardRatio    float64
	Prefill        runtimepredictive.PrefillTokenBounds
}

type predictiveCapabilityInitialization struct {
	Profile runtimepredictive.BackendCapabilityProfile
	Startup predictiveVLLMStartup
	Reason  string
}

func initializePredictiveCapability(
	config predictiveCapabilityInitializationConfig,
	startup predictiveVLLMStartup,
) (predictiveCapabilityInitialization, error) {
	if startup.ModelIdentitySHA256 == "" || startup.CapacityTokens <= 0 || startup.BlockSize <= 0 {
		return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability startup state is invalid")
	}
	base := runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: startup.ModelIdentitySHA256,
		KVCapacityTokens:    startup.CapacityTokens,
		KVBlockSize:         int64(startup.BlockSize),
		KVTargetRatio:       config.KVTargetRatio,
		KVHardRatio:         config.KVHardRatio,
	}
	explicit, automatic, err := predictivePrefillProfileMode(config.Prefill)
	if err != nil {
		return predictiveCapabilityInitialization{}, err
	}
	if !automatic {
		base.Source = runtimepredictive.CapabilityProfileExplicit
		base.Prefill = explicit
		return newPredictiveCapabilityInitialization(base, startup, "explicit_override")
	}
	fallback := base
	fallback.Source = runtimepredictive.CapabilityProfileFallback
	fallback.Prefill = runtimepredictive.PrefillTokenBounds{
		Regular:   domainpredictive.DefaultPrefillRegularTokens,
		Exclusive: domainpredictive.DefaultPrefillExclusiveTokens,
		Quiescent: domainpredictive.DefaultPrefillQuiescentTokens,
		Aggregate: domainpredictive.DefaultPrefillAggregateBudgetTokens,
	}
	if startup.Running != 0 || startup.Waiting != 0 {
		return newPredictiveCapabilityInitialization(fallback, startup, "busy_fallback")
	}
	if !startup.CapabilityMetricsOK {
		return newPredictiveCapabilityInitialization(fallback, startup, "metrics_fallback")
	}

	if config.RequestTimeout <= 0 || config.RetryInterval <= 0 {
		return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability calibration timing is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), predictiveCalibrationDeadline)
	defer cancel()
	client := newPredictiveCalibrationHTTPClient()
	defer client.CloseIdleConnections()
	metadata, err := fetchPredictiveModelMetadata(ctx, client, config.UpstreamURL, startup.modelName)
	if err != nil {
		return newPredictiveCapabilityInitialization(fallback, startup, "metadata_fallback")
	}
	hardTokens, ok := predictiveCapabilityRatioTokens(startup.CapacityTokens, int64(startup.BlockSize), config.KVHardRatio)
	if !ok {
		return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability hard KV geometry is invalid")
	}
	firstTokens := predictiveFirstProbeTokens(metadata.MaxModelLen, hardTokens, int64(startup.BlockSize))
	if firstTokens == 0 {
		return newPredictiveCapabilityInitialization(fallback, startup, "geometry_fallback")
	}

	probeConfig := predictiveColdPrefillProbeConfig{
		MetricsURL:     config.MetricsURL,
		UpstreamURL:    config.UpstreamURL,
		RequestTimeout: config.RequestTimeout,
		RetryInterval:  config.RetryInterval,
		ModelName:      startup.modelName,
		IdentitySHA256: startup.ModelIdentitySHA256,
		CapacityTokens: startup.CapacityTokens,
		BlockSize:      startup.BlockSize,
	}
	first, err := runPredictiveColdPrefillProbe(ctx, client, probeConfig, startup, firstTokens)
	if err != nil {
		if first.Final.ModelIdentitySHA256 == "" {
			return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability probe left upstream state unknown: %w", err)
		}
		return newPredictiveCapabilityInitialization(fallback, first.Final, "probe_fallback")
	}
	secondTokens := predictiveSecondProbeTokens(
		metadata.MaxModelLen,
		hardTokens,
		int64(startup.BlockSize),
		firstTokens,
		first.Rate,
	)
	if secondTokens == 0 {
		return newPredictiveCapabilityInitialization(fallback, first.Final, "scale_fallback")
	}
	second, err := runPredictiveColdPrefillProbe(ctx, client, probeConfig, first.Final, secondTokens)
	if err != nil {
		if second.Final.ModelIdentitySHA256 == "" {
			return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability probe left upstream state unknown: %w", err)
		}
		return newPredictiveCapabilityInitialization(fallback, second.Final, "probe_fallback")
	}
	observedRate := math.Min(first.Rate, second.Rate)
	calibrated := base
	calibrated.Source = runtimepredictive.CapabilityProfileCalibrated
	calibrated.ObservedColdPrefillTokensPerSec = observedRate
	return newPredictiveCapabilityInitialization(calibrated, second.Final, "calibrated")
}

func newPredictiveCalibrationHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func predictivePrefillProfileMode(bounds runtimepredictive.PrefillTokenBounds) (runtimepredictive.PrefillTokenBounds, bool, error) {
	values := [...]int64{bounds.Regular, bounds.Exclusive, bounds.Quiescent, bounds.Aggregate}
	configured := 0
	for _, value := range values {
		if value < 0 {
			return runtimepredictive.PrefillTokenBounds{}, false, fmt.Errorf("predictive Prefill override is invalid")
		}
		if value > 0 {
			configured++
		}
	}
	if configured == 0 {
		return runtimepredictive.PrefillTokenBounds{}, true, nil
	}
	if configured != len(values) {
		return runtimepredictive.PrefillTokenBounds{}, false, fmt.Errorf("predictive Prefill override must be complete")
	}
	return bounds, false, nil
}

func newPredictiveCapabilityInitialization(
	input runtimepredictive.CapabilityProfileInput,
	startup predictiveVLLMStartup,
	reason string,
) (predictiveCapabilityInitialization, error) {
	profile, err := runtimepredictive.NewBackendCapabilityProfile(input)
	if err != nil {
		return predictiveCapabilityInitialization{}, fmt.Errorf("construct predictive backend capability profile: %w", err)
	}
	return predictiveCapabilityInitialization{Profile: profile, Startup: startup, Reason: reason}, nil
}

type predictiveModelMetadata struct {
	MaxModelLen int64
}

func fetchPredictiveModelMetadata(
	ctx context.Context,
	client *http.Client,
	upstreamURL string,
	modelName string,
) (predictiveModelMetadata, error) {
	endpoint, err := predictiveUpstreamEndpoint(upstreamURL, "/v1/models")
	if err != nil {
		return predictiveModelMetadata{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return predictiveModelMetadata{}, fmt.Errorf("construct predictive model metadata request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return predictiveModelMetadata{}, fmt.Errorf("fetch predictive model metadata: %w", err)
	}
	body, err := readPredictiveBoundedResponse(response, predictiveCalibrationMaximumModelBody)
	if err != nil {
		return predictiveModelMetadata{}, err
	}
	if response.StatusCode != http.StatusOK {
		return predictiveModelMetadata{}, fmt.Errorf("predictive model metadata status is not successful")
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int64  `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return predictiveModelMetadata{}, fmt.Errorf("decode predictive model metadata: %w", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != modelName || payload.Data[0].MaxModelLen <= 0 {
		return predictiveModelMetadata{}, fmt.Errorf("predictive model metadata is ambiguous or inconsistent")
	}
	return predictiveModelMetadata{MaxModelLen: payload.Data[0].MaxModelLen}, nil
}

type predictiveColdPrefillProbeConfig struct {
	MetricsURL     string
	UpstreamURL    string
	RequestTimeout time.Duration
	RetryInterval  time.Duration
	ModelName      string
	IdentitySHA256 string
	CapacityTokens int64
	BlockSize      int
}

type predictiveColdPrefillProbeResult struct {
	Rate  float64
	Final predictiveVLLMStartup
}

func runPredictiveColdPrefillProbe(
	ctx context.Context,
	client *http.Client,
	config predictiveColdPrefillProbeConfig,
	before predictiveVLLMStartup,
	estimatedTokens int64,
) (predictiveColdPrefillProbeResult, error) {
	if !predictiveStartupMatchesProbe(before, config) || before.Running != 0 || before.Waiting != 0 ||
		!before.CapabilityMetricsOK || estimatedTokens <= 0 {
		return predictiveColdPrefillProbeResult{}, fmt.Errorf("predictive cold-Prefill probe baseline is invalid")
	}
	nonce, err := predictiveCalibrationNonce()
	if err != nil {
		return predictiveColdPrefillProbeResult{}, err
	}
	prompt, err := predictiveCalibrationPrompt(nonce, estimatedTokens)
	if err != nil {
		return predictiveColdPrefillProbeResult{}, err
	}
	endpoint, err := predictiveUpstreamEndpoint(config.UpstreamURL, "/v1/completions")
	if err != nil {
		return predictiveColdPrefillProbeResult{}, err
	}
	payload, err := json.Marshal(struct {
		Model       string  `json:"model"`
		Prompt      string  `json:"prompt"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}{
		Model: config.ModelName, Prompt: prompt, MaxTokens: 1, Temperature: 0,
	})
	if err != nil {
		return predictiveColdPrefillProbeResult{}, fmt.Errorf("encode predictive cold-Prefill probe: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return predictiveColdPrefillProbeResult{}, fmt.Errorf("construct predictive cold-Prefill probe: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, requestErr := client.Do(request)
	if requestErr == nil {
		_, requestErr = readPredictiveBoundedResponse(response, predictiveCalibrationMaximumReplyBody)
		if requestErr == nil && response.StatusCode != http.StatusOK {
			requestErr = fmt.Errorf("predictive cold-Prefill probe status is not successful")
		}
	}
	final, stateErr := waitPredictiveProbeIdle(ctx, client, config, before, requestErr == nil)
	result := predictiveColdPrefillProbeResult{Final: final}
	if stateErr != nil {
		if requestErr != nil {
			return result, fmt.Errorf("probe request failed (%v) and final state is unavailable: %w", requestErr, stateErr)
		}
		return result, stateErr
	}
	if requestErr != nil {
		return result, requestErr
	}
	rate, err := predictiveColdPrefillRate(before, final)
	if err != nil {
		return result, err
	}
	result.Rate = rate
	return result, nil
}

func waitPredictiveProbeIdle(
	ctx context.Context,
	client *http.Client,
	config predictiveColdPrefillProbeConfig,
	before predictiveVLLMStartup,
	requirePrefillProgress bool,
) (predictiveVLLMStartup, error) {
	retry := config.RetryInterval
	if retry > 250*time.Millisecond {
		retry = 250 * time.Millisecond
	}
	for {
		requestContext, cancel := context.WithTimeout(ctx, config.RequestTimeout)
		sample, fetchErr := prometheus.FetchSampleContext(requestContext, client, config.MetricsURL)
		cancel()
		if fetchErr == nil {
			startup, validationErr := predictiveVLLMStartupFromSample(sample, time.Now())
			prefillPublished := !requirePrefillProgress || startup.PrefillRequests > before.PrefillRequests
			if validationErr == nil && predictiveStartupMatchesProbe(startup, config) &&
				startup.Running == 0 && startup.Waiting == 0 && startup.CapabilityMetricsOK && prefillPublished {
				return startup, nil
			}
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
			return predictiveVLLMStartup{}, fmt.Errorf("predictive cold-Prefill final state is unavailable: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func predictiveStartupMatchesProbe(startup predictiveVLLMStartup, config predictiveColdPrefillProbeConfig) bool {
	return startup.modelName == config.ModelName && startup.ModelIdentitySHA256 == config.IdentitySHA256 &&
		startup.CapacityTokens == config.CapacityTokens && startup.BlockSize == config.BlockSize
}

func predictiveColdPrefillRate(before, after predictiveVLLMStartup) (float64, error) {
	if !before.CapabilityMetricsOK || !after.CapabilityMetricsOK ||
		after.PromptLocalCompute <= before.PromptLocalCompute ||
		after.PromptLocalCacheHit != before.PromptLocalCacheHit ||
		after.PrefillRequests != before.PrefillRequests+1 ||
		after.PrefillSeconds <= before.PrefillSeconds ||
		after.Preemptions != before.Preemptions {
		return 0, fmt.Errorf("predictive cold-Prefill metric delta is not isolated")
	}
	tokens := after.PromptLocalCompute - before.PromptLocalCompute
	seconds := after.PrefillSeconds - before.PrefillSeconds
	rate := float64(tokens) / seconds
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return 0, fmt.Errorf("predictive cold-Prefill rate is invalid")
	}
	return rate, nil
}

func predictiveFirstProbeTokens(maxModelLen, hardTokens, blockSize int64) int64 {
	if maxModelLen <= 0 || hardTokens <= 0 || blockSize <= 0 {
		return 0
	}
	tokens := minInt64(maxModelLen/64, hardTokens/64)
	if tokens > predictiveCalibrationMaximumFirstProbe {
		tokens = predictiveCalibrationMaximumFirstProbe
	}
	if tokens < predictiveCalibrationMinimumProbe {
		return 0
	}
	return capabilityProbeRoundDown(tokens, blockSize)
}

func predictiveSecondProbeTokens(maxModelLen, hardTokens, blockSize, firstTokens int64, firstRate float64) int64 {
	if maxModelLen <= 0 || hardTokens <= 0 || blockSize <= 0 || firstTokens <= 0 ||
		math.IsNaN(firstRate) || math.IsInf(firstRate, 0) || firstRate <= 0 || firstRate > float64(math.MaxInt64)/4 {
		return 0
	}
	tokens := minInt64(predictiveCalibrationMaximumNextProbe, maxModelLen/4)
	tokens = minInt64(tokens, hardTokens/8)
	tokens = minInt64(tokens, int64(math.Floor(firstRate*4)))
	tokens = capabilityProbeRoundDown(tokens, blockSize)
	if tokens < firstTokens*2 || tokens < firstTokens+predictiveCalibrationMinimumProbe {
		return 0
	}
	return tokens
}

func predictiveCapabilityRatioTokens(capacity, blockSize int64, ratio float64) (int64, bool) {
	if capacity <= 0 || blockSize <= 0 || ratio <= 0 || ratio >= 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, false
	}
	value := int64(math.Floor(float64(capacity) * ratio))
	value = capabilityProbeRoundDown(value, blockSize)
	return value, value > 0 && value < capacity
}

func capabilityProbeRoundDown(tokens, blockSize int64) int64 {
	if tokens <= 0 || blockSize <= 0 {
		return 0
	}
	return tokens - tokens%blockSize
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func predictiveCalibrationNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create predictive calibration nonce: %w", err)
	}
	return "pig-capability-" + hex.EncodeToString(raw[:]), nil
}

func predictiveCalibrationPrompt(nonce string, estimatedTokens int64) (string, error) {
	if nonce == "" || estimatedTokens <= 0 || estimatedTokens > predictiveCalibrationMaximumNextProbe ||
		estimatedTokens > int64((math.MaxInt-len(nonce)-1)/2) {
		return "", fmt.Errorf("predictive calibration prompt bound is invalid")
	}
	return nonce + " " + strings.Repeat("x ", int(estimatedTokens)), nil
}

func predictiveUpstreamEndpoint(baseURL, endpointPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("predictive upstream URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpointPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func readPredictiveBoundedResponse(response *http.Response, maximum int64) ([]byte, error) {
	if response == nil || response.Body == nil || maximum <= 0 {
		return nil, fmt.Errorf("predictive upstream response is invalid")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read predictive upstream response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close predictive upstream response: %w", closeErr)
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("predictive upstream response exceeds bound")
	}
	return body, nil
}
