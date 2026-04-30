package report_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/report"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestComputeBaselineDelta_DetectsRegressionAndImprovement(t *testing.T) {
	baseline := []cache.BaselineEntry{
		{AssertionID: "a1", Type: "schema", Status: types.StatusPass, Score: 1.0, Cost: 0.0, DurationMS: 5},
		{AssertionID: "a2", Type: "llm_judge", Status: types.StatusSoftFail, Score: 0.6, Cost: 0.01, DurationMS: 100},
		{AssertionID: "a3", Type: "constraint", Status: types.StatusPass, Score: 1.0, DurationMS: 3},
	}
	current := []types.AssertionResult{
		{AssertionID: "a1", Type: "schema", Status: types.StatusHardFail, Score: 0.0, DurationMS: 6},
		{AssertionID: "a2", Type: "llm_judge", Status: types.StatusPass, Score: 0.9, Cost: 0.02, DurationMS: 110},
		{AssertionID: "a3", Type: "constraint", Status: types.StatusPass, Score: 1.0, DurationMS: 4},
	}

	delta := report.ComputeBaselineDelta("v0.9.0", baseline, current)
	if delta.BaselineTag != "v0.9.0" {
		t.Errorf("tag = %q, want v0.9.0", delta.BaselineTag)
	}
	if got := len(delta.Regressions); got != 1 {
		t.Errorf("regressions = %d, want 1", got)
	}
	if delta.Regressions[0].AssertionID != "a1" {
		t.Errorf("regression id = %q, want a1", delta.Regressions[0].AssertionID)
	}
	if delta.Regressions[0].Classification != "regression" {
		t.Errorf("regression classification = %q", delta.Regressions[0].Classification)
	}
	if got := len(delta.Improvements); got != 1 {
		t.Errorf("improvements = %d, want 1", got)
	}
	if delta.Improvements[0].AssertionID != "a2" {
		t.Errorf("improvement id = %q, want a2", delta.Improvements[0].AssertionID)
	}

	// Pass rate: baseline 2/3, current 2/3 → delta 0
	if delta.PassRateBaseline < 0.66 || delta.PassRateBaseline > 0.67 {
		t.Errorf("pass_rate_baseline = %f, want ~0.667", delta.PassRateBaseline)
	}
	if delta.PassRateDelta != 0 {
		t.Errorf("pass_rate_delta = %f, want 0", delta.PassRateDelta)
	}

	// Cost delta: 0.02 - 0.01 = +0.01
	if delta.CostDelta < 0.0099 || delta.CostDelta > 0.0101 {
		t.Errorf("cost_delta = %f, want ~0.01", delta.CostDelta)
	}
}

func TestComputeBaselineDelta_DetectsAddedAndRemoved(t *testing.T) {
	baseline := []cache.BaselineEntry{
		{AssertionID: "old", Type: "schema", Status: types.StatusPass, Score: 1.0},
	}
	current := []types.AssertionResult{
		{AssertionID: "new", Type: "schema", Status: types.StatusPass, Score: 1.0},
	}
	d := report.ComputeBaselineDelta("v1", baseline, current)
	if got := d.AssertionsAdded; len(got) != 1 || got[0] != "new" {
		t.Errorf("added = %v, want [new]", got)
	}
	if got := d.AssertionsRemoved; len(got) != 1 || got[0] != "old" {
		t.Errorf("removed = %v, want [old]", got)
	}
	if len(d.Regressions) != 0 {
		t.Errorf("regressions on disjoint sets must be 0; got %d", len(d.Regressions))
	}
}

func TestComputeBaselineDelta_HasChangesFalseOnIdenticalSets(t *testing.T) {
	baseline := []cache.BaselineEntry{
		{AssertionID: "a1", Status: types.StatusPass, Score: 1.0, Cost: 0.0, DurationMS: 5},
	}
	current := []types.AssertionResult{
		{AssertionID: "a1", Status: types.StatusPass, Score: 1.0, Cost: 0.0, DurationMS: 5},
	}
	d := report.ComputeBaselineDelta("v1", baseline, current)
	if d.HasChanges() {
		t.Errorf("HasChanges should be false on identical sets; got %+v", d)
	}
}

func TestComputeBaselineDelta_RegressedAssertionIDs(t *testing.T) {
	baseline := []cache.BaselineEntry{
		{AssertionID: "a", Status: types.StatusPass},
		{AssertionID: "b", Status: types.StatusPass},
	}
	current := []types.AssertionResult{
		{AssertionID: "a", Status: types.StatusHardFail},
		{AssertionID: "b", Status: types.StatusSoftFail},
	}
	d := report.ComputeBaselineDelta("t", baseline, current)
	got := d.RegressedAssertionIDs()
	if len(got) != 2 {
		t.Fatalf("RegressedAssertionIDs = %v, want 2 items", got)
	}
}

func TestGenerateMarkdown_RendersBaselineSection(t *testing.T) {
	baseline := []cache.BaselineEntry{
		{AssertionID: "a1", Status: types.StatusPass, Score: 1.0, Cost: 0.0, DurationMS: 5},
		{AssertionID: "a2", Status: types.StatusPass, Score: 1.0, Cost: 0.01, DurationMS: 50},
	}
	current := []types.AssertionResult{
		{AssertionID: "a1", Status: types.StatusHardFail, Score: 0.0, DurationMS: 6, Layer: 1, Type: "schema", FailureClass: types.FailureClassBrokenCode},
		{AssertionID: "a2", Status: types.StatusPass, Score: 1.0, Cost: 0.02, DurationMS: 60, Layer: 1, Type: "schema"},
	}
	delta := report.ComputeBaselineDelta("v0.9.0", baseline, current)

	r := &report.MarkdownReport{
		Title:    "Run",
		RunAt:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Results:  current,
		Baseline: &delta,
	}
	var buf bytes.Buffer
	if err := report.GenerateMarkdown(&buf, r); err != nil {
		t.Fatalf("GenerateMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "vs `v0.9.0`") {
		t.Errorf("markdown missing baseline header; got:\n%s", out)
	}
	if !strings.Contains(out, "Regressions (1)") {
		t.Errorf("markdown missing regressions row; got:\n%s", out)
	}
	if !strings.Contains(out, "broken_code") {
		t.Errorf("markdown missing failure class; got:\n%s", out)
	}
	if !strings.Contains(out, "Pass rate") {
		t.Errorf("markdown missing pass-rate row; got:\n%s", out)
	}
}

func TestGenerateMarkdown_OmitsBaselineSectionWhenNil(t *testing.T) {
	r := &report.MarkdownReport{
		Title: "Run",
		Results: []types.AssertionResult{
			{AssertionID: "a1", Status: types.StatusPass, Score: 1.0, Layer: 1, Type: "schema"},
		},
	}
	var buf bytes.Buffer
	if err := report.GenerateMarkdown(&buf, r); err != nil {
		t.Fatalf("GenerateMarkdown: %v", err)
	}
	if strings.Contains(buf.String(), "vs `") {
		t.Errorf("markdown rendered baseline section without delta; got:\n%s", buf.String())
	}
}
