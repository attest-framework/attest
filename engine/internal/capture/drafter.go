package capture

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// Contract kinds.
const (
	KindTools        = "tools"
	KindOrder        = "order"
	KindCardinality  = "cardinality"
	KindOutputSchema = "output_schema"
	KindCost         = "cost"
)

// CostHeadroom multiplies the observed cost into the drafted cost ceiling.
const CostHeadroom = 1.5

// Draft proposes deterministic contracts from a captured trace, encoding the
// observed behavior as ceilings: the tools it called, the order of their
// first calls, per-id cardinality for each tool called with an identifier
// argument (id or *_id), the shape of its structured output and a cost
// ceiling with headroom.
//
// Cardinality depends on the capture mode. A normal capture allows the
// observed per-id maximum, so a healthy run that reads the same id twice
// still verifies. An incident capture (expectFail) allows one call per id,
// so the calls the run repeated become its expected failures.
func Draft(t *types.Trace, expectFail bool) ([]Contract, error) {
	var out []Contract
	add := func(id, kind, typ, desc string, spec any) error {
		raw, err := canonical(spec)
		if err != nil {
			return fmt.Errorf("draft %s: %w", id, err)
		}
		out = append(out, Contract{ID: id, Kind: kind, Description: desc, Type: typ, Spec: raw})
		return nil
	}

	tools, idArgs := toolUsage(t)
	if len(tools) > 0 {
		spec := map[string]any{"check": "trajectory", "mode": "superset", "tools": tools, "soft": false}
		if err := add("tools-called", KindTools, types.TypeTrace, "calls "+strings.Join(tools, ", "), spec); err != nil {
			return nil, err
		}
	}
	for i := 1; i < len(tools); i++ {
		before, after := tools[i-1], tools[i]
		spec := map[string]any{"check": "tool_order", "before": before, "after": after, "soft": false}
		id := fmt.Sprintf("order-%s-before-%s", before, after)
		if err := add(id, KindOrder, types.TypeTrace, before+" is called before "+after, spec); err != nil {
			return nil, err
		}
	}
	for _, tool := range tools {
		for _, arg := range idArgs[tool] {
			bound := arg.observedMax
			if expectFail {
				bound = 1
			}
			spec := map[string]any{"check": "tool_cardinality", "tool": tool, "per": arg.name, "max": bound, "soft": false}
			id := fmt.Sprintf("cardinality-%s-per-%s", tool, arg.name)
			if err := add(id, KindCardinality, types.TypeTrace, cardinalityText(tool, arg.name, bound), spec); err != nil {
				return nil, err
			}
		}
	}
	if schema, ok := structuredOutputSchema(t.Output); ok {
		spec := map[string]any{"target": "output.structured", "schema": schema}
		if err := add("output-schema", KindOutputSchema, types.TypeSchema, "structured output keeps its observed shape", spec); err != nil {
			return nil, err
		}
	}
	if t.Metadata != nil && t.Metadata.CostUSD != nil && *t.Metadata.CostUSD > 0 {
		ceiling := math.Ceil(*t.Metadata.CostUSD*CostHeadroom*1e6) / 1e6
		spec := map[string]any{"field": "metadata.cost_usd", "operator": "lte", "value": ceiling, "soft": false}
		desc := fmt.Sprintf("cost at most $%g (observed $%g × %g)", ceiling, *t.Metadata.CostUSD, CostHeadroom)
		if err := add("cost-ceiling", KindCost, types.TypeConstraint, desc, spec); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// idArg is an identifier argument of one tool and the most calls that tool
// made with any one value of it.
type idArg struct {
	name        string
	observedMax int
}

func cardinalityText(tool, arg string, bound int) string {
	if bound == 1 {
		return tool + " at most once per " + arg
	}
	return fmt.Sprintf("%s at most %d× per %s (observed maximum)", tool, bound, arg)
}

// toolUsage returns the distinct tools in order of first call and, per tool,
// its identifier arguments (id or *_id) sorted by name with the observed
// per-value maximum. Only top-level steps count, as in the trace contract
// checks; values are compared as canonical JSON.
func toolUsage(t *types.Trace) ([]string, map[string][]idArg) {
	var tools []string
	counts := map[string]map[string]map[string]int{} // tool → arg → value → calls
	for i := range t.Steps {
		s := &t.Steps[i]
		if s.Type != types.StepTypeToolCall || s.Name == "" {
			continue
		}
		if counts[s.Name] == nil {
			counts[s.Name] = map[string]map[string]int{}
			tools = append(tools, s.Name)
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(s.Args, &args) != nil {
			continue
		}
		for name, raw := range args {
			if name != "id" && !strings.HasSuffix(name, "_id") {
				continue
			}
			v, err := decodeJSON(raw)
			if err != nil {
				continue
			}
			key, err := canonical(v)
			if err != nil {
				continue
			}
			if counts[s.Name][name] == nil {
				counts[s.Name][name] = map[string]int{}
			}
			counts[s.Name][name][string(key)]++
		}
	}
	out := make(map[string][]idArg, len(counts))
	for tool, byArg := range counts {
		args := make([]idArg, 0, len(byArg))
		for name, byValue := range byArg {
			most := 0
			for _, n := range byValue {
				most = max(most, n)
			}
			args = append(args, idArg{name: name, observedMax: most})
		}
		sort.Slice(args, func(i, j int) bool { return args[i].name < args[j].name })
		out[tool] = args
	}
	return tools, out
}

// structuredOutputSchema infers a JSON Schema for output.structured when the
// output carries a structured object: its keys are required and each keeps
// its observed JSON type.
func structuredOutputSchema(output json.RawMessage) (map[string]any, bool) {
	var out struct {
		Structured map[string]json.RawMessage `json:"structured"`
	}
	if json.Unmarshal(output, &out) != nil || len(out.Structured) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(out.Structured))
	props := map[string]any{}
	for k, raw := range out.Structured {
		typ := jsonType(raw)
		if typ == "" {
			continue
		}
		keys = append(keys, k)
		props[k] = map[string]any{"type": typ}
	}
	sort.Strings(keys)
	return map[string]any{"type": "object", "required": keys, "properties": props}, true
}

func jsonType(raw json.RawMessage) string {
	v, err := decodeJSON(raw)
	if err != nil {
		return ""
	}
	switch x := v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case nil:
		return "null"
	}
	return ""
}

// evaluators run the drafted contract types. All are deterministic.
var evaluators = map[string]interface {
	Evaluate(*types.Trace, *types.Assertion) *types.AssertionResult
}{
	types.TypeTrace:      &assertion.TraceEvaluator{},
	types.TypeSchema:     &assertion.SchemaEvaluator{},
	types.TypeConstraint: &assertion.ConstraintEvaluator{},
}

// Verify evaluates contracts on the trace. Without expectFail the fixture is
// verified when every contract passes; with expectFail (an incident
// reproduction) when at least one fails and the rest pass, and the failing
// ones are recorded as expected failures. A trace that fails trace check,
// holds no llm_call and no tool_call step, or yields no contracts (nothing to
// verify) is never verified.
func Verify(t *types.Trace, contracts []Contract, expectFail bool, check TraceCheckResult) (Verification, error) {
	v := Verification{ExpectFail: expectFail, TraceCheck: check, Results: []ContractResult{}, ExpectedFailures: []string{}}
	var failing, other []string
	for _, c := range contracts {
		ev, ok := evaluators[c.Type]
		if !ok {
			return v, fmt.Errorf("contract %s: type %q is not a deterministic contract type", c.ID, c.Type)
		}
		r := ev.Evaluate(t, &types.Assertion{AssertionID: c.ID, Type: c.Type, Spec: c.Spec})
		v.Results = append(v.Results, ContractResult{Contract: c.ID, Status: r.Status, Explanation: r.Explanation})
		switch r.Status {
		case types.StatusPass:
		case types.StatusHardFail, types.StatusSoftFail:
			failing = append(failing, c.ID)
		default:
			other = append(other, c.ID)
		}
	}
	// A capture that drafted no contract has nothing to hold: passing it
	// would be vacuous.
	evidence := HasModelOrToolSteps(t) && len(contracts) > 0
	if expectFail {
		v.ExpectedFailures = append(v.ExpectedFailures, failing...)
		v.Verified = check.OK && evidence && len(failing) > 0 && len(other) == 0
	} else {
		v.Verified = check.OK && evidence && len(failing) == 0 && len(other) == 0
	}
	return v, nil
}

// HasModelOrToolSteps reports whether a trace records at least one llm_call or
// tool_call step. Contracts over a trace without either pass vacuously.
func HasModelOrToolSteps(t *types.Trace) bool {
	for i := range t.Steps {
		if k := t.Steps[i].Type; k == types.StepTypeLLMCall || k == types.StepTypeToolCall {
			return true
		}
	}
	return false
}
