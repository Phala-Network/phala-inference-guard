package goodput

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/kvshadow"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	simulationManifest  = "goodput-model-agnostic-approximate"
	simulationEpoch     = "goodput-vllm-epoch-1"
	simulationBlock     = int64(64)
	simulationCapacity  = int64(1_000_000)
	simulationProtected = int64(900_000)
)

var simulationBaseTime = time.Unix(10_000, 0)

type requestSpec struct {
	ID                 string
	At                 time.Duration
	InputTokens        int64
	OutputUpper        int64
	ActualOutput       int64
	EstimatedInputHigh int64
	TerminalCause      runtimepredictive.TerminalCause
	Lifetime           time.Duration
	LocalReject        bool
	ProfileMismatch    bool
	Unsupported        bool
	Prefix             string
}

type scenarioSpec struct {
	Name                 string
	LongPromptSuite      bool
	ColdStart            bool
	Requests             []requestSpec
	CurrentLimit         int
	PollInterval         time.Duration
	StopPollingAt        time.Duration
	InitialKVTokens      int64
	InitialRunning       int
	InitialPrefillTokens int64
	ReportedWaitingUntil time.Duration
	PreemptionAt         time.Duration
	CounterResetAt       time.Duration
	EpochRestoredAt      time.Duration
	TTFTSLO              time.Duration
	BaseCompletionTPS    float64
	PrefillPenaltyPerK   float64
}

type serviceProfile struct {
	capacity               int64
	protected              int64
	baseCompletionTPS      float64
	prefillPenaltyPerK     float64
	userTPSTarget          float64
	baseTTFT               time.Duration
	backendTTFTPerToken    time.Duration
	predictiveTTFTPerToken time.Duration
	ttftSLO                time.Duration
	baseTPOT               time.Duration
	tpotPerExisting        time.Duration
	tpotSLO                time.Duration
}

type actualState struct {
	initialKV      int64
	initialRunning int
	initialPrefill int64
	active         map[string]*activeRequest
	kvOverloaded   bool
}

type activeRequest struct {
	spec          requestSpec
	admittedAt    time.Duration
	terminalAt    time.Duration
	terminalCause runtimepredictive.TerminalCause
	prefillActive bool
	ttft          time.Duration
	tps           float64
	tpot          time.Duration
	tpsViolation  bool
	ttftViolation bool
	tpotViolation bool
	kvViolation   bool
}

type groundEvaluation struct {
	projectedKV int64
	userTPS     float64
	ttft        time.Duration
	tpot        time.Duration
	kvSafe      bool
	tpsSafe     bool
	ttftSafe    bool
	tpotSafe    bool
}

func (e groundEvaluation) safe() bool {
	return e.kvSafe && e.tpsSafe && e.ttftSafe && e.tpotSafe
}

type observedState struct {
	kvTokens        int64
	decodeSequences int
	prefillTokens   int64
	waiting         int
	preemption      bool
	counterReset    bool
	epochRestored   bool
}

type simulationController interface {
	Admit(time.Time, requestSpec) (bool, string)
	MarkForwarded(string)
	MarkSemantic(string)
	Terminate(time.Time, string, runtimepredictive.TerminalCause, simulatedRequestOutcome)
	Observe(time.Time, observedState) error
	Reservations() int
	ReservedPhysicalKVUpper() int64
	UsesExactTokenizer() bool
}

type simulatedRequestOutcome struct {
	completionTokens int64
	userTPS          float64
	ttft             time.Duration
	tpot             time.Duration
}

type simulationEventKind uint8

const (
	eventTerminal simulationEventKind = iota
	eventSemantic
	eventPoll
	eventArrival
)

type simulationEvent struct {
	at      time.Duration
	kind    simulationEventKind
	request requestSpec
	id      string
	cause   runtimepredictive.TerminalCause
	order   int
}

func runAcceptanceSuite() (SuiteResult, error) {
	scenarios := acceptanceScenarios()
	result := SuiteResult{Scenarios: make([]ScenarioResult, 0, len(scenarios))}
	for _, scenario := range scenarios {
		scenarioResult := ScenarioResult{
			Name:            scenario.Name,
			LongPromptSuite: scenario.LongPromptSuite,
			Policies:        make(map[PolicyName]Metrics, 4),
		}
		for _, policy := range []PolicyName{PolicyCurrentThreshold, PolicyV090KVOnly, PolicyExactKVOnly, PolicyPredictiveQoS} {
			metrics, err := runScenario(scenario, policy)
			if err != nil {
				return SuiteResult{}, fmt.Errorf("scenario %s policy %s: %w", scenario.Name, policy, err)
			}
			scenarioResult.Policies[policy] = metrics
		}
		result.Scenarios = append(result.Scenarios, scenarioResult)
	}
	return result, nil
}

