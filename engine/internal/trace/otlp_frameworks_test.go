package trace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Framework routes: the sanitized exports of each framework's OTel
// instrumentation under protocol-tests/trace-v2/otlp. Golden equality is
// asserted by TestConvertOTLP_MatchesGoldenTraces; these cases pin the
// contract each route must satisfy for capture.
func TestConvertOTLP_FrameworkRoutes(t *testing.T) {
	cases := []struct {
		fixture    string
		llm, tool  int
		agents     int
		toolArgs   map[string]any
		toolResult map[string]any
	}{
		{"pydantic-ai-v5", 2, 1, 0, map[string]any{"order_id": "O-1"}, map[string]any{"result": "shipped"}},
		{"vercel-ai-modern", 2, 1, 0, map[string]any{"order_id": "O-1"}, map[string]any{"result": `"shipped"`}},
		{"mastra", 2, 1, 0, map[string]any{"order_id": "O-1"}, map[string]any{"result": "shipped"}},
		{"openai-agents-v2", 2, 1, 1, map[string]any{"order_id": "O-1"}, map[string]any{"result": "shipped"}},
		{"claude-code-py", 2, 1, 0, map[string]any{}, map[string]any{"result": "shipped"}},
		{"claude-code-ts", 2, 1, 0, map[string]any{}, map[string]any{"result": "shipped"}},
		{"langsmith-py", 2, 1, 0, map[string]any{"order_id": "O-1"}, map[string]any{"result": "shipped"}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			data := readFixture(t, "otlp/"+c.fixture+".otlp.json")
			tr, err := ConvertOTLP(data, ConvertOptions{})
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			var tool *types.Step
			for i := range tr.Steps {
				counts[tr.Steps[i].Type]++
				if tr.Steps[i].Type == types.StepTypeToolCall {
					tool = &tr.Steps[i]
				}
			}
			if counts[types.StepTypeLLMCall] != c.llm || counts[types.StepTypeToolCall] != c.tool || counts[types.StepTypeAgentCall] != c.agents {
				t.Fatalf("step counts = %v, want llm %d tool %d agent %d", counts, c.llm, c.tool, c.agents)
			}
			if tool.Name != "lookup_order" {
				t.Errorf("tool name = %q, want lookup_order", tool.Name)
			}
			assertJSONObject(t, "tool args", tool.Args, c.toolArgs)
			assertJSONObject(t, "tool result", tool.Result, c.toolResult)

			spans, err := ParseOTLP(data)
			if err != nil {
				t.Fatal(err)
			}
			report := Check(spans)
			if !report.OK {
				t.Errorf("trace check errors: %+v", report.Errors())
			}
			for _, is := range report.Issues {
				if is.Code == IssueUnrequestedTool || is.Code == IssueUnpairedToolCall || is.Code == IssueUnpairedToolResult {
					t.Errorf("unexpected issue %s: %s", is.Code, is.Message)
				}
			}
		})
	}
}

