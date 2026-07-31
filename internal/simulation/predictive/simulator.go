package predictive

import (
	"fmt"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type EventKind string

const (
	EventAdmit    EventKind = "admit"
	EventComplete EventKind = "complete"
	EventSample   EventKind = "sample"
)

type Event struct {
	At     time.Duration
	Kind   EventKind
	ID     string
	Cost   domain.RequestCost
	Sample runtimepredictive.SampleWindow
}

type Scenario struct {
	ManifestID  string
	Initial     domain.VirtualState
	Constraints domain.Constraints
	Events      []Event
}

type Result struct {
	Decisions     []domain.Decision
	Completions   int
	SampleEvents  int
	FinalSnapshot runtimepredictive.Snapshot
}

func Run(start time.Time, scenario Scenario, scheduler runtimepredictive.Scheduler) (Result, error) {
	if scenario.ManifestID == "" {
		return Result{}, fmt.Errorf("scenario tokenizer manifest id is required")
	}
	manager := runtimepredictive.NewManager(scenario.ManifestID, scenario.Initial, scenario.Constraints, scheduler)
	result := Result{}
	previous := time.Duration(0)
	for index, event := range scenario.Events {
		if event.At < 0 || (index > 0 && event.At < previous) {
			return Result{}, fmt.Errorf("event %d has non-monotonic time", index)
		}
		previous = event.At
		switch event.Kind {
		case EventAdmit:
			if event.ID == "" {
				return Result{}, fmt.Errorf("event %d has empty request id", index)
			}
			result.Decisions = append(result.Decisions, manager.DecideAndReserve(start.Add(event.At), event.ID, event.Cost))
		case EventComplete:
			if !manager.Complete(event.ID) {
				return Result{}, fmt.Errorf("event %d completes unknown request %q", index, event.ID)
			}
			result.Completions++
		case EventSample:
			if err := manager.ReconcileSample(event.Sample); err != nil {
				return Result{}, fmt.Errorf("event %d reconciles invalid sample: %w", index, err)
			}
			result.SampleEvents++
		default:
			return Result{}, fmt.Errorf("event %d has unknown kind %q", index, event.Kind)
		}
	}
	result.FinalSnapshot = manager.Snapshot()
	return result, nil
}
