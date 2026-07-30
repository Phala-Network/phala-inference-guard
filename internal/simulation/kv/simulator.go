package kv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/kvshadow"
)

type Event struct {
	AtMS                        int64          `json:"at_ms"`
	StepMS                      int64          `json:"step_ms,omitempty"`
	Type                        string         `json:"type"`
	Name                        string         `json:"name,omitempty"`
	Backend                     string         `json:"backend,omitempty"`
	Kind                        string         `json:"kind,omitempty"`
	ID                          string         `json:"id,omitempty"`
	IDPrefix                    string         `json:"id_prefix,omitempty"`
	Repeat                      int            `json:"repeat,omitempty"`
	CapacityTokens              int64          `json:"capacity_tokens,omitempty"`
	UsedTokens                  int64          `json:"used_tokens,omitempty"`
	AvailableTokens             int64          `json:"available_tokens,omitempty"`
	EvictableTokens             int64          `json:"evictable_tokens,omitempty"`
	GenerationTokens            uint64         `json:"generation_tokens,omitempty"`
	GenerationTPS               float64        `json:"generation_tps,omitempty"`
	Waiting                     int            `json:"waiting,omitempty"`
	Running                     int            `json:"running,omitempty"`
	PreemptionDelta             uint64         `json:"preemption_delta,omitempty"`
	PreemptionDeltaValid        *bool          `json:"preemption_delta_valid,omitempty"`
	TokenMetricsValid           *bool          `json:"token_metrics_valid,omitempty"`
	Failed                      bool           `json:"failed,omitempty"`
	EstimateLow                 int64          `json:"estimate_low,omitempty"`
	EstimateHigh                int64          `json:"estimate_high,omitempty"`
	DecodeTokens                int64          `json:"decode_tokens,omitempty"`
	ActualTokens                *int64         `json:"actual_tokens,omitempty"`
	Class                       string         `json:"class,omitempty"`
	Expect                      string         `json:"expect,omitempty"`
	ExpectBackend               string         `json:"expect_backend,omitempty"`
	ControlLimit                int            `json:"control_limit,omitempty"`
	VLLMTarget                  float64        `json:"vllm_target,omitempty"`
	VLLMHard                    float64        `json:"vllm_hard,omitempty"`
	VLLMEmergency               float64        `json:"vllm_emergency,omitempty"`
	SGLangTarget                float64        `json:"sglang_target,omitempty"`
	SGLangHard                  float64        `json:"sglang_hard,omitempty"`
	SGLangEmergency             float64        `json:"sglang_emergency,omitempty"`
	MaxMetricsAgeMS             int64          `json:"max_metrics_age_ms,omitempty"`
	CooldownMS                  int64          `json:"cooldown_ms,omitempty"`
	DecodeDriftTokens           *int64         `json:"decode_drift_tokens,omitempty"`
	ReservationTTLMS            int64          `json:"reservation_ttl_ms,omitempty"`
	ExpectShadowFit             *int           `json:"expect_shadow_fit,omitempty"`
	ExpectControlFit            *int           `json:"expect_control_fit,omitempty"`
	ExpectHardViolations        *int           `json:"expect_hard_violations,omitempty"`
	ExpectControlHardViolations *int           `json:"expect_control_hard_violations,omitempty"`
	ExpectReservations          *int           `json:"expect_reservations,omitempty"`
	ExpectDecisionCounts        map[string]int `json:"expect_decision_counts,omitempty"`
	MinImprovementPercent       float64        `json:"min_improvement_percent,omitempty"`
}

type Result struct {
	Name                        string                     `json:"name"`
	Events                      int                        `json:"events"`
	Requests                    int                        `json:"requests"`
	ShadowFit                   int                        `json:"shadow_fit"`
	ControlFit                  int                        `json:"control_fit"`
	SafeShortShadowFit          int                        `json:"safe_short_shadow_fit"`
	SafeShortControlFit         int                        `json:"safe_short_control_fit"`
	HardBudgetViolations        int                        `json:"hard_budget_violations"`
	ControlHardBudgetViolations int                        `json:"control_hard_budget_violations"`
	Decisions                   map[kvadmission.Reason]int `json:"decisions"`
	FinalReservations           int                        `json:"final_reservations"`
	FinalUnabsorbedTokens       int64                      `json:"final_unabsorbed_tokens"`
	ImprovementPercent          float64                    `json:"improvement_percent"`
}

type actualReservation struct {
	Backend string
	Tokens  int64
}

