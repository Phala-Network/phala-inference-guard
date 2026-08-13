package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
)

const admissionConfigPath = "/pig/admin/v1/admission-config"
const maxAdmissionConfigBody = 64 << 10

type admissionConfigDocument struct {
	Revision                  uint64  `json:"revision"`
	GlobalLimit               int     `json:"global_limit"`
	Enabled                   bool    `json:"enabled"`
	Enforce                   bool    `json:"enforce"`
	FailsafeState             string  `json:"failsafe_state"`
	KVYellow                  float64 `json:"kv_yellow"`
	KVRed                     float64 `json:"kv_red"`
	RunningYellow             int     `json:"running_yellow"`
	RunningRed                int     `json:"running_red"`
	WaitingYellow             int     `json:"waiting_yellow"`
	WaitingRed                int     `json:"waiting_red"`
	PreemptRed                uint64  `json:"preempt_red"`
	PressureEnabled           bool    `json:"pressure_enabled"`
	PressureHeadroom          int     `json:"pressure_headroom"`
	PressureMinLimit          int     `json:"pressure_min_limit"`
	PressureLearnRatio        float64 `json:"pressure_learn_ratio"`
	PressureLearnMinRunning   int     `json:"pressure_learn_min_running"`
	UserTPSEnabled            bool    `json:"user_tps_enabled"`
	UserTPSYellow             float64 `json:"user_tps_yellow"`
	UserTPSRed                float64 `json:"user_tps_red"`
	UserTPSMinRun             int     `json:"user_tps_min_running"`
	UserTPSYellowN            int     `json:"user_tps_yellow_consecutive"`
	UserTPSRedN               int     `json:"user_tps_red_consecutive"`
	UserTPSGraceMinSeconds    float64 `json:"user_tps_grace_min_seconds"`
	UserTPSGraceMaxSeconds    float64 `json:"user_tps_grace_max_seconds"`
	UserTPSGraceBps           float64 `json:"user_tps_grace_body_bytes_per_second"`
	UserTPSGraceMul           float64 `json:"user_tps_grace_multiplier"`
	TTFTEnabled               bool    `json:"ttft_enabled"`
	TTFTTargetSeconds         float64 `json:"ttft_target_seconds"`
	TTFTRedSeconds            float64 `json:"ttft_red_seconds"`
	TTFTP99TargetSeconds      float64 `json:"ttft_p99_target_seconds"`
	TTFTP99RedSeconds         float64 `json:"ttft_p99_red_seconds"`
	TTFTP99SignalWeight       float64 `json:"ttft_p99_signal_weight"`
	UserTPSCapacityRatio      float64 `json:"user_tps_capacity_ratio"`
	UserTPSCapacityRatioMax   float64 `json:"user_tps_capacity_ratio_max"`
	UserTPSCapacitySmoothing  float64 `json:"user_tps_capacity_smoothing"`
	UserTPSCapacityLearn      bool    `json:"user_tps_capacity_learn"`
	UserTPSCapacityStepUp     float64 `json:"user_tps_capacity_step_up"`
	UserTPSCapacityHealthyN   int     `json:"user_tps_capacity_healthy_n"`
	UserTPSCapacityHealthyMul float64 `json:"user_tps_capacity_healthy_multiplier"`
	GlobalGreen               int     `json:"global_green"`
	GlobalYellow              int     `json:"global_yellow"`
	GlobalRed                 int     `json:"global_red"`
}

func (s *proxyServer) admissionConfig(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeAdmissionError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.admissionMu.Lock()
		doc := admissionDocument(s.admissionRevision, s.dynamicController.AdmissionConfig())
		doc.GlobalLimit = s.globalLn.Limit()
		s.admissionMu.Unlock()
		writeAdmissionJSON(w, http.StatusOK, doc)
	case http.MethodPut:
		s.putAdmissionConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeAdmissionError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *proxyServer) adminAuthorized(r *http.Request) bool {
	token := s.cfg.AdminToken
	if token == "" {
		token = s.cfg.Token
	}
	values := r.Header.Values("Authorization")
	return token != "" && len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+token)) == 1
}

