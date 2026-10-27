package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const (
	maximumSGLangServerInfoBytes   = 1 << 20
	maximumPredictiveSequenceBound = int64(1 << 20)
)

type predictiveRunningLimit struct {
	Value  int64
	Source coreadmission.RunningLimitSource
}

// sglangServerInfo is what PIG reads from SGLang /server_info at startup.
type sglangServerInfo struct {
	MaxRunningRequests int64
	// ChunkedPrefillSize is the backend's Prefill chunk in tokens, the warm
	// Prompt size for TPS health; zero when the backend reports none or
	// Prefill is not chunked.
	ChunkedPrefillSize    int64
	BackendTierScheduling bool
}

// predictiveBackendDiscovery is what startup learns about the backend beyond
// the metrics probe: the running limit and the warm Prompt size.
type predictiveBackendDiscovery struct {
	RunningLimit     predictiveRunningLimit
	WarmPromptTokens int64
	// BackendTierScheduling is true for a backend that orders its waiting
	// queue by the priority PIG writes into each request. SGLang does with
	// --enable-priority-scheduling (larger values first); vLLM's priority
	// policy serves smaller values first, so PIG neither writes the field nor
	// exempts the priority tier from TPS health there.
	BackendTierScheduling bool
}

type sglangRunningLimitProbeConfig struct {
	MetricsURL     string
	RequestTimeout time.Duration
}

func discoverPredictiveBackend(
	ctx context.Context,
	cfg config,
	startup predictiveBackendStartup,
	metricsURL string,
) predictiveBackendDiscovery {
	explicit := cfg.PredictiveRunningLimitConfigured || cfg.PredictiveRunningLimit > 0
	discovery := predictiveBackendDiscovery{RunningLimit: predictiveRunningLimit{Source: coreadmission.RunningLimitSourceUnknown}}
	if explicit {
		discovery.RunningLimit = predictiveRunningLimit{
			Value:  cfg.PredictiveRunningLimit,
			Source: coreadmission.RunningLimitSourceEnvironment,
		}
	}
	if startup.BackendKind != "sglang" {
		return discovery
	}
	info, err := probeSGLangServerInfo(ctx, sglangRunningLimitProbeConfig{
		MetricsURL: metricsURL, RequestTimeout: cfg.PredictiveMetricsRequestTimeout,
	})
	if err != nil {
		if !explicit {
			log.Printf("level=warn component=tps_controller event=running_limit_discovery backend_kind=sglang result=unavailable source=unknown")
		}
		log.Printf("level=warn component=tps_controller event=warm_prompt_discovery backend_kind=sglang result=unavailable warm_prompt_tokens=0")
		return discovery
	}
	if !explicit {
		discovery.RunningLimit = predictiveRunningLimit{
			Value: info.MaxRunningRequests, Source: coreadmission.RunningLimitSourceSGLangServerInfo,
		}
	}
	discovery.WarmPromptTokens = info.ChunkedPrefillSize
	discovery.BackendTierScheduling = info.BackendTierScheduling
	return discovery
}

func initializePredictiveRunningLimit(
	ctx context.Context,
	cfg config,
	startup predictiveBackendStartup,
	metricsURL string,
) predictiveRunningLimit {
	return discoverPredictiveBackend(ctx, cfg, startup, metricsURL).RunningLimit
}

func probeSGLangRunningLimit(ctx context.Context, config sglangRunningLimitProbeConfig) (int64, error) {
	info, err := probeSGLangServerInfo(ctx, config)
	return info.MaxRunningRequests, err
}

