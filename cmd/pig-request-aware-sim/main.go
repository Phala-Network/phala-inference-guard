package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/requestaware"
)

type simulationReport struct {
	Suite       requestaware.Suite   `json:"suite"`
	NoAdmission requestaware.Metrics `json:"no_admission_aggregate"`
	V0122       requestaware.Metrics `json:"v0_12_2_aggregate"`
	Candidate   requestaware.Metrics `json:"v0_12_3_aggregate"`
	Acceptance  string               `json:"acceptance"`
}

func buildSimulationReport() (simulationReport, error) {
	suite, err := requestaware.RunSuite()
	if err != nil {
		return simulationReport{}, err
	}
	if err := requestaware.ValidateAcceptance(suite); err != nil {
		return simulationReport{}, err
	}
	return simulationReport{
		Suite:       suite,
		NoAdmission: suite.Aggregate(requestaware.PolicyNoAdmission),
		V0122:       suite.Aggregate(requestaware.PolicyV0122),
		Candidate:   suite.Aggregate(requestaware.PolicyV0123),
		Acceptance:  "passed",
	}, nil
}

func writeSimulationReport(w io.Writer) error {
	report, err := buildSimulationReport()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode simulation report: %w", err)
	}
	return nil
}

func main() {
	if err := writeSimulationReport(os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pig request-aware simulation: %v\n", err)
		os.Exit(1)
	}
}
