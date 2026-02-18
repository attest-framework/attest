package assertion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestJudgeMeta_MedianScore(t *testing.T) {
	// Three responses with different scores — median should be selected
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.3, "explanation": "run one"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.7, "explanation": "run two"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.5, "explanation": "run three"}`, Model: "mock-model", Cost: 0.001},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)

	trace := &types.Trace{
		Output: json.RawMessage(`"Test output for meta-eval"`),
	}
	a := &types.Assertion{
		AssertionID: "meta-1",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.4,"meta_eval":true}`),
	}

	result := evaluator.Evaluate(trace, a)

	// Sorted scores: [0.3, 0.5, 0.7] → median is 0.5
	if result.Score != 0.5 {
		t.Errorf("expected median score 0.5, got %f", result.Score)
	}

	if result.Status != types.StatusPass {
		t.Errorf("expected pass (0.5 >= 0.4 threshold), got %s", result.Status)
	}

	// Should have called the mock 3 times
	if mock.GetCallCount() != 3 {
		t.Errorf("expected 3 LLM calls for meta-eval, got %d", mock.GetCallCount())
	}

	// Explanation should contain all three runs
	if !strings.Contains(result.Explanation, "Run 1:") {
		t.Error("explanation missing Run 1")
	}
	if !strings.Contains(result.Explanation, "Run 2:") {
		t.Error("explanation missing Run 2")
	}
	if !strings.Contains(result.Explanation, "Run 3:") {
		t.Error("explanation missing Run 3")
	}
	if !strings.Contains(result.Explanation, "Median selected.") {
		t.Error("explanation missing 'Median selected.' marker")
	}
}

func TestJudgeMeta_HighVarianceFlag(t *testing.T) {
	// Three responses with high spread (>0.2)
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.2, "explanation": "low"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.8, "explanation": "high"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.5, "explanation": "mid"}`, Model: "mock-model", Cost: 0.001},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)

	trace := &types.Trace{
		Output: json.RawMessage(`"Ambiguous output"`),
	}
	a := &types.Assertion{
		AssertionID: "meta-variance-1",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.4,"meta_eval":true}`),
	}

	result := evaluator.Evaluate(trace, a)

	// Spread = 0.8 - 0.2 = 0.6 > 0.2 threshold
	if !strings.Contains(result.Explanation, "HIGH VARIANCE") {
		t.Error("expected HIGH VARIANCE flag in explanation for spread > 0.2")
	}

	// Median of [0.2, 0.5, 0.8] = 0.5
	if result.Score != 0.5 {
		t.Errorf("expected median score 0.5, got %f", result.Score)
	}
}

func TestJudgeMeta_LowVarianceNoFlag(t *testing.T) {
	// Three responses with low spread (<=0.2)
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.7, "explanation": "good"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.8, "explanation": "good"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.75, "explanation": "good"}`, Model: "mock-model", Cost: 0.001},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)

	trace := &types.Trace{
		Output: json.RawMessage(`"Consistent output"`),
	}
	a := &types.Assertion{
		AssertionID: "meta-lowvar-1",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.5,"meta_eval":true}`),
	}

	result := evaluator.Evaluate(trace, a)

	// Spread = 0.8 - 0.7 = 0.1 <= 0.2 threshold
	if strings.Contains(result.Explanation, "HIGH VARIANCE") {
		t.Error("did not expect HIGH VARIANCE flag for spread <= 0.2")
	}

	// Median of [0.7, 0.75, 0.8] = 0.75
	if result.Score != 0.75 {
		t.Errorf("expected median score 0.75, got %f", result.Score)
	}
}

func TestJudgeMeta_DisabledByDefault(t *testing.T) {
	// Without meta_eval: true, should do single pass
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.6, "explanation": "single pass"}`, Model: "mock-model", Cost: 0.001},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)

	trace := &types.Trace{
		Output: json.RawMessage(`"Test output"`),
	}
	a := &types.Assertion{
		AssertionID: "meta-disabled-1",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.5}`),
	}

	result := evaluator.Evaluate(trace, a)

	if result.Score != 0.6 {
		t.Errorf("expected score 0.6, got %f", result.Score)
	}

	// Single pass = 1 call
	if mock.GetCallCount() != 1 {
		t.Errorf("expected 1 LLM call (single pass), got %d", mock.GetCallCount())
	}
}
