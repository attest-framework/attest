package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/internal/contractdsl"
	"github.com/attest-framework/attest/engine/internal/llm"
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
//
// At most contractdsl.MaxStatements contracts are returned, the number
// run_contract accepts in one call; the bool reports that more were found.
func DraftRelational(snapshot json.RawMessage) ([]Contract, bool, error) {
	var snap struct {
		State map[string][]map[string]any `json:"state"`
	}
	dec := json.NewDecoder(bytes.NewReader(snapshot))
	dec.UseNumber()
	if err := dec.Decode(&snap); err != nil {
		return nil, false, fmt.Errorf("world snapshot: %w", err)
	}
	var out []Contract
	seen := map[string]bool{}
	for _, res := range sortedResourceNames(snap.State) {
		for _, key := range keyFields(snap.State[res]) {
			target, ok := relatedResource(snap.State, res, key)
			if !ok {
				continue
			}
			for _, field := range sharedFields(snap.State[res], snap.State[target], key) {
				c, err := relationalContract(res, target, field, key, snap.State)
				if err != nil {
					return nil, false, err
				}
				if c == nil {
					continue
				}
				if len(out) == contractdsl.MaxStatements {
					return out, true, nil
				}
				if seen[c.ID] {
					// Distinct raw names can still render one id: the
					// hyphens of relational-sum-a-b-c do not say which
					// part is the resource.
					c.ID += "~" + shortHash(res, field, key)
					if seen[c.ID] {
						return nil, false, fmt.Errorf("relational contract id %q is not unique", c.ID)
					}
				}
				seen[c.ID] = true
				out = append(out, *c)
			}
		}
	}
	return out, false, nil
}

// maxDisplayName bounds a world name quoted in a contract id or description.
const maxDisplayName = 64

// displayName makes a world resource or field name safe to quote in an id or
// description: control characters are removed and the length is capped. The
// spec keeps the raw name, which the evaluator needs to find the data.
func displayName(name string) string {
	clean := llm.StripControl(name)
	if r := []rune(clean); len(r) > maxDisplayName {
		return string(r[:maxDisplayName]) + "..."
	}
	return clean
}

// idName makes a world name safe inside a contract id: displayName, with
// whitespace replaced (fixture-v1 ids match ^\S+$). A name any of that
// changed gets a short hash of the raw name, so two raw names that sanitize
// alike stay apart.
func idName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\uFEFF' {
			return '_'
		}
		return r
	}, displayName(name))
	if clean == name {
		return clean
	}
	return clean + "~" + shortHash(name)
}

func shortHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:4])
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
	dRes, dTarget, dField, dKey := displayName(res), displayName(target), displayName(field), displayName(key)
	iRes, iField, iKey := idName(res), idName(field), idName(key)
	switch {
	case numeric:
		spec = map[string]any{"check": "sum", "resource": res, "field": field, "at_most": map[string]any{"related": related}}
		id = fmt.Sprintf("relational-sum-%s-%s-per-%s", iRes, iField, iKey)
		desc = fmt.Sprintf("%s %s sums to at most %s.%s for each %s", dRes, dField, dTarget, dField, dKey)
	case identifier:
		spec = map[string]any{"check": "matches", "resource": res, "field": field, "related": related}
		id = fmt.Sprintf("relational-matches-%s-%s-per-%s", iRes, iField, iKey)
		desc = fmt.Sprintf("%s %s equals %s.%s for the same %s", dRes, dField, dTarget, dField, dKey)
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
// they were drafted from. The snapshot is validated and decoded once, and ctx
// is checked between contracts.
func VerifyRelational(ctx context.Context, snapshot json.RawMessage, contracts []Contract) ([]ContractResult, error) {
	w, err := assertion.LoadEffectWorld(snapshot)
	if err != nil {
		return nil, err
	}
	ev := &assertion.EffectEvaluator{}
	out := make([]ContractResult, 0, len(contracts))
	for _, c := range contracts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := ev.EvaluateIn(w, &types.Assertion{AssertionID: c.ID, Type: c.Type, Spec: c.Spec})
		out = append(out, ContractResult{Contract: c.ID, Status: r.Status, Explanation: r.Explanation})
	}
	return out, nil
}
