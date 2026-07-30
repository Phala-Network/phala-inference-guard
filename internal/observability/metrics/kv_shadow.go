package metrics

import (
	"fmt"
	"io"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/kvshadow"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type KVShadowInput struct {
	Mode      string
	Policy    kvadmission.Policy
	Estimator kvadmission.EstimatorConfig
	Snapshot  kvshadow.Snapshot
	Now       time.Time
}

func WriteKVShadow(w io.Writer, input KVShadowInput) {
	mode := input.Mode
	if mode == "" {
		mode = "off"
	}
	fmt.Fprintf(w, "pig_kv_admission_mode_info{mode=%q} 1\n", mode)
	fmt.Fprintf(w, "pig_kv_admission_shadow_enabled %d\n", num.BoolAsInt(mode == "shadow"))
	writeKVShadowBudget(w, kvadmission.BackendVLLM, input.Policy.VLLM)
	writeKVShadowBudget(w, kvadmission.BackendSGLang, input.Policy.SGLang)
	fmt.Fprintf(w, "pig_kv_shadow_max_metrics_age_seconds %.6f\n", input.Policy.MaxMetricsAge.Seconds())
	fmt.Fprintf(w, "pig_kv_shadow_preemption_cooldown_seconds %.6f\n", input.Policy.PreemptionCooldown.Seconds())
	fmt.Fprintf(w, "pig_kv_shadow_decode_drift_tokens %d\n", input.Policy.DecodeDriftTokens)
	fmt.Fprintf(w, "pig_kv_shadow_reservation_ttl_seconds %.6f\n", input.Policy.ReservationTTL.Seconds())
	fmt.Fprintf(w, "pig_kv_estimator_min_bytes_per_token %d\n", input.Estimator.MinBytesPerToken)
	fmt.Fprintf(w, "pig_kv_estimator_max_bytes_per_token %d\n", input.Estimator.MaxBytesPerToken)
	fmt.Fprintf(w, "pig_kv_estimator_new_request_decode_tokens %d\n", input.Estimator.BlindOutputTokens)

	for _, reason := range []kvadmission.Reason{
		kvadmission.ReasonFit,
		kvadmission.ReasonOverBudget,
		kvadmission.ReasonEmergencyRed,
		kvadmission.ReasonBackendWaiting,
		kvadmission.ReasonPreemptionCooldown,
		kvadmission.ReasonStaleMetrics,
		kvadmission.ReasonCapacityUnknown,
		kvadmission.ReasonUnsupportedRequest,
	} {
		fmt.Fprintf(w, "pig_kv_shadow_decisions_total{decision=%q} %d\n", reason, input.Snapshot.Decisions[reason])
	}
	fmt.Fprintf(w, "pig_kv_shadow_reservations %d\n", input.Snapshot.Reservations)
	fmt.Fprintf(w, "pig_kv_shadow_unabsorbed_reservation_tokens %d\n", input.Snapshot.UnabsorbedTokens)
	fmt.Fprintf(w, "pig_kv_shadow_reservations_expired_total %d\n", input.Snapshot.ExpiredTotal)
	fmt.Fprintf(w, "pig_kv_shadow_reservations_released_total %d\n", input.Snapshot.ReleasedTotal)
	fmt.Fprintf(w, "pig_kv_shadow_duplicate_reservation_ids_total %d\n", input.Snapshot.DuplicateIDTotal)
	last := input.Snapshot.LastDecision
	fmt.Fprintf(w, "pig_kv_shadow_last_decision_info{decision=%q,backend=%q,kind=%q} 1\n", last.Reason, last.Backend, last.BackendKind)
	fmt.Fprintf(w, "pig_kv_shadow_last_projected_high_tokens %d\n", last.ProjectedHighTokens)
	fmt.Fprintf(w, "pig_kv_shadow_last_projected_ratio %.6f\n", last.ProjectedRatio)
	fmt.Fprintf(w, "pig_kv_shadow_last_estimated_input_low_tokens %d\n", last.EstimatedInputLow)
	fmt.Fprintf(w, "pig_kv_shadow_last_estimated_input_high_tokens %d\n", last.EstimatedInputHigh)

	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	for _, backend := range input.Snapshot.Backends {
		age := float64(0)
		if !backend.Updated.IsZero() {
			age = now.Sub(backend.Updated).Seconds()
			if age < 0 {
				age = 0
			}
		}
		fmt.Fprintf(w, "pig_kv_shadow_backend_epoch{name=%q,kind=%q} %d\n", backend.Name, backend.Kind, backend.Epoch)
		fmt.Fprintf(w, "pig_kv_shadow_backend_observed_tokens{name=%q} %d\n", backend.Name, backend.ObservedTokens)
		fmt.Fprintf(w, "pig_kv_shadow_backend_capacity_tokens{name=%q} %d\n", backend.Name, backend.CapacityTokens)
		fmt.Fprintf(w, "pig_kv_shadow_backend_unabsorbed_tokens{name=%q} %d\n", backend.Name, backend.UnabsorbedTokens)
		fmt.Fprintf(w, "pig_kv_shadow_backend_reservations{name=%q} %d\n", backend.Name, backend.Reservations)
		fmt.Fprintf(w, "pig_kv_shadow_backend_sample_age_seconds{name=%q} %.6f\n", backend.Name, age)
		fmt.Fprintf(w, "pig_kv_shadow_backend_resets_total{name=%q} %d\n", backend.Name, backend.ResetTotal)
		fmt.Fprintf(w, "pig_kv_shadow_backend_absorbed_tokens_total{name=%q} %d\n", backend.Name, backend.AbsorbedTokens)
		fmt.Fprintf(w, "pig_kv_shadow_backend_preemption_cooldown_active{name=%q} %d\n", backend.Name, num.BoolAsInt(now.Before(backend.CooldownUntil)))
	}
}

func writeKVShadowBudget(w io.Writer, kind kvadmission.BackendKind, budget kvadmission.Budget) {
	fmt.Fprintf(w, "pig_kv_shadow_budget_ratio{kind=%q,budget=%q} %.6f\n", kind, "target", budget.TargetRatio)
	fmt.Fprintf(w, "pig_kv_shadow_budget_ratio{kind=%q,budget=%q} %.6f\n", kind, "hard", budget.HardRatio)
	fmt.Fprintf(w, "pig_kv_shadow_budget_ratio{kind=%q,budget=%q} %.6f\n", kind, "emergency", budget.EmergencyRatio)
}
