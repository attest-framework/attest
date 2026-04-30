package assertion

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

func TestAnnotateDiagnostics_FillsLayerAndType(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())

	trace := &types.Trace{
		TraceID: "trc_diag_test",
		Output:  json.RawMessage(`{"message":"Hello world"}`),
	}

	assertions := []types.Assertion{
		{
			AssertionID: "schema_ok",
			Type:        types.TypeSchema,
			Spec: json.RawMessage(`{
				"target": "output",
				"schema": {"type": "object", "required": ["message"]}
			}`),
		},
		{
			AssertionID: "content_fail",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"goodbye"}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch returned error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}

	for _, r := range result.Results {
		if r.Type == "" {
			t.Errorf("assertion %s: Type not populated", r.AssertionID)
		}
		if r.Layer == 0 {
			t.Errorf("assertion %s: Layer not populated", r.AssertionID)
		}
		if r.TraceNodePath == "" {
			t.Errorf("assertion %s: TraceNodePath not populated", r.AssertionID)
		}
	}

	// Failure case must have an Actual + SuggestedAction.
	for _, r := range result.Results {
		if r.AssertionID == "content_fail" {
			if r.Actual == "" {
				t.Errorf("content_fail Actual should be populated, got empty")
			}
			if r.Expected == "" {
				t.Errorf("content_fail Expected should be populated, got empty")
			}
			if r.SuggestedAction == "" {
				t.Errorf("content_fail SuggestedAction should be populated, got empty")
			}
		}
	}
}

func TestAnnotateDiagnostics_LayerForType(t *testing.T) {
	cases := []struct {
		assertionType string
		wantLayer     int
	}{
		{types.TypeSchema, 1},
		{types.TypeConstraint, 2},
		{types.TypeTrace, 3},
		{types.TypeTraceTree, 3},
		{types.TypeContent, 4},
		{types.TypeEmbedding, 5},
		{types.TypeLLMJudge, 6},
		{types.TypePlugin, 8},
		{"unknown_type", 0},
	}

	for _, tc := range cases {
		got := types.LayerForType(tc.assertionType)
		if got != tc.wantLayer {
			t.Errorf("LayerForType(%q) = %d, want %d", tc.assertionType, got, tc.wantLayer)
		}
	}
}

func TestInferTraceNodePath(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string
	}{
		{"target wins", `{"target":"output.message"}`, "output.message"},
		{"field fallback", `{"field":"metadata.cost_usd","operator":"lt","value":0.5}`, "metadata.cost_usd"},
		{"check label fallback", `{"check":"loop_detection","tool":"search"}`, "trace.loop_detection"},
		{"empty spec", `{}`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &types.Assertion{Spec: json.RawMessage(tc.spec)}
			got := inferTraceNodePath(a)
			if got != tc.want {
				t.Errorf("inferTraceNodePath(%s) = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

func TestTruncate_LongString(t *testing.T) {
	long := make([]byte, maxDiagnosticBytes*2)
	for i := range long {
		long[i] = 'x'
	}
	got := truncate(string(long))
	if len(got) != maxDiagnosticBytes {
		t.Errorf("truncate length = %d, want %d", len(got), maxDiagnosticBytes)
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("truncate did not append ellipsis: %q", got[len(got)-3:])
	}
}

func TestTruncate_ShortString(t *testing.T) {
	got := truncate("short")
	if got != "short" {
		t.Errorf("truncate(short) = %q, want unchanged", got)
	}
}
