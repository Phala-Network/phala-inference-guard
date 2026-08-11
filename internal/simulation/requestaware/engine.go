package requestaware

import (
	"fmt"
	"math"
	"sort"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	simulationManifestID      = "deterministic-request-aware-simulation"
	simulationAggregateTPS    = 140.0
	simulationUncontendedTPS  = 30.0
	simulationPrefillTokensPS = 20_000.0
	simulationMaximumNoWait   = 7
	simulationFloatTolerance  = 1e-9
)

type activeRequest struct {
	spec             requestSpec
	prefillRemaining float64
	outputRemaining  float64
	generated        float64
	unabsorbed       bool
	materialized     bool
	prefillComplete  bool
	violatedFloor    bool
	cancelAt         time.Duration
}

type scheduledRequest struct {
	id     string
	active *activeRequest
}

type workerPoolState struct {
	spec workerPoolSpec
	next int
}

type observedState struct {
	usedTokens          int64
	running             int
	waiting             int
	aggregateTPS        float64
	tps                 float64
	tpsValid            bool
	observationSequence uint64
	at                  time.Duration
}

type scenarioRunner struct {
	spec                   scenarioSpec
	policyName             PolicyName
	profile                runtimepredictive.BackendCapabilityProfile
	policy                 *runtimepredictive.RequestAwarePolicy
	manager                *runtimepredictive.Manager
	productionPolicyCalls  int
	active                 map[string]*activeRequest
	order                  []string
	observed               observedState
	lastObservedGeneration float64
	metrics                Metrics
	arrivals               map[time.Duration][]requestSpec
	workerPools            []workerPoolState
	requestWorkerPool      map[string]int
	ending                 bool
	idleDemandStarted      time.Duration
	idleDemandActive       bool
	externalPreemptionSeen bool
}

func runScenario(
	spec scenarioSpec,
	policyName PolicyName,
	profile runtimepredictive.BackendCapabilityProfile,
	policy *runtimepredictive.RequestAwarePolicy,
) (Metrics, int, error) {
	capacity := spec.capacityTokens
	if capacity <= 0 {
		capacity = simulationCapacityTokens
	}
	if spec.duration <= 0 || spec.initialKVTokens < 0 || spec.initialKVTokens >= capacity || spec.backgroundRunning < 0 {
		return Metrics{}, 0, fmt.Errorf("invalid scenario")
	}
	runner := &scenarioRunner{
		spec:              spec,
		policyName:        policyName,
		profile:           profile,
		policy:            policy,
		active:            make(map[string]*activeRequest),
		arrivals:          make(map[time.Duration][]requestSpec),
		requestWorkerPool: make(map[string]int),
		observed: observedState{
			usedTokens: spec.initialKVTokens,
			running:    spec.backgroundRunning,
		},
	}
	if policyName == PolicyV01210 {
		runner.manager = runtimepredictive.NewManager(simulationManifestID, domainpredictive.VirtualState{
			PhysicalKVUpper:     spec.initialKVTokens,
			ActiveKVUpper:       spec.initialKVTokens,
			DecodeSequences:     spec.backgroundRunning,
			ActiveContextTokens: spec.initialKVTokens,
		})
	}
	runner.metrics.PeakKVTokens = spec.initialKVTokens
	if err := runner.initializeArrivals(); err != nil {
		return Metrics{}, 0, err
	}
	for at := time.Duration(0); at <= spec.duration; at += simulationTick {
		if at >= spec.duration {
			runner.ending = true
		}
		if at > 0 {
			runner.advance(at, simulationTick)
		}
		if at%simulationPollInterval == 0 {
			runner.poll(at)
		}
		runner.processArrivals(at)
		if !runner.externalPreemptionSeen && spec.preemptionAt > 0 && at >= spec.preemptionAt {
			runner.metrics.Preemptions++
			runner.externalPreemptionSeen = true
		}
	}
	if runner.idleDemandActive {
		runner.recordIdleDuration(spec.duration)
	}
	durationSeconds := spec.duration.Seconds()
	if durationSeconds > 0 {
		runner.metrics.CompletionTokensPerSecond = runner.metrics.CompletionTokens / durationSeconds
		runner.metrics.SLOCompletionTokensPerSecond = runner.metrics.SLOCompletionTokens / durationSeconds
	}
	runner.terminateAll(spec.duration, runtimepredictive.TerminalExpired)
	if runner.manager != nil && runner.manager.Snapshot().Reservations != 0 {
		return Metrics{}, runner.productionPolicyCalls, fmt.Errorf("candidate manager leaked reservations")
	}
	return runner.metrics, runner.productionPolicyCalls, nil
}

