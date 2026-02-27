package report

import (
	"github.com/segmentio/encoding/json"
	"fmt"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

type JSONReport struct {
	Version       string                `json:"version"`
	Timestamp     string                `json:"timestamp"`
	Results       []types.AssertionResult `json:"results"`
	Summary       JSONSummary           `json:"summary"`
	TotalCost     float64               `json:"total_cost"`
	TotalDuration int64                 `json:"total_duration_ms"`
}

type JSONSummary struct {
	Total    int `json:"total"`
	Passed   int `json:"passed"`
	SoftFail int `json:"soft_fail"`
	HardFail int `json:"hard_fail"`
}

// GenerateJSONReport generates a structured JSON report from assertion results.
func GenerateJSONReport(results []types.AssertionResult, totalCost float64, totalDurationMS int64) ([]byte, error) {
	summary := JSONSummary{
		Total: len(results),
	}

	var totalCostSum float64
	for _, result := range results {
		totalCostSum += result.Cost

		switch result.Status {
		case types.StatusPass:
			summary.Passed++
		case types.StatusSoftFail:
			summary.SoftFail++
		case types.StatusHardFail:
			summary.HardFail++
		}
	}

	// Use provided totalCost if non-zero, otherwise use calculated sum
	if totalCost == 0 && totalCostSum > 0 {
		totalCost = totalCostSum
	}

	report := JSONReport{
		Version:       "1.0",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Results:       results,
		Summary:       summary,
		TotalCost:     totalCost,
		TotalDuration: totalDurationMS,
	}

	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return output, nil
}
