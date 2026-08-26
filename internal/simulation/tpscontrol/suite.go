package tpscontrol

import (
	"fmt"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const (
	DefaultReferenceTPS       = 25.0
	DefaultPollInterval       = 500 * time.Millisecond
	defaultObservationMaxAge  = 1500 * time.Millisecond
	defaultRuntimeStartTime   = 1.0
	defaultSimulationUnixTime = int64(1_800_000_000)
)

type EventKind string

const (
	EventObservation EventKind = "observation"
	EventArrival     EventKind = "arrival"
	EventForward     EventKind = "forward"
	EventFirstByte   EventKind = "first_byte"
	EventTerminal    EventKind = "terminal"
)

type Event struct {
	AtMilliseconds        int64                       `json:"at_ms"`
	Kind                  EventKind                   `json:"kind"`
	RequestID             string                      `json:"request_id,omitempty"`
	DecodeSequences       int64                       `json:"decode_sequences,omitempty"`
	Running               int64                       `json:"running,omitempty"`
	Waiting               int64                       `json:"waiting,omitempty"`
	GenerationTokensTotal uint64                      `json:"generation_tokens_total,omitempty"`
	PreemptionsTotal      uint64                      `json:"preemptions_total,omitempty"`
	RuntimeStartTime      float64                     `json:"runtime_start_time,omitempty"`
	TerminalCause         coreadmission.TerminalCause `json:"terminal_cause,omitempty"`
}

type StepResult struct {
	AtMilliseconds           int64  `json:"at_ms"`
	Kind                     string `json:"kind"`
	RequestID                string `json:"request_id,omitempty"`
	Applied                  bool   `json:"applied"`
	Action                   string `json:"action,omitempty"`
	Reason                   string `json:"reason,omitempty"`
	TPSSubreason             string `json:"tps_subreason,omitempty"`
	ProjectedRunning         int64  `json:"projected_running,omitempty"`
	ProjectedWindowSequences int64  `json:"projected_window_sequences,omitempty"`
	RunningLimit             int64  `json:"running_limit,omitempty"`
	RunningLimitSource       string `json:"running_limit_source,omitempty"`
	WindowConcurrency        int64  `json:"window_concurrency,omitempty"`
	ReservationID            uint64 `json:"reservation_id,omitempty"`
	RuntimeReset             bool   `json:"runtime_reset,omitempty"`
	RuntimeEpoch             uint64 `json:"runtime_epoch"`
	LiveReservations         int64  `json:"live_reservations"`
	UnobservedSequences      int64  `json:"unobserved_sequences"`
	SequenceLiabilities      int64  `json:"sequence_liabilities"`
	ResidualDebts            int64  `json:"residual_debts"`
}

type ScenarioSummary struct {
	Observations          int   `json:"observations"`
	RuntimeResets         int   `json:"runtime_resets"`
	Arrivals              int   `json:"arrivals"`
	Admitted              int   `json:"admitted"`
	Protected             int   `json:"protected"`
	LifecycleApplied      int   `json:"lifecycle_applied"`
	LifecycleRejected     int   `json:"lifecycle_rejected"`
	FinalLiveReservations int64 `json:"final_live_reservations"`
	FinalUnobserved       int64 `json:"final_unobserved_sequences"`
	FinalLiabilities      int64 `json:"final_sequence_liabilities"`
	FinalResidualDebts    int64 `json:"final_residual_debts"`
}

type ScenarioResult struct {
	Name     string          `json:"name"`
	Category string          `json:"category"`
	Steps    []StepResult    `json:"steps"`
	Summary  ScenarioSummary `json:"summary"`
}

type Suite struct {
	Contract                 string           `json:"contract"`
	ReferenceTPS             float64          `json:"reference_tps"`
	PollIntervalMilliseconds int64            `json:"poll_interval_ms"`
	Scenarios                []ScenarioResult `json:"scenarios"`
}

type scenario struct {
	name     string
	category string
	events   []Event
}

type simulationClock struct {
	now time.Time
}

func (c *simulationClock) Now() time.Time {
	return c.now
}

func (c *simulationClock) Set(now time.Time) {
	c.now = now
}

func RunDefaultSuite() (Suite, error) {
	suite := Suite{
		Contract:                 "diagnostic_only",
		ReferenceTPS:             DefaultReferenceTPS,
		PollIntervalMilliseconds: DefaultPollInterval.Milliseconds(),
	}
	for _, spec := range defaultScenarios() {
		result, err := runScenario(spec)
		if err != nil {
			return Suite{}, fmt.Errorf("scenario %s: %w", spec.name, err)
		}
		suite.Scenarios = append(suite.Scenarios, result)
	}
	return suite, nil
}

func runScenario(spec scenario) (ScenarioResult, error) {
	if spec.name == "" || spec.category == "" || len(spec.events) == 0 {
		return ScenarioResult{}, fmt.Errorf("identity is invalid")
	}
	start := time.Unix(defaultSimulationUnixTime, 0)
	clock := &simulationClock{now: start}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: "tps-controller-simulation",
		TPS:             coreadmission.TPSPolicyConfig{Reference: DefaultReferenceTPS},
		Now:             clock.Now,
	})
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("construct controller: %w", err)
	}
	result := ScenarioResult{Name: spec.name, Category: spec.category}
	handles := make(map[string]coreadmission.ReservationHandle)
	previousAt := int64(-1)
	for _, event := range spec.events {
		if event.AtMilliseconds < 0 || event.AtMilliseconds < previousAt {
			return ScenarioResult{}, fmt.Errorf("events are not monotonic")
		}
		previousAt = event.AtMilliseconds
		now := start.Add(time.Duration(event.AtMilliseconds) * time.Millisecond)
		clock.Set(now)
		step, eventErr := applyEvent(controller, handles, now, event)
		if eventErr != nil {
			return ScenarioResult{}, eventErr
		}
		result.Steps = append(result.Steps, step)
		result.Summary.observe(step)
	}
	if len(handles) != 0 {
		return ScenarioResult{}, fmt.Errorf("%d request handles did not reach a terminal event", len(handles))
	}
	final := controller.Snapshot(clock.Now()).State
	result.Summary.FinalLiveReservations = final.LiveReservations
	result.Summary.FinalUnobserved = final.UnobservedSequences
	result.Summary.FinalLiabilities = final.SequenceLiabilities
	result.Summary.FinalResidualDebts = final.ResidualDebts
	return result, nil
}

