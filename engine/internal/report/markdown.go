package report

import (
	"fmt"
	"io"
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
}

// GenerateMarkdown writes a Markdown-formatted report to w.
func GenerateMarkdown(w io.Writer, r *MarkdownReport) error {
	title := r.Title
	if title == "" {
		title = "Attest Evaluation Report"
	}

	if _, err := fmt.Fprintf(w, "## %s\n\n", title); err != nil {
		return err
	}

	// Summary metadata
	if !r.RunAt.IsZero() {
		if _, err := fmt.Fprintf(w, "**Run at:** %s\n\n", r.RunAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}

	// Counts
	var passed, softFailed, hardFailed int
	for _, res := range r.Results {
		switch res.Status {
		case types.StatusPass:
			passed++
		case types.StatusSoftFail:
			softFailed++
		case types.StatusHardFail:
			hardFailed++
		}
	}
	total := len(r.Results)

	if _, err := fmt.Fprintf(w, "**Results:** %d total — %d passed, %d soft failed, %d hard failed\n\n",
		total, passed, softFailed, hardFailed); err != nil {
		return err
	}

	if r.TotalCost > 0 {
		if _, err := fmt.Fprintf(w, "**Cost:** $%.6f\n\n", r.TotalCost); err != nil {
			return err
		}
	}

	if r.DurationMS > 0 {
		if _, err := fmt.Fprintf(w, "**Duration:** %dms\n\n", r.DurationMS); err != nil {
			return err
		}
	}

	// Results table
	if len(r.Results) == 0 {
		_, err := fmt.Fprintln(w, "_No assertions evaluated._")
		return err
	}

	if _, err := fmt.Fprintln(w, "| Assertion | Status | Score | Explanation |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "|-----------|--------|-------|-------------|"); err != nil {
		return err
	}

	for _, res := range r.Results {
		statusIcon := statusIcon(res.Status)
		explanation := strings.ReplaceAll(res.Explanation, "|", "\\|")
		if len(explanation) > 100 {
			explanation = explanation[:97] + "..."
		}
		if _, err := fmt.Fprintf(w, "| `%s` | %s %s | %.3f | %s |\n",
			res.AssertionID, statusIcon, res.Status, res.Score, explanation); err != nil {
			return err
		}
	}

	return nil
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
