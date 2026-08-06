package requestaware

import (
	"fmt"
	"math/rand"
	"time"
)

const (
	simulationCapacityTokens = int64(100_000)
	simulationBlockSize      = int64(64)
	simulationSoftKVRatio    = 0.70
	simulationHardKVRatio    = 0.90
	simulationTPSTarget      = 25.0
	simulationTPSFloor       = 20.0
	simulationTick           = 100 * time.Millisecond
	simulationPollInterval   = 500 * time.Millisecond
)

type timeWindow struct {
	start time.Duration
	end   time.Duration
}

func (w timeWindow) contains(at time.Duration) bool {
	return at >= w.start && at < w.end
}

type requestSpec struct {
	id             string
	at             time.Duration
	selectionInput int64
	reservedTokens int64
	actualInput    int64
	actualOutput   float64
	cancelAfter    time.Duration
}

type scenarioSpec struct {
	name               string
	category           string
	duration           time.Duration
	initialKVTokens    int64
	backgroundRunning  int
	requests           []requestSpec
	forcedWaiting      []timeWindow
	staleMetrics       []timeWindow
	preemptionCooldown []timeWindow
	preemptionAt       time.Duration
	aggregateTPSCap    float64
}

type requestShape uint8

const (
	shapeTiny requestShape = iota
	shapeSmall
	shapeLarge
	shapeSmallLargeOutput
	shapeLargeSmallOutput
	shapeCancel
	shapeShortCompletion
	shapeLongStreaming
)

func simulationScenarios(seed int64) []scenarioSpec {
	random := rand.New(rand.NewSource(seed))
	return []scenarioSpec{
		newUniformScenario("short-only", "uniform", 20_000, 2, shapeSmall, 18, 300*time.Millisecond, 500*time.Millisecond),
		newUniformScenario("large-only", "uniform", 20_000, 1, shapeLarge, 6, 300*time.Millisecond, 1500*time.Millisecond),
		newMixedScenario(random, "mix-80-20", "mixed", 16, 4),
		newMixedScenario(random, "mix-50-50", "mixed", 10, 10),
		newMixedScenario(random, "mix-20-80", "mixed", 4, 16),
		newOrderedScenario("small-then-large", true),
		newOrderedScenario("large-then-small", false),
		newUniformScenario("pre-poll-burst", "burst", 20_000, 2, shapeSmall, 5, 100*time.Millisecond, 0),
		newUniformScenario("low-flow-first-large", "low-flow", 10_000, 0, shapeLarge, 2, 100*time.Millisecond, 6*time.Second),
		withWaiting(newUniformScenario("transient-waiting", "waiting", 20_000, 2, shapeSmall, 8, 1200*time.Millisecond, 500*time.Millisecond), timeWindow{start: time.Second, end: 2 * time.Second}),
		withWaiting(newUniformScenario("sustained-waiting", "waiting", 20_000, 2, shapeSmall, 10, 1200*time.Millisecond, 500*time.Millisecond), timeWindow{start: time.Second, end: 5 * time.Second}),
		withAggregateTPS(newUniformScenario("tps-target", "tps", 20_000, 6, shapeSmall, 8, 1200*time.Millisecond, 500*time.Millisecond), 150),
		withAggregateTPS(newUniformScenario("tps-floor", "tps", 20_000, 7, shapeTiny, 5, 1200*time.Millisecond, 500*time.Millisecond), 140),
		newUniformScenario("kv-low", "kv", 20_000, 2, shapeSmall, 8, 500*time.Millisecond, 500*time.Millisecond),
		newUniformScenario("kv-mid", "kv", 75_000, 2, shapeSmall, 8, 500*time.Millisecond, 500*time.Millisecond),
		newUniformScenario("kv-high", "kv", 84_000, 2, shapeSmall, 6, 500*time.Millisecond, 500*time.Millisecond),
		withPreemption(newUniformScenario("preemption", "guard", 20_000, 2, shapeSmall, 6, 800*time.Millisecond, 700*time.Millisecond)),
		withStaleRecovery(newUniformScenario("stale-recovery", "guard", 20_000, 1, shapeSmall, 7, 800*time.Millisecond, 700*time.Millisecond)),
		newUniformScenario("small-large-output", "output-horizon", 70_000, 2, shapeSmallLargeOutput, 4, 500*time.Millisecond, 2*time.Second),
		newUniformScenario("large-small-output", "output-horizon", 65_000, 2, shapeLargeSmallOutput, 4, 500*time.Millisecond, 2*time.Second),
		newUniformScenario("cancel", "terminal", 20_000, 1, shapeCancel, 6, 500*time.Millisecond, time.Second),
		newUniformScenario("short-completion", "terminal", 20_000, 1, shapeShortCompletion, 10, 500*time.Millisecond, 700*time.Millisecond),
		newUniformScenario("long-streaming", "terminal", 20_000, 1, shapeLongStreaming, 4, 500*time.Millisecond, 2*time.Second),
	}
}

