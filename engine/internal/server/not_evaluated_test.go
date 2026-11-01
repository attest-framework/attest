package server

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// withoutProviders clears every env var that could enable an L5/L6
// provider so the test sees the engine's no-key behavior.
func withoutProviders(t *testing.T, simulation string) {
	t.Helper()
	t.Setenv("ATTEST_JUDGE_PROVIDER", "")
	t.Setenv("ATTEST_JUDGE_API_KEY", "")
	t.Setenv("ATTEST_JUDGE_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ATTEST_EMBEDDING_PROVIDER", "")
	t.Setenv("ATTEST_CACHE_DIR", t.TempDir())
	t.Setenv("ATTEST_SIMULATION", simulation)
}

func judgeBatch() types.EvaluateBatchParams {
	return types.EvaluateBatchParams{
		Trace: types.Trace{
			SchemaVersion: 1,
			TraceID:       "trace-ne",
			AgentID:       "agent-1",
			Input:         json.RawMessage(`"hello"`),
			Output:        json.RawMessage(`{"message":"world"}`),
		},
		Assertions: []types.Assertion{{
			AssertionID: "judge-1",
			Type:        types.TypeLLMJudge,
			Spec:        json.RawMessage(`{"target":"output.message","criteria":"helpful"}`),
		}},
	}
}

func evaluateJudgeOnly(t *testing.T) (types.InitializeResult, types.AssertionResult) {
	t.Helper()
	stdin, stdout, _ := newTestServer(t)
	sendRequest(t, stdin, 1, "initialize", initializeParams())
	initResp := readResponse(t, stdout)
	if initResp.Error != nil {
		t.Fatalf("initialize: %+v", initResp.Error)
	}
	var initRes types.InitializeResult
	if err := json.Unmarshal(initResp.Result, &initRes); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}

	sendRequest(t, stdin, 2, "evaluate_batch", judgeBatch())
	resp := readResponse(t, stdout)
	if resp.Error != nil {
		t.Fatalf("evaluate_batch: %+v", resp.Error)
	}
	var res types.EvaluateBatchResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode evaluate_batch: %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(res.Results))
	}
	return initRes, res.Results[0]
}

func TestHandler_NoJudgeKey_AcceptsJudgeAsNotConfigured(t *testing.T) {
	withoutProviders(t, "")

	initRes, r := evaluateJudgeOnly(t)

	for _, c := range []string{"llm_judge", "embedding"} {
		if !slices.Contains(initRes.Capabilities, c) {
			t.Errorf("capabilities %v missing %q", initRes.Capabilities, c)
		}
	}
	if slices.Contains(initRes.Capabilities, "simulation_mode") {
		t.Errorf("capabilities %v include simulation_mode without ATTEST_SIMULATION", initRes.Capabilities)
	}
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonProviderNotConfigured {
		t.Errorf("status=%q reason=%q, want not_evaluated/provider_not_configured", r.Status, r.Reason)
	}
}

func TestHandler_SimulationEnv_AdvertisesModeAndSkipsJudge(t *testing.T) {
	withoutProviders(t, "1")

	initRes, r := evaluateJudgeOnly(t)

	if !slices.Contains(initRes.Capabilities, "simulation_mode") {
		t.Errorf("capabilities %v missing simulation_mode", initRes.Capabilities)
	}
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonSimulation {
		t.Errorf("status=%q reason=%q, want not_evaluated/simulation", r.Status, r.Reason)
	}
}
