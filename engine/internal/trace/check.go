package trace

import (
	"fmt"
	"sort"
	"strings"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Issue severities. Errors make `trace check` exit 1; warnings flag
// suspicious structure without failing.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Issue codes reported by Check.
const (
	IssueMultipleTraces     = "multiple_traces"
	IssueDuplicateSpanID    = "duplicate_span_id"
	IssueOrphanSpan         = "orphan_span"
	IssueParentCycle        = "parent_cycle"
	IssueUnpairedToolCall   = "unpaired_tool_call"
	IssueUnpairedToolResult = "unpaired_tool_result"
	IssueConversion         = "conversion_failed"
	IssueEndBeforeStart     = "end_before_start"
	IssueOutsideParent      = "outside_parent"
	IssueMultipleRoots      = "multiple_roots"
	IssueNoGenAISpans       = "no_genai_spans"
)

// Issue is one structural finding about a span.
type Issue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	SpanID   string `json:"span_id,omitempty"`
	Message  string `json:"message"`
}

// CoverageGap counts spans that lack an evidence field.
type CoverageGap struct {
	Field      string   `json:"field"`
	Attributes []string `json:"attributes"`
	SpanKind   string   `json:"span_kind"`
	Missing    int      `json:"missing"`
	Total      int      `json:"total"`
	SpanIDs    []string `json:"span_ids"`
}

// CheckReport is the result of `attest trace check`.
type CheckReport struct {
	TraceID       string         `json:"trace_id"`
	SchemaVersion int            `json:"schema_version"`
	Spans         int            `json:"spans"`
	Steps         map[string]int `json:"steps"`
	Turns         int            `json:"turns"`
	Handoffs      int            `json:"handoffs"`
	Retries       int            `json:"retries"`
	OK            bool           `json:"ok"`
	Issues        []Issue        `json:"issues"`
	Coverage      []CoverageGap  `json:"coverage"`
}

// Errors returns the error-severity issues.
func (r *CheckReport) Errors() []Issue {
	var out []Issue
	for _, is := range r.Issues {
		if is.Severity == SeverityError {
			out = append(out, is)
		}
	}
	return out
}

// Check reports the structural integrity and evidence coverage of one
// trace's spans. It never fails: problems become issues.
func Check(spans []Span) *CheckReport {
	r := &CheckReport{
		SchemaVersion: types.TraceSchemaVersion2,
		Spans:         len(spans),
		Steps:         map[string]int{},
		Issues:        []Issue{},
		Coverage:      []CoverageGap{},
	}
	if len(spans) == 0 {
		return r
	}
	r.TraceID = spans[0].TraceID
	x := newSpanIndex(spans)
	c := checker{x: x, r: r}
	if ids := traceIDs(spans); len(ids) > 1 {
		c.add(SeverityError, IssueMultipleTraces, "", fmt.Sprintf("input holds %d traces (%s); check one trace per file", len(ids), strings.Join(ids, ", ")))
	}
	c.checkParentage()
	c.checkToolPairing()
	c.coverage()

	if t, err := FromSpans(spans, ConvertOptions{}); err != nil {
		if len(traceIDs(spans)) == 1 {
			c.add(SeverityError, IssueConversion, "", err.Error())
		}
	} else {
		for _, s := range t.Steps {
			r.Steps[s.Type]++
			if s.RetryOf != nil {
				r.Retries++
			}
		}
		r.Turns = len(t.Turns)
		r.Handoffs = len(t.Handoffs)
		if len(t.Steps) == 0 && len(t.Turns) == 0 {
			c.add(SeverityWarning, IssueNoGenAISpans, "", "no span carries GenAI semantic convention attributes (gen_ai.operation.name)")
		}
	}
	r.OK = len(r.Errors()) == 0
	return r
}

type checker struct {
	x           *spanIndex
	r           *CheckReport
	toolParents map[string]bool // span ids with a tool span child
	hasModel    *bool
}

func (c *checker) add(severity, code, spanID, msg string) {
	c.r.Issues = append(c.r.Issues, Issue{Severity: severity, Code: code, SpanID: spanID, Message: msg})
}

