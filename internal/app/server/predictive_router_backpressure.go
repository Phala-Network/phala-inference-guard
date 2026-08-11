package server

import (
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveRouterBackpressureSnapshot struct {
	Active          bool
	Activation      uint64
	Scope           predictiveProtectionScope
	Reason          domainpredictive.Reason
	Source          runtimepredictive.PredictionSource
	ActivatedAt     time.Time
	MinimumRunning  int
	InspectCapacity int
	Activations     uint64
	LatestRejectAt  time.Time
}
