package capture

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/trace"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func incidentTrace(t *testing.T) *types.Trace {
	t.Helper()
	data, err := os.ReadFile("../../../protocol-tests/trace-v2/otlp/inc-1042-refund.otlp.json")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := trace.ConvertOTLP(data, trace.ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func contractIDs(cs []Contract) string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return strings.Join(ids, " ")
}

func cardinalitySpecs(cs []Contract) map[string]string {
	out := map[string]string{}
	for _, c := range cs {
		if c.Kind == KindCardinality {
			out[c.ID] = string(c.Spec)
		}
	}
	return out
}

func draftAndVerify(t *testing.T, tr *types.Trace, expectFail bool) ([]Contract, Verification) {
	t.Helper()
	cs, err := Draft(tr, expectFail)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(tr, cs, expectFail, TraceCheckResult{OK: true})
	if err != nil {
		t.Fatal(err)
	}
	return cs, v
}

// toolTrace is a trace whose top-level tool calls are the given (name, order_id)
// pairs, in order.
func toolTrace(calls ...[2]string) *types.Trace {
	tr := &types.Trace{SchemaVersion: 2, TraceID: "t", Output: json.RawMessage(`{"message":"ok"}`)}
	for _, c := range calls {
		tr.Steps = append(tr.Steps, types.Step{
			Type: types.StepTypeToolCall, Name: c[0],
			Args: json.RawMessage(`{"order_id":"` + c[1] + `"}`), Result: json.RawMessage(`{}`),
		})
	}
	return tr
}

func TestDraft_Incident_ToolsOrderAndCardinalityIDs(t *testing.T) {
	cs, err := Draft(incidentTrace(t), true)
	if err != nil {
		t.Fatal(err)
	}

	want := "tools-called order-lookup_order-before-issue_refund order-issue_refund-before-notify_customer " +
		"cardinality-lookup_order-per-order_id cardinality-issue_refund-per-order_id cardinality-notify_customer-per-order_id"
	if got := contractIDs(cs); got != want {
		t.Fatalf("contracts\n got %s\nwant %s", got, want)
	}
}

// (a) A healthy run that reads the same id twice drafts the observed maximum
// and verifies.
func TestDraft_HealthyRunRepeatedRead_ObservedMaxAndVerified(t *testing.T) {
	tr := toolTrace([2]string{"lookup_order", "O-1"}, [2]string{"issue_refund", "O-1"},
		[2]string{"lookup_order", "O-1"}, [2]string{"notify_customer", "O-1"})

	cs, v := draftAndVerify(t, tr, false)

	got := cardinalitySpecs(cs)["cardinality-lookup_order-per-order_id"]
	if got != `{"check":"tool_cardinality","max":2,"per":"order_id","soft":false,"tool":"lookup_order"}` {
		t.Fatalf("lookup_order spec %s", got)
	}
	if !v.Verified || len(v.ExpectedFailures) != 0 {
		t.Fatalf("verification %+v", v)
	}
}

// (b) --expect-fail on the INC-1042 shape allows one call per id: only the
// repeated issue_refund fails, and it is the one expected failure.
func TestDraft_ExpectFailIncidentShape_OnlyIssueRefundExpectedFailure(t *testing.T) {
	tr := toolTrace([2]string{"lookup_order", "O-1042"}, [2]string{"issue_refund", "O-1042"},
		[2]string{"issue_refund", "O-1042"}, [2]string{"notify_customer", "O-1042"})

	cs, v := draftAndVerify(t, tr, true)

	for id, spec := range cardinalitySpecs(cs) {
		if !strings.Contains(spec, `"max":1,`) {
			t.Errorf("%s: %s; want max 1", id, spec)
		}
	}
	if !v.Verified || strings.Join(v.ExpectedFailures, ",") != "cardinality-issue_refund-per-order_id" {
		t.Fatalf("verification %+v", v)
	}
}

