package predictive

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

var ErrCacheMirrorCapacity = errors.New("cache mirror capacity exhausted by pinned blocks")

type CacheMirrorConfig struct {
	CapacityBlocks int
	BlockSize      int
	ManifestID     string
	BackendEpoch   string
	HashKey        []byte
}

type CacheMirrorEpoch struct {
	ManifestID   string
	BackendEpoch string
	BlockSize    int
}

type CacheMirrorSnapshot struct {
	Entries        int
	Requests       int
	PendingBlocks  int
	ActiveBlocks   int
	ProbableBlocks int
	CapacityBlocks int
	BlockSize      int
	ManifestID     string
	BackendEpoch   string
}

type CacheBlockDigest [sha256.Size]byte

type cacheBlockKey = CacheBlockDigest

type TokenBlockAnalysis struct {
	ManifestID         string
	BackendEpoch       string
	BlockSize          int
	ExactInputTokens   int64
	FullBlockDigests   []CacheBlockDigest
	PartialBlockTokens int64
	PartialBlockDigest CacheBlockDigest
}

func (a TokenBlockAnalysis) Clone() TokenBlockAnalysis {
	a.FullBlockDigests = append([]CacheBlockDigest(nil), a.FullBlockDigests...)
	return a
}

func (a TokenBlockAnalysis) Validate(epoch CacheMirrorEpoch) error {
	if a.ManifestID == "" || a.ManifestID != epoch.ManifestID {
		return fmt.Errorf("token block analysis manifest does not match cache mirror epoch")
	}
	if a.BackendEpoch == "" || a.BackendEpoch != epoch.BackendEpoch {
		return fmt.Errorf("token block analysis backend epoch does not match cache mirror epoch")
	}
	if a.BlockSize <= 0 || a.BlockSize != epoch.BlockSize {
		return fmt.Errorf("token block analysis block size does not match cache mirror epoch")
	}
	if a.ExactInputTokens < 0 {
		return fmt.Errorf("token block analysis input tokens must be non-negative")
	}
	fullBlocks := a.ExactInputTokens / int64(a.BlockSize)
	if fullBlocks != int64(len(a.FullBlockDigests)) {
		return fmt.Errorf("token block analysis full-block count does not match input tokens")
	}
	wantPartial := a.ExactInputTokens % int64(a.BlockSize)
	if a.PartialBlockTokens != wantPartial {
		return fmt.Errorf("token block analysis partial-block count does not match input tokens")
	}
	for index, digest := range a.FullBlockDigests {
		if digest == (CacheBlockDigest{}) {
			return fmt.Errorf("token block analysis full-block digest %d is empty", index)
		}
	}
	if a.PartialBlockTokens == 0 {
		if a.PartialBlockDigest != (CacheBlockDigest{}) {
			return fmt.Errorf("token block analysis has a digest for an empty partial block")
		}
	} else if a.PartialBlockDigest == (CacheBlockDigest{}) {
		return fmt.Errorf("token block analysis partial-block digest is empty")
	}
	return nil
}

type cacheBlockEntry struct {
	PendingReferences int
	ActiveReferences  int
	ProbableResident  bool
	LastUsed          uint64
}

type cacheMirrorRequest struct {
	Keys            []cacheBlockKey
	PrefillComplete bool
}

type CacheMirror struct {
	mu             sync.Mutex
	capacityBlocks int
	blockSize      int
	manifestID     string
	backendEpoch   string
	hashKey        []byte
	clock          uint64
	entries        map[cacheBlockKey]*cacheBlockEntry
	requests       map[string]cacheMirrorRequest
}

func NewCacheMirror(config CacheMirrorConfig) (*CacheMirror, error) {
	if config.CapacityBlocks <= 0 {
		return nil, fmt.Errorf("cache mirror capacity must be positive")
	}
	if config.BlockSize <= 0 {
		return nil, fmt.Errorf("cache mirror block size must be positive")
	}
	if config.ManifestID == "" {
		return nil, fmt.Errorf("cache mirror manifest id is required")
	}
	if config.BackendEpoch == "" {
		return nil, fmt.Errorf("cache mirror backend epoch is required")
	}
	hashKey := append([]byte(nil), config.HashKey...)
	if len(hashKey) == 0 {
		hashKey = make([]byte, sha256.Size)
		if _, err := rand.Read(hashKey); err != nil {
			return nil, fmt.Errorf("generate cache mirror hash key: %w", err)
		}
	}
	if len(hashKey) < 16 {
		return nil, fmt.Errorf("cache mirror hash key must contain at least 16 bytes")
	}
	return &CacheMirror{
		capacityBlocks: config.CapacityBlocks,
		blockSize:      config.BlockSize,
		manifestID:     config.ManifestID,
		backendEpoch:   config.BackendEpoch,
		hashKey:        hashKey,
		entries:        make(map[cacheBlockKey]*cacheBlockEntry),
		requests:       make(map[string]cacheMirrorRequest),
	}, nil
}

