package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
)

func TestAdmissionPDDecodeStartupAndRoleDrift(t *testing.T) {
	r := httptest.NewRecorder()
	writeAdmissionSGLangMetrics(r, "vendor/pd-model", 100, 0)
	unified := prometheus.ParseSample(r.Body.String())
	decode := prometheus.ParseSample(strings.ReplaceAll(r.Body.String(), `engine_type="unified"`, `engine_type="decode"`))
	at := time.Unix(400, 0)
	u, err := predictiveBackendStartupFromSample(unified, at)
	if err != nil {
		t.Fatal(err)
	}
	d, err := predictiveBackendStartupFromSample(decode, at)
	if err != nil {
		t.Fatalf("PD decode startup rejected: %v", err)
	}
	if u.ModelIdentitySHA256 == d.ModelIdentitySHA256 {
		t.Fatal("PD role must be part of the runtime observation identity")
	}
	for _, tc := range []struct {
		name    string
		startup predictiveBackendStartup
		text    string
	}{
		{"decode-to-unified", d, r.Body.String()},
		{"unified-to-decode", u, strings.ReplaceAll(r.Body.String(), `engine_type="unified"`, `engine_type="decode"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{RuntimeIdentity: tc.startup.ModelIdentitySHA256})
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			observer := &admissionBackendObserver{backendKind: "sglang", runtimeIdentity: tc.startup.ModelIdentitySHA256, maximumAge: time.Second}
			obs, disposition := observer.observation(prometheus.ParseSample(tc.text), at)
			if disposition != admissionSampleIdentityDrift {
				t.Fatalf("role drift disposition=%v", disposition)
			}
			window, ok := controller.StartSampleWindow()
			if !ok {
				t.Fatal("window unavailable")
			}
			controller.PublishObservation(window, obs)
			s := controller.Snapshot(at)
			if s.IntakeOpen || s.MinimumDecision.Reason != coreadmission.ReasonRuntimeIdentityDrift {
				t.Fatalf("role drift did not fail closed: %#v", s)
			}
		})
	}
}
