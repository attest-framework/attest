package simulation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/internal/simulation"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// newMockWithResponses builds a MockProvider returning the given strings in order (cycling).
func newMockWithResponses(responses []string) *llm.MockProvider {
	resps := make([]*llm.CompletionResponse, len(responses))
	for i, r := range responses {
		resps[i] = &llm.CompletionResponse{Content: r, Model: "mock-model"}
	}
	return llm.NewMockProvider(resps, nil)
}

func echoAgentFn(_ context.Context, userMessage string) (string, error) {
	return "echo: " + userMessage, nil
}

// TestIntegration_SimulationLoop runs a 3-turn simulation end-to-end and verifies
// turn count, content, and stop reason.
func TestIntegration_SimulationLoop(t *testing.T) {
	t.Parallel()

	mock := newMockWithResponses([]string{"follow-up A", "follow-up B"})
	cfg := simulation.SimulationConfig{
		Persona:  simulation.FriendlyUser,
		MaxTurns: 3,
		Provider: mock,
	}
	orch := simulation.NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "hello world", echoAgentFn)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}

	if result.TotalTurns != 3 {
		t.Errorf("TotalTurns = %d, want 3", result.TotalTurns)
	}
	if len(result.Turns) != 3 {
		t.Fatalf("len(Turns) = %d, want 3", len(result.Turns))
	}
	if result.StoppedBy != "max_turns" {
		t.Errorf("StoppedBy = %q, want %q", result.StoppedBy, "max_turns")
	}

	// Turn 1 uses the initial prompt.
	if result.Turns[0].UserMessage != "hello world" {
		t.Errorf("turn 1 UserMessage = %q, want %q", result.Turns[0].UserMessage, "hello world")
	}
	if result.Turns[0].AgentResponse != "echo: hello world" {
		t.Errorf("turn 1 AgentResponse = %q, want %q", result.Turns[0].AgentResponse, "echo: hello world")
	}

	// Turn 2 uses the first mock user response.
	if result.Turns[1].UserMessage != "follow-up A" {
		t.Errorf("turn 2 UserMessage = %q, want %q", result.Turns[1].UserMessage, "follow-up A")
	}

	// Turn 3 uses the second mock user response.
	if result.Turns[2].UserMessage != "follow-up B" {
		t.Errorf("turn 3 UserMessage = %q, want %q", result.Turns[2].UserMessage, "follow-up B")
	}

	// Turn numbers must be sequential starting at 1.
	for i, turn := range result.Turns {
		if turn.TurnNumber != i+1 {
			t.Errorf("turn[%d].TurnNumber = %d, want %d", i, turn.TurnNumber, i+1)
		}
	}
}

// TestIntegration_FaultInjectionDuringSimulation verifies that a FaultInjector with a
// high ErrorRate causes the simulation to fail rather than returning a result.
func TestIntegration_FaultInjectionDuringSimulation(t *testing.T) {
	t.Parallel()

	inner := newMockWithResponses([]string{"user message"})
	fc := simulation.FaultConfig{
		ErrorRate: 1.0, // always fail
	}
	faulty := simulation.NewFaultInjectorWithSeed(inner, fc, 42)

	cfg := simulation.SimulationConfig{
		Persona:  simulation.AdversarialUser,
		MaxTurns: 3,
		Provider: faulty,
	}
	orch := simulation.NewOrchestrator(cfg)

	// Turn 1 calls the echoAgent (succeeds), then the SimulatedUser tries to generate
	// turn 2's message — that's when the faulty provider is hit (ErrorRate = 1.0).
	_, err := orch.RunSimulation(context.Background(), "start", echoAgentFn)
	if err == nil {
		t.Fatal("expected fault injection error, got nil")
	}
}

// TestIntegration_FaultInjection_ZeroRate verifies that FaultInjector with ErrorRate=0
// passes all calls through and simulation completes normally.
func TestIntegration_FaultInjection_ZeroRate(t *testing.T) {
	t.Parallel()

	inner := newMockWithResponses([]string{"next message"})
	fc := simulation.FaultConfig{
		ErrorRate: 0.0,
	}
	clean := simulation.NewFaultInjectorWithSeed(inner, fc, 99)

	cfg := simulation.SimulationConfig{
		Persona:  simulation.FriendlyUser,
		MaxTurns: 2,
		Provider: clean,
	}
	orch := simulation.NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "hello", echoAgentFn)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}
	if result.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", result.TotalTurns)
	}
}

