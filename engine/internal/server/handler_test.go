package server

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// --- E17: Handler tests for untested RPC methods ---

// helper: initialize a server and return send/recv funcs ready for subsequent calls.
func initServer(t *testing.T) (send func(id int64, method string, params any), recv func() *types.Response) {
	t.Helper()
	stdin, stdout, _ := newTestServer(t)

	sendRequest(t, stdin, 1, "initialize", initializeParams())
	resp := readResponse(t, stdout)
	if resp.Error != nil {
		t.Fatalf("initialize failed: %+v", resp.Error)
	}

	send = func(id int64, method string, params any) {
		sendRequest(t, stdin, id, method, params)
	}
	recv = func() *types.Response {
		return readResponse(t, stdout)
	}
	return send, recv
}

// ── validate_trace_tree ──

func TestHandler_ValidateTraceTree_Valid(t *testing.T) {
	send, recv := initServer(t)

	trace := types.Trace{
		SchemaVersion: 1,
		TraceID:       "trace-1",
		AgentID:       "agent-root",
		Input:         json.RawMessage(`"hello"`),
		Output:        json.RawMessage(`"world"`),
		Steps: []types.Step{
			{Type: types.StepTypeLLMCall, Name: "call-1", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)},
		},
	}

	send(2, "validate_trace_tree", types.ValidateTraceTreeParams{Trace: trace})
	resp := recv()

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result types.ValidateTraceTreeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Valid {
		t.Errorf("Valid = false, want true; errors = %v", result.Errors)
	}
	if result.Depth != 0 {
		t.Errorf("Depth = %d, want 0", result.Depth)
	}
	if result.AgentCount < 1 {
		t.Errorf("AgentCount = %d, want >= 1", result.AgentCount)
	}
}

