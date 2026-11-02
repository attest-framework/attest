package trace

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func checkData(t *testing.T, data []byte) *CheckReport {
	t.Helper()
	spans, err := ParseOTLP(data)
	if err != nil {
		t.Fatalf("ParseOTLP: %v", err)
	}
	return Check(spans)
}

// mutateINC1042 edits the INC-1042 export's span list in place.
func mutateINC1042(t *testing.T, edit func(spans []map[string]any) []map[string]any) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readFixture(t, "otlp/inc-1042-refund.otlp.json"), &doc); err != nil {
		t.Fatal(err)
	}
	scope := doc["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)
	var spans []map[string]any
	for _, s := range scope["spans"].([]any) {
		spans = append(spans, s.(map[string]any))
	}
	spans = edit(spans)
	out := make([]any, len(spans))
	for i, s := range spans {
		out[i] = s
	}
	scope["spans"] = out
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func findIssue(r *CheckReport, code, spanID string) *Issue {
	for i := range r.Issues {
		if r.Issues[i].Code == code && (spanID == "" || r.Issues[i].SpanID == spanID) {
			return &r.Issues[i]
		}
	}
	return nil
}

func TestCheck_INC1042_StructureOKWithCoverageGaps(t *testing.T) {
	r := checkData(t, readFixture(t, "otlp/inc-1042-refund.otlp.json"))
	if !r.OK || len(r.Issues) != 0 {
		t.Fatalf("ok=%v issues=%+v, want clean structure", r.OK, r.Issues)
	}
	gaps := map[string]CoverageGap{}
	for _, g := range r.Coverage {
		gaps[g.Field] = g
	}
	for _, field := range []string{"cost", "content"} {
		g, ok := gaps[field]
		if !ok || g.Missing != 5 || g.Total != 5 {
			t.Errorf("coverage %s = %+v, want 5 of 5 llm spans", field, g)
		}
	}
	if _, ok := gaps["tokens"]; ok {
		t.Errorf("tokens reported missing; INC-1042 records usage on every model span")
	}
	if r.Retries != 1 || r.Turns != 1 || r.Steps["tool_call"] != 4 || r.Steps["llm_call"] != 5 {
		t.Errorf("summary = steps %v turns %d retries %d", r.Steps, r.Turns, r.Retries)
	}
}

func TestCheck_CorruptedCopy_ReportsEachIssueBySpanID(t *testing.T) {
	var orphanID, lookupChatID string
	data := mutateINC1042(t, func(spans []map[string]any) []map[string]any {
		var kept []map[string]any
		for i, s := range spans {
			switch s["name"] {
			case "execute_tool lookup_order":
				lookupChatID = spans[i-1]["spanId"].(string)
				continue
			case "execute_tool notify_customer":
				s["parentSpanId"] = "deadbeefdeadbeef"
				orphanID = s["spanId"].(string)
			}
			kept = append(kept, s)
		}
		return kept
	})
	r := checkData(t, data)
	if r.OK {
		t.Fatal("corrupted trace reported OK")
	}
	if findIssue(r, IssueOrphanSpan, orphanID) == nil {
		t.Errorf("no orphan_span for %s: %+v", orphanID, r.Issues)
	}
	if findIssue(r, IssueUnpairedToolCall, lookupChatID) == nil {
		t.Errorf("no unpaired_tool_call for model span %s: %+v", lookupChatID, r.Issues)
	}
}

func TestCheck_StructuralErrors(t *testing.T) {
	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	span := func(id, parent, attrs string, start, end int) string {
		p := ""
		if parent != "" {
			p = fmt.Sprintf(`"parentSpanId":%q,`, parent)
		}
		return fmt.Sprintf(`{"traceId":%q,"spanId":%q,%s"name":"s-%s","startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[%s]}`, tid, id, p, id, start, end, attrs)
	}
	op := func(name string) string {
		return fmt.Sprintf(`{"key":"gen_ai.operation.name","value":{"stringValue":%q}}`, name)
	}
	finish := func(reason string) string {
		return fmt.Sprintf(`{"key":"gen_ai.response.finish_reasons","value":{"arrayValue":{"values":[{"stringValue":%q}]}}}`, reason)
	}
	callID := func(id string) string {
		return fmt.Sprintf(`{"key":"gen_ai.tool.call.id","value":{"stringValue":%q}}`, id)
	}
	out := func(ids ...string) string {
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"type":"tool_call","id":%q,"name":"t"}`, id))
		}
		msgs, _ := json.Marshal(fmt.Sprintf(`[{"role":"assistant","parts":[%s]}]`, strings.Join(parts, ",")))
		return fmt.Sprintf(`{"key":"gen_ai.output.messages","value":{"stringValue":%s}}`, msgs)
	}

	cases := []struct {
		name   string
		spans  []string
		code   string
		spanID string
	}{
		{"duplicate span id", []string{span("a000000000000001", "", "", 0, 10), span("a000000000000001", "", "", 1, 2)}, IssueDuplicateSpanID, "a000000000000001"},
		{"parent cycle", []string{
			span("a000000000000001", "", "", 0, 10),
			span("a000000000000002", "a000000000000003", "", 1, 2),
			span("a000000000000003", "a000000000000002", "", 1, 2),
		}, IssueParentCycle, "a000000000000002"},
		{"tool span with no model call in scope", []string{
			span("a000000000000001", "", "", 0, 10),
			span("a000000000000003", "a000000000000001", op("execute_tool"), 3, 4),
			span("a000000000000002", "a000000000000001", op("chat")+","+finish("tool_calls"), 5, 6),
			span("a000000000000004", "a000000000000001", op("execute_tool"), 7, 8),
		}, IssueUnpairedToolResult, "a000000000000003"},
		{"requested call id never executed", []string{
			span("a000000000000001", "", "", 0, 10),
			span("a000000000000002", "a000000000000001", op("chat")+","+finish("tool_calls")+","+out("call_1", "call_2"), 1, 2),
			span("a000000000000003", "a000000000000001", op("execute_tool")+","+callID("call_1"), 3, 4),
		}, IssueUnpairedToolCall, "a000000000000002"},
		{"executed call id never requested", []string{
			span("a000000000000001", "", "", 0, 10),
			span("a000000000000002", "a000000000000001", op("chat")+","+finish("tool_calls")+","+out("call_1"), 1, 2),
			span("a000000000000003", "a000000000000001", op("execute_tool")+","+callID("call_9"), 3, 4),
		}, IssueUnpairedToolResult, "a000000000000003"},
		{"two traces", []string{
			span("a000000000000001", "", "", 0, 10),
			strings.Replace(span("a000000000000002", "", "", 0, 10), tid, "5bf92f3577b34da6a3ce929d0e0e4736", 1),
		}, IssueMultipleTraces, ""},
		{"non-numeric tokens", []string{
			span("a000000000000001", "", op("chat")+`,{"key":"gen_ai.usage.input_tokens","value":{"stringValue":"many"}}`, 0, 10),
		}, IssueConversion, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := checkData(t, otlpDoc(tc.spans...))
			if r.OK {
				t.Fatalf("reported OK; issues=%+v", r.Issues)
			}
			is := findIssue(r, tc.code, tc.spanID)
			if is == nil || is.Severity != SeverityError {
				t.Errorf("want error %s on span %q, got %+v", tc.code, tc.spanID, r.Issues)
			}
		})
	}
}

func TestCheck_SuspiciousParentageWarnsWithoutFailing(t *testing.T) {
	data := otlpDoc(
		`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000001","name":"root","startTimeUnixNano":"10","endTimeUnixNano":"20"}`,
		`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000002","parentSpanId":"a000000000000001","name":"late","startTimeUnixNano":"15","endTimeUnixNano":"30"}`,
		`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000003","name":"second root","startTimeUnixNano":"40","endTimeUnixNano":"35"}`,
	)
	r := checkData(t, data)
	if !r.OK {
		t.Fatalf("warnings failed the check: %+v", r.Issues)
	}
	for _, code := range []string{IssueOutsideParent, IssueMultipleRoots, IssueEndBeforeStart, IssueNoGenAISpans} {
		if is := findIssue(r, code, ""); is == nil || is.Severity != SeverityWarning {
			t.Errorf("want warning %s, got %+v", code, r.Issues)
		}
	}
}

