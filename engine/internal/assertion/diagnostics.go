package assertion

import (
	"fmt"
	"strings"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// maxDiagnosticBytes truncates Expected/Actual fields so reports do not
// balloon when an assertion target resolves to a multi-megabyte payload.
const maxDiagnosticBytes = 512

// annotateDiagnostics fills in the report-side fields (Layer, Type,
// TraceNodePath, Expected, Actual, SuggestedAction) on an AssertionResult
// after the evaluator has returned. Evaluators populate what they can
// inline (Expected/Actual via assertion-specific data); this helper
// guarantees Layer/Type/TraceNodePath are always set so the markdown
// renderer can group failures consistently.
//
// Evaluators that pre-populate any field win — annotateDiagnostics never
// overwrites a non-empty value.
func annotateDiagnostics(r *types.AssertionResult, a *types.Assertion) {
	if r == nil || a == nil {
		return
	}
	if r.Type == "" {
		r.Type = a.Type
	}
	if r.Layer == 0 {
		r.Layer = types.LayerForType(a.Type)
	}
	if r.TraceNodePath == "" {
		r.TraceNodePath = inferTraceNodePath(a)
	}
}

// inferTraceNodePath does a best-effort decode of the assertion spec to
// find a "target" or "field" key — the two conventions used by every
// builtin evaluator. Returns "" when no obvious node path is present.
func inferTraceNodePath(a *types.Assertion) string {
	if len(a.Spec) == 0 {
		return ""
	}
	var probe struct {
		Target string `json:"target"`
		Field  string `json:"field"`
		Check  string `json:"check"`
	}
	if err := json.Unmarshal(a.Spec, &probe); err != nil {
		return ""
	}
	if probe.Target != "" {
		return probe.Target
	}
	if probe.Field != "" {
		return probe.Field
	}
	if probe.Check != "" {
		return "trace." + probe.Check
	}
	return ""
}

// truncate cuts s to maxDiagnosticBytes and appends an ellipsis. Cuts
// on byte boundary, then runs strings.ToValidUTF8 over the prefix so a
// multi-byte rune sliced at the cut site does not produce invalid UTF-8
// — the orphaned bytes are dropped rather than corrupting downstream
// JSON encoding or markdown rendering.
func truncate(s string) string {
	if len(s) <= maxDiagnosticBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxDiagnosticBytes-3], "") + "..."
}

// describeKeywordCheck renders a content-assertion check name + values
// pair as a one-line "expected" string, e.g. "contains 'foo'" or
// "keyword_all [foo bar]".
func describeKeywordCheck(check, value string, values []string) string {
	if value != "" {
		return fmt.Sprintf("%s %q", check, value)
	}
	if len(values) > 0 {
		return fmt.Sprintf("%s %s", check, strings.Join(values, ", "))
	}
	return check
}

// diagFields bundles the five diagnostic strings every evaluator
// populates — TraceNodePath/Expected/Actual/SuggestedAction — so
// failResultWithDiag and passResultWithDiag share one parameter shape.
type diagFields struct {
	target     string
	expected   string
	actual     string
	suggestion string
}

// failResultWithDiag builds a fail (hard or soft) AssertionResult with
// the standard diagnostic fields populated. Used by evaluators with
// many failure variants (e.g. content checks) to keep each branch a
// single line.
func failResultWithDiag(
	assertion *types.Assertion,
	start time.Time,
	status string,
	score float64,
	explanation string,
	d diagFields,
) *types.AssertionResult {
	return &types.AssertionResult{
		AssertionID:     assertion.AssertionID,
		Status:          status,
		Score:           score,
		Explanation:     explanation,
		DurationMS:      time.Since(start).Milliseconds(),
		RequestID:       assertion.RequestID,
		TraceNodePath:   d.target,
		Expected:        d.expected,
		Actual:          d.actual,
		SuggestedAction: d.suggestion,
	}
}
