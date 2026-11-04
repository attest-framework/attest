package trace

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// OpenTelemetry GenAI semantic convention attribute keys read by the
// converter. Deprecated keys (gen_ai.system, gen_ai.prompt,
// gen_ai.completion) are still read because existing producers emit them.
const (
	attrOperation      = "gen_ai.operation.name"
	attrProvider       = "gen_ai.provider.name"
	attrSystem         = "gen_ai.system"
	attrRequestModel   = "gen_ai.request.model"
	attrResponseModel  = "gen_ai.response.model"
	attrFinishReasons  = "gen_ai.response.finish_reasons"
	attrInputTokens    = "gen_ai.usage.input_tokens"
	attrOutputTokens   = "gen_ai.usage.output_tokens"
	attrCost           = "gen_ai.usage.cost"
	attrInputMessages  = "gen_ai.input.messages"
	attrOutputMessages = "gen_ai.output.messages"
	attrSystemInstr    = "gen_ai.system_instructions"
	attrPrompt         = "gen_ai.prompt"
	attrCompletion     = "gen_ai.completion"
	attrConversationID = "gen_ai.conversation.id"
	attrAgentName      = "gen_ai.agent.name"
	attrAgentID        = "gen_ai.agent.id"
	attrAgentDesc      = "gen_ai.agent.description"
	attrToolName       = "gen_ai.tool.name"
	attrToolCallID     = "gen_ai.tool.call.id"
	attrToolArguments  = "gen_ai.tool.call.arguments"
	attrToolResult     = "gen_ai.tool.call.result"
	attrRetrievalQuery = "gen_ai.retrieval.query"
	attrRetrievalSrc   = "gen_ai.retrieval.source"
	attrRetrievalCount = "gen_ai.retrieval.documents.count"
	attrRetrievalDocs  = "gen_ai.retrieval.documents"
	attrErrorType      = "error.type"

	// Claude Code native trace attributes (traces beta). They stand in for
	// the GenAI keys on claude_code.llm_request spans when those are absent.
	attrNativeModel         = "model"
	attrNativeInputTokens   = "input_tokens"
	attrNativeOutputTokens  = "output_tokens"
	attrNativeCacheRead     = "cache_read_tokens"
	attrNativeCacheCreation = "cache_creation_tokens"
	attrToolOutputEvent     = "output"
	// LangSmith (OTel mode) marks every span it exports with this attribute.
	attrLangSmithKind = "langsmith.span.kind"

	opInvokeAgent = "invoke_agent"
)

// operationStep maps gen_ai.operation.name to a step type.
var operationStep = map[string]string{
	"chat":             types.StepTypeLLMCall,
	"completion":       types.StepTypeLLMCall,
	"text_completion":  types.StepTypeLLMCall,
	"generate_content": types.StepTypeLLMCall,
	"embeddings":       types.StepTypeLLMCall,
	"execute_tool":     types.StepTypeToolCall,
	"tool":             types.StepTypeToolCall,
	opInvokeAgent:      types.StepTypeAgentCall,
	"create_agent":     types.StepTypeAgentCall,
	"retrieval":        types.StepTypeRetrieval,
	"retrieve":         types.StepTypeRetrieval,
}

// claudeCodeSpans classifies Claude Code's native spans, which carry no
// gen_ai.operation.name, by span name. Its tool and interaction spans carry
// the operation (execute_tool, invoke_agent); the tool.execution and
// tool.blocked_on_user children are timing detail of the tool span and add no
// step.
const spanClaudeLLMRequest = "claude_code.llm_request"

var claudeCodeSpans = map[string]string{
	spanClaudeLLMRequest:               types.StepTypeLLMCall,
	"claude_code.tool.execution":       "",
	"claude_code.tool.blocked_on_user": "",
}

// ConvertOptions controls OTLP → Trace conversion.
type ConvertOptions struct {
	// AgentID overrides the trace agent_id. Empty uses the first turn's
	// gen_ai.agent.name (or gen_ai.agent.id).
	AgentID string
}

// ConvertOTLP parses an OTLP/JSON export and converts it to a Trace v2.
func ConvertOTLP(data []byte, opts ConvertOptions) (*types.Trace, error) {
	spans, err := ParseOTLP(data)
	if err != nil {
		return nil, err
	}
	return FromSpans(spans, opts)
}

