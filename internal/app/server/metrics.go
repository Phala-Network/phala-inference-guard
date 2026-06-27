package server

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/semantic"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

func (s *proxyServer) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	expected := "Bearer " + s.cfg.Token
	got := values[0]
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func (s *proxyServer) metrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.writeLocalMetrics(w)
}

func (s *proxyServer) combinedMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.writeLocalMetrics(w)
	_, _ = io.WriteString(w, "\n# --- Backend Metrics ---\n")
	s.writeBackendMetricsRaw(w)
}

func (s *proxyServer) writeLocalMetrics(w io.Writer) {
	metrics.WriteRuntime(w, s.runtimeMetricsInput())
	metrics.WriteBackends(w, s.backendMetricsInput())
	s.writeDynamicMetrics(w)
	metrics.WriteClassifier(w, s.classifierMetricsInput())
	metrics.WritePriority(w, s.priorityMetricsInput())
	metrics.WriteLanes(w, s.laneMetricsInput())
}

func (s *proxyServer) writeBackendMetricsRaw(w io.Writer) {
	client := &http.Client{Timeout: 2 * time.Second}
	wrote := false
	for _, backend := range s.backends {
		metricsURL := backend.MetricsURL()
		if metricsURL == "" {
			_, _ = fmt.Fprintf(w, "# backend %s metrics URL is empty\n", backend.Name())
			continue
		}
		_, _ = fmt.Fprintf(w, "\n# --- backend %s %s ---\n", backend.Name(), metricsURL)
		response, err := client.Get(metricsURL)
		if err != nil {
			_, _ = fmt.Fprintf(w, "# failed to fetch backend metrics: %v\n", err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024))
		closeErr := response.Body.Close()
		if readErr != nil {
			_, _ = fmt.Fprintf(w, "# failed to read backend metrics: %v\n", readErr)
			continue
		}
		if closeErr != nil {
			_, _ = fmt.Fprintf(w, "# failed to close backend metrics body: %v\n", closeErr)
			continue
		}
		if response.StatusCode != http.StatusOK {
			_, _ = fmt.Fprintf(w, "# backend metrics status %d\n", response.StatusCode)
			continue
		}
		_, _ = w.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			_, _ = io.WriteString(w, "\n")
		}
		wrote = true
	}
	if !wrote {
		_, _ = io.WriteString(w, "# no backend metrics were fetched\n")
	}
}

func (s *proxyServer) runtimeMetricsInput() metrics.RuntimeInput {
	currentLimit, _, _ := s.currentQoSLimit()
	dynamicCounters := s.dynamicController.Counters()
	return metrics.RuntimeInput{
		Config: metrics.RuntimeConfig{
			Version:                    version,
			QueueWait:                  s.cfg.QoSQueueWait,
			QueueWaitEffectiveCap:      num.MinDuration(s.cfg.QoSQueueWait, maxQoSQueueWait),
			QueuePoll:                  s.cfg.QoSQueuePoll,
			DynamicEnabled:             s.cfg.DynamicEnabled,
			DynamicEnforce:             s.cfg.DynamicEnforce,
			SemanticTTFTScanLimitBytes: semantic.ScanLimitBytes,
			BackendCount:               len(s.backends),
		},
		Uptime:        time.Since(s.started),
		RejectedTotal: s.total429.Load(),
		Queue: metrics.QueueSnapshot{
			Current:        s.qosGate.QueueCurrent(),
			Total:          s.qosGate.QueueTotal(),
			Timeout:        s.qosGate.QueueTimeout(),
			WaitCount:      s.qosGate.QueueWaitCount(),
			WaitSecondsSum: s.qosGate.QueueWaitSecondsSum(),
		},
		Tier: s.qosGate.TierSnapshot(currentLimit),
		Streams: metrics.StreamSnapshot{
			KeepAliveStreams:  s.sseKeepAliveStreams.Load(),
			KeepAliveComments: s.sseKeepAliveComments.Load(),
			BridgeStreams:     s.sseBridgeStreams.Load(),
			BridgeUpstreamErr: s.sseBridgeUpstreamErr.Load(),
			BridgeInvalid:     s.sseBridgeInvalid.Load(),
			BridgeCopyErr:     s.sseBridgeCopyErr.Load(),
		},
		Errors: metrics.ErrorSnapshot{
			RequestSemanticTTFTLimited: s.semanticTTFTLimited.Load(),
			ProxyUpstreamErr:           s.proxyUpstreamErr.Load(),
			ProxyCopyErr:               s.proxyCopyErr.Load(),
		},
		Dynamic: metrics.DynamicCounterSnapshot{
			PollOK:             dynamicCounters.PollOK,
			PollFailed:         dynamicCounters.PollFailed,
			DynamicRejected:    s.qosGate.DynamicRejected(),
			BackendUnavailable: s.backendUnavailable.Load(),
		},
		Histograms: metrics.RuntimeHistograms{
			DecisionDuration:    &s.decisionDuration,
			ProxyTTFB:           &s.proxyTTFB,
			RequestSemanticTTFT: &s.requestSemanticTTFT,
			ProxyTotal:          &s.proxyTotal,
			InternalOverhead:    &s.internalOverhead,
		},
	}
}

