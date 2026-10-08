package prometheus

import (
	"strings"
	"testing"
)

// vLLM 0.31.0's Rust frontend declares the OpenMetrics counter family name,
// while exposing its monotonically increasing sample with the _total suffix.
// Values and label spelling follow the sealed 2026-10-08 replica-0 scrape.
const vllmRustCounterFixture = `# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="deepseek/deepseek-v4.1-flash",engine="0"} 0
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model_name="deepseek/deepseek-v4.1-flash",engine="0"} 0
# TYPE vllm:num_preemptions counter
vllm:num_preemptions_total{model_name="deepseek/deepseek-v4.1-flash",engine="0"} 0
# TYPE vllm:generation_tokens counter
vllm:generation_tokens_total{model_name="deepseek/deepseek-v4.1-flash",engine="0"} 3427
# EOF
`

func TestParseSampleAcceptsVLLMRustOpenMetricsCounterFamilies(t *testing.T) {
	sample := ParseSample(vllmRustCounterFixture)
	if sample.BackendKind != "vllm" || !sample.ModelNameValid || !sample.RunningValid ||
		!sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid {
		t.Fatalf("Rust OpenMetrics admission counters rejected: %#v", sample)
	}
	if sample.Preemptions != 0 || sample.Generation != 3427 || sample.Running != 0 || sample.Waiting != 0 {
		t.Fatalf("counter normalization changed metric values: %#v", sample)
	}
	if sample.RuntimeStartTimeValid {
		t.Fatal("counter family normalization fabricated a runtime epoch")
	}
}

func TestOpenMetricsCounterFamilyRequiresCounterDeclarationAndTotalSample(t *testing.T) {
	for name, fixture := range map[string]string{
		"missing type":         strings.Replace(vllmRustCounterFixture, "# TYPE vllm:generation_tokens counter\n", "", 1),
		"gauge family":         strings.Replace(vllmRustCounterFixture, "# TYPE vllm:generation_tokens counter", "# TYPE vllm:generation_tokens gauge", 1),
		"untyped family":       strings.Replace(vllmRustCounterFixture, "# TYPE vllm:generation_tokens counter", "# TYPE vllm:generation_tokens untyped", 1),
		"missing total sample": strings.Replace(vllmRustCounterFixture, "vllm:generation_tokens_total{", "vllm:generation_tokens{", 1),
		"unrelated family":     strings.Replace(vllmRustCounterFixture, "# TYPE vllm:generation_tokens counter", "# TYPE unrelated:generation_tokens counter", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if ParseSample(fixture).GenerationValid {
				t.Fatal("invalid family/sample contract accepted")
			}
		})
	}
}

func TestOpenMetricsAndPrometheusDeclarationsAgreeOrFailClosed(t *testing.T) {
	for _, position := range []string{"before", "after"} {
		for _, typ := range []string{"counter", "gauge"} {
			t.Run(position+"/"+typ, func(t *testing.T) {
				declaration := "# TYPE vllm:generation_tokens_total " + typ + "\n"
				fixture := vllmRustCounterFixture + declaration
				if position == "before" {
					fixture = declaration + vllmRustCounterFixture
				}
				if valid := ParseSample(fixture).GenerationValid; valid != (typ == "counter") {
					t.Fatalf("generation valid=%v with %s %s declaration", valid, position, typ)
				}
			})
		}
	}
}
