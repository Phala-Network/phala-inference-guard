package server

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type staticAdmissionTelemetryService struct {
	snapshot admissionTelemetrySnapshot
}

func (*staticAdmissionTelemetryService) Decide(context.Context, coreadmission.TPSRequestDemand) admissionDecision {
	return unavailableAdmissionDecision(coreadmission.TPSRequestDemand{})
}

func (s *staticAdmissionTelemetryService) Snapshot(time.Time) admissionTelemetrySnapshot {
	return s.snapshot
}

func (*staticAdmissionTelemetryService) Close() error { return nil }

func TestAdmissionRouterProjectionMatchesCurrentCapacityContract(t *testing.T) {
	tests := []struct {
		name               string
		capacity           coreadmission.CapacitySnapshot
		wantActive         bool
		wantRunning        int
		wantWaiting        int
		wantRawLimit       int
		wantEffectiveLimit int
	}{
		{
			name: "open ignores scalar waiting while preserving raw diagnostics",
			capacity: coreadmission.CapacitySnapshot{
				Available:       true,
				MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
				State:           coreadmission.ProjectedState{RawRunning: 3, RawWaiting: 2},
			},
			wantRunning: 3,
		},
		{
			name: "load protection blocks an idle Router candidate immediately",
			capacity: coreadmission.CapacitySnapshot{
				MinimumDecision: coreadmission.DecisionRecord{
					Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonTPSReference,
					Scope: coreadmission.ProtectionLoad,
				},
				State: coreadmission.ProjectedState{RawWaiting: 4},
			},
			wantActive: true, wantRunning: 1, wantRawLimit: 0, wantEffectiveLimit: 1,
		},
		{
			name: "TPS reference protection closes Router capacity",
			capacity: coreadmission.CapacitySnapshot{
				MinimumDecision: coreadmission.DecisionRecord{
					Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonTPSReference,
					Scope: coreadmission.ProtectionLoad,
				},
				State: coreadmission.ProjectedState{RawRunning: 8},
			},
			wantActive: true, wantRunning: 8, wantRawLimit: 8, wantEffectiveLimit: 8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := projectAdmissionCapacity("enforce", test.capacity, admissionReportSnapshot{})
			compatibility := projectRouterCompatibility("enforce", test.capacity, projection)
			if projection.Active != test.wantActive || compatibility.ObservedRunning != test.wantRunning ||
				compatibility.ObservedWaiting != test.wantWaiting || compatibility.GlobalLimitRaw != test.wantRawLimit ||
				compatibility.GlobalLimit != test.wantEffectiveLimit ||
				compatibility.ObservedWaitingRaw != nonnegativeInt(test.capacity.State.RawWaiting) {
				t.Fatalf("projection=%+v compatibility=%+v", projection, compatibility)
			}
		})
	}
}

func TestAdmissionMetricsPublishProtectionBeforeAnyHTTPReject(t *testing.T) {
	capacity := coreadmission.CapacitySnapshot{
		IntakeOpen: true, HasObservation: true,
		MinimumDecision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonTPSReference,
			Scope: coreadmission.ProtectionLoad,
		},
		State: coreadmission.ProjectedState{RawWaiting: 1},
		Observation: coreadmission.BackendObservation{
			ObservedAt: time.Now(), MaximumAge: time.Minute,
		},
	}
	srv := &proxyServer{
		cfg:       config{PredictiveAdmissionMode: "enforce"},
		admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{Capacity: capacity}},
	}
	var rendered strings.Builder
	srv.writeAdmissionAndRouterMetrics(&rendered)
	body := rendered.String()
	for _, want := range []string{
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running_raw 0",
		"pig_dynamic_observed_waiting_raw 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_observed_waiting 0",
		"pig_dynamic_global_limit_raw 0",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "pig_predictive_admission_enforced_rejects_total 1") {
		t.Fatalf("current capacity was incorrectly made dependent on historical HTTP rejects:\n%s", body)
	}
}

func TestTPSReferenceShadowProtectionDoesNotReduceRouterCapacity(t *testing.T) {
	capacity := coreadmission.CapacitySnapshot{
		MinimumDecision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonTPSReference,
			Scope: coreadmission.ProtectionLoad,
		},
		State: coreadmission.ProjectedState{RawRunning: 8, RawWaiting: 2},
	}
	projection := projectAdmissionCapacity("shadow", capacity, admissionReportSnapshot{})
	compatibility := projectRouterCompatibility("shadow", capacity, projection)
	if projection.Active || compatibility.ObservedRunning != 8 || compatibility.ObservedWaiting != 2 ||
		compatibility.GlobalLimit != 0 {
		t.Fatalf("shadow TPS protection changed Router capacity: projection=%+v compatibility=%+v", projection, compatibility)
	}
}