func runScenario(scenario scenarioSpec, policy PolicyName) (Metrics, error) {
	profile := scenario.serviceProfile()
	controller, err := newSimulationController(policy, scenario, profile)
	if err != nil {
		return Metrics{}, err
	}
	if policy == PolicyPredictiveQoS && !scenario.ColdStart {
		if err := warmPredictiveController(controller, profile, scenario); err != nil {
			return Metrics{}, fmt.Errorf("warm predictive controller: %w", err)
		}
	}
	state := &actualState{
		initialKV:      scenario.InitialKVTokens,
		initialRunning: scenario.InitialRunning,
		initialPrefill: scenario.InitialPrefillTokens,
		active:         make(map[string]*activeRequest),
	}
	metrics := Metrics{MinimumProjectedKVHeadroom: profile.protected - state.initialKV}
	events := initialEvents(scenario)
	order := len(events)
	for len(events) > 0 {
		event := events[0]
		events = events[1:]
		now := simulationBaseTime.Add(event.at)
		markRuntimeKV(&metrics, state, profile, event.at)
		switch event.kind {
		case eventPoll:
			observed := state.observed(scenario, event.at)
			if err := controller.Observe(now, observed); err != nil {
				return Metrics{}, err
			}
		case eventArrival:
			metrics.Arrivals++
			evaluation := state.evaluate(profile, controller.UsesExactTokenizer(), event.request, event.at)
			admitted, _ := controller.Admit(now, event.request)
			if !admitted {
				if evaluation.safe() && !event.request.ProfileMismatch && !event.request.Unsupported {
					metrics.FalseDenies++
				}
				continue
			}
			updateReservedKV(&metrics, controller.ReservedPhysicalKVUpper())
			if event.request.LocalReject {
				controller.Terminate(now, event.request.ID, runtimepredictive.TerminalLocalQoSReject, simulatedRequestOutcome{})
				continue
			}
			controller.MarkForwarded(event.request.ID)
			metrics.Admitted++
			if !evaluation.safe() {
				metrics.FalseAccepts++
			}
			active := &activeRequest{
				spec:          event.request,
				admittedAt:    event.at,
				prefillActive: true,
				ttft:          evaluation.ttft,
				tps:           evaluation.userTPS,
				tpot:          evaluation.tpot,
			}
			state.active[event.request.ID] = active
			markGroundViolations(&metrics, state, active, evaluation)
			markRuntimeKV(&metrics, state, profile, event.at)

			cause := event.request.TerminalCause
			if cause == "" {
				cause = runtimepredictive.TerminalCompleted
			}
			lifetime := event.request.Lifetime
			if lifetime <= 0 {
				if cause == runtimepredictive.TerminalCompleted {
					lifetime = evaluation.ttft + completionDuration(event.request.ActualOutput, evaluation.userTPS)
				} else {
					lifetime = 100 * time.Millisecond
				}
			}
			active.terminalAt = event.at + lifetime
			active.terminalCause = cause
			if evaluation.ttft > 0 && evaluation.ttft < lifetime {
				order++
				events = append(events, simulationEvent{at: event.at + evaluation.ttft, kind: eventSemantic, id: event.request.ID, order: order})
			}
			order++
			events = append(events, simulationEvent{at: event.at + lifetime, kind: eventTerminal, id: event.request.ID, cause: cause, order: order})
			sortSimulationEvents(events)
		case eventSemantic:
			active := state.active[event.id]
			if active == nil || !active.prefillActive {
				continue
			}
			active.prefillActive = false
			controller.MarkSemantic(event.id)
		case eventTerminal:
			active := state.active[event.id]
			if active == nil {
				continue
			}
			controller.Terminate(now, event.id, event.cause, simulatedRequestOutcome{
				completionTokens: active.spec.ActualOutput,
				userTPS:          active.tps,
				ttft:             active.ttft,
				tpot:             active.tpot,
			})
			delete(state.active, event.id)
			if event.cause == runtimepredictive.TerminalCompleted {
				metrics.Completed++
				if !active.tpsViolation && !active.ttftViolation && !active.tpotViolation && !active.kvViolation {
					metrics.SLOCompliantCompletions++
					metrics.CompletionTokenGoodput += active.spec.ActualOutput
				}
			}
		}
	}
	metrics.ReservationLeaks = controller.Reservations()
	return metrics, nil
}

func warmPredictiveController(controller simulationController, profile serviceProfile, scenario scenarioSpec) error {
	shapes := predictiveWarmupShapes(scenario)
	if len(shapes) == 0 {
		shapes = []requestSpec{request("predictive-warmup-fallback", 0, 49, 64, 64)}
	}
	start := simulationBaseTime.Add(-59 * time.Second)
	step := 0
	for shapeIndex, shape := range shapes {
		safeConcurrency := matureSafeConcurrency(profile, shape)
		singleSamples := 8
		if safeConcurrency == 0 {
			singleSamples = 4
		}
		for sample := 0; sample < singleSamples; sample++ {
			now := start.Add(time.Duration(step) * 500 * time.Millisecond)
			step++
			candidate := shape
			candidate.ID = fmt.Sprintf("predictive-warmup-s%d-single-%d", shapeIndex, sample)
			if err := admitWarmupWave(controller, profile, now, []requestSpec{candidate}); err != nil {
				return err
			}
		}
		for concurrency := 2; concurrency <= safeConcurrency; concurrency++ {
			for wave := 0; wave < 4; wave++ {
				now := start.Add(time.Duration(step) * 500 * time.Millisecond)
				step++
				requests := make([]requestSpec, concurrency)
				for index := range requests {
					requests[index] = shape
					requests[index].ID = fmt.Sprintf("predictive-warmup-s%d-c%d-w%d-%d", shapeIndex, concurrency, wave, index)
				}
				if err := admitWarmupWave(controller, profile, now, requests); err != nil {
					return err
				}
			}
		}
	}
	if reservations := controller.Reservations(); reservations != 0 {
		return fmt.Errorf("warmup leaked %d reservations", reservations)
	}
	return nil
}