func TestCheck_IssueOrderIsDeterministic(t *testing.T) {
	// Each tool span gets its own missing parent, so issues come from many
	// parent groups.
	data := mutateINC1042(t, func(spans []map[string]any) []map[string]any {
		for i, s := range spans {
			if strings.HasPrefix(s["name"].(string), "execute_tool") {
				s["parentSpanId"] = fmt.Sprintf("dead%012x", i)
			}
		}
		return spans
	})
	first, _ := json.Marshal(checkData(t, data))
	if !strings.Contains(string(first), IssueOrphanSpan) {
		t.Fatalf("mutation produced no orphan issues: %s", first)
	}
	for i := 0; i < 20; i++ {
		again, _ := json.Marshal(checkData(t, data))
		if string(again) != string(first) {
			t.Fatalf("report changed between runs:\n%s\n%s", first, again)
		}
	}
}

// A maximal parent chain must check and convert in linear time: every span
// is resolved once, not walked to the root per span.
func TestCheck_LongParentChainStaysLinear(t *testing.T) {
	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	const n = MaxStepsPerTrace + 1 // one turn plus the step limit of handoffs
	spans := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parent := ""
		if i > 1 {
			parent = fmt.Sprintf(`"parentSpanId":"%016x",`, i-1)
		}
		spans = append(spans, fmt.Sprintf(`{"traceId":%q,"spanId":"%016x",%s"name":"invoke_agent a","startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}}]}`, tid, i, parent, i, 2*n-i))
	}
	start := time.Now()
	r := checkData(t, otlpDoc(spans...))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("check took %s", elapsed)
	}
	if !r.OK || r.Handoffs != n-1 || r.Turns != 1 {
		t.Errorf("ok=%v turns=%d handoffs=%d issues=%+v", r.OK, r.Turns, r.Handoffs, r.Issues)
	}
}