// spanIndex is the parent/child view of one trace's spans.
type spanIndex struct {
	spans    []Span // sorted by start time, then input order
	byID     map[string]int
	kinds    []string // step type per span, "" when not a GenAI step
	ops      []string // normalized gen_ai.operation.name
	parentOf []int    // parent span index, -1 for a root or orphan
	cyclic   []bool   // span lies on a parent cycle
	agentOf  []int    // nearest invoke_agent ancestor, -1 for none
	topAgent []int    // outermost invoke_agent ancestor-or-self, -1 for none
	// root is the earliest span without a parent id or, when there is none,
	// the earliest span whose parent is outside the export; -1 for none.
	// remoteRoot is set in the second case: every span with a missing parent
	// is then a local root.
	root       int
	remoteRoot bool
}

// operation returns the normalized gen_ai.operation.name of a span.
func operation(s *Span) string {
	op, _ := s.Attributes[attrOperation].(string)
	return strings.ToLower(strings.TrimSpace(op))
}

// classify returns the step type of a span, or "" when it is not a GenAI
// operation. gen_ai.operation.name is authoritative: an operation the engine
// does not know (chain, agent_step, ...) is not a step, and the attribute
// families are not consulted. Claude Code's native span names, attribute
// families and the span name's first token are consulted only when the
// operation is absent.
func classify(s *Span) string {
	if op := operation(s); op != "" {
		return operationStep[op]
	}
	if t, ok := claudeCodeSpans[s.Name]; ok {
		return t
	}
	a := s.Attributes
	if _, ok := a[attrToolName]; ok {
		return types.StepTypeToolCall
	}
	if hasAny(a, attrCompletion, attrPrompt, attrInputMessages, attrOutputMessages) {
		return types.StepTypeLLMCall
	}
	if hasAny(a, attrAgentName, attrAgentID) {
		return types.StepTypeAgentCall
	}
	for k := range a {
		if strings.HasPrefix(k, "gen_ai.retrieval.") {
			return types.StepTypeRetrieval
		}
	}
	if fields := strings.Fields(s.Name); len(fields) > 0 {
		return operationStep[strings.ToLower(fields[0])]
	}
	return ""
}

func hasAny(a map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := a[k]; ok {
			return true
		}
	}
	return false
}

// newSpanIndex sorts spans and resolves parents and agent ancestry in one
// pass, so orphans and parent cycles terminate and long chains stay linear.
func newSpanIndex(in []Span) *spanIndex {
	spans := make([]Span, len(in))
	copy(spans, in)
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartNs != spans[j].StartNs {
			return spans[i].StartNs < spans[j].StartNs
		}
		return spans[i].Order < spans[j].Order
	})
	n := len(spans)
	idx := &spanIndex{
		spans:    spans,
		byID:     make(map[string]int, n),
		kinds:    make([]string, n),
		ops:      make([]string, n),
		parentOf: make([]int, n),
		cyclic:   make([]bool, n),
		agentOf:  make([]int, n),
		topAgent: make([]int, n),
	}
	for i := range spans {
		if _, dup := idx.byID[spans[i].SpanID]; !dup {
			idx.byID[spans[i].SpanID] = i
		}
		idx.kinds[i] = classify(&spans[i])
		idx.ops[i] = operation(&spans[i])
	}
	for i := range spans {
		idx.parentOf[i] = -1
		if pid := spans[i].ParentSpanID; pid != "" {
			if p, ok := idx.byID[pid]; ok {
				idx.parentOf[i] = p
			}
		}
	}
	idx.resolveAncestry()
	idx.root = -1
	firstMissing := -1
	for i := range spans {
		if spans[i].ParentSpanID == "" {
			idx.root = i
			break
		}
		if idx.parentOf[i] < 0 && firstMissing < 0 {
			firstMissing = i
		}
	}
	if idx.root < 0 && firstMissing >= 0 {
		// No span is a true root: this export is one service's part of a
		// distributed trace, and spans with a missing parent are its local
		// roots. The earliest is the trace root.
		idx.root, idx.remoteRoot = firstMissing, true
	}
	return idx
}

// detached reports a span cut off from the trace: its parent is missing
// (and it is not a local root of a partial export) or it lies on a cycle.
func (x *spanIndex) detached(i int) bool {
	if x.cyclic[i] {
		return true
	}
	return x.spans[i].ParentSpanID != "" && x.parentOf[i] < 0 && !x.remoteRoot
}

// isChatModel reports whether span i is a model call that can request tools
// (any LLM operation except embeddings).
func (x *spanIndex) isChatModel(i int) bool {
	return x.kinds[i] == types.StepTypeLLMCall && x.ops[i] != "embeddings"
}

