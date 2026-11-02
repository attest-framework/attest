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
	IssueRemoteParent       = "remote_parent"
	IssueUnrequestedTool    = "unrequested_tool"
	IssueEndBeforeStart     = "end_before_start"
	IssueOutsideParent      = "outside_parent"
	IssueMultipleRoots      = "multiple_roots"
	IssueNoRoot             = "no_root"
	IssueNoGenAISpans       = "no_genai_spans"
)

// toolRequestReasons are finish reasons with which providers end a model
// call that requests tools (OpenAI, Anthropic and Bedrock, legacy OpenAI).
var toolRequestReasons = map[string]bool{
	"tool_calls":    true,
	"tool_call":     true,
	"tool_use":      true,
	"function_call": true,
}

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
	multiple := len(traceIDs(spans)) > 1
	if multiple {
		ids := traceIDs(spans)
		c.add(SeverityError, IssueMultipleTraces, "", fmt.Sprintf("input holds %d traces (%s); check one trace per file", len(ids), strings.Join(ids, ", ")))
	}
	c.checkParentage()
	c.checkToolPairing()
	c.coverage()

	if t, err := FromSpans(spans, ConvertOptions{}); err != nil {
		if !multiple {
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
	x *spanIndex
	r *CheckReport
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
		p := c.x.parentOf[i]
		if p < 0 {
			if c.x.remoteRoot {
				c.add(SeverityWarning, IssueRemoteParent, s.SpanID, fmt.Sprintf("span %q is a local root; its parent %s is outside this export", s.Name, s.ParentSpanID))
				continue
			}
			c.add(SeverityError, IssueOrphanSpan, s.SpanID, fmt.Sprintf("span %q names parent %s, which is not in the trace", s.Name, s.ParentSpanID))
			continue
		}
		if c.x.cyclic[i] {
			c.add(SeverityError, IssueParentCycle, s.SpanID, fmt.Sprintf("span %q is its own ancestor", s.Name))
			continue
		}
		ps := &c.x.spans[p]
		if s.StartNs < ps.StartNs || s.EndNs > ps.EndNs {
			c.add(SeverityWarning, IssueOutsideParent, s.SpanID, fmt.Sprintf("span %q runs outside its parent %s (%q)", s.Name, ps.SpanID, ps.Name))
		}
	}
	switch {
	case len(roots) > 1:
		c.add(SeverityWarning, IssueMultipleRoots, "", fmt.Sprintf("trace has %d root spans (%s)", len(roots), strings.Join(roots, ", ")))
	case len(roots) == 0 && !c.x.remoteRoot:
		c.add(SeverityWarning, IssueNoRoot, "", "trace has no root span")
	}
}

// checkToolPairing pairs model tool requests with tool executions inside
// each agent scope (the spans under one invoke_agent span, or the spans under
// no agent), in start order, so tools may be siblings of the model call,
// its children, or under separate framework node spans.
//
// Without recorded content a model call that finished with a tool request
// must be followed by a tool span before the next model call in its scope,
// and a tool span must follow some model call in its scope. Where the
// requesting model call recorded gen_ai.output.messages, tool call ids are
// matched exactly instead.
func (c *checker) checkToolPairing() {
	scopes := map[int][]int{} // nearest invoke_agent span (-1 for none) → spans
	var order []int
	for i := range c.x.spans {
		k := c.x.kinds[i]
		if k != types.StepTypeToolCall && !c.x.isChatModel(i) || c.x.detached(i) {
			continue // detached spans are already reported once
		}
		scope := c.x.agentOf[i]
		if _, ok := scopes[scope]; !ok {
			order = append(order, scope)
		}
		scopes[scope] = append(scopes[scope], i)
	}
	requested := map[string]string{} // recorded tool call id → model span id
	executed := map[string]bool{}    // tool call ids on tool spans
	for _, scope := range order {
		c.pairScope(scopes[scope], requested, executed)
	}
	ids := make([]string, 0, len(requested))
	for id := range requested {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !executed[id] {
			c.add(SeverityError, IssueUnpairedToolCall, requested[id], fmt.Sprintf("model requested tool call id %q, but no tool span records it", id))
		}
	}
}

func (c *checker) pairScope(members []int, requested map[string]string, executed map[string]bool) {
	pending := -1 // model call awaiting a tool execution (no recorded content)
	lastModel := -1
	var scopeIDs map[string]bool // ids requested by recorded model calls in scope
	for _, i := range members {
		s := &c.x.spans[i]
		if c.x.kinds[i] != types.StepTypeToolCall {
			c.flushPending(pending)
			pending, lastModel = -1, i
			if ids, recorded := recordedToolCallIDs(s); recorded {
				if scopeIDs == nil {
					scopeIDs = map[string]bool{}
				}
				for _, id := range ids {
					requested[id] = s.SpanID
					scopeIDs[id] = true
				}
				continue
			}
			if requestsTools(s) {
				pending = i
			}
			continue
		}
		pending = -1
		id, _ := s.Attributes[attrToolCallID].(string)
		if id != "" {
			executed[id] = true
		}
		switch {
		case lastModel < 0:
			c.add(SeverityError, IssueUnpairedToolResult, s.SpanID, fmt.Sprintf("tool span %q has no model call before it in its agent scope", s.Name))
		case hasRecordedOutput(&c.x.spans[lastModel]):
			if id != "" && !scopeIDs[id] {
				c.add(SeverityError, IssueUnpairedToolResult, s.SpanID, fmt.Sprintf("tool call id %q was not requested by any recorded model output", id))
			}
		case c.x.parentOf[i] == lastModel:
			// A tool span nested under the model call is its execution.
		case knownNonToolFinish(&c.x.spans[lastModel]):
			c.add(SeverityWarning, IssueUnrequestedTool, s.SpanID, fmt.Sprintf("tool span %q follows model call %s, whose finish reasons do not request tools", s.Name, c.x.spans[lastModel].SpanID))
		}
	}
	c.flushPending(pending)
}

func (c *checker) flushPending(i int) {
	if i < 0 {
		return
	}
	s := &c.x.spans[i]
	c.add(SeverityError, IssueUnpairedToolCall, s.SpanID, fmt.Sprintf("model span %q finished with a tool request but no tool span executes it", s.Name))
}

func hasRecordedOutput(s *Span) bool {
	_, ok := decodeJSONText(s.Attributes[attrOutputMessages]).([]any)
	return ok
}

// recordedToolCallIDs returns the tool call ids in a model span's recorded
// output messages, and whether output messages were recorded.
func recordedToolCallIDs(s *Span) ([]string, bool) {
	msgs, ok := decodeJSONText(s.Attributes[attrOutputMessages]).([]any)
	if !ok {
		return nil, false
	}
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
	return ids, true
}

func finishReasons(s *Span) []string {
	var out []string
	switch v := s.Attributes[attrFinishReasons].(type) {
	case string:
		out = append(out, v)
	case []any:
		for _, r := range v {
			if str, ok := r.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out
}

func requestsTools(s *Span) bool {
	for _, r := range finishReasons(s) {
		if toolRequestReasons[strings.ToLower(r)] {
			return true
		}
	}
	return false
}

// knownNonToolFinish reports a model call whose finish reasons are recorded
// and none of them requests tools. Some providers (Gemini) finish tool
// requests with "STOP", so this is only a warning.
func knownNonToolFinish(s *Span) bool {
	return len(finishReasons(s)) > 0 && !requestsTools(s)
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
