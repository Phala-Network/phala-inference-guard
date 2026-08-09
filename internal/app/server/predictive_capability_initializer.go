package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	predictiveMetadataMaximumModelBody    = 64 * 1024
	predictiveMetadataFallbackMaxModelLen = int64(512 * 1024)
)

type predictiveCapabilityInitializationConfig struct {
	UpstreamURL    string
	RequestTimeout time.Duration
	KVHardRatio    float64
	Prefill        runtimepredictive.PrefillTokenBounds
}

type predictiveCapabilityInitialization struct {
	Profile runtimepredictive.BackendCapabilityProfile
	Reason  string
}

func initializePredictiveCapability(
	config predictiveCapabilityInitializationConfig,
	startup predictiveVLLMStartup,
) (predictiveCapabilityInitialization, error) {
	if startup.modelName == "" || startup.ModelIdentitySHA256 == "" || startup.CapacityTokens <= 0 || startup.BlockSize <= 0 {
		return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability startup state is invalid")
	}
	base := runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: startup.ModelIdentitySHA256,
		KVCapacityTokens:    startup.CapacityTokens,
		KVBlockSize:         int64(startup.BlockSize),
		KVHardRatio:         config.KVHardRatio,
	}
	explicit, automatic, err := predictivePrefillProfileMode(config.Prefill)
	if err != nil {
		return predictiveCapabilityInitialization{}, err
	}
	if !automatic {
		base.Source = runtimepredictive.CapabilityProfileExplicit
		base.Prefill = explicit
		return newPredictiveCapabilityInitialization(base, "explicit_override")
	}
	if config.RequestTimeout <= 0 {
		return predictiveCapabilityInitialization{}, fmt.Errorf("predictive capability metadata timeout is invalid")
	}

	base.Source = runtimepredictive.CapabilityProfileAutomatic
	base.MaxModelLen = predictiveMetadataFallbackMaxModelLen
	reason := "metadata_fallback"
	ctx, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
	defer cancel()
	client := newPredictiveMetadataHTTPClient()
	defer client.CloseIdleConnections()
	metadata, metadataErr := fetchPredictiveModelMetadata(ctx, client, config.UpstreamURL, startup.modelName)
	if metadataErr == nil {
		base.MaxModelLen = metadata.MaxModelLen
		reason = "metadata"
	}
	return newPredictiveCapabilityInitialization(base, reason)
}

func newPredictiveMetadataHTTPClient() *http.Client {
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
	reason string,
) (predictiveCapabilityInitialization, error) {
	profile, err := runtimepredictive.NewBackendCapabilityProfile(input)
	if err != nil {
		return predictiveCapabilityInitialization{}, fmt.Errorf("construct predictive backend capability profile: %w", err)
	}
	return predictiveCapabilityInitialization{Profile: profile, Reason: reason}, nil
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
	body, err := readPredictiveBoundedResponse(response, predictiveMetadataMaximumModelBody)
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