func (x *spanIndex) isInvokeAgent(i int) bool {
	return x.kinds[i] == types.StepTypeAgentCall && (x.ops[i] == opInvokeAgent || x.ops[i] == "")
}

// parent returns the index of a span's parent, or -1.
func (x *spanIndex) parent(i int) int {
	return x.parentOf[i]
}

// resolveAncestry fills cyclic, agentOf and topAgent. Each span is visited
// once: a walk up the parent chain stops at a resolved span, a root, or a
// span already on the current walk (a cycle), then resolves the walk from
// the top down. Spans on a cycle are treated as having no parent.
func (x *spanIndex) resolveAncestry() {
	n := len(x.spans)
	const (
		unvisited = iota
		onWalk
		resolved
	)
	state := make([]uint8, n)
	topAbove := make([]int, n) // outermost invoke_agent strictly above
	var walk []int
	for i := 0; i < n; i++ {
		if state[i] != unvisited {
			continue
		}
		walk = walk[:0]
		cur := i
		for cur >= 0 && state[cur] == unvisited {
			state[cur] = onWalk
			walk = append(walk, cur)
			cur = x.parentOf[cur]
		}
		if cur >= 0 && state[cur] == onWalk {
			for k := len(walk) - 1; k >= 0; k-- {
				x.cyclic[walk[k]] = true
				if walk[k] == cur {
					break
				}
			}
		}
		for k := len(walk) - 1; k >= 0; k-- {
			s := walk[k]
			x.agentOf[s], topAbove[s] = -1, -1
			if p := x.parentOf[s]; p >= 0 && !x.cyclic[s] {
				x.agentOf[s], topAbove[s] = x.agentOf[p], topAbove[p]
				if x.isInvokeAgent(p) {
					x.agentOf[s] = p
					if topAbove[s] < 0 {
						topAbove[s] = p
					}
				}
			}
			x.topAgent[s] = topAbove[s]
			if x.topAgent[s] < 0 && x.isInvokeAgent(s) {
				x.topAgent[s] = s
			}
			state[s] = resolved
		}
	}
}

// FromSpans converts the spans of one trace to a Trace v2. Spans from more
// than one trace id are an error.
func FromSpans(spans []Span, opts ConvertOptions) (*types.Trace, error) {
	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans to convert")
	}
	if ids := traceIDs(spans); len(ids) > 1 {
		return nil, fmt.Errorf("input holds %d traces (%s); convert one trace per file", len(ids), listIDs(ids))
	}
	if n := contentBytes(spans); n > MaxTraceSize {
		return nil, fmt.Errorf("recorded content is %d bytes; a trace holds at most %d", n, MaxTraceSize)
	}
	x := newSpanIndex(spans)
	c := converter{x: x, stepOf: make(map[int]int), texts: make(map[int]string)}
	if err := c.buildSteps(); err != nil {
		return nil, err
	}
	if err := c.buildTurns(); err != nil {
		return nil, err
	}
	if err := c.buildHandoffs(); err != nil {
		return nil, err
	}
	c.markRetries()

	t := &types.Trace{
		SchemaVersion: types.TraceSchemaVersion2,
		TraceID:       "otel_" + x.spans[0].TraceID[:16],
		AgentID:       opts.AgentID,
		Input:         json.RawMessage(`{}`),
		Steps:         c.steps,
		Turns:         c.turns,
		Handoffs:      c.handoffs,
	}
	if t.AgentID == "" && len(c.turnSpans) > 0 {
		t.AgentID = agentName(&x.spans[c.turnSpans[0]], "")
	}
	if len(c.turnSpans) > 0 {
		if msgs, ok := x.spans[c.turnSpans[0]].Attributes[attrInputMessages]; ok {
			t.Input = mustJSON(map[string]any{"messages": decodeJSONText(msgs)})
		}
	}
	text := c.outputText()
	if err := c.charge(jsonSize(text)); err != nil {
		return nil, err
	}
	t.Output = mustJSON(map[string]any{"message": text})
	for i := range x.spans {
		if id, ok := x.spans[i].Attributes[attrConversationID].(string); ok && id != "" {
			t.ConversationID = id
			break
		}
	}
	t.Metadata = c.metadata()
	if rpcErr := Validate(t, 0); rpcErr != nil {
		return nil, fmt.Errorf("converted trace is invalid: %s", rpcErr.Message)
	}
	return t, nil
}

// maxListedIDs bounds how many ids one message lists.
const maxListedIDs = 10

