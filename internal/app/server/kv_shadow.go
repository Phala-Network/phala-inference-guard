package server

import (
	"strconv"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
)

func (s *proxyServer) shadowKVRequest(cost kvadmission.Cost) func() {
	if s == nil || s.kvShadow == nil {
		return func() {}
	}
	requestID := strconv.FormatUint(s.nextKVShadowID.Add(1), 10)
	started := time.Now()
	_, reserved := s.kvShadow.DecideAndReserve(started, requestID, cost, s.kvBackendSnapshots())
	s.kvShadowDecisionDuration.Observe(time.Since(started))
	if !reserved {
		return func() {}
	}
	return func() { s.kvShadow.Release(requestID) }
}

func (s *proxyServer) kvBackendSnapshots() []kvadmission.BackendSnapshot {
	if s == nil {
		return nil
	}
	result := make([]kvadmission.BackendSnapshot, 0, len(s.backends))
	for index, backend := range s.backends {
		status := s.backendRuntimeStatus(index, backend)
		result = append(result, kvadmission.BackendSnapshot{
			Name:                 backend.Name(),
			Kind:                 kvadmission.ParseBackendKind(status.BackendKind),
			CapacityTokens:       status.KVCapacityTokens,
			UsedTokens:           status.KVUsedTokens,
			AvailableTokens:      status.KVAvailableTokens,
			EvictableTokens:      status.KVEvictableTokens,
			Usage:                status.KVCacheUsage,
			Updated:              status.Updated,
			GenerationTokens:     status.Generation,
			GenerationTPS:        status.GenerationTPS,
			Waiting:              status.Waiting,
			PreemptionDelta:      status.PreemptionDelta,
			PreemptionDeltaValid: status.PreemptionDeltaValid,
			Failed:               status.Failed,
			TokenMetricsValid:    status.KVTokenMetricsValid,
		})
	}
	return result
}

func (s *proxyServer) backendRuntimeStatus(index int, backend *backendProxy) runtimebackend.Runtime {
	status := backend.Status()
	if !status.Updated.IsZero() || s.dynamicController == nil {
		return status
	}
	static := s.dynamicController.StaticMetricRuntimes()
	if index < 0 || index >= len(static) || static[index].Updated.IsZero() {
		return status
	}
	status = static[index]
	status.Name = backend.Name()
	return status
}