func predictiveWarmupShapes(scenario scenarioSpec) []requestSpec {
	type shapeKey struct {
		input, estimated, output int64
	}
	seen := make(map[shapeKey]struct{}, len(scenario.Requests))
	result := make([]requestSpec, 0, len(scenario.Requests))
	for _, candidate := range scenario.Requests {
		cause := candidate.TerminalCause
		if cause == "" {
			cause = runtimepredictive.TerminalCompleted
		}
		if cause != runtimepredictive.TerminalCompleted || candidate.LocalReject || candidate.ActualOutput <= 0 {
			continue
		}
		key := shapeKey{input: candidate.InputTokens, estimated: candidate.EstimatedInputHigh, output: candidate.OutputUpper}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func admitWarmupWave(controller simulationController, profile serviceProfile, now time.Time, requests []requestSpec) error {
	if err := controller.Observe(now, observedState{}); err != nil {
		return fmt.Errorf("observe warmup wave: %w", err)
	}
	ids := make([]string, 0, len(requests))
	var totalInput int64
	for index, candidate := range requests {
		admitted, reason := controller.Admit(now, candidate)
		if !admitted {
			if predictive, ok := controller.(*predictiveSimulationController); ok {
				return fmt.Errorf("warmup concurrency %d request %d rejected as %s: prediction=%+v cost=%+v", len(requests), index, reason, predictive.lastAdmission.Prediction, predictive.lastAdmission.Cost)
			}
			return fmt.Errorf("warmup concurrency %d request %d rejected as %s", len(requests), index, reason)
		}
		controller.MarkForwarded(candidate.ID)
		ids = append(ids, candidate.ID)
		totalInput = addInt64(totalInput, candidate.InputTokens)
	}
	for _, id := range ids {
		controller.MarkSemantic(id)
	}
	aggregateTPS := profile.baseCompletionTPS - profile.prefillPenaltyPerK*float64(totalInput)/1_000
	if aggregateTPS < 0 {
		aggregateTPS = 0
	}
	userTPS := aggregateTPS / float64(len(requests))
	ttft := profile.baseTTFT + multiplyDuration(profile.backendTTFTPerToken, totalInput)
	tpot := profile.baseTPOT + time.Duration(len(requests)-1)*profile.tpotPerExisting
	for index, id := range ids {
		controller.Terminate(now.Add(250*time.Millisecond), id, runtimepredictive.TerminalCompleted, simulatedRequestOutcome{
			completionTokens: requests[index].ActualOutput, userTPS: userTPS, ttft: ttft, tpot: tpot,
		})
	}
	return nil
}

func matureSafeConcurrency(profile serviceProfile, shape requestSpec) int {
	result := 0
	for concurrency := 1; concurrency <= 8; concurrency++ {
		prefill := int64(concurrency) * shape.InputTokens
		aggregateTPS := profile.baseCompletionTPS - profile.prefillPenaltyPerK*float64(prefill)/1_000
		userTPS := aggregateTPS / float64(concurrency)
		ttft := profile.baseTTFT + multiplyDuration(profile.backendTTFTPerToken, prefill)
		tpot := profile.baseTPOT + time.Duration(concurrency-1)*profile.tpotPerExisting
		projectedKV := int64(concurrency) * roundedTokens(shape.InputTokens+shape.OutputUpper)
		if userTPS < profile.userTPSTarget || ttft > profile.ttftSLO || tpot > profile.tpotSLO || projectedKV > profile.protected {
			break
		}
		result = concurrency
	}
	return result
}

func initialEvents(scenario scenarioSpec) []simulationEvent {
	events := make([]simulationEvent, 0, len(scenario.Requests)+512)
	order := 0
	poll := scenario.PollInterval
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	stop := 120 * time.Second
	if scenario.StopPollingAt > 0 && scenario.StopPollingAt < stop {
		stop = scenario.StopPollingAt
	}
	for at := time.Duration(0); at <= stop; at += poll {
		events = append(events, simulationEvent{at: at, kind: eventPoll, order: order})
		order++
	}
	for _, request := range scenario.Requests {
		events = append(events, simulationEvent{at: request.At, kind: eventArrival, request: request, order: order})
		order++
	}
	sortSimulationEvents(events)
	return events
}

func sortSimulationEvents(events []simulationEvent) {
	sort.SliceStable(events, func(left, right int) bool {
		if events[left].at != events[right].at {
			return events[left].at < events[right].at
		}
		if events[left].kind != events[right].kind {
			return events[left].kind < events[right].kind
		}
		return events[left].order < events[right].order
	})
}

func (s scenarioSpec) serviceProfile() serviceProfile {
	ttftSLO := s.TTFTSLO
	if ttftSLO <= 0 {
		ttftSLO = 3 * time.Second
	}
	baseTPS := s.BaseCompletionTPS
	if baseTPS <= 0 {
		baseTPS = 120
	}
	penalty := s.PrefillPenaltyPerK
	if penalty <= 0 {
		penalty = 0.12
	}
	return serviceProfile{
		capacity:               simulationCapacity,
		protected:              simulationProtected,
		baseCompletionTPS:      baseTPS,
		prefillPenaltyPerK:     penalty,
		userTPSTarget:          25,
		baseTTFT:               100 * time.Millisecond,
		backendTTFTPerToken:    8 * time.Microsecond,
		predictiveTTFTPerToken: 20 * time.Microsecond,
		ttftSLO:                ttftSLO,
		baseTPOT:               12 * time.Millisecond,
		tpotPerExisting:        5 * time.Millisecond,
		tpotSLO:                35 * time.Millisecond,
	}
}

func (s *actualState) evaluate(profile serviceProfile, exactTokenizer bool, request requestSpec, at time.Duration) groundEvaluation {
	prefill := s.initialPrefill + request.InputTokens
	for _, active := range s.active {
		if active.prefillActive {
			prefill = addInt64(prefill, active.spec.InputTokens)
		}
	}
	decodeSequences := s.initialRunning + len(s.active) + 1
	aggregateTPS := profile.baseCompletionTPS - profile.prefillPenaltyPerK*float64(prefill)/1_000
	if aggregateTPS < 0 {
		aggregateTPS = 0
	}
	userTPS := aggregateTPS / float64(maxInt(1, decodeSequences))
	ttft := profile.baseTTFT + multiplyDuration(profile.backendTTFTPerToken, prefill)
	if exactTokenizer {
		ttft += tokenizerP95(request.InputTokens)
	}
	tpot := profile.baseTPOT + time.Duration(maxInt(0, decodeSequences-1))*profile.tpotPerExisting
	projectedKV := s.currentKV(at)
	projectedKV = addInt64(projectedKV, roundedTokens(request.InputTokens))
	return groundEvaluation{
		projectedKV: projectedKV,
		userTPS:     userTPS,
		ttft:        ttft,
		tpot:        tpot,
		kvSafe:      projectedKV <= profile.protected,
		tpsSafe:     userTPS >= profile.userTPSTarget,
		ttftSafe:    ttft <= profile.ttftSLO,
		tpotSafe:    tpot <= profile.tpotSLO,
	}
}

func (s *actualState) currentKV(at time.Duration) int64 {
	result := s.initialKV
	for _, active := range s.active {
		result = addInt64(result, roundedMaterializedContext(active, at))
	}
	return result
}

func (s *actualState) observed(scenario scenarioSpec, at time.Duration) observedState {
	prefill := s.initialPrefill
	for _, active := range s.active {
		if active.prefillActive {
			prefill = addInt64(prefill, active.spec.InputTokens)
		}
	}
	waiting := 0
	if scenario.ReportedWaitingUntil > 0 && at < scenario.ReportedWaitingUntil {
		waiting = 1
	}
	return observedState{
		kvTokens:        s.currentKV(at),
		decodeSequences: s.initialRunning + len(s.active) + waiting,
		prefillTokens:   prefill,
		waiting:         waiting,
		preemption:      scenario.PreemptionAt > 0 && at == scenario.PreemptionAt,
		counterReset:    scenario.CounterResetAt > 0 && at == scenario.CounterResetAt,
		epochRestored:   scenario.EpochRestoredAt > 0 && at >= scenario.EpochRestoredAt,
	}
}

func markGroundViolations(metrics *Metrics, state *actualState, candidate *activeRequest, evaluation groundEvaluation) {
	if !evaluation.tpsSafe {
		for _, active := range state.active {
			markTPSViolation(metrics, active)
		}
	}
	if !evaluation.tpotSafe {
		for _, active := range state.active {
			markTPOTViolation(metrics, active)
		}
	}
	if !evaluation.ttftSafe && !candidate.ttftViolation {
		candidate.ttftViolation = true
		metrics.TTFTViolations++
	}
}

func markRuntimeKV(metrics *Metrics, state *actualState, profile serviceProfile, at time.Duration) {
	current := state.currentKV(at)
	updateHeadroom(metrics, profile.protected, current)
	overloaded := current > profile.protected
	if overloaded && !state.kvOverloaded {
		metrics.KVHardViolations++
		metrics.PreemptionProxyEvents++
	}
	if overloaded {
		for _, active := range state.active {
			active.kvViolation = true
		}
	}
	state.kvOverloaded = overloaded
}

func markTPSViolation(metrics *Metrics, active *activeRequest) {
	if !active.tpsViolation {
		active.tpsViolation = true
		metrics.ExistingOrNewTPSViolations++
	}
}

func markTPOTViolation(metrics *Metrics, active *activeRequest) {
	if !active.tpotViolation {
		active.tpotViolation = true
		metrics.TPOTViolations++
	}
}

func updateHeadroom(metrics *Metrics, protected, projected int64) {
	if projected > metrics.PeakProjectedKVTokens {
		metrics.PeakProjectedKVTokens = projected
	}
	headroom := protected - projected
	if headroom < metrics.MinimumProjectedKVHeadroom {
		metrics.MinimumProjectedKVHeadroom = headroom
	}
}

func updateReservedKV(metrics *Metrics, reserved int64) {
	if reserved > metrics.PeakReservedKVTokens {
		metrics.PeakReservedKVTokens = reserved
	}
}

func completionDuration(tokens int64, tps float64) time.Duration {
	if tokens <= 0 {
		return time.Millisecond
	}
	if tps <= 0 || math.IsNaN(tps) || math.IsInf(tps, 0) {
		return 60 * time.Second
	}
	seconds := float64(tokens) / tps
	return time.Duration(seconds * float64(time.Second))
}

func roundedMaterializedContext(active *activeRequest, at time.Duration) int64 {
	if active == nil {
		return 0
	}
	generated := int64(0)
	if active.terminalCause == runtimepredictive.TerminalCompleted && active.terminalAt > 0 && at >= active.terminalAt {
		generated = active.spec.ActualOutput
	}
	decodeStartedAt := active.admittedAt + active.ttft
	if generated == 0 && at > decodeStartedAt && active.tps > 0 && !math.IsNaN(active.tps) && !math.IsInf(active.tps, 0) {
		generated = int64(math.Floor((at - decodeStartedAt).Seconds() * active.tps))
		if generated > active.spec.ActualOutput {
			generated = active.spec.ActualOutput
		}
	}
	return roundedTokens(addInt64(active.spec.InputTokens, generated))
}

func roundedTokens(tokens int64) int64 {
	if tokens <= 0 {
		return 0
	}
	return ((tokens + simulationBlock - 1) / simulationBlock) * simulationBlock
}

func tokenizerP95(tokens int64) time.Duration {
	switch {
	case tokens <= 49:
		return 52_539 * time.Nanosecond
	case tokens <= 3_074:
		return 9 * time.Millisecond
	case tokens <= 24_578:
		return 132_639 * time.Microsecond
	case tokens <= 65_538:
		return 650 * time.Millisecond
	default:
		return 1_516 * time.Millisecond
	}
}

type currentThresholdController struct {
	limit         int
	active        map[string]struct{}
	lastSample    time.Time
	maxAge        time.Duration
	observedKV    int64
	waiting       int
	cooldownUntil time.Time
	epochHealthy  bool
}

func newCurrentThresholdController(scenario scenarioSpec) *currentThresholdController {
	limit := scenario.CurrentLimit
	if limit <= 0 {
		limit = 2
	}
	return &currentThresholdController{limit: limit, active: make(map[string]struct{}), maxAge: 750 * time.Millisecond, epochHealthy: true}
}

func (c *currentThresholdController) Admit(now time.Time, request requestSpec) (bool, string) {
	if c.lastSample.IsZero() || now.Sub(c.lastSample) < 0 || now.Sub(c.lastSample) > c.maxAge || !c.epochHealthy {
		return false, "stale_or_epoch_unknown"
	}
	if now.Before(c.cooldownUntil) || c.waiting > 0 || c.observedKV >= int64(float64(simulationCapacity)*0.80) {
		return false, "feedback_pressure"
	}
	if len(c.active) >= c.limit {
		return false, "count_limit"
	}
	c.active[request.ID] = struct{}{}
	return true, "fit"
}

func (c *currentThresholdController) MarkSemantic(string) {}

func (c *currentThresholdController) MarkForwarded(string) {}

func (c *currentThresholdController) Terminate(_ time.Time, id string, _ runtimepredictive.TerminalCause, _ simulatedRequestOutcome) {
	delete(c.active, id)
}

func (c *currentThresholdController) Observe(now time.Time, observed observedState) error {
	c.lastSample = now
	c.observedKV = observed.kvTokens
	c.waiting = observed.waiting
	if observed.preemption {
		c.cooldownUntil = now.Add(time.Second)
	}
	if observed.counterReset {
		c.epochHealthy = false
	}
	if observed.epochRestored {
		c.epochHealthy = true
	}
	return nil
}

func (c *currentThresholdController) Reservations() int { return 0 }
func (c *currentThresholdController) ReservedPhysicalKVUpper() int64 {
	return 0
}
func (c *currentThresholdController) UsesExactTokenizer() bool { return false }

type v090KVController struct {
	manager    *kvshadow.Manager
	snapshot   kvadmission.BackendSnapshot
	generation uint64
}

func newV090KVController() *v090KVController {
	policy := kvadmission.DefaultPolicy()
	policy.VLLM.TargetRatio = 0.85
	policy.VLLM.HardRatio = 0.90
	policy.VLLM.EmergencyRatio = 0.95
	policy.DecodeDriftTokens = 0
	policy.MaxMetricsAge = 750 * time.Millisecond
	policy.PreemptionCooldown = time.Second
	return &v090KVController{manager: kvshadow.New(policy)}
}

func (c *v090KVController) Admit(now time.Time, request requestSpec) (bool, string) {
	estimate := request.EstimatedInputHigh
	if estimate <= 0 {
		estimate = heuristicInputHigh(request.InputTokens)
	}
	cost := kvadmission.Cost{
		Supported:           !request.Unsupported,
		EstimatedInputLow:   estimate / 2,
		EstimatedInputHigh:  estimate,
		BoundedDecodeTokens: request.OutputUpper,
	}
	decision, reserved := c.manager.DecideAndReserve(now, request.ID, cost, []kvadmission.BackendSnapshot{c.snapshot})
	return decision.Reason == kvadmission.ReasonFit && reserved, string(decision.Reason)
}

func (c *v090KVController) MarkSemantic(string) {}

func (c *v090KVController) MarkForwarded(string) {}

func (c *v090KVController) Terminate(_ time.Time, id string, _ runtimepredictive.TerminalCause, _ simulatedRequestOutcome) {
	c.manager.Release(id)
}

func (c *v090KVController) Observe(now time.Time, observed observedState) error {
	if observed.counterReset {
		c.generation = 0
	} else {
		c.generation++
	}
	c.snapshot = kvadmission.BackendSnapshot{
		Name:                 "simulated-vllm",
		Kind:                 kvadmission.BackendVLLM,
		CapacityTokens:       simulationCapacity,
		UsedTokens:           observed.kvTokens,
		Usage:                float64(observed.kvTokens) / float64(simulationCapacity),
		Updated:              now,
		GenerationTokens:     c.generation,
		Waiting:              observed.waiting,
		PreemptionDelta:      boolUint64(observed.preemption),
		PreemptionDeltaValid: true,
		TokenMetricsValid:    true,
	}
	c.manager.Observe(now, []kvadmission.BackendSnapshot{c.snapshot})
	return nil
}

func (c *v090KVController) Reservations() int { return c.manager.Snapshot().Reservations }
func (c *v090KVController) ReservedPhysicalKVUpper() int64 {
	return 0
}
func (c *v090KVController) UsesExactTokenizer() bool { return false }

type simulatedSizeReservation struct {
	estimate runtimepredictive.InputSizeEstimate
	actual   int64
}

type predictiveSimulationController struct {
	coordinator      *runtimepredictive.CountCoordinator
	identity         runtimepredictive.ModelIdentity
	sizeCalibrator   *runtimepredictive.InputSizeCalibrator
	sizeReservations map[string]simulatedSizeReservation
	learnsQoS        bool
	usesExactInput   bool
	lastSample       time.Time
	maxAge           time.Duration
	cooldownUntil    time.Time
	epochHealthy     bool
	lastAdmission    runtimepredictive.CountAdmissionResult
}

func newPredictiveSimulationController(policy PolicyName, profile serviceProfile) (*predictiveSimulationController, error) {
	identity := runtimepredictive.ModelIdentity{
		ProfileID:        string(policy),
		BackendEpoch:     simulationEpoch,
		PredictorVersion: "goodput-approximate-v1",
	}
	var scheduler runtimepredictive.Scheduler
	var sizeCalibrator *runtimepredictive.InputSizeCalibrator
	constraints := domainpredictive.Constraints{
		PhysicalKVHard:    profile.protected,
		ActiveKVHard:      profile.protected,
		MinimumConfidence: 0.99,
	}
	if policy == PolicyPredictiveQoS {
		learned, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
			Identity:                      identity,
			BaseCompletionTPS:             profile.userTPSTarget,
			PrefillTPSPenaltyPerKToken:    0,
			BaseTTFT:                      profile.ttftSLO / 4,
			TTFTPerUncachedPrefillToken:   (profile.ttftSLO - profile.ttftSLO/4) / time.Duration(profile.protected),
			BaseTPOT:                      time.Duration(float64(time.Second) / profile.userTPSTarget / 2),
			TPOTPerExistingDecodeSequence: time.Duration(float64(time.Second) / profile.userTPSTarget / 2),
			Confidence:                    0.99,
		}, runtimepredictive.ResidualCalibratorConfig{
			Identity:                 identity,
			MinimumSamples:           4,
			MaximumSamplesPerCell:    16,
			MaximumCells:             256,
			MaxAge:                   time.Minute,
			LowerQuantile:            0.10,
			UpperQuantile:            0.90,
			MinimumTPSMultiplier:     0.10,
			MaximumTPSMultiplier:     8,
			MinimumLatencyMultiplier: 0.50,
			MaximumLatencyMultiplier: 4,
			CalibratedConfidence:     0.99,
			DecodeSequenceBucket:     1,
			ContextTokenBucket:       1_024,
			PrefillTokenBucket:       1_024,
			KVTokenBucket:            1_024,
		})
		if err != nil {
			return nil, err
		}
		scheduler = learned
		constraints.UserTPSTarget = profile.userTPSTarget
		constraints.TTFTSLO = profile.ttftSLO
		constraints.TPOTSLO = time.Duration(float64(time.Second) / profile.userTPSTarget)
		sizeCalibrator, err = runtimepredictive.NewInputSizeCalibrator(runtimepredictive.InputSizeCalibratorConfig{
			EstimatorVersion:       "goodput-json-cost-v1",
			MinimumSamples:         4,
			MaximumSamplesPerClass: 16,
			MaxAge:                 time.Minute,
			UpperQuantile:          0.95,
			SafetyMargin:           1.10,
			MinimumMultiplier:      0.25,
			MaximumMultiplier:      8,
			ColdConfidence:         0.99,
			LearnedConfidence:      0.99,
		})
		if err != nil {
			return nil, err
		}
	} else {
		scheduler = constantSimulationScheduler{identity: identity}
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   simulationManifest,
			BackendEpoch: simulationEpoch,
			Scheduler:    identity,
			BlockSize:    int(simulationBlock),
		},
		ModelMaximumLength: 262_144,
		Constraints:        constraints,
		Scheduler:          scheduler,
	})
	if err != nil {
		return nil, err
	}
	return &predictiveSimulationController{
		coordinator: coordinator, identity: identity, sizeCalibrator: sizeCalibrator,
		sizeReservations: make(map[string]simulatedSizeReservation),
		learnsQoS:        policy == PolicyPredictiveQoS, usesExactInput: policy == PolicyExactKVOnly,
		maxAge: 750 * time.Millisecond, epochHealthy: true,
	}, nil
}