func probeSGLangServerInfo(ctx context.Context, config sglangRunningLimitProbeConfig) (sglangServerInfo, error) {
	if strings.TrimSpace(config.MetricsURL) == "" || config.RequestTimeout <= 0 {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info probe configuration is invalid")
	}
	endpoint, err := predictiveSGLangServerInfoURL(config.MetricsURL)
	if err != nil {
		return sglangServerInfo{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   config.RequestTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sglangServerInfo{}, fmt.Errorf("construct SGLang server_info request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return sglangServerInfo{}, fmt.Errorf("fetch SGLang server_info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info returned status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info content type is not application/json")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumSGLangServerInfoBytes+1))
	if err != nil {
		return sglangServerInfo{}, fmt.Errorf("read SGLang server_info: %w", err)
	}
	if len(body) > maximumSGLangServerInfoBytes {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info exceeds %d bytes", maximumSGLangServerInfoBytes)
	}
	return decodeSGLangServerInfo(body)
}

func predictiveSGLangServerInfoURL(metricsURL string) (string, error) {
	parsed, err := url.Parse(metricsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("SGLang metrics URL is invalid")
	}
	parsed.Path = "/server_info"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func decodeSGLangServerInfo(body []byte) (sglangServerInfo, error) {
	var info sglangServerInfo
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info must be one JSON object")
	}
	found := false
	chunkFound := false
	prioritySettings := make(map[string]bool, 2)
	var effectiveLimit int64
	effectiveFound := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return sglangServerInfo{}, fmt.Errorf("decode SGLang server_info key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return sglangServerInfo{}, fmt.Errorf("SGLang server_info contains a non-string key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return sglangServerInfo{}, fmt.Errorf("decode SGLang server_info field %q: %w", key, err)
		}
		switch key {
		case "internal_states":
			candidate, err := decodeSGLangEffectiveRunningLimit(raw)
			if err != nil {
				return sglangServerInfo{}, err
			}
			if effectiveFound && candidate != effectiveLimit {
				return sglangServerInfo{}, fmt.Errorf("SGLang internal_states is duplicate and inconsistent")
			}
			effectiveLimit, effectiveFound = candidate, true
		case "enable_priority_scheduling", "schedule_low_priority_values_first":
			var candidate *bool
			if err := json.Unmarshal(raw, &candidate); err != nil || candidate == nil {
				return sglangServerInfo{}, fmt.Errorf("SGLang %s must be a boolean", key)
			}
			if previous, exists := prioritySettings[key]; exists && previous != *candidate {
				return sglangServerInfo{}, fmt.Errorf("SGLang %s is duplicate and inconsistent", key)
			}
			prioritySettings[key] = *candidate
		case "max_running_requests":
			var candidate int64
			if err := json.Unmarshal(raw, &candidate); err != nil ||
				candidate <= 0 || candidate > maximumPredictiveSequenceBound {
				return sglangServerInfo{}, fmt.Errorf("SGLang max_running_requests must be an integer in [1, %d]", maximumPredictiveSequenceBound)
			}
			if found && candidate != info.MaxRunningRequests {
				return sglangServerInfo{}, fmt.Errorf("SGLang max_running_requests is duplicate and inconsistent")
			}
			info.MaxRunningRequests = candidate
			found = true
		case "chunked_prefill_size":
			// SGLang reports -1 when Prefill is not chunked; that leaves no
			// warm Prompt size.
			var candidate int64
			if err := json.Unmarshal(raw, &candidate); err != nil || candidate > maximumPredictiveSequenceBound {
				return sglangServerInfo{}, fmt.Errorf("SGLang chunked_prefill_size must be an integer at most %d", maximumPredictiveSequenceBound)
			}
			if candidate < 0 {
				candidate = 0
			}
			if chunkFound && candidate != info.ChunkedPrefillSize {
				return sglangServerInfo{}, fmt.Errorf("SGLang chunked_prefill_size is duplicate and inconsistent")
			}
			info.ChunkedPrefillSize = candidate
			chunkFound = true
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info object is incomplete")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return sglangServerInfo{}, fmt.Errorf("SGLang server_info must contain exactly one JSON object")
	}
	if !found {
		return sglangServerInfo{}, fmt.Errorf("SGLang max_running_requests is missing")
	}
	if effectiveLimit > 0 && effectiveLimit < info.MaxRunningRequests {
		info.MaxRunningRequests = effectiveLimit
	}
	lowValuesFirst, directionKnown := prioritySettings["schedule_low_priority_values_first"]
	info.BackendTierScheduling = prioritySettings["enable_priority_scheduling"] && directionKnown && !lowValuesFirst
	return info, nil
}

// The engine publishes its configured, static per-DP request-pool capacity,
// not currently available slots. Use the weakest reported pool conservatively;
// states do not establish enough rank identity to aggregate DP capacity here.
func decodeSGLangEffectiveRunningLimit(raw json.RawMessage) (int64, error) {
	var states []json.RawMessage
	if err := json.Unmarshal(raw, &states); err != nil {
		return 0, fmt.Errorf("SGLang internal_states must be an array")
	}
	var minimum int64
	var reported int
	for _, state := range states {
		decoder := json.NewDecoder(bytes.NewReader(state))
		opening, err := decoder.Token()
		if err != nil || opening != json.Delim('{') {
			return 0, fmt.Errorf("SGLang internal state must be an object")
		}
		var value int64
		found := false
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return 0, fmt.Errorf("decode SGLang internal state key")
			}
			var field json.RawMessage
			if err := decoder.Decode(&field); err != nil {
				return 0, fmt.Errorf("decode SGLang internal state field")
			}
			if key != "effective_max_running_requests_per_dp" {
				continue
			}
			var candidate int64
			if err := json.Unmarshal(field, &candidate); err != nil || candidate <= 0 || candidate > maximumPredictiveSequenceBound {
				return 0, fmt.Errorf("SGLang effective request pool must be an integer in [1, %d]", maximumPredictiveSequenceBound)
			}
			if found && candidate != value {
				return 0, fmt.Errorf("SGLang effective request pool is duplicate and inconsistent")
			}
			value, found = candidate, true
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return 0, fmt.Errorf("SGLang internal state is incomplete")
		}
		if found {
			reported++
			if minimum == 0 || value < minimum {
				minimum = value
			}
		}
	}
	if reported > 0 && reported != len(states) {
		return 0, fmt.Errorf("SGLang effective request pools are incomplete")
	}
	return minimum, nil
}
