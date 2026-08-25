package kvadmission

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type crossTokenizerOracleManifest struct {
	SchemaVersion                     int                             `json:"schema_version"`
	Purpose                           string                          `json:"purpose"`
	ProductionRuntimeConsumesManifest bool                            `json:"production_runtime_consumes_manifest"`
	Tokenizers                        []crossTokenizerOracleTokenizer `json:"tokenizer_manifest"`
	Fixtures                          []crossTokenizerOracleFixture   `json:"fixtures"`
}

type crossTokenizerOracleTokenizer struct {
	Family         string `json:"family"`
	Model          string `json:"model"`
	Revision       string `json:"revision"`
	Source         string `json:"source"`
	Class          string `json:"class"`
	Fast           bool   `json:"fast"`
	VocabularySize int    `json:"vocabulary_size"`
}

type crossTokenizerOracleFixture struct {
	Name       string                         `json:"name"`
	Endpoint   string                         `json:"endpoint"`
	Kind       string                         `json:"kind"`
	Parameters crossTokenizerOracleParameters `json:"parameters"`
	Tags       []string                       `json:"tags"`
	BodyBytes  int                            `json:"body_bytes"`
	BodySHA256 string                         `json:"body_sha256"`
	Oracle     []crossTokenizerOracleCount    `json:"oracle"`
}

type crossTokenizerOracleParameters struct {
	Unit          string `json:"unit"`
	Repetitions   int    `json:"repetitions"`
	EscapeCJK     bool   `json:"escape_cjk"`
	Bytes         int    `json:"bytes"`
	MetadataBytes int    `json:"metadata_bytes"`
	Properties    int    `json:"properties"`
}

type crossTokenizerOracleCount struct {
	Family                     string `json:"family"`
	AggregateInputTokens       int64  `json:"aggregate_input_tokens"`
	MaximumSequenceInputTokens int64  `json:"maximum_sequence_input_tokens"`
	Method                     string `json:"method"`
}

func TestV01218CrossTokenizerOracleRejectsDangerousKVUnderestimate(t *testing.T) {
	manifest := loadCrossTokenizerOracleManifest(t)
	validateCrossTokenizerOracleManifest(t, manifest)
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			cost := crossTokenizerEstimate(t, fixture)
			for _, oracle := range fixture.Oracle {
				if cost.Estimate.KVReservationInputTokens < oracle.AggregateInputTokens {
					t.Errorf(
						"family=%s aggregate KV reservation=%d under oracle=%d selection=%d confidence=%s",
						oracle.Family,
						cost.Estimate.KVReservationInputTokens,
						oracle.AggregateInputTokens,
						cost.Estimate.SelectionInputTokens,
						cost.Estimate.InputEstimateConfidence,
					)
				}
				if cost.Estimate.MaximumSequenceKVReservationInputTokens <
					oracle.MaximumSequenceInputTokens {
					t.Errorf(
						"family=%s maximum-sequence KV reservation=%d under oracle=%d selection=%d",
						oracle.Family,
						cost.Estimate.MaximumSequenceKVReservationInputTokens,
						oracle.MaximumSequenceInputTokens,
						cost.Estimate.MaximumSequenceInputTokens,
					)
				}
				t.Logf(
					"family=%s oracle=%d/%d selection=%d/%d reservation=%d/%d confidence=%s method=%s",
					oracle.Family,
					oracle.AggregateInputTokens,
					oracle.MaximumSequenceInputTokens,
					cost.Estimate.SelectionInputTokens,
					cost.Estimate.MaximumSequenceInputTokens,
					cost.Estimate.KVReservationInputTokens,
					cost.Estimate.MaximumSequenceKVReservationInputTokens,
					cost.Estimate.InputEstimateConfidence,
					oracle.Method,
				)
			}
			if hasCrossTokenizerOracleTag(fixture.Tags, "exact") &&
				(cost.Estimate.SelectionInputTokens != 384 ||
					cost.Estimate.MaximumSequenceInputTokens != 256) {
				t.Fatalf("explicit token-array exactness changed: %+v", cost.Estimate)
			}
		})
	}
}

