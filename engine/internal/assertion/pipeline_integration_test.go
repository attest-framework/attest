package assertion

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// --- E19: Full pipeline integration tests with mock LLM ---

// mockEmbedder returns fixed vectors for testing embedding similarity evaluation.
type mockEmbedder struct {
	model     string
	callCount atomic.Int64
	// vectors maps input text → vector. Falls back to a default vector.
	vectors map[string][]float32
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.callCount.Add(1)
	if v, ok := m.vectors[text]; ok {
		return v, nil
	}
	// Default: return a unit vector.
	return []float32{1.0, 0.0, 0.0}, nil
}

func (m *mockEmbedder) Model() string { return m.model }

// testTrace returns a minimal trace suitable for pipeline evaluation.
func testTrace() *types.Trace {
	return &types.Trace{
		TraceID: "trc_integration",
		AgentID: "agent-1",
		Input:   json.RawMessage(`"test input"`),
		Output:  json.RawMessage(`"The agent produced a helpful, accurate response about climate change."`),
		Steps: []types.Step{
			{
				Type:   types.StepTypeLLMCall,
				Name:   "generate",
				Args:   json.RawMessage(`{"prompt":"test"}`),
				Result: json.RawMessage(`{"content":"response"}`),
			},
		},
	}
}

func TestPipeline_Integration_L5Embedding_Pass(t *testing.T) {
	embedder := &mockEmbedder{
		model: "mock-embed",
		vectors: map[string][]float32{
			// Target resolves to the trace output string.
			"The agent produced a helpful, accurate response about climate change.": {0.9, 0.1, 0.0},
			"climate change information": {0.85, 0.15, 0.0},
		},
	}

	registry := NewRegistry(WithEmbedding(embedder, nil))
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		{
			AssertionID: "emb-1",
			Type:        types.TypeEmbedding,
			Spec: json.RawMessage(`{
				"target": "output",
				"reference": "climate change information",
				"threshold": 0.8
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != types.StatusPass {
		t.Errorf("embedding assertion: status = %q, want pass; explanation: %s",
			result.Results[0].Status, result.Results[0].Explanation)
	}
	if embedder.callCount.Load() < 2 {
		t.Errorf("expected >= 2 embedder calls (target + reference), got %d", embedder.callCount.Load())
	}
}

func TestPipeline_Integration_L6Judge_Pass(t *testing.T) {
	mockProvider := llm.NewMockProvider([]*llm.CompletionResponse{
		{
			Content:      `{"score": 0.9, "explanation": "Excellent response on climate change."}`,
			Model:        "mock-model",
			InputTokens:  50,
			OutputTokens: 20,
			Cost:         0.002,
			DurationMS:   100,
		},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(WithJudge(mockProvider, rubrics, nil))
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		{
			AssertionID: "judge-1",
			Type:        types.TypeLLMJudge,
			Spec: json.RawMessage(`{
				"target": "output",
				"criteria": "Is the response helpful and accurate?",
				"threshold": 0.8
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != types.StatusPass {
		t.Errorf("judge assertion: status = %q, want pass; explanation: %s",
			result.Results[0].Status, result.Results[0].Explanation)
	}
	if result.Results[0].Score < 0.9 {
		t.Errorf("judge score = %f, want >= 0.9", result.Results[0].Score)
	}
	if result.TotalCost == 0 {
		t.Error("TotalCost should be > 0 when judge runs")
	}
}

func TestPipeline_Integration_L6Judge_HardFail(t *testing.T) {
	mockProvider := llm.NewMockProvider([]*llm.CompletionResponse{
		{
			Content:      `{"score": 0.3, "explanation": "Response was vague and unhelpful."}`,
			Model:        "mock-model",
			InputTokens:  50,
			OutputTokens: 20,
			Cost:         0.002,
		},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(WithJudge(mockProvider, rubrics, nil))
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		{
			AssertionID: "judge-fail",
			Type:        types.TypeLLMJudge,
			Spec: json.RawMessage(`{
				"target": "output",
				"threshold": 0.8
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if result.Results[0].Status != types.StatusHardFail {
		t.Errorf("judge assertion: status = %q, want hard_fail", result.Results[0].Status)
	}
}

func TestPipeline_Integration_ConcurrentL5L6(t *testing.T) {
	embedder := &mockEmbedder{
		model: "mock-embed",
		vectors: map[string][]float32{
			"The agent produced a helpful, accurate response about climate change.": {0.9, 0.1, 0.0},
			"relevant topic": {0.85, 0.15, 0.0},
		},
	}

	mockProvider := llm.NewMockProvider([]*llm.CompletionResponse{
		{
			Content:      `{"score": 0.85, "explanation": "Good quality response."}`,
			Model:        "mock-model",
			InputTokens:  50,
			OutputTokens: 20,
			Cost:         0.002,
		},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(
		WithEmbedding(embedder, nil),
		WithJudge(mockProvider, rubrics, nil),
	)
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		// L1: schema assertion (runs first).
		{
			AssertionID: "schema-1",
			Type:        types.TypeSchema,
			Spec: json.RawMessage(`{
				"target": "output",
				"schema": {"type": "string"}
			}`),
		},
		// L5: embedding (runs concurrently with L6).
		{
			AssertionID: "emb-concurrent",
			Type:        types.TypeEmbedding,
			Spec: json.RawMessage(`{
				"target": "output",
				"reference": "relevant topic",
				"threshold": 0.5
			}`),
		},
		// L6: judge (runs concurrently with L5).
		{
			AssertionID: "judge-concurrent",
			Type:        types.TypeLLMJudge,
			Spec: json.RawMessage(`{
				"target": "output",
				"threshold": 0.7
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.Results))
	}

	// Verify all pass.
	for _, r := range result.Results {
		if r.Status != types.StatusPass {
			t.Errorf("assertion %q: status = %q, want pass; explanation: %s",
				r.AssertionID, r.Status, r.Explanation)
		}
	}

	// Verify ordering: L1 result first, then L5/L6 results.
	if result.Results[0].AssertionID != "schema-1" {
		t.Errorf("first result should be schema-1 (L1), got %q", result.Results[0].AssertionID)
	}
}

func TestPipeline_Integration_L14HardFail_GatesL56(t *testing.T) {
	embedder := &mockEmbedder{model: "mock-embed"}
	mockProvider := llm.NewMockProvider(nil, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(
		WithEmbedding(embedder, nil),
		WithJudge(mockProvider, rubrics, nil),
	)
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		// L1: schema assertion that will fail.
		{
			AssertionID: "schema-fail",
			Type:        types.TypeSchema,
			Spec: json.RawMessage(`{
				"target": "output",
				"schema": {"type": "number"}
			}`),
		},
		// L5: should be skipped due to L1 hard fail.
		{
			AssertionID: "emb-skipped",
			Type:        types.TypeEmbedding,
			Spec: json.RawMessage(`{
				"target": "output",
				"reference": "something",
				"threshold": 0.5
			}`),
		},
		// L6: should be skipped due to L1 hard fail.
		{
			AssertionID: "judge-skipped",
			Type:        types.TypeLLMJudge,
			Spec: json.RawMessage(`{
				"target": "output",
				"threshold": 0.5
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}

	// Only L1 result should be present — L5/L6 are gated.
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result (L5/L6 gated), got %d", len(result.Results))
	}
	if result.Results[0].AssertionID != "schema-fail" {
		t.Errorf("result[0] = %q, want schema-fail", result.Results[0].AssertionID)
	}
	if result.Results[0].Status != types.StatusHardFail {
		t.Errorf("schema status = %q, want hard_fail", result.Results[0].Status)
	}

	// Verify mock provider was never called.
	if mockProvider.GetCallCount() != 0 {
		t.Errorf("mock provider called %d times, want 0 (L6 should be gated)", mockProvider.GetCallCount())
	}
	if embedder.callCount.Load() != 0 {
		t.Errorf("embedder called %d times, want 0 (L5 should be gated)", embedder.callCount.Load())
	}
}

func TestPipeline_Integration_BudgetEnforcement(t *testing.T) {
	mockProvider := llm.NewMockProvider([]*llm.CompletionResponse{
		{
			Content:      `{"score": 0.3, "explanation": "Poor response."}`,
			Model:        "mock-model",
			InputTokens:  50,
			OutputTokens: 20,
			Cost:         0.002,
		},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(WithJudge(mockProvider, rubrics, nil))
	pipeline := NewPipeline(registry)

	// Budget: 0 soft failures allowed.
	budget := NewBudgetTracker(0)

	assertions := []types.Assertion{
		{
			AssertionID: "judge-soft-1",
			Type:        types.TypeLLMJudge,
			Spec: json.RawMessage(`{
				"target": "output",
				"threshold": 0.8,
				"soft": true
			}`),
		},
	}

	_, err := pipeline.EvaluateBatchWithBudget(context.Background(), testTrace(), assertions, budget)
	if err == nil {
		t.Fatal("expected BudgetExceededError, got nil")
	}

	var budgetErr *BudgetExceededError
	isBudgetErr := false
	if be, ok := err.(*BudgetExceededError); ok {
		budgetErr = be
		isBudgetErr = true
	}
	if !isBudgetErr {
		t.Fatalf("expected *BudgetExceededError, got %T: %v", err, err)
	}
	if budgetErr.Limit != 0 {
		t.Errorf("BudgetExceededError.Limit = %d, want 0", budgetErr.Limit)
	}
}

func TestPipeline_Integration_MultipleConcurrentL56(t *testing.T) {
	// Test with multiple L5 and L6 assertions running concurrently.
	embedder := &mockEmbedder{
		model: "mock-embed",
		vectors: map[string][]float32{
			"The agent produced a helpful, accurate response about climate change.": {0.9, 0.1, 0.0},
			"topic A": {0.85, 0.15, 0.0},
			"topic B": {0.8, 0.2, 0.0},
		},
	}

	mockProvider := llm.NewMockProvider([]*llm.CompletionResponse{
		{
			Content: `{"score": 0.85, "explanation": "Good."}`,
			Model:   "mock-model",
			Cost:    0.001,
		},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	registry := NewRegistry(
		WithEmbedding(embedder, nil),
		WithJudge(mockProvider, rubrics, nil),
	)
	pipeline := NewPipeline(registry)

	assertions := []types.Assertion{
		{
			AssertionID: "emb-a",
			Type:        types.TypeEmbedding,
			Spec:        json.RawMessage(`{"target":"output","reference":"topic A","threshold":0.5}`),
		},
		{
			AssertionID: "emb-b",
			Type:        types.TypeEmbedding,
			Spec:        json.RawMessage(`{"target":"output","reference":"topic B","threshold":0.5}`),
		},
		{
			AssertionID: "judge-a",
			Type:        types.TypeLLMJudge,
			Spec:        json.RawMessage(`{"target":"output","threshold":0.7}`),
		},
		{
			AssertionID: "judge-b",
			Type:        types.TypeLLMJudge,
			Spec:        json.RawMessage(`{"target":"output","threshold":0.7}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), testTrace(), assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(result.Results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result.Results))
	}

	for _, r := range result.Results {
		if r.Status != types.StatusPass {
			t.Errorf("assertion %q: status = %q, want pass; explanation: %s",
				r.AssertionID, r.Status, r.Explanation)
		}
	}

	// Verify both embedding and judge were called.
	if embedder.callCount.Load() < 4 {
		t.Errorf("embedder calls = %d, want >= 4 (2 per assertion)", embedder.callCount.Load())
	}
	if mockProvider.GetCallCount() < 2 {
		t.Errorf("judge calls = %d, want >= 2", mockProvider.GetCallCount())
	}
}
