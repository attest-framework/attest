package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// MarkdownReport holds data for a Markdown PR comment report.
type MarkdownReport struct {
	Title      string
	RunAt      time.Time
	Results    []types.AssertionResult
	TotalCost  float64
	DurationMS int64
	// Simulated marks the report as produced from an SDK simulation shim
	// rather than from real engine evaluation. Renders a "[SIMULATED]"
	// banner so reviewers do not mistake the run for a real evaluation.
	Simulated bool
	// Verbose enables passing-assertion detail tables under each layer
	// section. Defaults to false (failures only) so PR comments stay
	// scannable.
	Verbose bool
}

// GenerateMarkdown writes a hierarchical Markdown report to w. Layout:
//
//  1. Summary header with simulation banner, pass/fail counts, totals,
//     P50/P95 latency.
//  2. Layer sections (1–8) each grouped by assertion type.
//  3. Per-failure detail block — assertion id, layer, type, trace path,
//     expected vs actual, judge metadata if present, suggested action.
//
// Backwards-compatible callers see no panic on the legacy single-arg
// shape because all new fields are optional in MarkdownReport.
func GenerateMarkdown(w io.Writer, r *MarkdownReport) error {
	title := r.Title
	if title == "" {
		title = "Attest Evaluation Report"
	}

	if _, err := fmt.Fprintf(w, "## %s\n\n", title); err != nil {
		return err
	}

	if r.Simulated {
		if _, err := fmt.Fprintln(w, "> **[SIMULATED]** Results produced without contacting the engine. Do not gate releases on this run."); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	if !r.RunAt.IsZero() {
		if _, err := fmt.Fprintf(w, "**Run at:** %s\n\n", r.RunAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}

	stats := computeReportStats(r.Results, r.TotalCost, r.DurationMS)
	if err := writeSummaryBlock(w, stats); err != nil {
		return err
	}

	if len(r.Results) == 0 {
		_, err := fmt.Fprintln(w, "_No assertions evaluated._")
		return err
	}

	failures := filterFailures(r.Results)
	if len(failures) > 0 {
		if err := writeFailureSection(w, failures); err != nil {
			return err
		}
	}

	if err := writeLayerBreakdown(w, r.Results, r.Verbose); err != nil {
		return err
	}

	return nil
}

// reportStats aggregates pass/fail/cost/latency over a result set so the
// summary block can render in one pass.
type reportStats struct {
	total      int
	passed     int
	softFailed int
	hardFailed int
	totalCost  float64
	durationMS int64
	p50        int64
	p95        int64
}

func computeReportStats(results []types.AssertionResult, providedCost float64, providedDur int64) reportStats {
	stats := reportStats{
		total:      len(results),
		totalCost:  providedCost,
		durationMS: providedDur,
	}
	durations := make([]int64, 0, len(results))
	var costSum float64
	for _, res := range results {
		switch res.Status {
		case types.StatusPass:
			stats.passed++
		case types.StatusSoftFail:
			stats.softFailed++
		case types.StatusHardFail:
			stats.hardFailed++
		}
		costSum += res.Cost
		if res.DurationMS > 0 {
			durations = append(durations, res.DurationMS)
		}
	}
	if stats.totalCost == 0 && costSum > 0 {
		stats.totalCost = costSum
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		stats.p50 = percentile(durations, 50)
		stats.p95 = percentile(durations, 95)
	}
	return stats
}

// percentile returns the p-th percentile of a sorted ascending int64
// slice using nearest-rank. Caller must ensure xs is sorted and
// non-empty.
func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	idx := (p * len(xs)) / 100
	if idx >= len(xs) {
		idx = len(xs) - 1
	}
	return xs[idx]
}

func writeSummaryBlock(w io.Writer, s reportStats) error {
	if _, err := fmt.Fprintln(w, "### Summary"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Metric | Value |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "|--------|-------|"); err != nil {
		return err
	}
	fmt.Fprintf(w, "| Total | %d |\n", s.total)
	fmt.Fprintf(w, "| Passed | %d |\n", s.passed)
	fmt.Fprintf(w, "| Soft failed | %d |\n", s.softFailed)
	fmt.Fprintf(w, "| Hard failed | %d |\n", s.hardFailed)
	if s.totalCost > 0 {
		fmt.Fprintf(w, "| Cost | $%.6f |\n", s.totalCost)
	}
	if s.durationMS > 0 {
		fmt.Fprintf(w, "| Duration | %dms |\n", s.durationMS)
	}
	if s.p50 > 0 || s.p95 > 0 {
		fmt.Fprintf(w, "| Latency P50 | %dms |\n", s.p50)
		fmt.Fprintf(w, "| Latency P95 | %dms |\n", s.p95)
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return nil
}

func filterFailures(results []types.AssertionResult) []types.AssertionResult {
	out := make([]types.AssertionResult, 0)
	for _, r := range results {
		if r.Status != types.StatusPass {
			out = append(out, r)
		}
	}
	return out
}

func writeFailureSection(w io.Writer, failures []types.AssertionResult) error {
	if _, err := fmt.Fprintln(w, "### Failures"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	for i := range failures {
		if err := writeFailureBlock(w, &failures[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeFailureBlock(w io.Writer, r *types.AssertionResult) error {
	icon := statusIcon(r.Status)
	layer := r.Layer
	typ := r.Type
	if typ == "" {
		typ = "(unknown)"
	}
	fmt.Fprintf(w, "#### %s `%s` — L%d %s\n\n", icon, r.AssertionID, layer, typ)
	if r.TraceNodePath != "" {
		fmt.Fprintf(w, "- **Trace path:** `%s`\n", r.TraceNodePath)
	}
	fmt.Fprintf(w, "- **Score:** %.3f\n", r.Score)
	if r.Expected != "" {
		fmt.Fprintf(w, "- **Expected:** %s\n", escapeMarkdownInline(r.Expected))
	}
	if r.Actual != "" {
		fmt.Fprintf(w, "- **Actual:** %s\n", escapeMarkdownInline(r.Actual))
	}
	if r.Explanation != "" {
		fmt.Fprintf(w, "- **Explanation:** %s\n", escapeMarkdownInline(r.Explanation))
	}
	if r.ThresholdSource != "" && r.ThresholdSource != types.ThresholdSourceStatic {
		fmt.Fprintf(w, "- **Threshold source:** %s\n", r.ThresholdSource)
	}
	if r.Judge != nil {
		writeJudgeMetadata(w, r.Judge)
	}
	if r.SuggestedAction != "" {
		fmt.Fprintf(w, "- **Suggested action:** %s\n", escapeMarkdownInline(r.SuggestedAction))
	}
	if r.Cost > 0 || r.DurationMS > 0 {
		fmt.Fprintf(w, "- **Cost / latency:** $%.6f, %dms\n", r.Cost, r.DurationMS)
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeJudgeMetadata(w io.Writer, m *types.JudgeMetadata) {
	if m.Model != "" {
		fmt.Fprintf(w, "- **Judge model:** `%s`\n", m.Model)
	}
	if m.RubricName != "" {
		rubric := m.RubricName
		if m.RubricVersion != "" {
			rubric = fmt.Sprintf("%s @ %s", m.RubricName, m.RubricVersion)
		}
		fmt.Fprintf(w, "- **Rubric:** %s\n", rubric)
	}
	if m.PromptHash != "" {
		fmt.Fprintf(w, "- **Prompt hash:** `%s`\n", m.PromptHash)
	}
	if len(m.SampleScores) > 1 {
		samples := make([]string, len(m.SampleScores))
		for i, s := range m.SampleScores {
			samples[i] = fmt.Sprintf("%.2f", s)
		}
		varianceTag := ""
		if m.HighVarianceFlag {
			varianceTag = " ⚠ HIGH VARIANCE"
		}
		fmt.Fprintf(w, "- **Sample scores:** [%s] (mean=%.2f, stddev=%.2f)%s\n",
			strings.Join(samples, ", "), m.ScoreMean, m.ScoreStddev, varianceTag)
	}
}

// writeLayerBreakdown groups the entire result set by Layer and emits a
// per-layer table. Useful when reviewers want the full picture rather
// than just failures.
func writeLayerBreakdown(w io.Writer, results []types.AssertionResult, verbose bool) error {
	groups := groupByLayer(results)
	if len(groups) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "### By layer"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	layers := make([]int, 0, len(groups))
	for k := range groups {
		layers = append(layers, k)
	}
	sort.Ints(layers)

	for _, layer := range layers {
		group := groups[layer]
		passed, soft, hard := countByStatus(group)
		fmt.Fprintf(w, "#### Layer %d (%s) — %d total: %d passed, %d soft failed, %d hard failed\n\n",
			layer, layerName(layer), len(group), passed, soft, hard)

		if !verbose {
			// Show only failures at this layer in the by-layer breakdown.
			fail := filterFailures(group)
			if len(fail) == 0 {
				if _, err := fmt.Fprintln(w, "_All passing._"); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
				continue
			}
			if err := writeAssertionTable(w, fail); err != nil {
				return err
			}
			continue
		}

		if err := writeAssertionTable(w, group); err != nil {
			return err
		}
	}
	return nil
}

func groupByLayer(results []types.AssertionResult) map[int][]types.AssertionResult {
	out := make(map[int][]types.AssertionResult)
	for _, r := range results {
		layer := r.Layer
		if layer == 0 {
			layer = types.LayerForType(r.Type)
		}
		out[layer] = append(out[layer], r)
	}
	return out
}

func countByStatus(rs []types.AssertionResult) (pass, soft, hard int) {
	for _, r := range rs {
		switch r.Status {
		case types.StatusPass:
			pass++
		case types.StatusSoftFail:
			soft++
		case types.StatusHardFail:
			hard++
		}
	}
	return
}

func writeAssertionTable(w io.Writer, rs []types.AssertionResult) error {
	if _, err := fmt.Fprintln(w, "| Assertion | Status | Score | Trace path | Expected | Actual |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "|-----------|--------|-------|-----------|----------|--------|"); err != nil {
		return err
	}
	for _, r := range rs {
		fmt.Fprintf(w, "| `%s` | %s %s | %.3f | %s | %s | %s |\n",
			r.AssertionID,
			statusIcon(r.Status), r.Status,
			r.Score,
			renderCell(r.TraceNodePath),
			renderCell(r.Expected),
			renderCell(r.Actual),
		)
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return nil
}

func layerName(layer int) string {
	switch layer {
	case 1:
		return "schema"
	case 2:
		return "constraint"
	case 3:
		return "trace"
	case 4:
		return "content"
	case 5:
		return "embedding"
	case 6:
		return "llm_judge"
	case 7:
		return "trace_tree"
	case 8:
		return "plugin"
	default:
		return "uncategorized"
	}
}

func statusIcon(status string) string {
	switch status {
	case types.StatusPass:
		return ":white_check_mark:"
	case types.StatusSoftFail:
		return ":warning:"
	case types.StatusHardFail:
		return ":x:"
	default:
		return ":grey_question:"
	}
}

// escapeMarkdownInline escapes pipes and backticks and collapses
// newlines so a value can be embedded in a one-line markdown bullet or
// table cell without breaking surrounding structure. Strings longer
// than ~140 chars are truncated; the truncate is byte-based so a
// multi-byte rune at the cut site is replaced with U+FFFD by
// ToValidUTF8.
func escapeMarkdownInline(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 140 {
		s = strings.ToValidUTF8(s[:137], "") + "..."
	}
	return s
}

// renderCell escapes a string for an inline table cell. Empty values
// render as an em-dash so rows stay aligned in raw markdown.
func renderCell(s string) string {
	if s == "" {
		return "—"
	}
	return escapeMarkdownInline(s)
}