// (c) A normal capture of the INC-1042 shape does not know it is an incident:
// issue_refund is drafted at its observed maximum of 2 and the run verifies.
func TestDraft_NormalCaptureIncidentShape_IssueRefundMax2Verified(t *testing.T) {
	tr := toolTrace([2]string{"lookup_order", "O-1042"}, [2]string{"issue_refund", "O-1042"},
		[2]string{"issue_refund", "O-1042"}, [2]string{"notify_customer", "O-1042"})

	cs, v := draftAndVerify(t, tr, false)

	got := cardinalitySpecs(cs)["cardinality-issue_refund-per-order_id"]
	if got != `{"check":"tool_cardinality","max":2,"per":"order_id","soft":false,"tool":"issue_refund"}` {
		t.Fatalf("issue_refund spec %s", got)
	}
	if !v.Verified {
		t.Fatalf("verification %+v", v)
	}
}

func TestDraft_ObservedMaxIsPerValue(t *testing.T) {
	tr := toolTrace([2]string{"lookup_order", "O-1"}, [2]string{"lookup_order", "O-2"},
		[2]string{"lookup_order", "O-2"}, [2]string{"lookup_order", "O-3"})

	cs, _ := draftAndVerify(t, tr, false)

	if got := cardinalitySpecs(cs)["cardinality-lookup_order-per-order_id"]; !strings.Contains(got, `"max":2,`) {
		t.Fatalf("spec %s; want max 2 (O-2 twice), not 4 calls", got)
	}
}

func TestVerify_IncidentTrace_ExpectFailRecordsOnlyIssueRefund_TraceCheckGates(t *testing.T) {
	tr := incidentTrace(t)
	cs, err := Draft(tr, true)
	if err != nil {
		t.Fatal(err)
	}

	incident, err := Verify(tr, cs, true, TraceCheckResult{OK: true})
	if err != nil {
		t.Fatal(err)
	}
	badTrace, err := Verify(tr, cs, true, TraceCheckResult{OK: false, Errors: 1})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Verify(tr, cs, false, TraceCheckResult{OK: true})
	if err != nil {
		t.Fatal(err)
	}

	if !incident.Verified || strings.Join(incident.ExpectedFailures, ",") != "cardinality-issue_refund-per-order_id" {
		t.Errorf("incident = %+v", incident)
	}
	if badTrace.Verified {
		t.Errorf("verified a trace that failed trace check")
	}
	if plain.Verified || len(plain.ExpectedFailures) != 0 {
		t.Errorf("incident-mode contracts verified without --expect-fail: %+v", plain)
	}
}

func TestDraft_StructuredOutputAndCost_DraftedAndPass(t *testing.T) {
	cost := 0.0123
	tr := &types.Trace{
		SchemaVersion: 2, TraceID: "t",
		Output:   json.RawMessage(`{"message":"ok","structured":{"refund_id":"R-1","amount_cents":8400,"ratio":0.5,"approved":true,"tags":[],"meta":{},"note":null}}`),
		Steps:    []types.Step{{Type: types.StepTypeLLMCall, Name: "chat", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)}},
		Metadata: &types.TraceMetadata{CostUSD: &cost},
	}

	cs, err := Draft(tr, false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(tr, cs, false, TraceCheckResult{OK: true})
	if err != nil {
		t.Fatal(err)
	}

	if got := contractIDs(cs); got != "output-schema cost-ceiling" {
		t.Fatalf("contracts %s", got)
	}
	wantSchema := `{"schema":{"properties":{"amount_cents":{"type":"integer"},"approved":{"type":"boolean"},"meta":{"type":"object"},"note":{"type":"null"},"ratio":{"type":"number"},"refund_id":{"type":"string"},"tags":{"type":"array"}},"required":["amount_cents","approved","meta","note","ratio","refund_id","tags"],"type":"object"},"target":"output.structured"}`
	if string(cs[0].Spec) != wantSchema {
		t.Errorf("schema spec\n got %s\nwant %s", cs[0].Spec, wantSchema)
	}
	if string(cs[1].Spec) != `{"field":"metadata.cost_usd","operator":"lte","soft":false,"value":0.01845}` {
		t.Errorf("cost spec %s", cs[1].Spec)
	}
	if !v.Verified {
		t.Errorf("not verified: %+v", v.Results)
	}
}

