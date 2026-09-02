package request

import (
	"net/http"
	"strings"
)

// UserTierHeader is set by the trusted ingress for customer-tier handling.
const UserTierHeader = "X-User-Tier"

// IsPremiumTier reports true only for one unambiguous premium header value.
// Missing, duplicate, and unknown values stay on the normal admission path.
func IsPremiumTier(r *http.Request) bool {
	if r == nil {
		return false
	}
	values := r.Header.Values(UserTierHeader)
	return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "premium")
}