func (r *scenarioRunner) initializeArrivals() error {
	seen := make(map[string]struct{}, len(r.spec.requests))
	add := func(request requestSpec) error {
		if request.id == "" {
			return fmt.Errorf("scenario request ID is empty")
		}
		if _, duplicate := seen[request.id]; duplicate {
			return fmt.Errorf("scenario request ID %q is duplicated", request.id)
		}
		seen[request.id] = struct{}{}
		return nil
	}
	for _, request := range r.spec.requests {
		if request.at < 0 || request.at >= r.spec.duration {
			return fmt.Errorf("scenario request %q arrival is outside the active window", request.id)
		}
		if err := add(request); err != nil {
			return err
		}
		r.arrivals[request.at] = append(r.arrivals[request.at], request)
	}
	for poolIndex, spec := range r.spec.workerPools {
		if spec.at < 0 || spec.at >= r.spec.duration || spec.concurrency <= 0 || len(spec.requests) == 0 {
			return fmt.Errorf("scenario worker pool %d is invalid", poolIndex)
		}
		state := workerPoolState{spec: spec}
		for _, request := range spec.requests {
			if request.at != 0 {
				return fmt.Errorf("scenario worker pool %d request %q has an independent arrival", poolIndex, request.id)
			}
			if err := add(request); err != nil {
				return err
			}
		}
		r.workerPools = append(r.workerPools, state)
		for range spec.concurrency {
			r.releaseWorkerPoolRequest(spec.at, poolIndex)
		}
	}
	return nil
}

func (r *scenarioRunner) releaseWorkerPoolRequest(at time.Duration, poolIndex int) {
	if r.ending || poolIndex < 0 || poolIndex >= len(r.workerPools) {
		return
	}
	pool := &r.workerPools[poolIndex]
	if pool.next >= len(pool.spec.requests) {
		return
	}
	request := pool.spec.requests[pool.next]
	pool.next++
	r.requestWorkerPool[request.id] = poolIndex
	r.arrivals[at] = append(r.arrivals[at], request)
}

func (r *scenarioRunner) releaseWorkerPoolSlot(at time.Duration, requestID string) {
	poolIndex, ok := r.requestWorkerPool[requestID]
	if !ok {
		return
	}
	delete(r.requestWorkerPool, requestID)
	r.releaseWorkerPoolRequest(at, poolIndex)
}

func (r *scenarioRunner) processArrivals(at time.Duration) {
	for {
		queue := r.arrivals[at]
		if len(queue) == 0 {
			delete(r.arrivals, at)
			return
		}
		request := queue[0]
		r.arrivals[at] = queue[1:]
		r.arrive(at, request)
	}
}

func (r *scenarioRunner) arrive(at time.Duration, request requestSpec) {
	r.metrics.Arrivals++
	effectiveKV := r.observed.usedTokens + r.unabsorbedReservations()
	hardLimit := r.profile.KVHardLimitTokens
	reservedTokens := simulationReservedTokens(request)
	hardFit := effectiveKV >= 0 && reservedTokens > 0 && reservedTokens <= hardLimit-effectiveKV
	admit, hardProtect := r.decide(at, request, effectiveKV, hardFit)
	if !admit {
		r.metrics.Rejected++
		if hardProtect {
			r.metrics.HardProtects++
		} else {
			r.metrics.SizeProtects++
		}
		if hardFit && !hardProtect && r.spec.backgroundRunning == 0 && len(r.active) == 0 {
			r.metrics.HardFitIdleRejects++
			if !r.idleDemandActive {
				r.idleDemandStarted = at
				r.idleDemandActive = true
			}
		}
		r.releaseWorkerPoolSlot(at, request.id)
		return
	}
	r.metrics.Admitted++
	if r.idleDemandActive {
		r.recordIdleDuration(at)
		r.idleDemandActive = false
	}
	active := &activeRequest{
		spec:             request,
		prefillRemaining: float64(request.actualInput),
		outputRemaining:  request.actualOutput,
		unabsorbed:       true,
	}
	if request.cancelAfter > 0 {
		active.cancelAt = at + request.cancelAfter
	}
	r.active[request.id] = active
	r.order = append(r.order, request.id)
	r.updatePeaks(at)
}