func (s *proxyServer) putAdmissionConfig(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAdmissionConfigBody+1))
	if err != nil {
		writeAdmissionError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if len(raw) > maxAdmissionConfigBody {
		writeAdmissionError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var doc admissionConfigDocument
	if err := decoder.Decode(&doc); err != nil {
		writeAdmissionError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeAdmissionError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAdmissionDocument(doc); err != nil {
		writeAdmissionError(w, http.StatusBadRequest, err.Error())
		return
	}
	if doc.Enabled && !s.dynamicController.HasMetricsSource() {
		writeAdmissionError(w, http.StatusBadRequest, "enabled requires at least one configured metrics source")
		return
	}

	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if doc.Revision != s.admissionRevision {
		writeAdmissionError(w, http.StatusConflict, fmt.Sprintf("revision conflict: current revision is %d", s.admissionRevision))
		return
	}
	cfg := s.dynamicController.AdmissionConfig()
	applyAdmissionDocument(&cfg, doc)
	// Serialize the hard-limit and policy swap with complete data-plane
	// admission decisions. Lowering the limit keeps inflight work draining.
	s.qosGate.UpdateAdmission(func() {
		s.globalLn.SetLimit(doc.GlobalLimit)
		s.dynamicController.SetAdmissionConfig(cfg)
	})
	s.qosGate.Notify()
	s.admissionRevision++
	response := admissionDocument(s.admissionRevision, cfg)
	response.GlobalLimit = s.globalLn.Limit()
	writeAdmissionJSON(w, http.StatusOK, response)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request body must contain exactly one JSON object")
	}
	return fmt.Errorf("invalid JSON: %w", err)
}

func validateAdmissionDocument(d admissionConfigDocument) error {
	if d.Revision == 0 {
		return errors.New("revision must be greater than zero")
	}
	if d.GlobalLimit < 0 {
		return errors.New("global_limit must be non-negative")
	}
	if !decision.ValidState(d.FailsafeState) {
		return errors.New("failsafe_state must be one of green, yellow, red")
	}
	finite := map[string]float64{
		"kv_yellow": d.KVYellow, "kv_red": d.KVRed, "pressure_learn_ratio": d.PressureLearnRatio,
		"user_tps_yellow": d.UserTPSYellow, "user_tps_red": d.UserTPSRed,
		"user_tps_grace_min_seconds": d.UserTPSGraceMinSeconds, "user_tps_grace_max_seconds": d.UserTPSGraceMaxSeconds,
		"user_tps_grace_body_bytes_per_second": d.UserTPSGraceBps, "user_tps_grace_multiplier": d.UserTPSGraceMul,
		"ttft_target_seconds": d.TTFTTargetSeconds, "ttft_red_seconds": d.TTFTRedSeconds,
		"ttft_p99_target_seconds": d.TTFTP99TargetSeconds, "ttft_p99_red_seconds": d.TTFTP99RedSeconds,
		"ttft_p99_signal_weight": d.TTFTP99SignalWeight, "user_tps_capacity_ratio": d.UserTPSCapacityRatio,
		"user_tps_capacity_ratio_max": d.UserTPSCapacityRatioMax, "user_tps_capacity_smoothing": d.UserTPSCapacitySmoothing,
		"user_tps_capacity_step_up": d.UserTPSCapacityStepUp, "user_tps_capacity_healthy_multiplier": d.UserTPSCapacityHealthyMul,
	}
	for name, value := range finite {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be finite", name)
		}
	}
	if d.KVYellow < 0 || d.KVRed < d.KVYellow {
		return errors.New("KV thresholds must be non-negative and increasing")
	}
	if d.RunningYellow < 0 || d.RunningRed < d.RunningYellow {
		return errors.New("running thresholds must be non-negative and increasing")
	}
	if d.WaitingYellow < 0 || d.WaitingRed < d.WaitingYellow {
		return errors.New("waiting thresholds must be non-negative and increasing")
	}
	if d.PressureHeadroom < 0 || d.PressureMinLimit < 0 || d.PressureLearnMinRunning < 0 {
		return errors.New("pressure counts/limits must be non-negative")
	}
	if d.PressureEnabled && (d.PressureLearnRatio <= 0 || d.PressureLearnRatio > 1) {
		return errors.New("pressure_learn_ratio must be > 0 and <= 1")
	}
	if d.UserTPSEnabled && (d.UserTPSYellow <= 0 || d.UserTPSRed <= 0 || d.UserTPSRed > d.UserTPSYellow) {
		return errors.New("user TPS thresholds must be > 0 and red <= yellow")
	}
	if d.UserTPSEnabled && (d.UserTPSMinRun <= 0 || d.UserTPSYellowN <= 0 || d.UserTPSRedN <= 0) {
		return errors.New("user TPS running/consecutive counts must be greater than zero")
	}
	if d.UserTPSGraceMinSeconds < 0 || d.UserTPSGraceMaxSeconds < d.UserTPSGraceMinSeconds {
		return errors.New("user TPS grace must satisfy 0 <= min <= max")
	}
	maxDurationSeconds := float64(^uint64(0)>>1) / float64(time.Second)
	if d.UserTPSGraceMinSeconds > maxDurationSeconds || d.UserTPSGraceMaxSeconds > maxDurationSeconds {
		return errors.New("user TPS grace exceeds time.Duration")
	}
	if d.UserTPSEnabled && (d.UserTPSGraceBps <= 0 || d.UserTPSGraceMul <= 0) {
		return errors.New("user TPS grace rate and multiplier must be greater than zero")
	}
	if d.TTFTEnabled && (d.TTFTTargetSeconds <= 0 || d.TTFTRedSeconds < d.TTFTTargetSeconds || d.TTFTP99TargetSeconds <= 0 || d.TTFTP99RedSeconds < d.TTFTP99TargetSeconds) {
		return errors.New("TTFT thresholds must be > 0 and red >= target")
	}
	if d.TTFTEnabled && (d.TTFTP99SignalWeight <= 0 || d.TTFTP99SignalWeight > 1) {
		return errors.New("ttft_p99_signal_weight must be > 0 and <= 1")
	}
	if d.GlobalGreen < 0 || d.GlobalYellow < 0 || d.GlobalRed < 0 {
		return errors.New("dynamic global limits must be non-negative")
	}
	if d.GlobalGreen > d.GlobalLimit || d.GlobalYellow > d.GlobalLimit || d.GlobalRed > d.GlobalLimit {
		return fmt.Errorf("dynamic global limits must not exceed global_limit (%d)", d.GlobalLimit)
	}
	if d.GlobalYellow > d.GlobalGreen || d.GlobalRed > d.GlobalYellow {
		return errors.New("global limits must satisfy red <= yellow <= green")
	}
	if d.UserTPSCapacityRatio <= 0 || d.UserTPSCapacityRatio > 1 {
		return errors.New("user_tps_capacity_ratio must be > 0 and <= 1")
	}
	if d.UserTPSCapacityRatioMax < d.UserTPSCapacityRatio || d.UserTPSCapacityRatioMax > 1 {
		return errors.New("user_tps_capacity_ratio_max must be >= ratio and <= 1")
	}
	if d.UserTPSCapacitySmoothing < 0 || d.UserTPSCapacitySmoothing >= 1 {
		return errors.New("user_tps_capacity_smoothing must be >= 0 and < 1")
	}
	if d.UserTPSCapacityStepUp <= 0 || d.UserTPSCapacityStepUp > 1 {
		return errors.New("user_tps_capacity_step_up must be > 0 and <= 1")
	}
	if d.UserTPSCapacityHealthyN <= 0 {
		return errors.New("user_tps_capacity_healthy_n must be greater than zero")
	}
	if d.UserTPSCapacityHealthyMul < 1 {
		return errors.New("user_tps_capacity_healthy_multiplier must be >= 1")
	}
	return nil
}