func applyEvent(
	controller *coreadmission.AdmissionController,
	handles map[string]coreadmission.ReservationHandle,
	now time.Time,
	event Event,
) (StepResult, error) {
	step := StepResult{
		AtMilliseconds: event.AtMilliseconds,
		Kind:           string(event.Kind),
		RequestID:      event.RequestID,
	}
	switch event.Kind {
	case EventObservation:
		window, ok := controller.StartSampleWindow()
		if !ok {
			return StepResult{}, fmt.Errorf("sample window is unavailable at %dms", event.AtMilliseconds)
		}
		runtimeStart := event.RuntimeStartTime
		if runtimeStart == 0 {
			runtimeStart = defaultRuntimeStartTime
		}
		publication := controller.PublishObservation(window, coreadmission.BackendObservation{
			RuntimeIdentity:       "tps-controller-simulation",
			ObservedAt:            now,
			MaximumAge:            defaultObservationMaxAge,
			Running:               event.Running,
			Waiting:               event.Waiting,
			GenerationTokensTotal: event.GenerationTokensTotal,
			PreemptionsTotal:      event.PreemptionsTotal,
			RuntimeStartTime:      runtimeStart,
		})
		step.Applied = publication.Accepted
		step.Reason = string(publication.Reason)
		step.RuntimeReset = publication.RuntimeReset
		if !publication.Accepted {
			return StepResult{}, fmt.Errorf("observation rejected at %dms: %s", event.AtMilliseconds, publication.Reason)
		}
	case EventArrival:
		if event.RequestID == "" || event.DecodeSequences <= 0 {
			return StepResult{}, fmt.Errorf("arrival is invalid at %dms", event.AtMilliseconds)
		}
		if _, exists := handles[event.RequestID]; exists {
			return StepResult{}, fmt.Errorf("request %s already has a live handle", event.RequestID)
		}
		admission := controller.Admit(now, coreadmission.NewTPSRequestDemand(event.DecodeSequences))
		decision := admission.Decision
		step.Applied = decision.Admitted()
		step.Action = string(decision.Action)
		step.Reason = string(decision.Reason)
		step.TPSSubreason = decision.TPSDecisionSubreason.String()
		step.ProjectedRunning = decision.ProjectedRunning
		step.ProjectedWindowSequences = decision.ProjectedWindowSequences
		step.RunningLimit = decision.RunningLimit
		step.RunningLimitSource = string(decision.RunningLimitSource)
		step.WindowConcurrency = decision.WindowConcurrency
		step.ReservationID = decision.ReservationID
		if decision.Admitted() {
			handles[event.RequestID] = admission.Handle
		}
	case EventForward:
		handle, ok := handles[event.RequestID]
		if !ok {
			return StepResult{}, fmt.Errorf("request %s has no handle for forward", event.RequestID)
		}
		step.Applied = handle.MarkForwarded()
	case EventFirstByte:
		handle, ok := handles[event.RequestID]
		if !ok {
			return StepResult{}, fmt.Errorf("request %s has no handle for first byte", event.RequestID)
		}
		step.Applied = handle.MarkFirstByte()
	case EventTerminal:
		handle, ok := handles[event.RequestID]
		if !ok {
			return StepResult{}, fmt.Errorf("request %s has no handle for terminal", event.RequestID)
		}
		step.Applied = handle.Terminate(event.TerminalCause)
		delete(handles, event.RequestID)
	default:
		return StepResult{}, fmt.Errorf("event kind %q is invalid", event.Kind)
	}
	return withSnapshot(step, controller.Snapshot(now)), nil
}

