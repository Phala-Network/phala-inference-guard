package lexical

import (
	"math"
	"testing"
)

func TestAddRoundedRunRejectsInvalidAndOverflowingInputs(t *testing.T) {
	for _, test := range []struct {
		name          string
		total         *int64
		runBytes      int64
		bytesPerToken int64
	}{
		{name: "nil total", runBytes: 1, bytesPerToken: 4},
		{name: "negative run", total: new(int64), runBytes: -1, bytesPerToken: 4},
		{name: "zero divisor", total: new(int64), runBytes: 1},
		{name: "rounding overflow", total: new(int64), runBytes: math.MaxInt64, bytesPerToken: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			if addRoundedRun(test.total, test.runBytes, test.bytesPerToken) {
				t.Fatalf("accepted invalid rounded run: %+v", test)
			}
		})
	}

	total := int64(math.MaxInt64 - 3)
	if addRoundedRun(&total, 1, 4) {
		t.Fatal("accepted rounded run whose quarter-token sum overflows")
	}
	total = 0
	if !addRoundedRun(&total, 5, 4) || total != 8 {
		t.Fatalf("rounded five-byte run=%d want 8 quarter-token units", total)
	}
}
