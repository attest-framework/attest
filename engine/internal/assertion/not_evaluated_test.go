package assertion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// failingEmbedder simulates an embedding provider outage.
type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("503 service unavailable")
}
func (failingEmbedder) Model() string { return "failing" }

func judgeAssertion(id string) types.Assertion {
	return types.Assertion{
		AssertionID: id,
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output","threshold":0.5}`),
	}
}

func embeddingAssertion(id string) types.Assertion {
	return types.Assertion{
		AssertionID: id,
		Type:        types.TypeEmbedding,
		Spec:        json.RawMessage(`{"target":"output","reference":"x","threshold":0.5}`),
	}
}

func evalOne(t *testing.T, p *Pipeline, trace *types.Trace, as ...types.Assertion) map[string]types.AssertionResult {
	t.Helper()
	res, err := p.EvaluateBatch(context.Background(), trace, as)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(res.Results) != len(as) {
		t.Fatalf("got %d results for %d assertions; every requested assertion must appear", len(res.Results), len(as))
	}
	out := make(map[string]types.AssertionResult, len(res.Results))
	for _, r := range res.Results {
		out[r.AssertionID] = r
	}
	return out
}

func assertNotEvaluated(t *testing.T, r types.AssertionResult, reason string) {
	t.Helper()
	if r.Status != types.StatusNotEvaluated {
		t.Fatalf("%s: status=%q, want not_evaluated (explanation: %s)", r.AssertionID, r.Status, r.Explanation)
	}
	if r.Reason != reason {
		t.Errorf("%s: reason=%q, want %q", r.AssertionID, r.Reason, reason)
	}
	if r.FailureClass != "" {
		t.Errorf("%s: failure_class=%q, want empty (set only on failures)", r.AssertionID, r.FailureClass)
	}
	if r.Layer == 0 || r.Type == "" {
		t.Errorf("%s: layer=%d type=%q, want diagnostics annotated", r.AssertionID, r.Layer, r.Type)
	}
}

func TestPipeline_NoProvider_ReturnsProviderNotConfigured(t *testing.T) {
	p := NewPipeline(NewRegistry())
	p.SetSimulation(false)

	got := evalOne(t, p, testTrace(), judgeAssertion("judge"), embeddingAssertion("emb"))

	assertNotEvaluated(t, got["judge"], types.ReasonProviderNotConfigured)
	assertNotEvaluated(t, got["emb"], types.ReasonProviderNotConfigured)
}

