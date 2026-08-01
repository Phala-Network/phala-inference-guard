package prometheus

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

const maximumMetricsBodyBytes = 4 * 1024 * 1024

func FetchSample(client *http.Client, metricsURL string) (telemetry.Sample, error) {
	return FetchSampleContext(context.Background(), client, metricsURL)
}

func FetchSampleContext(ctx context.Context, client *http.Client, metricsURL string) (telemetry.Sample, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximumMetricsBodyBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, readErr)
	}
	if closeErr != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, closeErr)
	}
	if len(body) > maximumMetricsBodyBytes {
		return telemetry.Sample{}, fmt.Errorf("%s: metrics body exceeds %d bytes", metricsURL, maximumMetricsBodyBytes)
	}
	if response.StatusCode != http.StatusOK {
		return telemetry.Sample{}, fmt.Errorf("%s: metrics status %d", metricsURL, response.StatusCode)
	}
	return ParseSample(string(body)), nil
}