// Real-world export shapes that are structurally sound must not report
// integrity errors.
func TestCheck_RealWorldShapesHaveNoErrors(t *testing.T) {
	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	type sp struct {
		id, parent string
		attrs      map[string]any
		start, end int
	}
	chat := func(reason string) map[string]any {
		return map[string]any{"gen_ai.operation.name": "chat", "gen_ai.response.finish_reasons": reason}
	}
	tool := func(id string) map[string]any {
		a := map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "lookup"}
		if id != "" {
			a["gen_ai.tool.call.id"] = id
		}
		return a
	}
	recorded := func(ids ...string) map[string]any {
		a := chat("tool_calls")
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"type":"tool_call","id":%q,"name":"lookup"}`, id))
		}
		a["gen_ai.output.messages"] = fmt.Sprintf(`[{"role":"assistant","parts":[%s]}]`, strings.Join(parts, ","))
		return a
	}
	node := map[string]any{"langgraph.node": "agent"}
	cases := map[string][]sp{
		"anthropic tool_use": {
			{"a000000000000001", "", map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "a"}, 0, 100},
			{"a000000000000002", "a000000000000001", chat("tool_use"), 1, 2},
			{"a000000000000003", "a000000000000001", tool("toolu_1"), 3, 4},
			{"a000000000000004", "a000000000000001", chat("end_turn"), 5, 6},
		},
		"tools under separate framework node spans": {
			{"a000000000000001", "", map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "graph"}, 0, 100},
			{"a000000000000002", "a000000000000001", node, 1, 10},
			{"a000000000000003", "a000000000000002", chat("tool_calls"), 2, 9},
			{"a000000000000004", "a000000000000001", map[string]any{"langgraph.node": "tools"}, 11, 20},
			{"a000000000000005", "a000000000000004", tool("call_1"), 12, 19},
		},
		"tool nested under the model span": {
			{"a000000000000001", "", nil, 0, 100},
			{"a000000000000002", "a000000000000001", chat("tool_calls"), 1, 50},
			{"a000000000000003", "a000000000000002", tool(""), 10, 20},
		},
		"parallel tool calls": {
			{"a000000000000001", "", nil, 0, 100},
			{"a000000000000002", "a000000000000001", chat("tool_calls"), 1, 2},
			{"a000000000000003", "a000000000000001", tool("call_1"), 3, 9},
			{"a000000000000004", "a000000000000001", tool("call_2"), 3, 8},
			{"a000000000000005", "a000000000000001", chat("stop"), 10, 11},
		},
		"embeddings between request and tool": {
			{"a000000000000001", "", nil, 0, 100},
			{"a000000000000002", "a000000000000001", chat("tool_calls"), 1, 2},
			{"a000000000000003", "a000000000000001", map[string]any{"gen_ai.operation.name": "embeddings"}, 3, 4},
			{"a000000000000004", "a000000000000001", tool(""), 5, 6},
		},
		"remote parent of the local root": {
			{"a000000000000001", "ffffffffffffffff", nil, 0, 100},
			{"a000000000000002", "a000000000000001", chat("stop"), 1, 2},
		},
		"partial content capture": {
			{"a000000000000001", "", nil, 0, 100},
			{"a000000000000002", "a000000000000001", recorded("call_1"), 1, 2},
			{"a000000000000003", "a000000000000001", tool("call_1"), 3, 4},
			{"a000000000000004", "a000000000000001", chat("tool_calls"), 5, 6},
			{"a000000000000005", "a000000000000001", tool("call_2"), 7, 8},
		},
		"gemini STOP before a tool": {
			{"a000000000000001", "", nil, 0, 100},
			{"a000000000000002", "a000000000000001", chat("STOP"), 1, 2},
			{"a000000000000003", "a000000000000001", tool(""), 3, 4},
		},
	}
	for name, spans := range cases {
		t.Run(name, func(t *testing.T) {
			var encoded []string
			for _, s := range spans {
				parent := ""
				if s.parent != "" {
					parent = fmt.Sprintf(`"parentSpanId":%q,`, s.parent)
				}
				encoded = append(encoded, fmt.Sprintf(`{"traceId":%q,"spanId":%q,%s"name":"s","startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[%s]}`,
					tid, s.id, parent, s.start, s.end, otlpAttrs(t, s.attrs)))
			}
			r := checkData(t, otlpDoc(encoded...))
			if !r.OK {
				t.Errorf("integrity errors on a sound trace: %+v", r.Errors())
			}
		})
	}
}

func TestCheck_RemoteParentIsTheRoot(t *testing.T) {
	data := otlpDoc(`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000001","parentSpanId":"ffffffffffffffff","name":"invoke_agent a","startTimeUnixNano":"0","endTimeUnixNano":"5000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}}]}`)
	r := checkData(t, data)
	if is := findIssue(r, IssueRemoteParent, "a000000000000001"); is == nil || is.Severity != SeverityWarning {
		t.Errorf("want remote_parent warning, got %+v", r.Issues)
	}
	tr, err := ConvertOTLP(data, ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Metadata == nil || tr.Metadata.LatencyMS == nil || *tr.Metadata.LatencyMS != 5 {
		t.Errorf("latency from the remote-parented root = %+v, want 5ms", tr.Metadata)
	}
}
