package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// allLayersFixture returns a result per layer 1–8, alternating pass /
// soft / hard so the renderer must handle every status × layer
// combination in one snapshot.
func allLayersFixture() []types.AssertionResult {
	return []types.AssertionResult{
		{
			AssertionID: "schema_001", Type: types.TypeSchema, Layer: 1,
			Status: types.StatusPass, Score: 1.0,
			Explanation: "schema valid", TraceNodePath: "output",
			Expected: "matches JSON Schema", Actual: `{"ok":true}`,
		},
		{
			AssertionID: "constraint_001", Type: types.TypeConstraint, Layer: 2,
			Status: types.StatusHardFail, Score: 0.0,
			Explanation:   "metadata.cost_usd = 0.05, constraint lt 0.01 — constraint not satisfied.",
			TraceNodePath: "metadata.cost_usd",
			Expected:      "metadata.cost_usd lt 0.01", Actual: "metadata.cost_usd = 0.05",
			SuggestedAction: "Reduce model cost or widen the budget.",
		},
		{
			AssertionID: "trace_001", Type: types.TypeTrace, Layer: 3,
			Status: types.StatusSoftFail, Score: 0.5,
			Explanation: "tool sequence incomplete", TraceNodePath: "trace.contains_in_order",
			Expected: "contains_in_order [search, summarize]", Actual: "[search]",
		},
		{
			AssertionID: "tracetree_001", Type: types.TypeTraceTree, Layer: 3,
			Status: types.StatusPass, Score: 1.0,
			Explanation: "trace_tree depth ok", TraceNodePath: "trace.depth_lte",
		},
		{
			AssertionID: "content_001", Type: types.TypeContent, Layer: 4,
			Status: types.StatusHardFail, Score: 0.0,
			Explanation:     "output.message does not contain 'thanks'.",
			TraceNodePath:   "output.message",
			Expected:        `contains "thanks"`,
			Actual:          "Hello world.",
			Cost:            0,
			DurationMS:      2,
			RequestID:       "req_001",
			SuggestedAction: "Update prompt or remove assertion if contract changed.",
		},
		{
			AssertionID: "embedding_001", Type: types.TypeEmbedding, Layer: 5,
			Status: types.StatusSoftFail, Score: 0.62,
			Explanation:   "cosine similarity 0.6200 < threshold 0.8000",
			TraceNodePath: "output.summary", Cost: 0.0001, DurationMS: 12,
			Expected: "cosine_similarity(target, reference) >= 0.8000",
			Actual:   "cosine_similarity = 0.6200 against \"reference summary\"",
		},
		{
			AssertionID: "judge_001", Type: types.TypeLLMJudge, Layer: 6,
			Status: types.StatusHardFail, Score: 0.4,
			Explanation:   "Run 1: weak | Run 2: weak | Run 3: ok | Median selected.",
			TraceNodePath: "output.answer",
			Expected:      "judge_score >= 0.80 against rubric \"correctness\"",
			Actual:        "judge_score=0.40, model=gpt-4.1, rationale=weak",
			Cost:          0.012, DurationMS: 1820,
			Judge: &types.JudgeMetadata{
				Model:            "gpt-4.1",
				RubricName:       "correctness",
				RubricVersion:    "v3",
				PromptHash:       "abcd1234",
				SampleScores:     []float64{0.4, 0.4, 0.6},
				ScoreMean:        0.466,
				ScoreStddev:      0.115,
				HighVarianceFlag: false,
			},
			SuggestedAction: "Calibrate judge or refine rubric.",
		},
		{
			AssertionID: "tracetree_l7", Type: types.TypeTraceTree, Layer: 7,
			Status: types.StatusPass, Score: 1.0,
			Explanation: "agent hierarchy depth=3 within budget",
		},
		{
			AssertionID: "plugin_001", Type: types.TypePlugin, Layer: 8,
			Status: types.StatusPass, Score: 1.0,
			Explanation:   "plugin pii_scan no findings",
			TraceNodePath: "output", Cost: 0.001, DurationMS: 7,
		},
	}
}

