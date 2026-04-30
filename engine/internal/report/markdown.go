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

// stickyWriter wraps an io.Writer and remembers the first error
// encountered so a long sequence of Fprintf calls can stay free of
// per-call error bubbling. Callers run the report and read err once at
// the end.
type stickyWriter struct {
	w   io.Writer
	err error
}

func (s *stickyWriter) printf(format string, args ...any) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintf(s.w, format, args...)
}

func (s *stickyWriter) println(args ...any) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintln(s.w, args...)
}

// GenerateMarkdown writes a hierarchical Markdown report to w. Layout:
//
//  1. Summary header with simulation banner, pass/fail counts, totals,
//     P50/P95 latency.
//  2. Failure section — one detail block per failing assertion with
//     trace path, expected vs actual, judge metadata, and suggested
//     action.
//  3. Per-layer breakdown grouping every result (pass + fail) by
//     pipeline layer 1–8.
func GenerateMarkdown(w io.Writer, r *MarkdownReport) error {
	sw := &stickyWriter{w: w}

	title := r.Title
	if title == "" {
		title = "Attest Evaluation Report"
	}
	sw.printf("## %s\n\n", title)

	if r.Simulated {
		sw.println("> **[SIMULATED]** Results produced without contacting the engine. Do not gate releases on this run.")
		sw.println()
	}
	if !r.RunAt.IsZero() {
		sw.printf("**Run at:** %s\n\n", r.RunAt.UTC().Format(time.RFC3339))
	}

	writeSummaryBlock(sw, computeReportStats(r.Results, r.TotalCost, r.DurationMS))

	if len(r.Results) == 0 {
		sw.println("_No assertions evaluated._")
		return sw.err
	}

	failures := filterFailures(r.Results)
	if len(failures) > 0 {
		writeFailureSection(sw, failures)
	}
	writeLayerBreakdown(sw, r.Results, r.Verbose)
	return sw.err
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

func writeSummaryBlock(sw *stickyWriter, s reportStats) {
	sw.println("### Summary")
	sw.println()
	sw.println("| Metric | Value |")
	sw.println("|--------|-------|")
	sw.printf("| Total | %d |\n", s.total)
	sw.printf("| Passed | %d |\n", s.passed)
	sw.printf("| Soft failed | %d |\n", s.softFailed)
	sw.printf("| Hard failed | %d |\n", s.hardFailed)
	if s.totalCost > 0 {
		sw.printf("| Cost | $%.6f |\n", s.totalCost)
	}
	if s.durationMS > 0 {
		sw.printf("| Duration | %dms |\n", s.durationMS)
	}
	if s.p50 > 0 || s.p95 > 0 {
		sw.printf("| Latency P50 | %dms |\n", s.p50)
		sw.printf("| Latency P95 | %dms |\n", s.p95)
	}
	sw.println()
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

func writeFailureSection(sw *stickyWriter, failures []types.AssertionResult) {
	sw.println("### Failures")
	sw.println()
	for i := range failures {
		writeFailureBlock(sw, &failures[i])
	}
}

func writeFailureBlock(sw *stickyWriter, r *types.AssertionResult) {
	typ := r.Type
	if typ == "" {
		typ = "(unknown)"
	}
	sw.printf("#### %s `%s` — L%d %s\n\n", statusIcon(r.Status), r.AssertionID, r.Layer, typ)
	if r.TraceNodePath != "" {
		sw.printf("- **Trace path:** `%s`\n", r.TraceNodePath)
	}
	sw.printf("- **Score:** %.3f\n", r.Score)
	if r.Expected != "" {
		sw.printf("- **Expected:** %s\n", escapeMarkdownInline(r.Expected))
	}
	if r.Actual != "" {
		sw.printf("- **Actual:** %s\n", escapeMarkdownInline(r.Actual))
	}
	if r.Explanation != "" {
		sw.printf("- **Explanation:** %s\n", escapeMarkdownInline(r.Explanation))
	}
	if r.ThresholdSource != "" && r.ThresholdSource != types.ThresholdSourceStatic {
		sw.printf("- **Threshold source:** %s\n", r.ThresholdSource)
	}
	if r.Judge != nil {
		writeJudgeMetadata(sw, r.Judge)
	}
	if r.SuggestedAction != "" {
		sw.printf("- **Suggested action:** %s\n", escapeMarkdownInline(r.SuggestedAction))
	}
	if r.Cost > 0 || r.DurationMS > 0 {
		sw.printf("- **Cost / latency:** $%.6f, %dms\n", r.Cost, r.DurationMS)
	}
	sw.println()
}

func writeJudgeMetadata(sw *stickyWriter, m *types.JudgeMetadata) {
	if m.Model != "" {
		sw.printf("- **Judge model:** `%s`\n", m.Model)
	}
	if m.RubricName != "" {
		rubric := m.RubricName
		if m.RubricVersion != "" {
			rubric = fmt.Sprintf("%s @ %s", m.RubricName, m.RubricVersion)
		}
		sw.printf("- **Rubric:** %s\n", rubric)
	}
	if m.PromptHash != "" {
		sw.printf("- **Prompt hash:** `%s`\n", m.PromptHash)
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
		sw.printf("- **Sample scores:** [%s] (mean=%.2f, stddev=%.2f)%s\n",
			strings.Join(samples, ", "), m.ScoreMean, m.ScoreStddev, varianceTag)
	}
	if len(m.BiasProbes) > 0 {
		entries := make([]string, len(m.BiasProbes))
		for i, p := range m.BiasProbes {
			entries[i] = fmt.Sprintf("%s Δ%+.2f", p.Name, p.Delta)
		}
		sw.printf("- **Bias probes:** %s\n", strings.Join(entries, ", "))
	}
	if m.Calibration != nil {
		c := m.Calibration
		sw.printf("- **Calibration:** %d labels, agreement=%.2f, κ=%.2f\n",
			c.LabelCount, c.Agreement, c.CohenKappa)
	}
}

// writeLayerBreakdown groups the entire result set by Layer and emits a
// per-layer table. Useful when reviewers want the full picture rather
// than just failures.
func writeLayerBreakdown(sw *stickyWriter, results []types.AssertionResult, verbose bool) {
	groups := groupByLayer(results)
	if len(groups) == 0 {
		return
	}
	sw.println("### By layer")
	sw.println()

	layers := make([]int, 0, len(groups))
	for k := range groups {
		layers = append(layers, k)
	}
	sort.Ints(layers)

	for _, layer := range layers {
		group := groups[layer]
		passed, soft, hard := countByStatus(group)
		sw.printf("#### Layer %d (%s) — %d total: %d passed, %d soft failed, %d hard failed\n\n",
			layer, layerName(layer), len(group), passed, soft, hard)

		rows := group
		if !verbose {
			rows = filterFailures(group)
		}
		if len(rows) == 0 {
			sw.println("_All passing._")
			sw.println()
			continue
		}
		writeAssertionTable(sw, rows)
	}
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

func writeAssertionTable(sw *stickyWriter, rs []types.AssertionResult) {
	sw.println("| Assertion | Status | Score | Trace path | Expected | Actual |")
	sw.println("|-----------|--------|-------|-----------|----------|--------|")
	for _, r := range rs {
		sw.printf("| `%s` | %s %s | %.3f | %s | %s | %s |\n",
			r.AssertionID,
			statusIcon(r.Status), r.Status,
			r.Score,
			renderCell(r.TraceNodePath),
			renderCell(r.Expected),
			renderCell(r.Actual),
		)
	}
	sw.println()
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
