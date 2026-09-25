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
	if u.PDDecode || !d.PDDecode {
		t.Fatalf("PD decode mode was not derived from the backend role: unified=%t decode=%t", u.PDDecode, d.PDDecode)
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

func TestAdmissionPDDecodePendingReachesControllerObservation(t *testing.T) {
	r := httptest.NewRecorder()
	writeAdmissionSGLangMetrics(r, "vendor/pd-model", 100, 0)
	text := strings.ReplaceAll(r.Body.String(), `engine_type="unified"`, `engine_type="decode"`)
	text += "# TYPE sglang:num_decode_transfer_queue_reqs gauge\n" +
		"sglang:num_decode_transfer_queue_reqs{engine_type=\"decode\",model_name=\"vendor/pd-model\",tp_rank=\"0\"} 124\n"
	sample := prometheus.ParseSample(text)
	at := time.Unix(500, 0)
	startup, err := predictiveBackendStartupFromSample(sample, at)
	if err != nil || startup.Waiting != 0 || startup.DecodePending != 124 {
		t.Fatalf("PD startup lost pending debt: startup=%+v err=%v", startup, err)
	}
	observer := &admissionBackendObserver{backendKind: "sglang", runtimeIdentity: startup.ModelIdentitySHA256, maximumAge: time.Second}
	observation, disposition := observer.observation(sample, at.Add(time.Second))
	if disposition != admissionSampleUsable || observation.Waiting != 0 || observation.DecodePending != 124 {
		t.Fatalf("PD observer lost pending debt: observation=%+v disposition=%v", observation, disposition)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity:    startup.ModelIdentitySHA256,
		RunningLimit:       128,
		RunningLimitSource: coreadmission.RunningLimitSourceAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	window, ok := controller.StartSampleWindow()
	if !ok || !controller.PublishObservation(window, observation).Accepted {
		t.Fatal("PD observation was not published")
	}
	if state := controller.Snapshot(at.Add(time.Second)).State; state.RawWaiting != 0 || state.RawDecodePending != 124 {
		t.Fatalf("controller lost split PD observation: %+v", state)
	}
}