// listIDs joins at most maxListedIDs ids for a message.
func listIDs(ids []string) string {
	if len(ids) <= maxListedIDs {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:maxListedIDs], ", ") + fmt.Sprintf(", … (%d more)", len(ids)-maxListedIDs)
}

func traceIDs(spans []Span) []string {
	seen := map[string]bool{}
	var ids []string
	for i := range spans {
		if !seen[spans[i].TraceID] {
			seen[spans[i].TraceID] = true
			ids = append(ids, spans[i].TraceID)
		}
	}
	return ids
}

type converter struct {
	x         *spanIndex
	steps     []types.Step
	stepSpan  []int       // span index per step
	stepOf    map[int]int // span index → step index
	turnSpans []int
	turns     []types.Turn
	handoffs  []types.Handoff

	texts map[int]string // span index → model output text
	// outBytes is a running upper bound on the converted trace's encoded
	// size; conversion stops as soon as it passes MaxTraceSize, before a
	// value copied many times (an agent name in every handoff) is built.
	outBytes    int
	totalTokens *int
	costUSD     *float64
	model       *string
}

// stepOverhead bounds the encoded size of a step's keys, punctuation and
// numeric fields.
const stepOverhead = 256

// charge adds n bytes to the converted trace's size bound and fails once it
// exceeds the trace limit.
func (c *converter) charge(n int) error {
	c.outBytes += n
	if c.outBytes > MaxTraceSize {
		return fmt.Errorf("converted trace would exceed %d bytes; a trace holds at most %d", c.outBytes, MaxTraceSize)
	}
	return nil
}

func stepSize(s *types.Step) int {
	n := stepOverhead + len(s.Args) + len(s.Result) + len(s.Metadata) +
		jsonSize(s.Name) + jsonSize(s.AgentID) + jsonSize(s.AgentRole) + jsonSize(s.ToolCallID) +
		len(s.SpanID) + len(s.ParentSpanID)
	if s.Error != nil {
		n += jsonSize(s.Error.Type) + jsonSize(s.Error.Message)
	}
	if s.Exchange != nil {
		n += len(s.Exchange.Request) + len(s.Exchange.Response)
	}
	return n
}

func (c *converter) buildSteps() error {
	c.steps = make([]types.Step, 0, len(c.x.spans))
	for i := range c.x.spans {
		kind := c.x.kinds[i]
		if kind == "" {
			continue
		}
		if c.x.isInvokeAgent(i) && c.x.topAgent[i] == i {
			c.turnSpans = append(c.turnSpans, i)
			continue
		}
		step, err := c.buildStep(i, kind)
		if err != nil {
			s := &c.x.spans[i]
			return fmt.Errorf("span %s (%s): %w", s.SpanID, quoteShort(s.Name), err)
		}
		c.stepOf[i] = len(c.steps)
		c.stepSpan = append(c.stepSpan, i)
		if err := c.charge(stepSize(&step)); err != nil {
			return err
		}
		c.steps = append(c.steps, step)
	}
	return nil
}

func (c *converter) buildStep(i int, kind string) (types.Step, error) {
	s := &c.x.spans[i]
	a := s.Attributes
	startMs := int64(s.StartNs / 1_000_000)
	endMs := int64(s.EndNs / 1_000_000)
	durationMs := int64(0)
	if s.EndNs > s.StartNs {
		durationMs = int64((s.EndNs - s.StartNs) / 1_000_000)
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = kind
	}
	step := types.Step{
		Type:         kind,
		Name:         name,
		Metadata:     mustJSON(map[string]any{"duration_ms": durationMs}),
		StartedAtMs:  &startMs,
		EndedAtMs:    &endMs,
		SpanID:       s.SpanID,
		ParentSpanID: s.ParentSpanID,
	}
	step.AgentID, _ = a[attrAgentID].(string)
	step.AgentRole, _ = a[attrAgentName].(string)
	if s.StatusError {
		errType, _ := a[attrErrorType].(string)
		if errType == "" {
			errType = "error"
		}
		step.Error = &types.StepError{Type: errType, Message: s.StatusMessage}
	}

	var args, result map[string]any
	var err error
	switch kind {
	case types.StepTypeLLMCall:
		args, result, err = c.llmStep(i, &step, a)
	case types.StepTypeToolCall:
		if name, ok := a[attrToolName].(string); ok && name != "" {
			step.Name = name
		}
		step.ToolCallID, _ = a[attrToolCallID].(string)
		args, result = toolPayloads(s)
	case types.StepTypeAgentCall:
		step.Name = agentName(s, name)
		args = pick(a, map[string]string{attrAgentID: "agent_id", attrAgentName: "agent_name", attrAgentDesc: "description", attrPrompt: "prompt"})
		result = pick(a, map[string]string{attrCompletion: "completion"})
	case types.StepTypeRetrieval:
		args = pick(a, map[string]string{attrRetrievalQuery: "query", attrRetrievalSrc: "source"})
		result = pick(a, map[string]string{attrRetrievalCount: "documents_count", attrRetrievalDocs: "documents"})
	}
	if err != nil {
		return types.Step{}, err
	}
	step.Args = mustJSON(args)
	step.Result = mustJSON(result)
	return step, nil
}

