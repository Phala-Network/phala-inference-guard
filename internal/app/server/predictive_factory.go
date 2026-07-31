package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive/nativeffi"
)

const predictiveProfileSchemaVersion = 1

const predictiveGemma4RendererVersion = "gemma4-text-v1"

const maximumPredictiveProfileBytes = 1024 * 1024

type predictiveProfileAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type predictiveProfileManifest struct {
	SchemaVersion                       int                    `json:"schema_version"`
	ManifestID                          string                 `json:"manifest_id"`
	ProfileID                           string                 `json:"profile_id"`
	PredictorVersion                    string                 `json:"predictor_version"`
	ServedModel                         string                 `json:"served_model"`
	ModelRevision                       string                 `json:"model_revision"`
	BackendKind                         string                 `json:"backend_kind"`
	BackendVersion                      string                 `json:"backend_version"`
	BackendSourceRevision               string                 `json:"backend_source_revision"`
	BackendImageDigest                  string                 `json:"backend_image_digest"`
	BackendEpoch                        string                 `json:"backend_epoch"`
	RendererVersion                     string                 `json:"renderer_version"`
	BOSToken                            string                 `json:"bos_token"`
	Tokenizer                           predictiveProfileAsset `json:"tokenizer"`
	TokenizerConfig                     predictiveProfileAsset `json:"tokenizer_config"`
	ChatTemplate                        predictiveProfileAsset `json:"chat_template"`
	CompletionAddSpecialTokens          *bool                  `json:"completion_add_special_tokens"`
	ChatAddSpecialTokens                *bool                  `json:"chat_add_special_tokens"`
	BlockSize                           int                    `json:"block_size"`
	ModelMaximumLength                  int64                  `json:"model_maximum_length"`
	MaximumKVTokens                     int64                  `json:"maximum_kv_tokens"`
	ProtectedKVTokens                   int64                  `json:"protected_kv_tokens"`
	DefaultDecodeHorizon                int64                  `json:"default_decode_horizon"`
	MaximumDecodeHorizon                int64                  `json:"maximum_decode_horizon"`
	BaseCompletionTPS                   float64                `json:"base_completion_tps"`
	PrefillTPSPenaltyPerKToken          float64                `json:"prefill_tps_penalty_per_k_token"`
	BaseTTFTMilliseconds                int64                  `json:"base_ttft_milliseconds"`
	TTFTPerPrefillTokenMicroseconds     int64                  `json:"ttft_per_prefill_token_microseconds"`
	BaseTPOTMilliseconds                int64                  `json:"base_tpot_milliseconds"`
	TPOTPerExistingSequenceMilliseconds int64                  `json:"tpot_per_existing_sequence_milliseconds"`
	WorkspaceRiskUpper                  float64                `json:"workspace_risk_upper"`
	PreemptionRiskUpper                 float64                `json:"preemption_risk_upper"`
	ProfileConfidence                   float64                `json:"profile_confidence"`
	UserTPSTarget                       float64                `json:"user_tps_target"`
	TTFTSLOMilliseconds                 int64                  `json:"ttft_slo_milliseconds"`
	TPOTSLOMilliseconds                 int64                  `json:"tpot_slo_milliseconds"`
	WorkspaceRiskBudget                 float64                `json:"workspace_risk_budget"`
	PreemptionRiskBudget                float64                `json:"preemption_risk_budget"`
	MinimumConfidence                   float64                `json:"minimum_confidence"`
	CalibratorMinimumSamples            int                    `json:"calibrator_minimum_samples"`
	CalibratorMaximumSamplesPerCell     int                    `json:"calibrator_maximum_samples_per_cell"`
	CalibratorMaxAgeSeconds             int64                  `json:"calibrator_max_age_seconds"`
	CalibratorLowerQuantile             float64                `json:"calibrator_lower_quantile"`
	CalibratorUpperQuantile             float64                `json:"calibrator_upper_quantile"`
	CalibratorMinimumTPSMultiplier      float64                `json:"calibrator_minimum_tps_multiplier"`
	CalibratorMaximumTPSMultiplier      float64                `json:"calibrator_maximum_tps_multiplier"`
	CalibratorMinimumLatencyMultiplier  float64                `json:"calibrator_minimum_latency_multiplier"`
	CalibratorMaximumLatencyMultiplier  float64                `json:"calibrator_maximum_latency_multiplier"`
	CalibratorConfidence                float64                `json:"calibrator_confidence"`
	CalibratorDecodeSequenceBucket      int                    `json:"calibrator_decode_sequence_bucket"`
	CalibratorContextTokenBucket        int64                  `json:"calibrator_context_token_bucket"`
	CalibratorPrefillTokenBucket        int64                  `json:"calibrator_prefill_token_bucket"`
	CalibratorKVTokenBucket             int64                  `json:"calibrator_kv_token_bucket"`
}

