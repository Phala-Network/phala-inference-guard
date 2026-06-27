package request

import (
	"net/http"
	"strings"
)

type Tier int

const (
	Basic Tier = iota
	Premium
)

const Header = "X-User-Tier"

func FromHeader(r *http.Request) Tier {
	if r == nil {
		return Basic
	}
	values := r.Header.Values(Header)
	if len(values) != 1 {
		return Basic
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "premium":
		return Premium
	default:
		return Basic
	}
}

func (t Tier) String() string {
	if t == Premium {
		return "premium"
	}
	return "basic"
}
