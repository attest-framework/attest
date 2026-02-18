package types_test

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestTrace_JSON_RoundTrip(t *testing.T) {
	tokens := 100
	costUSD := 0.002
	latencyMS := 350
	model := "gpt-4.1"
	ts := "2026-02-18T00:00:00Z"
	parentID := "parent-trace-001"

	original := types.Trace{
		SchemaVersion: 1,
		TraceID:       "trace-001",
		AgentID:       "agent-001",
		Input:         json.RawMessage(`{"prompt":"hello"}`),
		Steps: []types.Step{
			{
				Type:     types.StepTypeLLMCall,
				Name:     "generate",
				Args:     json.RawMessage(`{"model":"gpt-4.1"}`),
				Result:   json.RawMessage(`{"text":"world"}`),
				Metadata: json.RawMessage(`{"tokens":100}`),
			},
		},
		Output: json.RawMessage(`{"response":"world"}`),
		Metadata: &types.TraceMetadata{
			TotalTokens: &tokens,
			CostUSD:     &costUSD,
			LatencyMS:   &latencyMS,
			Model:       &model,
			Timestamp:   &ts,
		},
		ParentTraceID: &parentID,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored types.Trace
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.TraceID != original.TraceID {
		t.Errorf("TraceID: got %q, want %q", restored.TraceID, original.TraceID)
	}
	if restored.AgentID != original.AgentID {
		t.Errorf("AgentID: got %q, want %q", restored.AgentID, original.AgentID)
	}
	if restored.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", restored.SchemaVersion, original.SchemaVersion)
	}
	if len(restored.Steps) != len(original.Steps) {
		t.Fatalf("Steps length: got %d, want %d", len(restored.Steps), len(original.Steps))
	}
	if restored.Steps[0].Type != types.StepTypeLLMCall {
		t.Errorf("Step[0].Type: got %q, want %q", restored.Steps[0].Type, types.StepTypeLLMCall)
	}
	if restored.Metadata == nil {
		t.Fatal("Metadata is nil after round-trip")
	}
	if *restored.Metadata.TotalTokens != tokens {
		t.Errorf("Metadata.TotalTokens: got %d, want %d", *restored.Metadata.TotalTokens, tokens)
	}
	if restored.ParentTraceID == nil || *restored.ParentTraceID != parentID {
		t.Errorf("ParentTraceID: got %v, want %q", restored.ParentTraceID, parentID)
	}
}

func TestAssertionResult_JSON_RoundTrip(t *testing.T) {
	original := types.AssertionResult{
		AssertionID: "assert-001",
		Status:      types.StatusPass,
		Score:       0.95,
		Explanation: "all constraints satisfied",
		Cost:        0.001,
		DurationMS:  42,
		RequestID:   "req-001",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored types.AssertionResult
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.AssertionID != original.AssertionID {
		t.Errorf("AssertionID: got %q, want %q", restored.AssertionID, original.AssertionID)
	}
	if restored.Status != types.StatusPass {
		t.Errorf("Status: got %q, want %q", restored.Status, types.StatusPass)
	}
	if restored.Score != original.Score {
		t.Errorf("Score: got %f, want %f", restored.Score, original.Score)
	}
	if restored.DurationMS != original.DurationMS {
		t.Errorf("DurationMS: got %d, want %d", restored.DurationMS, original.DurationMS)
	}
}

func TestRequest_JSON_RoundTrip(t *testing.T) {
	original := types.Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "evaluate_batch",
		Params:  json.RawMessage(`{"trace":{},"assertions":[]}`),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored types.Request
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.JSONRPC != "2.0" {
		t.Errorf("JSONRPC: got %q, want %q", restored.JSONRPC, "2.0")
	}
	if restored.ID != original.ID {
		t.Errorf("ID: got %d, want %d", restored.ID, original.ID)
	}
	if restored.Method != original.Method {
		t.Errorf("Method: got %q, want %q", restored.Method, original.Method)
	}
}

func TestResponse_WithError(t *testing.T) {
	rpcErr := types.NewRPCError(
		types.ErrInvalidTrace,
		"invalid trace",
		types.ErrTypeInvalidTrace,
		false,
		"trace_id is empty",
	)
	resp := types.NewErrorResponse(42, rpcErr)

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored types.Response
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.JSONRPC != "2.0" {
		t.Errorf("JSONRPC: got %q, want %q", restored.JSONRPC, "2.0")
	}
	if restored.ID != 42 {
		t.Errorf("ID: got %d, want 42", restored.ID)
	}
	if restored.Error == nil {
		t.Fatal("Error is nil after round-trip")
	}
	if restored.Error.Code != types.ErrInvalidTrace {
		t.Errorf("Error.Code: got %d, want %d", restored.Error.Code, types.ErrInvalidTrace)
	}
	if restored.Error.Data == nil {
		t.Fatal("Error.Data is nil")
	}
	if restored.Error.Data.ErrorType != types.ErrTypeInvalidTrace {
		t.Errorf("Error.Data.ErrorType: got %q, want %q", restored.Error.Data.ErrorType, types.ErrTypeInvalidTrace)
	}
	if restored.Error.Data.Retryable {
		t.Error("Error.Data.Retryable: got true, want false")
	}
	if len(restored.Result) != 0 {
		t.Errorf("Result should be empty for error response, got %s", restored.Result)
	}
}

func TestNewRPCError(t *testing.T) {
	err := types.NewRPCError(
		types.ErrProviderError,
		"provider unavailable",
		types.ErrTypeProviderError,
		true,
		"upstream timeout",
	)

	if err.Code != types.ErrProviderError {
		t.Errorf("Code: got %d, want %d", err.Code, types.ErrProviderError)
	}
	if err.Message != "provider unavailable" {
		t.Errorf("Message: got %q, want %q", err.Message, "provider unavailable")
	}
	if err.Data == nil {
		t.Fatal("Data is nil")
	}
	if err.Data.ErrorType != types.ErrTypeProviderError {
		t.Errorf("Data.ErrorType: got %q, want %q", err.Data.ErrorType, types.ErrTypeProviderError)
	}
	if !err.Data.Retryable {
		t.Error("Data.Retryable: got false, want true")
	}
	if err.Data.Detail != "upstream timeout" {
		t.Errorf("Data.Detail: got %q, want %q", err.Data.Detail, "upstream timeout")
	}
}