func (c *checker) checkParentage() {
	seen := map[string]bool{}
	var roots []string
	for i := range c.x.spans {
		s := &c.x.spans[i]
		if seen[s.SpanID] {
			c.add(SeverityError, IssueDuplicateSpanID, s.SpanID, "span id appears more than once")
			continue
		}
		seen[s.SpanID] = true
		if s.EndNs < s.StartNs {
			c.add(SeverityWarning, IssueEndBeforeStart, s.SpanID, fmt.Sprintf("span %q ends before it starts", s.Name))
		}
		if s.ParentSpanID == "" {
			roots = append(roots, s.SpanID)
			continue
		}
		p, ok := c.x.byID[s.ParentSpanID]
		if !ok {
			c.add(SeverityError, IssueOrphanSpan, s.SpanID, fmt.Sprintf("span %q names parent %s, which is not in the trace", s.Name, s.ParentSpanID))
			continue
		}
		if c.inCycle(i) {
			c.add(SeverityError, IssueParentCycle, s.SpanID, fmt.Sprintf("span %q is its own ancestor", s.Name))
			continue
		}
		ps := &c.x.spans[p]
		if s.StartNs < ps.StartNs || s.EndNs > ps.EndNs {
			c.add(SeverityWarning, IssueOutsideParent, s.SpanID, fmt.Sprintf("span %q runs outside its parent %s (%q)", s.Name, ps.SpanID, ps.Name))
		}
	}
	if len(roots) > 1 {
		c.add(SeverityWarning, IssueMultipleRoots, "", fmt.Sprintf("trace has %d root spans (%s)", len(roots), strings.Join(roots, ", ")))
	}
	if len(roots) == 0 {
		c.add(SeverityWarning, IssueMultipleRoots, "", "trace has no root span")
	}
}

// inCycle reports whether span i lies on a parent cycle.
func (c *checker) inCycle(i int) bool {
	return c.x.cyclic[i]
}

// checkToolPairing pairs model tool requests with tool executions.
//
// Without recorded content, a model span whose finish reasons include
// "tool_calls" must be followed by at least one tool span (a later sibling
// before the next model span, or a child), and every tool span must follow
// such a model span. With recorded output messages, each requested tool
// call id must match a tool span's gen_ai.tool.call.id and vice versa.
func (c *checker) checkToolPairing() {
	groups := map[string][]int{} // parent span id → children in start order
	var order []string
	c.toolParents = map[string]bool{}
	for i := range c.x.spans {
		pid := c.x.spans[i].ParentSpanID
		if _, ok := groups[pid]; !ok {
			order = append(order, pid)
		}
		groups[pid] = append(groups[pid], i)
		if c.x.kinds[i] == types.StepTypeToolCall {
			c.toolParents[pid] = true
		}
	}
	for _, pid := range order {
		c.pairSiblings(groups[pid])
	}
	c.pairByCallID()
}

func (c *checker) pairSiblings(members []int) {
	pending := -1 // model span awaiting a tool execution
	for _, i := range members {
		switch c.x.kinds[i] {
		case types.StepTypeLLMCall:
			c.flushPending(pending)
			pending = -1
			if requestsTools(&c.x.spans[i]) && !c.hasToolChild(i) {
				pending = i
			}
		case types.StepTypeToolCall:
			if pending >= 0 {
				pending = -1
				continue
			}
			c.checkToolHasRequest(i, members)
		}
	}
	c.flushPending(pending)
}

func (c *checker) flushPending(i int) {
	if i < 0 {
		return
	}
	s := &c.x.spans[i]
	c.add(SeverityError, IssueUnpairedToolCall, s.SpanID, fmt.Sprintf("model span %q finished with tool_calls but no tool span executes the call", s.Name))
}

// checkToolHasRequest flags a tool span whose preceding model span (its
// parent, or the latest earlier model sibling) did not request tools.
func (c *checker) checkToolHasRequest(i int, siblings []int) {
	s := &c.x.spans[i]
	if p := c.x.parent(i); p >= 0 && c.x.kinds[p] == types.StepTypeLLMCall {
		return
	}
	prev := -1
	for _, j := range siblings {
		if j == i {
			break
		}
		if c.x.kinds[j] == types.StepTypeLLMCall {
			prev = j
		}
	}
	if prev < 0 {
		if c.anyModelSpan() {
			c.add(SeverityError, IssueUnpairedToolResult, s.SpanID, fmt.Sprintf("tool span %q has no preceding model span that requested it", s.Name))
		}
		return
	}
	if _, known := c.x.spans[prev].Attributes[attrFinishReasons]; known && !requestsTools(&c.x.spans[prev]) {
		c.add(SeverityError, IssueUnpairedToolResult, s.SpanID, fmt.Sprintf("tool span %q follows model span %s, which did not finish with tool_calls", s.Name, c.x.spans[prev].SpanID))
	}
}