func (c *predictiveSimulationController) Admit(now time.Time, request requestSpec) (bool, string) {
	if c == nil || c.lastSample.IsZero() || now.Sub(c.lastSample) < 0 || now.Sub(c.lastSample) > c.maxAge || !c.epochHealthy || now.Before(c.cooldownUntil) {
		return false, "predictive_unknown"
	}
	if c.usesExactInput && (request.ProfileMismatch || request.Unsupported) {
		return false, "predictive_unknown"
	}
	inputUpper := request.InputTokens
	rawInputHigh := request.InputTokens
	confidence := 0.99
	admissionLatency := tokenizerP95(request.InputTokens)
	var size runtimepredictive.InputSizeEstimate
	if c.sizeCalibrator != nil {
		if request.EstimatedInputHigh <= 0 {
			return false, "request_size_unknown"
		}
		rawLow := request.EstimatedInputHigh / 3
		if rawLow < 1 {
			rawLow = 1
		}
		size = c.sizeCalibrator.Estimate(now, runtimepredictive.RequestClassChat, rawLow, request.EstimatedInputHigh)
		if !size.Known {
			return false, "request_size_unknown"
		}
		inputUpper = size.InputTokensUpper
		rawInputHigh = size.RawInputTokensHigh
		confidence = size.Confidence
		admissionLatency = approximateEstimatorP95(request.EstimatedInputHigh)
	}
	result := c.coordinator.DecideUpperBoundAndReserve(now, runtimepredictive.UpperBoundAdmissionProposal{
		RequestID:                    request.ID,
		InputTokensUpper:             inputUpper,
		RawInputTokensHigh:           rawInputHigh,
		DecodeHorizonUpper:           request.OutputUpper,
		AccruedLocalAdmissionLatency: admissionLatency,
		Confidence:                   confidence,
	})
	c.lastAdmission = result
	if result.Reserved && c.sizeCalibrator != nil {
		c.sizeReservations[request.ID] = simulatedSizeReservation{estimate: size, actual: request.InputTokens}
	}
	return result.Reserved, string(result.Decision.Reason)
}