func (r *scenarioRunner) decide(at time.Duration, request requestSpec, effectiveKV int64, hardFit bool) (admit bool, hardProtect bool) {
	metricsFresh := !insideAny(r.spec.staleMetrics, at)
	preemptionCooldown := insideAny(r.spec.preemptionCooldown, at)
	switch r.policyName {
	case PolicyNoAdmission:
		return true, false
	case PolicyV0122:
		return r.decideV0122(request, effectiveKV, hardFit, metricsFresh, preemptionCooldown)
	}
	if r.policyName != PolicyV01210 || r.policy == nil || r.manager == nil {
		return false, true
	}
	r.productionPolicyCalls++
	result := r.manager.DecideRequestAwareAndReserve(
		time.Unix(0, int64(at)),
		request.id,
		simulationRequestCost(request),
		request.selectionInput,
		r.policy,
		runtimepredictive.RequestAwareInput{
			MetricsFresh:        metricsFresh,
			IdentityValid:       true,
			ObservationSequence: r.observed.observationSequence,
			CapacityTokens:      r.capacityTokens(),
			Running:             r.observed.running,
			Waiting:             r.observed.waiting,
			AggregateTPSProxy:   r.observed.aggregateTPS,
			MeanActiveTPSProxy:  r.observed.tps,
			TPSValid:            r.observed.tpsValid,
			PreemptionObserved:  preemptionCooldown,
		},
	)
	switch result.Decision.Action {
	case runtimepredictive.RequestAwareAdmit:
		if !result.Reserved || !r.manager.MarkForwarded(request.id) {
			if result.Reserved {
				r.manager.Terminate(request.id, runtimepredictive.TerminalLocalQoSReject)
			}
			return false, true
		}
		return true, false
	case runtimepredictive.RequestAwareSizeProtect:
		return false, false
	default:
		return false, true
	}
}

func (r *scenarioRunner) advance(at, elapsed time.Duration) {
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		return
	}
	for _, id := range r.order {
		active := r.active[id]
		if active != nil && active.cancelAt > 0 && at >= active.cancelAt {
			r.removeActive(at, id, runtimepredictive.TerminalClientCancelled)
			r.processArrivals(at)
		}
	}
	scheduled := r.scheduledRequests()
	waitingAtStart := r.currentWaiting(at - elapsed)
	for _, request := range scheduled {
		request.active.materialized = true
	}
	prefillAtStart := 0
	prefillExcessAtStart := 0.0
	for _, request := range scheduled {
		if active := request.active; active.prefillRemaining > 0 {
			prefillAtStart++
			if excess := active.prefillRemaining - float64(runtimepredictive.DefaultRequestAwarePrefillRegularTokens); excess > 0 {
				prefillExcessAtStart += excess
			}
		}
	}
	prefillBudget := simulationPrefillTokensPS * seconds
	for _, request := range scheduled {
		active := request.active
		if active.prefillRemaining <= 0 || prefillBudget <= 0 {
			continue
		}
		consumed := math.Min(active.prefillRemaining, prefillBudget)
		active.prefillRemaining -= consumed
		prefillBudget -= consumed
		if active.prefillRemaining <= simulationFloatTolerance && !active.prefillComplete {
			active.prefillComplete = true
			if r.manager != nil && !r.manager.MarkPrefillComplete(request.id) {
				panic("simulation manager rejected Prefill completion")
			}
		}
	}
	ready := make([]*activeRequest, 0, len(scheduled))
	for _, request := range scheduled {
		if active := request.active; active.prefillRemaining <= simulationFloatTolerance {
			ready = append(ready, active)
		}
	}
	decodeSequences := r.spec.backgroundRunning + len(ready)
	aggregateTPSCap := r.spec.aggregateTPSCap
	if aggregateTPSCap <= 0 {
		aggregateTPSCap = simulationAggregateTPS
	}
	aggregateTPS := math.Min(aggregateTPSCap, simulationUncontendedTPS*float64(decodeSequences))
	if prefillAtStart > 0 {
		aggregateTPS /= 1 + 0.08*float64(prefillAtStart) +
			0.9*prefillExcessAtStart/float64(runtimepredictive.DefaultRequestAwarePrefillRegularTokens)
	}
	perUserTPS := 0.0
	if decodeSequences > 0 {
		perUserTPS = aggregateTPS / float64(decodeSequences)
	}
	generated := perUserTPS * float64(r.spec.backgroundRunning) * seconds
	for _, active := range ready {
		requestTokens := math.Min(active.outputRemaining, perUserTPS*seconds)
		if requestTokens < 0 {
			requestTokens = 0
		}
		active.outputRemaining -= requestTokens
		active.generated += requestTokens
		generated += requestTokens
		if perUserTPS+simulationFloatTolerance < simulationTPSFloor {
			active.violatedFloor = true
		}
	}
	r.metrics.CompletionTokens += generated
	if decodeSequences > 0 && perUserTPS+simulationFloatTolerance >= simulationTPSFloor {
		r.metrics.SLOCompletionTokens += generated
	} else if decodeSequences > 0 {
		r.metrics.TPSFloorViolationSeconds += seconds
	}
	for _, id := range r.order {
		active := r.active[id]
		if active != nil && active.outputRemaining <= simulationFloatTolerance {
			r.metrics.Completed++
			r.removeActive(at, id, runtimepredictive.TerminalCompleted)
			r.processArrivals(at)
		}
	}
	if waitingAtStart > 0 {
		r.metrics.WaitingSeconds += seconds
	}
	r.updatePeaks(at)
	r.enforceHardKV(at)
}

