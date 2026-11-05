package mutate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/attest-framework/attest/engine/pkg/types"
)

const injectedErrorMessage = "injected tool failure"

func cloneTrace(t *types.Trace) (*types.Trace, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var c types.Trace
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// toolSteps returns the indices of the top-level tool_call steps.
func toolSteps(t *types.Trace) []int {
	var idx []int
	for i := range t.Steps {
		if t.Steps[i].Type == types.StepTypeToolCall {
			idx = append(idx, i)
		}
	}
	return idx
}

// removeStep deletes step i and keeps turns and handoffs pointing at the
// same steps.
func removeStep(t *types.Trace, i int) {
	t.Steps = append(t.Steps[:i], t.Steps[i+1:]...)
	remapRetries(t, func(k int) int {
		switch {
		case k == i:
			return -1
		case k > i:
			return k - 1
		}
		return k
	})
	for k := range t.Turns {
		kept := make([]int, 0, len(t.Turns[k].StepIndices))
		for _, s := range t.Turns[k].StepIndices {
			switch {
			case s < i:
				kept = append(kept, s)
			case s > i:
				kept = append(kept, s-1)
			}
		}
		t.Turns[k].StepIndices = kept
	}
	for k := range t.Handoffs {
		if t.Handoffs[k].StepIndex > i {
			t.Handoffs[k].StepIndex--
		}
	}
}

// remapRetries rewrites every retry_of through m, which maps an old step
// index to its new one (-1 when the step is gone). A retry whose target is
// gone, or no longer precedes it, becomes a plain call, so the trace still
// passes validation.
func remapRetries(t *types.Trace, m func(int) int) {
	for i := range t.Steps {
		r := t.Steps[i].RetryOf
		if r == nil {
			continue
		}
		n := m(*r)
		if n < 0 || n >= i {
			t.Steps[i].RetryOf = nil
			continue
		}
		t.Steps[i].RetryOf = &n
	}
}

// insertAfter places a copy of step i at i+1; the copy joins the turn that
// holds step i.
func insertAfter(t *types.Trace, i int, step types.Step) {
	t.Steps = append(t.Steps, types.Step{})
	copy(t.Steps[i+2:], t.Steps[i+1:])
	t.Steps[i+1] = step
	remapRetries(t, func(k int) int {
		if k > i {
			return k + 1
		}
		return k
	})
	for k := range t.Turns {
		var next []int
		for _, s := range t.Turns[k].StepIndices {
			switch {
			case s < i:
				next = append(next, s)
			case s == i:
				next = append(next, i, i+1)
			default:
				next = append(next, s+1)
			}
		}
		t.Turns[k].StepIndices = next
	}
	for k := range t.Handoffs {
		if t.Handoffs[k].StepIndex > i {
			t.Handoffs[k].StepIndex++
		}
	}
}

// decodeArgs parses a step's arguments as an object, keeping numbers exact.
func decodeArgs(raw json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scalar reports whether v is a string, number or bool.
func scalar(v any) bool {
	switch v.(type) {
	case string, json.Number, bool:
		return true
	}
	return false
}

// businessKey picks the argument that identifies the entity a record or call
// is about: the first argument, in name order, called id or ending in _id,
// whose value is a string or number.
func businessKey(m map[string]any) (string, any, bool) {
	for _, k := range sortedKeys(m) {
		if k != "id" && !strings.HasSuffix(k, "_id") {
			continue
		}
		switch m[k].(type) {
		case string, json.Number:
			return k, m[k], true
		}
	}
	return "", nil, false
}

func show(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mutate: marshal %T: %v", v, err))
	}
	return b
}

func describeCall(verb string, step *types.Step) string {
	args := decodeArgs(step.Args)
	if k, v, ok := businessKey(args); ok {
		return fmt.Sprintf("%s %s (%s=%s)", verb, step.Name, k, show(v))
	}
	return fmt.Sprintf("%s %s", verb, step.Name)
}

func genDrop(s Subject, add func(Mutant) error) error {
	for _, i := range toolSteps(s.Trace) {
		t, err := cloneTrace(s.Trace)
		if err != nil {
			return err
		}
		name := t.Steps[i].Name
		desc := describeCall("drop", &t.Steps[i])
		remaining := 0
		for _, j := range toolSteps(s.Trace) {
			if j != i && s.Trace.Steps[j].Name == name {
				remaining++
			}
		}
		total := remaining + 1
		removeStep(t, i)
		cands := []Contract{}
		if remaining == 0 {
			cands = append(cands, Contract{
				Type:        types.TypeTrace,
				Spec:        rawJSON(map[string]any{"check": "required_tools", "tools": []string{name}, "soft": false}),
				Description: fmt.Sprintf("%s is called", name),
			})
		} else {
			cands = append(cands, Contract{
				Type:        types.TypeTrace,
				Spec:        rawJSON(map[string]any{"check": "tool_cardinality", "tool": name, "min": total, "soft": false}),
				Description: fmt.Sprintf("%s is called at least %d times", name, total),
			})
		}
		if err := add(Mutant{site: strconv.Itoa(i), Operator: DropToolCall, Description: desc, Trace: t, candidates: cands}); err != nil {
			return err
		}
	}
	return nil
}

// cardinalityCandidate suggests tool_cardinality on name: at most the
// original count per business key, or in total when the call has no key.
func cardinalityCandidate(orig *types.Trace, step *types.Step) Contract {
	name := step.Name
	args := decodeArgs(step.Args)
	if per, _, ok := businessKey(args); ok {
		groups := map[string]int{}
		complete := true
		for _, j := range toolSteps(orig) {
			if orig.Steps[j].Name != name {
				continue
			}
			v, has := decodeArgs(orig.Steps[j].Args)[per]
			if !has {
				complete = false
				break
			}
			groups[show(v)]++
		}
		if complete {
			maxN := 0
			for _, n := range groups {
				if n > maxN {
					maxN = n
				}
			}
			return Contract{
				Type:        types.TypeTrace,
				Spec:        rawJSON(map[string]any{"check": "tool_cardinality", "tool": name, "per": per, "max": maxN, "soft": false}),
				Description: fmt.Sprintf("%s is called at most %d× per %s", name, maxN, per),
			}
		}
	}
	total := 0
	for _, j := range toolSteps(orig) {
		if orig.Steps[j].Name == name {
			total++
		}
	}
	return Contract{
		Type:        types.TypeTrace,
		Spec:        rawJSON(map[string]any{"check": "tool_cardinality", "tool": name, "max": total, "soft": false}),
		Description: fmt.Sprintf("%s is called at most %d× in total", name, total),
	}
}

func genDuplicate(s Subject, add func(Mutant) error) error {
	for _, i := range toolSteps(s.Trace) {
		t, err := cloneTrace(s.Trace)
		if err != nil {
			return err
		}
		cp := t.Steps[i]
		if cp.SpanID != "" {
			cp.SpanID += "-dup"
		}
		if cp.ToolCallID != "" {
			cp.ToolCallID += "-dup"
		}
		desc := describeCall("duplicate", &t.Steps[i])
		cand := cardinalityCandidate(s.Trace, &s.Trace.Steps[i])
		insertAfter(t, i, cp)
		if err := add(Mutant{site: strconv.Itoa(i), Operator: DuplicateToolCall, Description: desc, Trace: t, candidates: []Contract{cand}}); err != nil {
			return err
		}
	}
	return nil
}

func genReorder(s Subject, add func(Mutant) error) error {
	idx := toolSteps(s.Trace)
	for k := 0; k+1 < len(idx); k++ {
		i, j := idx[k], idx[k+1]
		if s.Trace.Steps[i].Name == s.Trace.Steps[j].Name {
			// Two calls of one tool (a retry, a repeat) swap into a trace no
			// tool_order contract can tell apart.
			continue
		}
		t, err := cloneTrace(s.Trace)
		if err != nil {
			return err
		}
		first, second := t.Steps[i].Name, t.Steps[j].Name
		t.Steps[i], t.Steps[j] = t.Steps[j], t.Steps[i]
		remapRetries(t, func(k int) int {
			switch k {
			case i:
				return j
			case j:
				return i
			}
			return k
		})
		cands := []Contract{{
			Type:        types.TypeTrace,
			Spec:        rawJSON(map[string]any{"check": "tool_order", "before": first, "after": second, "soft": false}),
			Description: fmt.Sprintf("%s is called before %s", first, second),
		}}
		desc := fmt.Sprintf("swap %s and %s", first, second)
		if err := add(Mutant{site: fmt.Sprintf("%d-%d", i, j), Operator: ReorderToolCalls, Description: desc, Trace: t, candidates: cands}); err != nil {
			return err
		}
	}
	return nil
}

// perturbed returns a deterministic different value of the same type: a
// string gets a suffix, a number doubles (zero becomes one), a bool flips.
func perturbed(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		return x + "_x", true
	case bool:
		return !x, true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			if n == 0 {
				return json.Number("1"), true
			}
			if n > -(1<<62) && n < 1<<62 {
				return json.Number(strconv.FormatInt(n*2, 10)), true
			}
		}
		f, err := x.Float64()
		if err != nil || math.IsInf(f*2, 0) {
			// Doubling must stay a finite JSON number.
			return nil, false
		}
		if f == 0 {
			return json.Number("1"), true
		}
		return json.Number(strconv.FormatFloat(f*2, 'g', -1, 64)), true
	}
	return nil, false
}

