package mutate_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/internal/mutate"
)

// worldSubject swaps the world of the refund-desk subject for state.
func worldSubject(t *testing.T, state map[string]any) mutate.Subject {
	t.Helper()
	s, _ := loadSubject(t)
	st, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(s.World, &raw); err != nil {
		t.Fatal(err)
	}
	raw["state"] = st
	w, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.World = w
	return s
}

func relationalMutants(t *testing.T, s mutate.Subject) []mutate.Mutant {
	t.Helper()
	ms, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	var out []mutate.Mutant
	for _, m := range ms {
		if strings.Contains(m.ID, ".exceed.") || strings.Contains(m.ID, ".redirect.") {
			out = append(out, m)
		}
	}
	return out
}

// refundMutants keeps the relational mutants of the refunds resource.
func refundMutants(ms []mutate.Mutant) []mutate.Mutant {
	var out []mutate.Mutant
	for _, m := range ms {
		if strings.Contains(m.ID, ":refunds.") {
			out = append(out, m)
		}
	}
	return out
}

func multiRelationState() map[string]any {
	return map[string]any{
		"orders":    []map[string]any{{"order_id": "O-1", "customer_id": "C-1", "amount_cents": 100}},
		"customers": []map[string]any{{"customer_id": "C-1", "amount_cents": 500}},
		"refunds":   []map[string]any{{"refund_id": "R-1", "order_id": "O-1", "customer_id": "C-1", "amount_cents": 10}},
	}
}

func TestGenerate_MultiRelationRecord_GivesDistinctMutantIDs(t *testing.T) {
	ms := refundMutants(relationalMutants(t, worldSubject(t, multiRelationState())))
	seen := map[string]bool{}
	exceed := 0
	for _, m := range ms {
		if seen[m.ID] {
			t.Errorf("duplicate mutant id %s", m.ID)
		}
		seen[m.ID] = true
		if strings.Contains(m.ID, ".exceed.") {
			exceed++
		}
	}
	if exceed != 2 {
		t.Errorf("want one exceed mutant per relation (2), got %d: %v", exceed, seen)
	}
}

func TestRun_MultiRelationRecord_EachSuggestionMatchesItsOwnMutant(t *testing.T) {
	s := worldSubject(t, multiRelationState())
	rep, err := mutate.Run([]mutate.Subject{s}, outputOnly(), mutate.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string][]mutate.Suggestion{}
	for _, sg := range rep.SurvivorsSuggestions {
		byID[sg.MutantID] = append(byID[sg.MutantID], sg)
	}
	checked := 0
	for _, m := range rep.Mutants {
		if !strings.Contains(m.ID, ":refunds.") || !strings.Contains(m.ID, ".exceed.") {
			continue
		}
		checked++
		sgs := byID[m.ID]
		if len(sgs) != 1 {
			t.Fatalf("%s has %d suggestions", m.ID, len(sgs))
		}
		target := "orders"
		if strings.Contains(m.ID, ".customers.") {
			target = "customers"
		}
		if !strings.Contains(string(sgs[0].Contract.Spec), fmt.Sprintf(`"resource":"%s"`, target)) ||
			!strings.Contains(string(sgs[0].Contract.Spec), `"related":{"field":"amount_cents","on":"`) {
			t.Errorf("%s suggestion %s does not name %s", m.ID, sgs[0].Contract.Spec, target)
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d exceed mutants, want 2", checked)
	}
}

func TestGenerate_SharedFields_GiveOneExceedAndOneRedirectPerRelation(t *testing.T) {
	state := map[string]any{
		"orders": []map[string]any{{"order_id": "O-1", "customer_id": "C-1", "region_id": "R-1",
			"amount_cents": 100, "fee_cents": 5}},
		"refunds": []map[string]any{{"order_id": "O-1", "customer_id": "C-1", "region_id": "R-1",
			"amount_cents": 10, "fee_cents": 1}},
	}
	var exceed, redirect []string
	for _, m := range refundMutants(relationalMutants(t, worldSubject(t, state))) {
		if strings.Contains(m.ID, ".exceed.") {
			exceed = append(exceed, m.ID)
		} else {
			redirect = append(redirect, m.ID)
		}
	}
	if len(exceed) != 1 || !strings.HasSuffix(exceed[0], ".amount_cents") {
		t.Errorf("exceed mutants %v, want only the first shared numeric field", exceed)
	}
	if len(redirect) != 1 || !strings.HasSuffix(redirect[0], ".customer_id") {
		t.Errorf("redirect mutants %v, want only the first shared *_id field", redirect)
	}
}

func TestGenerate_ManyIDFieldsAcrossManyResources_StayUnderTheCap(t *testing.T) {
	const keys, records = 100, 1
	state := map[string]any{}
	main := make([]map[string]any, records)
	for j := range main {
		main[j] = map[string]any{"name": fmt.Sprintf("m%d", j)}
	}
	for i := 0; i < keys; i++ {
		field := fmt.Sprintf("k%03d_id", i)
		target := make([]map[string]any, records)
		for j := range target {
			target[j] = map[string]any{}
			for f := 0; f < keys; f++ {
				target[j][fmt.Sprintf("k%03d_id", f)] = fmt.Sprintf("k%03d-%d", f, j)
			}
		}
		state[fmt.Sprintf("k%03d_ids", i)] = target
		for j := range main {
			main[j][field] = fmt.Sprintf("k%03d-%d", i, j)
		}
	}
	// resource names follow <base>s: k000s etc.
	renamed := map[string]any{"mains": main}
	for i := 0; i < keys; i++ {
		renamed[fmt.Sprintf("k%03ds", i)] = state[fmt.Sprintf("k%03d_ids", i)]
	}
	start := time.Now()
	ms := relationalMutants(t, worldSubject(t, renamed))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("generation took %s", elapsed)
	}
	if len(ms) != 256 {
		t.Errorf("relational mutants %d, want the cap 256", len(ms))
	}
}

func TestGenerate_HugeExponentBound_SkipsTheExceedMutant(t *testing.T) {
	state := map[string]any{
		"orders":  []map[string]any{{"order_id": "O-1", "amount_cents": json.Number("1e1000000")}},
		"refunds": []map[string]any{{"order_id": "O-1", "amount_cents": json.Number("1")}},
	}
	start := time.Now()
	ms := relationalMutants(t, worldSubject(t, state))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("generation took %s", elapsed)
	}
	for _, m := range ms {
		if len(m.World) > 10_000 {
			t.Errorf("mutant %s carries a %d byte world", m.ID, len(m.World))
		}
		if strings.Contains(m.ID, ".exceed.") {
			t.Errorf("exceed mutant %s was generated for an unrepresentable bound", m.ID)
		}
	}
}