// TestIntegration_TraceTreeEvaluation builds a 3-agent trace tree (root → child1, child2)
// and evaluates trace_tree assertions through the Pipeline.
func TestIntegration_TraceTreeEvaluation(t *testing.T) {
	t.Parallel()

	tokens100 := 100
	cost01 := 0.01
	latency50 := 50

	parentID := "trace-root"
	childParentID := parentID

	child1 := &types.Trace{
		SchemaVersion: 1,
		TraceID:       "trace-child1",
		AgentID:       "agent-child1",
		ParentTraceID: &childParentID,
		Input:         json.RawMessage(`{"task":"summarize"}`),
		Output:        json.RawMessage(`{"summary":"done"}`),
		Metadata: &types.TraceMetadata{
			TotalTokens: &tokens100,
			CostUSD:     &cost01,
			LatencyMS:   &latency50,
		},
	}

	child2 := &types.Trace{
		SchemaVersion: 1,
		TraceID:       "trace-child2",
		AgentID:       "agent-child2",
		ParentTraceID: &childParentID,
		Input:         json.RawMessage(`{"task":"translate"}`),
		Output:        json.RawMessage(`{"translation":"done"}`),
		Metadata: &types.TraceMetadata{
			TotalTokens: &tokens100,
			CostUSD:     &cost01,
			LatencyMS:   &latency50,
		},
	}

	root := &types.Trace{
		SchemaVersion: 1,
		TraceID:       parentID,
		AgentID:       "agent-root",
		Input:         json.RawMessage(`{"query":"orchestrate"}`),
		Output:        json.RawMessage(`{"result":"orchestrated"}`),
		Steps: []types.Step{
			{
				Type:     types.StepTypeAgentCall,
				Name:     "call-child1",
				Args:     json.RawMessage(`{}`),
				Result:   json.RawMessage(`{}`),
				SubTrace: child1,
			},
			{
				Type:     types.StepTypeAgentCall,
				Name:     "call-child2",
				Args:     json.RawMessage(`{}`),
				Result:   json.RawMessage(`{}`),
				SubTrace: child2,
			},
		},
		Metadata: &types.TraceMetadata{
			TotalTokens: &tokens100,
			CostUSD:     &cost01,
			LatencyMS:   &latency50,
		},
	}

	registry := assertion.NewRegistry()
	pipeline := assertion.NewPipeline(registry)

	mustSpec := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal spec: %v", err)
		}
		return b
	}

	assertions := []types.Assertion{
		{
			AssertionID: "check-root-called",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "agent_called", "agent_id": "agent-root"}),
		},
		{
			AssertionID: "check-child1-called",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "agent_called", "agent_id": "agent-child1"}),
		},
		{
			AssertionID: "check-child2-called",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "agent_called", "agent_id": "agent-child2"}),
		},
		{
			AssertionID: "check-depth",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "delegation_depth", "max_depth": 2}),
		},
		{
			AssertionID: "check-cost",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "aggregate_cost", "operator": "lte", "value": 1.0}),
		},
		{
			AssertionID: "check-tokens",
			Type:        types.TypeTraceTree,
			Spec:        mustSpec(map[string]any{"check": "aggregate_tokens", "operator": "gte", "value": 200.0}),
		},
	}

	result, err := pipeline.EvaluateBatch(root, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch error: %v", err)
	}

	if len(result.Results) != len(assertions) {
		t.Fatalf("result count = %d, want %d", len(result.Results), len(assertions))
	}

	for _, ar := range result.Results {
		if ar.Status != types.StatusPass {
			t.Errorf("assertion %q: status = %q, explanation = %q", ar.AssertionID, ar.Status, ar.Explanation)
		}
	}
}

// TestIntegration_AggregateMetadata verifies AggregateMetadata sums tokens, cost, and latency
// across all traces in the tree.
func TestIntegration_AggregateMetadata(t *testing.T) {
	t.Parallel()

	tokens := func(n int) *int { return &n }
	cost := func(f float64) *float64 { return &f }
	latency := func(n int) *int { return &n }

	parentID := "root"
	child := &types.Trace{
		SchemaVersion: 1,
		TraceID:       "child",
		AgentID:       "agent-child",
		ParentTraceID: &parentID,
		Input:         json.RawMessage(`{}`),
		Output:        json.RawMessage(`{"ok":true}`),
		Metadata: &types.TraceMetadata{
			TotalTokens: tokens(500),
			CostUSD:     cost(0.05),
			LatencyMS:   latency(200),
		},
	}

	root := &types.Trace{
		SchemaVersion: 1,
		TraceID:       parentID,
		AgentID:       "agent-root",
		Input:         json.RawMessage(`{}`),
		Output:        json.RawMessage(`{"ok":true}`),
		Steps: []types.Step{
			{
				Type:     types.StepTypeAgentCall,
				Name:     "delegate",
				Args:     json.RawMessage(`{}`),
				Result:   json.RawMessage(`{}`),
				SubTrace: child,
			},
		},
		Metadata: &types.TraceMetadata{
			TotalTokens: tokens(300),
			CostUSD:     cost(0.03),
			LatencyMS:   latency(100),
		},
	}

	totalTokens, totalCostUSD, totalLatencyMS, agentCount := trace.AggregateMetadata(root)

	if agentCount != 2 {
		t.Errorf("agentCount = %d, want 2", agentCount)
	}
	if totalTokens != 800 {
		t.Errorf("totalTokens = %d, want 800", totalTokens)
	}
	if fmt.Sprintf("%.4f", totalCostUSD) != "0.0800" {
		t.Errorf("totalCostUSD = %f, want 0.08", totalCostUSD)
	}
	if totalLatencyMS != 300 {
		t.Errorf("totalLatencyMS = %d, want 300", totalLatencyMS)
	}
}
