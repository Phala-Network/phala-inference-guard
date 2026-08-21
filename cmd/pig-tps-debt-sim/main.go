package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/requestaware"
)

type simulationReport struct {
	Suite      requestaware.TPSDebtSuite            `json:"suite"`
	Acceptance requestaware.TPSDebtAcceptanceReport `json:"acceptance"`
}

func main() {
	suite, err := requestaware.RunTPSDebtSuite()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pig TPS debt simulation: %v\n", err)
		os.Exit(1)
	}
	acceptance, err := requestaware.ValidateTPSDebtAcceptance(suite)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "PIG TPS debt simulation acceptance: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(simulationReport{Suite: suite, Acceptance: acceptance}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encode TPS debt simulation: %v\n", err)
		os.Exit(1)
	}
}
