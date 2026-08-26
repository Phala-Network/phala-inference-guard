package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestV01223WindowConcurrencyHistogramExportsFineBucketsAndOneOverflow(t *testing.T) {
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: testAdmissionRuntimeIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	start := time.Unix(91_000, 0)
	publishAdmissionObservationForTest(t, controller, coreadmission.BackendObservation{
		RuntimeIdentity: testAdmissionRuntimeIdentity,
		ObservedAt:      start, MaximumAge: time.Second,
	})
	admitted := controller.Admit(start.Add(time.Millisecond), coreadmission.NewTPSRequestDemand(3))
	if !admitted.Decision.Admitted() {
		t.Fatalf("fixture admission=%+v", admitted.Decision)
	}
	publishAdmissionObservationForTest(t, controller, coreadmission.BackendObservation{
		RuntimeIdentity: testAdmissionRuntimeIdentity,
		ObservedAt:      start.Add(500 * time.Millisecond), MaximumAge: time.Second,
	})

	var output bytes.Buffer
	writeWindowConcurrencyHistogram(&output, controller.Snapshot(start.Add(501*time.Millisecond)).WindowConcurrencyHistogram)
	body := output.String()
	for _, want := range []string{
		`pig_predictive_window_concurrency_observed_bucket{le="2"} 0`,
		`pig_predictive_window_concurrency_observed_bucket{le="4"} 1`,
		`pig_predictive_window_concurrency_observed_bucket{le="64"} 1`,
		`pig_predictive_window_concurrency_observed_bucket{le="+Inf"} 1`,
		"pig_predictive_window_concurrency_observed_count 1",
		"pig_predictive_window_concurrency_observed_sum 3",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("window histogram missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `le="66"`) {
		t.Fatalf("window histogram split values above 64:\n%s", body)
	}
}
