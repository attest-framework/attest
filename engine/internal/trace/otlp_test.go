package trace

import (
	"encoding/json"
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
