package requestaware

import (
	"fmt"
	"math"
	"sort"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	simulationManifestID      = "deterministic-admission-simulation"
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
	usedTokens   int64
	running      int
	waiting      int
	aggregateTPS float64
	tps          float64
	tpsValid     bool
	at           time.Duration
}

type scenarioRunner struct {
	spec                   scenarioSpec
	policyName             PolicyName
	profile                runtimepredictive.BackendCapabilityProfile
	controller             *coreadmission.AdmissionController
	controllerHandles      map[string]coreadmission.ReservationHandle
	controllerPolicyCalls  int
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
	cacheQueryTokens       uint64
	cacheHitTokens         uint64
}

func runScenario(
	spec scenarioSpec,
	policyName PolicyName,
	profile runtimepredictive.BackendCapabilityProfile,
) (Metrics, int, error) {
	return runScenarioWithTPSReference(spec, policyName, profile, 0)
}

func runScenarioWithTPSReference(
	spec scenarioSpec,
	policyName PolicyName,
	profile runtimepredictive.BackendCapabilityProfile,
	tpsReference float64,
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
		active:            make(map[string]*activeRequest),
		arrivals:          make(map[time.Duration][]requestSpec),
		requestWorkerPool: make(map[string]int),
		observed: observedState{
			usedTokens: spec.initialKVTokens,
			running:    spec.backgroundRunning,
		},
	}
	if policyName == PolicyV01210 {
		return Metrics{}, 0, fmt.Errorf("historical v0.12.10 must be loaded from its frozen fixture")
	}
	if policyName == PolicyCandidate {
		capability := simulationAdmissionCapability(profile)
		controller, controllerErr := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
			Capability: capability,
			WorkProfile: simulationRequestWorkProfile(),
			TPS:        coreadmission.TPSPolicyConfig{Reference: tpsReference},
		})
		if controllerErr != nil {
			return Metrics{}, 0, fmt.Errorf("construct candidate AdmissionController: %w", controllerErr)
		}
		runner.controller = controller
		runner.controllerHandles = make(map[string]coreadmission.ReservationHandle)
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
	if runner.metrics.DecodeSequenceSeconds > 0 {
		runner.metrics.MeanActiveTPS = runner.metrics.CompletionTokens / runner.metrics.DecodeSequenceSeconds
	}
	runner.terminateAll(spec.duration, coreadmission.TerminalTimeout)
	if runner.controller != nil {
		runner.publishControllerObservation(spec.duration, true)
		snapshot := runner.controller.Snapshot(time.Unix(0, int64(spec.duration)))
		if snapshot.State.LiveReservations != 0 || snapshot.State.ResidualDebts != 0 ||
			snapshot.State.ReservationKVTokens != 0 {
			return Metrics{}, runner.controllerPolicyCalls, fmt.Errorf("candidate Controller leaked reservations: %+v", snapshot.State)
		}
		runner.controller.Close()
	}
	return runner.metrics, runner.controllerPolicyCalls, nil
}

