package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const traceV2Fixtures = "../../../protocol-tests/trace-v2"

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(traceV2Fixtures, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func fixturePaths(t *testing.T, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(traceV2Fixtures, pattern))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures match %s (err=%v)", pattern, err)
	}
	return paths
}

// otlpDoc wraps spans in a minimal ExportTraceServiceRequest.
func otlpDoc(spans ...string) []byte {
	return []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[` + strings.Join(spans, ",") + `]}]}]}`)
}

func TestParseOTLP_AcceptsProto3JSONVariants(t *testing.T) {
	data := otlpDoc(`{
		"traceId":"4BF92F3577B34DA6A3CE929D0E0E4736","spanId":"00F067AA0BA902B7",
		"parentSpanId":"0000000000000000",
		"name":"chat m","startTimeUnixNano":1000000,"endTimeUnixNano":"3000000",
		"status":{"code":"STATUS_CODE_ERROR","message":"boom"},
		"attributes":[
			{"key":"n","value":{"intValue":42}},
			{"key":"s","value":{"intValue":"43"}},
			{"key":"d","value":{"doubleValue":1.5}},
			{"key":"b","value":{"boolValue":true}},
			{"key":"bytes","value":{"bytesValue":"aGk="}},
			{"key":"arr","value":{"arrayValue":{"values":[{"stringValue":"a"},{"intValue":"1"}]}}},
			{"key":"kv","value":{"kvlistValue":{"values":[{"key":"x","value":{"boolValue":false}}]}}},
			{"key":"empty","value":{}}
		]}`)
	spans, err := ParseOTLP(data)
	if err != nil {
		t.Fatalf("ParseOTLP: %v", err)
	}
	s := spans[0]
	if s.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || s.SpanID != "00f067aa0ba902b7" {
		t.Errorf("ids not lowercased: %s %s", s.TraceID, s.SpanID)
	}
	if s.ParentSpanID != "" {
		t.Errorf("all-zero parentSpanId = %q, want root", s.ParentSpanID)
	}
	if s.StartNs != 1000000 || s.EndNs != 3000000 {
		t.Errorf("times = %d..%d", s.StartNs, s.EndNs)
	}
	if !s.StatusError || s.StatusMessage != "boom" {
		t.Errorf("status = %v %q", s.StatusError, s.StatusMessage)
	}
	want := map[string]any{
		"n": int64(42), "s": int64(43), "d": 1.5, "b": true, "bytes": "aGk=",
		"arr": []any{"a", int64(1)},
		"kv":  map[string]any{"x": false},
	}
	if !reflect.DeepEqual(s.Attributes, want) {
		t.Errorf("attributes = %#v\nwant %#v", s.Attributes, want)
	}
}

func TestParseOTLP_StatusCodeInteger(t *testing.T) {
	spans, err := ParseOTLP(otlpDoc(`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","name":"x","startTimeUnixNano":"1","endTimeUnixNano":"2","status":{"code":2}}`))
	if err != nil {
		t.Fatalf("ParseOTLP: %v", err)
	}
	if !spans[0].StatusError {
		t.Error("status code 2 not read as error")
	}
}

func TestParseOTLP_OversizeInputRejected(t *testing.T) {
	_, err := ParseOTLP(make([]byte, MaxOTLPBytes+1))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v, want size limit error", err)
	}
}

// Every protocol-tests/trace-v2/otlp-invalid input is rejected by
// conversion with an error.
func TestConvertOTLP_MalformedCorpusRejected(t *testing.T) {
	for _, p := range fixturePaths(t, "otlp-invalid/*.json") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if tr, err := ConvertOTLP(data, ConvertOptions{}); err == nil {
			t.Errorf("%s: converted to trace %s, want error", filepath.Base(p), tr.TraceID)
		}
	}
}

// Every protocol-tests/trace-v2/otlp/<name>.otlp.json converts to exactly
// <name>.trace.json. The Python and TypeScript adapters assert the same
// golden output.
func TestConvertOTLP_MatchesGoldenTraces(t *testing.T) {
	for _, p := range fixturePaths(t, "otlp/*.otlp.json") {
		name := strings.TrimSuffix(filepath.Base(p), ".otlp.json")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := ConvertOTLP(data, ConvertOptions{})
			if err != nil {
				t.Fatalf("ConvertOTLP: %v", err)
			}
			got, err := json.Marshal(tr)
			if err != nil {
				t.Fatal(err)
			}
			var gotDoc, wantDoc any
			if err := json.Unmarshal(got, &gotDoc); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(readFixture(t, "otlp/"+name+".trace.json"), &wantDoc); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotDoc, wantDoc) {
				t.Errorf("converted trace differs from golden %s.trace.json:\n%s", name, got)
			}
		})
	}
}

