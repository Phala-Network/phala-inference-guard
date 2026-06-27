package decision

import "testing"

func TestApplyCapacityLimit(t *testing.T) {
	tests := []struct {
		name  string
		input CapacityLimitInput
		want  int
	}{
		{
			name: "learned at current leaves current limit",
			input: CapacityLimitInput{
				CurrentLimit: 100,
				LearnedLimit: 100,
				TargetLimit:  120,
				LearnState:   "probe_up",
			},
			want: 100,
		},
		{
			name: "converge down wait holds previous active limit without pressure",
			input: CapacityLimitInput{
				CurrentLimit:  100,
				PreviousLimit: 50,
				LearnedLimit:  50,
				TargetLimit:   40,
				LearnState:    "converge_down_wait",
			},
			want: 50,
		},
		{
			name: "probe wait holds until load reaches learned limit",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          20,
				LearnedLimit:           20,
				TargetLimit:            60,
				LearnState:             "probe_wait",
				ProvisionalGrowthRatio: 0.10,
			},
			want: 20,
		},
		{
			name: "probe wait holds without demand pressure",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          20,
				LearnedLimit:           20,
				TargetLimit:            60,
				LearnState:             "probe_wait",
				ProvisionalGrowthRatio: 0.10,
			},
			want: 20,
		},
		{
			name: "demand pressure permits provisional recovery",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          20,
				LearnedLimit:           20,
				TargetLimit:            60,
				LearnState:             "probe_wait",
				DemandPressure:         true,
				ProvisionalGrowthRatio: 0.10,
			},
			want: 28,
		},
		{
			name: "sparse probe holds previous active limit without pressure",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          3,
				LearnedLimit:           3,
				TargetLimit:            4,
				LearnState:             "sparse_probe",
				ProvisionalGrowthRatio: 0.10,
			},
			want: 3,
		},
		{
			name: "sparse probe can recover under demand pressure",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          3,
				LearnedLimit:           3,
				TargetLimit:            4,
				LearnState:             "sparse_probe",
				DemandPressure:         true,
				ProvisionalGrowthRatio: 0.10,
			},
			want: 4,
		},
		{
			name: "hold underutilized preserves previous active limit",
			input: CapacityLimitInput{
				CurrentLimit:           100,
				PreviousLimit:          10,
				LearnedLimit:           10,
				TargetLimit:            50,
				LearnState:             "hold_underutilized",
				ProvisionalGrowthRatio: 0.25,
			},
			want: 10,
		},
		{
			name: "prefill transition freezes throughput active limit",
			input: CapacityLimitInput{
				CurrentLimit:      100,
				PreviousLimit:     30,
				LearnedLimit:      30,
				TargetLimit:       20,
				PrefillTransition: true,
			},
			want: 30,
		},
		{
			name: "prefill transition does not grow throughput active limit from observed TPS",
			input: CapacityLimitInput{
				CurrentLimit:      100,
				PreviousLimit:     30,
				LearnedLimit:      30,
				TargetLimit:       40,
				PrefillTransition: true,
			},
			want: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyCapacityLimit(tt.input)
			if got != tt.want {
				t.Fatalf("limit = %d, want %d", got, tt.want)
			}
		})
	}
}
