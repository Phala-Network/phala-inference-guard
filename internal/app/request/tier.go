package request

import (
	"net/http"
	"strings"
)

// UserTierHeader is set by the trusted ingress for customer-tier handling.
const UserTierHeader = "X-User-Tier"

// TierValue returns the request's single tier header value, trimmed. Missing
// or duplicate headers return "" so an ambiguous request never gains a tier.
func TierValue(r *http.Request) string {
	if r == nil {
		return ""
	}
	values := r.Header.Values(UserTierHeader)
	if len(values) != 1 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
