package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/simulation/kv"
)

func main() {
	scenarioPath := flag.String("scenario", "scenarios/kv-admission", "JSONL scenario file or directory")
	all := flag.Bool("all", false, "run every .jsonl file in the scenario directory")
	jsonOutput := flag.Bool("json", false, "write JSON results")
	performance := flag.Bool("performance", false, "measure and enforce estimator and shadow decision latency targets")
	flag.Parse()
	if *performance {
		result, err := kv.MeasurePerformance()
		if *jsonOutput {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(result)
		} else {
			fmt.Printf("performance estimator_64kib_p95=%s estimator_2mib_p99=%s shadow_decision_p99=%s samples=%d/%d/%d\n",
				result.Estimator64KiBP95, result.Estimator2MiBP99, result.ShadowDecisionP99, result.Estimator64KiBN, result.Estimator2MiBN, result.ShadowDecisionN)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*scenarioPath, *all, *jsonOutput); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, all, jsonOutput bool) error {
	files, err := scenarioFiles(path, all)
	if err != nil {
		return err
	}
	results := make([]kv.Result, 0, len(files))
	for _, file := range files {
		input, err := os.Open(file)
		if err != nil {
			return err
		}
		result, runErr := kv.Run(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), input)
		closeErr := input.Close()
		if runErr != nil {
			return runErr
		}
		if closeErr != nil {
			return closeErr
		}
		results = append(results, result)
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(results)
	}
	for _, result := range results {
		fmt.Printf("scenario=%s requests=%d shadow_fit=%d control_fit=%d hard_violations=%d control_hard_violations=%d reservations=%d improvement=%.2f%%\n",
			result.Name, result.Requests, result.ShadowFit, result.ControlFit, result.HardBudgetViolations, result.ControlHardBudgetViolations, result.FinalReservations, result.ImprovementPercent)
	}
	return nil
}

func scenarioFiles(path string, all bool) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	if !all {
		return nil, fmt.Errorf("%s is a directory; pass -all", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(path, entry.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no .jsonl scenarios found in %s", path)
	}
	return files, nil
}