type loadedPredictiveProfile struct {
	manifest        predictiveProfileManifest
	tokenizerPath   string
	tokenizerConfig string
	chatTemplate    string
}

func newDefaultPredictiveShadow(cfg config) (predictiveAdmissionShadow, error) {
	profile, err := loadPredictiveProfile(cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
	if err != nil {
		return nil, err
	}
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             profile.manifest.BOSToken,
		DefaultDecodeHorizon: profile.manifest.DefaultDecodeHorizon,
		MaximumDecodeHorizon: profile.manifest.MaximumDecodeHorizon,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive renderer: %w", err)
	}
	identity := runtimepredictive.ModelIdentity{
		ProfileID:        profile.manifest.ProfileID,
		BackendEpoch:     profile.manifest.BackendEpoch,
		PredictorVersion: profile.manifest.PredictorVersion,
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity:                      identity,
		BaseCompletionTPS:             profile.manifest.BaseCompletionTPS,
		PrefillTPSPenaltyPerKToken:    profile.manifest.PrefillTPSPenaltyPerKToken,
		BaseTTFT:                      time.Duration(profile.manifest.BaseTTFTMilliseconds) * time.Millisecond,
		TTFTPerUncachedPrefillToken:   time.Duration(profile.manifest.TTFTPerPrefillTokenMicroseconds) * time.Microsecond,
		BaseTPOT:                      time.Duration(profile.manifest.BaseTPOTMilliseconds) * time.Millisecond,
		TPOTPerExistingDecodeSequence: time.Duration(profile.manifest.TPOTPerExistingSequenceMilliseconds) * time.Millisecond,
		WorkspaceRiskUpper:            profile.manifest.WorkspaceRiskUpper,
		PreemptionRiskUpper:           profile.manifest.PreemptionRiskUpper,
		Confidence:                    profile.manifest.ProfileConfidence,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity:                 identity,
		MinimumSamples:           profile.manifest.CalibratorMinimumSamples,
		MaximumSamplesPerCell:    profile.manifest.CalibratorMaximumSamplesPerCell,
		MaxAge:                   time.Duration(profile.manifest.CalibratorMaxAgeSeconds) * time.Second,
		LowerQuantile:            profile.manifest.CalibratorLowerQuantile,
		UpperQuantile:            profile.manifest.CalibratorUpperQuantile,
		MinimumTPSMultiplier:     profile.manifest.CalibratorMinimumTPSMultiplier,
		MaximumTPSMultiplier:     profile.manifest.CalibratorMaximumTPSMultiplier,
		MinimumLatencyMultiplier: profile.manifest.CalibratorMinimumLatencyMultiplier,
		MaximumLatencyMultiplier: profile.manifest.CalibratorMaximumLatencyMultiplier,
		CalibratedConfidence:     profile.manifest.CalibratorConfidence,
		DecodeSequenceBucket:     profile.manifest.CalibratorDecodeSequenceBucket,
		ContextTokenBucket:       profile.manifest.CalibratorContextTokenBucket,
		PrefillTokenBucket:       profile.manifest.CalibratorPrefillTokenBucket,
		KVTokenBucket:            profile.manifest.CalibratorKVTokenBucket,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive scheduler: %w", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   profile.manifest.ManifestID,
			BackendEpoch: profile.manifest.BackendEpoch,
			Scheduler:    identity,
			BlockSize:    profile.manifest.BlockSize,
		},
		ModelMaximumLength: profile.manifest.ModelMaximumLength,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard:       profile.manifest.ProtectedKVTokens,
			ActiveKVHard:         profile.manifest.ProtectedKVTokens,
			UserTPSTarget:        profile.manifest.UserTPSTarget,
			TTFTSLO:              time.Duration(profile.manifest.TTFTSLOMilliseconds) * time.Millisecond,
			TPOTSLO:              time.Duration(profile.manifest.TPOTSLOMilliseconds) * time.Millisecond,
			WorkspaceRiskBudget:  profile.manifest.WorkspaceRiskBudget,
			PreemptionRiskBudget: profile.manifest.PreemptionRiskBudget,
			MinimumConfidence:    profile.manifest.MinimumConfidence,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive coordinator: %w", err)
	}
	counter, err := nativeffi.OpenCounter(nativeffi.CounterConfig{
		TokenizerPath:              profile.tokenizerPath,
		ManifestID:                 profile.manifest.ManifestID,
		BackendEpoch:               profile.manifest.BackendEpoch,
		CompletionAddSpecialTokens: *profile.manifest.CompletionAddSpecialTokens,
		ChatAddSpecialTokens:       *profile.manifest.ChatAddSpecialTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("open predictive count-only tokenizer: %w", err)
	}
	if err := verifyPredictiveAsset(profile.tokenizerPath, profile.manifest.Tokenizer.SHA256); err != nil {
		_ = counter.Close()
		return nil, fmt.Errorf("reverify predictive tokenizer after open: %w", err)
	}
	metricsURL, err := predictiveVLLMMetricsURL(cfg)
	if err != nil {
		_ = counter.Close()
		return nil, err
	}
	pollInterval := cfg.DynamicPollInterval
	if pollInterval <= 0 || pollInterval > time.Duration(math.MaxInt64/3) {
		_ = counter.Close()
		return nil, fmt.Errorf("predictive metrics poll interval is invalid")
	}
	maximumAge := cfg.KVAdmissionPolicy.MaxMetricsAge
	if maximumAge <= 0 {
		maximumAge = 3 * pollInterval
	}
	preemptionCooldown := cfg.KVAdmissionPolicy.PreemptionCooldown
	if preemptionCooldown <= 0 {
		preemptionCooldown = maximumAge
	}
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:         metricsURL,
		MaximumKVTokens:    profile.manifest.MaximumKVTokens,
		PollInterval:       pollInterval,
		MaximumAge:         maximumAge,
		RequestTimeout:     2 * time.Second,
		PreemptionCooldown: preemptionCooldown,
		Coordinator:        coordinator,
	})
	if err != nil {
		_ = counter.Close()
		return nil, fmt.Errorf("construct predictive vLLM observer: %w", err)
	}
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
		Coordinator: coordinator,
		Upstream:    observer,
	})
	if err != nil {
		_ = observer.Close()
		_ = counter.Close()
		return nil, err
	}
	return adapter, nil
}

