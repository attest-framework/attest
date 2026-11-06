package mutate_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/mutate"
)

// withNotificationCustomer adds the customer the notification went to, so the
// world holds a field that should match the order's customer.
func withNotificationCustomer(t *testing.T, s mutate.Subject) mutate.Subject {
	t.Helper()
	var world map[string]json.RawMessage
	if err := json.Unmarshal(s.World, &world); err != nil {
		t.Fatal(err)
	}
	var state map[string][]map[string]any
	if err := json.Unmarshal(world["state"], &state); err != nil {
		t.Fatal(err)
	}
	state["notifications"][0]["customer_id"] = "C-0311"
	var err error
	if world["state"], err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if s.World, err = json.Marshal(world); err != nil {
		t.Fatal(err)
	}
	return s
}

func relationalSpecs(t *testing.T, world json.RawMessage) []mutate.Spec {
	t.Helper()
	sum := `{"check":"sum","resource":"refunds","field":"amount_cents","at_most":{"related":{"resource":"orders","field":"amount_cents","on":"order_id"}},"world":` + string(world) + `}`
	matches := `{"check":"matches","resource":"notifications","field":"customer_id","related":{"resource":"orders","field":"customer_id","on":"order_id"},"world":` + string(world) + `}`
	return []mutate.Spec{
		{AssertionID: "refund-within-order", Type: "effect", Spec: json.RawMessage(sum)},
		{AssertionID: "notice-to-customer", Type: "effect", Spec: json.RawMessage(matches)},
	}
}

func TestRun_RelationalEffectSpecs_KillRaisedAndRedirectedRecords(t *testing.T) {
	s, _ := loadSubject(t)
	s = withNotificationCustomer(t, s)
	rep, err := mutate.Run([]mutate.Subject{s}, relationalSpecs(t, s.World), mutate.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	exceed := find(t, rep, mutate.AlterWorldEffect, "raise refunds record 0")
	if exceed.Status != mutate.StatusKilled || len(exceed.KilledBy) != 1 || exceed.KilledBy[0].AssertionID != "refund-within-order" {
		t.Errorf("raised refund: %s killed by %+v", exceed.Status, exceed.KilledBy)
	}
	if !strings.Contains(exceed.Description, "above orders.amount_cents") {
		t.Errorf("description %q does not name the bound", exceed.Description)
	}
	redirect := find(t, rep, mutate.AlterWorldEffect, "redirect notifications record 0")
	if redirect.Status != mutate.StatusKilled || redirect.KilledBy[0].AssertionID != "notice-to-customer" {
		t.Errorf("redirected notification: %s killed by %+v", redirect.Status, redirect.KilledBy)
	}
}

func TestRun_WithoutRelationalSpecs_RaisedRefundSurvivesWithASumSuggestion(t *testing.T) {
	s, _ := loadSubject(t)
	rep, err := mutate.Run([]mutate.Subject{s}, outputOnly(), mutate.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	exceed := find(t, rep, mutate.AlterWorldEffect, "raise refunds record 0")
	if exceed.Status != mutate.StatusSurvived {
		t.Fatalf("status %s", exceed.Status)
	}
	var found bool
	for _, sg := range rep.SurvivorsSuggestions {
		if sg.MutantID == exceed.ID {
			found = strings.Contains(string(sg.Contract.Spec), `"check":"sum"`)
		}
	}
	if !found {
		t.Errorf("no sum suggestion for %s", exceed.ID)
	}
}

func TestGenerate_RelationalMutants_AreDeterministic(t *testing.T) {
	s, _ := loadSubject(t)
	a, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	b, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(ms []mutate.Mutant) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if !reflect.DeepEqual(ids(a), ids(b)) {
		t.Error("two generations of one seed differ")
	}
}

func TestRun_PerOperatorStats_MatchMutantsAndExcludedPopulations(t *testing.T) {
	s, _ := loadSubject(t)
	specs := append(outputOnly(), mutate.Spec{AssertionID: "j", Type: "llm_judge", Spec: json.RawMessage(`{}`)})
	rep, err := mutate.Run([]mutate.Subject{s}, specs, mutate.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	var order []mutate.Operator
	for _, st := range rep.Operators {
		order = append(order, st.Operator)
		total += st.Generated
		var k, sv, na int
		for _, m := range rep.Mutants {
			if m.Operator != st.Operator {
				continue
			}
			switch m.Status {
			case mutate.StatusKilled:
				k++
			case mutate.StatusSurvived:
				sv++
			default:
				na++
			}
		}
		if st.Killed != k || st.Survived != sv || st.NoApplicableSpecs != na || st.Generated != k+sv+na {
			t.Errorf("%s: stats %+v, counted killed %d survived %d n/a %d", st.Operator, st, k, sv, na)
		}
		if (k+sv > 0) != (st.Score != nil) || (st.Score != nil && *st.Score != float64(k)/float64(k+sv)) {
			t.Errorf("%s: score %v for killed %d survived %d", st.Operator, st.Score, k, sv)
		}
	}
	if total != len(rep.Mutants) {
		t.Errorf("operators cover %d of %d mutants", total, len(rep.Mutants))
	}
	idx := map[mutate.Operator]int{}
	for i, op := range mutate.Operators {
		idx[op] = i
	}
	for i := 1; i < len(order); i++ {
		if idx[order[i-1]] >= idx[order[i]] {
			t.Errorf("operators out of enumeration order: %v", order)
		}
	}
	if len(rep.Excluded.SkippedSpecs) != 1 || rep.Excluded.SkippedSpecs[0].Specs != 1 || !strings.Contains(rep.Excluded.SkippedSpecs[0].Reason, "llm_judge") {
		t.Errorf("skipped specs %+v", rep.Excluded.SkippedSpecs)
	}
}

func TestRun_NoApplicableMutants_AreCountedAsExcluded(t *testing.T) {
	s, _ := loadSubject(t)
	rep, err := mutate.Run([]mutate.Subject{s}, nil, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Excluded.MutantsNoApplicableSpecs != len(rep.Mutants) || len(rep.Mutants) == 0 {
		t.Errorf("excluded %d of %d mutants", rep.Excluded.MutantsNoApplicableSpecs, len(rep.Mutants))
	}
	if rep.Excluded.SkippedSpecs == nil || rep.Operators == nil {
		t.Error("empty lists must serialize as [] not null")
	}
}
