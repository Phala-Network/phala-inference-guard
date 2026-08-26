package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWriteReportIsDeterministicDiagnosticJSON(t *testing.T) {
	var first bytes.Buffer
	if err := writeReport(&first); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := writeReport(&second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("simulation JSON is not byte deterministic")
	}
	var header struct {
		Contract  string `json:"contract"`
		Scenarios []any  `json:"scenarios"`
	}
	if err := json.Unmarshal(first.Bytes(), &header); err != nil {
		t.Fatal(err)
	}
	if header.Contract != "diagnostic_only" || len(header.Scenarios) == 0 {
		t.Fatalf("simulation report header=%+v", header)
	}
}
