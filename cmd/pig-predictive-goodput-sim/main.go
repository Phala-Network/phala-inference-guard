package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/goodput"
)

func main() {
	result, err := goodput.RunAcceptanceSuite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "run predictive goodput simulation: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode predictive goodput simulation: %v\n", err)
		os.Exit(1)
	}
}
