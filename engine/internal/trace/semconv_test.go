package trace

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Semantic-convention drift guard: one span per GenAI operation and the step
// it must produce. Add a case when the converter learns a new operation or
// attribute. Ported from the Python adapter's conformance vectors when the
// mapping moved into the engine.
func TestConvert_SemconvOperations(t *testing.T) {
	const parentAgent = `{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}},{"key":"gen_ai.agent.name","value":{"stringValue":"orchestrator"}}`
	cases := []struct {
		name     string
		spanName string
		attrs    map[string]any
		want     types.Step // Type, Name, AgentID, AgentRole, ToolCallID
		args     map[string]any
		result   map[string]any
	}{
		{
			name: "chat", spanName: "chat gpt-4.1",
			attrs: map[string]any{
				"gen_ai.operation.name": "chat", "gen_ai.system": "openai", "gen_ai.provider.name": "openai",
				"gen_ai.request.model": "gpt-4.1", "gen_ai.response.model": "gpt-4.1",
				"gen_ai.completion": "Hello, world.", "gen_ai.usage.input_tokens": 12, "gen_ai.usage.output_tokens": 4,
			},
			want:   types.Step{Type: types.StepTypeLLMCall, Name: "chat gpt-4.1"},
			args:   map[string]any{"operation": "chat", "model": "gpt-4.1", "system": "openai", "provider": "openai"},
			result: map[string]any{"completion": "Hello, world.", "input_tokens": 12.0, "output_tokens": 4.0, "model": "gpt-4.1"},
		},
		{
			name: "text_completion", spanName: "text_completion gpt-4.1",
			attrs:  map[string]any{"gen_ai.operation.name": "text_completion", "gen_ai.request.model": "gpt-4.1", "gen_ai.prompt": "Once upon a time", "gen_ai.completion": "there was a llama."},
			want:   types.Step{Type: types.StepTypeLLMCall, Name: "text_completion gpt-4.1"},
			args:   map[string]any{"operation": "text_completion", "model": "gpt-4.1", "prompt": "Once upon a time"},
			result: map[string]any{"completion": "there was a llama."},
		},
		{
			name: "embeddings", spanName: "embeddings text-embedding-3-small",
			attrs:  map[string]any{"gen_ai.operation.name": "embeddings", "gen_ai.request.model": "text-embedding-3-small", "gen_ai.usage.input_tokens": 8},
			want:   types.Step{Type: types.StepTypeLLMCall, Name: "embeddings text-embedding-3-small"},
			args:   map[string]any{"operation": "embeddings", "model": "text-embedding-3-small"},
			result: map[string]any{"input_tokens": 8.0},
		},
		{
			name: "execute_tool", spanName: "execute_tool web_search",
			attrs: map[string]any{
				"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "web_search", "gen_ai.tool.call.id": "call_001",
				"gen_ai.tool.call.arguments": `{"query":"attest"}`, "gen_ai.tool.call.result": `["https://example.com"]`,
			},
			want:   types.Step{Type: types.StepTypeToolCall, Name: "web_search", ToolCallID: "call_001"},
			args:   map[string]any{"query": "attest"},
			result: map[string]any{"result": []any{"https://example.com"}},
		},
		{
			name: "nested invoke_agent", spanName: "invoke_agent researcher",
			attrs:  map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.id": "agent-001", "gen_ai.agent.name": "researcher", "gen_ai.agent.description": "Background research"},
			want:   types.Step{Type: types.StepTypeAgentCall, Name: "researcher", AgentID: "agent-001", AgentRole: "researcher"},
			args:   map[string]any{"agent_id": "agent-001", "agent_name": "researcher", "description": "Background research"},
			result: map[string]any{},
		},
		{
			name: "retrieval", spanName: "retrieval vector_store",
			attrs:  map[string]any{"gen_ai.operation.name": "retrieval", "gen_ai.retrieval.query": "Capital of France", "gen_ai.retrieval.source": "vector_store", "gen_ai.retrieval.documents.count": 3},
			want:   types.Step{Type: types.StepTypeRetrieval, Name: "retrieval vector_store"},
			args:   map[string]any{"query": "Capital of France", "source": "vector_store"},
			result: map[string]any{"documents_count": 3.0},
		},
		{
			name: "retrieval by attribute family", spanName: "search",
			attrs: map[string]any{"gen_ai.retrieval.query": "q"},
			want:  types.Step{Type: types.StepTypeRetrieval, Name: "search"},
			args:  map[string]any{"query": "q"}, result: map[string]any{},
		},
		{
			name: "agent identity on a model call", spanName: "chat",
			attrs:  map[string]any{"gen_ai.operation.name": "chat", "gen_ai.completion": "ok", "gen_ai.agent.id": "agent-42", "gen_ai.agent.name": "support_bot"},
			want:   types.Step{Type: types.StepTypeLLMCall, Name: "chat", AgentID: "agent-42", AgentRole: "support_bot"},
			args:   map[string]any{"operation": "chat"},
			result: map[string]any{"completion": "ok"},
		},
		{
			name: "operation from span name", spanName: "chat gpt-4.1",
			attrs: map[string]any{"gen_ai.request.model": "gpt-4.1"},
			want:  types.Step{Type: types.StepTypeLLMCall, Name: "chat gpt-4.1"},
			args:  map[string]any{"model": "gpt-4.1"}, result: map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := otlpDoc(
				`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000001","name":"invoke_agent orchestrator","startTimeUnixNano":"0","endTimeUnixNano":"100","attributes":[`+parentAgent+`]}`,
				fmt.Sprintf(`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"a000000000000002","parentSpanId":"a000000000000001","name":%q,"startTimeUnixNano":"10","endTimeUnixNano":"20","attributes":[%s]}`, tc.spanName, otlpAttrs(t, tc.attrs)),
			)
			tr, err := ConvertOTLP(data, ConvertOptions{})
			if err != nil {
				t.Fatalf("ConvertOTLP: %v", err)
			}
			if len(tr.Steps) != 1 {
				t.Fatalf("steps = %d, want 1", len(tr.Steps))
			}
			s := tr.Steps[0]
			got := types.Step{Type: s.Type, Name: s.Name, AgentID: s.AgentID, AgentRole: s.AgentRole, ToolCallID: s.ToolCallID}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("step = %+v, want %+v", got, tc.want)
			}
			var args, result map[string]any
			if err := json.Unmarshal(s.Args, &args); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(s.Result, &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args, tc.args) {
				t.Errorf("args = %v, want %v", args, tc.args)
			}
			if !reflect.DeepEqual(result, tc.result) {
				t.Errorf("result = %v, want %v", result, tc.result)
			}
		})
	}
}

// otlpAttrs encodes a Go map as an OTLP attribute list in key order.
func otlpAttrs(t *testing.T, attrs map[string]any) string {
	t.Helper()
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var value string
		switch v := attrs[k].(type) {
		case string:
			b, _ := json.Marshal(v)
			value = fmt.Sprintf(`{"stringValue":%s}`, b)
		case int:
			value = fmt.Sprintf(`{"intValue":"%d"}`, v)
		default:
			t.Fatalf("unsupported attribute type %T", v)
		}
		parts = append(parts, fmt.Sprintf(`{"key":%q,"value":%s}`, k, value))
	}
	return strings.Join(parts, ",")
}
