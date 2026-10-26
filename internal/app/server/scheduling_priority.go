package server

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

// schedulingPriority is the backend scheduling priority for a tier. SGLang
// with --enable-priority-scheduling serves higher values first. The values are
// one apart so a waiting request never crosses SGLang's preemption threshold
// (default 10) and running sequences are not recomputed.
func schedulingPriority(tier coreadmission.RequestTier) int {
	switch tier {
	case coreadmission.TierPriority:
		return 2
	case coreadmission.TierFlex:
		return 0
	default:
		return 1
	}
}

type schedulingPriorityCounters struct {
	written [admissionEvidenceTierCount]atomic.Uint64
	skipped atomic.Uint64
}

// writeSchedulingPriority writes the tier into an admitted request as the
// backend scheduling priority when the backend orders its waiting queue by
// it (SGLang with --enable-priority-scheduling); the TPS gate relies on that
// to let the priority tier through below the reference. Other backends read
// the field differently (vLLM serves smaller values first), so they get the
// body unchanged.
func (s *proxyServer) writeSchedulingPriority(r *http.Request, tier coreadmission.RequestTier) {
	if !s.backendTierScheduling {
		return
	}
	if !apprequest.SetSchedulingPriority(r, schedulingPriority(tier)) {
		s.schedulingPriority.skipped.Add(1)
		return
	}
	s.schedulingPriority.written[admissionEvidenceTierIndex(tier)].Add(1)
}

func (s *proxyServer) writeSchedulingPriorityMetrics(w io.Writer) {
	fmt.Fprintf(w, "pig_predictive_backend_tier_scheduling_enabled %d\n", boolGauge(s.backendTierScheduling))
	fmt.Fprintf(w, "pig_predictive_tps_warm_prompt_tokens %d\n", s.warmPromptTokens)
	for index, tier := range coreadmission.RequestTiers {
		fmt.Fprintf(w, "pig_predictive_backend_tier_scheduling_requests_total{tier=%q,result=%q} %d\n",
			tier.String(), "written", s.schedulingPriority.written[index].Load())
	}
	fmt.Fprintf(w, "pig_predictive_backend_tier_scheduling_requests_total{tier=%q,result=%q} %d\n",
		"any", "skipped", s.schedulingPriority.skipped.Load())
}
