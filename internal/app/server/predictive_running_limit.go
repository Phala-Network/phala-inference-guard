package server

import (
	"bytes"
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

type sglangRunningLimitProbeConfig struct {
	MetricsURL     string
	RequestTimeout time.Duration
}

func initializePredictiveRunningLimit(
	cfg config,
	startup predictiveBackendStartup,
	metricsURL string,
) predictiveRunningLimit {
	if cfg.PredictiveRunningLimitConfigured || cfg.PredictiveRunningLimit > 0 {
		return predictiveRunningLimit{
			Value:  cfg.PredictiveRunningLimit,
			Source: coreadmission.RunningLimitSourceEnvironment,
		}
	}
	if startup.BackendKind != "sglang" {
		return predictiveRunningLimit{Source: coreadmission.RunningLimitSourceUnknown}
	}
	limit, err := probeSGLangRunningLimit(sglangRunningLimitProbeConfig{
		MetricsURL: metricsURL, RequestTimeout: cfg.PredictiveMetricsRequestTimeout,
	})
	if err != nil {
		log.Printf("level=warn component=tps_controller event=running_limit_discovery backend_kind=sglang result=unavailable source=unknown")
		return predictiveRunningLimit{Source: coreadmission.RunningLimitSourceUnknown}
	}
	return predictiveRunningLimit{
		Value: limit, Source: coreadmission.RunningLimitSourceSGLangServerInfo,
	}
}

func probeSGLangRunningLimit(config sglangRunningLimitProbeConfig) (int64, error) {
	if strings.TrimSpace(config.MetricsURL) == "" || config.RequestTimeout <= 0 {
		return 0, fmt.Errorf("SGLang running-limit probe configuration is invalid")
	}
	endpoint, err := predictiveSGLangServerInfoURL(config.MetricsURL)
	if err != nil {
		return 0, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Timeout:   config.RequestTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(endpoint)
	if err != nil {
		return 0, fmt.Errorf("fetch SGLang server_info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("SGLang server_info returned status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return 0, fmt.Errorf("SGLang server_info content type is not application/json")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumSGLangServerInfoBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read SGLang server_info: %w", err)
	}
	if len(body) > maximumSGLangServerInfoBytes {
		return 0, fmt.Errorf("SGLang server_info exceeds %d bytes", maximumSGLangServerInfoBytes)
	}
	return decodeSGLangRunningLimit(body)
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

func decodeSGLangRunningLimit(body []byte) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return 0, fmt.Errorf("SGLang server_info must be one JSON object")
	}
	var limit int64
	found := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("decode SGLang server_info key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return 0, fmt.Errorf("SGLang server_info contains a non-string key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, fmt.Errorf("decode SGLang server_info field %q: %w", key, err)
		}
		if key != "max_running_requests" {
			continue
		}
		var candidate int64
		if err := json.Unmarshal(raw, &candidate); err != nil ||
			candidate <= 0 || candidate > maximumPredictiveSequenceBound {
			return 0, fmt.Errorf("SGLang max_running_requests must be an integer in [1, %d]", maximumPredictiveSequenceBound)
		}
		if found && candidate != limit {
			return 0, fmt.Errorf("SGLang max_running_requests is duplicate and inconsistent")
		}
		limit = candidate
		found = true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return 0, fmt.Errorf("SGLang server_info object is incomplete")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("SGLang server_info must contain exactly one JSON object")
	}
	if !found {
		return 0, fmt.Errorf("SGLang max_running_requests is missing")
	}
	return limit, nil
}
