package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/tpscontrol"
)

func writeReport(w io.Writer) error {
	suite, err := tpscontrol.RunDefaultSuite()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(suite); err != nil {
		return fmt.Errorf("encode TPS controller simulation: %w", err)
	}
	return nil
}

func main() {
	if err := writeReport(os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pig TPS controller simulation: %v\n", err)
		os.Exit(1)
	}
}
