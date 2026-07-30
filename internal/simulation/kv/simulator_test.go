package kv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedScenarios(t *testing.T) {
	root := filepath.Join("..", "..", "..", "scenarios", "kv-admission")
	files, err := filepath.Glob(filepath.Join(root, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob scenarios: %v", err)
	}
	if len(files) < 12 {
		t.Fatalf("scenario count=%d want at least 12", len(files))
	}
	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			input, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			result, runErr := Run(filepath.Base(path), input)
			closeErr := input.Close()
			if runErr != nil {
				t.Fatalf("run: %v", runErr)
			}
			if closeErr != nil {
				t.Fatalf("close: %v", closeErr)
			}
			if result.HardBudgetViolations != 0 {
				t.Fatalf("hard violations=%d", result.HardBudgetViolations)
			}
		})
	}
}

func TestActualTokensExposeEstimatorUnderprediction(t *testing.T) {
	input := strings.NewReader(`
{"at_ms":0,"type":"config","control_limit":4,"decode_drift_tokens":0}
{"at_ms":0,"type":"sample","backend":"vllm-a","kind":"vllm","capacity_tokens":100000,"used_tokens":70000}
{"at_ms":1,"type":"request","id":"underestimated","estimate_low":1000,"estimate_high":2000,"actual_tokens":20000,"expect":"fit"}
{"at_ms":2,"type":"assert","expect_hard_violations":1,"expect_control_hard_violations":1}
`)
	result, err := Run("actual-underprediction", input)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.HardBudgetViolations != 1 || result.ControlHardBudgetViolations != 1 {
		t.Fatalf("violations shadow/control=%d/%d want 1/1", result.HardBudgetViolations, result.ControlHardBudgetViolations)
	}
}
