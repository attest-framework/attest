package capture

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
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

func TestDraft_Incident_ToolsOrderAndPerIDCardinality(t *testing.T) {
	cs, err := Draft(incidentTrace(t))
	if err != nil {
		t.Fatal(err)
	}

	want := "tools-called order-lookup_order-before-issue_refund order-issue_refund-before-notify_customer " +
		"cardinality-lookup_order-per-order_id cardinality-issue_refund-per-order_id cardinality-notify_customer-per-order_id"
	if got := contractIDs(cs); got != want {
		t.Fatalf("contracts\n got %s\nwant %s", got, want)
	}
	if got := string(cs[4].Spec); got != `{"check":"tool_cardinality","max":1,"per":"order_id","soft":false,"tool":"issue_refund"}` {
		t.Fatalf("cardinality spec %s", got)
	}
}

func TestVerify_Incident_FailsWithoutExpectFail_VerifiedWithIt(t *testing.T) {
	tr := incidentTrace(t)
	cs, err := Draft(tr)
	if err != nil {
		t.Fatal(err)
	}
	ok := TraceCheckResult{OK: true}

	plain, err := Verify(tr, cs, false, ok)
	if err != nil {
		t.Fatal(err)
	}
	incident, err := Verify(tr, cs, true, ok)
	if err != nil {
		t.Fatal(err)
	}
	badTrace, err := Verify(tr, cs, true, TraceCheckResult{OK: false, Errors: 1})
	if err != nil {
		t.Fatal(err)
	}

	if plain.Verified || len(plain.ExpectedFailures) != 0 {
		t.Errorf("plain = %+v", plain)
	}
	if !incident.Verified || strings.Join(incident.ExpectedFailures, ",") != "cardinality-issue_refund-per-order_id" {
		t.Errorf("incident = %+v", incident)
	}
	if badTrace.Verified {
		t.Errorf("verified a trace that failed trace check")
	}
}

func TestDraft_StructuredOutputAndCost_DraftedAndPass(t *testing.T) {
	cost := 0.0123
	tr := &types.Trace{
		SchemaVersion: 2, TraceID: "t",
		Output:   json.RawMessage(`{"message":"ok","structured":{"refund_id":"R-1","amount_cents":8400,"ratio":0.5,"approved":true,"tags":[],"meta":{},"note":null}}`),
		Steps:    []types.Step{},
		Metadata: &types.TraceMetadata{CostUSD: &cost},
	}

	cs, err := Draft(tr)
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
