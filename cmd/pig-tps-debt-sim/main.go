package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/requestaware"
)

func main() {
	suite, err := requestaware.RunTPSDebtSuite()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pig TPS debt simulation: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(suite); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encode TPS debt simulation: %v\n", err)
		os.Exit(1)
	}
}