func crossTokenizerEstimate(t testing.TB, fixture crossTokenizerOracleFixture) Cost {
	t.Helper()
	body := crossTokenizerOracleBody(t, fixture)
	cost, valid := crossTokenizerEstimateBody(fixture.Endpoint, body)
	if !valid {
		t.Fatalf("semantic estimate unsupported: %+v", cost)
	}
	return cost
}

func crossTokenizerEstimateBody(endpoint string, body []byte) (Cost, bool) {
	fields, valid := domainrequest.ParseEndpointJSONFields(
		body,
		[]string{"max_tokens", "max_completion_tokens", "max_output_tokens"},
		domainrequest.EndpointForPath(endpoint),
	)
	if !valid || !fields.ShapeSupported {
		return Cost{}, false
	}
	cost := EstimateSemanticRequest(
		SemanticRequestShape{
			BodyBytes:       len(body),
			BasePromptCount: fields.BasePromptCount,
			DecodeSequences: fields.DecodeSequences,
			Aggregate:       crossTokenizerSemanticFeatures(fields.Aggregate),
			MaximumSequence: crossTokenizerSemanticFeatures(fields.MaximumSequence),
		},
		fields.OutputTokens,
		fields.HasOutputTokens,
		DefaultEstimatorConfig(),
	)
	return cost, cost.Supported
}

func loadCrossTokenizerOracleManifest(t testing.TB) crossTokenizerOracleManifest {
	t.Helper()
	data := []byte(crossTokenizerOracleManifestJSON)
	var manifest crossTokenizerOracleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode cross-tokenizer oracle: %v", err)
	}
	return manifest
}

//go:embed testdata/v01218_cross_tokenizer_oracle.json
var crossTokenizerOracleManifestJSON string

func validateCrossTokenizerOracleManifest(t testing.TB, manifest crossTokenizerOracleManifest) {
	t.Helper()
	if manifest.SchemaVersion != 1 || manifest.Purpose != "offline_cross_tokenizer_acceptance_only" ||
		manifest.ProductionRuntimeConsumesManifest || len(manifest.Tokenizers) != 4 ||
		len(manifest.Fixtures) < 10 {
		t.Fatalf("invalid oracle manifest header: %+v", manifest)
	}
	families := make(map[string]struct{}, len(manifest.Tokenizers))
	for _, tokenizer := range manifest.Tokenizers {
		if tokenizer.Family == "" || tokenizer.Model == "" || len(tokenizer.Revision) != 40 ||
			tokenizer.Source == "" || tokenizer.Class == "" || !tokenizer.Fast ||
			tokenizer.VocabularySize <= 0 {
			t.Fatalf("invalid tokenizer provenance: %+v", tokenizer)
		}
		if _, duplicate := families[tokenizer.Family]; duplicate {
			t.Fatalf("duplicate tokenizer family: %s", tokenizer.Family)
		}
		families[tokenizer.Family] = struct{}{}
	}
}

func crossTokenizerSemanticFeatures(features domainrequest.EndpointInputFeatures) SemanticInputFeatures {
	return SemanticInputFeatures{
		PromptBytes:            features.PromptBytes,
		TextBytes:              features.TextBytes,
		ToolSchemaBytes:        features.ToolSchemaBytes,
		MessageCount:           features.MessageCount,
		ToolCount:              features.ToolCount,
		ModalityCount:          features.ModalityCount,
		ApproximateInputTokens: features.ApproximateInputTokens,
		ExplicitPromptTokens:   features.ExplicitPromptTokens,
		Conservative:           features.Conservative,
	}
}