func TestPipeline_FailingJudgeProvider_ReturnsProviderUnavailable(t *testing.T) {
	provider := llm.NewMockProvider(nil, []error{errors.New("connection refused")})
	p := NewPipeline(NewRegistry(WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(false)

	got := evalOne(t, p, testTrace(), judgeAssertion("judge"))

	assertNotEvaluated(t, got["judge"], types.ReasonProviderUnavailable)
}

func TestPipeline_UnparseableJudgeResponse_ReturnsProviderUnavailable(t *testing.T) {
	provider := llm.NewMockProvider([]*llm.CompletionResponse{{Content: "not json", Cost: 0.002}}, nil)
	p := NewPipeline(NewRegistry(WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(false)

	got := evalOne(t, p, testTrace(), judgeAssertion("judge"))

	assertNotEvaluated(t, got["judge"], types.ReasonProviderUnavailable)
	if got["judge"].Cost != 0.002 {
		t.Errorf("cost = %v, want 0.002 (billed call must be reported)", got["judge"].Cost)
	}
}

func TestPipeline_RepeatedJudgeAllRunsFail_ReturnsProviderUnavailable(t *testing.T) {
	boom := errors.New("timeout")
	provider := llm.NewMockProvider(nil, []error{boom, boom, boom})
	p := NewPipeline(NewRegistry(WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(false)
	a := judgeAssertion("judge")
	a.Spec = json.RawMessage(`{"target":"output","threshold":0.5,"repeat":3}`)

	got := evalOne(t, p, testTrace(), a)

	assertNotEvaluated(t, got["judge"], types.ReasonProviderUnavailable)
}

func TestPipeline_FailingEmbedder_ReturnsProviderUnavailable(t *testing.T) {
	p := NewPipeline(NewRegistry(WithEmbedding(failingEmbedder{}, nil)))
	p.SetSimulation(false)

	got := evalOne(t, p, testTrace(), embeddingAssertion("emb"))

	assertNotEvaluated(t, got["emb"], types.ReasonProviderUnavailable)
}

func TestPipeline_Simulation_RunsDeterministicLayersAndSkipsProviders(t *testing.T) {
	provider := llm.NewMockProvider([]*llm.CompletionResponse{{Content: `{"score":1,"explanation":"ok"}`}}, nil)
	p := NewPipeline(NewRegistry(WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(true)
	content := types.Assertion{
		AssertionID: "content",
		Type:        types.TypeContent,
		Spec:        json.RawMessage(`{"target":"output","check":"contains","value":"definitely-absent"}`),
	}
	passing := types.Assertion{
		AssertionID: "steps",
		Type:        types.TypeConstraint,
		Spec:        json.RawMessage(`{"field":"steps.length","operator":"gte","value":0}`),
	}

	got := evalOne(t, p, testTrace(), passing, judgeAssertion("judge"))
	failing := evalOne(t, p, testTrace(), content, judgeAssertion("judge"))

	if got["steps"].Status != types.StatusPass {
		t.Errorf("steps: status=%q, want pass", got["steps"].Status)
	}
	assertNotEvaluated(t, got["judge"], types.ReasonSimulation)
	if failing["content"].Status != types.StatusHardFail {
		t.Errorf("content: status=%q, want hard_fail in simulation", failing["content"].Status)
	}
	assertNotEvaluated(t, failing["judge"], types.ReasonShortCircuited)
	if provider.GetCallCount() != 0 {
		t.Errorf("judge provider called %d times in simulation, want 0", provider.GetCallCount())
	}
}

func TestConstraint_UnsetMetadata_ReturnsMissingEvidence(t *testing.T) {
	p := NewPipeline(NewRegistry())
	trace := &types.Trace{TraceID: "trc_no_meta", Output: json.RawMessage(`{"message":"ok"}`)}
	a := types.Assertion{
		AssertionID: "cost",
		Type:        types.TypeConstraint,
		Spec:        json.RawMessage(`{"field":"metadata.cost_usd","operator":"lte","value":0.01}`),
	}

	got := evalOne(t, p, trace, a)

	assertNotEvaluated(t, got["cost"], types.ReasonMissingEvidence)
}

// A dynamic threshold must not reclassify a not_evaluated row: with a
// configured history store it used to become hard_fail/broken_code.
func TestDynamicThreshold_KeepsNotEvaluated(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	p := NewPipelineWithHistory(NewRegistry(), store)
	trace := &types.Trace{TraceID: "trc_dyn_missing", Output: json.RawMessage(`{"message":"ok"}`)}
	a := types.Assertion{
		AssertionID: "cost",
		Type:        types.TypeConstraint,
		Spec:        json.RawMessage(`{"field":"metadata.cost_usd","operator":"lte","value":0.01,"threshold":"dynamic"}`),
	}

	got := evalOne(t, p, trace, a)["cost"]

	assertNotEvaluated(t, got, types.ReasonMissingEvidence)
	if got.FailureClass != "" {
		t.Errorf("failure_class=%q on a not_evaluated row", got.FailureClass)
	}
	if got.ThresholdSource != "" {
		t.Errorf("threshold_source=%q on a not_evaluated row", got.ThresholdSource)
	}
}

// Pipelines built by tests must not pick up simulation mode from the shell.
func TestNewPipeline_IgnoresSimulationEnv(t *testing.T) {
	t.Setenv("ATTEST_SIMULATION", "1")
	p := NewPipeline(NewRegistry())
	if p.simulation {
		t.Fatal("NewPipeline enabled simulation from ATTEST_SIMULATION; only SetSimulation may")
	}
}

func TestSimulationModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{{"1", true}, {"true", true}, {"YES", true}, {"", false}, {"0", false}} {
		t.Setenv("ATTEST_SIMULATION", tc.val)
		if got := SimulationModeFromEnv(); got != tc.want {
			t.Errorf("ATTEST_SIMULATION=%q: got %v, want %v", tc.val, got, tc.want)
		}
	}
}
