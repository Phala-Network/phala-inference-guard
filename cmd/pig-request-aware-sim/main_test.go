package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWriteSimulationReportIsAcceptedAndDeterministic(t *testing.T) {
	var first bytes.Buffer
	if err := writeSimulationReport(&first); err != nil {
		t.Fatalf("first report: %v", err)
	}
	var second bytes.Buffer
	if err := writeSimulationReport(&second); err != nil {
		t.Fatalf("second report: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("fixed-seed JSON simulation report is not byte deterministic")
	}
	var report simulationReport
	if err := json.Unmarshal(first.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Acceptance != "passed" || report.Suite.ProductionPolicyCalls == 0 || len(report.Suite.Scenarios) == 0 {
		t.Fatalf("incomplete accepted report: %+v", report)
	}
}