func TestVerify_UnknownContractType_Errors(t *testing.T) {
	_, err := Verify(&types.Trace{}, []Contract{{ID: "x", Type: "llm_judge", Spec: json.RawMessage(`{}`)}}, false, TraceCheckResult{OK: true})

	if err == nil {
		t.Fatal("want error")
	}
}

func TestCheckFixture_Invariants(t *testing.T) {
	for name, doc := range map[string]string{
		"not json":           `{`,
		"unknown field":      `{"fixture_version":1,"trace":{},"bogus":1}`,
		"wrong version":      `{"fixture_version":2}`,
		"no trace":           `{"fixture_version":1}`,
		"bad index":          `{"fixture_version":1,"trace":{"trace_id":"t","output":{},"steps":[]},"exchanges":[{"index":3}]}`,
		"duplicate contract": `{"fixture_version":1,"trace":{"trace_id":"t","output":{},"steps":[]},"contracts":[{"id":"a"},{"id":"a"}]}`,
		"unknown result":     `{"fixture_version":1,"trace":{"trace_id":"t","output":{},"steps":[]},"verification":{"results":[{"contract":"a"}]}}`,
		"unknown expected":   `{"fixture_version":1,"trace":{"trace_id":"t","output":{},"steps":[]},"verification":{"expected_failures":["a"]}}`,
		"invalid trace":      `{"fixture_version":1,"trace":{"trace_id":"","output":{},"steps":[]},"verification":{"verified":true,"trace_check":{"ok":true}}}`,
		"capture check":      `{"fixture_version":1,"trace":{"trace_id":"t","output":{},"steps":[]},"verification":{"verified":true,"trace_check":{"ok":false,"errors":1}}}`,
	} {
		if r := CheckFixture([]byte(doc)); r.OK {
			t.Errorf("%s: want errors, got OK", name)
		}
	}
	unverified := CheckFixture([]byte(`{"fixture_version":1,"trace":{"trace_id":"t","output":{"message":"ok"},"steps":[]},"verification":{"trace_check":{"ok":true}}}`))
	if !unverified.OK || len(unverified.Issues) != 1 || unverified.Issues[0].Code != IssueUnverified {
		t.Errorf("unverified = %+v", unverified)
	}
}

func TestIsFixture(t *testing.T) {
	if !IsFixture([]byte(`{"fixture_version":1}`)) || IsFixture([]byte(`{"resourceSpans":[]}`)) || IsFixture([]byte(`[`)) {
		t.Fatal("IsFixture misclassified")
	}
}

func TestVerify_NoModelOrToolStep_NeverVerified(t *testing.T) {
	for _, expectFail := range []bool{false, true} {
		tr := &types.Trace{
			SchemaVersion: 2, TraceID: "t",
			Output: json.RawMessage(`{"message":"ok"}`),
			Steps:  []types.Step{{Type: types.StepTypeAgentCall, Name: "agent", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)}},
		}

		v, err := Verify(tr, nil, expectFail, TraceCheckResult{OK: true})

		if err != nil {
			t.Fatal(err)
		}
		if v.Verified {
			t.Errorf("expectFail=%v: trace with only an agent_call step verified", expectFail)
		}
	}
}

func TestVerify_NoContractsDrafted_NeverVerified(t *testing.T) {
	for _, expectFail := range []bool{false, true} {
		tr := &types.Trace{
			SchemaVersion: 2, TraceID: "t",
			Steps: []types.Step{{Type: types.StepTypeLLMCall, Name: "chat", Args: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)}},
		}

		v, err := Verify(tr, nil, expectFail, TraceCheckResult{OK: true})

		if err != nil {
			t.Fatal(err)
		}
		if v.Verified {
			t.Errorf("expectFail=%v: zero contracts verified", expectFail)
		}
	}
}