func assertJSONObject(t *testing.T, what string, raw json.RawMessage, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// Vacuous exports (Vercel legacy ai.* telemetry, OpenInference) convert to a
// trace without model or tool steps.
func TestConvertOTLP_VacuousRoutesHaveNoModelOrToolSteps(t *testing.T) {
	for _, p := range fixturePaths(t, "otlp-vacuous/*.otlp.json") {
		t.Run(filepath.Base(p), func(t *testing.T) {
			tr, err := ConvertOTLP(readFixture(t, "otlp-vacuous/"+filepath.Base(p)), ConvertOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tr.Steps {
				if s.Type == types.StepTypeLLMCall || s.Type == types.StepTypeToolCall {
					t.Errorf("step %s %q from a route attest does not map", s.Type, s.Name)
				}
			}
		})
	}
}

func fwSpan(t *testing.T, id, parent, name string, start, end int, attrs map[string]any, events ...string) string {
	t.Helper()
	s := spanJSON(t, id, parent, name, start, end, attrs)
	if len(events) > 0 {
		s = strings.TrimSuffix(s, "}") + `,"events":[` + strings.Join(events, ",") + `]}`
	}
	return s
}

func toolOutputEvent(t *testing.T, value string) string {
	t.Helper()
	b, _ := json.Marshal(value)
	return `{"name":"tool.output","attributes":[{"key":"output","value":{"stringValue":` + string(b) + `}}]}`
}

func convertSpans(t *testing.T, spans ...string) (*types.Trace, error) {
	t.Helper()
	return ConvertOTLP(otlpDoc(spans...), ConvertOptions{})
}

func stepTypes(tr *types.Trace) []string {
	var out []string
	for _, s := range tr.Steps {
		out = append(out, s.Type)
	}
	return out
}

func TestConvert_UnknownOperationYieldsNoStepAndKeepsChildren(t *testing.T) {
	tr, err := convertSpans(t,
		fwSpan(t, "0000000000000001", "", "root", 1, 100, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "a"}),
		// A chain span carrying prompt/completion must not fall into the heuristic.
		fwSpan(t, "0000000000000002", "0000000000000001", "chain", 2, 90, map[string]any{"gen_ai.operation.name": "chain", "gen_ai.prompt": "p", "gen_ai.completion": "c"}),
		fwSpan(t, "0000000000000003", "0000000000000002", "agent_step", 3, 80, map[string]any{"gen_ai.operation.name": "agent_step", "gen_ai.tool.name": "x"}),
		fwSpan(t, "0000000000000004", "0000000000000003", "model", 4, 40, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "m"}),
		fwSpan(t, "0000000000000005", "0000000000000003", "unknown", 41, 60, map[string]any{"gen_ai.operation.name": "unknown", "gen_ai.agent.name": "b"}),
		fwSpan(t, "0000000000000006", "0000000000000005", "tool", 42, 50, map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "t"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepTypes(tr), []string{types.StepTypeLLMCall, types.StepTypeToolCall}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if len(tr.Turns) != 1 || len(tr.Turns[0].StepIndices) != 2 {
		t.Errorf("turn does not hold both descendants of the unknown spans: %+v", tr.Turns)
	}
}

func TestConvert_SpanWithoutOperationStillUsesAttributeHeuristics(t *testing.T) {
	tr, err := convertSpans(t, fwSpan(t, "0000000000000001", "", "llm", 1, 2, map[string]any{"gen_ai.prompt": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := stepTypes(tr); !reflect.DeepEqual(got, []string{types.StepTypeLLMCall}) {
		t.Fatalf("steps = %v, want one llm_call", got)
	}
}

func TestConvert_ClaudeCodeSpans(t *testing.T) {
	root := fwSpan(t, "0000000000000001", "", "claude_code.interaction", 1, 100, map[string]any{"gen_ai.operation.name": "invoke_agent"})
	llm := func(attrs map[string]any) string {
		return fwSpan(t, "0000000000000002", "0000000000000001", "claude_code.llm_request", 2, 10, attrs)
	}
	t.Run("native usage fills in absent GenAI keys", func(t *testing.T) {
		tr, err := convertSpans(t, root, llm(map[string]any{"model": "claude-x", "input_tokens": 12, "output_tokens": 7, "cache_read_tokens": 3, "cache_creation_tokens": 4}))
		if err != nil {
			t.Fatal(err)
		}
		assertJSONObject(t, "args", tr.Steps[0].Args, map[string]any{"model": "claude-x"})
		assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{"input_tokens": 12.0, "output_tokens": 7.0, "cache_read_tokens": 3.0, "cache_creation_tokens": 4.0})
		if tr.Metadata == nil || tr.Metadata.Model == nil || *tr.Metadata.Model != "claude-x" || *tr.Metadata.TotalTokens != 19 {
			t.Errorf("metadata = %+v", tr.Metadata)
		}
	})
	t.Run("GenAI keys win over native keys", func(t *testing.T) {
		tr, err := convertSpans(t, root, llm(map[string]any{
			"gen_ai.request.model": "genai-model", "model": "claude-x",
			"gen_ai.usage.input_tokens": 1, "gen_ai.usage.output_tokens": 2, "input_tokens": 12, "output_tokens": 7,
		}))
		if err != nil {
			t.Fatal(err)
		}
		assertJSONObject(t, "args", tr.Steps[0].Args, map[string]any{"model": "genai-model"})
		assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{"input_tokens": 1.0, "output_tokens": 2.0})
	})
	t.Run("native keys are ignored on other spans", func(t *testing.T) {
		tr, err := convertSpans(t, root, fwSpan(t, "0000000000000002", "0000000000000001", "chat m", 2, 10, map[string]any{"gen_ai.operation.name": "chat", "input_tokens": 12, "model": "x"}))
		if err != nil {
			t.Fatal(err)
		}
		assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{})
	})
	t.Run("non-numeric native tokens are skipped and reported by trace check", func(t *testing.T) {
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"} {
			data := otlpDoc(root, llm(map[string]any{key: "twelve"}))
			tr, err := ConvertOTLP(data, ConvertOptions{})
			if err != nil {
				t.Errorf("%s: %v", key, err)
				continue
			}
			var res map[string]any
			_ = json.Unmarshal(tr.Steps[0].Result, &res)
			if _, ok := res[key]; ok {
				t.Errorf("%s: result kept the malformed value: %v", key, res)
			}
			spans, _ := ParseOTLP(data)
			found := false
			for _, is := range Check(spans).Issues {
				if is.Code == IssueMalformedNative && strings.Contains(is.Message, key) && is.Severity == SeverityWarning {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no malformed_native_attribute warning", key)
			}
		}
	})
	tool := func(attrs map[string]any, events ...string) string {
		base := map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "lookup_order"}
		for k, v := range attrs {
			base[k] = v
		}
		return fwSpan(t, "0000000000000003", "0000000000000001", "claude_code.tool", 11, 30, base, events...)
	}
	children := []string{
		fwSpan(t, "0000000000000004", "0000000000000003", "claude_code.tool.execution", 12, 20, map[string]any{"gen_ai.tool.call.id": "c1"}),
		fwSpan(t, "0000000000000005", "0000000000000003", "claude_code.tool.blocked_on_user", 12, 13, map[string]any{"gen_ai.provider.name": "anthropic"}),
	}
	t.Run("tool result from the tool.output event; children add no step", func(t *testing.T) {
		tr, err := convertSpans(t, append([]string{root, tool(nil, toolOutputEvent(t, `{"status":"shipped"}`))}, children...)...)
		if err != nil {
			t.Fatal(err)
		}
		if got := stepTypes(tr); !reflect.DeepEqual(got, []string{types.StepTypeToolCall}) {
			t.Fatalf("steps = %v", got)
		}
		assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{"status": "shipped"})
	})
	t.Run("GenAI result wins over the event", func(t *testing.T) {
		tr, err := convertSpans(t, root, tool(map[string]any{"gen_ai.tool.call.result": "from-attr"}, toolOutputEvent(t, "from-event")))
		if err != nil {
			t.Fatal(err)
		}
		assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{"result": "from-attr"})
	})
	t.Run("event without output, other events and non-string output", func(t *testing.T) {
		for name, ev := range map[string]string{
			"no attributes":     `{"name":"tool.output"}`,
			"no output key":     `{"name":"tool.output","attributes":[{"key":"other","value":{"stringValue":"x"}}]}`,
			"other event name":  `{"name":"gen_ai.request.attempt","attributes":[{"key":"output","value":{"stringValue":"x"}}]}`,
			"non-string output": `{"name":"tool.output","attributes":[{"key":"output","value":{"intValue":"7"}}]}`,
			"null attributes":   `{"name":"tool.output","attributes":null}`,
		} {
			tr, err := convertSpans(t, root, tool(nil, ev))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			var got map[string]any
			_ = json.Unmarshal(tr.Steps[0].Result, &got)
			if name == "non-string output" {
				if !reflect.DeepEqual(got, map[string]any{"result": 7.0}) {
					t.Errorf("%s: result = %v", name, got)
				}
			} else if len(got) != 0 {
				t.Errorf("%s: result = %v, want {}", name, got)
			}
		}
	})
	t.Run("a huge output is bounded by the trace limit", func(t *testing.T) {
		_, err := convertSpans(t, root, tool(nil, toolOutputEvent(t, strings.Repeat("x", MaxTraceSize+1))))
		if err == nil {
			t.Fatal("converted a tool output above the trace limit")
		}
	})
}