// tokenAttr reads a token count from the GenAI key, or, on a Claude Code
// llm_request span without it, from the native key.
func tokenAttr(a map[string]any, key, nativeKey string, claude bool) (int, bool, error) {
	n, ok, err := intAttr(a, key)
	if err != nil || ok || !claude {
		return n, ok, err
	}
	return intAttr(a, nativeKey)
}

// nativeModel is Claude Code's model attribute, read only on its llm_request
// spans.
func nativeModel(a map[string]any, claude bool) (string, bool) {
	if !claude {
		return "", false
	}
	m, ok := a[attrNativeModel].(string)
	return m, ok && m != ""
}

// claudeUsage records a Claude Code llm_request span's model (when the GenAI
// request model is absent) and cache token counts.
func claudeUsage(a, args, result map[string]any) error {
	if _, ok := args["model"]; !ok {
		if m, ok := nativeModel(a, true); ok {
			args["model"] = m
		}
	}
	for _, c := range [...]struct{ attr, key string }{{attrNativeCacheRead, "cache_read_tokens"}, {attrNativeCacheCreation, "cache_creation_tokens"}} {
		attr, key := c.attr, c.key
		n, ok, err := intAttr(a, attr)
		if err != nil {
			return err
		}
		if ok {
			result[key] = n
		}
	}
	return nil
}

// toolPayloads returns a tool span's arguments and result. The GenAI
// attributes win. A result falls back to the output attribute of a tool.output
// span event (Claude Code), then to gen_ai.completion; arguments fall back to
// gen_ai.prompt (LangSmith). LangSmith wraps run inputs and outputs, which
// are unwrapped only on spans it exported.
func toolPayloads(s *Span) (args, result map[string]any) {
	a := s.Attributes
	_, langSmith := a[attrLangSmithKind]
	if _, ok := a[attrToolArguments]; ok {
		args = objectOf(a, attrToolArguments, "arguments")
	} else if v, ok := a[attrPrompt]; ok {
		args = payloadObject(v, "input", "arguments", langSmith)
	} else {
		args = map[string]any{}
	}
	if _, ok := a[attrToolResult]; ok {
		return args, objectOf(a, attrToolResult, "result")
	}
	for _, ev := range s.Events {
		if out, ok := ev.Attributes[attrToolOutputEvent]; ok && ev.Name == EventToolOutput {
			return args, payloadObject(out, "", "result", false)
		}
	}
	if v, ok := a[attrCompletion]; ok {
		return args, payloadObject(v, "output", "result", langSmith)
	}
	return args, map[string]any{}
}

// payloadObject decodes a recorded payload into an object. A value that is not
// a JSON object is kept under wrapKey. With unwrap, a sole wrapper key and a
// LangChain tool message ({"type": "tool", "content": ...}) are removed
// first; nothing else is.
func payloadObject(v any, wrapper, wrapKey string, unwrap bool) map[string]any {
	d := decodeJSONText(v)
	if unwrap {
		if m, ok := d.(map[string]any); ok && len(m) == 1 {
			if inner, ok := m[wrapper]; ok {
				d = decodeJSONText(inner)
			}
		}
		if m, ok := d.(map[string]any); ok && m["type"] == "tool" {
			if content, ok := m["content"]; ok {
				d = decodeJSONText(content)
			}
		}
	}
	if obj, ok := d.(map[string]any); ok {
		return obj
	}
	return map[string]any{wrapKey: d}
}