func convertFixture(t *testing.T, name string) *types.Trace {
	t.Helper()
	tr, err := ConvertOTLP(readFixture(t, "otlp/"+name+".otlp.json"), ConvertOptions{})
	if err != nil {
		t.Fatalf("ConvertOTLP(%s): %v", name, err)
	}
	return tr
}

func TestConvertOTLP_INC1042_RepeatedRefundIsRetry(t *testing.T) {
	tr := convertFixture(t, "inc-1042-refund")
	var refunds []int
	for i, s := range tr.Steps {
		if s.Type == types.StepTypeToolCall && s.Name == "issue_refund" {
			refunds = append(refunds, i)
		}
	}
	if len(refunds) != 2 {
		t.Fatalf("issue_refund steps = %v, want 2", refunds)
	}
	first, second := tr.Steps[refunds[0]], tr.Steps[refunds[1]]
	if first.Error == nil || first.Error.Type != "payments_timeout" {
		t.Errorf("first refund error = %+v, want payments_timeout", first.Error)
	}
	if second.RetryOf == nil || *second.RetryOf != refunds[0] {
		t.Errorf("second refund retry_of = %v, want %d", second.RetryOf, refunds[0])
	}
	if tr.Metadata == nil || tr.Metadata.CostUSD != nil {
		t.Errorf("metadata = %+v, want no cost_usd (no span records gen_ai.usage.cost)", tr.Metadata)
	}
	if len(tr.Turns) != 1 || tr.AgentID != "refund-desk" {
		t.Errorf("turns = %d agent_id = %q, want 1 turn by refund-desk", len(tr.Turns), tr.AgentID)
	}
}

func TestConvertOTLP_AgentIDOverride(t *testing.T) {
	tr, err := ConvertOTLP(readFixture(t, "otlp/inc-1042-refund.otlp.json"), ConvertOptions{AgentID: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.AgentID != "custom" {
		t.Errorf("agent_id = %q, want custom", tr.AgentID)
	}
}

func TestConvertOTLP_SpanWithoutGenAIAttributesIsNotAStep(t *testing.T) {
	tr, err := ConvertOTLP(otlpDoc(`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","name":"GET /health","startTimeUnixNano":"1","endTimeUnixNano":"2"}`), ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) != 0 || tr.Steps == nil {
		t.Errorf("steps = %#v, want empty non-nil", tr.Steps)
	}
}

func TestValidate_StepReferences(t *testing.T) {
	base := func() *types.Trace {
		return &types.Trace{
			SchemaVersion: 2, TraceID: "trc", Output: json.RawMessage(`{"m":1}`),
			Steps: []types.Step{{Type: "llm_call", Name: "a"}, {Type: "llm_call", Name: "b"}},
		}
	}
	self := 1
	later := 1
	cases := map[string]func(*types.Trace){
		"retry_of self":       func(t *types.Trace) { t.Steps[1].RetryOf = &self },
		"retry_of later step": func(t *types.Trace) { t.Steps[0].RetryOf = &later },
		"turn out of range":   func(t *types.Trace) { t.Turns = []types.Turn{{StepIndices: []int{2}}} },
		"handoff negative":    func(t *types.Trace) { t.Handoffs = []types.Handoff{{FromAgent: "a", ToAgent: "b", StepIndex: -1}} },
	}
	for name, mutate := range cases {
		tr := base()
		mutate(tr)
		if Validate(tr, 0) == nil {
			t.Errorf("%s: Validate accepted the trace", name)
		}
	}
	ok := base()
	zero := 0
	ok.Steps[1].RetryOf = &zero
	ok.Turns = []types.Turn{{StepIndices: []int{0, 1}}}
	if err := Validate(ok, 0); err != nil {
		t.Errorf("valid references rejected: %s", err.Message)
	}
}

// spanJSON encodes one span of the shared test trace id.
func spanJSON(t *testing.T, id, parent, name string, start, end int, attrs map[string]any) string {
	t.Helper()
	p := ""
	if parent != "" {
		p = `"parentSpanId":"` + parent + `",`
	}
	return `{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"` + id + `",` + p +
		`"name":"` + name + `","startTimeUnixNano":"` + fmt.Sprint(start) + `","endTimeUnixNano":"` + fmt.Sprint(end) +
		`","attributes":[` + otlpAttrs(t, attrs) + `]}`
}

func TestConvert_AgentNameOnChatSpanStaysAModelCall(t *testing.T) {
	tr, err := ConvertOTLP(otlpDoc(
		spanJSON(t, "a000000000000001", "", "invoke_agent x", 0, 100, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "x"}),
		spanJSON(t, "a000000000000002", "a000000000000001", "chat", 1, 2, map[string]any{"gen_ai.agent.name": "x", "gen_ai.completion": "hi"}),
	), ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) != 1 || tr.Steps[0].Type != types.StepTypeLLMCall || len(tr.Handoffs) != 0 {
		t.Errorf("steps=%+v handoffs=%+v, want one llm_call and no handoff", tr.Steps, tr.Handoffs)
	}
}

func TestConvert_ModelRetryNeedsSameNameAndModel(t *testing.T) {
	failed := map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "m1"}
	tr, err := ConvertOTLP(otlpDoc(
		spanJSON(t, "a000000000000001", "", "root", 0, 100, nil),
		strings.Replace(spanJSON(t, "a000000000000002", "a000000000000001", "chat m1", 1, 2, failed), `"attributes"`, `"status":{"code":2},"attributes"`, 1),
		spanJSON(t, "a000000000000003", "a000000000000001", "chat m2", 3, 4, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "m2"}),
		strings.Replace(spanJSON(t, "a000000000000004", "a000000000000001", "chat m2", 5, 6, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "m2"}), `"attributes"`, `"status":{"code":2},"attributes"`, 1),
		spanJSON(t, "a000000000000005", "a000000000000001", "chat m2", 7, 8, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "m2"}),
	), ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Steps[1].RetryOf != nil {
		t.Errorf("a different model after a failure was marked a retry of %d", *tr.Steps[1].RetryOf)
	}
	if tr.Steps[3].RetryOf == nil || *tr.Steps[3].RetryOf != 2 {
		t.Errorf("retry_of = %v, want 2", tr.Steps[3].RetryOf)
	}
}

