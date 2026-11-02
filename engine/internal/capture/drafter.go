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

// Draft proposes deterministic contracts from a captured trace: the tools it
// called, the order of their first calls, at most one call per identifier
// argument for each tool called with one, the shape of its structured output
// and a cost ceiling with headroom. Cardinality is drafted from the argument
// names, not the observed counts, so a captured duplicate call fails it.
func Draft(t *types.Trace) ([]Contract, error) {
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
			spec := map[string]any{"check": "tool_cardinality", "tool": tool, "per": arg, "max": 1, "soft": false}
			id := fmt.Sprintf("cardinality-%s-per-%s", tool, arg)
			if err := add(id, KindCardinality, types.TypeTrace, tool+" at most once per "+arg, spec); err != nil {
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

// toolUsage returns the distinct tools in order of first call and, per tool,
// its sorted identifier argument names (id or *_id) across all its calls.
// Only top-level steps count, as in the trace contract checks.
func toolUsage(t *types.Trace) ([]string, map[string][]string) {
	var tools []string
	ids := map[string]map[string]bool{}
	for i := range t.Steps {
		s := &t.Steps[i]
		if s.Type != types.StepTypeToolCall || s.Name == "" {
			continue
		}
		if ids[s.Name] == nil {
			ids[s.Name] = map[string]bool{}
			tools = append(tools, s.Name)
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(s.Args, &args) != nil {
			continue
		}
		for name := range args {
			if name == "id" || strings.HasSuffix(name, "_id") {
				ids[s.Name][name] = true
			}
		}
	}
	out := make(map[string][]string, len(ids))
	for tool, set := range ids {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		out[tool] = names
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
// ones are recorded as expected failures. A trace that fails trace check is
// never verified.
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
	if expectFail {
		v.ExpectedFailures = append(v.ExpectedFailures, failing...)
		v.Verified = check.OK && len(failing) > 0 && len(other) == 0
	} else {
		v.Verified = check.OK && len(failing) == 0 && len(other) == 0
	}
	return v, nil
}
