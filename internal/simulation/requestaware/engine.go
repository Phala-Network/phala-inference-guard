package requestaware

import (
	"fmt"
	"math"
	"sort"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
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
	violatedFloor    bool
	cancelAt         time.Duration
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
	policy                 *runtimepredictive.RequestAwarePolicy
	productionPolicyCalls  int
	active                 map[string]*activeRequest
	order                  []string
	observed               observedState
	lastObservedGeneration float64
	metrics                Metrics
	idleDemandStarted      time.Duration
	idleDemandActive       bool
	externalPreemptionSeen bool
}

func runScenario(spec scenarioSpec, policyName PolicyName, policy *runtimepredictive.RequestAwarePolicy) (Metrics, int, error) {
	if spec.duration <= 0 || spec.initialKVTokens < 0 || spec.initialKVTokens >= simulationCapacityTokens || spec.backgroundRunning < 0 {
		return Metrics{}, 0, fmt.Errorf("invalid scenario")
	}
	runner := &scenarioRunner{
		spec:       spec,
		policyName: policyName,
		policy:     policy,
		active:     make(map[string]*activeRequest),
		observed: observedState{
			usedTokens: spec.initialKVTokens,
			running:    spec.backgroundRunning,
		},
	}
	runner.metrics.PeakKVTokens = spec.initialKVTokens
	arrivals := make(map[time.Duration][]requestSpec)
	for _, request := range spec.requests {
		arrivals[request.at] = append(arrivals[request.at], request)
	}
	for at := time.Duration(0); at <= spec.duration; at += simulationTick {
		if at > 0 {
			runner.advance(at, simulationTick)
		}
		if at%simulationPollInterval == 0 {
			runner.poll(at)
		}
		for _, request := range arrivals[at] {
			runner.arrive(at, request)
		}
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
	return runner.metrics, runner.productionPolicyCalls, nil
}

func (r *scenarioRunner) arrive(at time.Duration, request requestSpec) {
	r.metrics.Arrivals++
	effectiveKV := r.observed.usedTokens + r.unabsorbedReservations()
	hardLimit := int64(math.Floor(float64(simulationCapacityTokens) * simulationHardKVRatio))
	hardFit := effectiveKV >= 0 && request.reservedTokens > 0 && request.reservedTokens <= hardLimit-effectiveKV
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
	if r.policyName == PolicyGlobalBinary {
		if !metricsFresh || preemptionCooldown || !hardFit {
			return false, true
		}
		if float64(effectiveKV)/float64(simulationCapacityTokens) >= simulationSoftKVRatio ||
			r.observed.waiting > 0 ||
			(r.observed.tpsValid && r.observed.tps < simulationTPSTarget) {
			return false, false
		}
		return true, false
	}
	if r.policyName != PolicyRequestAware || r.policy == nil {
		return false, true
	}
	r.productionPolicyCalls++
	decision := r.policy.Evaluate(runtimepredictive.RequestAwareInput{
		MetricsFresh:          metricsFresh,
		IdentityValid:         true,
		CapacityTokens:        simulationCapacityTokens,
		UsedTokens:            r.observed.usedTokens,
		ReservedTokens:        r.unabsorbedReservations(),
		RequestReservedTokens: request.reservedTokens,
		SelectionInputTokens:  request.selectionInput,
		Running:               r.observed.running,
		Waiting:               r.observed.waiting,
		EffectiveSequences:    r.observed.running + r.unabsorbedSequenceCount(),
		AggregateTPSProxy:     r.observed.aggregateTPS,
		MeanActiveTPSProxy:    r.observed.tps,
		TPSValid:              r.observed.tpsValid,
		PreemptionCooldown:    preemptionCooldown,
	})
	switch decision.Action {
	case runtimepredictive.RequestAwareAdmit:
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
			delete(r.active, id)
		}
	}
	prefillAtStart := 0
	for _, id := range r.order {
		if active := r.active[id]; active != nil && active.prefillRemaining > 0 {
			prefillAtStart++
		}
	}
	prefillBudget := simulationPrefillTokensPS * seconds
	for _, id := range r.order {
		active := r.active[id]
		if active == nil || active.prefillRemaining <= 0 || prefillBudget <= 0 {
			continue
		}
		consumed := math.Min(active.prefillRemaining, prefillBudget)
		active.prefillRemaining -= consumed
		prefillBudget -= consumed
	}
	ready := make([]*activeRequest, 0, len(r.active))
	for _, id := range r.order {
		if active := r.active[id]; active != nil && active.prefillRemaining <= simulationFloatTolerance {
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
		aggregateTPS /= 1 + 0.08*float64(prefillAtStart)
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
			delete(r.active, id)
		}
	}
	if r.currentWaiting(at) > 0 {
		r.metrics.WaitingSeconds += seconds
	}
	r.updatePeaks(at)
	r.enforceHardKV()
}

func (r *scenarioRunner) poll(at time.Duration) {
	used := r.trueKVTokens()
	running := r.spec.backgroundRunning + len(r.active)
	waiting := r.currentWaiting(at)
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
	r.observed = observedState{
		usedTokens:   used,
		running:      running,
		waiting:      waiting,
		aggregateTPS: aggregateTPS,
		tps:          tps,
		tpsValid:     tpsValid,
		at:           at,
	}
	r.lastObservedGeneration = r.metrics.CompletionTokens
	for _, active := range r.active {
		active.unabsorbed = false
	}
}

func (r *scenarioRunner) currentWaiting(at time.Duration) int {
	waiting := r.spec.backgroundRunning + len(r.active) - simulationMaximumNoWait
	if waiting < 0 {
		waiting = 0
	}
	if insideAny(r.spec.forcedWaiting, at) {
		waiting++
	}
	return waiting
}

func (r *scenarioRunner) trueKVTokens() int64 {
	tokens := r.spec.initialKVTokens
	for _, active := range r.active {
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
			tokens += active.spec.reservedTokens
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

func (r *scenarioRunner) updatePeaks(at time.Duration) {
	kv := r.trueKVTokens()
	if kv > r.metrics.PeakKVTokens {
		r.metrics.PeakKVTokens = kv
	}
	running := r.spec.backgroundRunning + len(r.active)
	if running > r.metrics.MaximumRunning {
		r.metrics.MaximumRunning = running
	}
}

func (r *scenarioRunner) enforceHardKV() {
	hardLimit := int64(math.Floor(float64(simulationCapacityTokens) * simulationHardKVRatio))
	for r.trueKVTokens() > hardLimit && len(r.active) > 0 {
		ids := make([]string, 0, len(r.active))
		for id := range r.active {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		delete(r.active, ids[len(ids)-1])
		r.metrics.Preemptions++
	}
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