func genPerturb(s Subject, add func(Mutant) error) error {
	for _, i := range toolSteps(s.Trace) {
		args := decodeArgs(s.Trace.Steps[i].Args)
		for _, name := range sortedKeys(args) {
			nv, ok := perturbed(args[name])
			if !ok {
				continue
			}
			t, err := cloneTrace(s.Trace)
			if err != nil {
				return err
			}
			mutated := decodeArgs(t.Steps[i].Args)
			mutated[name] = nv
			t.Steps[i].Args = rawJSON(mutated)
			tool := t.Steps[i].Name
			spec := argPredicate(s.Trace, tool, name)
			desc := fmt.Sprintf("perturb %s.%s (%s -> %s)", tool, name, show(args[name]), show(nv))
			if err := add(Mutant{site: fmt.Sprintf("%d.%s", i, name), Operator: PerturbArgument, Description: desc, Trace: t, candidates: []Contract{spec}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// argPredicate pins the argument to the values the original calls used: eq
// for one distinct value, in for several.
func argPredicate(orig *types.Trace, tool, arg string) Contract {
	var distinct []any
	seen := map[string]bool{}
	for _, j := range toolSteps(orig) {
		if orig.Steps[j].Name != tool {
			continue
		}
		v, ok := decodeArgs(orig.Steps[j].Args)[arg]
		if !ok || seen[show(v)] {
			continue
		}
		seen[show(v)] = true
		distinct = append(distinct, v)
	}
	spec := map[string]any{"check": "arg_predicate", "tool": tool, "arg": arg, "soft": false}
	desc := ""
	if len(distinct) == 1 {
		spec["op"], spec["value"] = "eq", distinct[0]
		desc = fmt.Sprintf("%s.%s equals %s", tool, arg, show(distinct[0]))
	} else {
		spec["op"], spec["value"] = "in", distinct
		desc = fmt.Sprintf("%s.%s is one of the captured values", tool, arg)
	}
	return Contract{Type: types.TypeTrace, Spec: rawJSON(spec), Description: desc}
}

func genInjectError(s Subject, add func(Mutant) error) error {
	if s.Trace.SchemaVersion < types.TraceSchemaVersion2 {
		// Step errors are a trace-v2 field; contracts read none from v1.
		return nil
	}
	streak, run := 0, 0
	for _, i := range toolSteps(s.Trace) {
		if s.Trace.Steps[i].Error == nil {
			run = 0
			continue
		}
		if run++; run > streak {
			streak = run
		}
	}
	for _, i := range toolSteps(s.Trace) {
		if s.Trace.Steps[i].Error != nil {
			continue
		}
		t, err := cloneTrace(s.Trace)
		if err != nil {
			return err
		}
		t.Steps[i].Error = &types.StepError{Type: "tool_error", Message: injectedErrorMessage}
		t.Steps[i].Result = json.RawMessage("null")
		cands := []Contract{{
			Type:        types.TypeTrace,
			Spec:        rawJSON(map[string]any{"check": "failure_streak", "max": streak, "soft": false}),
			Description: fmt.Sprintf("no more than %d tool call(s) fail in a row", streak),
		}}
		desc := describeCall("inject error into", &t.Steps[i])
		if err := add(Mutant{site: strconv.Itoa(i), Operator: InjectToolError, Description: desc, Trace: t, candidates: cands}); err != nil {
			return err
		}
	}
	return nil
}
