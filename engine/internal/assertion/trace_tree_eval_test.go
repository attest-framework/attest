package assertion

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// buildAgentTrace creates a trace for a specific agent with the given output map.
func buildAgentTrace(agentID string, input map[string]interface{}, output map[string]interface{}, steps ...types.Step) *types.Trace {
	inputBytes, _ := json.Marshal(input)
	outputBytes, _ := json.Marshal(output)
	return &types.Trace{
		SchemaVersion: 1,
		TraceID:       "trc_" + agentID,
		AgentID:       agentID,
		Input:         inputBytes,
		Output:        outputBytes,
		Steps:         steps,
	}
}

// buildAgentStep creates an agent_call step containing a sub-trace.
func buildAgentStep(subTrace *types.Trace) types.Step {
	return types.Step{
		Type:     types.StepTypeAgentCall,
		Name:     subTrace.AgentID,
		SubTrace: subTrace,
	}
}

// makeTreeAssertion builds a trace_tree assertion from a JSON spec string.
func makeTreeAssertion(spec string) *types.Assertion {
	return &types.Assertion{
		AssertionID: "assert_tree_test",
		Type:        types.TypeTraceTree,
		Spec:        json.RawMessage(spec),
	}
}

func TestTraceTreeEval_AgentCalled_Found(t *testing.T) {
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"result": "done"})
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_called","agent_id":"child_agent"}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AgentCalled_NotFound(t *testing.T) {
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true})

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_called","agent_id":"missing_agent"}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_DelegationDepth_Pass(t *testing.T) {
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 1})
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"delegation_depth","max_depth":2}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_DelegationDepth_Fail(t *testing.T) {
	grandchild := buildAgentTrace("grandchild_agent", nil, map[string]interface{}{"x": 1})
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 2}, buildAgentStep(grandchild))
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"delegation_depth","max_depth":1}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AgentOutputContains_Pass(t *testing.T) {
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"summary": "task complete"})
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_output_contains","agent_id":"child_agent","value":"task complete"}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AgentOutputContains_Fail(t *testing.T) {
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"summary": "nothing useful"})
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_output_contains","agent_id":"child_agent","value":"task complete"}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AgentOutputContains_CaseInsensitive(t *testing.T) {
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"msg": "Task Complete"})
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))

	eval := &TraceTreeEvaluator{}
	// case_sensitive=false (default), should match regardless of case
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_output_contains","agent_id":"child_agent","value":"task complete","case_sensitive":false}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass (case-insensitive), got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_CrossAgentDataFlow_Pass(t *testing.T) {
	child := buildAgentTrace("child_agent",
		map[string]interface{}{"task": "summarize"},
		map[string]interface{}{"order_id": "ORD-123", "status": "complete"},
	)
	root := buildAgentTrace("root_agent",
		map[string]interface{}{"order_id": "ORD-123"},
		map[string]interface{}{"ok": true},
		buildAgentStep(child),
	)

	eval := &TraceTreeEvaluator{}
	// child_agent output field "order_id" should appear in root_agent input
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"cross_agent_data_flow","from_agent":"child_agent","to_agent":"root_agent","field":"order_id"}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_CrossAgentDataFlow_Fail(t *testing.T) {
	child := buildAgentTrace("child_agent",
		nil,
		map[string]interface{}{"order_id": "ORD-456"},
	)
	root := buildAgentTrace("root_agent",
		map[string]interface{}{"order_id": "ORD-123"},
		map[string]interface{}{"ok": true},
		buildAgentStep(child),
	)

	eval := &TraceTreeEvaluator{}
	// child outputs ORD-456 but root received ORD-123 — data did not flow
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"cross_agent_data_flow","from_agent":"child_agent","to_agent":"root_agent","field":"order_id"}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AggregateCost_Pass(t *testing.T) {
	cost1 := 0.05
	cost2 := 0.03
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 1})
	child.Metadata = &types.TraceMetadata{CostUSD: &cost2}
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))
	root.Metadata = &types.TraceMetadata{CostUSD: &cost1}

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_cost","operator":"lte","value":0.10}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass (total 0.08 <= 0.10), got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AggregateCost_Fail(t *testing.T) {
	cost1 := 0.08
	cost2 := 0.05
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 1})
	child.Metadata = &types.TraceMetadata{CostUSD: &cost2}
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))
	root.Metadata = &types.TraceMetadata{CostUSD: &cost1}

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_cost","operator":"lte","value":0.10}`))
	// total = 0.13 > 0.10 → fail
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail (total 0.13 > 0.10), got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_AggregateTokens(t *testing.T) {
	tokens1 := 500
	tokens2 := 300
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 1})
	child.Metadata = &types.TraceMetadata{TotalTokens: &tokens2}
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))
	root.Metadata = &types.TraceMetadata{TotalTokens: &tokens1}

	eval := &TraceTreeEvaluator{}

	// Pass: 800 <= 1000
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_tokens","operator":"lte","value":1000}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass (800 <= 1000), got %q: %s", result.Status, result.Explanation)
	}

	// Fail: 800 <= 700
	result = eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_tokens","operator":"lte","value":700}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail (800 > 700), got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_SoftFail(t *testing.T) {
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true})

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"agent_called","agent_id":"missing_agent","soft":true}`))
	if result.Status != types.StatusSoftFail {
		t.Errorf("expected soft_fail, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_UnknownCheck(t *testing.T) {
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true})

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"unknown_check_xyz"}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail for unknown check, got %q: %s", result.Status, result.Explanation)
	}
}

func TestTraceTreeEval_RegisteredInRegistry(t *testing.T) {
	r := NewRegistry()
	eval, err := r.Get(types.TypeTraceTree)
	if err != nil {
		t.Fatalf("trace_tree not registered in NewRegistry: %v", err)
	}
	if eval == nil {
		t.Fatal("NewRegistry returned nil evaluator for trace_tree")
	}
	if _, ok := eval.(*TraceTreeEvaluator); !ok {
		t.Fatalf("expected *TraceTreeEvaluator, got %T", eval)
	}
}

func TestTraceTreeEval_MissingCheck(t *testing.T) {
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true})

	eval := &TraceTreeEvaluator{}
	result := eval.Evaluate(root, makeTreeAssertion(`{}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail for missing check field, got %q", result.Status)
	}
}

func TestTraceTreeEval_AggregateLatency(t *testing.T) {
	latency1 := 200
	latency2 := 150
	child := buildAgentTrace("child_agent", nil, map[string]interface{}{"x": 1})
	child.Metadata = &types.TraceMetadata{LatencyMS: &latency2}
	root := buildAgentTrace("root_agent", nil, map[string]interface{}{"ok": true}, buildAgentStep(child))
	root.Metadata = &types.TraceMetadata{LatencyMS: &latency1}

	eval := &TraceTreeEvaluator{}

	// Pass: 350 <= 500
	result := eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_latency","operator":"lte","value":500}`))
	if result.Status != types.StatusPass {
		t.Errorf("expected pass (350ms <= 500ms), got %q: %s", result.Status, result.Explanation)
	}

	// Fail: 350 <= 300
	result = eval.Evaluate(root, makeTreeAssertion(`{"check":"aggregate_latency","operator":"lte","value":300}`))
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail (350ms > 300ms), got %q: %s", result.Status, result.Explanation)
	}
}
