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

func TestJudgeRepeat_RunsExplicitN(t *testing.T) {
	// Repeat=5: median of [0.1, 0.4, 0.5, 0.6, 0.9] is 0.5.
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.1, "explanation": "r1"}`, Model: "mock-model", Cost: 0.002},
		{Content: `{"score": 0.4, "explanation": "r2"}`, Model: "mock-model", Cost: 0.002},
		{Content: `{"score": 0.5, "explanation": "r3"}`, Model: "mock-model", Cost: 0.002},
		{Content: `{"score": 0.6, "explanation": "r4"}`, Model: "mock-model", Cost: 0.002},
		{Content: `{"score": 0.9, "explanation": "r5"}`, Model: "mock-model", Cost: 0.002},
	}, nil)

	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)

	trace := &types.Trace{Output: json.RawMessage(`"five-sample target"`)}
	a := &types.Assertion{
		AssertionID: "repeat-5",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.4,"repeat":5}`),
	}

	result := evaluator.Evaluate(trace, a)

	if result.Score != 0.5 {
		t.Errorf("expected median 0.5, got %f", result.Score)
	}
	if mock.GetCallCount() != 5 {
		t.Errorf("expected 5 LLM calls, got %d", mock.GetCallCount())
	}
	if result.Judge == nil {
		t.Fatal("judge metadata missing")
	}
	if len(result.Judge.SampleScores) != 5 {
		t.Errorf("expected 5 sample scores, got %d", len(result.Judge.SampleScores))
	}
	// Cost should be sum of all 5 individual costs.
	if result.Cost < 0.009 || result.Cost > 0.011 {
		t.Errorf("cost should be ~5x single-run (~0.010), got %f", result.Cost)
	}
}

func TestJudgeRepeat_RangeRejected(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"zero", `{"target":"output","repeat":0,"meta_eval":false}`},
		{"negative", `{"target":"output","repeat":-3}`},
		{"over-max", `{"target":"output","repeat":17}`},
	}
	// Note: repeat:0 alone is treated as "unset" — combined with no other
	// run-count signal it falls through to single-pass. The negative and
	// over-max cases must hard-fail.
	for _, tc := range cases[1:] {
		t.Run(tc.name, func(t *testing.T) {
			mock := llm.NewMockProvider([]*llm.CompletionResponse{
				{Content: `{"score": 0.5, "explanation": "ok"}`, Model: "mock-model"},
			}, nil)
			rubrics := judge.NewRubricRegistry()
			evaluator := NewJudgeEvaluator(mock, rubrics, nil)
			trace := &types.Trace{Output: json.RawMessage(`"x"`)}
			a := &types.Assertion{
				AssertionID: "range-" + tc.name,
				Type:        types.TypeLLMJudge,
				Spec:        json.RawMessage(tc.spec),
			}
			result := evaluator.Evaluate(trace, a)
			if result.Status != types.StatusHardFail {
				t.Errorf("expected hard_fail for out-of-range repeat, got %s", result.Status)
			}
			if !strings.Contains(result.Explanation, "out of range") {
				t.Errorf("expected explanation to mention 'out of range', got %q", result.Explanation)
			}
			if mock.GetCallCount() != 0 {
				t.Errorf("expected 0 LLM calls for invalid repeat, got %d", mock.GetCallCount())
			}
		})
	}
}

func TestJudgeRepeat_VariancePopulatesMetadata(t *testing.T) {
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.4, "explanation": "a"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.7, "explanation": "b"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.5, "explanation": "c"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.6, "explanation": "d"}`, Model: "mock-model", Cost: 0.001},
	}, nil)
	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)
	trace := &types.Trace{Output: json.RawMessage(`"variance check"`)}
	a := &types.Assertion{
		AssertionID: "variance-meta",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.4,"repeat":4}`),
	}
	result := evaluator.Evaluate(trace, a)
	if result.Judge == nil {
		t.Fatal("judge metadata missing")
	}
	if result.Judge.RubricVersion == "" {
		t.Error("expected rubric_version on metadata")
	}
	if result.Judge.PromptHash == "" {
		t.Error("expected prompt_hash on metadata")
	}
	if result.Judge.ScoreStddev <= 0 {
		t.Errorf("expected positive stddev across 4 distinct scores, got %f", result.Judge.ScoreStddev)
	}
	if result.Judge.ScoreMean < 0.5 || result.Judge.ScoreMean > 0.6 {
		t.Errorf("expected mean ~0.55, got %f", result.Judge.ScoreMean)
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
