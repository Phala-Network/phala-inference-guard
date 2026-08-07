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
	cost, protocolError := c.classifyJSONFields(r)
	if protocolError != nil {
		return Classification{}, protocolError
	}
	return Classification{Cost: cost}, nil
}
