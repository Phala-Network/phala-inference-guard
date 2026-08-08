package request

import (
	"net/http"

	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

func (c *Classifier) AdmittedPath(r *http.Request) bool {
	return domainrequest.AdmittedPath(r, domainrequest.PathConfig{
		Paths:       c.cfg.Paths,
		SuffixMatch: c.cfg.SuffixMatch,
	})
}

func (c *Classifier) ClassifyRequest(r *http.Request) (Classification, *ProtocolError) {
	return c.classifyJSONFields(r)
}