func admissionDocument(revision uint64, c dynamic.Config) admissionConfigDocument {
	p := c.TTFTPolicy.Normalize()
	return admissionConfigDocument{Revision: revision, Enabled: c.Enabled, Enforce: c.Enforce, FailsafeState: c.FailsafeState,
		KVYellow: c.KVYellow, KVRed: c.KVRed, RunningYellow: c.RunningYellow, RunningRed: c.RunningRed, WaitingYellow: c.WaitingYellow, WaitingRed: c.WaitingRed, PreemptRed: c.PreemptRed,
		PressureEnabled: c.PressureEnabled, PressureHeadroom: c.PressureHeadroom, PressureMinLimit: c.PressureMinLimit, PressureLearnRatio: c.PressureLearnRatio, PressureLearnMinRunning: c.PressureLearnMinRunning,
		UserTPSEnabled: c.UserTPSEnabled, UserTPSYellow: c.UserTPSYellow, UserTPSRed: c.UserTPSRed, UserTPSMinRun: c.UserTPSMinRun, UserTPSYellowN: c.UserTPSYellowN, UserTPSRedN: c.UserTPSRedN,
		UserTPSGraceMinSeconds: c.UserTPSGraceMin.Seconds(), UserTPSGraceMaxSeconds: c.UserTPSGraceMax.Seconds(), UserTPSGraceBps: c.UserTPSGraceBps, UserTPSGraceMul: c.UserTPSGraceMul,
		TTFTEnabled: c.TTFTEnabled, TTFTTargetSeconds: p.TargetSeconds, TTFTRedSeconds: p.RedSeconds, TTFTP99TargetSeconds: p.P99TargetSeconds, TTFTP99RedSeconds: p.P99RedSeconds, TTFTP99SignalWeight: p.P99SignalWeight,
		UserTPSCapacityRatio: c.UserTPSCapacityRatio, UserTPSCapacityRatioMax: c.UserTPSCapacityRatioMax, UserTPSCapacitySmoothing: c.UserTPSCapacitySmoothing, UserTPSCapacityLearn: c.UserTPSCapacityLearn, UserTPSCapacityStepUp: c.UserTPSCapacityStepUp, UserTPSCapacityHealthyN: c.UserTPSCapacityHealthyN, UserTPSCapacityHealthyMul: c.UserTPSCapacityHealthyMul,
		GlobalGreen: c.GlobalGreen, GlobalYellow: c.GlobalYellow, GlobalRed: c.GlobalRed}
}

