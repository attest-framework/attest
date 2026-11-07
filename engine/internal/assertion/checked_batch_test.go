package assertion

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/attest-framework/attest/engine/internal/assertion/judge"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func contentContains(id, keyword string) types.Assertion {
	return types.Assertion{
		AssertionID: id,
		Type:        types.TypeContent,
		Spec:        json.RawMessage(`{"target":"output","check":"contains","value":"` + keyword + `"}`),
	}
}

func pluginAssertion(id, plugin string) types.Assertion {
	return types.Assertion{
		AssertionID: id,
		Type:        types.TypePlugin,
		Spec:        json.RawMessage(`{"plugin_id":"` + plugin + `","soft":false}`),
	}
}

func TestPipeline_SoftBudgetBreachWithHardFail_KeepsEveryRowAndFlagsBatch(t *testing.T) {
	// Arrange
	p := NewPipeline(NewRegistry())
	budget := NewBudgetTracker(0)
	soft := pluginAssertion("soft", "p")
	reg := p.registry.PluginResults()
	reg.Submit("trace", "soft", "p", types.PluginResult{Status: types.StatusSoftFail, Score: 0.4, Explanation: "meh"})
	reg.Submit("trace", "hard", "p", types.PluginResult{Status: types.StatusHardFail, Score: 0, Explanation: "bad"})
	tr := testTrace()
	tr.TraceID = "trace"

	// Act
	res, err := p.EvaluateBatchWithBudget(context.Background(), tr,
		[]types.Assertion{soft, pluginAssertion("hard", "p"), contentContains("c", "zzz-not-there")}, budget)

	// Assert
	if err != nil {
		t.Fatalf("EvaluateBatchWithBudget: %v", err)
	}
	got := map[string]string{}
	for _, r := range res.Results {
		got[r.AssertionID] = r.Status
	}
	if len(got) != 3 || got["hard"] != types.StatusHardFail || got["soft"] != types.StatusSoftFail || got["c"] != types.StatusHardFail {
		t.Errorf("rows = %v, want all three with their own statuses", got)
	}
	if !res.BudgetExceeded {
		t.Error("BudgetExceeded = false, want true")
	}
}

func TestPipeline_PluginAssertion_FollowsSubmittedResultWhileJudgeStillEvaluates(t *testing.T) {
	// Arrange: a local fake judge scoring 0.95 and a plugin result that hard-fails.
	provider := llm.NewMockProvider([]*llm.CompletionResponse{{Content: `{"score": 0.95, "explanation": "good"}`, Model: "fake"}}, nil)
	p := NewPipeline(NewRegistry(WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(false)
	p.registry.PluginResults().Submit("trace", "plug", "lint", types.PluginResult{Status: types.StatusHardFail, Score: 0.1, Explanation: "lint failed"})
	tr := testTrace()
	tr.TraceID = "trace"

	// Act
	got := evalOne(t, p, tr, pluginAssertion("plug", "lint"), judgeAssertion("judge"))

	// Assert
	if got["plug"].Status != types.StatusHardFail || got["plug"].Explanation != "lint failed" || got["plug"].Score != 0.1 {
		t.Errorf("plugin row = %+v, want the submitted hard_fail", got["plug"])
	}
	if got["judge"].Status != types.StatusPass {
		t.Errorf("judge row = %+v, want pass: a plugin hard fail must not gate L5/L6", got["judge"])
	}
	if provider.CallCount != 1 {
		t.Errorf("judge provider calls = %d, want 1", provider.CallCount)
	}
}

func TestPipeline_PluginAssertion_WithoutSubmittedResult_IsNotEvaluated(t *testing.T) {
	p := NewPipeline(NewRegistry())
	tr := testTrace()
	tr.TraceID = "trace"

	got := evalOne(t, p, tr, pluginAssertion("plug", "lint"))

	assertNotEvaluated(t, got["plug"], types.ReasonMissingEvidence)
}

func TestPipeline_PluginAssertion_ResultFromOtherPlugin_HardFails(t *testing.T) {
	p := NewPipeline(NewRegistry())
	p.registry.PluginResults().Submit("trace", "plug", "other", types.PluginResult{Status: types.StatusPass, Score: 1})
	tr := testTrace()
	tr.TraceID = "trace"

	got := evalOne(t, p, tr, pluginAssertion("plug", "lint"))

	if got["plug"].Status != types.StatusHardFail {
		t.Errorf("status = %q, want hard_fail", got["plug"].Status)
	}
}

func TestValidatePluginResult_RejectsBadStatusAndScore(t *testing.T) {
	for _, r := range []types.PluginResult{
		{Status: "ok", Score: 1},
		{Status: types.StatusPass, Score: 1.5},
		{Status: types.StatusPass, Score: -0.1},
	} {
		if err := ValidatePluginResult(r); err == nil {
			t.Errorf("ValidatePluginResult(%+v) = nil, want error", r)
		}
	}
	if err := ValidatePluginResult(types.PluginResult{Status: types.StatusSoftFail, Score: 0.5}); err != nil {
		t.Errorf("valid result rejected: %v", err)
	}
}

func TestSpecHash_KeyOrderInsensitive(t *testing.T) {
	a := SpecHash(types.TypeContent, json.RawMessage(`{"a":1,"b":[1,2]}`))
	b := SpecHash(types.TypeContent, json.RawMessage(`{ "b": [1, 2], "a": 1 }`))
	c := SpecHash(types.TypeContent, json.RawMessage(`{"a":2,"b":[1,2]}`))
	if a != b || a == c {
		t.Errorf("hashes a=%s b=%s c=%s: want a==b, a!=c", a, b, c)
	}
}

func TestAnnotateDiagnostics_EchoesScopeAndSpecHash(t *testing.T) {
	a := types.Assertion{AssertionID: "x", Type: types.TypeContent, Spec: json.RawMessage(`{"k":1}`), ScopeID: "tests/t.py::test_a"}
	r := types.AssertionResult{AssertionID: "x"}

	annotateDiagnostics(&r, &a)

	if r.ScopeID != "tests/t.py::test_a" || r.SpecHash != SpecHash(a.Type, a.Spec) {
		t.Errorf("row = %+v, want scope and spec hash filled", r)
	}
}