func (m *CacheMirror) Estimate(tokenIDs []int64) (domain.CacheHitInterval, error) {
	if m == nil {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys, err := m.blockKeysLocked(tokenIDs)
	if err != nil {
		return domain.CacheHitInterval{}, err
	}
	return m.estimateLocked(keys), nil
}

func (m *CacheMirror) EstimateAnalysis(analysis TokenBlockAnalysis) (domain.CacheHitInterval, error) {
	if m == nil {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys, err := m.analysisKeysLocked(analysis)
	if err != nil {
		return domain.CacheHitInterval{}, err
	}
	return m.estimateLocked(keys), nil
}

func (m *CacheMirror) BeginRequest(requestID string, tokenIDs []int64) (domain.CacheHitInterval, error) {
	if m == nil {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror is nil")
	}
	if requestID == "" {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror request id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.requests[requestID]; exists {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror request %q already exists", requestID)
	}
	keys, err := m.blockKeysLocked(tokenIDs)
	if err != nil {
		return domain.CacheHitInterval{}, err
	}
	return m.beginRequestLocked(requestID, keys)
}

func (m *CacheMirror) BeginAnalyzedRequest(requestID string, analysis TokenBlockAnalysis) (domain.CacheHitInterval, error) {
	if m == nil {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror is nil")
	}
	if requestID == "" {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror request id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.requests[requestID]; exists {
		return domain.CacheHitInterval{}, fmt.Errorf("cache mirror request %q already exists", requestID)
	}
	keys, err := m.analysisKeysLocked(analysis)
	if err != nil {
		return domain.CacheHitInterval{}, err
	}
	return m.beginRequestLocked(requestID, keys)
}

func (m *CacheMirror) beginRequestLocked(requestID string, keys []cacheBlockKey) (domain.CacheHitInterval, error) {
	hit := m.estimateLocked(keys)
	if err := m.ensureCapacityLocked(keys); err != nil {
		return domain.CacheHitInterval{}, err
	}
	for _, key := range keys {
		entry, exists := m.entries[key]
		if !exists {
			entry = &cacheBlockEntry{}
			m.entries[key] = entry
		}
		entry.PendingReferences++
		m.touchLocked(entry)
	}
	m.requests[requestID] = cacheMirrorRequest{Keys: append([]cacheBlockKey(nil), keys...)}
	return hit, nil
}

func (m *CacheMirror) MarkPrefillComplete(requestID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request, exists := m.requests[requestID]
	if !exists || request.PrefillComplete {
		return false
	}
	for _, key := range request.Keys {
		entry, present := m.entries[key]
		if !present || entry.PendingReferences <= 0 {
			return false
		}
	}
	for _, key := range request.Keys {
		entry := m.entries[key]
		entry.PendingReferences--
		entry.ActiveReferences++
		entry.ProbableResident = true
		m.touchLocked(entry)
	}
	request.PrefillComplete = true
	m.requests[requestID] = request
	return true
}

func (m *CacheMirror) CompleteRequest(requestID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request, exists := m.requests[requestID]
	if !exists {
		return false
	}
	for _, key := range request.Keys {
		entry, present := m.entries[key]
		if !present {
			continue
		}
		if request.PrefillComplete {
			if entry.ActiveReferences > 0 {
				entry.ActiveReferences--
			}
			entry.ProbableResident = true
		} else if entry.PendingReferences > 0 {
			entry.PendingReferences--
		}
		if entry.PendingReferences == 0 && entry.ActiveReferences == 0 && !entry.ProbableResident {
			delete(m.entries, key)
			continue
		}
		m.touchLocked(entry)
	}
	delete(m.requests, requestID)
	return true
}

func (m *CacheMirror) Reset(epoch CacheMirrorEpoch) error {
	if m == nil {
		return fmt.Errorf("cache mirror is nil")
	}
	if epoch.ManifestID == "" || epoch.BackendEpoch == "" || epoch.BlockSize <= 0 {
		return fmt.Errorf("cache mirror reset epoch is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manifestID = epoch.ManifestID
	m.backendEpoch = epoch.BackendEpoch
	m.blockSize = epoch.BlockSize
	m.clock = 0
	m.entries = make(map[cacheBlockKey]*cacheBlockEntry)
	m.requests = make(map[string]cacheMirrorRequest)
	return nil
}

func (m *CacheMirror) Snapshot() CacheMirrorSnapshot {
	if m == nil {
		return CacheMirrorSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := CacheMirrorSnapshot{
		Entries:        len(m.entries),
		Requests:       len(m.requests),
		CapacityBlocks: m.capacityBlocks,
		BlockSize:      m.blockSize,
		ManifestID:     m.manifestID,
		BackendEpoch:   m.backendEpoch,
	}
	for _, entry := range m.entries {
		switch {
		case entry.ActiveReferences > 0:
			snapshot.ActiveBlocks++
		case entry.PendingReferences > 0:
			snapshot.PendingBlocks++
		case entry.ProbableResident:
			snapshot.ProbableBlocks++
		}
	}
	return snapshot
}

func (m *CacheMirror) blockKeysLocked(tokenIDs []int64) ([]cacheBlockKey, error) {
	for index, tokenID := range tokenIDs {
		if tokenID < 0 {
			return nil, fmt.Errorf("token id %d at index %d is negative", tokenID, index)
		}
	}
	blocks := len(tokenIDs) / m.blockSize
	keys := make([]cacheBlockKey, 0, blocks)
	var previous cacheBlockKey
	var encoded [8]byte
	for block := 0; block < blocks; block++ {
		digest := hmac.New(sha256.New, m.hashKey)
		digest.Write([]byte(m.manifestID))
		digest.Write([]byte{0})
		digest.Write([]byte(m.backendEpoch))
		digest.Write([]byte{0})
		binary.LittleEndian.PutUint64(encoded[:], uint64(m.blockSize))
		digest.Write(encoded[:])
		digest.Write(previous[:])
		start := block * m.blockSize
		for _, tokenID := range tokenIDs[start : start+m.blockSize] {
			binary.LittleEndian.PutUint64(encoded[:], uint64(tokenID))
			digest.Write(encoded[:])
		}
		var key cacheBlockKey
		copy(key[:], digest.Sum(nil))
		keys = append(keys, key)
		previous = key
	}
	return keys, nil
}

func (m *CacheMirror) analysisKeysLocked(analysis TokenBlockAnalysis) ([]cacheBlockKey, error) {
	if err := analysis.Validate(CacheMirrorEpoch{
		ManifestID:   m.manifestID,
		BackendEpoch: m.backendEpoch,
		BlockSize:    m.blockSize,
	}); err != nil {
		return nil, err
	}
	return append([]cacheBlockKey(nil), analysis.FullBlockDigests...), nil
}

func (m *CacheMirror) estimateLocked(keys []cacheBlockKey) domain.CacheHitInterval {
	var certain int64
	var expected int64
	certainPrefix := true
	for _, key := range keys {
		entry, exists := m.entries[key]
		if !exists || (entry.ActiveReferences == 0 && !entry.ProbableResident) {
			break
		}
		m.touchLocked(entry)
		switch {
		case entry.ActiveReferences > 0:
			expected += int64(m.blockSize)
			if certainPrefix {
				certain += int64(m.blockSize)
			}
		case entry.ProbableResident && entry.PendingReferences == 0:
			expected += int64(m.blockSize)
			certainPrefix = false
		default:
			return domain.CacheHitInterval{
				Certain:  certain,
				Lower:    certain,
				Expected: expected,
				Upper:    expected,
			}
		}
	}
	return domain.CacheHitInterval{
		Certain:  certain,
		Lower:    certain,
		Expected: expected,
		Upper:    expected,
	}
}

func (m *CacheMirror) ensureCapacityLocked(keys []cacheBlockKey) error {
	requested := make(map[cacheBlockKey]struct{}, len(keys))
	missing := 0
	for _, key := range keys {
		requested[key] = struct{}{}
		if _, exists := m.entries[key]; !exists {
			missing++
		}
	}
	excess := len(m.entries) + missing - m.capacityBlocks
	if excess <= 0 {
		return nil
	}
	type candidate struct {
		Key      cacheBlockKey
		LastUsed uint64
	}
	candidates := make([]candidate, 0, len(m.entries))
	for key, entry := range m.entries {
		if _, needed := requested[key]; needed || entry.ActiveReferences > 0 || entry.PendingReferences > 0 {
			continue
		}
		candidates = append(candidates, candidate{Key: key, LastUsed: entry.LastUsed})
	}
	if len(candidates) < excess {
		return ErrCacheMirrorCapacity
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].LastUsed != candidates[right].LastUsed {
			return candidates[left].LastUsed < candidates[right].LastUsed
		}
		return bytes.Compare(candidates[left].Key[:], candidates[right].Key[:]) < 0
	})
	for _, item := range candidates[:excess] {
		delete(m.entries, item.Key)
	}
	return nil
}

func (m *CacheMirror) touchLocked(entry *cacheBlockEntry) {
	m.clock++
	entry.LastUsed = m.clock
}
