package predictive

import "testing"

func TestTokenCountAnalysisValidationBindsIdentityAndNonNegativeCount(t *testing.T) {
	valid := TokenCountAnalysis{
		ManifestID:       "manifest-a",
		BackendEpoch:     "epoch-a",
		ExactInputTokens: 42,
	}
	if err := valid.Validate("manifest-a", "epoch-a"); err != nil {
		t.Fatalf("valid analysis: %v", err)
	}

	tests := []struct {
		name         string
		analysis     TokenCountAnalysis
		manifestID   string
		backendEpoch string
	}{
		{name: "empty expected manifest", analysis: valid, backendEpoch: "epoch-a"},
		{name: "wrong manifest", analysis: valid, manifestID: "manifest-b", backendEpoch: "epoch-a"},
		{name: "empty expected epoch", analysis: valid, manifestID: "manifest-a"},
		{name: "wrong epoch", analysis: valid, manifestID: "manifest-a", backendEpoch: "epoch-b"},
		{name: "negative count", analysis: TokenCountAnalysis{ManifestID: "manifest-a", BackendEpoch: "epoch-a", ExactInputTokens: -1}, manifestID: "manifest-a", backendEpoch: "epoch-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.analysis.Validate(test.manifestID, test.backendEpoch); err == nil {
				t.Fatal("Validate unexpectedly succeeded")
			}
		})
	}
}
