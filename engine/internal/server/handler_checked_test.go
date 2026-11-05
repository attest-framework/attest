package server

import (
	"encoding/json"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func checkedTrace() types.Trace {
	return types.Trace{
		SchemaVersion: 1,
		TraceID:       "trace-1",
		AgentID:       "agent-1",
		Input:         json.RawMessage(`"hello"`),
		Output:        json.RawMessage(`{"message":"world"}`),
		Steps:         []types.Step{{Type: types.StepTypeLLMCall, Name: "c", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)}},
	}
}

func submitPlugin(t *testing.T, send func(int64, string, any), recv func() *types.Response, id int64, assertionID, status string, score float64) {
	t.Helper()
	send(id, "submit_plugin_result", types.SubmitPluginResultParams{
		TraceID: "trace-1", PluginName: "lint", AssertionID: assertionID,
		Result: types.PluginResult{Status: status, Score: score, Explanation: "from the sdk"},
	})
	if resp := recv(); resp.Error != nil {
		t.Fatalf("submit_plugin_result: %+v", resp.Error)
	}
}

func evaluate(t *testing.T, send func(int64, string, any), recv func() *types.Response, id int64, as ...types.Assertion) types.EvaluateBatchResult {
	t.Helper()
	send(id, "evaluate_batch", types.EvaluateBatchParams{Trace: checkedTrace(), Assertions: as})
	resp := recv()
	if resp.Error != nil {
		t.Fatalf("evaluate_batch returned an error instead of results: %+v", resp.Error)
	}
	var out types.EvaluateBatchResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestHandler_EvaluateBatch_PluginAssertionFollowsSubmittedResultOverTheWire(t *testing.T) {
	send, recv := initServer(t)
	submitPlugin(t, send, recv, 2, "plug", types.StatusHardFail, 0.2)

	out := evaluate(t, send, recv, 3,
		types.Assertion{AssertionID: "plug", Type: types.TypePlugin, Spec: json.RawMessage(`{"plugin_id":"lint","soft":false}`), ScopeID: "t.py::test_a"},
		types.Assertion{AssertionID: "c", Type: types.TypeContent, Spec: json.RawMessage(`{"target":"output.message","check":"contains","value":"world"}`), ScopeID: "t.py::test_a"},
	)

	got := map[string]types.AssertionResult{}
	for _, r := range out.Results {
		got[r.AssertionID] = r
	}
	if got["plug"].Status != types.StatusHardFail || got["plug"].Explanation != "from the sdk" || got["plug"].Layer != 8 {
		t.Errorf("plugin row = %+v, want the submitted hard_fail on layer 8", got["plug"])
	}
	if got["plug"].ScopeID != "t.py::test_a" || len(got["plug"].SpecHash) != 64 {
		t.Errorf("plugin row scope/spec_hash = %q/%q, want echo and sha256", got["plug"].ScopeID, got["plug"].SpecHash)
	}
	if got["c"].Status != types.StatusPass {
		t.Errorf("content row = %+v, want pass", got["c"])
	}
}

func TestHandler_SubmitPluginResult_InvalidStatusOrScore_IsRejected(t *testing.T) {
	send, recv := initServer(t)

	for i, r := range []types.PluginResult{{Status: "ok", Score: 1}, {Status: types.StatusPass, Score: 2}} {
		send(int64(2+i), "submit_plugin_result", types.SubmitPluginResultParams{TraceID: "trace-1", PluginName: "p", AssertionID: "a", Result: r})
		resp := recv()
		if resp.Error == nil || resp.Error.Code != types.ErrAssertionError {
			t.Errorf("result %+v: error = %+v, want ASSERTION_ERROR", r, resp.Error)
		}
	}
}

func TestHandler_EvaluateBatch_SoftBudgetBreachKeepsHardFailAndSetsFlag(t *testing.T) {
	t.Setenv("ATTEST_MAX_SOFT_FAILURES", "0")
	send, recv := initServer(t)
	submitPlugin(t, send, recv, 2, "soft", types.StatusSoftFail, 0.4)

	out := evaluate(t, send, recv, 3,
		types.Assertion{AssertionID: "soft", Type: types.TypePlugin, Spec: json.RawMessage(`{"plugin_id":"lint","soft":true}`)},
		types.Assertion{AssertionID: "hard", Type: types.TypeContent, Spec: json.RawMessage(`{"target":"output.message","check":"contains","value":"absent"}`)},
	)

	if !out.BudgetExceeded {
		t.Error("budget_exceeded = false, want true")
	}
	statuses := map[string]string{}
	for _, r := range out.Results {
		statuses[r.AssertionID] = r.Status
	}
	if statuses["hard"] != types.StatusHardFail || statuses["soft"] != types.StatusSoftFail {
		t.Errorf("statuses = %v, want hard_fail and soft_fail rows kept", statuses)
	}
}