func (c *predictiveSimulationController) MarkSemantic(id string) {
	c.coordinator.MarkPrefillComplete(id)
}

func (c *predictiveSimulationController) MarkForwarded(id string) {
	c.coordinator.MarkForwarded(id)
}

func (c *predictiveSimulationController) Terminate(now time.Time, id string, cause runtimepredictive.TerminalCause, observed simulatedRequestOutcome) {
	var outcome *runtimepredictive.SchedulerOutcome
	if c.learnsQoS && cause != runtimepredictive.TerminalLocalQoSReject {
		candidate := &runtimepredictive.SchedulerOutcome{
			Identity: c.identity, ObservedAt: now, Attributed: true,
		}
		if cause == runtimepredictive.TerminalCompleted {
			if observed.ttft > 0 {
				candidate.TTFT = observed.ttft
				candidate.TTFTValid = true
			}
			if observed.completionTokens > 1 && observed.userTPS > 0 && observed.tpot > 0 {
				candidate.UserTPS = observed.userTPS
				candidate.UserTPSValid = true
				candidate.TPOT = observed.tpot
				candidate.TPOTValid = true
			}
			if !candidate.TTFTValid && !candidate.UserTPSValid && !candidate.TPOTValid {
				candidate.Censored = true
			}
		} else {
			candidate.Censored = true
		}
		outcome = candidate
	}
	if outcome != nil {
		c.coordinator.TerminateWithOutcome(id, cause, outcome)
	} else {
		c.coordinator.Terminate(id, cause)
	}
	size, ok := c.sizeReservations[id]
	delete(c.sizeReservations, id)
	if ok && cause == runtimepredictive.TerminalCompleted {
		_ = c.sizeCalibrator.Observe(runtimepredictive.InputSizeOutcome{
			EstimatorVersion: size.estimate.EstimatorVersion,
			Class:            size.estimate.Class, RawInputTokensHigh: size.estimate.RawInputTokensHigh,
			ActualPromptTokens: size.actual, ObservedAt: now, Attributed: true,
		})
	}
}

