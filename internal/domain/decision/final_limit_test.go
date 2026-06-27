package decision

import "testing"

func TestEnforceFinalLimit(t *testing.T) {
	tests := []struct {
		name           string
		overrideReason string
		components     []LimitComponent
		want           FinalLimit
	}{
		{
			name: "strict minimum wins",
			components: []LimitComponent{
				{Reason: "hard_global", Limit: 100},
				{Reason: "state", Limit: 80},
				{Reason: "throughput", Limit: 40},
				{Reason: "pressure", Limit: 50},
			},
			want: FinalLimit{Limit: 40, Reason: "throughput"},
		},
		{
			name: "zero component is failsafe winner",
			components: []LimitComponent{
				{Reason: "hard_global", Limit: 100},
				{Reason: "state", Limit: 80},
				{Reason: "availability", Limit: 0},
			},
			want: FinalLimit{Limit: 0, Reason: "availability"},
		},
		{
			name:           "override closes intake with explicit reason",
			overrideReason: "backend_waiting",
			components: []LimitComponent{
				{Reason: "hard_global", Limit: 100},
				{Reason: "pressure", Limit: 0},
			},
			want: FinalLimit{Limit: 0, Reason: "backend_waiting"},
		},
		{
			name:       "empty components fail closed",
			components: nil,
			want:       FinalLimit{Limit: 0, Reason: "none"},
		},
		{
			name: "blank component reason is normalized",
			components: []LimitComponent{
				{Limit: 0},
			},
			want: FinalLimit{Limit: 0, Reason: "none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnforceFinalLimit(tt.overrideReason, tt.components...)
			if got != tt.want {
				t.Fatalf("final limit = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnforceFinalLimitComponentsMatchesVariadicEntryPoint(t *testing.T) {
	components := []LimitComponent{
		{Reason: "hard_global", Limit: 128},
		{Reason: "ttft", Limit: 96},
		{Reason: "throughput", Limit: 104},
	}

	got := EnforceFinalLimitComponents("", components)
	want := EnforceFinalLimit("", components...)
	if got != want {
		t.Fatalf("slice entrypoint = %+v, variadic entrypoint = %+v", got, want)
	}
}