type runner struct {
	name                  string
	base                  time.Time
	policy                kvadmission.Policy
	manager               *kvshadow.Manager
	backends              map[string]kvadmission.BackendSnapshot
	controlLimit          int
	controlRunning        int
	controlReserved       map[string]bool
	shadowActualUsed      map[string]int64
	controlActualUsed     map[string]int64
	shadowActualReserved  map[string]actualReservation
	controlActualReserved map[string]actualReservation
	result                Result
}

func Run(name string, input io.Reader) (Result, error) {
	events, err := decodeEvents(input)
	if err != nil {
		return Result{}, err
	}
	r := &runner{
		name:                  name,
		base:                  time.Unix(0, 0),
		policy:                kvadmission.DefaultPolicy(),
		backends:              make(map[string]kvadmission.BackendSnapshot),
		controlReserved:       make(map[string]bool),
		shadowActualUsed:      make(map[string]int64),
		controlActualUsed:     make(map[string]int64),
		shadowActualReserved:  make(map[string]actualReservation),
		controlActualReserved: make(map[string]actualReservation),
		result: Result{
			Name:      name,
			Decisions: make(map[kvadmission.Reason]int),
		},
	}
	for index, event := range events {
		if err := r.apply(event); err != nil {
			return Result{}, fmt.Errorf("%s event %d (%s): %w", name, index+1, event.Type, err)
		}
	}
	if r.manager != nil {
		snapshot := r.manager.Snapshot()
		r.result.FinalReservations = snapshot.Reservations
		r.result.FinalUnabsorbedTokens = snapshot.UnabsorbedTokens
	}
	if r.result.SafeShortControlFit > 0 {
		r.result.ImprovementPercent = 100 * float64(r.result.SafeShortShadowFit-r.result.SafeShortControlFit) / float64(r.result.SafeShortControlFit)
	}
	return r.result, nil
}