func TestConvert_LangSmithToolPayloads(t *testing.T) {
	root := fwSpan(t, "0000000000000001", "", "LangGraph", 1, 100, map[string]any{"gen_ai.operation.name": "chain"})
	tool := func(attrs map[string]any) string {
		base := map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "lookup_order"}
		for k, v := range attrs {
			base[k] = v
		}
		return fwSpan(t, "0000000000000002", "0000000000000001", "lookup_order", 2, 10, base)
	}
	run := func(t *testing.T, attrs map[string]any) (args, result map[string]any) {
		t.Helper()
		tr, err := convertSpans(t, root, tool(attrs))
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(tr.Steps[0].Args, &args)
		_ = json.Unmarshal(tr.Steps[0].Result, &result)
		return args, result
	}
	msg := `{"output":{"content":"shipped","type":"tool","name":"lookup_order","tool_call_id":"c1","status":"success"}}`
	t.Run("prompt and completion become arguments and result", func(t *testing.T) {
		args, result := run(t, map[string]any{"langsmith.span.kind": "tool", "gen_ai.prompt": `{"order_id":"O-1"}`, "gen_ai.completion": msg})
		if !reflect.DeepEqual(args, map[string]any{"order_id": "O-1"}) || !reflect.DeepEqual(result, map[string]any{"result": "shipped"}) {
			t.Errorf("args %v result %v", args, result)
		}
	})
	t.Run("arguments are never unwrapped, so a parameter named input survives", func(t *testing.T) {
		for _, attrs := range []map[string]any{
			{"langsmith.span.kind": "tool", "gen_ai.prompt": `{"input":"O-1"}`},
			{"gen_ai.prompt": `{"input":"O-1"}`},
		} {
			args, _ := run(t, attrs)
			if !reflect.DeepEqual(args, map[string]any{"input": "O-1"}) {
				t.Errorf("%v: args = %v, want the recorded object", attrs, args)
			}
		}
	})
	t.Run("a tool message outside LangSmith is not unwrapped", func(t *testing.T) {
		_, result := run(t, map[string]any{"gen_ai.completion": msg})
		if _, ok := result["output"]; !ok {
			t.Errorf("result = %v, want the recorded object", result)
		}
	})
	t.Run("GenAI arguments and result win", func(t *testing.T) {
		args, result := run(t, map[string]any{
			"langsmith.span.kind": "tool", "gen_ai.prompt": `{"order_id":"O-2"}`, "gen_ai.completion": msg,
			"gen_ai.tool.call.arguments": `{"order_id":"O-1"}`, "gen_ai.tool.call.result": "ok",
		})
		if !reflect.DeepEqual(args, map[string]any{"order_id": "O-1"}) || !reflect.DeepEqual(result, map[string]any{"result": "ok"}) {
			t.Errorf("args %v result %v", args, result)
		}
	})
	t.Run("wrong JSON is kept as the raw string", func(t *testing.T) {
		for _, raw := range []string{`{"order_id":`, `{'order_id': 'O-1'}`, `[1,2`, `not json`, `{"input": `, ``} {
			args, result := run(t, map[string]any{"langsmith.span.kind": "tool", "gen_ai.prompt": raw, "gen_ai.completion": raw})
			if got, ok := args["arguments"]; !ok || got != raw {
				t.Errorf("%q: args = %v", raw, args)
			}
			if got, ok := result["result"]; !ok || got != raw {
				t.Errorf("%q: result = %v", raw, result)
			}
		}
	})
	t.Run("non-object wrappers and nested tool messages stay in range", func(t *testing.T) {
		for _, raw := range []string{`{"output":null}`, `{"output":[1]}`, `{"output":"text"}`, `{"output":{"type":"tool"}}`, `{"output":{"type":"tool","content":["a"]}}`, `{"type":"tool","content":{"output":"x"}}`} {
			if _, err := convertSpans(t, root, tool(map[string]any{"langsmith.span.kind": "tool", "gen_ai.completion": raw})); err != nil {
				t.Errorf("%s: %v", raw, err)
			}
		}
	})
	t.Run("a prompt above the step payload limit is an error, not a panic", func(t *testing.T) {
		big := `{"order_id":"` + strings.Repeat("x", MaxStepPayload) + `"}`
		if _, err := convertSpans(t, root, tool(map[string]any{"langsmith.span.kind": "tool", "gen_ai.prompt": big})); err == nil {
			t.Error("converted a step above the payload limit")
		}
	})
}