func crossTokenizerOracleBody(t testing.TB, fixture crossTokenizerOracleFixture) []byte {
	t.Helper()
	payload := crossTokenizerOraclePayload(t, fixture)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", fixture.Name, err)
	}
	if fixture.Parameters.EscapeCJK {
		body = []byte(strings.NewReplacer("中", `\u4e2d`, "文", `\u6587`).Replace(string(body)))
	}
	if len(body) != fixture.BodyBytes {
		t.Fatalf("body bytes=%d want=%d", len(body), fixture.BodyBytes)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	if digest != fixture.BodySHA256 {
		t.Fatalf("body SHA-256=%s want=%s", digest, fixture.BodySHA256)
	}
	return body
}

func crossTokenizerOraclePayload(t testing.TB, fixture crossTokenizerOracleFixture) map[string]any {
	t.Helper()
	parameters := fixture.Parameters
	switch fixture.Kind {
	case "chat_text":
		return crossTokenizerChatPayload(strings.Repeat(parameters.Unit, parameters.Repetitions))
	case "chat_entropy":
		return crossTokenizerChatPayload(crossTokenizerEntropyText(parameters.Bytes))
	case "chat_metadata":
		payload := crossTokenizerChatPayload("hello")
		payload["metadata"] = map[string]any{"trace": strings.Repeat("m", parameters.MetadataBytes)}
		return payload
	case "chat_tools":
		payload := crossTokenizerChatPayload("look up all requested fields")
		payload["tools"] = crossTokenizerToolSchema(parameters.Properties)
		return payload
	case "completion_batch":
		return map[string]any{
			"best_of":    3,
			"max_tokens": 128,
			"model":      "model-agnostic",
			"n":          2,
			"prompt": []string{
				strings.Repeat("plain completion prompt ", 64),
				strings.Repeat("中文补全", 128),
				strings.Repeat("func main() { return }\n", 64),
			},
			"suffix": strings.Repeat(" suffix", 32),
		}
	case "responses_visible":
		return map[string]any{
			"input":             "分析这段代码并返回 JSON: func main() { return }",
			"instructions":      "Answer with concise evidence.",
			"max_output_tokens": 256,
			"model":             "model-agnostic",
			"tools":             crossTokenizerToolSchema(parameters.Properties),
		}
	case "explicit_token_arrays":
		first := make([]int, 256)
		for index := range first {
			first[index] = index + 1
		}
		second := make([]int, 128)
		for index := range second {
			second[index] = index + 257
		}
		return map[string]any{
			"max_tokens": 64,
			"model":      "model-agnostic",
			"n":          2,
			"prompt":     [][]int{first, second},
		}
	default:
		t.Fatalf("unsupported oracle fixture kind: %s", fixture.Kind)
		return nil
	}
}

func crossTokenizerChatPayload(content string) map[string]any {
	return map[string]any{
		"max_tokens": 256,
		"messages": []map[string]any{{
			"content": content,
			"role":    "user",
		}},
		"model": "model-agnostic",
	}
}

func crossTokenizerToolSchema(properties int) []map[string]any {
	propertySchema := make(map[string]any, properties)
	for index := 0; index < properties; index++ {
		propertySchema[fmt.Sprintf("field_%05d", index)] = map[string]any{
			"description": fmt.Sprintf("requested property %05d", index),
			"type":        "string",
		}
	}
	return []map[string]any{{
		"function": map[string]any{
			"description": "Return all requested values.",
			"name":        "lookup",
			"parameters": map[string]any{
				"properties": propertySchema,
				"type":       "object",
			},
		},
		"type": "function",
	}}
}

func crossTokenizerEntropyText(size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var builder strings.Builder
	builder.Grow(size)
	for index := 0; index < size; index++ {
		builder.WriteByte(alphabet[(index*29+index/7)%len(alphabet)])
	}
	return builder.String()
}

func hasCrossTokenizerOracleTag(tags []string, candidate string) bool {
	for _, tag := range tags {
		if tag == candidate {
			return true
		}
	}
	return false
}
