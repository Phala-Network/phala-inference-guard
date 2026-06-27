package request

import (
	"net/http"

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

func (c *Classifier) ClassifyRequest(r *http.Request) (*lane.Lane, int, bool) {
	ln := c.classify(r)
	if ln == c.lanes.UnknownBody {
		return ln, 0, false
	}
	outputTokens, ok := c.classifyOutputTokens(r)
	if !ok {
		return ln, 0, false
	}
	c.observeOutputTokens(outputTokens)
	return requestclass.MoreRestrictiveLane(ln, c.outputLane(outputTokens)), outputTokens, true
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