func (c *predictiveSimulationController) Observe(now time.Time, observed observedState) error {
	started := c.coordinator.StartSampleWindow()
	finished := c.coordinator.EventSequence()
	if err := c.coordinator.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:       observed.kvTokens,
			ActiveKVUpper:         observed.kvTokens,
			DecodeSequences:       observed.decodeSequences,
			ActiveContextTokens:   observed.kvTokens,
			UncachedPrefillTokens: 0,
		},
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		return err
	}
	c.lastSample = now
	if observed.preemption {
		c.cooldownUntil = now.Add(time.Second)
	}
	if observed.counterReset {
		c.epochHealthy = false
	}
	if observed.epochRestored {
		c.epochHealthy = true
	}
	return nil
}

func (c *predictiveSimulationController) Reservations() int {
	return c.coordinator.Snapshot().Manager.Reservations
}

func (c *predictiveSimulationController) ReservedPhysicalKVUpper() int64 {
	return c.coordinator.Snapshot().Manager.Virtual.Upper.PhysicalKVUpper
}

func (c *predictiveSimulationController) UsesExactTokenizer() bool { return c.usesExactInput }

type constantSimulationScheduler struct {
	identity runtimepredictive.ModelIdentity
}

func (s constantSimulationScheduler) Identity() runtimepredictive.ModelIdentity { return s.identity }

