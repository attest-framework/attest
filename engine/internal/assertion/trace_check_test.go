package assertion

import (
	"encoding/json"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func TestTraceEvaluator(t *testing.T) {
	evaluator := &TraceEvaluator{}

	makeSteps := func(names ...string) []types.Step {
		steps := make([]types.Step, len(names))
		for i, name := range names {
			steps[i] = types.Step{
				Name:   name,
				Type:   types.StepTypeToolCall,
				Result: json.RawMessage(`{}`),
			}
		}
		return steps
	}

	makeTrace := func(steps []types.Step) *types.Trace {
		return &types.Trace{
			TraceID: "trc_test",
			Output:  json.RawMessage(`{"message":"ok"}`),
			Steps:   steps,
		}
	}

	makeAssertion := func(spec string) *types.Assertion {
		return &types.Assertion{
			AssertionID: "assert_test",
			Type:        types.TypeTrace,
			Spec:        json.RawMessage(spec),
		}
	}

	tests := []struct {
		name       string
		steps      []types.Step
		spec       string
		wantStatus string
	}{
		// contains_in_order
		{
			name:       "contains_in_order passes",
			steps:      makeSteps("lookup_order", "reasoning", "process_refund"),
			spec:       `{"check":"contains_in_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "contains_in_order passes non-contiguous",
			steps:      makeSteps("auth", "lookup_order", "log", "process_refund"),
			spec:       `{"check":"contains_in_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "contains_in_order fails wrong order",
			steps:      makeSteps("process_refund", "lookup_order"),
			spec:       `{"check":"contains_in_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "contains_in_order fails missing tool",
			steps:      makeSteps("lookup_order"),
			spec:       `{"check":"contains_in_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusHardFail,
		},

		// exact_order
		{
			name:       "exact_order passes contiguous",
			steps:      makeSteps("lookup_order", "process_refund"),
			spec:       `{"check":"exact_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "exact_order passes within larger trace",
			steps:      makeSteps("auth", "lookup_order", "process_refund", "log"),
			spec:       `{"check":"exact_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "exact_order fails with step in between",
			steps:      makeSteps("lookup_order", "reasoning", "process_refund"),
			spec:       `{"check":"exact_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "exact_order fails wrong order",
			steps:      makeSteps("process_refund", "lookup_order"),
			spec:       `{"check":"exact_order","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusHardFail,
		},

		// loop_detection
		{
			name:       "loop_detection passes within limit",
			steps:      makeSteps("lookup_order", "lookup_order"),
			spec:       `{"check":"loop_detection","tool":"lookup_order","max_repetitions":2}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "loop_detection fails exceeds limit",
			steps:      makeSteps("lookup_order", "lookup_order", "lookup_order"),
			spec:       `{"check":"loop_detection","tool":"lookup_order","max_repetitions":2}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "loop_detection passes tool not in trace",
			steps:      makeSteps("other_tool"),
			spec:       `{"check":"loop_detection","tool":"lookup_order","max_repetitions":1}`,
			wantStatus: types.StatusPass,
		},

		// no_duplicates
		{
			name:       "no_duplicates passes unique names",
			steps:      makeSteps("step_a", "step_b", "step_c"),
			spec:       `{"check":"no_duplicates"}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "no_duplicates fails duplicate names",
			steps:      makeSteps("step_a", "step_b", "step_a"),
			spec:       `{"check":"no_duplicates"}`,
			wantStatus: types.StatusHardFail,
		},

		// required_tools
		{
			name:       "required_tools passes all present",
			steps:      makeSteps("lookup_order", "process_refund", "notify"),
			spec:       `{"check":"required_tools","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "required_tools fails missing tool",
			steps:      makeSteps("lookup_order"),
			spec:       `{"check":"required_tools","tools":["lookup_order","process_refund"]}`,
			wantStatus: types.StatusHardFail,
		},

		// forbidden_tools
		{
			name:       "forbidden_tools passes none present",
			steps:      makeSteps("lookup_order", "process_refund"),
			spec:       `{"check":"forbidden_tools","tools":["delete_account","wipe_data"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "forbidden_tools fails tool present",
			steps:      makeSteps("lookup_order", "delete_account"),
			spec:       `{"check":"forbidden_tools","tools":["delete_account","wipe_data"]}`,
			wantStatus: types.StatusHardFail,
		},

		// soft flag
		{
			name:       "soft flag returns soft_fail",
			steps:      makeSteps("lookup_order"),
			spec:       `{"check":"required_tools","tools":["lookup_order","process_refund"],"soft":true}`,
			wantStatus: types.StatusSoftFail,
		},

		// unknown check
		{
			name:       "unknown check fails",
			steps:      makeSteps("step_a"),
			spec:       `{"check":"unknown_check_type"}`,
			wantStatus: types.StatusHardFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := makeTrace(tt.steps)
			assertion := makeAssertion(tt.spec)
			result := evaluator.Evaluate(trace, assertion)
			if result.Status != tt.wantStatus {
				t.Errorf("got status %q, want %q; explanation: %s", result.Status, tt.wantStatus, result.Explanation)
			}
		})
	}
}
