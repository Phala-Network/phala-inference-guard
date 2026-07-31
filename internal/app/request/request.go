package request

import (
	"net/http"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

func (c *Classifier) AdmittedPath(r *http.Request) bool {
	return requestclass.AdmittedPath(r, requestclass.PathConfig{
		Paths:       c.cfg.QoSPaths,
		SuffixMatch: c.cfg.PathSuffixMatch,
	})
}

func (c *Classifier) WantsStreamingResponse(r *http.Request) bool {
	return c.AdmittedPath(r) && requestclass.WantsEventStream(r)
}

func (c *Classifier) SafeForEarlySSEBridge(r *http.Request, outputTokens int, hasOutputTokens bool) bool {
	return requestclass.SafeForEarlySSEBridge(r, c.cfg.VeryLongBodyBytes, c.cfg.VeryLongOutputTokens, outputTokens, hasOutputTokens)
}

type Classification struct {
	Lane                      *lane.Lane
	OutputTokens              int
	HasOutputTokens           bool
	Streaming                 bool
	KVCost                    kvadmission.Cost
	PredictiveBody            []byte
	PredictiveOutputTokens    int
	PredictiveHasOutputTokens bool
}

func (c *Classifier) ClassifyRequest(r *http.Request) Classification {
	result := Classification{
		Lane:      c.classify(r),
		Streaming: c.WantsStreamingResponse(r),
	}
	fields, kvCost, predictiveBody, ok := c.classifyJSONFields(r)
	result.KVCost = kvCost
	result.PredictiveBody = predictiveBody
	if !ok {
		return result
	}
	result.Streaming = result.Streaming || (fields.HasStream && fields.Stream)
	if fields.HasOutputTokens && c.cfg.PredictiveAdmissionMode == "shadow" {
		result.PredictiveOutputTokens = fields.OutputTokens
		result.PredictiveHasOutputTokens = true
	}
	if result.Lane == c.lanes.UnknownBody || !c.cfg.ClassifyOutputTokens || !fields.HasOutputTokens {
		return result
	}
	result.OutputTokens = fields.OutputTokens
	result.HasOutputTokens = true
	c.observeOutputTokens(result.OutputTokens)
	result.Lane = requestclass.MoreRestrictiveLane(result.Lane, c.outputLane(result.OutputTokens))
	return result
}

func (c *Classifier) classify(r *http.Request) *lane.Lane {
	if !c.AdmittedPath(r) {
		return c.lanes.Default
	}
	return requestclass.BodyLane(r, requestclass.BodyLanes{
		Default:  c.lanes.Default,
		Medium:   c.lanes.MediumBody,
		Long:     c.lanes.LongBody,
		VeryLong: c.lanes.VeryLongBody,
		Unknown:  c.lanes.UnknownBody,
	}, requestclass.BodyThresholds{
		Medium:   c.cfg.MediumBodyBytes,
		Long:     c.cfg.LongBodyBytes,
		VeryLong: c.cfg.VeryLongBodyBytes,
	})
}
