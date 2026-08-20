package metrics

import (
	"bytes"
	"strings"
	"testing"

	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
)

func TestWriteBackendsDoesNotExportUnobservedTTFTPlaceholders(t *testing.T) {
	var output bytes.Buffer
	WriteBackends(&output, []BackendSnapshot{{
		Name: "upstream",
		Status: runtimebackend.Runtime{
			Name: "upstream", GenerationTPS: 42, GenerationTPSValid: true,
		},
	}})
	text := output.String()
	if !strings.Contains(text, "pig_backend_observed_generation_tokens_per_second") {
		t.Fatalf("backend metrics omitted active generation telemetry:\n%s", text)
	}
	if strings.Contains(text, "observed_ttft") {
		t.Fatalf("backend metrics retained unavailable TTFT placeholders:\n%s", text)
	}
}
