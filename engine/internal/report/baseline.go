package report

import (
	"fmt"

	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// BaselineDelta summarises how a current evaluation compares to a stored
// baseline (pinned via `attest baseline pin <tag>`). It powers the
// `attest report --vs <tag>` and PR-comment regression block.
type BaselineDelta struct {
	BaselineTag       string                `json:"baseline_tag"`
	PassRateBaseline  float64               `json:"pass_rate_baseline"`
	PassRateCurrent   float64               `json:"pass_rate_current"`
	PassRateDelta     float64               `json:"pass_rate_delta"`
	CostBaseline      float64               `json:"cost_baseline"`
	CostCurrent       float64               `json:"cost_current"`
	CostDelta         float64               `json:"cost_delta"`
	DurationBaseline  int64                 `json:"duration_baseline_ms"`
	DurationCurrent   int64                 `json:"duration_current_ms"`
	DurationDelta     int64                 `json:"duration_delta_ms"`
	AssertionsAdded   []string              `json:"assertions_added,omitempty"`
	AssertionsRemoved []string              `json:"assertions_removed,omitempty"`
	Regressions       []AssertionTransition `json:"regressions,omitempty"`
	Improvements      []AssertionTransition `json:"improvements,omitempty"`
}

// AssertionTransition records a status change for a single assertion
// between baseline and current. Classification is one of
// "regression", "improvement", "stochastic" (status unchanged but
// score moved enough to be visible).
type AssertionTransition struct {
	AssertionID    string  `json:"assertion_id"`
	BaselineStatus string  `json:"baseline_status"`
	CurrentStatus  string  `json:"current_status"`
	BaselineScore  float64 `json:"baseline_score"`
	CurrentScore   float64 `json:"current_score"`
	Classification string  `json:"classification"`
}

// statusRank ranks pass < soft_fail < hard_fail so a higher rank means
// "more failed". Used to decide whether a transition is a regression
// (rank up) or improvement (rank down).
func statusRank(status string) int {
	switch status {
	case types.StatusPass:
		return 0
	case types.StatusSoftFail:
		return 1
	case types.StatusHardFail:
		return 2
	default:
		return -1
	}
}

// ComputeBaselineDelta diffs current results against baseline entries
// (typically loaded from BaselineStore.Get). Pass-rate is over assertions
// that exist in the respective set. Per-assertion transitions are
// classified into Regressions and Improvements; same-status entries are
// dropped to keep the report focused on what changed.
func ComputeBaselineDelta(tag string, baseline []cache.BaselineEntry, current []types.AssertionResult) BaselineDelta {
	d := BaselineDelta{BaselineTag: tag}

	baselineByID := make(map[string]cache.BaselineEntry, len(baseline))
	for _, e := range baseline {
		baselineByID[e.AssertionID] = e
		d.CostBaseline += e.Cost
		d.DurationBaseline += e.DurationMS
	}

	currentByID := make(map[string]types.AssertionResult, len(current))
	var passCurrent int
	for _, r := range current {
		currentByID[r.AssertionID] = r
		d.CostCurrent += r.Cost
		d.DurationCurrent += r.DurationMS
		if r.Status == types.StatusPass {
			passCurrent++
		}
	}

	var passBaseline int
	for _, e := range baseline {
		if e.Status == types.StatusPass {
			passBaseline++
		}
	}
	if len(baseline) > 0 {
		d.PassRateBaseline = float64(passBaseline) / float64(len(baseline))
	}
	if len(current) > 0 {
		d.PassRateCurrent = float64(passCurrent) / float64(len(current))
	}
	d.PassRateDelta = d.PassRateCurrent - d.PassRateBaseline
	d.CostDelta = d.CostCurrent - d.CostBaseline
	d.DurationDelta = d.DurationCurrent - d.DurationBaseline

	// Assertions present in current but not baseline — newly added.
	for id := range currentByID {
		if _, ok := baselineByID[id]; !ok {
			d.AssertionsAdded = append(d.AssertionsAdded, id)
		}
	}
	// Assertions present in baseline but not current — removed.
	for id := range baselineByID {
		if _, ok := currentByID[id]; !ok {
			d.AssertionsRemoved = append(d.AssertionsRemoved, id)
		}
	}

	// Status transitions for assertions present in both sides.
	for id, b := range baselineByID {
		c, ok := currentByID[id]
		if !ok {
			continue
		}
		bRank := statusRank(b.Status)
		cRank := statusRank(c.Status)
		if bRank == cRank {
			continue
		}
		t := AssertionTransition{
			AssertionID:    id,
			BaselineStatus: b.Status,
			CurrentStatus:  c.Status,
			BaselineScore:  b.Score,
			CurrentScore:   c.Score,
		}
		if cRank > bRank {
			t.Classification = "regression"
			d.Regressions = append(d.Regressions, t)
		} else {
			t.Classification = "improvement"
			d.Improvements = append(d.Improvements, t)
		}
	}

	return d
}

// HasChanges reports whether the delta has anything worth showing in a
// report — used to skip rendering an empty baseline section.
func (d BaselineDelta) HasChanges() bool {
	return len(d.Regressions) > 0 ||
		len(d.Improvements) > 0 ||
		len(d.AssertionsAdded) > 0 ||
		len(d.AssertionsRemoved) > 0 ||
		d.PassRateDelta != 0 ||
		d.CostDelta != 0 ||
		d.DurationDelta != 0
}

// RegressedAssertionIDs returns the regression list as a flat string slice
// for downstream consumers (e.g. policy block-on-regression rules).
func (d BaselineDelta) RegressedAssertionIDs() []string {
	out := make([]string, 0, len(d.Regressions))
	for _, t := range d.Regressions {
		out = append(out, t.AssertionID)
	}
	return out
}

// writeBaselineSection emits a markdown block summarising the diff. Only
// invoked when GenerateMarkdown is given a non-nil Baseline pointer.
func writeBaselineSection(sw *stickyWriter, d *BaselineDelta) {
	sw.printf("### vs `%s`\n\n", d.BaselineTag)
	sw.println("| Metric | Baseline | Current | Δ |")
	sw.println("|--------|----------|---------|---|")
	sw.printf("| Pass rate | %.1f%% | %.1f%% | %+.1f%% |\n",
		d.PassRateBaseline*100, d.PassRateCurrent*100, d.PassRateDelta*100)
	if d.CostBaseline > 0 || d.CostCurrent > 0 {
		sw.printf("| Cost | $%.6f | $%.6f | %s |\n",
			d.CostBaseline, d.CostCurrent, formatCostDelta(d.CostDelta))
	}
	if d.DurationBaseline > 0 || d.DurationCurrent > 0 {
		sw.printf("| Duration | %dms | %dms | %s |\n",
			d.DurationBaseline, d.DurationCurrent, formatDurationDelta(d.DurationDelta))
	}
	sw.println()

	if len(d.Regressions) > 0 {
		sw.printf("**Regressions (%d):**\n\n", len(d.Regressions))
		writeTransitionTable(sw, d.Regressions)
	}
	if len(d.Improvements) > 0 {
		sw.printf("**Improvements (%d):**\n\n", len(d.Improvements))
		writeTransitionTable(sw, d.Improvements)
	}
	if len(d.AssertionsAdded) > 0 {
		sw.printf("**Added (%d):** ", len(d.AssertionsAdded))
		writeIDList(sw, d.AssertionsAdded)
	}
	if len(d.AssertionsRemoved) > 0 {
		sw.printf("**Removed (%d):** ", len(d.AssertionsRemoved))
		writeIDList(sw, d.AssertionsRemoved)
	}
}

func writeTransitionTable(sw *stickyWriter, ts []AssertionTransition) {
	sw.println("| Assertion | Was | Now | Score Δ |")
	sw.println("|-----------|-----|-----|---------|")
	for _, t := range ts {
		sw.printf("| `%s` | %s | %s | %.3f → %.3f |\n",
			t.AssertionID, t.BaselineStatus, t.CurrentStatus, t.BaselineScore, t.CurrentScore)
	}
	sw.println()
}

func writeIDList(sw *stickyWriter, ids []string) {
	for i, id := range ids {
		if i > 0 {
			sw.printf(", ")
		}
		sw.printf("`%s`", id)
	}
	sw.println()
	sw.println()
}

func formatCostDelta(d float64) string {
	if d == 0 {
		return "0"
	}
	return fmt.Sprintf("%+.6f", d)
}

func formatDurationDelta(d int64) string {
	if d == 0 {
		return "0ms"
	}
	return fmt.Sprintf("%+dms", d)
}