func (c *converter) llmStep(i int, step *types.Step, a map[string]any) (args, result map[string]any, err error) {
	args = pick(a, map[string]string{attrRequestModel: "model", attrSystem: "system", attrProvider: "provider", attrPrompt: "prompt"})
	if op, _ := a[attrOperation].(string); op != "" {
		args["operation"] = strings.ToLower(strings.TrimSpace(op))
	}
	result = pick(a, map[string]string{attrCompletion: "completion", attrResponseModel: "model", attrFinishReasons: "finish_reasons"})
	claude := c.x.spans[i].Name == spanClaudeLLMRequest
	in, inOK, err := tokenAttr(a, attrInputTokens, attrNativeInputTokens, claude)
	if err != nil {
		return nil, nil, err
	}
	out, outOK, err := tokenAttr(a, attrOutputTokens, attrNativeOutputTokens, claude)
	if err != nil {
		return nil, nil, err
	}
	if claude {
		if err := claudeUsage(a, args, result); err != nil {
			return nil, nil, err
		}
	}
	if inOK {
		result["input_tokens"] = in
	}
	if outOK {
		result["output_tokens"] = out
	}
	if inOK || outOK {
		total := in + out
		if c.totalTokens != nil {
			total += *c.totalTokens
		}
		c.totalTokens = &total
	}
	if raw, ok := a[attrCost]; ok {
		cost, isNum := toFloat(raw)
		if !isNum || cost < 0 {
			return nil, nil, fmt.Errorf("%s must be a non-negative number", attrCost)
		}
		result["cost_usd"] = cost
		total := cost
		if c.costUSD != nil {
			total += *c.costUSD
		}
		if math.IsInf(total, 0) {
			return nil, nil, fmt.Errorf("%s total overflows", attrCost)
		}
		c.costUSD = &total
	}
	if c.model == nil {
		if m, ok := a[attrResponseModel].(string); ok && m != "" {
			c.model = &m
		} else if m, ok := a[attrRequestModel].(string); ok && m != "" {
			c.model = &m
		} else if m, ok := nativeModel(a, claude); ok {
			c.model = &m
		}
	}
	if text := llmText(a); text != "" {
		c.texts[i] = text
	}
	inMsgs, hasIn := a[attrInputMessages]
	outMsgs, hasOut := a[attrOutputMessages]
	if hasIn && hasOut {
		req := pick(a, map[string]string{attrRequestModel: "model"})
		req["messages"] = decodeJSONText(inMsgs)
		if sys, ok := a[attrSystemInstr]; ok {
			req["system_instructions"] = decodeJSONText(sys)
		}
		resp := pick(a, map[string]string{attrResponseModel: "model", attrFinishReasons: "finish_reasons"})
		resp["messages"] = decodeJSONText(outMsgs)
		step.Exchange = &types.Exchange{Request: mustJSON(req), Response: mustJSON(resp)}
	}
	return args, result, nil
}

// buildTurns makes one turn per top-level invoke_agent span, holding every
// step in its subtree.
func (c *converter) buildTurns() error {
	if len(c.turnSpans) == 0 {
		return nil
	}
	turnOf := make(map[int]int, len(c.turnSpans))
	c.turns = make([]types.Turn, len(c.turnSpans))
	for n, si := range c.turnSpans {
		turnOf[si] = n
		s := &c.x.spans[si]
		turn := types.Turn{Index: n, SpanID: s.SpanID, StepIndices: []int{}}
		if v, ok := s.Attributes[attrInputMessages]; ok {
			turn.Input = mustJSON(decodeJSONText(v))
		}
		if v, ok := s.Attributes[attrOutputMessages]; ok {
			turn.Output = mustJSON(decodeJSONText(v))
		}
		if err := c.charge(stepOverhead + len(turn.Input) + len(turn.Output) + len(turn.SpanID)); err != nil {
			return err
		}
		c.turns[n] = turn
	}
	for stepIdx, si := range c.stepSpan {
		if top := c.x.topAgent[si]; top >= 0 {
			n := turnOf[top]
			c.turns[n].StepIndices = append(c.turns[n].StepIndices, stepIdx)
		}
	}
	return nil
}

// buildHandoffs records an invoke_agent span nested under another agent as
// a handoff from the enclosing agent.
func (c *converter) buildHandoffs() error {
	for stepIdx, si := range c.stepSpan {
		if !c.x.isInvokeAgent(si) {
			continue
		}
		from := c.x.agentOf[si]
		if from < 0 {
			continue
		}
		fromName := agentName(&c.x.spans[from], c.x.spans[from].SpanID)
		toName := agentName(&c.x.spans[si], c.x.spans[si].SpanID)
		if fromName == toName {
			continue
		}
		if err := c.charge(stepOverhead + jsonSize(fromName) + jsonSize(toName)); err != nil {
			return err
		}
		c.handoffs = append(c.handoffs, types.Handoff{
			FromAgent: fromName,
			ToAgent:   toName,
			StepIndex: stepIdx,
		})
	}
	return nil
}