func loadPredictiveProfile(path, expectedSHA string) (loadedPredictiveProfile, error) {
	if strings.TrimSpace(path) == "" {
		return loadedPredictiveProfile{}, fmt.Errorf("PREDICTIVE_ADMISSION_PROFILE_PATH is required in shadow mode")
	}
	if err := validatePredictiveSHA256(expectedSHA); err != nil {
		return loadedPredictiveProfile{}, fmt.Errorf("PREDICTIVE_ADMISSION_PROFILE_SHA256: %w", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return loadedPredictiveProfile{}, fmt.Errorf("resolve predictive profile path: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return loadedPredictiveProfile{}, fmt.Errorf("stat predictive profile: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumPredictiveProfileBytes {
		return loadedPredictiveProfile{}, fmt.Errorf("predictive profile must be a non-empty regular file no larger than %d bytes", maximumPredictiveProfileBytes)
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return loadedPredictiveProfile{}, fmt.Errorf("read predictive profile: %w", err)
	}
	if got := predictiveSHA256(content); got != expectedSHA {
		clear(content)
		return loadedPredictiveProfile{}, fmt.Errorf("predictive profile SHA-256 mismatch")
	}
	if err := validateUniqueJSONKeys(content); err != nil {
		clear(content)
		return loadedPredictiveProfile{}, fmt.Errorf("predictive profile JSON: %w", err)
	}
	var manifest predictiveProfileManifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		clear(content)
		return loadedPredictiveProfile{}, fmt.Errorf("decode predictive profile: %w", err)
	}
	if err := requirePredictiveJSONEOF(decoder); err != nil {
		clear(content)
		return loadedPredictiveProfile{}, err
	}
	clear(content)
	if err := validatePredictiveProfile(manifest); err != nil {
		return loadedPredictiveProfile{}, err
	}
	base := filepath.Dir(absolutePath)
	loaded := loadedPredictiveProfile{
		manifest:        manifest,
		tokenizerPath:   resolvePredictiveAssetPath(base, manifest.Tokenizer.Path),
		tokenizerConfig: resolvePredictiveAssetPath(base, manifest.TokenizerConfig.Path),
		chatTemplate:    resolvePredictiveAssetPath(base, manifest.ChatTemplate.Path),
	}
	for _, asset := range []struct {
		name string
		path string
		sha  string
	}{
		{name: "tokenizer", path: loaded.tokenizerPath, sha: manifest.Tokenizer.SHA256},
		{name: "tokenizer config", path: loaded.tokenizerConfig, sha: manifest.TokenizerConfig.SHA256},
		{name: "chat template", path: loaded.chatTemplate, sha: manifest.ChatTemplate.SHA256},
	} {
		if err := verifyPredictiveAsset(asset.path, asset.sha); err != nil {
			return loadedPredictiveProfile{}, fmt.Errorf("verify predictive %s: %w", asset.name, err)
		}
	}
	return loaded, nil
}

func validatePredictiveProfile(profile predictiveProfileManifest) error {
	if profile.SchemaVersion != predictiveProfileSchemaVersion {
		return fmt.Errorf("predictive profile schema_version must be %d", predictiveProfileSchemaVersion)
	}
	for name, value := range map[string]string{
		"manifest_id":             profile.ManifestID,
		"profile_id":              profile.ProfileID,
		"predictor_version":       profile.PredictorVersion,
		"served_model":            profile.ServedModel,
		"model_revision":          profile.ModelRevision,
		"backend_version":         profile.BackendVersion,
		"backend_source_revision": profile.BackendSourceRevision,
		"backend_epoch":           profile.BackendEpoch,
		"bos_token":               profile.BOSToken,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("predictive profile %s is required", name)
		}
	}
	if profile.BackendKind != "vllm" {
		return fmt.Errorf("predictive profile backend_kind must be vllm")
	}
	if profile.RendererVersion != predictiveGemma4RendererVersion {
		return fmt.Errorf("predictive profile renderer_version must be %s", predictiveGemma4RendererVersion)
	}
	if !strings.HasPrefix(profile.BackendImageDigest, "sha256:") || validatePredictiveSHA256(strings.TrimPrefix(profile.BackendImageDigest, "sha256:")) != nil {
		return fmt.Errorf("predictive profile backend_image_digest must be a pinned sha256 digest")
	}
	for name, asset := range map[string]predictiveProfileAsset{
		"tokenizer":        profile.Tokenizer,
		"tokenizer_config": profile.TokenizerConfig,
		"chat_template":    profile.ChatTemplate,
	} {
		if strings.TrimSpace(asset.Path) == "" {
			return fmt.Errorf("predictive profile %s path is required", name)
		}
		if err := validatePredictiveSHA256(asset.SHA256); err != nil {
			return fmt.Errorf("predictive profile %s SHA-256: %w", name, err)
		}
	}
	if profile.CompletionAddSpecialTokens == nil || profile.ChatAddSpecialTokens == nil || !*profile.CompletionAddSpecialTokens || *profile.ChatAddSpecialTokens {
		return fmt.Errorf("predictive profile special-token policy must be completion=true and chat=false")
	}
	if profile.BlockSize <= 0 || profile.ModelMaximumLength <= 0 {
		return fmt.Errorf("predictive profile block size and model maximum length must be positive")
	}
	blockSize := int64(profile.BlockSize)
	if profile.MaximumKVTokens <= 0 || profile.ProtectedKVTokens <= 0 || profile.ProtectedKVTokens > profile.MaximumKVTokens || profile.MaximumKVTokens%blockSize != 0 || profile.ProtectedKVTokens%blockSize != 0 {
		return fmt.Errorf("predictive profile KV capacities must be positive, block-aligned, and protected <= maximum")
	}
	if profile.DefaultDecodeHorizon <= 0 || profile.MaximumDecodeHorizon < profile.DefaultDecodeHorizon || profile.MaximumDecodeHorizon > profile.ModelMaximumLength {
		return fmt.Errorf("predictive profile decode horizons are invalid")
	}
	for name, value := range map[string]int64{
		"base_ttft_milliseconds": profile.BaseTTFTMilliseconds,
		"base_tpot_milliseconds": profile.BaseTPOTMilliseconds,
		"ttft_slo_milliseconds":  profile.TTFTSLOMilliseconds,
		"tpot_slo_milliseconds":  profile.TPOTSLOMilliseconds,
	} {
		if err := validatePredictiveDuration(name, value, time.Millisecond); err != nil {
			return err
		}
	}
	if err := validatePredictiveDuration("calibrator_max_age_seconds", profile.CalibratorMaxAgeSeconds, time.Second); err != nil {
		return err
	}
	if profile.TTFTPerPrefillTokenMicroseconds < 0 || profile.TTFTPerPrefillTokenMicroseconds > math.MaxInt64/int64(time.Microsecond) {
		return fmt.Errorf("predictive profile ttft_per_prefill_token_microseconds is invalid")
	}
	if profile.TPOTPerExistingSequenceMilliseconds < 0 || profile.TPOTPerExistingSequenceMilliseconds > math.MaxInt64/int64(time.Millisecond) {
		return fmt.Errorf("predictive profile tpot_per_existing_sequence_milliseconds is invalid")
	}
	identity := runtimepredictive.ModelIdentity{ProfileID: profile.ProfileID, BackendEpoch: profile.BackendEpoch, PredictorVersion: profile.PredictorVersion}
	static := runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: profile.BaseCompletionTPS, PrefillTPSPenaltyPerKToken: profile.PrefillTPSPenaltyPerKToken,
		BaseTTFT: time.Duration(profile.BaseTTFTMilliseconds) * time.Millisecond, TTFTPerUncachedPrefillToken: time.Duration(profile.TTFTPerPrefillTokenMicroseconds) * time.Microsecond,
		BaseTPOT: time.Duration(profile.BaseTPOTMilliseconds) * time.Millisecond, TPOTPerExistingDecodeSequence: time.Duration(profile.TPOTPerExistingSequenceMilliseconds) * time.Millisecond,
		WorkspaceRiskUpper: profile.WorkspaceRiskUpper, PreemptionRiskUpper: profile.PreemptionRiskUpper, Confidence: profile.ProfileConfidence,
	}
	calibrator := runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: profile.CalibratorMinimumSamples, MaximumSamplesPerCell: profile.CalibratorMaximumSamplesPerCell,
		MaxAge: time.Duration(profile.CalibratorMaxAgeSeconds) * time.Second, LowerQuantile: profile.CalibratorLowerQuantile, UpperQuantile: profile.CalibratorUpperQuantile,
		MinimumTPSMultiplier: profile.CalibratorMinimumTPSMultiplier, MaximumTPSMultiplier: profile.CalibratorMaximumTPSMultiplier,
		MinimumLatencyMultiplier: profile.CalibratorMinimumLatencyMultiplier, MaximumLatencyMultiplier: profile.CalibratorMaximumLatencyMultiplier,
		CalibratedConfidence: profile.CalibratorConfidence, DecodeSequenceBucket: profile.CalibratorDecodeSequenceBucket,
		ContextTokenBucket: profile.CalibratorContextTokenBucket, PrefillTokenBucket: profile.CalibratorPrefillTokenBucket, KVTokenBucket: profile.CalibratorKVTokenBucket,
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(static, calibrator)
	if err != nil {
		return fmt.Errorf("predictive profile scheduler: %w", err)
	}
	constraints := domainpredictive.Constraints{
		PhysicalKVHard: profile.ProtectedKVTokens, ActiveKVHard: profile.ProtectedKVTokens, UserTPSTarget: profile.UserTPSTarget,
		TTFTSLO: time.Duration(profile.TTFTSLOMilliseconds) * time.Millisecond, TPOTSLO: time.Duration(profile.TPOTSLOMilliseconds) * time.Millisecond,
		WorkspaceRiskBudget: profile.WorkspaceRiskBudget, PreemptionRiskBudget: profile.PreemptionRiskBudget, MinimumConfidence: profile.MinimumConfidence,
	}
	if _, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity:           runtimepredictive.CoordinatorIdentity{ManifestID: profile.ManifestID, BackendEpoch: profile.BackendEpoch, Scheduler: identity, BlockSize: profile.BlockSize},
		ModelMaximumLength: profile.ModelMaximumLength, Constraints: constraints, Scheduler: scheduler,
	}); err != nil {
		return fmt.Errorf("predictive profile coordinator: %w", err)
	}
	return nil
}

func requirePredictiveJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("predictive profile contains a trailing JSON value")
		}
		return fmt.Errorf("decode predictive profile trailing data: %w", err)
	}
	return nil
}

func resolvePredictiveAssetPath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

func verifyPredictiveAsset(path, expectedSHA string) error {
	if err := validatePredictiveSHA256(expectedSHA); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("asset must be a non-empty regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}

func validatePredictiveSHA256(value string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("must be 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func predictiveSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func validatePredictiveDuration(name string, value int64, unit time.Duration) error {
	if value <= 0 || value > math.MaxInt64/int64(unit) {
		return fmt.Errorf("predictive profile %s is invalid", name)
	}
	return nil
}