func (s *proxyServer) backendMetricsInput() []metrics.BackendSnapshot {
	backends := make([]metrics.BackendSnapshot, 0, len(s.backends))
	for _, backend := range s.backends {
		stats := backend.Stats()
		backends = append(backends, metrics.BackendSnapshot{
			Name:     backend.Name(),
			Upstream: backend.Upstream(),
			Stats: metrics.BackendStats{
				Inflight:  stats.Inflight,
				Accepted:  stats.Accepted,
				Completed: stats.Completed,
				Failed:    stats.Failed,
				ProxyErrs: stats.ProxyErrs,
				CopyErrs:  stats.CopyErrs,
			},
			Status: backend.Status(),
		})
	}
	return backends
}

func (s *proxyServer) writeDynamicMetrics(w io.Writer) {
	if s.dynamicController == nil {
		return
	}
	snapshot := s.dynamicController.Snapshot()
	metrics.WriteDynamic(w, snapshot, metrics.DynamicConfig{
		TTFTEnabled:               s.cfg.DynamicTTFTEnabled,
		PressureEnabled:           s.cfg.DynamicPressureEnabled,
		PressureHeadroom:          s.cfg.DynamicPressureHeadroom,
		PressureMinLimit:          s.cfg.DynamicPressureMinLimit,
		PressureLearnRatio:        s.cfg.DynamicPressureLearnRatio,
		PressureLearnMinRunning:   s.cfg.DynamicPressureLearnMinRunning,
		UserTPSEnabled:            s.cfg.DynamicUserTPSEnabled,
		UserTPSYellow:             s.cfg.DynamicUserTPSYellow,
		UserTPSRed:                s.cfg.DynamicUserTPSRed,
		UserTPSYellowN:            s.cfg.DynamicUserTPSYellowN,
		UserTPSRedN:               s.cfg.DynamicUserTPSRedN,
		UserTPSGraceMin:           s.cfg.DynamicUserTPSGraceMin,
		UserTPSGraceMax:           s.cfg.DynamicUserTPSGraceMax,
		UserTPSGraceBps:           s.cfg.DynamicUserTPSGraceBps,
		UserTPSGraceMul:           s.cfg.DynamicUserTPSGraceMul,
		UserTPSCapacityLearn:      s.cfg.DynamicUserTPSCapacityLearn,
		UserTPSCapacityRatio:      s.cfg.DynamicUserTPSCapacityRatio,
		UserTPSCapacityRatioMax:   s.cfg.DynamicUserTPSCapacityRatioMax,
		UserTPSCapacityStepUp:     s.cfg.DynamicUserTPSCapacityStepUp,
		UserTPSCapacityHealthyN:   s.cfg.DynamicUserTPSCapacityHealthyN,
		UserTPSCapacityHealthyMul: s.cfg.DynamicUserTPSCapacityHealthyMul,
		UserTPSCapacitySmoothing:  s.cfg.DynamicUserTPSCapacitySmoothing,
	}, s.dynamicController.PressureCap())
}

func (s *proxyServer) classifierMetricsInput() metrics.ClassifierInput {
	return metrics.ClassifierInput{
		Enabled:       s.cfg.ClassifyOutputTokens,
		BodyBytes:     s.cfg.JSONClassifyBodyBytes,
		Limit:         s.cfg.JSONClassifyLimit,
		Inflight:      s.requestClassifier.Inflight(),
		RejectedTotal: s.requestClassifier.Rejected(),
		Paths:         s.cfg.QoSPaths,
	}
}

func (s *proxyServer) priorityMetricsInput() metrics.PriorityInput {
	stats := s.priorityInjector.Stats()
	return metrics.PriorityInput{
		Enabled:            stats.Enabled,
		BodyBytes:          stats.BodyBytes,
		BufferBytes:        stats.BufferBytes,
		StreamBufferBytes:  stats.StreamBufferBytes,
		Limit:              stats.Limit,
		Inflight:           stats.Inflight,
		Rewritten:          stats.Rewritten,
		Skipped:            stats.Skipped,
		Failed:             stats.Failed,
		DurationCount:      stats.DurationCount,
		DurationSeconds:    stats.DurationSeconds,
		DurationBuckets:    stats.DurationBuckets,
		DurationMaxSeconds: stats.DurationMaxSeconds,
	}
}

func (s *proxyServer) laneMetricsInput() metrics.LaneInput {
	return metrics.LaneInput{
		Snapshots:              s.laneMetricSnapshots(),
		DurationBucketsSeconds: durationBucketsSeconds,
		BodyBucketsBytes:       bodyBucketsBytes,
		Thresholds: metrics.LaneThresholds{
			MediumBodyBytes:      s.cfg.MediumBodyBytes,
			LongBodyBytes:        s.cfg.LongBodyBytes,
			VeryLongBodyBytes:    s.cfg.VeryLongBodyBytes,
			MediumOutputTokens:   s.cfg.MediumOutputTokens,
			LongOutputTokens:     s.cfg.LongOutputTokens,
			VeryLongOutputTokens: s.cfg.VeryLongOutputTokens,
		},
		AdaptiveOutputEnabled:     s.cfg.AdaptiveOutput,
		AdaptiveOutputSamples:     s.outputSampleCount(),
		EffectiveOutputThresholds: s.effectiveOutputThresholds(),
	}
}

func (s *proxyServer) laneMetricSnapshots() []lane.Snapshot {
	lanes := []*lane.Lane{s.globalLn, s.defaultLn, s.mediumLn, s.longLn, s.veryLongLn, s.mediumOutputLn, s.longOutputLn, s.veryLongOutputLn, s.unknownLn}
	snapshots := make([]lane.Snapshot, 0, len(lanes))
	for _, ln := range lanes {
		snapshots = append(snapshots, ln.Snapshot())
	}
	return snapshots
}
