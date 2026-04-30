package assertion

import (
	"fmt"
	"strings"

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

// truncate cuts s to maxDiagnosticBytes, appending an ellipsis when it
// truncated. Operates on bytes, not runes — overshoot by one rune is
// preferable to scanning every multibyte character on every assertion.
func truncate(s string) string {
	if len(s) <= maxDiagnosticBytes {
		return s
	}
	return s[:maxDiagnosticBytes-3] + "..."
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
