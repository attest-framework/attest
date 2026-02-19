package trace

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func testTrace(agentID string, steps ...types.Step) *types.Trace {
	return &types.Trace{
		SchemaVersion: 1,
		TraceID:       "trc_" + agentID,
		AgentID:       agentID,
		Output:        json.RawMessage(`{"message":"ok"}`),
		Steps:         steps,
	}
}

func agentStep(name string, sub *types.Trace) types.Step {
	return types.Step{
		Type:     types.StepTypeAgentCall,
		Name:     name,
		SubTrace: sub,
	}
}

func ptr[T any](v T) *T { return &v }

func TestCollectSubTraces_Flat(t *testing.T) {
	root := testTrace("root")
	traces := CollectSubTraces(root)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0] != root {
		t.Fatal("expected root trace")
	}
}

func TestCollectSubTraces_Nested(t *testing.T) {
	child2 := testTrace("child2")
	child1 := testTrace("child1", agentStep("call-child2", child2))
	root := testTrace("root", agentStep("call-child1", child1))

	traces := CollectSubTraces(root)
	if len(traces) != 3 {
		t.Fatalf("expected 3 traces, got %d", len(traces))
	}
	// depth-first: root, child1, child2
	if traces[0].AgentID != "root" {
		t.Errorf("expected root first, got %q", traces[0].AgentID)
	}
	if traces[1].AgentID != "child1" {
		t.Errorf("expected child1 second, got %q", traces[1].AgentID)
	}
	if traces[2].AgentID != "child2" {
		t.Errorf("expected child2 third, got %q", traces[2].AgentID)
	}
}

func TestFindAgentByID_Found(t *testing.T) {
	child := testTrace("target")
	root := testTrace("root", agentStep("call", child))

	found := FindAgentByID(root, "target")
	if found == nil {
		t.Fatal("expected to find agent, got nil")
	}
	if found.AgentID != "target" {
		t.Errorf("expected agent_id 'target', got %q", found.AgentID)
	}
}

func TestFindAgentByID_NotFound(t *testing.T) {
	root := testTrace("root")
	found := FindAgentByID(root, "missing")
	if found != nil {
		t.Fatalf("expected nil, got trace with agent_id %q", found.AgentID)
	}
}

func TestTreeDepth_Flat(t *testing.T) {
	root := testTrace("root")
	depth := TreeDepth(root)
	if depth != 0 {
		t.Errorf("expected depth 0, got %d", depth)
	}
}

func TestTreeDepth_ThreeLevel(t *testing.T) {
	level2 := testTrace("level2")
	level1 := testTrace("level1", agentStep("call-l2", level2))
	root := testTrace("root", agentStep("call-l1", level1))

	depth := TreeDepth(root)
	if depth != 2 {
		t.Errorf("expected depth 2, got %d", depth)
	}
}

func TestAgentIDs(t *testing.T) {
	child2 := testTrace("agent-c")
	child1 := testTrace("agent-b", agentStep("call", child2))
	root := testTrace("agent-a", agentStep("call", child1))

	ids := AgentIDs(root)
	if len(ids) != 3 {
		t.Fatalf("expected 3 agent IDs, got %d: %v", len(ids), ids)
	}
	expected := map[string]bool{"agent-a": true, "agent-b": true, "agent-c": true}
	for _, id := range ids {
		if !expected[id] {
			t.Errorf("unexpected agent ID: %q", id)
		}
	}
}

func TestValidateTraceTree_Valid(t *testing.T) {
	child := testTrace("child")
	child.ParentTraceID = ptr("trc_root")
	root := testTrace("root", agentStep("call", child))

	if err := ValidateTraceTree(root); err != nil {
		t.Errorf("expected valid tree, got error: %v", err)
	}
}

func TestValidateTraceTree_MissingSubTrace(t *testing.T) {
	root := testTrace("root", types.Step{
		Type: types.StepTypeAgentCall,
		Name: "bad-call",
	})

	err := ValidateTraceTree(root)
	if err == nil {
		t.Fatal("expected error for agent_call missing sub_trace, got nil")
	}
}

func TestValidateTraceTree_DuplicateTraceID(t *testing.T) {
	// Child has same TraceID as root — creates a cycle
	child := &types.Trace{
		SchemaVersion: 1,
		TraceID:       "trc_root",
		AgentID:       "child",
		Output:        json.RawMessage(`{"message":"ok"}`),
	}
	root := testTrace("root", agentStep("call", child))

	err := ValidateTraceTree(root)
	if err == nil {
		t.Fatal("expected error for duplicate trace_id, got nil")
	}
}

func TestValidateTraceTree_DepthExceeded(t *testing.T) {
	// Build a chain deeper than MaxSubTraceDepth (5)
	deepest := testTrace("d6")
	d5 := testTrace("d5", agentStep("c", deepest))
	d4 := testTrace("d4", agentStep("c", d5))
	d3 := testTrace("d3", agentStep("c", d4))
	d2 := testTrace("d2", agentStep("c", d3))
	d1 := testTrace("d1", agentStep("c", d2))
	root := testTrace("root", agentStep("c", d1))

	err := ValidateTraceTree(root)
	if err == nil {
		t.Fatal("expected error for depth exceeded, got nil")
	}
}

func TestAggregateMetadata(t *testing.T) {
	child := testTrace("child")
	child.Metadata = &types.TraceMetadata{
		TotalTokens: ptr(300),
		CostUSD:     ptr(0.003),
		LatencyMS:   ptr(200),
	}

	root := testTrace("root", agentStep("call", child))
	root.Metadata = &types.TraceMetadata{
		TotalTokens: ptr(500),
		CostUSD:     ptr(0.005),
		LatencyMS:   ptr(400),
	}

	tokens, cost, latency, agents := AggregateMetadata(root)

	if tokens != 800 {
		t.Errorf("expected 800 tokens, got %d", tokens)
	}
	if agents != 2 {
		t.Errorf("expected 2 agents, got %d", agents)
	}
	if latency != 600 {
		t.Errorf("expected 600ms latency, got %d", latency)
	}
	// float comparison with tolerance
	if cost < 0.0079 || cost > 0.0081 {
		t.Errorf("expected cost ~0.008, got %f", cost)
	}
}
