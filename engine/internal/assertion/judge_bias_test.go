package assertion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestResolveBiasProbes_All(t *testing.T) {
	got, err := resolveBiasProbes([]string{"all"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 probes for 'all', got %d", len(got))
	}
}

func TestResolveBiasProbes_Unknown(t *testing.T) {
	_, err := resolveBiasProbes([]string{"verbosity", "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown probe")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention the unknown name, got %v", err)
	}
}

func TestResolveBiasProbes_Empty(t *testing.T) {
	got, err := resolveBiasProbes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestMutateForProbe_PreservesDelimiters(t *testing.T) {
	wrapped := judge.WrapAgentOutput("hello world")
	for _, name := range []string{biasProbeVerbosity, biasProbePosition, biasProbeSelfPreference} {
		mutated := mutateForProbe(name, wrapped)
		if !strings.Contains(mutated, "<<<AGENT_OUTPUT_START>>>") {
			t.Errorf("%s probe lost start delimiter", name)
		}
		if !strings.Contains(mutated, "<<<AGENT_OUTPUT_END>>>") {
			t.Errorf("%s probe lost end delimiter", name)
		}
		if !strings.Contains(mutated, "hello world") {
			t.Errorf("%s probe lost original content", name)
		}
		if mutated == wrapped {
			t.Errorf("%s probe did not mutate input", name)
		}
	}
}

func TestMutateForProbe_NoDelimitersFallsBack(t *testing.T) {
	plain := "no delimiters here"
	mutated := mutateForProbe(biasProbeVerbosity, plain)
	if mutated == plain {
		t.Error("verbosity probe should still mutate even without delimiters")
	}
}

func TestEvaluate_BiasProbesPopulateMetadata(t *testing.T) {
	// Baseline call returns 0.5; three probe calls return 0.9 each.
	// Expected deltas: +0.4 for each.
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.5, "explanation": "baseline"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.9, "explanation": "verbose"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.9, "explanation": "position"}`, Model: "mock-model", Cost: 0.001},
		{Content: `{"score": 0.9, "explanation": "self"}`, Model: "mock-model", Cost: 0.001},
	}, nil)
	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)
	trace := &types.Trace{Output: json.RawMessage(`"target text"`)}
	a := &types.Assertion{
		AssertionID: "bias-probes",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","rubric":"default","threshold":0.4,"bias_probes":["all"]}`),
	}
	result := evaluator.Evaluate(trace, a)

	if result.Score != 0.5 {
		t.Errorf("expected baseline score 0.5, got %f", result.Score)
	}
	if result.Judge == nil {
		t.Fatal("judge metadata missing")
	}
	if len(result.Judge.BiasProbes) != 3 {
		t.Fatalf("expected 3 bias probes, got %d", len(result.Judge.BiasProbes))
	}
	for _, p := range result.Judge.BiasProbes {
		if p.Score != 0.9 {
			t.Errorf("probe %s: expected score 0.9, got %f", p.Name, p.Score)
		}
		if p.Delta < 0.39 || p.Delta > 0.41 {
			t.Errorf("probe %s: expected delta ~+0.4, got %f", p.Name, p.Delta)
		}
	}
	// Cost should be 4× per-call (~0.004).
	if result.Cost < 0.0035 || result.Cost > 0.0045 {
		t.Errorf("expected cost ~0.004 (4 calls), got %f", result.Cost)
	}
}

func TestEvaluate_BiasProbesUnknownNameFails(t *testing.T) {
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: `{"score": 0.5, "explanation": "ok"}`, Model: "mock-model"},
	}, nil)
	rubrics := judge.NewRubricRegistry()
	evaluator := NewJudgeEvaluator(mock, rubrics, nil)
	trace := &types.Trace{Output: json.RawMessage(`"x"`)}
	a := &types.Assertion{
		AssertionID: "bias-bad",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","bias_probes":["typo"]}`),
	}
	result := evaluator.Evaluate(trace, a)
	if result.Status != types.StatusHardFail {
		t.Errorf("expected hard_fail for unknown probe, got %s", result.Status)
	}
	if mock.GetCallCount() != 0 {
		t.Errorf("expected 0 LLM calls before validation, got %d", mock.GetCallCount())
	}
}