func (s constantSimulationScheduler) Predict(now time.Time, _ domainpredictive.VirtualState, _ domainpredictive.RequestCost) runtimepredictive.SchedulerPrediction {
	estimate := domainpredictive.SchedulerEstimate{
		ExistingUserTPSLower:         1_000_000,
		ExistingUserTPSNotApplicable: true,
		NewUserTPSLower:              1_000_000,
		TTFTUpper:                    time.Nanosecond,
		TPOTUpper:                    time.Nanosecond,
	}
	return runtimepredictive.SchedulerPrediction{Identity: s.identity, PredictedAt: now, Prior: estimate, Estimate: estimate, Source: runtimepredictive.PredictionSourceStatic, Confidence: 0.99}
}

func newSimulationController(policy PolicyName, scenario scenarioSpec, profile serviceProfile) (simulationController, error) {
	switch policy {
	case PolicyCurrentThreshold:
		return newCurrentThresholdController(scenario), nil
	case PolicyV090KVOnly:
		return newV090KVController(), nil
	case PolicyExactKVOnly, PolicyPredictiveQoS:
		return newPredictiveSimulationController(policy, profile)
	default:
		return nil, fmt.Errorf("unknown simulation policy %q", policy)
	}
}

func heuristicInputHigh(tokens int64) int64 {
	if tokens <= 0 {
		return 1
	}
	return tokens + tokens/2
}

func approximateEstimatorP95(estimatedInputHigh int64) time.Duration {
	if estimatedInputHigh <= 0 {
		return 0
	}
	return 25*time.Microsecond + time.Duration(estimatedInputHigh/4_096)*time.Microsecond
}