func TestConvert_OutputIsTheTopLevelAnswer(t *testing.T) {
	agent := func(name string) map[string]any {
		return map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": name}
	}
	text := func(s string) map[string]any {
		return map[string]any{"gen_ai.operation.name": "chat", "gen_ai.completion": s}
	}
	spans := []string{
		spanJSON(t, "a000000000000001", "", "invoke_agent main", 0, 100, agent("main")),
		spanJSON(t, "a000000000000002", "a000000000000001", "chat", 1, 10, text("final answer")),
		spanJSON(t, "a000000000000003", "a000000000000001", "invoke_agent helper", 11, 30, agent("helper")),
		spanJSON(t, "a000000000000004", "a000000000000003", "chat", 12, 29, text("helper scratch")),
	}
	tr, err := ConvertOTLP(otlpDoc(spans...), ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tr.Output); got != `{"message":"final answer"}` {
		t.Errorf("output = %s, want the top-level agent's answer", got)
	}

	withTurnOutput := map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "main",
		"gen_ai.output.messages": `[{"role":"assistant","parts":[{"type":"text","content":"from turn"}]}]`}
	spans[0] = spanJSON(t, "a000000000000001", "", "invoke_agent main", 0, 100, withTurnOutput)
	tr, err = ConvertOTLP(otlpDoc(spans...), ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tr.Output); got != `{"message":"from turn"}` {
		t.Errorf("output = %s, want the turn's recorded output", got)
	}
}

func TestConvert_RecordedContentAboveTraceLimitRejected(t *testing.T) {
	big := `"` + strings.Repeat("a", MaxStepPayload-10) + `"`
	var spans []string
	for i := 1; i <= MaxTraceSize/MaxStepPayload+1; i++ {
		spans = append(spans, spanJSON(t, fmt.Sprintf("a%015x", i), "", "execute_tool t", i, i+1,
			map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.call.arguments": big}))
	}
	_, err := ConvertOTLP(otlpDoc(spans...), ConvertOptions{})
	if err == nil || !strings.Contains(err.Error(), "recorded content") {
		t.Fatalf("err = %v, want recorded content limit", err)
	}
}

func TestParseOTLP_FloodsRejectedWhileStreaming(t *testing.T) {
	const validSpan = `{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000001","startTimeUnixNano":"1","endTimeUnixNano":"2"}`
	values := `{"key":"a","value":{"arrayValue":{"values":[` + strings.Repeat(`{},`, MaxOTLPValues) + `{}]}}}`
	cases := map[string][]byte{
		"spans":           otlpDoc(strings.Repeat(validSpan+`,`, MaxOTLPSpans) + validSpan),
		"values":          otlpDoc(`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000001","startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[` + values + `]}`),
		"attributes":      otlpDoc(`{"attributes":[` + strings.Repeat(`{"key":"k","value":{}},`, maxAttrsPerSpan) + `{"key":"k","value":{}}]}`),
		"skipped nesting": []byte(`{"resource":` + strings.Repeat(`[`, maxSkipDepth+1) + strings.Repeat(`]`, maxSkipDepth+1) + `,"resourceSpans":[]}`),
	}
	for name, data := range cases {
		if _, err := ParseOTLP(data); err == nil || !strings.Contains(err.Error(), "more than") && !strings.Contains(err.Error(), "deeper") {
			t.Errorf("%s: err = %v, want a limit error", name, err)
		}
	}
}