func applyAdmissionDocument(c *dynamic.Config, d admissionConfigDocument) {
	c.Enabled = d.Enabled
	c.Enforce = d.Enforce
	c.FailsafeState = d.FailsafeState
	c.KVYellow = d.KVYellow
	c.KVRed = d.KVRed
	c.RunningYellow = d.RunningYellow
	c.RunningRed = d.RunningRed
	c.WaitingYellow = d.WaitingYellow
	c.WaitingRed = d.WaitingRed
	c.PreemptRed = d.PreemptRed
	c.PressureEnabled = d.PressureEnabled
	c.PressureHeadroom = d.PressureHeadroom
	c.PressureMinLimit = d.PressureMinLimit
	c.PressureLearnRatio = d.PressureLearnRatio
	c.PressureLearnMinRunning = d.PressureLearnMinRunning
	c.UserTPSEnabled = d.UserTPSEnabled
	c.UserTPSYellow = d.UserTPSYellow
	c.UserTPSRed = d.UserTPSRed
	c.UserTPSMinRun = d.UserTPSMinRun
	c.UserTPSYellowN = d.UserTPSYellowN
	c.UserTPSRedN = d.UserTPSRedN
	c.UserTPSGraceMin = time.Duration(d.UserTPSGraceMinSeconds * float64(time.Second))
	c.UserTPSGraceMax = time.Duration(d.UserTPSGraceMaxSeconds * float64(time.Second))
	c.UserTPSGraceBps = d.UserTPSGraceBps
	c.UserTPSGraceMul = d.UserTPSGraceMul
	c.TTFTEnabled = d.TTFTEnabled
	c.TTFTPolicy = latency.Policy{TargetSeconds: d.TTFTTargetSeconds, RedSeconds: d.TTFTRedSeconds, P99TargetSeconds: d.TTFTP99TargetSeconds, P99RedSeconds: d.TTFTP99RedSeconds, P99SignalWeight: d.TTFTP99SignalWeight}
	c.UserTPSCapacityRatio = d.UserTPSCapacityRatio
	c.UserTPSCapacityRatioMax = d.UserTPSCapacityRatioMax
	c.UserTPSCapacitySmoothing = d.UserTPSCapacitySmoothing
	c.UserTPSCapacityLearn = d.UserTPSCapacityLearn
	c.UserTPSCapacityStepUp = d.UserTPSCapacityStepUp
	c.UserTPSCapacityHealthyN = d.UserTPSCapacityHealthyN
	c.UserTPSCapacityHealthyMul = d.UserTPSCapacityHealthyMul
	c.GlobalGreen = d.GlobalGreen
	c.GlobalYellow = d.GlobalYellow
	c.GlobalRed = d.GlobalRed
}

func writeAdmissionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeAdmissionError(w http.ResponseWriter, status int, message string) {
	writeAdmissionJSON(w, status, struct {
		Error string `json:"error"`
	}{message})
}
