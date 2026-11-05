package report_test

import (
	"math"
	"testing"

	"github.com/attest-framework/attest/engine/internal/report"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func near(a, b float64) bool { return math.Abs(a-b) < 5e-4 }

func TestWilson_KnownValues_MatchReference(t *testing.T) {
	cases := []struct {
		passes, n    int
		lower, upper float64
	}{
		{16, 20, 0.5840, 0.9193},
		{8, 20, 0.2188, 0.6134},
		{20, 20, 0.8389, 1.0},
		{0, 10, 0.0, 0.2775},
	}
	for _, c := range cases {
		lo, hi := report.Wilson(c.passes, c.n, 0.95)
		if !near(lo, c.lower) || !near(hi, c.upper) {
			t.Errorf("Wilson(%d, %d, 0.95) = [%.4f, %.4f], want [%.4f, %.4f]", c.passes, c.n, lo, hi, c.lower, c.upper)
		}
	}
}

func trialRows(id string, passes, fails, notEvaluated int) []types.AssertionResult {
	var rows []types.AssertionResult
	trial := 1
	add := func(status, reason string, k int) {
		for range k {
			rows = append(rows, types.AssertionResult{AssertionID: id, Status: status, Reason: reason, Trial: trial, Cost: 0.001, DurationMS: 2})
			trial++
		}
	}
	add(types.StatusPass, "", passes)
	add(types.StatusHardFail, "", fails)
	add(types.StatusNotEvaluated, types.ReasonBudgetExhausted, notEvaluated)
	return rows
}

func TestCompareTrials_PlanAcceptanceRates_GiveExpectedVerdicts(t *testing.T) {
	// Baseline pinned at --flaky 0.1 (18/20); current runs at 20 trials.
	baseline := map[string]report.PassCount{"tone": {Passes: 18, Evaluated: 20}}
	rule := report.RegressionRule{Confidence: report.DefaultConfidence, Tolerance: report.DefaultTolerance}
	cases := []struct {
		name   string
		passes int
		want   string
	}{
		{"flaky 0.2 → 16/20", 16, report.TrialInconclusive},
		{"flaky 0.6 → 8/20", 8, report.TrialRegressed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			current := report.CountPasses(trialRows("tone", c.passes, 20-c.passes, 0))
			got := report.CompareTrials(current, baseline, rule)
			if len(got) != 1 || got[0].Verdict != c.want {
				t.Fatalf("verdicts = %+v, want one %s", got, c.want)
			}
			if !near(got[0].Line, 0.85) {
				t.Errorf("line = %v, want 0.9 - 0.05", got[0].Line)
			}
		})
	}
}

func TestCompareTrials_IntervalAboveLine_Passes(t *testing.T) {
	baseline := map[string]report.PassCount{"tone": {Passes: 1, Evaluated: 2}}
	current := report.CountPasses(trialRows("tone", 100, 0, 0))

	got := report.CompareTrials(current, baseline, report.RegressionRule{Confidence: 0.95, Tolerance: 0.05})

	if len(got) != 1 || got[0].Verdict != report.TrialPass {
		t.Fatalf("verdicts = %+v, want pass", got)
	}
}

func TestCountPasses_NotEvaluatedRuns_ExcludedFromRate(t *testing.T) {
	c := report.CountPasses(trialRows("tone", 3, 1, 6))["tone"]

	if c.Passes != 3 || c.Evaluated != 4 || c.NotEvaluated != 6 || c.Rate() != 0.75 {
		t.Errorf("count = %+v rate %v, want 3/4 with 6 not evaluated", c, c.Rate())
	}
}

func TestCompareTrials_NoEvaluatedRunOnOneSide_Skipped(t *testing.T) {
	baseline := map[string]report.PassCount{"a": {Evaluated: 0, NotEvaluated: 3}, "b": {Passes: 1, Evaluated: 1}}
	current := report.CountPasses(append(trialRows("a", 2, 0, 0), trialRows("b", 0, 0, 2)...))

	if got := report.CompareTrials(current, baseline, report.RegressionRule{Confidence: 0.95}); len(got) != 0 {
		t.Errorf("verdicts = %+v, want none", got)
	}
}

func TestOverallTrialVerdict_RegressedOutranksInconclusive(t *testing.T) {
	vs := []report.TrialVerdict{{Verdict: report.TrialPass}, {Verdict: report.TrialInconclusive}, {Verdict: report.TrialRegressed}}

	if got := report.OverallTrialVerdict(vs); got != report.TrialRegressed {
		t.Errorf("overall = %s, want regressed", got)
	}
	if got := report.OverallTrialVerdict(vs[:2]); got != report.TrialInconclusive {
		t.Errorf("overall = %s, want inconclusive", got)
	}
}

func TestBaselineEntries_TrialRows_FoldToOneEntryWithCounts(t *testing.T) {
	rows := append(trialRows("tone", 2, 1, 1), types.AssertionResult{AssertionID: "schema", Status: types.StatusPass, Score: 1})

	entries := report.BaselineEntries(rows)

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	tone := entries[0]
	if tone.AssertionID != "tone" || tone.Status != types.StatusHardFail || tone.Passes != 2 || tone.Evaluated != 3 || tone.NotEvaluated != 1 {
		t.Errorf("tone entry = %+v, want worst status hard_fail and 2/3 with 1 not evaluated", tone)
	}
	if !near(tone.Cost, 0.004) || tone.DurationMS != 8 {
		t.Errorf("tone cost/duration = %v/%d, want summed 0.004/8", tone.Cost, tone.DurationMS)
	}
	if s := entries[1]; s.Passes != 1 || s.Evaluated != 1 {
		t.Errorf("schema entry = %+v, want 1/1", s)
	}
}

func TestFormatUsage_RendersSummaryLine(t *testing.T) {
	u := types.Usage{ModelCalls: 12, InputTokens: 900, OutputTokens: 100, CachedTokens: 300, CostUSD: 0.0123, RetryCostUSD: 0.001, BudgetRefused: 2}

	got := report.FormatUsage(u)

	want := "12 model calls · 1000 tokens (300 cached) · $0.0123 · retry cost $0.0010 · 2 calls refused by budget"
	if got != want {
		t.Errorf("FormatUsage = %q, want %q", got, want)
	}
}
