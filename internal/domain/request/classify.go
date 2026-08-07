package request

import (
	"net/http"
	"strings"
)

type PathConfig struct {
	Paths       []string
	SuffixMatch bool
}

func AdmittedPath(r *http.Request, cfg PathConfig) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	for _, path := range cfg.Paths {
		if r.URL.Path == path || (cfg.SuffixMatch && strings.HasSuffix(r.URL.Path, path)) {
			return true
		}
	}
	return false
}
