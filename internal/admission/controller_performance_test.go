package admission

import (
	"testing"
	"time"
)

func BenchmarkControllerSnapshot(b *testing.B) {
	controller := benchmarkController(b, 4_096)
	now := time.Unix(18_000, 0).Add(time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = controller.Snapshot(now)
	}
}

func BenchmarkControllerProtectedAdmission(b *testing.B) {
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 20},
	})
	if err != nil {
		b.Fatal(err)
	}
	window, ok := controller.StartSampleWindow()
	if !ok {
		b.Fatal("sample window unavailable")
	}
	publication := controller.PublishObservation(
		window,
		testObservation(time.Unix(18_500, 0), 1, 1, 0, 0),
	)
	if !publication.Accepted {
		b.Fatalf("publication=%+v", publication)
	}
	now := time.Unix(18_500, 0).Add(time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = controller.Admit(now, testDemand(1))
	}
}

func BenchmarkControllerAdmitAndCancel(b *testing.B) {
	controller := benchmarkController(b, 0)
	now := time.Unix(18_000, 0).Add(time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result := controller.Admit(now, testDemand(1))
		if !result.Decision.Admitted() || !result.Handle.Terminate(TerminalCancel) {
			b.Fatalf("admit/cancel=%+v", result.Decision)
		}
	}
}

func BenchmarkControllerPublishObservationWith4096Reservations(b *testing.B) {
	controller := benchmarkController(b, 4_096)
	base := time.Unix(18_750, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		window, ok := controller.StartSampleWindow()
		if !ok {
			b.Fatal("sample window unavailable")
		}
		observedAt := base.Add(time.Duration(index+1) * time.Millisecond)
		publication := controller.PublishObservation(
			window,
			testObservation(observedAt, 0, 0, uint64(index+2), 0),
		)
		if !publication.Accepted {
			b.Fatalf("publication=%+v", publication)
		}
	}
}

func benchmarkController(b *testing.B, reservations int) *AdmissionController {
	b.Helper()
	now := time.Unix(18_000, 0)
	windowConcurrency := int64(reservations)
	if windowConcurrency < DefaultWindowConcurrency {
		windowConcurrency = DefaultWindowConcurrency
	}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: windowConcurrency,
	})
	if err != nil {
		b.Fatal(err)
	}
	window, ok := controller.StartSampleWindow()
	if !ok {
		b.Fatal("sample window unavailable")
	}
	if publication := controller.PublishObservation(window, testObservation(now, 0, 0, 1, 0)); !publication.Accepted {
		b.Fatalf("publication=%+v", publication)
	}
	for index := 0; index < reservations; index++ {
		result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
		if !result.Decision.Admitted() {
			b.Fatalf("populate admission %d=%+v", index, result.Decision)
		}
	}
	return controller
}
