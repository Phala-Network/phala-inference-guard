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
	simulationManifest  = "goodput-gemma4-count-only"
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
	Name                   string
	LongPromptSuite        bool
	Requests               []requestSpec
	CurrentLimit           int
	PollInterval           time.Duration
	StopPollingAt          time.Duration
	InitialKVTokens        int64
	InitialRunning         int
	InitialPrefillTokens   int64
	ReportedWaitingUntil   time.Duration
	PreemptionAt           time.Duration
	CounterResetAt         time.Duration
	EpochRestoredAt        time.Duration
	TTFTSLO                time.Duration
	BaseCompletionTPS      float64
	PrefillPenaltyPerK     float64
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
}

type activeRequest struct {
	spec          requestSpec
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
	MarkSemantic(string)
	Terminate(string, runtimepredictive.TerminalCause)
	Observe(time.Time, observedState) error
	Reservations() int
	UsesExactTokenizer() bool
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
		switch event.kind {
		case eventPoll:
			observed := state.observed(scenario, event.at)
			if err := controller.Observe(now, observed); err != nil {
				return Metrics{}, err
			}
		case eventArrival:
			metrics.Arrivals++
			evaluation := state.evaluate(profile, controller.UsesExactTokenizer(), event.request)
			admitted, _ := controller.Admit(now, event.request)
			if !admitted {
				if evaluation.safe() && !event.request.ProfileMismatch && !event.request.Unsupported {
					metrics.FalseDenies++
				}
				continue
			}
			if event.request.LocalReject {
				controller.Terminate(event.request.ID, runtimepredictive.TerminalLocalQoSReject)
				continue
			}
			metrics.Admitted++
			if !evaluation.safe() {
				metrics.FalseAccepts++
			}
			active := &activeRequest{
				spec:          event.request,
				prefillActive: true,
				ttft:          evaluation.ttft,
				tps:           evaluation.userTPS,
				tpot:          evaluation.tpot,
			}
			state.active[event.request.ID] = active
			markGroundViolations(&metrics, state, active, evaluation)
			updateHeadroom(&metrics, profile.protected, evaluation.projectedKV)

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
			controller.Terminate(event.id, event.cause)
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

func (s *actualState) evaluate(profile serviceProfile, exactTokenizer bool, request requestSpec) groundEvaluation {
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
	projectedKV := s.currentKV()
	projectedKV = addInt64(projectedKV, roundedContext(request))
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

func (s *actualState) currentKV() int64 {
	result := s.initialKV
	for _, active := range s.active {
		result = addInt64(result, roundedContext(active.spec))
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
		kvTokens:        s.currentKV(),
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
	if !evaluation.kvSafe {
		metrics.KVHardViolations++
		metrics.PreemptionProxyEvents++
		for _, active := range state.active {
			active.kvViolation = true
		}
	}
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

func roundedContext(request requestSpec) int64 {
	context := addInt64(request.InputTokens, request.OutputUpper)
	if context <= 0 {
		return 0
	}
	return ((context + simulationBlock - 1) / simulationBlock) * simulationBlock
}

func tokenizerP95(tokens int64) time.Duration {
	switch {
	case tokens <= 49:
		return 52_539 * time.Nanosecond
	case tokens <= 3_074:
		return 8_612 * time.Microsecond
	case tokens <= 24_578:
		return 132_639 * time.Microsecond
	case tokens <= 65_538:
		return 587_303 * time.Microsecond
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

func (c *currentThresholdController) Terminate(id string, _ runtimepredictive.TerminalCause) {
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

func (c *currentThresholdController) Reservations() int       { return 0 }
func (c *currentThresholdController) UsesExactTokenizer() bool { return false }

type v090KVController struct {
	manager      *kvshadow.Manager
	snapshot     kvadmission.BackendSnapshot
	generation   uint64
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

func (c *v090KVController) Terminate(id string, _ runtimepredictive.TerminalCause) {
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

func (c *v090KVController) Reservations() int        { return c.manager.Snapshot().Reservations }
func (c *v090KVController) UsesExactTokenizer() bool { return false }

type exactController struct {
	coordinator    *runtimepredictive.CountCoordinator
	lastSample     time.Time
	maxAge         time.Duration
	cooldownUntil  time.Time
	epochHealthy   bool
	exactTokenizer bool
}

func newExactController(policy PolicyName, profile serviceProfile) (*exactController, error) {
	identity := runtimepredictive.ModelIdentity{
		ProfileID:        string(policy),
		BackendEpoch:     simulationEpoch,
		PredictorVersion: "goodput-static-v1",
	}
	var scheduler runtimepredictive.Scheduler
	constraints := domainpredictive.Constraints{
		PhysicalKVHard:    profile.protected,
		ActiveKVHard:      profile.protected,
		MinimumConfidence: 0.99,
	}
	if policy == PolicyPredictiveQoS {
		learned, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
			Identity:                      identity,
			BaseCompletionTPS:             profile.baseCompletionTPS,
			PrefillTPSPenaltyPerKToken:    profile.prefillPenaltyPerK,
			BaseTTFT:                      profile.baseTTFT,
			TTFTPerUncachedPrefillToken:   profile.predictiveTTFTPerToken,
			BaseTPOT:                      profile.baseTPOT,
			TPOTPerExistingDecodeSequence: profile.tpotPerExisting,
			Confidence:                    0.99,
		}, runtimepredictive.ResidualCalibratorConfig{
			Identity:                 identity,
			MinimumSamples:           4,
			MaximumSamplesPerCell:    16,
			MaxAge:                   time.Minute,
			LowerQuantile:            0.10,
			UpperQuantile:            0.90,
			MinimumTPSMultiplier:     0.50,
			MaximumTPSMultiplier:     1,
			MinimumLatencyMultiplier: 1,
			MaximumLatencyMultiplier: 2,
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
		constraints.TPOTSLO = profile.tpotSLO
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
	return &exactController{coordinator: coordinator, maxAge: 750 * time.Millisecond, epochHealthy: true, exactTokenizer: true}, nil
}

func (c *exactController) Admit(now time.Time, request requestSpec) (bool, string) {
	if request.ProfileMismatch || request.Unsupported || c.lastSample.IsZero() || now.Sub(c.lastSample) < 0 || now.Sub(c.lastSample) > c.maxAge || !c.epochHealthy || now.Before(c.cooldownUntil) {
		return false, "predictive_unknown"
	}
	result := c.coordinator.DecideAndReserve(now, runtimepredictive.CountAdmissionProposal{
		RequestID: request.ID,
		Analysis: runtimepredictive.TokenCountAnalysis{
			ManifestID:       simulationManifest,
			BackendEpoch:     simulationEpoch,
			ExactInputTokens: request.InputTokens,
		},
		DecodeHorizonUpper: request.OutputUpper,
		Confidence:         0.99,
	})
	return result.Reserved, string(result.Decision.Reason)
}

func (c *exactController) MarkSemantic(id string) {
	c.coordinator.MarkPrefillComplete(id)
}

func (c *exactController) Terminate(id string, cause runtimepredictive.TerminalCause) {
	c.coordinator.Terminate(id, cause)
}

func (c *exactController) Observe(now time.Time, observed observedState) error {
	started, _ := c.coordinator.StartSampleWindow()
	finished := c.coordinator.EventSequence()
	if err := c.coordinator.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:       observed.kvTokens,
			ActiveKVUpper:         observed.kvTokens,
			DecodeSequences:       observed.decodeSequences,
			ActiveContextTokens:   observed.kvTokens,
			UncachedPrefillTokens: observed.prefillTokens,
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

func (c *exactController) Reservations() int {
	return c.coordinator.Snapshot().Manager.Reservations
}

func (c *exactController) UsesExactTokenizer() bool { return c.exactTokenizer }

type constantSimulationScheduler struct {
	identity runtimepredictive.ModelIdentity
}

func (s constantSimulationScheduler) Identity() runtimepredictive.ModelIdentity { return s.identity }

func (s constantSimulationScheduler) Predict(now time.Time, _ domainpredictive.VirtualState, _ domainpredictive.RequestCost) runtimepredictive.SchedulerPrediction {
	estimate := domainpredictive.SchedulerEstimate{
		ExistingUserTPSLower:         1_000_000,
		ExistingUserTPSNotApplicable: true,
		AllUserTPSLower:              1_000_000,
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
		return newExactController(policy, profile)
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
		{Name: "same_poll_short_burst_near_kv", InitialKVTokens: 700_000, Requests: burst("near-kv", 6, 0, 49, 39_936, 256)},
		{Name: "mixed_short_64k_128k", LongPromptSuite: true, Requests: mixedPromptBurst()},
		{Name: "long_prompt_short_decode", LongPromptSuite: true, Requests: []requestSpec{
			request("long-short-1", 0, 65_538, 32, 32),
			request("long-short-2", 4*time.Second, 131_074, 32, 32),
		}},
		{Name: "short_prompt_long_decode", Requests: burst("short-long", 6, 0, 49, 1_024, 1_024)},
		{Name: "many_decode_sequences_low_kv", Requests: burst("many-decode", 8, 0, 49, 128, 128)},
		{Name: "completion_before_poll", CurrentLimit: 1, PollInterval: 2 * time.Second, Requests: []requestSpec{
			request("before-poll-1", 0, 49, 16, 16),
			request("before-poll-2", time.Second, 49, 16, 16),
		}},
		{Name: "stale_waiting_after_owned_completion", CurrentLimit: 1, PollInterval: time.Second, ReportedWaitingUntil: time.Second, Requests: []requestSpec{
			request("stale-wait-1", 1100*time.Millisecond, 49, 16, 16),
			request("stale-wait-2", 1500*time.Millisecond, 49, 16, 16),
		}},
		{Name: "cancel_during_prefill_and_decode", Requests: cancellationRequests()},
		{Name: "local_qos_reject_after_reservation", Requests: localRejectRequests()},
		{Name: "timeout_and_upstream_failure", Requests: failureRequests()},
		{Name: "stale_or_reset_backend_epoch", CounterResetAt: time.Second, EpochRestoredAt: 2 * time.Second, Requests: epochResetRequests()},
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