func newUniformScenario(name, category string, initialKV int64, background int, shape requestShape, count int, start, interval time.Duration) scenarioSpec {
	scenario := scenarioSpec{
		name:              name,
		category:          category,
		duration:          12 * time.Second,
		initialKVTokens:   initialKV,
		backgroundRunning: background,
	}
	for index := 0; index < count; index++ {
		at := start
		if interval > 0 {
			at += time.Duration(index) * interval
		}
		scenario.requests = append(scenario.requests, shapedRequest(name, index, at, shape))
	}
	return scenario
}

func newMixedScenario(random *rand.Rand, name, category string, small, large int) scenarioSpec {
	shapes := make([]requestShape, 0, small+large)
	for index := 0; index < small; index++ {
		shapes = append(shapes, shapeSmall)
	}
	for index := 0; index < large; index++ {
		shapes = append(shapes, shapeLarge)
	}
	random.Shuffle(len(shapes), func(left, right int) {
		shapes[left], shapes[right] = shapes[right], shapes[left]
	})
	scenario := scenarioSpec{name: name, category: category, duration: 12 * time.Second, initialKVTokens: 75_000, backgroundRunning: 2}
	for index, shape := range shapes {
		scenario.requests = append(scenario.requests, shapedRequest(name, index, 300*time.Millisecond+time.Duration(index)*400*time.Millisecond, shape))
	}
	return scenario
}

func newOrderedScenario(name string, smallFirst bool) scenarioSpec {
	scenario := scenarioSpec{name: name, category: "order", duration: 12 * time.Second, initialKVTokens: 75_000, backgroundRunning: 2}
	for index := 0; index < 12; index++ {
		shape := shapeLarge
		if (index < 6) == smallFirst {
			shape = shapeSmall
		}
		scenario.requests = append(scenario.requests, shapedRequest(name, index, 300*time.Millisecond+time.Duration(index)*500*time.Millisecond, shape))
	}
	return scenario
}

func withWaiting(scenario scenarioSpec, window timeWindow) scenarioSpec {
	scenario.forcedWaiting = []timeWindow{window}
	return scenario
}

func withAggregateTPS(scenario scenarioSpec, aggregateTPSCap float64) scenarioSpec {
	scenario.aggregateTPSCap = aggregateTPSCap
	return scenario
}

func withPreemption(scenario scenarioSpec) scenarioSpec {
	scenario.preemptionAt = time.Second
	scenario.preemptionCooldown = []timeWindow{{start: time.Second, end: 2 * time.Second}}
	return scenario
}

func withStaleRecovery(scenario scenarioSpec) scenarioSpec {
	scenario.staleMetrics = []timeWindow{{start: time.Second, end: 2 * time.Second}}
	return scenario
}

func shapedRequest(prefix string, index int, at time.Duration, shape requestShape) requestSpec {
	request := requestSpec{id: fmt.Sprintf("%s-%02d", prefix, index), at: at}
	switch shape {
	case shapeTiny:
		request.selectionInput = 64
		request.actualInput = 96
		request.actualOutput = 64
		request.reservedTokens = blockRoundUp(128 + 256)
	case shapeLarge:
		request.selectionInput = 16_000
		request.actualInput = 18_000
		request.actualOutput = 256
		request.reservedTokens = blockRoundUp(20_000 + 1024)
	case shapeSmallLargeOutput:
		request.selectionInput = 256
		request.actualInput = 384
		request.actualOutput = 5_000
		request.reservedTokens = blockRoundUp(512 + 16_000)
	case shapeLargeSmallOutput:
		request.selectionInput = 16_000
		request.actualInput = 18_000
		request.actualOutput = 96
		request.reservedTokens = blockRoundUp(20_000 + 256)
	case shapeCancel:
		request.selectionInput = 256
		request.actualInput = 384
		request.actualOutput = 8_000
		request.reservedTokens = blockRoundUp(512 + 8_192)
		request.cancelAfter = 800 * time.Millisecond
	case shapeShortCompletion:
		request.selectionInput = 128
		request.actualInput = 192
		request.actualOutput = 32
		request.reservedTokens = blockRoundUp(256 + 128)
	case shapeLongStreaming:
		request.selectionInput = 256
		request.actualInput = 384
		request.actualOutput = 8_000
		request.reservedTokens = blockRoundUp(512 + 8_192)
	default:
		request.selectionInput = 256
		request.actualInput = 384
		request.actualOutput = 128
		request.reservedTokens = blockRoundUp(512 + 512)
	}
	return request
}

func blockRoundUp(tokens int64) int64 {
	if tokens <= 0 {
		return 0
	}
	remainder := tokens % simulationBlockSize
	if remainder == 0 {
		return tokens
	}
	return tokens + simulationBlockSize - remainder
}