func boolUint64(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func addInt64(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func multiplyDuration(unit time.Duration, count int64) time.Duration {
	if count <= 0 || unit <= 0 {
		return 0
	}
	if count > math.MaxInt64/int64(unit) {
		return time.Duration(math.MaxInt64)
	}
	return unit * time.Duration(count)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func acceptanceScenarios() []scenarioSpec {
	return []scenarioSpec{
		{Name: "cold_sparse_low_flow_progress", ColdStart: true, Requests: []requestSpec{
			request("cold-sparse-1", 0, 49, 128, 128),
			request("cold-sparse-2", 2*time.Second, 49, 128, 128),
			request("cold-sparse-3", 4*time.Second, 49, 128, 128),
			request("cold-sparse-4", 6*time.Second, 49, 128, 128),
		}},
		{Name: "cold_same_poll_tps_guard_and_drain_recovery", ColdStart: true, Requests: []requestSpec{
			request("cold-guard-first", 0, 49, 128, 128),
			request("cold-guard-concurrent", 0, 49, 128, 128),
			request("cold-guard-after-drain", 2*time.Second, 49, 128, 128),
		}},
		{Name: "same_poll_short_burst_near_kv", InitialKVTokens: 700_000, Requests: burst("near-kv", 6, 0, 49, 39_936, 256)},
		{Name: "mixed_short_64k_128k", LongPromptSuite: true, Requests: mixedPromptBurst()},
		{Name: "long_prompt_short_decode", LongPromptSuite: true, Requests: []requestSpec{
			request("long-short-1", 0, 65_538, 32, 32),
			request("long-short-2", 4*time.Second, 131_074, 32, 32),
		}},
		{Name: "short_prompt_long_decode", Requests: burst("short-long", 6, 0, 49, 1_024, 1_024)},
		{Name: "progressive_kv_arrival_after_scrapes", Requests: []requestSpec{
			request("progressive-long", 0, 49, 16_384, 16_384),
			request("progressive-after-scrapes", 2*time.Second, 49, 16_384, 16_384),
		}},
		{Name: "many_decode_sequences_low_kv", Requests: burst("many-decode", 8, 0, 49, 128, 128)},
		{Name: "completion_before_poll", CurrentLimit: 1, PollInterval: 2 * time.Second, Requests: []requestSpec{
			request("before-poll-1", 0, 49, 16, 16),
			request("before-poll-2", time.Second, 49, 16, 16),
		}},
		{Name: "stale_waiting_after_owned_completion", CurrentLimit: 1, PollInterval: time.Second, StopPollingAt: time.Second, ReportedWaitingUntil: time.Second, Requests: []requestSpec{
			request("stale-wait-1", 1100*time.Millisecond, 49, 16, 16),
			request("stale-wait-2", 2*time.Second, 49, 16, 16),
		}},
		{Name: "cancel_during_prefill_and_decode", Requests: cancellationRequests()},
		{Name: "local_qos_reject_after_reservation", Requests: localRejectRequests()},
		{Name: "timeout_and_upstream_failure", Requests: failureRequests()},
		{Name: "stale_or_reset_backend_epoch", PreemptionAt: time.Second, CounterResetAt: time.Second, EpochRestoredAt: 2 * time.Second, Requests: epochResetRequests()},
		{Name: "tokenizer_template_mismatch", Requests: []requestSpec{mismatchedFailure("template-mismatch", 100*time.Millisecond)}},
		{Name: "unsupported_tools_or_multimodal", Requests: []requestSpec{unsupportedFailure("unsupported-tools", 100*time.Millisecond)}},
		{Name: "near_capacity_atomic_burst", InitialKVTokens: 820_000, Requests: underestimatedNearCapacityBurst()},
		{Name: "repeated_prefixes_charged_cold", Requests: repeatedPrefixBurst()},
		{Name: "high_kv_headroom_low_tps", Requests: burst("low-tps", 6, 0, 49, 128, 128)},
		{Name: "low_kv_excessive_ttft", LongPromptSuite: true, TTFTSLO: time.Second, Requests: excessiveTTFTRequests()},
		{Name: "calibration_error_distribution_shift", BaseCompletionTPS: 80, Requests: burst("shift", 6, 0, 49, 256, 256)},
	}
}

func request(id string, at time.Duration, input, outputUpper, actualOutput int64) requestSpec {
	return requestSpec{
		ID:                 id,
		At:                 at,
		InputTokens:        input,
		OutputUpper:        outputUpper,
		ActualOutput:       actualOutput,
		EstimatedInputHigh: heuristicInputHigh(input),
		TerminalCause:      runtimepredictive.TerminalCompleted,
	}
}

func burst(prefix string, count int, at time.Duration, input, outputUpper, actualOutput int64) []requestSpec {
	result := make([]requestSpec, 0, count)
	for index := 0; index < count; index++ {
		result = append(result, request(fmt.Sprintf("%s-%d", prefix, index+1), at, input, outputUpper, actualOutput))
	}
	return result
}

func mixedPromptBurst() []requestSpec {
	result := []requestSpec{
		request("mixed-64k", 0, 65_538, 512, 512),
		request("mixed-128k", 0, 131_074, 256, 256),
	}
	result = append(result, burst("mixed-short", 4, 0, 49, 512, 512)...)
	return result
}

func cancellationRequests() []requestSpec {
	prefill := request("cancel-prefill", 0, 24_578, 256, 0)
	prefill.TerminalCause = runtimepredictive.TerminalClientCancelled
	prefill.Lifetime = 20 * time.Millisecond
	decode := request("cancel-decode", 100*time.Millisecond, 49, 512, 0)
	decode.TerminalCause = runtimepredictive.TerminalClientDisconnected
	decode.Lifetime = 500 * time.Millisecond
	return []requestSpec{prefill, decode, request("after-cancel-1", time.Second, 49, 128, 128), request("after-cancel-2", time.Second, 49, 128, 128)}
}

func localRejectRequests() []requestSpec {
	local := request("local-reject", 0, 3_074, 256, 0)
	local.LocalReject = true
	return []requestSpec{local, request("after-local-1", time.Millisecond, 49, 128, 128), request("after-local-2", time.Millisecond, 49, 128, 128)}
}

func failureRequests() []requestSpec {
	timeout := request("timeout", 0, 3_074, 256, 0)
	timeout.TerminalCause = runtimepredictive.TerminalTimeout
	timeout.Lifetime = 100 * time.Millisecond
	upstream := request("upstream-failure", 0, 49, 128, 0)
	upstream.TerminalCause = runtimepredictive.TerminalUpstreamFailure
	upstream.Lifetime = 50 * time.Millisecond
	return []requestSpec{timeout, upstream, request("after-failure-1", time.Second, 49, 128, 128), request("after-failure-2", time.Second, 49, 128, 128)}
}

func epochResetRequests() []requestSpec {
	during := request("during-reset", 1500*time.Millisecond, 49, 64, 0)
	during.TerminalCause = runtimepredictive.TerminalUpstreamFailure
	during.Lifetime = 50 * time.Millisecond
	return []requestSpec{request("before-reset", 0, 49, 64, 64), during, request("after-reset", 2100*time.Millisecond, 49, 64, 64)}
}

func mismatchedFailure(id string, at time.Duration) requestSpec {
	result := request(id, at, 49, 64, 0)
	result.ProfileMismatch = true
	result.TerminalCause = runtimepredictive.TerminalUpstreamFailure
	result.Lifetime = 10 * time.Millisecond
	return result
}

func unsupportedFailure(id string, at time.Duration) requestSpec {
	result := request(id, at, 49, 64, 0)
	result.Unsupported = true
	result.TerminalCause = runtimepredictive.TerminalUpstreamFailure
	result.Lifetime = 10 * time.Millisecond
	return result
}

func underestimatedNearCapacityBurst() []requestSpec {
	result := burst("atomic", 4, 0, 30_000, 64, 256)
	for index := range result {
		result[index].EstimatedInputHigh = 10_000
	}
	return result
}

func repeatedPrefixBurst() []requestSpec {
	result := burst("repeat", 4, 0, 3_074, 256, 256)
	for index := range result {
		result[index].Prefix = "identical-prefix"
	}
	return result
}

func excessiveTTFTRequests() []requestSpec {
	return []requestSpec{
		request("ttft-128k", 0, 131_074, 64, 64),
		request("ttft-short-1", time.Second, 49, 256, 256),
		request("ttft-short-2", time.Second, 49, 256, 256),
		request("ttft-short-3", time.Second, 49, 256, 256),
	}
}
