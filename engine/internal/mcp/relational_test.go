package mcp_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// relationalProject writes a fixture whose world holds order O-2077
// (12600 cents, customer C-0311), the given refunds and one notification to
// the given customer, and returns the project root.
func relationalProject(t *testing.T, refunds []map[string]any, notifiedCustomer string) string {
	t.Helper()
	root, _ := project(t)
	data, err := os.ReadFile(worldFixture)
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]any
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	state := fx["world"].(map[string]any)["state"].(map[string]any)
	state["orders"] = []map[string]any{{"order_id": "O-2077", "customer_id": "C-0311", "amount_cents": 12600}}
	state["refunds"] = refunds
	state["notifications"] = []map[string]any{{"notification_id": "N-1", "order_id": "O-2077", "customer_id": notifiedCustomer}}
	out, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixtures", "relational.attest.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

const (
	sumStatement     = `sum("refunds", "amount_cents", at_most=related("orders", "amount_cents", on="order_id"))`
	matchesStatement = `matches("notifications", "customer_id", related("orders", "customer_id", on="order_id"))`
)

func explanations(r callResult) []string {
	var out []string
	for _, row := range r.Doc["results"].([]any) {
		out = append(out, row.(map[string]any)["explanation"].(string))
	}
	return out
}

func TestRunContract_RelationalEffects_FailOnTheIncidentWorld(t *testing.T) {
	root := relationalProject(t, []map[string]any{
		{"refund_id": "R-1", "order_id": "O-2077", "amount_cents": 1200},
		{"refund_id": "R-2", "order_id": "O-2077", "amount_cents": 12600},
	}, "C-0520")
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/relational.attest.json",
		"statements":   []string{sumStatement, matchesStatement},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got, want := statuses(t, r), []string{"hard_fail", "hard_fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	want := []string{
		"effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600",
		"effect violated: notifications customer_id C-0520 for O-2077 does not match orders.customer_id C-0311",
	}
	if got := explanations(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("explanations %q, want %q", got, want)
	}
	first := r.Doc["results"].([]any)[0].(map[string]any)
	if first["chain_prefix"] != ".world(FIXTURE.world)" {
		t.Errorf("chain_prefix %v", first["chain_prefix"])
	}
	if _, has := first["spec"].(map[string]any)["world"]; has {
		t.Error("result spec repeats the world snapshot")
	}
}

func TestRunContract_RelationalEffects_PassOnTheFixedWorld(t *testing.T) {
	root := relationalProject(t, []map[string]any{
		{"refund_id": "R-1", "order_id": "O-2077", "amount_cents": 1200},
		{"refund_id": "R-2", "order_id": "O-2077", "amount_cents": 11400},
	}, "C-0311")
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/relational.attest.json",
		"statements":   []string{sumStatement, matchesStatement},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got, want := statuses(t, r), []string{"pass", "pass"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses %v, want %v (%q)", got, want, explanations(r))
	}
}

func TestRunContract_RelationalEffects_RelatedOutsideTheClosedFormIsRejected(t *testing.T) {
	root := relationalProject(t, nil, "C-0311")
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/relational.attest.json",
		"statements":   []string{`sum("refunds", "amount_cents", at_most=related(open("f"), "x", on="y"))`},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got := statuses(t, r); !reflect.DeepEqual(got, []string{"error"}) {
		t.Fatalf("statuses %v", got)
	}
}

func TestDraftContract_DraftsRelationalChecksFromTheWorld(t *testing.T) {
	root := relationalProject(t, []map[string]any{
		{"refund_id": "R-1", "order_id": "O-2077", "amount_cents": 1200},
		{"refund_id": "R-2", "order_id": "O-2077", "amount_cents": 12600},
	}, "C-0311")
	r := call(t, connect(t, root), "draft_contract", map[string]any{"fixture_path": "fixtures/relational.attest.json"})
	if r.IsError {
		t.Fatal(r.Text)
	}
	byID := map[string]map[string]any{}
	for _, c := range r.Doc["contracts"].([]any) {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
	}
	sum := byID["relational-sum-refunds-amount_cents-per-order_id"]
	if sum == nil {
		t.Fatalf("no drafted sum contract in %v", r.Text)
	}
	if sum["status"] != "hard_fail" || sum["statement"] != sumStatement ||
		sum["explanation"] != "effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600" {
		t.Errorf("sum contract %v", sum)
	}
	matches := byID["relational-matches-notifications-customer_id-per-order_id"]
	if matches == nil || matches["status"] != "pass" || matches["statement"] != matchesStatement {
		t.Errorf("matches contract %v", matches)
	}
	// A drafted statement runs through run_contract unchanged.
	run := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/relational.attest.json",
		"statements":   []string{sum["statement"].(string), matches["statement"].(string)},
	})
	if got, want := statuses(t, run), []string{"hard_fail", "pass"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses %v, want %v", got, want)
	}
}

func TestDraftContract_ManySharedFieldsAreCappedAndReported(t *testing.T) {
	root := relationalProject(t, []map[string]any{{"refund_id": "R-1", "order_id": "O-2077", "amount_cents": 1}}, "C-0311")
	path := filepath.Join(root, "fixtures", "relational.attest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]any
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	state := fx["world"].(map[string]any)["state"].(map[string]any)
	for _, res := range []string{"orders", "refunds"} {
		rec := state[res].([]any)[0].(map[string]any)
		for i := 0; i < 500; i++ {
			rec[fmt.Sprintf("f%04d", i)] = 1
		}
	}
	out, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	r := call(t, connect(t, root), "draft_contract", map[string]any{"fixture_path": "fixtures/relational.attest.json"})
	if r.IsError {
		t.Fatal(r.Text)
	}
	relational := 0
	for _, c := range r.Doc["contracts"].([]any) {
		if c.(map[string]any)["kind"] == "relational" {
			relational++
		}
	}
	if relational != 50 {
		t.Errorf("%d relational contracts, want the cap of 50", relational)
	}
	if _, ok := r.Doc["relational_capped"].(string); !ok {
		t.Errorf("result does not say the relational drafts were capped: %v", r.Doc)
	}
}