func (r *scenarioRunner) poll(at time.Duration) {
	used := r.trueKVTokens()
	running, waiting := r.schedulerCounts(at)
	aggregateTPS := 0.0
	tps := 0.0
	tpsValid := false
	if at > r.observed.at {
		delta := r.metrics.CompletionTokens - r.lastObservedGeneration
		denominator := r.observed.running
		if running > denominator {
			denominator = running
		}
		if delta > 0 && denominator > 0 {
			aggregateTPS = delta / (at - r.observed.at).Seconds()
			tps = aggregateTPS / float64(denominator)
			tpsValid = tps > 0
		}
	}
	observationSequence := r.observed.observationSequence
	if r.manager != nil {
		if observationSequence == ^uint64(0) {
			panic("simulation observation sequence overflow")
		}
		observationSequence++
	}
	nextObserved := observedState{
		usedTokens:          used,
		running:             running,
		waiting:             waiting,
		aggregateTPS:        aggregateTPS,
		tps:                 tps,
		tpsValid:            tpsValid,
		observationSequence: observationSequence,
		at:                  at,
	}
	if r.manager != nil {
		started := r.manager.StartSampleWindow()
		finished := r.manager.EventSequence()
		decodeSequences := running + waiting
		if err := r.manager.ReconcileSample(runtimepredictive.SampleWindow{
			Observed: domainpredictive.VirtualState{
				PhysicalKVUpper:         used,
				ActiveKVUpper:           used,
				DecodeSequences:         decodeSequences,
				PendingPrefillSequences: waiting,
				ActiveContextTokens:     used,
			},
			StartedSequence:     started,
			FinishedSequence:    finished,
			ObservationSequence: observationSequence,
		}); err != nil {
			panic(fmt.Sprintf("simulation reconcile: %v", err))
		}
	}
	r.observed = nextObserved
	r.lastObservedGeneration = r.metrics.CompletionTokens
	for _, active := range r.active {
		active.unabsorbed = false
	}
}

func (r *scenarioRunner) currentWaiting(at time.Duration) int {
	_, waiting := r.schedulerCounts(at)
	return waiting
}

func (r *scenarioRunner) schedulerCounts(at time.Duration) (running, waiting int) {
	scheduled := len(r.scheduledRequests())
	running = r.spec.backgroundRunning + scheduled
	waiting = len(r.active) - scheduled
	if insideAny(r.spec.forcedWaiting, at) {
		waiting++
	}
	return running, waiting
}

func (r *scenarioRunner) scheduledRequests() []scheduledRequest {
	available := r.maximumNoWait() - r.spec.backgroundRunning
	if available <= 0 || len(r.active) == 0 {
		return nil
	}
	if available > len(r.active) {
		available = len(r.active)
	}
	scheduled := make([]scheduledRequest, 0, available)
	for _, id := range r.order {
		active := r.active[id]
		if active == nil {
			continue
		}
		scheduled = append(scheduled, scheduledRequest{id: id, active: active})
		if len(scheduled) == available {
			break
		}
	}
	return scheduled
}

func (r *scenarioRunner) maximumNoWait() int {
	if r.spec.maximumNoWait > 0 {
		return r.spec.maximumNoWait
	}
	return simulationMaximumNoWait
}