func TestCheck_ToolRequestFinishReasonSpellings(t *testing.T) {
	for _, reason := range []any{"tool-calls", "tool_call", "tool_calls", "tool_use", "TOOL-CALLS", `["tool-calls"]`, []any{"tool-calls"}} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			s := &Span{Attributes: map[string]any{attrFinishReasons: reason}}
			if !requestsTools(s) {
				t.Errorf("finish reasons %v do not request tools", reason)
			}
		})
	}
	for _, reason := range []any{"stop", `["stop"]`, `[`, `["tool-calls"`, `[1]`, `{"a":1}`, strings.Repeat("[", maxFinishReasonsText+1), []any{1}} {
		if requestsTools(&Span{Attributes: map[string]any{attrFinishReasons: reason}}) {
			t.Errorf("finish reasons %.40v request tools", reason)
		}
	}
}

func TestParseOTLP_SpanEvents(t *testing.T) {
	span := func(events string) []byte {
		s := spanJSON(t, "0000000000000001", "", "s", 1, 2, nil)
		return otlpDoc(strings.TrimSuffix(s, "}") + `,"events":` + events + `}`)
	}
	t.Run("only tool.output events are kept, at most four", func(t *testing.T) {
		var evs []string
		for i := 0; i < 6; i++ {
			evs = append(evs, toolOutputEvent(t, fmt.Sprint("o", i)), `{"name":"other","attributes":[{"key":"k","value":{"stringValue":"v"}}]}`)
		}
		spans, err := ParseOTLP(span("[" + strings.Join(evs, ",") + "]"))
		if err != nil {
			t.Fatal(err)
		}
		if got := spans[0].Events; len(got) != maxKeptEvents || got[0].Name != EventToolOutput || got[0].Attributes["output"] != "o0" {
			t.Errorf("events = %+v", got)
		}
	})
	t.Run("null and empty events", func(t *testing.T) {
		for _, ev := range []string{`null`, `[]`, `[{}]`, `[{"name":null,"attributes":null}]`, `[{"timeUnixNano":"1","droppedAttributesCount":0}]`} {
			if _, err := ParseOTLP(span(ev)); err != nil {
				t.Errorf("%s: %v", ev, err)
			}
		}
	})
	t.Run("invalid JSON in events is an error", func(t *testing.T) {
		for _, ev := range []string{`{}`, `"x"`, `[1`, `[{"name":"tool.output","attributes":[{"key":"output","value":`} {
			if _, err := ParseOTLP(span(ev)); err == nil {
				t.Errorf("%s: parsed, want an error", ev)
			}
		}
	})
	t.Run("a malformed tool.output event is dropped and reported by trace check", func(t *testing.T) {
		for _, ev := range []string{`[{"name":"tool.output","attributes":{}}]`, `[{"name":"tool.output","attributes":[1]}]`, `[{"name":"tool.output","attributes":[{"key":"","value":{"stringValue":"x"}}]}]`} {
			spans, err := ParseOTLP(span(ev))
			if err != nil || len(spans[0].Events) != 0 || spans[0].DroppedEvents != 1 {
				t.Errorf("%s: err %v, events %v, dropped %d", ev, err, spans[0].Events, spans[0].DroppedEvents)
				continue
			}
			warned := false
			for _, is := range Check(spans).Issues {
				warned = warned || (is.Code == IssueMalformedEvent && is.Severity == SeverityWarning)
			}
			if !warned {
				t.Errorf("%s: no malformed_event warning", ev)
			}
		}
	})
	t.Run("events other than tool.output are never read", func(t *testing.T) {
		for _, ev := range []string{
			`[1]`, `["x"]`, `[{"name":1}]`, `[{"attributes":{}}]`,
			`[{"name":"gen_ai.user.message","attributes":[{"key":"","value":1}]}]`,
			// Text that merely mentions tool.output does not make an event one.
			`[{"name":"exception","attributes":[{"key":"exception.message","value":{"stringValue":"tool.output failed"}},1]}]`,
			`[{"name":"x","attributes":[{"key":"","value":{"stringValue":"tool.output"}}]}]`,
			// The key is exactly "name".
			`[{"NAME":"tool.output","attributes":[{"key":"output","value":{"stringValue":"x"}}]}]`,
			`[{"Name":"tool.output","attributes":[{"key":"output","value":{"stringValue":"x"}}]}]`,
			`[{"name":"exception","message":"tool.output","attributes":[null,{"key":"k"}]}]`,
		} {
			spans, err := ParseOTLP(span(ev))
			if err != nil || len(spans[0].Events) != 0 || spans[0].DroppedEvents != 0 {
				t.Errorf("%s: err %v, events %v, dropped %d", ev, err, spans[0].Events, spans[0].DroppedEvents)
			}
		}
	})
	t.Run("events past the cap are ignored, not an error", func(t *testing.T) {
		evs := strings.Repeat(`{"name":"x"},`, maxEventsPerSpan*2) + toolOutputEvent(t, "late")
		spans, err := ParseOTLP(span("[" + evs + "]"))
		if err != nil || len(spans[0].Events) != 0 {
			t.Errorf("err %v, events %v", err, spans[0].Events)
		}
	})
	t.Run("event attribute count is capped per event", func(t *testing.T) {
		kv := strings.Repeat(`{"key":"k","value":{"stringValue":"v"}},`, MaxAttrsPerSpan) + `{"key":"k","value":{"stringValue":"v"}}`
		if _, err := ParseOTLP(span(`[{"name":"tool.output","attributes":[` + kv + `]}]`)); err == nil || !strings.Contains(err.Error(), "event.attributes: more than") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("event attributes count toward the document value limit", func(t *testing.T) {
		kv := strings.Repeat(`{"key":"k","value":{"stringValue":"v"}},`, MaxAttrsPerSpan-1) + `{"key":"k","value":{"stringValue":"v"}}`
		ev := `{"name":"tool.output","attributes":[` + kv + `]}`
		evs := strings.Repeat(ev+",", maxKeptEvents-1) + ev
		var spans []string
		for i := 0; i < 130; i++ {
			s := spanJSON(t, fmt.Sprintf("%016x", i+1), "", "s", 1, 2, nil)
			spans = append(spans, strings.TrimSuffix(s, "}")+`,"events":[`+evs+`]}`)
		}
		if _, err := ParseOTLP(otlpDoc(spans...)); err == nil || !strings.Contains(err.Error(), "attribute values") {
			t.Errorf("err = %v, want the document value limit", err)
		}
	})
}

// Coverage must not report evidence the converter fills in from native or
// fallback attributes.
func TestCheck_CoverageMatchesConversion(t *testing.T) {
	for _, c := range []struct{ fixture, field string }{
		{"claude-code-py", "tokens"}, {"claude-code-ts", "tokens"}, {"langsmith-py", "tool_content"},
	} {
		t.Run(c.fixture+"/"+c.field, func(t *testing.T) {
			spans, err := ParseOTLP(readFixture(t, "otlp/"+c.fixture+".otlp.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, gap := range Check(spans).Coverage {
				if gap.Field == c.field {
					t.Errorf("coverage reports %+v although conversion fills the field", gap)
				}
			}
		})
	}
	t.Run("tool result from a tool.output event counts, missing arguments still do not", func(t *testing.T) {
		tool := fwSpan(t, "0000000000000002", "", "t", 1, 2, map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "x"}, toolOutputEvent(t, "o"))
		spans, err := ParseOTLP(otlpDoc(tool))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, gap := range Check(spans).Coverage {
			found = found || gap.Field == "tool_content"
		}
		if !found {
			t.Error("tool span without arguments reported complete")
		}
	})
}

func TestConvert_FinishReasonsStringIsStoredAsArrayInResultAndExchange(t *testing.T) {
	tr, err := convertSpans(t, fwSpan(t, "0000000000000001", "", "chat m", 1, 2, map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.response.finish_reasons": `["tool-calls"]`,
		"gen_ai.input.messages": `[]`, "gen_ai.output.messages": `[]`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var res, resp map[string]any
	_ = json.Unmarshal(tr.Steps[0].Result, &res)
	_ = json.Unmarshal(tr.Steps[0].Exchange.Response, &resp)
	want := []any{"tool-calls"}
	if !reflect.DeepEqual(res["finish_reasons"], want) || !reflect.DeepEqual(resp["finish_reasons"], want) {
		t.Errorf("result %v, exchange response %v, want %v", res["finish_reasons"], resp["finish_reasons"], want)
	}
}

func TestConvert_MiscasedEventNameGivesNoToolResult(t *testing.T) {
	root := fwSpan(t, "0000000000000001", "", "r", 1, 9, map[string]any{"gen_ai.operation.name": "invoke_agent"})
	tool := strings.TrimSuffix(spanJSON(t, "0000000000000002", "0000000000000001", "t", 2, 3, map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "x"}), "}") +
		`,"events":[{"NAME":"tool.output","attributes":[{"key":"output","value":{"stringValue":"leak"}}]}]}`
	tr, err := convertSpans(t, root, tool)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONObject(t, "result", tr.Steps[0].Result, map[string]any{})
}
