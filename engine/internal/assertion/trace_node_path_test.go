package assertion

import "testing"

// INC-1042 shape: the agent issues the same refund twice, with a lookup in
// between. The failing span is the second issue_refund, steps[3].
func TestToolCardinality_MaxViolation_NodePathNamesFirstCallOverLimit(t *testing.T) {
	refund := func(order string) contractCall {
		return contractCall{name: "issue_refund", args: `{"order_id":"` + order + `","amount":5}`}
	}
	trace := contractTrace(
		contractCall{name: "lookup_order"},
		refund("O-1"),
		contractCall{name: "lookup_order"},
		refund("O-1"),
		refund("O-1"),
	)
	cases := []struct {
		name string
		spec string
		want string
	}{
		{"per key", `{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`, "steps[3]"},
		{"without key", `{"check":"tool_cardinality","tool":"issue_refund","max":2}`, "steps[4]"},
		{"min violation keeps inferred path", `{"check":"tool_cardinality","tool":"issue_refund","min":9}`, ""},
		{"passing check has no node", `{"check":"tool_cardinality","tool":"issue_refund","max":5}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evalTraceSpec(t, trace, tc.spec)
			if got.TraceNodePath != tc.want {
				t.Fatalf("TraceNodePath = %q, want %q", got.TraceNodePath, tc.want)
			}
		})
	}
}

func TestToolCardinality_PerKey_NodePathUsesFirstViolatingKey(t *testing.T) {
	refund := func(order string) contractCall {
		return contractCall{name: "issue_refund", args: `{"order_id":"` + order + `"}`}
	}
	// O-2 is first seen before O-1, so O-2's second call (steps[2]) is the node.
	trace := contractTrace(refund("O-2"), refund("O-1"), refund("O-2"), refund("O-1"))
	got := evalTraceSpec(t, trace, `{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`)
	if got.TraceNodePath != "steps[2]" {
		t.Fatalf("TraceNodePath = %q, want steps[2]", got.TraceNodePath)
	}
}

func TestLoopDetection_Failure_KeepsInferredPath(t *testing.T) {
	trace := contractTrace(contractCall{name: "search"}, contractCall{name: "search"}, contractCall{name: "search"})

	got := evalTraceSpec(t, trace, `{"check":"loop_detection","tool":"search","max_repetitions":2}`)

	if got.Status != "hard_fail" || got.TraceNodePath != "" {
		t.Fatalf("status %q path %q; want hard_fail with the path left to inference", got.Status, got.TraceNodePath)
	}
}