// markRetries sets retry_of on a step that repeats a failed sibling under the
// same parent: a tool call with the same tool name and arguments as an
// earlier failed call, or a model call with the same name and request model
// directly after a failed model call. Each step is matched in constant time.
func (c *converter) markRetries() {
	type toolKey struct{ parent, name, args string }
	failedTool := map[toolKey]int{} // latest failed tool call per key
	lastModel := map[string]int{}   // latest model call per parent
	for i := range c.steps {
		cur := &c.steps[i]
		switch cur.Type {
		case types.StepTypeToolCall:
			key := toolKey{cur.ParentSpanID, cur.Name, string(cur.Args)}
			if j, ok := failedTool[key]; ok {
				prev := j
				cur.RetryOf = &prev
			}
			if cur.Error != nil {
				failedTool[key] = i
			} else {
				delete(failedTool, key)
			}
		case types.StepTypeLLMCall:
			if j, ok := lastModel[cur.ParentSpanID]; ok {
				prev := &c.steps[j]
				if prev.Error != nil && prev.Name == cur.Name && requestModel(prev) == requestModel(cur) {
					prevIdx := j
					cur.RetryOf = &prevIdx
				}
			}
			lastModel[cur.ParentSpanID] = i
		}
	}
}

// requestModel is the gen_ai.request.model recorded in a model step's args.
func requestModel(s *types.Step) string {
	var args struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(s.Args, &args); err != nil {
		return ""
	}
	return args.Model
}

// outputText is the trace's final answer: the text of the last turn's
// recorded output messages, else the text of the model call that ended last
// directly inside a top-level agent (or anywhere, when no model call is).
func (c *converter) outputText() string {
	if n := len(c.turnSpans); n > 0 {
		if text := messagesText(c.x.spans[c.turnSpans[n-1]].Attributes[attrOutputMessages]); text != "" {
			return text
		}
	}
	best, bestOuter := -1, false
	for i, text := range c.texts {
		if text == "" {
			continue
		}
		outer := c.x.agentOf[i] == c.x.topAgent[i]
		switch {
		case best < 0, outer && !bestOuter:
		case outer == bestOuter && c.endsAfter(i, best):
		default:
			continue
		}
		best, bestOuter = i, outer
	}
	if best < 0 {
		return ""
	}
	return c.texts[best]
}

// endsAfter orders spans by end time, then by start order.
func (c *converter) endsAfter(i, j int) bool {
	a, b := &c.x.spans[i], &c.x.spans[j]
	if a.EndNs != b.EndNs {
		return a.EndNs > b.EndNs
	}
	return i > j
}

func (c *converter) metadata() *types.TraceMetadata {
	m := &types.TraceMetadata{TotalTokens: c.totalTokens, CostUSD: c.costUSD, Model: c.model}
	if root := c.rootSpan(); root >= 0 {
		s := &c.x.spans[root]
		if s.EndNs >= s.StartNs {
			latency := int((s.EndNs - s.StartNs) / 1_000_000)
			m.LatencyMS = &latency
		}
	}
	if m.TotalTokens == nil && m.CostUSD == nil && m.Model == nil && m.LatencyMS == nil {
		return nil
	}
	return m
}

// rootSpan is the earliest span without a parent, or -1.
func (c *converter) rootSpan() int {
	return c.x.root
}

func agentName(s *Span, fallback string) string {
	if n, ok := s.Attributes[attrAgentName].(string); ok && n != "" {
		return n
	}
	if id, ok := s.Attributes[attrAgentID].(string); ok && id != "" {
		return id
	}
	return fallback
}

// pick copies the present attributes in keys (attribute → output key).
func pick(a map[string]any, keys map[string]string) map[string]any {
	out := map[string]any{}
	for attr, key := range keys {
		if v, ok := a[attr]; ok {
			out[key] = v
		}
	}
	return out
}

// objectOf returns the JSON object recorded in attribute attr. A value that
// is not a JSON object is kept under wrapKey; an absent attribute yields {}.
func objectOf(a map[string]any, attr, wrapKey string) map[string]any {
	v, ok := a[attr]
	if !ok {
		return map[string]any{}
	}
	decoded := decodeJSONText(v)
	if obj, isObj := decoded.(map[string]any); isObj {
		return obj
	}
	return map[string]any{wrapKey: decoded}
}

// decodeJSONText decodes a string attribute holding JSON (semconv records
// messages and tool arguments as JSON strings). Other values, and strings
// that are not JSON, are returned unchanged.
func decodeJSONText(v any) any {
	s, ok := v.(string)
	if !ok || len(s) > MaxStepPayload {
		return v
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return v
	}
	var out any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return v
	}
	return out
}