func TestGenerateMarkdown_Hierarchical_AllLayers(t *testing.T) {
	results := allLayersFixture()
	var buf bytes.Buffer
	r := &MarkdownReport{
		Title:      "All-layer snapshot",
		RunAt:      time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Results:    results,
		TotalCost:  0.0131,
		DurationMS: 1841,
	}
	if err := GenerateMarkdown(&buf, r); err != nil {
		t.Fatalf("GenerateMarkdown: %v", err)
	}
	out := buf.String()

	// Summary block must include latency percentiles + cost.
	for _, want := range []string{
		"### Summary",
		"| Total |",
		"| Cost | $0.01",
		"| Latency P50 |",
		"| Latency P95 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Failure section must contain expected/actual + suggested action.
	for _, want := range []string{
		"### Failures",
		"`constraint_001`",
		"**Trace path:** `metadata.cost_usd`",
		"**Expected:** metadata.cost_usd lt 0.01",
		"**Actual:** metadata.cost_usd = 0.05",
		"**Suggested action:** Reduce model cost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("failure block missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Judge metadata block.
	for _, want := range []string{
		"**Judge model:** `gpt-4.1`",
		"**Rubric:** correctness @ v3",
		"**Prompt hash:** `abcd1234`",
		"**Sample scores:** [0.40, 0.40, 0.60]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("judge block missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Layer breakdown headers.
	for _, name := range []string{
		"#### Layer 1 (schema)",
		"#### Layer 2 (constraint)",
		"#### Layer 3 (trace)",
		"#### Layer 4 (content)",
		"#### Layer 5 (embedding)",
		"#### Layer 6 (llm_judge)",
		"#### Layer 7 (trace_tree)",
		"#### Layer 8 (plugin)",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("layer breakdown missing %q\n--- output ---\n%s", name, out)
		}
	}
}

func TestGenerateMarkdown_NoFailuresNoSection(t *testing.T) {
	results := []types.AssertionResult{
		{AssertionID: "ok", Status: types.StatusPass, Score: 1.0, Type: types.TypeSchema, Layer: 1},
	}
	var buf bytes.Buffer
	if err := GenerateMarkdown(&buf, &MarkdownReport{Results: results}); err != nil {
		t.Fatalf("GenerateMarkdown: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "### Failures") {
		t.Errorf("unexpected Failures section on all-pass run:\n%s", out)
	}
}

func TestGenerateJSONReport_V2_Default(t *testing.T) {
	results := allLayersFixture()
	output, err := GenerateJSONReport(results, 0.0131, 1841)
	if err != nil {
		t.Fatalf("GenerateJSONReport v2 default: %v", err)
	}

	var report JSONReportV2
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("Failed to parse v2 report: %v", err)
	}

	if report.ReportVersion != 2 {
		t.Errorf("expected report_version 2, got %d", report.ReportVersion)
	}
	if report.Summary.Total != len(results) {
		t.Errorf("summary.total = %d, want %d", report.Summary.Total, len(results))
	}
	if len(report.Layers) == 0 {
		t.Errorf("expected layer summaries, got none")
	}

	layerByID := make(map[int]LayerSummary)
	for _, l := range report.Layers {
		layerByID[l.Layer] = l
	}
	for _, layer := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		if _, ok := layerByID[layer]; !ok {
			t.Errorf("layer %d missing from layer summaries", layer)
		}
	}
}

func TestGenerateJSONReport_V1_Backcompat(t *testing.T) {
	results := []types.AssertionResult{
		{AssertionID: "a", Status: types.StatusPass, Score: 1.0, Cost: 0.01, DurationMS: 2},
	}
	output, err := GenerateJSONReport(results, 0.01, 2, ReportOptions{Version: ReportVersionV1})
	if err != nil {
		t.Fatalf("v1 generate: %v", err)
	}
	var report JSONReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("v1 parse: %v", err)
	}
	if report.Version != "1.0" {
		t.Errorf("v1 schema marker missing; got %q", report.Version)
	}
}

func TestGenerateJSONReport_V2_SimulatedFlag(t *testing.T) {
	results := []types.AssertionResult{
		{AssertionID: "a", Status: types.StatusPass, Score: 1.0, Type: types.TypeSchema, Layer: 1},
	}
	output, err := GenerateJSONReport(results, 0, 0, ReportOptions{Simulated: true})
	if err != nil {
		t.Fatalf("v2 simulated: %v", err)
	}
	var report JSONReportV2
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("v2 parse: %v", err)
	}
	if !report.Simulated {
		t.Errorf("expected simulated=true in v2 report")
	}
}
