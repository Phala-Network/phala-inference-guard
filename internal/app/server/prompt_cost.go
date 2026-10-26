package server

import (
	"container/list"
	"fmt"
	"io"
	"math"
	"sync"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

// promptCostEstimator estimates the uncached Prompt tokens a request will make
// the backend compute. It mirrors the backend prefix cache with an LRU set of
// Prompt block hashes for requests this node admitted, so a turn that
// continues a conversation already on the node is priced at its new suffix
// while a cold Prompt is priced in full.
//
// Two ratios learned from response usage keep the estimate honest: body bytes
// per Prompt token, and the backend's measured uncached tokens over the
// estimate (which absorbs what the mirror cannot see, such as evictions under
// memory pressure or a host-memory cache tier).
type promptCostEstimator struct {
	mu             sync.Mutex
	index          map[uint64]*list.Element
	lru            *list.List
	capacityTokens int64

	bytesPerToken ratioEstimate
	correction    ratioEstimate

	estimates          [admissionEvidenceTierCount]uint64
	estimatedTokens    [admissionEvidenceTierCount]float64
	learnedRequests    uint64
	learnedEstimated   float64
	learnedUncached    float64
	warm               warmPromptOutcomes
	matchedBlocks      uint64
	consideredBlocks   uint64
	evictedBlocks      uint64
	insertedBlockTotal uint64
}

const (
	defaultPromptBytesPerToken = 4.0
	// ratioEstimateDecay weights roughly the last 200 completed requests.
	ratioEstimateDecay = 0.995
	minimumCorrection  = 0.1
	maximumCorrection  = 10.0
)

// ratioEstimate is a decayed ratio of sums, so large requests weigh by size.
type ratioEstimate struct {
	numerator   float64
	denominator float64
}

func (r *ratioEstimate) add(numerator, denominator float64) {
	r.numerator = r.numerator*ratioEstimateDecay + numerator
	r.denominator = r.denominator*ratioEstimateDecay + denominator
}

func (r ratioEstimate) value(fallback, minimum, maximum float64) float64 {
	if r.denominator <= 0 {
		return fallback
	}
	v := r.numerator / r.denominator
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return math.Min(math.Max(v, minimum), maximum)
}

type promptCost struct {
	known           bool
	uncachedTokens  int64
	rawUncachedToks float64
	// warmLimit is the warm-Prompt threshold the request was admitted under
	// when the TPS gate let it pass only as a warm Prompt; zero otherwise.
	warmLimit int64
}

// warmPromptOutcomes measures the requests the TPS gate admitted only because
// their estimated uncached Prompt fit the warm-Prompt threshold: what the
// backend then actually computed for them, and how many were in fact larger
// than the threshold (cold Prompts the estimate let through).
type warmPromptOutcomes struct {
	learnedRequests uint64
	uncachedTokens  float64
	overLimit       uint64
}

// AdmittedAsWarm marks a cost whose request passed the TPS gate as a warm
// Prompt under limit, so its measured usage is attributed to that decision.
func (c promptCost) AdmittedAsWarm(limit int64) promptCost {
	c.warmLimit = limit
	return c
}

func newPromptCostEstimator(capacityTokens int64) *promptCostEstimator {
	return &promptCostEstimator{
		index:          make(map[uint64]*list.Element),
		lru:            list.New(),
		capacityTokens: capacityTokens,
	}
}

func (e *promptCostEstimator) capacityBlocksLocked() int {
	if e.capacityTokens <= 0 {
		return 0
	}
	bytes := float64(e.capacityTokens) * e.bytesPerToken.value(defaultPromptBytesPerToken, 0.5, 64)
	return int(bytes / apprequest.PromptBlockBytes)
}

// Estimate prices one request without changing the mirror.
func (e *promptCostEstimator) Estimate(prompt apprequest.PromptFingerprint, tier int) promptCost {
	if e == nil || len(prompt.Blocks) == 0 || prompt.Bytes <= 0 {
		return promptCost{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	matched := 0
	for _, block := range prompt.Blocks {
		if _, ok := e.index[block]; !ok {
			break
		}
		matched++
	}
	e.matchedBlocks += uint64(matched)
	e.consideredBlocks += uint64(len(prompt.Blocks))
	uncachedBytes := prompt.Bytes - int64(matched)*apprequest.PromptBlockBytes
	if uncachedBytes < 0 {
		uncachedBytes = 0
	}
	raw := float64(uncachedBytes) / e.bytesPerToken.value(defaultPromptBytesPerToken, 0.5, 64)
	tokens := raw * e.correction.value(1, minimumCorrection, maximumCorrection)
	if tier >= 0 && tier < admissionEvidenceTierCount {
		e.estimates[tier]++
		e.estimatedTokens[tier] += tokens
	}
	return promptCost{known: true, uncachedTokens: int64(math.Round(tokens)), rawUncachedToks: raw}
}

// Admitted records that the backend will now hold this Prompt's prefix.
func (e *promptCostEstimator) Admitted(prompt apprequest.PromptFingerprint) {
	if e == nil || len(prompt.Blocks) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, block := range prompt.Blocks {
		if element, ok := e.index[block]; ok {
			e.lru.MoveToFront(element)
			continue
		}
		e.index[block] = e.lru.PushFront(block)
		e.insertedBlockTotal++
	}
	capacity := e.capacityBlocksLocked()
	for capacity > 0 && e.lru.Len() > capacity {
		oldest := e.lru.Back()
		delete(e.index, oldest.Value.(uint64))
		e.lru.Remove(oldest)
		e.evictedBlocks++
	}
}

// Learn compares the estimate with the usage the backend reported.
func (e *promptCostEstimator) Learn(prompt apprequest.PromptFingerprint, cost promptCost, promptTokens, cachedTokens int64) {
	if e == nil || !cost.known || promptTokens <= 0 || prompt.Bytes <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bytesPerToken.add(float64(prompt.Bytes), float64(promptTokens))
	if cachedTokens < 0 || cachedTokens > promptTokens {
		return
	}
	uncached := float64(promptTokens - cachedTokens)
	e.correction.add(uncached, cost.rawUncachedToks)
	e.learnedRequests++
	e.learnedEstimated += float64(cost.uncachedTokens)
	e.learnedUncached += uncached
	if cost.warmLimit > 0 {
		e.warm.learnedRequests++
		e.warm.uncachedTokens += uncached
		if uncached > float64(cost.warmLimit) {
			e.warm.overLimit++
		}
	}
}

type promptCostSnapshot struct {
	estimates          [admissionEvidenceTierCount]uint64
	estimatedTokens    [admissionEvidenceTierCount]float64
	learnedRequests    uint64
	learnedEstimated   float64
	learnedUncached    float64
	warm               warmPromptOutcomes
	bytesPerToken      float64
	correction         float64
	indexBlocks        int
	capacityBlocks     int
	matchedBlocks      uint64
	consideredBlocks   uint64
	evictedBlocks      uint64
	insertedBlockTotal uint64
}

func (e *promptCostEstimator) Snapshot() promptCostSnapshot {
	if e == nil {
		return promptCostSnapshot{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return promptCostSnapshot{
		estimates:          e.estimates,
		estimatedTokens:    e.estimatedTokens,
		learnedRequests:    e.learnedRequests,
		learnedEstimated:   e.learnedEstimated,
		learnedUncached:    e.learnedUncached,
		warm:               e.warm,
		bytesPerToken:      e.bytesPerToken.value(defaultPromptBytesPerToken, 0.5, 64),
		correction:         e.correction.value(1, minimumCorrection, maximumCorrection),
		indexBlocks:        e.lru.Len(),
		capacityBlocks:     e.capacityBlocksLocked(),
		matchedBlocks:      e.matchedBlocks,
		consideredBlocks:   e.consideredBlocks,
		evictedBlocks:      e.evictedBlocks,
		insertedBlockTotal: e.insertedBlockTotal,
	}
}

func writePromptCostMetrics(w io.Writer, snapshot promptCostSnapshot) {
	for tierIndex, tier := range coreadmission.RequestTiers {
		fmt.Fprintf(w, "pig_predictive_prompt_cost_estimates_total{tier=%q} %d\n", tier.String(), snapshot.estimates[tierIndex])
		fmt.Fprintf(w, "pig_predictive_prompt_cost_estimated_tokens_total{tier=%q} %.0f\n", tier.String(), snapshot.estimatedTokens[tierIndex])
	}
	fmt.Fprintf(w, "pig_predictive_prompt_cost_learned_requests_total %d\n", snapshot.learnedRequests)
	fmt.Fprintf(w, "pig_predictive_prompt_cost_learned_estimated_tokens_total %.0f\n", snapshot.learnedEstimated)
	fmt.Fprintf(w, "pig_predictive_prompt_cost_learned_uncached_tokens_total %.0f\n", snapshot.learnedUncached)
	fmt.Fprintf(w, "pig_predictive_prompt_cost_bytes_per_token %.6f\n", snapshot.bytesPerToken)
	fmt.Fprintf(w, "pig_predictive_prompt_cost_correction %.6f\n", snapshot.correction)
	fmt.Fprintf(w, "pig_predictive_warm_prompt_learned_requests_total %d\n", snapshot.warm.learnedRequests)
	fmt.Fprintf(w, "pig_predictive_warm_prompt_learned_uncached_tokens_total %.0f\n", snapshot.warm.uncachedTokens)
	fmt.Fprintf(w, "pig_predictive_warm_prompt_over_limit_total %d\n", snapshot.warm.overLimit)
	fmt.Fprintf(w, "pig_predictive_prompt_index_blocks %d\n", snapshot.indexBlocks)
	fmt.Fprintf(w, "pig_predictive_prompt_index_capacity_blocks %d\n", snapshot.capacityBlocks)
	fmt.Fprintf(w, "pig_predictive_prompt_index_matched_blocks_total %d\n", snapshot.matchedBlocks)
	fmt.Fprintf(w, "pig_predictive_prompt_index_considered_blocks_total %d\n", snapshot.consideredBlocks)
	fmt.Fprintf(w, "pig_predictive_prompt_index_inserted_blocks_total %d\n", snapshot.insertedBlockTotal)
	fmt.Fprintf(w, "pig_predictive_prompt_index_evicted_blocks_total %d\n", snapshot.evictedBlocks)
}

func boolGauge(value bool) int {
	if value {
		return 1
	}
	return 0
}
