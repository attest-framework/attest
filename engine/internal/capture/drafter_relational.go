package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// KindRelational marks a contract drafted from the relation between two
// resources of a world snapshot.
const KindRelational = "relational"

// DraftRelational proposes relational effect contracts from a world-v1
// snapshot. It exists apart from Draft because the relations live in the
// world, which a trace does not carry, and a fixture's drafted contract list
// stays trace-only.
//
// Heuristic. A resource R relates to a resource T when R's records carry a
// field F named <name>_id, T is named <name>, <name>s, <name>es or the
// y-to-ies plural of <name>, and every T record has F with a distinct value.
// F is the join key. For every other field G that both resources carry
// (every record of both resources has it):
//
//   - all values numeric: sum(R, G, at_most=related(T, G, on=F)), the
//     "refunded total stays within the order amount" shape;
//   - all values strings and G is itself named *_id:
//     matches(R, G, related(T, G, on=F)), the "notification goes to the
//     order's customer" shape.
//
// A field name has to match in both resources: the draft does not guess
// which of two differently named fields should be compared.
func DraftRelational(snapshot json.RawMessage) ([]Contract, error) {
	var snap struct {
		State map[string][]map[string]any `json:"state"`
	}
	dec := json.NewDecoder(bytes.NewReader(snapshot))
	dec.UseNumber()
	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("world snapshot: %w", err)
	}
	var out []Contract
	for _, res := range sortedResourceNames(snap.State) {
		for _, key := range keyFields(snap.State[res]) {
			target, ok := relatedResource(snap.State, res, key)
			if !ok {
				continue
			}
			for _, field := range sharedFields(snap.State[res], snap.State[target], key) {
				c, err := relationalContract(res, target, field, key, snap.State)
				if err != nil {
					return nil, err
				}
				if c != nil {
					out = append(out, *c)
				}
			}
		}
	}
	return out, nil
}

func sortedResourceNames(state map[string][]map[string]any) []string {
	names := make([]string, 0, len(state))
	for n := range state {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// keyFields lists the *_id fields some record of the resource carries.
func keyFields(records []map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range records {
		for f := range r {
			if strings.HasSuffix(f, "_id") && len(f) > len("_id") {
				seen[f] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// relatedResource finds the resource a key field points at: the one named
// after the field whose records all carry the key with distinct values.
func relatedResource(state map[string][]map[string]any, from, key string) (string, bool) {
	base := strings.TrimSuffix(key, "_id")
	candidates := []string{base, base + "s", base + "es"}
	if strings.HasSuffix(base, "y") {
		candidates = append(candidates, strings.TrimSuffix(base, "y")+"ies")
	}
	for _, name := range candidates {
		records, ok := state[name]
		if !ok || name == from || len(records) == 0 || len(state[from]) == 0 {
			continue
		}
		distinct := map[string]bool{}
		unique := true
		for _, r := range records {
			v, present := r[key]
			if !present {
				unique = false
				break
			}
			b, _ := json.Marshal(v)
			if distinct[string(b)] {
				unique = false
				break
			}
			distinct[string(b)] = true
		}
		if unique {
			return name, true
		}
	}
	return "", false
}

// sharedFields lists the fields every record of both resources carries,
// except the join key.
func sharedFields(a, b []map[string]any, key string) []string {
	var out []string
	for f := range a[0] {
		if f != key && everyRecordHas(a, f) && everyRecordHas(b, f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func everyRecordHas(records []map[string]any, field string) bool {
	for _, r := range records {
		if _, ok := r[field]; !ok {
			return false
		}
	}
	return true
}

func allValues(records []map[string]any, field string, pred func(any) bool) bool {
	for _, r := range records {
		if !pred(r[field]) {
			return false
		}
	}
	return true
}

func isNumber(v any) bool { _, ok := v.(json.Number); return ok }
func isString(v any) bool { _, ok := v.(string); return ok }

func relationalContract(res, target, field, key string, state map[string][]map[string]any) (*Contract, error) {
	related := map[string]any{"resource": target, "field": field, "on": key}
	numeric := allValues(state[res], field, isNumber) && allValues(state[target], field, isNumber)
	identifier := strings.HasSuffix(field, "_id") && allValues(state[res], field, isString) && allValues(state[target], field, isString)
	var spec map[string]any
	var id, desc string
	switch {
	case numeric:
		spec = map[string]any{"check": "sum", "resource": res, "field": field, "at_most": map[string]any{"related": related}}
		id = fmt.Sprintf("relational-sum-%s-%s-per-%s", res, field, key)
		desc = fmt.Sprintf("%s %s sums to at most %s.%s for each %s", res, field, target, field, key)
	case identifier:
		spec = map[string]any{"check": "matches", "resource": res, "field": field, "related": related}
		id = fmt.Sprintf("relational-matches-%s-%s-per-%s", res, field, key)
		desc = fmt.Sprintf("%s %s equals %s.%s for the same %s", res, field, target, field, key)
	default:
		return nil, nil
	}
	raw, err := canonical(spec)
	if err != nil {
		return nil, err
	}
	return &Contract{ID: id, Kind: KindRelational, Description: desc, Type: types.TypeEffect, Spec: raw}, nil
}

// VerifyRelational evaluates drafted relational contracts against the world
// they were drafted from.
func VerifyRelational(snapshot json.RawMessage, contracts []Contract) ([]ContractResult, error) {
	ev := &assertion.EffectEvaluator{}
	out := make([]ContractResult, 0, len(contracts))
	for _, c := range contracts {
		var spec map[string]json.RawMessage
		if err := json.Unmarshal(c.Spec, &spec); err != nil {
			return nil, fmt.Errorf("contract %s: %w", c.ID, err)
		}
		spec["world"] = snapshot
		raw, err := json.Marshal(spec)
		if err != nil {
			return nil, fmt.Errorf("contract %s: %w", c.ID, err)
		}
		r := ev.Evaluate(&types.Trace{}, &types.Assertion{AssertionID: c.ID, Type: c.Type, Spec: raw})
		out = append(out, ContractResult{Contract: c.ID, Status: r.Status, Explanation: r.Explanation})
	}
	return out, nil
}