func simulationAdmissionCapability(profile runtimepredictive.BackendCapabilityProfile) coreadmission.Capability {
	return coreadmission.Capability{
		Fingerprint:                  simulationManifestID,
		MaxModelLenTokens:            profile.MaxModelLenTokens,
		KVCapacityTokens:             profile.KVCapacityTokens,
		KVBlockSize:                  profile.KVBlockSize,
		KVHardLimitTokens:            profile.KVHardLimitTokens,
		MaximumInputTokens:           profile.MaximumAdmissibleInputTokens,
		MinimumDecodeHorizonTokens:   runtimepredictive.DefaultCapabilityDecodeHorizonTokens,
		PrefillRegularTokens:         profile.PrefillRegularTokens,
		PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
		PrefillContendedBudgetTokens: profile.PrefillContendedBudgetTokens,
		PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
	}
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
		if request.actualInput <= 0 || request.cacheHitTokens < 0 || request.cacheHitTokens > request.actualInput {
			return fmt.Errorf("scenario request %q has invalid input/cache tokens", request.id)
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
		if hardFit && r.spec.backgroundRunning == 0 && len(r.active) == 0 {
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
		prefillRemaining: float64(request.actualInput - request.cacheHitTokens),
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
	case PolicyCandidate:
		if r.controller == nil {
			return false, true
		}
		r.controllerPolicyCalls++
		result := r.controller.Admit(
			time.Unix(0, int64(at)),
			simulationRequestEstimate(request),
		)
		if !result.Decision.Admitted() {
			return false, candidateHardProtection(result.Decision)
		}
		if !result.Handle.MarkForwarded() {
			result.Handle.Terminate(coreadmission.TerminalError)
			return false, true
		}
		r.controllerHandles[request.id] = result.Handle
		return true, false
	default:
		return false, true
	}
}

func candidateHardProtection(decision coreadmission.DecisionRecord) bool {
	switch decision.Reason {
	case coreadmission.ReasonPrefillContention,
		coreadmission.ReasonPrefillBudget,
		coreadmission.ReasonPrefillExclusive,
		coreadmission.ReasonPrefillQuiescent:
		return false
	default:
		return true
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
			r.removeActive(at, id, coreadmission.TerminalCancel)
			r.processArrivals(at)
		}
	}
	scheduled := r.scheduledRequests()
	waitingAtStart := r.currentWaiting(at - elapsed)
	for _, request := range scheduled {
		if !request.active.materialized {
			r.observeCacheInput(request.active.spec)
			request.active.materialized = true
		}
	}
	prefillAtStart := 0
	prefillExcessAtStart := 0.0
	for _, request := range scheduled {
		if active := request.active; active.prefillRemaining > 0 {
			prefillAtStart++
			if excess := active.prefillRemaining - float64(domainpredictive.DefaultPrefillRegularTokens); excess > 0 {
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
	}
	for _, request := range scheduled {
		active := request.active
		if active.prefillRemaining > simulationFloatTolerance || active.prefillComplete {
			continue
		}
		active.prefillComplete = true
		if r.controller != nil {
			handle, exists := r.controllerHandles[request.id]
			if !exists || !handle.MarkFirstByte() {
				panic("simulation Controller rejected first-byte transition")
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
			0.9*prefillExcessAtStart/float64(domainpredictive.DefaultPrefillRegularTokens)
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
	}
	r.metrics.CompletionTokens += generated
	r.metrics.DecodeSequenceSeconds += float64(decodeSequences) * seconds
	if decodeSequences > 0 && perUserTPS+simulationFloatTolerance >= simulationTPSFloor {
		r.metrics.SLOCompletionTokens += generated
	} else if decodeSequences > 0 {
		r.metrics.TPSFloorViolationSeconds += seconds
	}
	for _, id := range r.order {
		active := r.active[id]
		if active != nil && active.outputRemaining <= simulationFloatTolerance {
			r.metrics.Completed++
			r.removeActive(at, id, coreadmission.TerminalSuccess)
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
	var controllerWindow coreadmission.SampleWindow
	publishController := r.controller != nil && !insideAny(r.spec.staleMetrics, at)
	if publishController {
		var ok bool
		controllerWindow, ok = r.controller.StartSampleWindow()
		if !ok {
			panic("simulation Controller rejected sample window")
		}
	}
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
	nextObserved := observedState{
		usedTokens:   used,
		running:      running,
		waiting:      waiting,
		aggregateTPS: aggregateTPS,
		tps:          tps,
		tpsValid:     tpsValid,
		at:           at,
	}
	if publishController {
		r.publishControllerObservationWindow(controllerWindow, at, used, running, waiting)
	}
	r.observed = nextObserved
	r.lastObservedGeneration = r.metrics.CompletionTokens
	for _, active := range r.active {
		active.unabsorbed = false
	}
}

func (r *scenarioRunner) publishControllerObservation(at time.Duration, force bool) {
	if r.controller == nil || !force && insideAny(r.spec.staleMetrics, at) {
		return
	}
	window, ok := r.controller.StartSampleWindow()
	if !ok {
		panic("simulation Controller rejected sample window")
	}
	running, waiting := r.schedulerCounts(at)
	r.publishControllerObservationWindow(window, at, r.trueKVTokens(), running, waiting)
}

func (r *scenarioRunner) publishControllerObservationWindow(
	window coreadmission.SampleWindow,
	at time.Duration,
	used int64,
	running int,
	waiting int,
) {
	result := r.controller.PublishObservation(window, coreadmission.BackendObservation{
		CapabilityFingerprint: simulationManifestID,
		MaxModelLenTokens:     r.profile.MaxModelLenTokens,
		KVCapacityTokens:      r.profile.KVCapacityTokens,
		KVBlockSize:           r.profile.KVBlockSize,
		ObservedAt:            time.Unix(0, int64(at)),
		MaximumAge:            simulationPollInterval,
		UsedKVTokens:          used,
		Running:               int64(running),
		Waiting:               int64(waiting),
		GenerationTokensTotal: uint64(math.Floor(r.metrics.CompletionTokens)),
		PreemptionsTotal:      uint64(r.metrics.Preemptions),
		CacheQueryTokensTotal: r.cacheQueryTokens,
		CacheHitTokensTotal:   r.cacheHitTokens,
		CacheCountersValid:    r.spec.cacheMetrics,
	})
	if !result.Accepted {
		panic(fmt.Sprintf("simulation Controller observation: %+v", result))
	}
}

func (r *scenarioRunner) observeCacheInput(request requestSpec) {
	if !r.spec.cacheMetrics {
		return
	}
	queryTokens := uint64(request.actualInput)
	hitTokens := uint64(request.cacheHitTokens)
	if r.cacheQueryTokens > math.MaxUint64-queryTokens || r.cacheHitTokens > math.MaxUint64-hitTokens {
		panic("simulation cache counter overflow")
	}
	r.cacheQueryTokens += queryTokens
	r.cacheHitTokens += hitTokens
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
		r.removeActive(at, ids[len(ids)-1], coreadmission.TerminalError)
		r.processArrivals(at)
		r.metrics.Preemptions++
	}
}

func (r *scenarioRunner) removeActive(at time.Duration, id string, cause coreadmission.TerminalCause) {
	if _, exists := r.active[id]; !exists {
		return
	}
	if r.controller != nil {
		handle, exists := r.controllerHandles[id]
		if !exists || !handle.Terminate(cause) {
			panic("simulation Controller rejected terminal event")
		}
		delete(r.controllerHandles, id)
	}
	delete(r.active, id)
	r.releaseWorkerPoolSlot(at, id)
}

func (r *scenarioRunner) terminateAll(at time.Duration, cause coreadmission.TerminalCause) {
	r.ending = true
	for _, id := range r.order {
		r.removeActive(at, id, cause)
	}
}

func simulationRequestEstimate(request requestSpec) domainpredictive.RequestEstimate {
	estimate := domainpredictive.RequestEstimate{
		SelectionInputTokens:                     request.selectionInput,
		MaximumSequenceInputTokens:              request.selectionInput,
		KVReservationInputTokens:                request.safetyInput,
		MaximumSequenceKVReservationInputTokens: request.safetyInput,
		DecodeHorizonTokens:                      request.decodeHorizon,
		BasePromptCount:                          1,
		DecodeSequences:                          1,
	}
	if err := estimate.Validate(); err != nil {
		panic(fmt.Sprintf("simulation request estimate for %q: %v", request.id, err))
	}
	return estimate
}

func simulationRequestWork(request requestSpec) domainpredictive.RequestWork {
	work, err := domainpredictive.BuildRequestWork(
		simulationRequestEstimate(request),
		simulationRequestWorkProfile(),
		simulationBlockSize,
	)
	if err != nil {
		panic(fmt.Sprintf("simulation request work for %q: %v", request.id, err))
	}
	return work
}

func simulationRequestWorkProfile() domainpredictive.RequestWorkProfile {
	return domainpredictive.RequestWorkProfile{
		InputAccounting: domainpredictive.InputAccountingBasePrompts,
	}
}

func simulationReservedTokens(request requestSpec) int64 {
	return simulationRequestWork(request).TotalKVTokens
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