func TestAdmissionUpstreamStatusUsesCurrentProtectionScope(t *testing.T) {
	tests := []struct {
		name      string
		scope     coreadmission.ProtectionScope
		available bool
		want      int
	}{
		{name: "open", available: true, want: upstreamStatusGreen},
		{name: "load", scope: coreadmission.ProtectionLoad, want: upstreamStatusYellow},
		{name: "request", scope: coreadmission.ProtectionRequest, want: upstreamStatusRed},
		{name: "availability", scope: coreadmission.ProtectionAvailability, want: upstreamStatusRed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := coreadmission.DecisionRecord{Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonTPSReference, Scope: test.scope}
			if test.available {
				decision = coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen}
			}
			service := &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{Capacity: coreadmission.CapacitySnapshot{
				IntakeOpen: true, HasObservation: true, Available: test.available, MinimumDecision: decision,
				Observation: coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
			}}}
			srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, admission: service}
			if got := srv.upstreamStatusCode(); got != test.want {
				t.Fatalf("upstream status=%d want=%d capacity=%+v", got, test.want, service.snapshot.Capacity)
			}
		})
	}
}

func TestStatusLogReportsCurrentTPSCapacityReasonWithoutARequestDecision(t *testing.T) {
	now := time.Now()
	capacity := coreadmission.CapacitySnapshot{
		IntakeOpen:     true,
		HasObservation: true,
		MinimumDecision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
		},
		State: coreadmission.ProjectedState{
			RawRunning: 2,
			TPS: coreadmission.TPSSnapshot{
				Enabled: true, Reference: 20,
			},
		},
		Observation: coreadmission.BackendObservation{ObservedAt: now, MaximumAge: time.Minute},
	}
	srv := &proxyServer{
		cfg: config{PredictiveAdmissionMode: "enforce"},
		admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{
			Capacity: capacity,
		}},
	}
	line := srv.statusLogLine()
	if !strings.Contains(line, "level=info") ||
		!strings.Contains(line, "component=controller") ||
		!strings.Contains(line, "event=status") ||
		!strings.Contains(line, "capacity=protect/tps_reference") ||
		!strings.Contains(line, "router=true/load/") {
		t.Fatalf("status omitted current TPS capacity protection: %s", line)
	}
}

func TestStatusLogSeparatesLastRequestFromCurrentCapacity(t *testing.T) {
	now := time.Now()
	current := coreadmission.DecisionRecord{
		Action: coreadmission.ActionAdmit,
		Reason: coreadmission.ReasonOpen,
		State: coreadmission.ProjectedState{
			RawRunning: 2,
			TPS:        coreadmission.TPSSnapshot{Reference: 25},
		},
		TPSSequenceLimit:      3,
		TPSCurrentSequences:   2,
		TPSPostAdmitSequences: 3,
	}
	last := coreadmission.DecisionRecord{
		Action: coreadmission.ActionProtect,
		Reason: coreadmission.ReasonTPSReference,
		Scope:  coreadmission.ProtectionLoad,
	}
	srv := &proxyServer{
		cfg: config{PredictiveAdmissionMode: "enforce"},
		admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{
			Capacity: coreadmission.CapacitySnapshot{
				IntakeOpen: true, HasObservation: true, Available: true,
				State: current.State, MinimumDecision: current,
				Observation: coreadmission.BackendObservation{ObservedAt: now, MaximumAge: time.Minute},
			},
			Report: admissionReportSnapshot{HasLastDecision: true, LastDecision: last},
		}},
	}
	line := srv.statusLogLine()
	if !strings.Contains(line, "last=load_protect/tps_reference") ||
		!strings.Contains(line, "capacity=admit/open") ||
		!strings.Contains(line, "sequences=2/3/3") {
		t.Fatalf("status conflated last request and current capacity: %s", line)
	}
}

func TestAdmissionTelemetryReadsDoNotMutateController(t *testing.T) {
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, admission: runtime}
	before := controller.Snapshot(clock.Now())
	_, _ = srv.predictiveAdmissionMetricsInput(clock.Now())
	_ = srv.statusLogLine()
	_ = srv.upstreamStatusCode()
	after := controller.Snapshot(clock.Now())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("telemetry reads mutated Controller:\nbefore=%+v\nafter=%+v", before, after)
	}
}
