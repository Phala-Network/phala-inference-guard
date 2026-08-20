package metrics

import (
	"fmt"
	"io"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type BackendStats struct {
	Inflight  int64
	Accepted  uint64
	Completed uint64
	Failed    uint64
	ProxyErrs uint64
	CopyErrs  uint64
}

type BackendSnapshot struct {
	Name     string
	Upstream string
	Stats    BackendStats
	Status   backend.Runtime
}

func WriteBackends(w io.Writer, backends []BackendSnapshot) {
	for _, backend := range backends {
		status := backend.Status
		stats := backend.Stats
		fmt.Fprintf(w, "pig_backend_info{name=%q,upstream=%q} 1\n", backend.Name, backend.Upstream)
		fmt.Fprintf(w, "pig_backend_inflight{name=%q} %d\n", backend.Name, stats.Inflight)
		fmt.Fprintf(w, "pig_backend_requests_total{name=%q,decision=%q} %d\n", backend.Name, "accepted", stats.Accepted)
		fmt.Fprintf(w, "pig_backend_requests_total{name=%q,decision=%q} %d\n", backend.Name, "failed", stats.Failed)
		fmt.Fprintf(w, "pig_backend_completed_total{name=%q} %d\n", backend.Name, stats.Completed)
		fmt.Fprintf(w, "pig_backend_proxy_errors_total{name=%q} %d\n", backend.Name, stats.ProxyErrs)
		fmt.Fprintf(w, "pig_backend_body_copy_errors_total{name=%q} %d\n", backend.Name, stats.CopyErrs)
		fmt.Fprintf(w, "pig_backend_metrics_failed{name=%q} %d\n", backend.Name, num.BoolAsInt(status.Failed))
		fmt.Fprintf(w, "pig_backend_observed_running{name=%q} %d\n", backend.Name, status.Running)
		fmt.Fprintf(w, "pig_backend_observed_waiting{name=%q} %d\n", backend.Name, status.Waiting)
		fmt.Fprintf(w, "pig_backend_observed_kv_cache_usage{name=%q} %.6f\n", backend.Name, status.KVCacheUsage)
		fmt.Fprintf(w, "pig_backend_kind_info{name=%q,kind=%q} 1\n", backend.Name, status.BackendKind)
		fmt.Fprintf(w, "pig_backend_kv_capacity_tokens{name=%q} %d\n", backend.Name, status.KVCapacityTokens)
		fmt.Fprintf(w, "pig_backend_kv_active_tokens{name=%q} %d\n", backend.Name, status.KVUsedTokens)
		fmt.Fprintf(w, "pig_backend_kv_available_tokens{name=%q} %d\n", backend.Name, status.KVAvailableTokens)
		fmt.Fprintf(w, "pig_backend_kv_evictable_tokens{name=%q} %d\n", backend.Name, status.KVEvictableTokens)
		fmt.Fprintf(w, "pig_backend_kv_token_metrics_valid{name=%q} %d\n", backend.Name, num.BoolAsInt(status.KVTokenMetricsValid))
		fmt.Fprintf(w, "pig_backend_observed_generation_tokens_per_second{name=%q} %.6f\n", backend.Name, status.GenerationTPS)
		fmt.Fprintf(w, "pig_backend_observed_generation_tokens_per_second_valid{name=%q} %d\n", backend.Name, num.BoolAsInt(status.GenerationTPSValid))
	}
}
