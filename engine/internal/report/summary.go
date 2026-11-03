package report

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const (
	// summaryRowCap bounds the failures and replay-misses table so one broken
	// run cannot overflow a PR comment.
	summaryRowCap = 20
	// summaryCellCap bounds one table cell, in runes.
	summaryCellCap = 200
)

// PolicyViolation is one entry of `attest-engine policy evaluate` output.
type PolicyViolation struct {
	Rule     string `json:"rule"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
}

// PolicyOutcome is the JSON `attest-engine policy evaluate` prints.
type PolicyOutcome struct {
	Violations []PolicyViolation `json:"violations"`
	ExitCode   int               `json:"exit_code"`
	Passed     bool              `json:"passed"`
	Verdict    string            `json:"verdict,omitempty"`
}

// LoadPolicyOutcome reads a policy-result file written from
// `attest-engine policy evaluate` stdout.
func LoadPolicyOutcome(path string) (*PolicyOutcome, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy result %s: %w", path, err)
	}
	var p PolicyOutcome
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode policy result %s: %w", path, err)
	}
	return &p, nil
}

// RenderSummary renders the PR summary comment for r as GitHub-flavored
// Markdown. The first line is the pass / fail / not-evaluated header, counted
// over tests, with a trailing skipped count when any test was skipped. Output is a pure function of its inputs.
func RenderSummary(r *CIReport, policy *PolicyOutcome) string {
	var b strings.Builder

	passed, failed, notEvaluated, skipped := countTests(r)
	fmt.Fprintf(&b, "### attest: %d passed · %d failed · %d not evaluated", passed, failed, notEvaluated)
	if skipped > 0 {
		fmt.Fprintf(&b, " · %d skipped", skipped)
	}
	b.WriteString("\n\n")

	b.WriteString(replayLine(r.Summary.Replay) + "\n\n")
	if r.Summary.Usage != nil {
		b.WriteString(FormatUsage(*r.Summary.Usage) + "\n\n")
	}

	if reasons := notEvaluatedReasons(r.Results); len(reasons) > 0 {
		b.WriteString("**Not evaluated (assertions):** " + strings.Join(reasons, " · ") + "\n\n")
	}

	writeFindings(&b, FindingRows(r))

	if policy != nil {
		writePolicy(&b, policy)
	}
	return b.String()
}

// countTests counts tests by outcome. A skipped test is not evaluated only
// when all its assertion rows are not_evaluated (contract C1); other skipped
// tests, such as plain skips and expected failures, count as skipped.
func countTests(r *CIReport) (passed, failed, notEvaluated, skipped int) {
	skips := notEvaluatedSkips(r)
	for _, t := range r.Tests {
		switch t.Outcome {
		case OutcomePassed:
			passed++
		case OutcomeFailed:
			failed++
		case OutcomeSkipped:
			if skips[testKey{t.src, t.TestID}] {
				notEvaluated++
			} else {
				skipped++
			}
		}
	}
	return passed, failed, notEvaluated, skipped
}

// replayLine is "<N> replayed · <N> live model calls · $<X.XX>". A report with
// no replay block states zeros: no test served model calls from a fixture and
// none went live.
func replayLine(s *CIReplaySummary) string {
	if s == nil {
		s = &CIReplaySummary{}
	}
	line := fmt.Sprintf("%d replayed · %d live model calls · $%.2f", s.Replayed, s.Live, s.LiveCostUSD)
	if s.UnpricedLiveCalls > 0 {
		line += fmt.Sprintf(" (+%d unpriced)", s.UnpricedLiveCalls)
	}
	if s.Misses > 0 {
		line += fmt.Sprintf(" · %d replay misses", s.Misses)
	}
	return line
}

// notEvaluatedReasons counts not_evaluated assertion rows by reason, most
// frequent first, ties by reason name.
func notEvaluatedReasons(results []CIResult) []string {
	counts := map[string]int{}
	for _, res := range results {
		if res.Status == types.StatusNotEvaluated {
			counts[res.Reason]++
		}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	out := make([]string, len(reasons))
	for i, reason := range reasons {
		out[i] = fmt.Sprintf("`%s` ×%d", reason, counts[reason])
	}
	return out
}

func writeFindings(b *strings.Builder, rows []FindingRow) {
	if len(rows) == 0 {
		return
	}
	b.WriteString("**Failures and replay misses**\n\n")
	b.WriteString("| Location | Test | Assertion | Explanation |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	shown := rows
	if len(shown) > summaryRowCap {
		shown = shown[:summaryRowCap]
	}
	for _, row := range shown {
		kind := row.Type
		if row.Reason != "" {
			kind += " (" + row.Reason + ")"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n",
			cell(location(row)), cell(row.TestID), cell(kind), cell(row.Explanation))
	}
	if extra := len(rows) - len(shown); extra > 0 {
		fmt.Fprintf(b, "\nand %d more\n", extra)
	}
	b.WriteString("\n")
}

func location(row FindingRow) string {
	switch {
	case row.File == "":
		return "-"
	case row.Line > 0:
		return fmt.Sprintf("%s:%d", row.File, row.Line)
	default:
		return row.File
	}
}

func writePolicy(b *strings.Builder, p *PolicyOutcome) {
	verdict := "pass"
	switch p.ExitCode {
	case 0:
	case 2:
		verdict = "warn"
	default:
		verdict = "fail"
	}
	fmt.Fprintf(b, "**Policy:** %s (exit %d)", verdict, p.ExitCode)
	if p.Verdict != "" {
		fmt.Fprintf(b, " · regression verdict %s", p.Verdict)
	}
	b.WriteString("\n")
	for _, v := range p.Violations {
		fmt.Fprintf(b, "- %s `%s`: %s\n", v.Severity, v.Rule, cell(v.Detail))
	}
}

// cell makes s safe inside a Markdown table cell: it removes line breaks and
// escapes pipes, then truncates to summaryCellCap runes.
func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	if utf8.RuneCountInString(s) > summaryCellCap {
		runes := []rune(s)
		s = string(runes[:summaryCellCap-1]) + "…"
	}
	return s
}

// FormatUsage renders usage as the run-summary line
// "<N> model calls · <T> tokens (<C> cached) · $<X> · retry cost $<Y>".
func FormatUsage(u types.Usage) string {
	line := fmt.Sprintf("%d model calls · %d tokens (%d cached) · $%.4f · retry cost $%.4f",
		u.ModelCalls, u.InputTokens+u.OutputTokens, u.CachedTokens, u.CostUSD, u.RetryCostUSD)
	if u.BudgetRefused > 0 {
		line += fmt.Sprintf(" · %d calls refused by budget", u.BudgetRefused)
	}
	if u.UnpricedCalls > 0 {
		line += fmt.Sprintf(" · %d unpriced calls not in $", u.UnpricedCalls)
	}
	return line
}
