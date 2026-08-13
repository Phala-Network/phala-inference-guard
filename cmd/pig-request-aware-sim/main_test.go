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
	if report.Acceptance != "passed" || report.Suite.HistoricalBaselineRecords == 0 ||
		report.Suite.ControllerPolicyCalls == 0 || len(report.Suite.Scenarios) == 0 {
		t.Fatalf("incomplete accepted report: %+v", report)
	}
}

func TestSimulationReportUsesVersionIndependentCandidateKey(t *testing.T) {
	var output bytes.Buffer
	if err := writeSimulationReport(&output); err != nil {
		t.Fatalf("write report: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatalf("decode report fields: %v", err)
	}
	if _, exists := fields["candidate_aggregate"]; !exists {
		t.Fatal("current candidate field is missing")
	}
	if _, exists := fields["v0_12_10_historical_aggregate"]; !exists {
		t.Fatal("frozen v0.12.10 historical field is missing")
	}
	if _, exists := fields["v0_12_10_aggregate"]; exists {
		t.Fatal("historical release is still mislabeled as the current candidate")
	}
}
