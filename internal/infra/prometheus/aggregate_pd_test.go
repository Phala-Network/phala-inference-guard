package prometheus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestAggregatePDDecodeSamplesDeduplicatesTPThenSumsWorkers(t *testing.T) {
	first := multiPDDecodeSample(5, 2, 3, 4, 123, 7, 1_000)
	second := multiPDDecodeSample(8, 1, 6, 9, 345, 11, 2_000)

	got, err := AggregatePDDecodeSamples([]telemetry.Sample{first, second})
	if err != nil {
		t.Fatalf("aggregate Decode samples: %v", err)
	}
	if got.BackendKind != "sglang" || got.BackendRole != "decode" || got.ModelName != "meta/test-model" ||
		!got.ModelNameValid || !got.RunningValid || got.Running != 13 ||
		!got.WaitingValid || got.Waiting != 3 || !got.DecodePendingValid || got.DecodePending != 22 ||
		!got.GenerationValid || got.Generation != 468 || !got.PreemptionsValid || got.Preemptions != 18 {
		t.Fatalf("aggregate did not sum independent Decode workers after local TP dedup: %#v", got)
	}
	if !got.RuntimeStartTimeValid || got.RuntimeStartTime != 1_000 || len(got.RuntimeEpochIdentity) != 64 {
		t.Fatalf("aggregate runtime epoch=%v valid=%t identity=%q, want a real endpoint start and aggregate identity", got.RuntimeStartTime, got.RuntimeStartTimeValid, got.RuntimeEpochIdentity)
	}
	if got.KVTokenMetricsValid || got.CacheTokensValid {
		t.Fatalf("Decode-0-only capacity/cache data was presented as an aggregate: %#v", got)
	}
}

func TestAggregatePDDecodeSamplesRejectsIdentityRoleCounterAndEpochDrift(t *testing.T) {
	valid := multiPDDecodeSample(1, 0, 0, 0, 1, 0, 1_000)
	for name, mutate := range map[string]func(*telemetry.Sample){
		"model":                 func(sample *telemetry.Sample) { sample.ModelName = "meta/other" },
		"role":                  func(sample *telemetry.Sample) { sample.BackendRole = "prefill" },
		"invalid_running":       func(sample *telemetry.Sample) { sample.RunningValid = false },
		"invalid_pending":       func(sample *telemetry.Sample) { sample.DecodePendingValid = false },
		"invalid_generation":    func(sample *telemetry.Sample) { sample.GenerationValid = false },
		"invalid_preemptions":   func(sample *telemetry.Sample) { sample.PreemptionsValid = false },
		"missing_runtime_start": func(sample *telemetry.Sample) { sample.RuntimeStartTimeValid = false },
	} {
		t.Run(name, func(t *testing.T) {
			second := valid
			mutate(&second)
			if _, err := AggregatePDDecodeSamples([]telemetry.Sample{valid, second}); err == nil {
				t.Fatal("incoherent Decode endpoint set was accepted")
			}
		})
	}
}

func TestAggregatePDDecodeSamplesRejectsCounterOverflow(t *testing.T) {
	valid := multiPDDecodeSample(0, 0, 0, 0, 0, 0, 1_000)
	cases := map[string]func(*telemetry.Sample, *telemetry.Sample){
		"running": func(first, second *telemetry.Sample) {
			first.Running = int(^uint(0) >> 1)
			second.Running = 1
		},
		"generation": func(first, second *telemetry.Sample) {
			first.Generation = ^uint64(0)
			second.Generation = 1
		},
		"preemptions": func(first, second *telemetry.Sample) {
			first.Preemptions = ^uint64(0)
			second.Preemptions = 1
		},
	}
	for name, overflow := range cases {
		t.Run(name, func(t *testing.T) {
			first, second := valid, valid
			overflow(&first, &second)
			if _, err := AggregatePDDecodeSamples([]telemetry.Sample{first, second}); err == nil {
				t.Fatal("counter overflow was accepted")
			}
		})
	}
}

func TestFetchAggregateSampleContextDoesNotReturnPartialEndpointSet(t *testing.T) {
	var body strings.Builder
	body.WriteString(pdDecodeFixture())
	_, _ = body.WriteString("process_start_time_seconds 1000\n")
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, body.String())
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
	}))
	defer second.Close()

	client := &http.Client{Timeout: time.Second}
	if sample, err := FetchAggregateSampleContext(t.Context(), client, []string{first.URL, second.URL}); err == nil {
		t.Fatalf("partial sample was returned after one endpoint failed: %#v", sample)
	}
}

func multiPDDecodeSample(running, waiting, prealloc, transfer int, generation, preemptions uint64, started float64) telemetry.Sample {
	var text strings.Builder
	text.WriteString(pdDecodeFixture())
	for _, rank := range []string{"0", "1"} {
		text.WriteString(pdQueue("num_running_reqs", `tp_rank="`+rank+`",priority=""`, strconv.Itoa(running)))
		text.WriteString(pdQueue("num_queue_reqs", `tp_rank="`+rank+`",priority=""`, strconv.Itoa(waiting)))
		text.WriteString(pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="`+rank+`"`, strconv.Itoa(prealloc)))
		text.WriteString(pdQueue("num_decode_transfer_queue_reqs", `tp_rank="`+rank+`"`, strconv.Itoa(transfer)))
		_, _ = fmt.Fprintf(&text, "sglang:realtime_tokens_total{engine_type=\"decode\",mode=\"decode\",model_name=\"meta/test-model\",tp_rank=%q,priority=\"\"} %d\n", rank, generation)
		_, _ = fmt.Fprintf(&text, "sglang:num_retracted_requests_total{engine_type=\"decode\",model_name=\"meta/test-model\",tp_rank=%q,priority=\"\"} %d\n", rank, preemptions)
	}
	_, _ = fmt.Fprintf(&text, "process_start_time_seconds %s\n", strconv.FormatFloat(started, 'f', -1, 64))
	return ParseSample(text.String())
}