func (r *scenarioRunner) trueKVTokens() int64 {
	tokens := r.spec.initialKVTokens
	for _, active := range r.active {
		if !active.materialized {
			continue
		}
		requestTokens := active.spec.actualInput + int64(math.Ceil(active.generated))
		if requestTokens > 0 && tokens <= math.MaxInt64-requestTokens {
			tokens += requestTokens
		} else {
			return math.MaxInt64
		}
	}
	return tokens
}

func (r *scenarioRunner) unabsorbedReservations() int64 {
	var tokens int64
	for _, active := range r.active {
		if active.unabsorbed {
			tokens += simulationReservedTokens(active.spec)
		}
	}
	return tokens
}

func (r *scenarioRunner) unabsorbedSequenceCount() int {
	count := 0
	for _, active := range r.active {
		if active.unabsorbed {
			count++
		}
	}
	return count
}

func (r *scenarioRunner) pendingPrefillSummary() (sequences int, tokens int64, long, quiescent int) {
	for _, active := range r.active {
		if active == nil || active.prefillRemaining <= simulationFloatTolerance {
			continue
		}
		estimated := active.spec.estimatedPrefill
		if estimated <= 0 {
			estimated = active.spec.selectionInput
		}
		sequences++
		if tokens <= math.MaxInt64-estimated {
			tokens += estimated
		} else {
			tokens = math.MaxInt64
		}
		if estimated >= r.profile.PrefillQuiescentTokens {
			quiescent++
			long++
		} else if estimated >= r.profile.PrefillExclusiveTokens {
			long++
		}
	}
	return sequences, tokens, long, quiescent
}

func (r *scenarioRunner) capacityTokens() int64 {
	if r.spec.capacityTokens > 0 {
		return r.spec.capacityTokens
	}
	return simulationCapacityTokens
}

func (r *scenarioRunner) updatePeaks(at time.Duration) {
	kv := r.trueKVTokens()
	if kv > r.metrics.PeakKVTokens {
		r.metrics.PeakKVTokens = kv
	}
	running, _ := r.schedulerCounts(at)
	if running > r.metrics.MaximumRunning {
		r.metrics.MaximumRunning = running
	}
}

func (r *scenarioRunner) enforceHardKV(at time.Duration) {
	hardLimit := r.profile.KVHardLimitTokens
	for r.trueKVTokens() > hardLimit && len(r.active) > 0 {
		ids := make([]string, 0, len(r.active))
		for id := range r.active {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		r.removeActive(at, ids[len(ids)-1], runtimepredictive.TerminalUpstreamFailure)
		r.processArrivals(at)
		r.metrics.Preemptions++
	}
}

func (r *scenarioRunner) removeActive(at time.Duration, id string, cause runtimepredictive.TerminalCause) {
	if _, exists := r.active[id]; !exists {
		return
	}
	if r.manager != nil && !r.manager.Terminate(id, cause) {
		panic("simulation manager rejected terminal event")
	}
	delete(r.active, id)
	r.releaseWorkerPoolSlot(at, id)
}

func (r *scenarioRunner) terminateAll(at time.Duration, cause runtimepredictive.TerminalCause) {
	r.ending = true
	for _, id := range r.order {
		r.removeActive(at, id, cause)
	}
}

func simulationRequestCost(request requestSpec) domainpredictive.RequestCost {
	cost, err := domainpredictive.BuildRequestCost(domainpredictive.RequestCostInput{
		ManifestID:             simulationManifestID,
		BlockSize:              simulationBlockSize,
		SelectionPrefillTokens: request.selectionInput,
		SafetyInputTokens:      request.safetyInput,
		DecodeHorizonTokens:    request.decodeHorizon,
		Confidence:             1,
	})
	if err != nil {
		panic(fmt.Sprintf("simulation request cost for %q: %v", request.id, err))
	}
	return cost
}

func simulationReservedTokens(request requestSpec) int64 {
	cost := simulationRequestCost(request)
	if cost.KV.PhysicalKVUpper > cost.KV.ActiveKVUpper {
		return cost.KV.PhysicalKVUpper
	}
	return cost.KV.ActiveKVUpper
}

func (r *scenarioRunner) recordIdleDuration(at time.Duration) {
	duration := at - r.idleDemandStarted
	if duration.Seconds() > r.metrics.MaximumIdleWithDemandSeconds {
		r.metrics.MaximumIdleWithDemandSeconds = duration.Seconds()
	}
}

func insideAny(windows []timeWindow, at time.Duration) bool {
	for _, window := range windows {
		if window.contains(at) {
			return true
		}
	}
	return false
}
