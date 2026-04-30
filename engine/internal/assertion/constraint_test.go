package assertion

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestConstraintEvaluator(t *testing.T) {
	evaluator := &ConstraintEvaluator{}

	intPtr := func(v int) *int { return &v }
	float64Ptr := func(v float64) *float64 { return &v }

	makeTrace := func(meta *types.TraceMetadata, steps []types.Step) *types.Trace {
		return &types.Trace{
			TraceID:  "trc_test",
			Output:   json.RawMessage(`{"message":"ok"}`),
			Metadata: meta,
			Steps:    steps,
		}
	}

	makeAssertion := func(spec string) *types.Assertion {
		return &types.Assertion{
			AssertionID: "assert_test",
			Type:        types.TypeConstraint,
			Spec:        json.RawMessage(spec),
		}
	}

	tests := []struct {
		name       string
		trace      *types.Trace
		spec       string
		wantStatus string
	}{
		{
			name:       "lte operator passes",
			trace:      makeTrace(&types.TraceMetadata{CostUSD: float64Ptr(0.0067)}, nil),
			spec:       `{"field":"metadata.cost_usd","operator":"lte","value":0.01}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "lte operator fails",
			trace:      makeTrace(&types.TraceMetadata{CostUSD: float64Ptr(0.02)}, nil),
			spec:       `{"field":"metadata.cost_usd","operator":"lte","value":0.01}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "lt operator passes",
			trace:      makeTrace(&types.TraceMetadata{CostUSD: float64Ptr(0.005)}, nil),
			spec:       `{"field":"metadata.cost_usd","operator":"lt","value":0.01}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "lt operator fails on equal",
			trace:      makeTrace(&types.TraceMetadata{CostUSD: float64Ptr(0.01)}, nil),
			spec:       `{"field":"metadata.cost_usd","operator":"lt","value":0.01}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "gt operator passes",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(1350)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"gt","value":1000}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "gt operator fails",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(500)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"gt","value":1000}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "gte operator passes on equal",
			trace:      makeTrace(&types.TraceMetadata{LatencyMS: intPtr(4200)}, nil),
			spec:       `{"field":"metadata.latency_ms","operator":"gte","value":4200}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "eq operator passes",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(100)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"eq","value":100}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "eq operator fails",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(100)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"eq","value":200}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "between operator passes",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(1350)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"between","min":100,"max":2000}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "between operator fails below min",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(50)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"between","min":100,"max":2000}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "between operator fails above max",
			trace:      makeTrace(&types.TraceMetadata{TotalTokens: intPtr(3000)}, nil),
			spec:       `{"field":"metadata.total_tokens","operator":"between","min":100,"max":2000}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "soft flag returns soft_fail",
			trace:      makeTrace(&types.TraceMetadata{LatencyMS: intPtr(6000)}, nil),
			spec:       `{"field":"metadata.latency_ms","operator":"lte","value":5000,"soft":true}`,
			wantStatus: types.StatusSoftFail,
		},
		{
			name:       "missing metadata field fails",
			trace:      makeTrace(nil, nil),
			spec:       `{"field":"metadata.cost_usd","operator":"lte","value":0.01}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name: "steps.length passes",
			trace: makeTrace(nil, []types.Step{
				{Name: "step1", Type: types.StepTypeToolCall, Result: json.RawMessage(`{}`)},
				{Name: "step2", Type: types.StepTypeLLMCall, Result: json.RawMessage(`{}`)},
			}),
			spec:       `{"field":"steps.length","operator":"eq","value":2}`,
			wantStatus: types.StatusPass,
		},
		{
			name: "filtered step count passes",
			trace: makeTrace(nil, []types.Step{
				{Name: "step1", Type: types.StepTypeToolCall, Result: json.RawMessage(`{}`)},
				{Name: "step2", Type: types.StepTypeToolCall, Result: json.RawMessage(`{}`)},
				{Name: "step3", Type: types.StepTypeLLMCall, Result: json.RawMessage(`{}`)},
			}),
			spec:       `{"field":"steps[?type=='tool_call'].length","operator":"eq","value":2}`,
			wantStatus: types.StatusPass,
		},
		{
			name: "filtered step count fails",
			trace: makeTrace(nil, []types.Step{
				{Name: "step1", Type: types.StepTypeToolCall, Result: json.RawMessage(`{}`)},
				{Name: "step2", Type: types.StepTypeLLMCall, Result: json.RawMessage(`{}`)},
			}),
			spec:       `{"field":"steps[?type=='tool_call'].length","operator":"gt","value":5}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "unsupported field fails",
			trace:      makeTrace(nil, nil),
			spec:       `{"field":"nonexistent.field","operator":"eq","value":1}`,
			wantStatus: types.StatusHardFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertion := makeAssertion(tt.spec)
			result := evaluator.Evaluate(tt.trace, assertion)
			if result.Status != tt.wantStatus {
				t.Errorf("got status %q, want %q; explanation: %s", result.Status, tt.wantStatus, result.Explanation)
			}
		})
	}
}
