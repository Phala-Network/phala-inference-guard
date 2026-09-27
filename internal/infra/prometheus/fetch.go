package prometheus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

const maximumMetricsBodyBytes = 4 * 1024 * 1024
const maximumAggregateMetricsEndpoints = 16

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

func FetchAggregateSampleContext(ctx context.Context, client *http.Client, metricsURLs []string) (telemetry.Sample, error) {
	if client == nil || len(metricsURLs) == 0 || len(metricsURLs) > maximumAggregateMetricsEndpoints {
		return telemetry.Sample{}, fmt.Errorf("metrics endpoint set is invalid")
	}
	if len(metricsURLs) == 1 {
		return FetchSampleContext(ctx, client, metricsURLs[0])
	}

	samples := make([]telemetry.Sample, len(metricsURLs))
	errs := make([]error, len(metricsURLs))
	var wait sync.WaitGroup
	wait.Add(len(metricsURLs))
	for i, metricsURL := range metricsURLs {
		go func(index int, endpoint string) {
			defer wait.Done()
			samples[index], errs[index] = FetchSampleContext(ctx, client, endpoint)
		}(i, metricsURL)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return telemetry.Sample{}, err
	}
	for _, err := range errs {
		if err != nil {
			return telemetry.Sample{}, fmt.Errorf("one or more metrics endpoints could not be fetched")
		}
	}
	return AggregatePDDecodeSamples(samples)
}
