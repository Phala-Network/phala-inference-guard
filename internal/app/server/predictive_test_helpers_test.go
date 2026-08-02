package server

import (
	"sync"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type adapterTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *adapterTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *adapterTestClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	c.mu.Unlock()
}

func newAdapterTestCoordinatorWithTPSTarget(t *testing.T, userTPSTarget float64) *runtimepredictive.CountCoordinator {
	t.Helper()
	identity := adapterTestIdentity()
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity:                      identity,
		BaseCompletionTPS:             100,
		PrefillTPSPenaltyPerKToken:    0,
		BaseTTFT:                      10 * time.Millisecond,
		TTFTPerUncachedPrefillToken:   0,
		BaseTPOT:                      10 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 0,
		WorkspaceRiskUpper:            0,
		PreemptionRiskUpper:           0,
		Confidence:                    1,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity:                 identity,
		MinimumSamples:           3,
		MaximumSamplesPerCell:    8,
		MaximumCells:             64,
		MaxAge:                   time.Hour,
		LowerQuantile:            0.1,
		UpperQuantile:            0.9,
		MinimumTPSMultiplier:     0.2,
		MaximumTPSMultiplier:     1,
		MinimumLatencyMultiplier: 1,
		MaximumLatencyMultiplier: 2,
		CalibratedConfidence:     1,
		DecodeSequenceBucket:     1,
		ContextTokenBucket:       1,
		PrefillTokenBucket:       1,
		KVTokenBucket:            1,
	})
	if err != nil {
		t.Fatalf("new learned scheduler: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   "adapter-test-manifest",
			BackendEpoch: identity.BackendEpoch,
			Scheduler:    identity,
			BlockSize:    4,
		},
		ModelMaximumLength: 262_144,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard:       1_000,
			ActiveKVHard:         1_000,
			UserTPSTarget:        userTPSTarget,
			TPOTSLO:              time.Second,
			WorkspaceRiskBudget:  1,
			PreemptionRiskBudget: 1,
			MinimumConfidence:    1,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return coordinator
}

func adapterTestIdentity() runtimepredictive.ModelIdentity {
	return runtimepredictive.ModelIdentity{
		ProfileID:        "adapter-test-profile",
		BackendEpoch:     "adapter-test-epoch",
		PredictorVersion: "adapter-test-predictor-v1",
	}
}
