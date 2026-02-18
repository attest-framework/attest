package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func loadFixture(t *testing.T, name string) *types.Trace {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "traces", name))
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", name, err)
	}
	var tr types.Trace
	if err := json.Unmarshal(data, &tr); err != nil {
		t.Fatalf("failed to unmarshal fixture %s: %v", name, err)
	}
	return &tr
}

func makeOutput(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}
	return raw
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		trace       func(t *testing.T) *types.Trace
		wantErrCode int
	}{
		{
			name: "valid trace passes",
			trace: func(t *testing.T) *types.Trace {
				return loadFixture(t, "valid.json")
			},
			wantErrCode: 0,
		},
		{
			name: "missing trace_id",
			trace: func(t *testing.T) *types.Trace {
				return loadFixture(t, "missing_trace_id.json")
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "empty trace_id whitespace only after normalize",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.TraceID = "   "
				Normalize(tr)
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "missing output",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.Output = nil
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "too many steps (10001)",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.Steps = make([]types.Step, MaxStepsPerTrace+1)
				for i := range tr.Steps {
					tr.Steps[i] = types.Step{
						Type:   types.StepTypeToolCall,
						Name:   "step",
						Args:   json.RawMessage(`{}`),
						Result: json.RawMessage(`{}`),
					}
				}
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "invalid step type",
			trace: func(t *testing.T) *types.Trace {
				return loadFixture(t, "invalid_step_type.json")
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "empty step name",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.Steps = []types.Step{
					{
						Type:   types.StepTypeToolCall,
						Name:   "",
						Args:   json.RawMessage(`{}`),
						Result: json.RawMessage(`{}`),
					},
				}
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "deep nesting (depth 6)",
			trace: func(t *testing.T) *types.Trace {
				return loadFixture(t, "deep_nesting.json")
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "schema version too old (-1)",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.SchemaVersion = -1
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "schema version too new (99)",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.SchemaVersion = 99
				return tr
			},
			wantErrCode: types.ErrInvalidTrace,
		},
		{
			name: "valid schema version 0 (deprecated, passes)",
			trace: func(t *testing.T) *types.Trace {
				tr := loadFixture(t, "valid.json")
				tr.SchemaVersion = 0
				return tr
			},
			wantErrCode: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.trace(t)
			err := Validate(tr)
			if tc.wantErrCode == 0 {
				if err != nil {
					t.Errorf("expected no error, got code=%d message=%q", err.Code, err.Message)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error code %d, got nil", tc.wantErrCode)
				return
			}
			if err.Code != tc.wantErrCode {
				t.Errorf("expected error code %d, got %d (message: %q)", tc.wantErrCode, err.Code, err.Message)
			}
			if err.Data == nil {
				t.Errorf("expected error data to be non-nil")
				return
			}
			if err.Data.ErrorType != types.ErrTypeInvalidTrace {
				t.Errorf("expected error type %q, got %q", types.ErrTypeInvalidTrace, err.Data.ErrorType)
			}
			if err.Data.Retryable {
				t.Errorf("expected retryable=false for INVALID_TRACE errors")
			}
			if err.Data.Detail == "" {
				t.Errorf("expected non-empty detail in error data")
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Run("trims whitespace from TraceID", func(t *testing.T) {
		tr := &types.Trace{TraceID: "  trc_123  ", SchemaVersion: 1}
		Normalize(tr)
		if tr.TraceID != "trc_123" {
			t.Errorf("expected TraceID %q, got %q", "trc_123", tr.TraceID)
		}
	})

	t.Run("defaults SchemaVersion 0 to 1", func(t *testing.T) {
		tr := &types.Trace{TraceID: "trc_123", SchemaVersion: 0}
		Normalize(tr)
		if tr.SchemaVersion != 1 {
			t.Errorf("expected SchemaVersion 1, got %d", tr.SchemaVersion)
		}
	})

	t.Run("does not change non-zero SchemaVersion", func(t *testing.T) {
		tr := &types.Trace{TraceID: "trc_123", SchemaVersion: 1}
		Normalize(tr)
		if tr.SchemaVersion != 1 {
			t.Errorf("expected SchemaVersion 1, got %d", tr.SchemaVersion)
		}
	})
}

func TestModel(t *testing.T) {
	tr := loadFixture(t, "valid.json")

	t.Run("StepCount", func(t *testing.T) {
		if got := StepCount(tr); got != 3 {
			t.Errorf("expected 3 steps, got %d", got)
		}
	})

	t.Run("ToolCallCount", func(t *testing.T) {
		if got := ToolCallCount(tr); got != 1 {
			t.Errorf("expected 1 tool_call step, got %d", got)
		}
	})

	t.Run("StepsByType llm_call", func(t *testing.T) {
		steps := StepsByType(tr, types.StepTypeLLMCall)
		if len(steps) != 1 {
			t.Errorf("expected 1 llm_call step, got %d", len(steps))
		}
		if steps[0].Name != "reasoning" {
			t.Errorf("expected step name %q, got %q", "reasoning", steps[0].Name)
		}
	})

	t.Run("StepByName found", func(t *testing.T) {
		step := StepByName(tr, "lookup_info")
		if step == nil {
			t.Fatal("expected non-nil step for name 'lookup_info'")
		}
		if step.Name != "lookup_info" {
			t.Errorf("expected step name %q, got %q", "lookup_info", step.Name)
		}
	})

	t.Run("StepByName not found", func(t *testing.T) {
		step := StepByName(tr, "nonexistent")
		if step != nil {
			t.Errorf("expected nil for nonexistent step, got %+v", step)
		}
	})
}
