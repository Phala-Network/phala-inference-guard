package server

import (
	"fmt"
	"io"
	"sync/atomic"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

const requestFeatureEndpointCount = 3

var requestFeatureEndpointLabels = [requestFeatureEndpointCount]string{
	"chat_completions",
	"completions",
	"responses",
}

var requestFeatureLogprobsLabels = [domainrequest.LogprobsBucketCount]string{
	"none",
	"0",
	"1-5",
	"6-20",
	">20",
	"invalid",
}

var requestFeatureToolChoiceLabels = [domainrequest.ToolChoiceCount]string{
	"unspecified",
	"none",
	"auto",
	"required",
	"named",
	"other",
}

var requestFeatureToolsLabels = [2]string{"absent", "present"}

var requestFeatureResponseFormatLabels = [domainrequest.ResponseFormatCount]string{
	"unspecified",
	"text",
	"json_object",
	"json_schema",
	"structural_tag",
	"regex",
	"grammar",
	"choice",
	"other",
}

type requestFeatureEvidence struct {
	logprobs       [requestFeatureEndpointCount][domainrequest.LogprobsBucketCount]atomic.Uint64
	toolChoice     [requestFeatureEndpointCount][domainrequest.ToolChoiceCount]atomic.Uint64
	tools          [requestFeatureEndpointCount][2]atomic.Uint64
	responseFormat [requestFeatureEndpointCount][domainrequest.ResponseFormatCount]atomic.Uint64
}

type requestFeatureEvidenceSnapshot struct {
	logprobs       [requestFeatureEndpointCount][domainrequest.LogprobsBucketCount]uint64
	toolChoice     [requestFeatureEndpointCount][domainrequest.ToolChoiceCount]uint64
	tools          [requestFeatureEndpointCount][2]uint64
	responseFormat [requestFeatureEndpointCount][domainrequest.ResponseFormatCount]uint64
}

func (e *requestFeatureEvidence) Record(classification apprequest.Classification) {
	endpoint, ok := requestFeatureEndpointIndex(classification.Endpoint)
	if e == nil || !classification.JSONFieldsKnown || !ok {
		return
	}
	features := classification.Features
	if features.Logprobs < domainrequest.LogprobsBucketCount {
		e.logprobs[endpoint][features.Logprobs].Add(1)
	}
	if features.ToolChoice < domainrequest.ToolChoiceCount {
		e.toolChoice[endpoint][features.ToolChoice].Add(1)
	}
	tools := 0
	if features.ToolsPresent {
		tools = 1
	}
	e.tools[endpoint][tools].Add(1)
	if features.ResponseFormat < domainrequest.ResponseFormatCount {
		e.responseFormat[endpoint][features.ResponseFormat].Add(1)
	}
}

func (e *requestFeatureEvidence) Snapshot() requestFeatureEvidenceSnapshot {
	var snapshot requestFeatureEvidenceSnapshot
	if e == nil {
		return snapshot
	}
	for endpoint := range requestFeatureEndpointCount {
		for bucket := range e.logprobs[endpoint] {
			snapshot.logprobs[endpoint][bucket] = e.logprobs[endpoint][bucket].Load()
		}
		for choice := range e.toolChoice[endpoint] {
			snapshot.toolChoice[endpoint][choice] = e.toolChoice[endpoint][choice].Load()
		}
		for tools := range e.tools[endpoint] {
			snapshot.tools[endpoint][tools] = e.tools[endpoint][tools].Load()
		}
		for format := range e.responseFormat[endpoint] {
			snapshot.responseFormat[endpoint][format] = e.responseFormat[endpoint][format].Load()
		}
	}
	return snapshot
}

func writeRequestFeatureEvidenceMetrics(w io.Writer, snapshot requestFeatureEvidenceSnapshot) {
	for endpoint, endpointLabel := range requestFeatureEndpointLabels {
		for bucket, label := range requestFeatureLogprobsLabels {
			fmt.Fprintf(w, "pig_request_feature_logprobs_total{endpoint=%q,top_logprobs=%q} %d\n",
				endpointLabel, label, snapshot.logprobs[endpoint][bucket])
		}
		for choice, label := range requestFeatureToolChoiceLabels {
			fmt.Fprintf(w, "pig_request_feature_tool_choice_total{endpoint=%q,tool_choice=%q} %d\n",
				endpointLabel, label, snapshot.toolChoice[endpoint][choice])
		}
		for tools, label := range requestFeatureToolsLabels {
			fmt.Fprintf(w, "pig_request_feature_tools_total{endpoint=%q,tools=%q} %d\n",
				endpointLabel, label, snapshot.tools[endpoint][tools])
		}
		for format, label := range requestFeatureResponseFormatLabels {
			fmt.Fprintf(w, "pig_request_feature_response_format_total{endpoint=%q,format=%q} %d\n",
				endpointLabel, label, snapshot.responseFormat[endpoint][format])
		}
	}
}

func requestFeatureEndpointIndex(endpoint domainrequest.EndpointKind) (int, bool) {
	switch endpoint {
	case domainrequest.EndpointChatCompletions:
		return 0, true
	case domainrequest.EndpointCompletions:
		return 1, true
	case domainrequest.EndpointResponses:
		return 2, true
	default:
		return 0, false
	}
}
