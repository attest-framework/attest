package assertion

import (
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestSchemaEvaluator(t *testing.T) {
	evaluator := &SchemaEvaluator{}

	makeTrace := func(output json.RawMessage, steps []types.Step) *types.Trace {
		return &types.Trace{
			TraceID: "trc_test",
			Output:  output,
			Steps:   steps,
		}
	}

	makeAssertion := func(spec string) *types.Assertion {
		return &types.Assertion{
			AssertionID: "assert_test",
			Type:        types.TypeSchema,
			Spec:        json.RawMessage(spec),
		}
	}

	tests := []struct {
		name          string
		trace         *types.Trace
		assertionSpec string
		wantStatus    string
	}{
		{
			name: "valid output.structured matches schema",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok","structured":{"refund_id":"RFD-001","confidence":0.95}}`),
				nil,
			),
			assertionSpec: `{
				"target": "output.structured",
				"schema": {
					"type": "object",
					"required": ["refund_id", "confidence"],
					"properties": {
						"refund_id": {"type": "string"},
						"confidence": {"type": "number", "minimum": 0.0, "maximum": 1.0}
					}
				}
			}`,
			wantStatus: types.StatusPass,
		},
		{
			name: "missing required field",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok","structured":{"confidence":0.95}}`),
				nil,
			),
			assertionSpec: `{
				"target": "output.structured",
				"schema": {
					"type": "object",
					"required": ["refund_id", "confidence"],
					"properties": {
						"refund_id": {"type": "string"},
						"confidence": {"type": "number"}
					}
				}
			}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name: "wrong type for field",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok","structured":{"refund_id":123,"confidence":0.95}}`),
				nil,
			),
			assertionSpec: `{
				"target": "output.structured",
				"schema": {
					"type": "object",
					"required": ["refund_id", "confidence"],
					"properties": {
						"refund_id": {"type": "string"},
						"confidence": {"type": "number"}
					}
				}
			}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name: "steps target resolution passes",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok"}`),
				[]types.Step{
					{
						Name:   "lookup_order",
						Type:   types.StepTypeToolCall,
						Args:   json.RawMessage(`{"order_id":"ORD-123"}`),
						Result: json.RawMessage(`{"status":"delivered","amount":89.99}`),
					},
				},
			),
			assertionSpec: `{
				"target": "steps[?name=='lookup_order'].result",
				"schema": {
					"type": "object",
					"required": ["status", "amount"],
					"properties": {
						"status": {"type": "string"},
						"amount": {"type": "number", "minimum": 0}
					}
				}
			}`,
			wantStatus: types.StatusPass,
		},
		{
			name: "invalid target",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok"}`),
				nil,
			),
			assertionSpec: `{
				"target": "nonexistent.path.xyz",
				"schema": {"type": "object"}
			}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name: "invalid schema",
			trace: makeTrace(
				json.RawMessage(`{"message":"ok","structured":{"key":"val"}}`),
				nil,
			),
			assertionSpec: `{
				"target": "output.structured",
				"schema": {"type": "not-a-valid-type"}
			}`,
			wantStatus: types.StatusHardFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertion := makeAssertion(tt.assertionSpec)
			result := evaluator.Evaluate(tt.trace, assertion)
			if result.Status != tt.wantStatus {
				t.Errorf("got status %q, want %q; explanation: %s", result.Status, tt.wantStatus, result.Explanation)
			}
		})
	}
}
