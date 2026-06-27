package prometheus

import (
	"fmt"
	"io"
	"net/http"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func FetchSample(client *http.Client, metricsURL string) (telemetry.Sample, error) {
	response, err := client.Get(metricsURL)
	if err != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	closeErr := response.Body.Close()
	if readErr != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, readErr)
	}
	if closeErr != nil {
		return telemetry.Sample{}, fmt.Errorf("%s: %w", metricsURL, closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return telemetry.Sample{}, fmt.Errorf("%s: metrics status %d", metricsURL, response.StatusCode)
	}
	return ParseSample(string(body)), nil
}
