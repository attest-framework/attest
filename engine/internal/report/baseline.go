package report

import (
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
	// BaselineCounts holds each baseline assertion's pinned pass tally for
	// the policy regression rule. Not serialized.
	BaselineCounts map[string]PassCount `json:"-"`
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
//
// Trial rows (several rows per assertion id) are first folded into one
// row per assertion, the same way `baseline pin` folds them.
func ComputeBaselineDelta(tag string, baseline []cache.BaselineEntry, current []types.AssertionResult) BaselineDelta {
	d := BaselineDelta{BaselineTag: tag, BaselineCounts: make(map[string]PassCount, len(baseline))}
	current, _ = FoldRuns(current)

	baselineByID := make(map[string]cache.BaselineEntry, len(baseline))
	for _, e := range baseline {
		baselineByID[e.AssertionID] = e
		d.BaselineCounts[e.AssertionID] = PassCount{Passes: e.Passes, Evaluated: e.Evaluated, NotEvaluated: e.NotEvaluated}
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

// FoldRuns folds result rows that share an assertion id (fixed-N trials)
// into one representative row per assertion, in first-seen order, and
// returns the pass tallies. The representative row has the worst status
// observed (hard_fail > soft_fail > pass > not_evaluated), the mean score
// of evaluated runs, summed cost and duration, and no trial index.
func FoldRuns(results []types.AssertionResult) ([]types.AssertionResult, map[string]PassCount) {
	counts := CountPasses(results)
	index := make(map[string]int, len(counts))
	folded := make([]types.AssertionResult, 0, len(counts))
	scoreSum := make(map[string]float64, len(counts))
	for _, r := range results {
		if r.Status != types.StatusNotEvaluated {
			scoreSum[r.AssertionID] += r.Score
		}
		i, seen := index[r.AssertionID]
		if !seen {
			row := r
			row.Trial = 0
			index[r.AssertionID] = len(folded)
			folded = append(folded, row)
			continue
		}
		row := &folded[i]
		row.Cost += r.Cost
		row.DurationMS += r.DurationMS
		if foldRank(r.Status) > foldRank(row.Status) {
			cost, dur := row.Cost, row.DurationMS
			*row = r
			row.Trial, row.Cost, row.DurationMS = 0, cost, dur
		}
	}
	for i := range folded {
		if c := counts[folded[i].AssertionID]; c.Evaluated > 0 {
			folded[i].Score = scoreSum[folded[i].AssertionID] / float64(c.Evaluated)
		}
	}
	return folded, counts
}

func foldRank(status string) int {
	switch status {
	case types.StatusHardFail:
		return 3
	case types.StatusSoftFail:
		return 2
	case types.StatusPass:
		return 1
	default:
		return 0
	}
}

// BaselineEntries builds the rows `baseline pin` stores for a report:
// one per assertion, with its pass tally across trials.
func BaselineEntries(results []types.AssertionResult) []cache.BaselineEntry {
	folded, counts := FoldRuns(results)
	out := make([]cache.BaselineEntry, 0, len(folded))
	for _, r := range folded {
		c := counts[r.AssertionID]
		out = append(out, cache.BaselineEntry{
			AssertionID:  r.AssertionID,
			Type:         r.Type,
			Status:       r.Status,
			Score:        r.Score,
			Cost:         r.Cost,
			DurationMS:   r.DurationMS,
			Passes:       c.Passes,
			Evaluated:    c.Evaluated,
			NotEvaluated: c.NotEvaluated,
		})
	}
	return out
}
