package trace

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

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
	// root is the earliest span without a parent id, or else the only span
	// whose parent is outside the export (a remote parent); -1 for none.
	root       int
	remoteRoot bool
}

// operation returns the normalized gen_ai.operation.name of a span.
func operation(s *Span) string {
	op, _ := s.Attributes[attrOperation].(string)
	return strings.ToLower(strings.TrimSpace(op))
}

// classify returns the step type of a span, or "" when it is not a GenAI
// operation. gen_ai.operation.name is authoritative; attribute families and
// the span name's first token are consulted only when it is absent.
func classify(s *Span) string {
	if op := operation(s); op != "" {
		if t, ok := operationStep[op]; ok {
			return t
		}
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
	var missing []int
	for i := range spans {
		if spans[i].ParentSpanID == "" {
			idx.root = i
			break
		}
		if idx.parentOf[i] < 0 {
			missing = append(missing, i)
		}
	}
	if idx.root < 0 && len(missing) == 1 {
		idx.root, idx.remoteRoot = missing[0], true
	}
	return idx
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
		return nil, fmt.Errorf("input holds %d traces (%s); convert one trace per file", len(ids), strings.Join(ids, ", "))
	}
	if n := contentBytes(spans); n > MaxTraceSize {
		return nil, fmt.Errorf("recorded content is %d bytes; a trace holds at most %d", n, MaxTraceSize)
	}
	x := newSpanIndex(spans)
	c := converter{x: x, stepOf: make(map[int]int), texts: make(map[int]string)}
	if err := c.buildSteps(); err != nil {
		return nil, err
	}
	c.buildTurns()
	c.buildHandoffs()
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
	t.Output = mustJSON(map[string]any{"message": c.outputText()})
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

	texts       map[int]string // span index → model output text
	totalTokens *int
	costUSD     *float64
	model       *string
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
			return fmt.Errorf("span %s (%q): %w", s.SpanID, s.Name, err)
		}
		c.stepOf[i] = len(c.steps)
		c.stepSpan = append(c.stepSpan, i)
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
		args = objectOf(a, attrToolArguments, "arguments")
		result = objectOf(a, attrToolResult, "result")
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

func (c *converter) llmStep(i int, step *types.Step, a map[string]any) (args, result map[string]any, err error) {
	args = pick(a, map[string]string{attrRequestModel: "model", attrSystem: "system", attrProvider: "provider", attrPrompt: "prompt"})
	if op, _ := a[attrOperation].(string); op != "" {
		args["operation"] = strings.ToLower(strings.TrimSpace(op))
	}
	result = pick(a, map[string]string{attrCompletion: "completion", attrResponseModel: "model", attrFinishReasons: "finish_reasons"})
	in, inOK, err := intAttr(a, attrInputTokens)
	if err != nil {
		return nil, nil, err
	}
	out, outOK, err := intAttr(a, attrOutputTokens)
	if err != nil {
		return nil, nil, err
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
func (c *converter) buildTurns() {
	if len(c.turnSpans) == 0 {
		return
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
		c.turns[n] = turn
	}
	for stepIdx, si := range c.stepSpan {
		if top := c.x.topAgent[si]; top >= 0 {
			n := turnOf[top]
			c.turns[n].StepIndices = append(c.turns[n].StepIndices, stepIdx)
		}
	}
}

// buildHandoffs records an invoke_agent span nested under another agent as
// a handoff from the enclosing agent.
func (c *converter) buildHandoffs() {
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
		c.handoffs = append(c.handoffs, types.Handoff{
			FromAgent: fromName,
			ToAgent:   toName,
			StepIndex: stepIdx,
		})
	}
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

// contentAttributes are the attributes whose JSON text is decoded into the
// trace.
var contentAttributes = []string{attrInputMessages, attrOutputMessages, attrSystemInstr, attrToolArguments, attrToolResult}

// contentBytes is the total length of recorded content strings, which bounds
// the size of the converted trace before any of it is decoded.
func contentBytes(spans []Span) int {
	n := 0
	for i := range spans {
		for _, k := range contentAttributes {
			if s, ok := spans[i].Attributes[k].(string); ok {
				n += len(s)
			}
		}
	}
	return n
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