func withSnapshot(step StepResult, snapshot coreadmission.CapacitySnapshot) StepResult {
	step.RuntimeEpoch = snapshot.RuntimeEpoch
	step.LiveReservations = snapshot.State.LiveReservations
	step.UnobservedSequences = snapshot.State.UnobservedSequences
	step.SequenceLiabilities = snapshot.State.SequenceLiabilities
	step.ResidualDebts = snapshot.State.ResidualDebts
	return step
}

func (s *ScenarioSummary) observe(step StepResult) {
	switch EventKind(step.Kind) {
	case EventObservation:
		s.Observations++
		if step.RuntimeReset {
			s.RuntimeResets++
		}
	case EventArrival:
		s.Arrivals++
		if step.Applied {
			s.Admitted++
		} else {
			s.Protected++
		}
	default:
		if step.Applied {
			s.LifecycleApplied++
		} else {
			s.LifecycleRejected++
		}
	}
}

func defaultScenarios() []scenario {
	return []scenario{
		healthyExpansionScenario(),
		atomicBatchScenario(),
		pressureRecoveryScenario("waiting_clear", true),
		pressureRecoveryScenario("preemption_clear", false),
		lowFlowScenario(),
		terminalLifecycleScenario(),
		runtimeResetScenario(),
		degradationScenario(),
		staleRecoveryScenario(),
	}
}

func healthyExpansionScenario() scenario {
	events, _ := healthyObservations(2, 50)
	events = append(events,
		arrival(4_100, "healthy-probe", 1),
		terminal(4_200, "healthy-probe", coreadmission.TerminalCancel),
	)
	return scenario{name: "healthy_window_throughput", category: "healthy", events: events}
}

func atomicBatchScenario() scenario {
	events, _ := healthyObservations(2, 50)
	events = append(events,
		arrival(4_100, "batch-32", 32),
		arrival(4_100, "same-snapshot-extra", 1),
		terminal(4_200, "batch-32", coreadmission.TerminalCancel),
	)
	return scenario{name: "same_snapshot_batch_reservation", category: "atomicity", events: events}
}

func pressureRecoveryScenario(name string, waiting bool) scenario {
	events, generation := healthyObservations(3, 50)
	preemptions := uint64(0)
	waitingCount := int64(0)
	if waiting {
		waitingCount = 1
	} else {
		preemptions = 1
	}
	generation += 50
	events = append(events,
		observation(4_500, 3, waitingCount, generation, preemptions, 1),
		arrival(4_550, name+"-protected", 1),
	)
	generation += 50
	events = append(events,
		observation(5_000, 3, 0, generation, preemptions, 1),
		arrival(5_050, name+"-recovered", 1),
		terminal(5_100, name+"-recovered", coreadmission.TerminalCancel),
	)
	return scenario{name: name, category: "pressure_recovery", events: events}
}