func (c *checker) anyModelSpan() bool {
	if c.hasModel == nil {
		found := false
		for _, k := range c.x.kinds {
			if k == types.StepTypeLLMCall {
				found = true
				break
			}
		}
		c.hasModel = &found
	}
	return *c.hasModel
}

func (c *checker) hasToolChild(i int) bool {
	return c.toolParents[c.x.spans[i].SpanID]
}

// pairByCallID matches tool call ids requested in recorded output messages
// with tool spans' gen_ai.tool.call.id. It runs only when some model span
// records output messages.
func (c *checker) pairByCallID() {
	requested := map[string]string{} // call id → model span id
	recorded := false
	for i := range c.x.spans {
		if c.x.kinds[i] != types.StepTypeLLMCall {
			continue
		}
		msgs, ok := decodeJSONText(c.x.spans[i].Attributes[attrOutputMessages]).([]any)
		if !ok {
			continue
		}
		recorded = true
		for _, id := range toolCallIDs(msgs) {
			requested[id] = c.x.spans[i].SpanID
		}
	}
	if !recorded {
		return
	}
	executed := map[string]bool{}
	for i := range c.x.spans {
		if c.x.kinds[i] != types.StepTypeToolCall {
			continue
		}
		id, _ := c.x.spans[i].Attributes[attrToolCallID].(string)
		if id == "" {
			continue
		}
		executed[id] = true
		if _, ok := requested[id]; !ok {
			c.add(SeverityError, IssueUnpairedToolResult, c.x.spans[i].SpanID, fmt.Sprintf("tool call id %s was not requested by any recorded model output", id))
		}
	}
	ids := make([]string, 0, len(requested))
	for id := range requested {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !executed[id] {
			c.add(SeverityError, IssueUnpairedToolCall, requested[id], fmt.Sprintf("model requested tool call id %s, but no tool span records it", id))
		}
	}
}

// toolCallIDs returns the ids of tool_call parts in GenAI output messages.
func toolCallIDs(msgs []any) []string {
	var ids []string
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "tool_call" {
				continue
			}
			if id, ok := part["id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func requestsTools(s *Span) bool {
	reasons, ok := s.Attributes[attrFinishReasons].([]any)
	if !ok {
		if one, isStr := s.Attributes[attrFinishReasons].(string); isStr {
			reasons = []any{one}
		}
	}
	for _, r := range reasons {
		if r == "tool_calls" || r == "tool_call" {
			return true
		}
	}
	return false
}

// coverageRule names evidence a span kind should record.
type coverageRule struct {
	field    string
	kind     string
	attrs    []string
	complete func(a map[string]any) bool
}

var coverageRules = []coverageRule{
	{"tokens", types.StepTypeLLMCall, []string{attrInputTokens, attrOutputTokens}, func(a map[string]any) bool {
		return hasAll(a, attrInputTokens, attrOutputTokens)
	}},
	{"cost", types.StepTypeLLMCall, []string{attrCost}, func(a map[string]any) bool {
		return hasAll(a, attrCost)
	}},
	{"content", types.StepTypeLLMCall, []string{attrInputMessages, attrOutputMessages}, func(a map[string]any) bool {
		return (hasAll(a, attrInputMessages) || hasAll(a, attrPrompt)) && (hasAll(a, attrOutputMessages) || hasAll(a, attrCompletion))
	}},
	{"model", types.StepTypeLLMCall, []string{attrResponseModel, attrRequestModel}, func(a map[string]any) bool {
		return hasAny(a, attrResponseModel, attrRequestModel)
	}},
	{"tool_call_id", types.StepTypeToolCall, []string{attrToolCallID}, func(a map[string]any) bool {
		return hasAll(a, attrToolCallID)
	}},
	{"tool_content", types.StepTypeToolCall, []string{attrToolArguments, attrToolResult}, func(a map[string]any) bool {
		return hasAll(a, attrToolArguments, attrToolResult)
	}},
}

func hasAll(a map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := a[k]; !ok {
			return false
		}
	}
	return true
}

func (c *checker) coverage() {
	for _, rule := range coverageRules {
		gap := CoverageGap{Field: rule.field, Attributes: rule.attrs, SpanKind: rule.kind, SpanIDs: []string{}}
		for i := range c.x.spans {
			if c.x.kinds[i] != rule.kind {
				continue
			}
			gap.Total++
			if !rule.complete(c.x.spans[i].Attributes) {
				gap.Missing++
				gap.SpanIDs = append(gap.SpanIDs, c.x.spans[i].SpanID)
			}
		}
		if gap.Missing > 0 {
			c.r.Coverage = append(c.r.Coverage, gap)
		}
	}
}
