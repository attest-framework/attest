package report

import (
	"fmt"
	"math"
	"sort"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Trial verdicts compare an assertion's pass rate over fixed-N trials with
// its pinned baseline.
const (
	TrialPass         = "pass"
	TrialRegressed    = "regressed"
	TrialInconclusive = "inconclusive"
)

// Default regression rule: a 95% Wilson interval and a 5-point tolerance.
const (
	DefaultConfidence = 0.95
	DefaultTolerance  = 0.05
)

// PassCount is an assertion's outcome tally over one or more runs. Passes
// counts status pass; Evaluated counts pass, soft_fail and hard_fail;
// NotEvaluated is kept out of the rate (contract C1).
type PassCount struct {
	Passes       int `json:"passes"`
	Evaluated    int `json:"evaluated"`
	NotEvaluated int `json:"not_evaluated"`
}

// Rate returns Passes/Evaluated, or 0 when nothing was evaluated.
func (c PassCount) Rate() float64 {
	if c.Evaluated == 0 {
		return 0
	}
	return float64(c.Passes) / float64(c.Evaluated)
}

// CountPasses tallies result rows by assertion id. Rows of one assertion
// across trials share an id; a single run yields one row per id.
func CountPasses(results []types.AssertionResult) map[string]PassCount {
	out := make(map[string]PassCount)
	for _, r := range results {
		c := out[r.AssertionID]
		switch r.Status {
		case types.StatusPass:
			c.Passes++
			c.Evaluated++
		case types.StatusSoftFail, types.StatusHardFail:
			c.Evaluated++
		default:
			c.NotEvaluated++
		}
		out[r.AssertionID] = c
	}
	return out
}

// Wilson returns the Wilson score interval for passes out of n at the
// two-sided confidence level (0 < confidence < 1). n must be > 0.
func Wilson(passes, n int, confidence float64) (lower, upper float64) {
	z := math.Sqrt2 * math.Erfinv(confidence)
	fn := float64(n)
	p := float64(passes) / fn
	z2 := z * z
	denom := 1 + z2/fn
	center := (p + z2/(2*fn)) / denom
	half := z * math.Sqrt(p*(1-p)/fn+z2/(4*fn*fn)) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}

// RegressionRule is the statistical comparison: the two-sided Wilson
// confidence (0 < Confidence < 1) and the tolerated drop below the
// baseline pass rate (0 <= Tolerance < 1).
type RegressionRule struct {
	Confidence float64
	Tolerance  float64
}

// TrialVerdict is one assertion's current pass rate, its Wilson interval
// and the verdict against the baseline rate minus tolerance (the line):
// regressed when the interval lies below the line, pass when it lies at or
// above it, inconclusive when it straddles it.
type TrialVerdict struct {
	AssertionID      string  `json:"assertion_id"`
	Passes           int     `json:"passes"`
	Evaluated        int     `json:"evaluated"`
	NotEvaluated     int     `json:"not_evaluated"`
	PassRate         float64 `json:"pass_rate"`
	Lower            float64 `json:"wilson_lower"`
	Upper            float64 `json:"wilson_upper"`
	BaselinePassRate float64 `json:"baseline_pass_rate"`
	BaselineRuns     int     `json:"baseline_evaluated"`
	Line             float64 `json:"line"`
	Verdict          string  `json:"verdict"`
}

// CompareTrials returns one verdict per assertion present in both current
// and baseline with at least one evaluated run on each side, sorted by
// assertion id. Assertions evaluated on one side only are reported by the
// baseline delta (added/removed) or the not_evaluated rule instead.
func CompareTrials(current, baseline map[string]PassCount, rule RegressionRule) []TrialVerdict {
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []TrialVerdict
	for _, id := range ids {
		cur, base := current[id], baseline[id]
		if cur.Evaluated == 0 || base.Evaluated == 0 {
			continue
		}
		lower, upper := Wilson(cur.Passes, cur.Evaluated, rule.Confidence)
		line := base.Rate() - rule.Tolerance
		v := TrialVerdict{
			AssertionID:      id,
			Passes:           cur.Passes,
			Evaluated:        cur.Evaluated,
			NotEvaluated:     cur.NotEvaluated,
			PassRate:         cur.Rate(),
			Lower:            lower,
			Upper:            upper,
			BaselinePassRate: base.Rate(),
			BaselineRuns:     base.Evaluated,
			Line:             line,
		}
		switch {
		case upper < line:
			v.Verdict = TrialRegressed
		case lower >= line:
			v.Verdict = TrialPass
		default:
			v.Verdict = TrialInconclusive
		}
		out = append(out, v)
	}
	return out
}

// OverallTrialVerdict folds per-assertion verdicts: any regressed →
// regressed; otherwise any inconclusive → inconclusive; otherwise pass.
func OverallTrialVerdict(vs []TrialVerdict) string {
	overall := TrialPass
	for _, v := range vs {
		switch v.Verdict {
		case TrialRegressed:
			return TrialRegressed
		case TrialInconclusive:
			overall = TrialInconclusive
		}
	}
	return overall
}

// String renders the verdict for logs and markdown.
func (v TrialVerdict) String() string {
	return fmt.Sprintf("%s: %d/%d passed (%.2f, Wilson [%.3f, %.3f]) vs baseline %.2f − tolerance = %.3f → %s",
		v.AssertionID, v.Passes, v.Evaluated, v.PassRate, v.Lower, v.Upper, v.BaselinePassRate, v.Line, v.Verdict)
}
