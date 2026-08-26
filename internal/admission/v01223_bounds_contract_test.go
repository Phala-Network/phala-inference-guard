package admission

import (
	"sync"
	"testing"
	"time"
)

func TestV01223WindowConcurrencyAdmitsExactlyThroughConfiguredBound(t *testing.T) {
	now := time.Unix(80_000, 0)
	controller := testControllerWithBounds(
		t,
		ControllerConfig{WindowConcurrency: 4},
		testObservation(now, 0, 0, 0, 0),
	)

	batch := controller.Admit(now.Add(time.Millisecond), testDemand(3))
	if !batch.Decision.Admitted() || batch.Decision.ProjectedWindowSequences != 3 {
		t.Fatalf("batch admission=%+v", batch.Decision)
	}
	lastFit := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !lastFit.Decision.Admitted() || lastFit.Decision.ProjectedWindowSequences != 4 {
		t.Fatalf("last fitting admission=%+v", lastFit.Decision)
	}
	overflow := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if overflow.Decision.Admitted() || overflow.Decision.Reason != ReasonWindowConcurrency ||
		overflow.Decision.ReservationID != 0 || overflow.Handle.usable() ||
		overflow.Decision.ProjectedWindowSequences != 5 {
		t.Fatalf("window overflow=%+v", overflow)
	}
}

func TestV01223RunningLimitAdmitsExactlyThroughConfiguredBound(t *testing.T) {
	now := time.Unix(80_100, 0)
	controller := testControllerWithBounds(
		t,
		ControllerConfig{
			WindowConcurrency: 10,
			RunningLimit:      8,
			RunningLimitSource: RunningLimitSourceEnvironment,
		},
		testObservation(now, 6, 0, 0, 0),
	)

	lastFit := controller.Admit(now.Add(time.Millisecond), testDemand(2))
	if !lastFit.Decision.Admitted() || lastFit.Decision.ProjectedRunning != 8 {
		t.Fatalf("last fitting admission=%+v", lastFit.Decision)
	}
	overflow := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if overflow.Decision.Admitted() || overflow.Decision.Reason != ReasonRunningLimit ||
		overflow.Decision.ReservationID != 0 || overflow.Handle.usable() ||
		overflow.Decision.ProjectedRunning != 9 {
		t.Fatalf("running overflow=%+v", overflow)
	}
}

func TestV01223ConcurrentAdmissionsCannotOverspendWindowOrRunningBound(t *testing.T) {
	now := time.Unix(80_200, 0)
	controller := testControllerWithBounds(
		t,
		ControllerConfig{
			WindowConcurrency: 32,
			RunningLimit:      40,
			RunningLimitSource: RunningLimitSourceEnvironment,
		},
		testObservation(now, 8, 0, 0, 0),
	)

	const callers = 128
	results := make(chan AdmissionResult, callers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- controller.Admit(now.Add(time.Millisecond), testDemand(1))
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var admitted int64
	for result := range results {
		if result.Decision.Admitted() {
			admitted++
			continue
		}
		if result.Decision.Reason != ReasonRunningLimit && result.Decision.Reason != ReasonWindowConcurrency {
			t.Fatalf("unexpected protection=%+v", result.Decision)
		}
		if result.Decision.ReservationID != 0 || result.Handle.usable() {
			t.Fatalf("protected admission retained reservation=%+v", result)
		}
	}
	if admitted != 32 {
		t.Fatalf("admitted=%d want=32", admitted)
	}
	snapshot := controller.Snapshot(now.Add(time.Millisecond))
	if snapshot.State.UnobservedSequences != 32 || snapshot.State.LiveReservations != 32 {
		t.Fatalf("atomic bound snapshot=%+v", snapshot.State)
	}
}

func TestV01223PolicyUpdatesKeepTPSAndBoundsIndependent(t *testing.T) {
	now := time.Unix(80_300, 0)
	reference := 25.0
	controller := testControllerWithBounds(
		t,
		ControllerConfig{
			TPS:               TPSPolicyConfig{Reference: reference},
			WindowConcurrency: 32,
			RunningLimit:      256,
			RunningLimitSource: RunningLimitSourceSGLangServerInfo,
		},
		testObservation(now, 0, 0, 0, 0),
	)

	window := int64(48)
	update, err := controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision:  1,
		WindowConcurrency: &window,
		UpdatedAt:         now.Add(time.Second),
	})
	if err != nil || update.TPSWindowReset || update.Policy.Revision != 2 ||
		update.Policy.TPSReference != reference || update.Policy.WindowConcurrency != 48 ||
		update.Policy.RunningLimit != 256 ||
		update.Policy.RunningLimitSource != RunningLimitSourceSGLangServerInfo {
		t.Fatalf("window-only update=%+v err=%v", update, err)
	}

	nextReference := 30.0
	update, err = controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision: 2,
		TPSReference:    &nextReference,
		UpdatedAt:       now.Add(2 * time.Second),
	})
	if err != nil || !update.TPSWindowReset || update.Policy.Revision != 3 ||
		update.Policy.WindowConcurrency != 48 || update.Policy.RunningLimit != 256 {
		t.Fatalf("TPS-only update=%+v err=%v", update, err)
	}

	running := int64(192)
	update, err = controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision: 3,
		RunningLimit:     &running,
		UpdatedAt:       now.Add(3 * time.Second),
	})
	if err != nil || update.TPSWindowReset || update.Policy.Revision != 4 ||
		update.Policy.RunningLimit != 192 || update.Policy.RunningLimitSource != RunningLimitSourceAdmin ||
		update.Policy.TPSReference != nextReference || update.Policy.WindowConcurrency != 48 {
		t.Fatalf("running-only update=%+v err=%v", update, err)
	}
}

func testControllerWithBounds(
	t *testing.T,
	config ControllerConfig,
	observation BackendObservation,
) *AdmissionController {
	t.Helper()
	config.RuntimeIdentity = testRuntimeIdentity
	controller, err := NewAdmissionController(config)
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, observation)
	return controller
}