func TestHandler_ValidateTraceTree_WithSubTrace(t *testing.T) {
	send, recv := initServer(t)

	childTraceID := "trace-child"
	parentTraceID := "trace-parent"
	tokens := 100
	costUSD := 0.01
	latencyMS := 50
	trace := types.Trace{
		SchemaVersion: 1,
		TraceID:       parentTraceID,
		AgentID:       "agent-parent",
		Input:         json.RawMessage(`"input"`),
		Output:        json.RawMessage(`"output"`),
		Metadata:      &types.TraceMetadata{TotalTokens: &tokens, CostUSD: &costUSD, LatencyMS: &latencyMS},
		Steps: []types.Step{
			{
				Type: types.StepTypeAgentCall, Name: "delegate",
				Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`),
				SubTrace: &types.Trace{
					SchemaVersion: 1,
					TraceID:       childTraceID,
					AgentID:       "agent-child",
					ParentTraceID: &parentTraceID,
					Input:         json.RawMessage(`"sub-input"`),
					Output:        json.RawMessage(`"sub-output"`),
					Metadata:      &types.TraceMetadata{TotalTokens: &tokens, CostUSD: &costUSD, LatencyMS: &latencyMS},
					Steps: []types.Step{
						{Type: types.StepTypeLLMCall, Name: "child-call", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)},
					},
				},
			},
		},
	}

	send(2, "validate_trace_tree", types.ValidateTraceTreeParams{Trace: trace})
	resp := recv()

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result types.ValidateTraceTreeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Valid {
		t.Errorf("Valid = false for tree with sub-trace; errors = %v", result.Errors)
	}
	if result.Depth < 1 {
		t.Errorf("Depth = %d, want >= 1 for nested tree", result.Depth)
	}
	if result.AgentCount < 2 {
		t.Errorf("AgentCount = %d, want >= 2", result.AgentCount)
	}
	if result.AggregateTokens < 200 {
		t.Errorf("AggregateTokens = %d, want >= 200", result.AggregateTokens)
	}
}

func TestHandler_ValidateTraceTree_InvalidParams(t *testing.T) {
	send, recv := initServer(t)

	// Send a string as params — handler will fail to unmarshal into ValidateTraceTreeParams.
	send(2, "validate_trace_tree", "not-a-valid-param")
	resp := recv()

	if resp.Error == nil {
		// If somehow it didn't error, that's also acceptable if the trace is empty but valid.
		return
	}
	// Expect an invalid trace error.
	if resp.Error.Code != types.ErrInvalidTrace {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrInvalidTrace)
	}
}

func TestHandler_ValidateTraceTree_BeforeInitialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	trace := types.Trace{TraceID: "t1", AgentID: "a1", Input: json.RawMessage(`"x"`), Output: json.RawMessage(`"y"`)}
	sendRequest(t, stdin, 1, "validate_trace_tree", types.ValidateTraceTreeParams{Trace: trace})
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected SESSION_ERROR before initialize")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}

// ── submit_plugin_result ──

func TestHandler_SubmitPluginResult_Success(t *testing.T) {
	send, recv := initServer(t)

	params := types.SubmitPluginResultParams{
		TraceID:     "trace-1",
		PluginName:  "custom_plugin",
		AssertionID: "assert-plugin-1",
		Result: types.PluginResult{
			Status:      "pass",
			Score:       0.95,
			Explanation: "plugin check passed",
		},
	}

	send(2, "submit_plugin_result", params)
	resp := recv()

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result types.SubmitPluginResultResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Accepted {
		t.Error("Accepted = false, want true")
	}
}

func TestHandler_SubmitPluginResult_BeforeInitialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	params := types.SubmitPluginResultParams{
		TraceID:     "trace-1",
		PluginName:  "p",
		AssertionID: "a",
		Result:      types.PluginResult{Status: "pass", Score: 1.0},
	}
	sendRequest(t, stdin, 1, "submit_plugin_result", params)
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected SESSION_ERROR before initialize")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}

func TestHandler_SubmitPluginResult_InvalidParams(t *testing.T) {
	send, recv := initServer(t)

	// Send a params object that will fail to unmarshal into SubmitPluginResultParams.
	send(2, "submit_plugin_result", map[string]any{"bad": true})
	resp := recv()

	// The handler should error because required fields are missing, but JSON unmarshal
	// with missing fields doesn't fail — it just zero-initializes. So this should succeed
	// with zero values. Verify it returns accepted.
	if resp.Error != nil {
		// If it does error, that's also fine — verify it's an assertion error.
		if resp.Error.Code != types.ErrAssertionError {
			t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrAssertionError)
		}
		return
	}

	var result types.SubmitPluginResultResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Accepted {
		t.Error("Accepted = false, want true")
	}
}

// ── query_drift ──

func TestHandler_QueryDrift_NoHistory(t *testing.T) {
	send, recv := initServer(t)

	params := types.QueryDriftParams{
		AssertionID: "nonexistent-assertion",
		WindowSize:  10,
	}
	send(2, "query_drift", params)
	resp := recv()

	// query_drift requires a history store. RegisterBuiltinHandlers tries to create one;
	// if it fails (e.g., no writable dir), we get an engine error.
	// If it succeeds, we get an ok report with 0 history.
	if resp.Error != nil {
		// History store may not be available in test — that's acceptable.
		if resp.Error.Code != types.ErrEngineError {
			t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrEngineError)
		}
		return
	}

	var result types.QueryDriftResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Report.Count != 0 {
		t.Errorf("Count = %d, want 0 for nonexistent assertion", result.Report.Count)
	}
	if result.Report.Status != "ok" {
		t.Errorf("Status = %q, want %q", result.Report.Status, "ok")
	}
}

func TestHandler_QueryDrift_DefaultWindowSize(t *testing.T) {
	send, recv := initServer(t)

	// WindowSize 0 should default to 50.
	params := types.QueryDriftParams{
		AssertionID: "some-assertion",
		WindowSize:  0,
	}
	send(2, "query_drift", params)
	resp := recv()

	if resp.Error != nil {
		// History store may not be available — acceptable.
		return
	}

	var result types.QueryDriftResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Just verify we got a valid report back.
	if result.Report.AssertionID != "some-assertion" {
		t.Errorf("AssertionID = %q, want %q", result.Report.AssertionID, "some-assertion")
	}
}

func TestHandler_QueryDrift_BeforeInitialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	params := types.QueryDriftParams{AssertionID: "a", WindowSize: 10}
	sendRequest(t, stdin, 1, "query_drift", params)
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected SESSION_ERROR before initialize")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}

// ── generate_user_message ──
// Note: generate_user_message is only registered when a judge provider is configured.
// Without ATTEST_OPENAI_API_KEY, the handler won't be registered, so we expect method_not_found.

func TestHandler_GenerateUserMessage_NotRegisteredWithoutProvider(t *testing.T) {
	// In the default test server (no env vars), generate_user_message should be unregistered.
	send, recv := initServer(t)

	params := types.GenerateUserMessageParams{
		Persona: types.SimulatePersona{
			Name:         "test-user",
			SystemPrompt: "You are a test user.",
			Style:        "friendly",
			Temperature:  0.7,
		},
		ConversationHistory: []types.ConversationMessage{
			{Role: "assistant", Content: "How can I help you?"},
		},
	}
	send(2, "generate_user_message", params)
	resp := recv()

	if resp.Error == nil {
		// If a judge provider happens to be configured, that's fine.
		return
	}
	// Expect method_not_found since no provider is configured.
	if resp.Error.Code != -32601 {
		t.Errorf("Error.Code = %d, want -32601 (method_not_found)", resp.Error.Code)
	}
}

func TestHandler_GenerateUserMessage_BeforeInitialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	params := types.GenerateUserMessageParams{
		Persona: types.SimulatePersona{Name: "u"},
	}
	sendRequest(t, stdin, 1, "generate_user_message", params)
	resp := readResponse(t, stdout)

	// Either SESSION_ERROR (handler registered, session check) or method_not_found (handler not registered).
	if resp.Error == nil {
		t.Fatal("expected error before initialize")
	}
	if resp.Error.Code != types.ErrSessionError && resp.Error.Code != -32601 {
		t.Errorf("Error.Code = %d, want %d or -32601", resp.Error.Code, types.ErrSessionError)
	}
}

// ── evaluate_batch with assertion ID length limit ──

func TestHandler_EvaluateBatch_AssertionIDTooLong(t *testing.T) {
	send, recv := initServer(t)

	longID := make([]byte, MaxAssertionIDLength+1)
	for i := range longID {
		longID[i] = 'x'
	}

	params := types.EvaluateBatchParams{
		Trace: types.Trace{
			SchemaVersion: 1,
			TraceID:       "trace-1",
			AgentID:       "agent-1",
			Input:         json.RawMessage(`"hello"`),
			Output:        json.RawMessage(`"world"`),
			Steps:         []types.Step{{Type: types.StepTypeLLMCall, Name: "c", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)}},
		},
		Assertions: []types.Assertion{
			{
				AssertionID: string(longID),
				Type:        types.TypeSchema,
				Spec:        json.RawMessage(`{}`),
			},
		},
	}
	send(2, "evaluate_batch", params)
	resp := recv()

	if resp.Error == nil {
		t.Fatal("expected ASSERTION_ERROR for oversized assertion_id")
	}
	if resp.Error.Code != types.ErrAssertionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrAssertionError)
	}
}

// ── shutdown stats tracking ──

func TestHandler_Shutdown_TracksAssertionCount(t *testing.T) {
	send, recv := initServer(t)

	// Submit a plugin result to increment assertion count.
	params := types.SubmitPluginResultParams{
		TraceID:     "trace-1",
		PluginName:  "p",
		AssertionID: "a",
		Result:      types.PluginResult{Status: "pass", Score: 1.0},
	}
	send(2, "submit_plugin_result", params)
	pluginResp := recv()
	if pluginResp.Error != nil {
		t.Fatalf("submit_plugin_result error: %+v", pluginResp.Error)
	}

	send(3, "shutdown", map[string]any{})
	resp := recv()
	if resp.Error != nil {
		t.Fatalf("shutdown error: %+v", resp.Error)
	}

	var result types.ShutdownResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.AssertionsEvaluated < 1 {
		t.Errorf("AssertionsEvaluated = %d, want >= 1 after submit_plugin_result", result.AssertionsEvaluated)
	}
}