func decodeEvents(input io.Reader) ([]Event, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	events := make([]Event, 0)
	line := 0
	for scanner.Scan() {
		line++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *runner) apply(event Event) error {
	r.result.Events++
	switch event.Type {
	case "config":
		if r.manager != nil {
			return fmt.Errorf("config must precede samples and requests")
		}
		r.configure(event)
		return nil
	case "sample":
		r.ensureManager()
		return r.sample(event)
	case "request":
		r.ensureManager()
		return r.requests(event)
	case "complete":
		r.ensureManager()
		r.manager.Release(event.ID)
		r.releaseActual(event.ID)
		if r.controlReserved[event.ID] {
			delete(r.controlReserved, event.ID)
			if r.controlRunning > 0 {
				r.controlRunning--
			}
		}
		return nil
	case "sweep":
		r.ensureManager()
		r.manager.Sweep(r.at(event.AtMS))
		return nil
	case "assert":
		r.ensureManager()
		return r.assert(event)
	default:
		return fmt.Errorf("unknown event type %q", event.Type)
	}
}

func (r *runner) configure(event Event) {
	if event.ControlLimit > 0 {
		r.controlLimit = event.ControlLimit
	}
	applyBudget := func(target, hard, emergency float64, budget *kvadmission.Budget) {
		if target > 0 {
			budget.TargetRatio = target
		}
		if hard > 0 {
			budget.HardRatio = hard
		}
		if emergency > 0 {
			budget.EmergencyRatio = emergency
		}
	}
	applyBudget(event.VLLMTarget, event.VLLMHard, event.VLLMEmergency, &r.policy.VLLM)
	applyBudget(event.SGLangTarget, event.SGLangHard, event.SGLangEmergency, &r.policy.SGLang)
	if event.MaxMetricsAgeMS > 0 {
		r.policy.MaxMetricsAge = time.Duration(event.MaxMetricsAgeMS) * time.Millisecond
	}
	if event.CooldownMS > 0 {
		r.policy.PreemptionCooldown = time.Duration(event.CooldownMS) * time.Millisecond
	}
	if event.DecodeDriftTokens != nil {
		r.policy.DecodeDriftTokens = *event.DecodeDriftTokens
	}
	if event.ReservationTTLMS > 0 {
		r.policy.ReservationTTL = time.Duration(event.ReservationTTLMS) * time.Millisecond
	}
}

func (r *runner) ensureManager() {
	if r.manager == nil {
		r.manager = kvshadow.New(r.policy)
	}
}

func (r *runner) sample(event Event) error {
	if event.Backend == "" {
		return fmt.Errorf("sample backend is required")
	}
	valid := true
	if event.TokenMetricsValid != nil {
		valid = *event.TokenMetricsValid
	}
	preemptionValid := event.PreemptionDelta > 0
	if event.PreemptionDeltaValid != nil {
		preemptionValid = *event.PreemptionDeltaValid
	}
	snapshot := kvadmission.BackendSnapshot{
		Name:                 event.Backend,
		Kind:                 kvadmission.ParseBackendKind(event.Kind),
		CapacityTokens:       event.CapacityTokens,
		UsedTokens:           event.UsedTokens,
		AvailableTokens:      event.AvailableTokens,
		EvictableTokens:      event.EvictableTokens,
		Updated:              r.at(event.AtMS),
		GenerationTokens:     event.GenerationTokens,
		GenerationTPS:        event.GenerationTPS,
		Waiting:              event.Waiting,
		PreemptionDelta:      event.PreemptionDelta,
		PreemptionDeltaValid: preemptionValid,
		Failed:               event.Failed,
		TokenMetricsValid:    valid,
	}
	if snapshot.CapacityTokens > 0 {
		snapshot.Usage = float64(snapshot.UsedTokens) / float64(snapshot.CapacityTokens)
	}
	r.backends[event.Backend] = snapshot
	r.resetActualBackend(event.Backend, event.UsedTokens)
	r.controlRunning = event.Running
	r.manager.Observe(r.at(event.AtMS), r.backendList())
	return nil
}

func (r *runner) requests(event Event) error {
	repeat := event.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	for index := 0; index < repeat; index++ {
		copy := event
		copy.AtMS += int64(index) * event.StepMS
		if repeat > 1 {
			prefix := event.IDPrefix
			if prefix == "" {
				prefix = event.ID
			}
			copy.ID = fmt.Sprintf("%s%d", prefix, index+1)
		}
		if copy.ID == "" {
			return fmt.Errorf("request id is required")
		}
		if err := r.request(copy); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) request(event Event) error {
	cost := kvadmission.Cost{
		Supported:           true,
		EstimatedInputLow:   event.EstimateLow,
		EstimatedInputHigh:  event.EstimateHigh,
		BoundedDecodeTokens: event.DecodeTokens,
	}
	actualTokens := cost.ProjectedHigh()
	if event.ActualTokens != nil {
		if *event.ActualTokens < 0 {
			return fmt.Errorf("request %s actual_tokens must be >= 0", event.ID)
		}
		actualTokens = *event.ActualTokens
	}
	decision, reserved := r.manager.DecideAndReserve(r.at(event.AtMS), event.ID, cost, r.backendList())
	r.result.Requests++
	r.result.Decisions[decision.Reason]++
	if decision.Reason == kvadmission.ReasonFit {
		r.result.ShadowFit++
		if event.Class == "short" {
			r.result.SafeShortShadowFit++
		}
		r.recordShadowActual(event.ID, decision.Backend, actualTokens, decision.HardBudgetTokens)
	}
	if event.Expect != "" && string(decision.Reason) != event.Expect {
		return fmt.Errorf("request %s decision=%s want %s", event.ID, decision.Reason, event.Expect)
	}
	if event.ExpectBackend != "" && decision.Backend != event.ExpectBackend {
		return fmt.Errorf("request %s backend=%s want %s", event.ID, decision.Backend, event.ExpectBackend)
	}
	if decision.Reason == kvadmission.ReasonFit && !reserved {
		return fmt.Errorf("request %s fit without reservation", event.ID)
	}

	if controlBackend, ok := r.countControlBackend(); ok {
		r.result.ControlFit++
		r.controlRunning++
		r.controlReserved[event.ID] = true
		r.recordControlActual(event.ID, controlBackend.Name, actualTokens, r.hardBudgetTokens(controlBackend))
		if event.Class == "short" {
			r.result.SafeShortControlFit++
		}
	}
	return nil
}

func (r *runner) countControlBackend() (kvadmission.BackendSnapshot, bool) {
	if r.controlLimit <= 0 || r.controlRunning >= r.controlLimit {
		return kvadmission.BackendSnapshot{}, false
	}
	for _, backend := range r.backendList() {
		if !backend.Failed && backend.Waiting == 0 {
			return backend, true
		}
	}
	return kvadmission.BackendSnapshot{}, false
}

func (r *runner) assert(event Event) error {
	snapshot := r.manager.Snapshot()
	if event.ExpectShadowFit != nil && r.result.ShadowFit != *event.ExpectShadowFit {
		return fmt.Errorf("shadow fit=%d want %d", r.result.ShadowFit, *event.ExpectShadowFit)
	}
	if event.ExpectControlFit != nil && r.result.ControlFit != *event.ExpectControlFit {
		return fmt.Errorf("control fit=%d want %d", r.result.ControlFit, *event.ExpectControlFit)
	}
	if event.ExpectHardViolations != nil && r.result.HardBudgetViolations != *event.ExpectHardViolations {
		return fmt.Errorf("hard violations=%d want %d", r.result.HardBudgetViolations, *event.ExpectHardViolations)
	}
	if event.ExpectControlHardViolations != nil && r.result.ControlHardBudgetViolations != *event.ExpectControlHardViolations {
		return fmt.Errorf("control hard violations=%d want %d", r.result.ControlHardBudgetViolations, *event.ExpectControlHardViolations)
	}
	if event.ExpectReservations != nil && snapshot.Reservations != *event.ExpectReservations {
		return fmt.Errorf("reservations=%d want %d", snapshot.Reservations, *event.ExpectReservations)
	}
	for rawReason, want := range event.ExpectDecisionCounts {
		reason := kvadmission.Reason(rawReason)
		if got := r.result.Decisions[reason]; got != want {
			return fmt.Errorf("decision %s count=%d want %d", reason, got, want)
		}
	}
	if event.MinImprovementPercent > 0 {
		if r.result.SafeShortControlFit <= 0 {
			return fmt.Errorf("cannot compute improvement without short control fits")
		}
		improvement := 100 * float64(r.result.SafeShortShadowFit-r.result.SafeShortControlFit) / float64(r.result.SafeShortControlFit)
		if improvement < event.MinImprovementPercent {
			return fmt.Errorf("short-fit improvement=%.2f%% want >= %.2f%%", improvement, event.MinImprovementPercent)
		}
	}
	return nil
}

func (r *runner) backendList() []kvadmission.BackendSnapshot {
	result := make([]kvadmission.BackendSnapshot, 0, len(r.backends))
	for _, backend := range r.backends {
		result = append(result, backend)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *runner) resetActualBackend(backend string, usedTokens int64) {
	r.shadowActualUsed[backend] = usedTokens
	r.controlActualUsed[backend] = usedTokens
	for id, reservation := range r.shadowActualReserved {
		if reservation.Backend == backend {
			delete(r.shadowActualReserved, id)
		}
	}
	for id, reservation := range r.controlActualReserved {
		if reservation.Backend == backend {
			delete(r.controlActualReserved, id)
		}
	}
}

func (r *runner) releaseActual(requestID string) {
	if reservation, ok := r.shadowActualReserved[requestID]; ok {
		r.shadowActualUsed[reservation.Backend] -= reservation.Tokens
		if r.shadowActualUsed[reservation.Backend] < 0 {
			r.shadowActualUsed[reservation.Backend] = 0
		}
		delete(r.shadowActualReserved, requestID)
	}
	if reservation, ok := r.controlActualReserved[requestID]; ok {
		r.controlActualUsed[reservation.Backend] -= reservation.Tokens
		if r.controlActualUsed[reservation.Backend] < 0 {
			r.controlActualUsed[reservation.Backend] = 0
		}
		delete(r.controlActualReserved, requestID)
	}
}

func (r *runner) recordShadowActual(requestID, backend string, tokens, hardBudget int64) {
	r.shadowActualUsed[backend] += tokens
	r.shadowActualReserved[requestID] = actualReservation{Backend: backend, Tokens: tokens}
	if hardBudget > 0 && r.shadowActualUsed[backend] > hardBudget {
		r.result.HardBudgetViolations++
	}
}

func (r *runner) recordControlActual(requestID, backend string, tokens, hardBudget int64) {
	r.controlActualUsed[backend] += tokens
	r.controlActualReserved[requestID] = actualReservation{Backend: backend, Tokens: tokens}
	if hardBudget > 0 && r.controlActualUsed[backend] > hardBudget {
		r.result.ControlHardBudgetViolations++
	}
}

func (r *runner) hardBudgetTokens(backend kvadmission.BackendSnapshot) int64 {
	budget, ok := r.policy.BudgetFor(backend.Kind)
	if !ok || backend.CapacityTokens <= 0 {
		return 0
	}
	return int64(math.Floor(float64(backend.CapacityTokens) * budget.HardRatio))
}

func (r *runner) at(milliseconds int64) time.Time {
	return r.base.Add(time.Duration(milliseconds) * time.Millisecond)
}
