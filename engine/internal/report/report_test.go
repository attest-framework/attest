package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func TestGenerateJSONReport_AllPass(t *testing.T) {
	results := []types.AssertionResult{
		{
			AssertionID: "assert_001",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Schema validation passed",
			Cost:        0.01,
			DurationMS:  2,
		},
		{
			AssertionID: "assert_002",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Constraints satisfied",
			Cost:        0.01,
			DurationMS:  1,
		},
		{
			AssertionID: "assert_003",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Trace valid",
			Cost:        0.01,
			DurationMS:  1,
		},
	}

	output, err := GenerateJSONReport(results, 0.03, 4, ReportOptions{Version: ReportVersionV1})
	if err != nil {
		t.Fatalf("GenerateJSONReport failed: %v", err)
	}

	var report JSONReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("Failed to parse generated JSON: %v", err)
	}

	if report.Version != "1.0" {
		t.Errorf("Expected version '1.0', got %q", report.Version)
	}

	if report.Summary.Total != 3 {
		t.Errorf("Expected 3 total results, got %d", report.Summary.Total)
	}

	if report.Summary.Passed != 3 {
		t.Errorf("Expected 3 passed, got %d", report.Summary.Passed)
	}

	if report.Summary.SoftFail != 0 {
		t.Errorf("Expected 0 soft_fail, got %d", report.Summary.SoftFail)
	}

	if report.Summary.HardFail != 0 {
		t.Errorf("Expected 0 hard_fail, got %d", report.Summary.HardFail)
	}

	if report.TotalCost != 0.03 {
		t.Errorf("Expected total cost 0.03, got %f", report.TotalCost)
	}

	if report.TotalDuration != 4 {
		t.Errorf("Expected total duration 4ms, got %d", report.TotalDuration)
	}

	if len(report.Results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(report.Results))
	}
}

func TestGenerateJSONReport_WithFailures(t *testing.T) {
	results := []types.AssertionResult{
		{
			AssertionID: "assert_001",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Schema valid",
			Cost:        0.01,
			DurationMS:  2,
		},
		{
			AssertionID: "assert_002",
			Status:      types.StatusSoftFail,
			Score:       0.5,
			Explanation: "Soft constraint violation",
			Cost:        0.01,
			DurationMS:  1,
		},
		{
			AssertionID: "assert_003",
			Status:      types.StatusHardFail,
			Score:       0.0,
			Explanation: "Hard constraint violation",
			Cost:        0.01,
			DurationMS:  1,
		},
	}

	output, err := GenerateJSONReport(results, 0.03, 4, ReportOptions{Version: ReportVersionV1})
	if err != nil {
		t.Fatalf("GenerateJSONReport failed: %v", err)
	}

	var report JSONReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("Failed to parse generated JSON: %v", err)
	}

	if report.Summary.Total != 3 {
		t.Errorf("Expected 3 total, got %d", report.Summary.Total)
	}

	if report.Summary.Passed != 1 {
		t.Errorf("Expected 1 passed, got %d", report.Summary.Passed)
	}

	if report.Summary.SoftFail != 1 {
		t.Errorf("Expected 1 soft_fail, got %d", report.Summary.SoftFail)
	}

	if report.Summary.HardFail != 1 {
		t.Errorf("Expected 1 hard_fail, got %d", report.Summary.HardFail)
	}
}

func TestGenerateJSONReport_Golden_AllPass(t *testing.T) {
	results := []types.AssertionResult{
		{
			AssertionID: "assert_001",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Tool result for 'lookup_order' matches schema",
			Cost:        0.01,
			DurationMS:  2,
		},
		{
			AssertionID: "assert_002",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "All constraints satisfied",
			Cost:        0.01,
			DurationMS:  1,
		},
		{
			AssertionID: "assert_003",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Tool sequence found in order",
			Cost:        0.01,
			DurationMS:  1,
		},
	}

	output, err := GenerateJSONReport(results, 0.03, 4, ReportOptions{Version: ReportVersionV1})
	if err != nil {
		t.Fatalf("GenerateJSONReport failed: %v", err)
	}

	compareWithGoldenJSON(t, output, "testdata/golden/all_pass.json")
}

func TestGenerateJSONReport_Golden_Mixed(t *testing.T) {
	results := []types.AssertionResult{
		{
			AssertionID: "assert_001",
			Status:      types.StatusPass,
			Score:       1.0,
			Explanation: "Schema validation passed",
			Cost:        0.01,
			DurationMS:  2,
		},
		{
			AssertionID: "assert_002",
			Status:      types.StatusHardFail,
			Score:       0.0,
			Explanation: "metadata.cost_usd = 0.05 exceeds limit 0.01",
			Cost:        0.01,
			DurationMS:  1,
		},
		{
			AssertionID: "assert_003",
			Status:      types.StatusSoftFail,
			Score:       0.5,
			Explanation: "Tool sequence incomplete",
			Cost:        0.01,
			DurationMS:  1,
		},
	}

	output, err := GenerateJSONReport(results, 0.03, 4, ReportOptions{Version: ReportVersionV1})
	if err != nil {
		t.Fatalf("GenerateJSONReport failed: %v", err)
	}

	compareWithGoldenJSON(t, output, "testdata/golden/mixed.json")
}

// Helper functions
func compareWithGoldenJSON(t *testing.T, actual []byte, goldenPath string) {
	t.Helper()

	goldenFile := filepath.Join(".", goldenPath)
	golden, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("Failed to read golden file %q: %v", goldenPath, err)
	}

	// Parse both JSON objects for structural comparison
	var actualObj, goldenObj map[string]interface{}
	if err := json.Unmarshal(actual, &actualObj); err != nil {
		t.Fatalf("Failed to parse actual JSON: %v", err)
	}
	if err := json.Unmarshal(golden, &goldenObj); err != nil {
		t.Fatalf("Failed to parse golden JSON: %v", err)
	}

	// Replace timestamp with golden value for comparison (timestamps change)
	if ts, ok := goldenObj["timestamp"]; ok {
		actualObj["timestamp"] = ts
	}

	// Compare by re-marshaling with consistent formatting
	actualNorm, _ := json.MarshalIndent(actualObj, "", "  ")
	goldenNorm, _ := json.MarshalIndent(goldenObj, "", "  ")

	if !bytes.Equal(actualNorm, goldenNorm) {
		t.Errorf("JSON output does not match golden file %q\nExpected:\n%s\n\nGot:\n%s",
			goldenPath, string(goldenNorm), string(actualNorm))
	}
}
