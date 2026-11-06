package mutate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/attest-framework/attest/engine/pkg/types"
)

type worldState struct {
	raw   map[string]json.RawMessage
	state map[string][]map[string]any
}

func parseWorld(w json.RawMessage) (*worldState, error) {
	if len(bytes.TrimSpace(w)) == 0 || string(bytes.TrimSpace(w)) == "null" {
		return nil, nil
	}
	ws := &worldState{}
	if err := json.Unmarshal(w, &ws.raw); err != nil {
		return nil, fmt.Errorf("world snapshot: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(ws.raw["state"]))
	dec.UseNumber()
	if err := dec.Decode(&ws.state); err != nil {
		return nil, fmt.Errorf("world snapshot state: %w", err)
	}
	return ws, nil
}

// clone returns an independent copy of the state.
func (ws *worldState) clone() *worldState {
	c := &worldState{raw: map[string]json.RawMessage{}, state: map[string][]map[string]any{}}
	for k, v := range ws.raw {
		c.raw[k] = v
	}
	for res, recs := range ws.state {
		out := make([]map[string]any, len(recs))
		for i, r := range recs {
			cp := make(map[string]any, len(r))
			for k, v := range r {
				cp[k] = v
			}
			out[i] = cp
		}
		c.state[res] = out
	}
	return c
}

func (ws *worldState) marshal() json.RawMessage {
	ws.raw["state"] = rawJSON(ws.state)
	return rawJSON(ws.raw)
}

// whereOf builds a where filter from fields of rec.
func whereOf(rec map[string]any, extra map[string]any) map[string]any {
	where := map[string]any{}
	if k, v, ok := businessKey(rec); ok {
		where[k] = v
	}
	for k, v := range extra {
		where[k] = v
	}
	return where
}

func matchesWhere(rec map[string]any, where map[string]any) bool {
	for k, v := range where {
		got, ok := rec[k]
		if !ok || show(got) != show(v) {
			return false
		}
	}
	return true
}

func countMatching(recs []map[string]any, where map[string]any) int {
	n := 0
	for _, r := range recs {
		if matchesWhere(r, where) {
			n++
		}
	}
	return n
}

func describeRecord(rec map[string]any) string {
	if k, v, ok := businessKey(rec); ok {
		return fmt.Sprintf(" (%s=%s)", k, show(v))
	}
	return ""
}

// countCandidate pins how many records of resource match where.
func countCandidate(resource string, where map[string]any, n int) Contract {
	spec := map[string]any{"check": "count", "resource": resource, "equals": n}
	if len(where) > 0 {
		spec["where"] = where
	}
	return Contract{
		Type:        types.TypeEffect,
		Spec:        rawJSON(spec),
		Description: fmt.Sprintf("%s has exactly %d matching record(s)", resource, n),
	}
}

func genWorld(s Subject, add func(Mutant) error) error {
	ws, err := parseWorld(s.World)
	if err != nil {
		return err
	}
	if ws == nil {
		return nil
	}
	for _, res := range sortedKeys(ws.state) {
		recs := ws.state[res]
		for i, rec := range recs {
			where := whereOf(rec, nil)
			n := countMatching(recs, where)

			dup := ws.clone()
			dup.state[res] = append(dup.state[res][:i+1], dup.state[res][i:]...)
			dup.state[res][i+1] = copyRecord(rec)
			cands := []Contract{}
			if f, ok := rec["idempotency_key"].(string); ok && f != "" {
				spec := map[string]any{"check": "idempotent", "resource": res, "field": "idempotency_key"}
				if len(where) > 0 {
					spec["where"] = where
				}
				cands = append(cands, Contract{
					Type: types.TypeEffect, Spec: rawJSON(spec),
					Description: fmt.Sprintf("each idempotency_key in %s is written once", res),
				})
			}
			cands = append(cands, countCandidate(res, where, n))
			if err := add(Mutant{
				site: fmt.Sprintf("%s.%d.duplicate", res, i), Operator: AlterWorldEffect,
				Description: fmt.Sprintf("duplicate %s record %d%s", res, i, describeRecord(rec)),
				Trace:       s.Trace, World: dup.marshal(), candidates: cands,
			}); err != nil {
				return err
			}

			drop := ws.clone()
			drop.state[res] = append(drop.state[res][:i], drop.state[res][i+1:]...)
			if err := add(Mutant{
				site: fmt.Sprintf("%s.%d.drop", res, i), Operator: AlterWorldEffect,
				Description: fmt.Sprintf("drop %s record %d%s", res, i, describeRecord(rec)),
				Trace:       s.Trace, World: drop.marshal(), candidates: []Contract{countCandidate(res, where, n)},
			}); err != nil {
				return err
			}

			if err := genRelational(ws, s, res, i, add); err != nil {
				return err
			}

			field, nv, ok := changeableField(rec)
			if !ok {
				continue
			}
			chg := ws.clone()
			chg.state[res][i][field] = nv
			cwhere := whereOf(rec, map[string]any{field: rec[field]})
			cn := countMatching(recs, cwhere)
			if err := add(Mutant{
				site: fmt.Sprintf("%s.%d.change.%s", res, i, field), Operator: AlterWorldEffect,
				Description: fmt.Sprintf("change %s record %d%s: %s %s -> %s", res, i, describeRecord(rec), field, show(rec[field]), show(nv)),
				Trace:       s.Trace, World: chg.marshal(), candidates: []Contract{countCandidate(res, cwhere, cn)},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyRecord(rec map[string]any) map[string]any {
	cp := make(map[string]any, len(rec))
	for k, v := range rec {
		cp[k] = v
	}
	return cp
}

// changeableField is the first field, in name order, holding a scalar that
// perturbed() can alter.
func changeableField(rec map[string]any) (string, any, bool) {
	for _, k := range sortedKeys(rec) {
		if !scalar(rec[k]) {
			continue
		}
		if nv, ok := perturbed(rec[k]); ok {
			return k, nv, true
		}
	}
	return "", nil, false
}

// genTruncate cuts each non-empty string of the final output to its first
// half.
func genTruncate(s Subject, add func(Mutant) error) error {
	out := bytes.TrimSpace(s.Trace.Output)
	if len(out) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("trace output: %w", err)
	}
	cut := func(site, target string, text string, rebuild func(string) json.RawMessage) error {
		if text == "" {
			return nil
		}
		runes := []rune(text)
		keep := string(runes[:len(runes)/2])
		removed := strings.TrimSpace(string(runes[len(runes)/2:]))
		tail := lastRunes(removed, 24)
		t, err := cloneTrace(s.Trace)
		if err != nil {
			return err
		}
		t.Output = rebuild(keep)
		var cands []Contract
		if tail != "" {
			cands = append(cands, Contract{
				Type:        types.TypeContent,
				Spec:        rawJSON(map[string]any{"target": target, "check": "contains", "value": tail, "soft": false}),
				Description: fmt.Sprintf("%s contains %q", target, tail),
			})
		}
		return add(Mutant{
			site: site, Operator: TruncateOutput, Trace: t, candidates: cands,
			Description: fmt.Sprintf("truncate %s (%d -> %d chars)", target, len(runes), utf8.RuneCountInString(keep)),
		})
	}
	switch x := v.(type) {
	case string:
		return cut("output", "output", x, func(k string) json.RawMessage { return rawJSON(k) })
	case map[string]any:
		for _, name := range sortedKeys(x) {
			str, ok := x[name].(string)
			if !ok {
				continue
			}
			name := name
			err := cut("output."+name, "output."+name, str, func(k string) json.RawMessage {
				m := copyRecord(x)
				m[name] = k
				return rawJSON(m)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[len(r)-n:]
	}
	return strings.TrimSpace(string(r))
}

// relatedRecords lists the records of other resources that rec points to
// through a <name>_id field: resource <name>, <name>s, <name>es or the
// y -> ies plural holding exactly one record with that key value.
func relatedRecords(ws *worldState, res string, rec map[string]any) []relation {
	var out []relation
	for _, k := range sortedKeys(rec) {
		if !strings.HasSuffix(k, "_id") || !scalar(rec[k]) {
			continue
		}
		base := strings.TrimSuffix(k, "_id")
		names := []string{base, base + "s", base + "es"}
		if strings.HasSuffix(base, "y") {
			names = append(names, strings.TrimSuffix(base, "y")+"ies")
		}
		for _, t := range names {
			if t == res {
				continue
			}
			var found map[string]any
			n := 0
			for _, r := range ws.state[t] {
				if v, ok := r[k]; ok && show(v) == show(rec[k]) {
					found = r
					n++
				}
			}
			if n == 1 {
				out = append(out, relation{key: k, target: t, record: found})
				break
			}
		}
	}
	return out
}

type relation struct {
	key    string
	target string
	record map[string]any
}

// plusOne returns n + 1 as a JSON number.
func plusOne(n json.Number) (json.Number, bool) {
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return "", false
	}
	r.Add(r, big.NewRat(1, 1))
	if r.IsInt() {
		return json.Number(r.Num().String()), true
	}
	return json.Number(strings.TrimRight(strings.TrimRight(r.FloatString(12), "0"), ".")), true
}

// genRelational mutates a record against the record it relates to: a numeric
// field shared with the related record is raised one above the related value
// (a sum bound is exceeded) and a shared *_id string field is redirected (the
// match breaks). Each is suggested as the sum or matches check that kills it.
func genRelational(ws *worldState, s Subject, res string, i int, add func(Mutant) error) error {
	rec := ws.state[res][i]
	for _, rel := range relatedRecords(ws, res, rec) {
		for _, f := range sortedKeys(rec) {
			if f == rel.key {
				continue
			}
			rv, ok := rel.record[f]
			if !ok {
				continue
			}
			ref := map[string]any{"resource": rel.target, "field": f, "on": rel.key}
			switch cur := rec[f].(type) {
			case json.Number:
				bound, isNum := rv.(json.Number)
				if !isNum {
					continue
				}
				nv, ok := plusOne(bound)
				if !ok {
					continue
				}
				m := ws.clone()
				m.state[res][i][f] = nv
				spec := map[string]any{"check": "sum", "resource": res, "field": f, "at_most": map[string]any{"related": ref}}
				if err := add(Mutant{
					site: fmt.Sprintf("%s.%d.exceed.%s", res, i, f), Operator: AlterWorldEffect,
					Description: fmt.Sprintf("raise %s record %d%s: %s %s -> %s, above %s.%s %s", res, i, describeRecord(rec), f, show(cur), show(nv), rel.target, f, show(bound)),
					Trace:       s.Trace, World: m.marshal(),
					candidates: []Contract{{
						Type: types.TypeEffect, Spec: rawJSON(spec),
						Description: fmt.Sprintf("%s %s sums stay within %s.%s per %s", res, f, rel.target, f, rel.key),
					}},
				}); err != nil {
					return err
				}
			case string:
				if !strings.HasSuffix(f, "_id") {
					continue
				}
				if _, isStr := rv.(string); !isStr {
					continue
				}
				nv := cur + "_x"
				m := ws.clone()
				m.state[res][i][f] = nv
				spec := map[string]any{"check": "matches", "resource": res, "field": f, "related": ref}
				if err := add(Mutant{
					site: fmt.Sprintf("%s.%d.redirect.%s", res, i, f), Operator: AlterWorldEffect,
					Description: fmt.Sprintf("redirect %s record %d%s: %s %s -> %s, away from %s.%s", res, i, describeRecord(rec), f, show(cur), show(nv), rel.target, f),
					Trace:       s.Trace, World: m.marshal(),
					candidates: []Contract{{
						Type: types.TypeEffect, Spec: rawJSON(spec),
						Description: fmt.Sprintf("%s %s matches %s.%s per %s", res, f, rel.target, f, rel.key),
					}},
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
