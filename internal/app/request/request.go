package request

import "net/http"

func (c *Classifier) ClassifyRequest(r *http.Request) (Classification, *ProtocolError) {
	return c.classifyJSONFields(r)
}