func lowFlowScenario() scenario {
	return scenario{
		name: "low_flow_no_self_lock", category: "low_flow",
		events: []Event{
			observation(0, 0, 0, 0, 0, 1),
			arrival(100, "low-a", 1),
			terminal(200, "low-a", coreadmission.TerminalCancel),
			observation(500, 0, 0, 0, 0, 1),
			arrival(600, "low-b", 1),
			terminal(700, "low-b", coreadmission.TerminalCancel),
		},
	}
}

func terminalLifecycleScenario() scenario {
	causes := []coreadmission.TerminalCause{
		coreadmission.TerminalSuccess,
		coreadmission.TerminalCancel,
		coreadmission.TerminalError,
		coreadmission.TerminalDisconnect,
		coreadmission.TerminalTimeout,
	}
	events := []Event{observation(0, 0, 0, 0, 0, 1)}
	generation := uint64(0)
	for index, cause := range causes {
		base := int64(index*500 + 100)
		id := "terminal-" + string(cause)
		events = append(events, arrival(base, id, 1), lifecycle(base+10, EventForward, id))
		if cause == coreadmission.TerminalSuccess {
			events = append(events, lifecycle(base+20, EventFirstByte, id))
		}
		events = append(events, terminal(base+30, id, cause))
		generation++
		events = append(events, observation(base+400, 0, 0, generation, 0, 1))
	}
	return scenario{name: "terminal_lifecycle_reconciliation", category: "lifecycle", events: events}
}

func runtimeResetScenario() scenario {
	return scenario{
		name: "runtime_reset_fences_old_handle", category: "runtime_reset",
		events: []Event{
			observation(0, 0, 0, 100, 0, 1),
			arrival(100, "old-epoch", 1),
			lifecycle(110, EventForward, "old-epoch"),
			observation(500, 1, 0, 120, 0, 1),
			observation(1_000, 0, 0, 5, 0, 2),
			terminal(1_100, "old-epoch", coreadmission.TerminalError),
			arrival(1_200, "new-epoch", 1),
			terminal(1_300, "new-epoch", coreadmission.TerminalCancel),
		},
	}
}

func degradationScenario() scenario {
	events, generation := healthyObservations(4, 50)
	events = append(events,
		observation(4_500, 4, 0, generation, 0, 1),
		arrival(4_550, "zero-output-stall", 1),
		terminal(4_600, "zero-output-stall", coreadmission.TerminalCancel),
	)
	generation += 10
	events = append(events,
		observation(5_000, 4, 0, generation, 0, 1),
		arrival(5_050, "degraded-output", 1),
	)
	return scenario{name: "observed_output_degradation", category: "external_stall", events: events}
}

func staleRecoveryScenario() scenario {
	events, generation := healthyObservations(2, 50)
	events = append(events,
		arrival(5_600, "stale-protected", 1),
	)
	generation += 100
	events = append(events,
		observation(6_000, 2, 0, generation, 0, 1),
		arrival(6_050, "fresh-recovered", 1),
		terminal(6_100, "fresh-recovered", coreadmission.TerminalCancel),
	)
	return scenario{name: "stale_observation_recovery", category: "availability", events: events}
}

func healthyObservations(running int64, tokenDelta uint64) ([]Event, uint64) {
	events := []Event{observation(0, running, 0, 0, 0, 1)}
	generation := uint64(0)
	for index := int64(1); index <= 8; index++ {
		generation += tokenDelta
		events = append(events, observation(index*500, running, 0, generation, 0, 1))
	}
	return events, generation
}

func observation(
	at int64,
	running, waiting int64,
	generation, preemptions uint64,
	runtimeStart float64,
) Event {
	return Event{
		AtMilliseconds: at, Kind: EventObservation,
		Running: running, Waiting: waiting,
		GenerationTokensTotal: generation, PreemptionsTotal: preemptions,
		RuntimeStartTime: runtimeStart,
	}
}

func arrival(at int64, requestID string, sequences int64) Event {
	return Event{
		AtMilliseconds: at, Kind: EventArrival,
		RequestID: requestID, DecodeSequences: sequences,
	}
}

func lifecycle(at int64, kind EventKind, requestID string) Event {
	return Event{AtMilliseconds: at, Kind: kind, RequestID: requestID}
}

func terminal(at int64, requestID string, cause coreadmission.TerminalCause) Event {
	return Event{
		AtMilliseconds: at, Kind: EventTerminal,
		RequestID: requestID, TerminalCause: cause,
	}
}
