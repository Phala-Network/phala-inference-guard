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
		fmt.Fprintf(w, "pig_backend_observed_generation_tokens_per_second{name=%q} %.6f\n", backend.Name, status.GenerationTPS)
		fmt.Fprintf(w, "pig_backend_observed_generation_tokens_per_second_valid{name=%q} %d\n", backend.Name, num.BoolAsInt(status.GenerationTPSValid))
		fmt.Fprintf(w, "pig_backend_observed_ttft_avg_seconds{name=%q} %.6f\n", backend.Name, status.TTFTAvg)
		fmt.Fprintf(w, "pig_backend_observed_ttft_p95_seconds{name=%q} %.6f\n", backend.Name, status.TTFTP95)
		fmt.Fprintf(w, "pig_backend_observed_ttft_p99_seconds{name=%q} %.6f\n", backend.Name, status.TTFTP99)
		fmt.Fprintf(w, "pig_backend_observed_ttft_valid{name=%q} %d\n", backend.Name, num.BoolAsInt(status.TTFTValid))
	}
}