// copiedAttributes are every attribute the converter copies into the trace:
// recorded content (decoded from JSON text) and the values pick() copies.
var copiedAttributes = []string{
	attrInputMessages, attrOutputMessages, attrSystemInstr, attrToolArguments, attrToolResult,
	attrRequestModel, attrResponseModel, attrSystem, attrProvider, attrPrompt, attrCompletion,
	attrFinishReasons, attrCost, attrInputTokens, attrOutputTokens, attrConversationID,
	attrAgentName, attrAgentID, attrAgentDesc, attrToolName, attrToolCallID, attrErrorType,
	attrRetrievalQuery, attrRetrievalSrc, attrRetrievalCount, attrRetrievalDocs,
	attrNativeModel, attrNativeInputTokens, attrNativeOutputTokens, attrNativeCacheRead,
	attrNativeCacheCreation,
}

// contentBytes estimates the JSON size of everything the converter copies
// from the spans (attribute values, span names, status messages), counting
// JSON escaping and the turn messages that appear twice, so oversized input
// is rejected before any of it is decoded. Values the converter copies more
// than once (an agent name into every handoff, output text into the trace
// output) are bounded while converting by converter.charge.
func contentBytes(spans []Span) int {
	n := 0
	for i := range spans {
		s := &spans[i]
		n += jsonSize(s.Name) + jsonSize(s.StatusMessage)
		for _, ev := range s.Events {
			n += jsonSize(ev.Attributes[attrToolOutputEvent])
		}
		turnSpan := operation(s) == opInvokeAgent
		for _, k := range copiedAttributes {
			v, ok := s.Attributes[k]
			if !ok {
				continue
			}
			size := jsonSize(v)
			if turnSpan && (k == attrInputMessages || k == attrOutputMessages) {
				size *= 2 // turn input/output and trace input
			}
			n += size
		}
	}
	return n
}

// jsonSize is an upper bound on the encoded size of an attribute value:
// every character encoding/json escapes (quotes, backslashes, control
// characters, <, >, &, U+2028, U+2029, invalid UTF-8) counts as six bytes.
func jsonSize(v any) int {
	switch x := v.(type) {
	case string:
		n := 2
		for i, r := range x {
			switch {
			case r == utf8.RuneError && !strings.HasPrefix(x[i:], "\uFFFD"):
				n += 6 // invalid UTF-8 byte → \ufffd
			case r < 0x20, r == '"', r == '\\', r == '<', r == '>', r == '&', r == '\u2028', r == '\u2029':
				n += 6
			default:
				n += utf8.RuneLen(r)
			}
		}
		return n
	case []any:
		n := 2
		for _, e := range x {
			n += jsonSize(e) + 1
		}
		return n
	case map[string]any:
		n := 2
		for k, e := range x {
			n += jsonSize(k) + jsonSize(e) + 2
		}
		return n
	}
	return 24 // numbers and booleans
}

// llmText is the model's text output: gen_ai.completion, else the text
// parts of the last gen_ai.output.messages entry.
func llmText(a map[string]any) string {
	if s, ok := a[attrCompletion].(string); ok && s != "" {
		return s
	}
	return messagesText(a[attrOutputMessages])
}

// messagesText joins the text parts of the last GenAI message in v (a JSON
// string or decoded list).
func messagesText(v any) string {
	msgs, ok := decodeJSONText(v).([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	last, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok {
		return ""
	}
	parts, _ := last["parts"].([]any)
	var b strings.Builder
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok || part["type"] != "text" {
			continue
		}
		if text, ok := part["content"].(string); ok {
			b.WriteString(text)
		}
	}
	return b.String()
}

func intAttr(a map[string]any, key string) (int, bool, error) {
	v, ok := a[key]
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case int64:
		if n < 0 || n > 1<<31-1 {
			return 0, false, fmt.Errorf("%s is out of range: %d", key, n)
		}
		return int(n), true, nil
	case float64:
		if n < 0 || n > 1<<31-1 || n != float64(int64(n)) {
			return 0, false, fmt.Errorf("%s must be a non-negative integer, got %v", key, n)
		}
		return int(n), true, nil
	}
	return 0, false, fmt.Errorf("%s must be numeric per the GenAI semantic conventions, got %T", key, v)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// mustJSON marshals values built from decoded JSON and OTLP scalars, which
// always encode.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("trace: marshal converted value: %v", err))
	}
	return b
}
