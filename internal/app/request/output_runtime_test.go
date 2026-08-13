package request

import "testing"

func TestAdaptiveOutputHonorsLiveDynamicEnable(t *testing.T) {
	enabled := true
	c := New(Config{
		AdaptiveOutput: true, AdaptiveOutputWindow: 4, AdaptiveOutputMin: 1,
		MediumOutputTokens: 100, LongOutputTokens: 200, VeryLongOutputTokens: 300,
		AdaptiveOutputMediumQ: 1, AdaptiveOutputLongQ: 1, AdaptiveOutputVeryQ: 1,
		AdaptiveOutputGreen: 1, AdaptiveOutputYellow: 1, AdaptiveOutputRed: 1,
		DynamicEnabled: true, DynamicFailsafeState: "red",
	}, Lanes{}, func() string { return "red" }, func() bool { return enabled })
	c.observeOutputTokens(1000)
	if got := c.EffectiveOutputThresholds(); got.Medium == 100 {
		t.Fatalf("adaptive thresholds not applied: %+v", got)
	}
	enabled = false
	if got := c.EffectiveOutputThresholds(); got.Medium != 100 || got.Long != 200 || got.VeryLong != 300 {
		t.Fatalf("disabled runtime policy still adaptive: %+v", got)
	}
}
