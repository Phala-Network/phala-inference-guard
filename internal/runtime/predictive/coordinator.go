package predictive

import (
	"errors"
	"fmt"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type CoordinatorIdentity struct {
	ManifestID   string
	BackendEpoch string
	Scheduler    ModelIdentity
	BlockSize    int
}

type CoordinatorConfig struct {
	Identity            CoordinatorIdentity
	Initial             domain.VirtualState
	Constraints         domain.Constraints
	Scheduler           Scheduler
	CacheCapacityBlocks int
	CacheHashKey        []byte
}

type AdmissionProposal struct {
	RequestID          string
	Analysis           TokenBlockAnalysis
	DecodeHorizonUpper int64
	Confidence         float64
}

type AdmissionResult struct {
	Decision  domain.Decision
	Cost      domain.RequestCost
	CacheHits domain.CacheHitInterval
	Reserved  bool
}

type CoordinatorSnapshot struct {
	Manager Snapshot
	Cache   CacheMirrorSnapshot
}

type Coordinator struct {
	mu        sync.Mutex
	identity  CoordinatorIdentity
	scheduler Scheduler
	manager   *Manager
	cache     *CacheMirror
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if err := validateCoordinatorIdentity(config.Identity); err != nil {
		return nil, err
	}
	if config.Scheduler == nil {
		return nil, fmt.Errorf("predictive coordinator scheduler is required")
	}
	if identity := config.Scheduler.Identity(); identity.Validate() != nil || identity != config.Identity.Scheduler {
		return nil, fmt.Errorf("predictive coordinator scheduler identity mismatch")
	}
	if err := validateInitialState(config.Initial); err != nil {
		return nil, err
	}
	if err := validateConstraints(config.Constraints); err != nil {
		return nil, err
	}
	cache, err := NewCacheMirror(CacheMirrorConfig{
		CapacityBlocks: config.CacheCapacityBlocks,
		BlockSize:      config.Identity.BlockSize,
		ManifestID:     config.Identity.ManifestID,
		BackendEpoch:   config.Identity.BackendEpoch,
		HashKey:        config.CacheHashKey,
	})
	if err != nil {
		return nil, err
	}
	return &Coordinator{
		identity:  config.Identity,
		scheduler: config.Scheduler,
		manager:   NewManager(config.Identity.ManifestID, config.Initial, config.Constraints, config.Scheduler),
		cache:     cache,
	}, nil
}

func (c *Coordinator) DecideAndReserve(now time.Time, proposal AdmissionProposal) AdmissionResult {
	if c == nil {
		return admissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	proposal.Analysis = proposal.Analysis.Clone()
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.scheduler == nil || c.scheduler.Identity() != c.identity.Scheduler {
		return admissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	if proposal.RequestID == "" || proposal.DecodeHorizonUpper < 0 || !positiveFinite(proposal.Confidence) || proposal.Confidence > 1 {
		return admissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	managerHas := c.manager.HasReservation(proposal.RequestID)
	cacheHas := c.cache.HasRequest(proposal.RequestID)
	if managerHas != cacheHas {
		return admissionFailure(domain.ReasonCacheStateUnknown)
	}
	if managerHas {
		return admissionFailure(domain.ReasonDuplicateRequest)
	}
	epoch := CacheMirrorEpoch{
		ManifestID:   c.identity.ManifestID,
		BackendEpoch: c.identity.BackendEpoch,
		BlockSize:    c.identity.BlockSize,
	}
	if err := proposal.Analysis.Validate(epoch); err != nil {
		return admissionFailure(domain.ReasonTokenizerProfileUnknown)
	}
	hits, err := c.cache.PreflightAnalyzedRequest(proposal.RequestID, proposal.Analysis)
	if err != nil {
		if errors.Is(err, ErrCacheMirrorCapacity) {
			return admissionFailure(domain.ReasonCacheStateUnknown)
		}
		return admissionFailure(domain.ReasonTokenizerProfileUnknown)
	}
	increment, err := domain.ProjectVLLM(domain.VLLMProjectionInput{
		InputTokens:        proposal.Analysis.ExactInputTokens,
		CacheHits:          hits,
		DecodeHorizonUpper: proposal.DecodeHorizonUpper,
		BlockSize:          int64(c.identity.BlockSize),
	})
	if err != nil {
		return admissionFailure(domain.ReasonCacheStateUnknown)
	}
	cost := domain.RequestCost{
		ManifestID:               c.identity.ManifestID,
		InputTokens:              proposal.Analysis.ExactInputTokens,
		KV:                       increment,
		UncachedPrefillUpper:     proposal.Analysis.ExactInputTokens - hits.Certain,
		CachedPrefillExpected:    hits.Expected,
		DecodeHorizonUpper:       proposal.DecodeHorizonUpper,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: addInt64Saturating(proposal.Analysis.ExactInputTokens, proposal.DecodeHorizonUpper),
		Confidence:               proposal.Confidence,
	}
	decision := c.manager.DecideAndReserve(now, proposal.RequestID, cost)
	result := AdmissionResult{
		Decision:  decision,
		Cost:      cost,
		CacheHits: hits,
	}
	if decision.Reason != domain.ReasonFit {
		return result
	}
	committedHits, err := c.cache.BeginAnalyzedRequest(proposal.RequestID, proposal.Analysis)
	if err != nil || committedHits != hits {
		if err == nil {
			_ = c.cache.CompleteRequest(proposal.RequestID)
		}
		if !c.manager.rollbackLatestReservation(proposal.RequestID) {
			panic("predictive coordinator could not roll back an uncommitted manager reservation")
		}
		result.Decision = domain.Decision{Reason: domain.ReasonCacheStateUnknown}
		return result
	}
	result.Reserved = true
	return result
}

func (c *Coordinator) MarkPrefillComplete(requestID string) bool {
	if c == nil || requestID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	managerCan := c.manager.CanMarkPrefillComplete(requestID)
	cacheCan := c.cache.CanMarkPrefillComplete(requestID)
	if managerCan != cacheCan {
		return false
	}
	if !managerCan {
		return false
	}
	if !c.manager.MarkPrefillComplete(requestID) || !c.cache.MarkPrefillComplete(requestID) {
		panic("predictive coordinator prefill transition violated its preflight invariant")
	}
	return true
}

func (c *Coordinator) Complete(requestID string) bool {
	if c == nil || requestID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	managerCan := c.manager.HasReservation(requestID)
	cacheCan := c.cache.CanCompleteRequest(requestID)
	if managerCan != cacheCan {
		return false
	}
	if !managerCan {
		return false
	}
	if !c.manager.Complete(requestID) || !c.cache.CompleteRequest(requestID) {
		panic("predictive coordinator completion violated its preflight invariant")
	}
	return true
}

func (c *Coordinator) Snapshot() CoordinatorSnapshot {
	if c == nil {
		return CoordinatorSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CoordinatorSnapshot{
		Manager: c.manager.Snapshot(),
		Cache:   c.cache.Snapshot(),
	}
}

func admissionFailure(reason domain.Reason) AdmissionResult {
	return AdmissionResult{Decision: domain.Decision{Reason: reason}}
}

func validateCoordinatorIdentity(identity CoordinatorIdentity) error {
	if identity.ManifestID == "" {
		return fmt.Errorf("predictive coordinator manifest id is required")
	}
	if identity.BackendEpoch == "" {
		return fmt.Errorf("predictive coordinator backend epoch is required")
	}
	if identity.BlockSize <= 0 {
		return fmt.Errorf("predictive coordinator block size must be positive")
	}
	if err := identity.Scheduler.Validate(); err != nil {
		return err
	}
	if identity.Scheduler.BackendEpoch != identity.BackendEpoch {
		return fmt.Errorf("predictive coordinator backend and scheduler epochs differ")
	}
	return nil
}

func validateInitialState(state domain.VirtualState) error {
	if state.PhysicalKVUpper < 0 || state.ActiveKVUpper < 0 || state.DecodeSequences < 0 || state.ActiveContextTokens < 0 || state.UncachedPrefillTokens < 0 {
		return fmt.Errorf("predictive coordinator initial state must be non-negative")
	}
	return nil
}

func validateConstraints(constraints domain.Constraints) error {
	if constraints.PhysicalKVHard < 0 || constraints.ActiveKVHard < 0 || !nonNegativeFinite(constraints.UserTPSTarget) || constraints.TTFTSLO < 0 || constraints.TPOTSLO < 0 || !nonNegativeFinite(constraints.WorkspaceRiskBudget) || !nonNegativeFinite(constraints.PreemptionRiskBudget) || !nonNegativeFinite(constraints.MinimumConfidence) || constraints.MinimumConfidence > 1 {
		return fmt.Errorf("predictive coordinator constraints are invalid")
	}
	return nil
}
