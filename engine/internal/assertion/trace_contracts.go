package assertion

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/segmentio/encoding/json"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Behavior-contract trace checks. They match tool_call steps only, so the
// model calls interleaved between tool calls never break a trajectory, an
// ordering rule or a cycle. Retried calls (retry_of) count as calls: a retry
// that repeats a side effect is exactly what a cardinality contract catches.

// maxCyclePeriod bounds cycle detection; longer periods are out of scope.
const maxCyclePeriod = 4

// indexedStep is a step with its index in Trace.Steps, for explanations.
type indexedStep struct {
	index int
	step  *types.Step
}

func indexSteps(trace *types.Trace, keep func(*types.Step) bool) []indexedStep {
	var out []indexedStep
	for i := range trace.Steps {
		if keep(&trace.Steps[i]) {
			out = append(out, indexedStep{index: i, step: &trace.Steps[i]})
		}
	}
	return out
}

func toolCalls(trace *types.Trace) []indexedStep {
	return indexSteps(trace, func(s *types.Step) bool { return s.Type == types.StepTypeToolCall })
}

func toolCallsNamed(trace *types.Trace, tool string) []indexedStep {
	return indexSteps(trace, func(s *types.Step) bool {
		return s.Type == types.StepTypeToolCall && s.Name == tool
	})
}

func callNames(calls []indexedStep) []string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.step.Name
	}
	return names
}

func stepIndices(calls []indexedStep) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = fmt.Sprint(c.index)
	}
	return strings.Join(parts, ", ")
}

// resolveArg reads a dot-separated argument path from a step's args.
func resolveArg(step *types.Step, path string) (json.RawMessage, bool) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(step.Args, &args); err != nil || args == nil {
		return nil, false
	}
	val, err := navigateDotPath(args, path, "args")
	if err != nil {
		return nil, false
	}
	return val, true
}

// displayJSON renders a JSON value for an explanation: strings unquoted,
// everything else as compact JSON.
func displayJSON(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// ── tool_cardinality ──

func runToolCardinality(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Tool == "" {
		return checkOutcome{}, fmt.Errorf("tool_cardinality requires 'tool'")
	}
	if spec.Min == nil && spec.Max == nil {
		return checkOutcome{}, fmt.Errorf("tool_cardinality requires 'min' or 'max'")
	}
	if (spec.Min != nil && *spec.Min < 0) || (spec.Max != nil && *spec.Max < 0) {
		return checkOutcome{}, fmt.Errorf("tool_cardinality 'min' and 'max' must be >= 0")
	}
	if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
		return checkOutcome{}, fmt.Errorf("tool_cardinality 'min' %d exceeds 'max' %d", *spec.Min, *spec.Max)
	}
	return cardinality(spec.Tool, toolCallsNamed(trace, spec.Tool), spec.Per, spec.Min, spec.Max), nil
}

func boundsText(minN, maxN *int) string {
	switch {
	case minN != nil && maxN != nil:
		return fmt.Sprintf("min %d, max %d", *minN, *maxN)
	case maxN != nil:
		return fmt.Sprintf("max %d", *maxN)
	default:
		return fmt.Sprintf("min %d", *minN)
	}
}

func outOfBounds(count int, minN, maxN *int) bool {
	return (maxN != nil && count > *maxN) || (minN != nil && count < *minN)
}

// cardinality counts calls, grouped by the value of argument per when per is
// set. Every group must fall within [min, max]. Calls missing the per
// argument make the check not_evaluated unless a group already violates.
func cardinality(tool string, calls []indexedStep, per string, minN, maxN *int) checkOutcome {
	bounds := boundsText(minN, maxN)
	if per == "" {
		n := len(calls)
		text := fmt.Sprintf("%s called %d× (%s)", tool, n, bounds)
		return checkOutcome{passed: !outOfBounds(n, minN, maxN), explanation: text}
	}

	var keys []string
	counts := map[string]int{}
	var missing []string
	for _, c := range calls {
		raw, ok := resolveArg(c.step, per)
		if !ok {
			missing = append(missing, fmt.Sprint(c.index))
			continue
		}
		key := displayJSON(raw)
		if _, seen := counts[key]; !seen {
			keys = append(keys, key)
		}
		counts[key]++
	}

	var violations []string
	for _, key := range keys {
		if outOfBounds(counts[key], minN, maxN) {
			violations = append(violations, fmt.Sprintf("%s called %d× for %s=%s (%s)", tool, counts[key], per, key, bounds))
		}
	}
	if len(violations) > 0 {
		return checkOutcome{explanation: strings.Join(violations, "; ")}
	}
	if len(missing) > 0 {
		return checkOutcome{missing: fmt.Sprintf("%s call(s) at step(s) %s have no argument %q", tool, strings.Join(missing, ", "), per)}
	}
	return checkOutcome{
		passed:      true,
		explanation: fmt.Sprintf("%s within %s for each of %d %s value(s)", tool, bounds, len(keys), per),
	}
}

// ── trajectory ──

func countNames(names []string) map[string]int {
	counts := make(map[string]int, len(names))
	for _, n := range names {
		counts[n]++
	}
	return counts
}

// multisetExcess lists names that occur more often in a than in b.
func multisetExcess(a, b map[string]int, order []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		if a[name] > b[name] {
			out = append(out, fmt.Sprintf("%s (%d vs %d)", name, a[name], b[name]))
		}
	}
	return out
}

func runTrajectory(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	actual := callNames(toolCalls(trace))
	if spec.Mode == "partial_order" {
		return partialOrder(trace, actual, spec)
	}
	if err := requireTools(spec); err != nil {
		return checkOutcome{}, err
	}
	if len(spec.Order) > 0 {
		return checkOutcome{}, fmt.Errorf("trajectory 'order' applies only to mode partial_order")
	}
	want, got := countNames(spec.Tools), countNames(actual)
	missing := multisetExcess(want, got, spec.Tools)
	extra := multisetExcess(got, want, actual)

	var passed bool
	var problem string
	switch spec.Mode {
	case "strict":
		passed = reflect.DeepEqual(actual, spec.Tools)
		problem = fmt.Sprintf("tool calls %v differ from expected %v", actual, spec.Tools)
	case "unordered":
		passed = len(missing) == 0 && len(extra) == 0
		problem = fmt.Sprintf("tool calls %v are not a permutation of %v", actual, spec.Tools)
	case "subset":
		passed = len(extra) == 0
		problem = fmt.Sprintf("tool calls not in the expected trajectory: %s", strings.Join(extra, ", "))
	case "superset":
		passed = len(missing) == 0
		problem = fmt.Sprintf("expected tool calls missing: %s", strings.Join(missing, ", "))
	default:
		return checkOutcome{}, fmt.Errorf("trajectory 'mode' must be strict, unordered, subset, superset or partial_order, got %q", spec.Mode)
	}
	if !passed {
		return checkOutcome{explanation: fmt.Sprintf("trajectory (%s): %s", spec.Mode, problem)}, nil
	}
	return checkOutcome{passed: true, explanation: fmt.Sprintf("trajectory (%s) matches %v", spec.Mode, spec.Tools)}, nil
}

// partialOrder requires every tool in spec.Tools and in each order edge to be
// called, and each edge [a, b] to hold as a tool_order rule.
func partialOrder(trace *types.Trace, actual []string, spec *traceSpec) (checkOutcome, error) {
	if len(spec.Order) == 0 {
		return checkOutcome{}, fmt.Errorf("trajectory mode partial_order requires 'order'")
	}
	required := append([]string(nil), spec.Tools...)
	for _, edge := range spec.Order {
		if len(edge) != 2 || edge[0] == "" || edge[1] == "" || edge[0] == edge[1] {
			return checkOutcome{}, fmt.Errorf("trajectory 'order' entries must be [before, after] pairs of distinct tools, got %v", edge)
		}
		required = append(required, edge[0], edge[1])
	}
	got := countNames(actual)
	var absent []string
	seen := map[string]bool{}
	for _, name := range required {
		if !seen[name] && got[name] == 0 {
			absent = append(absent, name)
		}
		seen[name] = true
	}
	if len(absent) > 0 {
		return checkOutcome{explanation: fmt.Sprintf("trajectory (partial_order): tools not called: %s", strings.Join(absent, ", "))}, nil
	}
	var broken []string
	for _, edge := range spec.Order {
		if ok, text := orderHolds(trace, edge[0], edge[1]); !ok {
			broken = append(broken, text)
		}
	}
	if len(broken) > 0 {
		return checkOutcome{explanation: "trajectory (partial_order): " + strings.Join(broken, "; ")}, nil
	}
	return checkOutcome{passed: true, explanation: fmt.Sprintf("trajectory (partial_order) satisfies %v", spec.Order)}, nil
}

// ── tool_order ──

func runToolOrder(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Before == "" || spec.After == "" {
		return checkOutcome{}, fmt.Errorf("tool_order requires 'before' and 'after'")
	}
	if spec.Before == spec.After {
		return checkOutcome{}, fmt.Errorf("tool_order 'before' and 'after' must differ")
	}
	return outcomeOf(orderHolds(trace, spec.Before, spec.After)), nil
}

// orderHolds reports whether every call to after has an earlier call to
// before, which is the same as the first before preceding the first after.
// It holds vacuously when after is never called.
func orderHolds(trace *types.Trace, before, after string) (bool, string) {
	firstBefore, firstAfter := -1, -1
	for _, c := range toolCalls(trace) {
		if c.step.Name == before && firstBefore < 0 {
			firstBefore = c.index
		}
		if c.step.Name == after && firstAfter < 0 {
			firstAfter = c.index
		}
	}
	switch {
	case firstAfter < 0:
		return true, fmt.Sprintf("%s not called; %s-before-%s holds", after, before, after)
	case firstBefore < 0:
		return false, fmt.Sprintf("%s called at step %d without a prior %s", after, firstAfter, before)
	case firstBefore > firstAfter:
		return false, fmt.Sprintf("%s called at step %d before %s at step %d", after, firstAfter, before, firstBefore)
	default:
		return true, fmt.Sprintf("%s (step %d) precedes %s (step %d)", before, firstBefore, after, firstAfter)
	}
}

// ── cycle ──

// primitive reports whether block is not itself a repetition of a shorter
// block, so a period-2 check does not re-report a period-1 run (A A A A).
func primitive(block []string) bool {
	p := len(block)
	for d := 1; d < p; d++ {
		if p%d != 0 {
			continue
		}
		repeats := true
		for i := d; i < p; i++ {
			if block[i] != block[i-d] {
				repeats = false
				break
			}
		}
		if repeats {
			return false
		}
	}
	return true
}

func runCycle(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Period < 1 || spec.Period > maxCyclePeriod {
		return checkOutcome{}, fmt.Errorf("cycle 'period' must be between 1 and %d, got %d", maxCyclePeriod, spec.Period)
	}
	minRepeats := 2
	if spec.MinRepeats != nil {
		minRepeats = *spec.MinRepeats
	}
	if minRepeats < 2 {
		return checkOutcome{}, fmt.Errorf("cycle 'min_repeats' must be >= 2, got %d", minRepeats)
	}
	calls := toolCalls(trace)
	names := callNames(calls)
	p := spec.Period
	for start := 0; start+p*minRepeats <= len(names); start++ {
		block := names[start : start+p]
		if !primitive(block) {
			continue
		}
		repeats := 1
		for next := start + p; next+p <= len(names) && reflect.DeepEqual(names[next:next+p], block); next += p {
			repeats++
		}
		if repeats >= minRepeats {
			end := start + p*repeats - 1
			return checkOutcome{explanation: fmt.Sprintf(
				"tool cycle of period %d repeated %d× at steps %d-%d: %s (min_repeats %d)",
				p, repeats, calls[start].index, calls[end].index, strings.Join(block, " → "), minRepeats,
			)}, nil
		}
	}
	return checkOutcome{passed: true, explanation: fmt.Sprintf("no tool cycle of period %d repeated %d× or more", p, minRepeats)}, nil
}

// ── failure_streak ──

func runFailureStreak(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Max == nil || *spec.Max < 0 {
		return checkOutcome{}, fmt.Errorf("failure_streak requires 'max' >= 0")
	}
	if trace.SchemaVersion < types.TraceSchemaVersion2 {
		return checkOutcome{missing: fmt.Sprintf(
			"failure_streak needs trace schema_version %d step errors; trace has schema_version %d",
			types.TraceSchemaVersion2, trace.SchemaVersion,
		)}, nil
	}
	var longest, current []indexedStep
	for _, c := range toolCalls(trace) {
		if c.step.Error == nil {
			current = nil
			continue
		}
		current = append(current, c)
		if len(current) > len(longest) {
			longest = append([]indexedStep(nil), current...)
		}
	}
	if len(longest) > *spec.Max {
		return checkOutcome{explanation: fmt.Sprintf(
			"%d consecutive tool failures at steps %s (max %d): %s",
			len(longest), stepIndices(longest), *spec.Max, strings.Join(callNames(longest), ", "),
		)}, nil
	}
	return checkOutcome{passed: true, explanation: fmt.Sprintf("longest tool failure streak %d (max %d)", len(longest), *spec.Max)}, nil
}

// ── arg_predicate ──

var argPredicateOps = map[string]bool{
	"eq": true, "ne": true, "lt": true, "lte": true, "gt": true, "gte": true, "in": true, "not_in": true,
}

func runArgPredicate(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Tool == "" || spec.Arg == "" || spec.Op == "" || len(spec.Value) == 0 {
		return checkOutcome{}, fmt.Errorf("arg_predicate requires 'tool', 'arg', 'op' and 'value'")
	}
	if !argPredicateOps[spec.Op] {
		return checkOutcome{}, fmt.Errorf("arg_predicate 'op' must be one of eq, ne, lt, lte, gt, gte, in, not_in, got %q", spec.Op)
	}
	var want any
	if err := json.Unmarshal(spec.Value, &want); err != nil {
		return checkOutcome{}, fmt.Errorf("arg_predicate 'value' is not valid JSON: %v", err)
	}
	if _, isNum := want.(float64); !isNum && isNumericOp(spec.Op) {
		return checkOutcome{}, fmt.Errorf("arg_predicate op %q requires a numeric 'value'", spec.Op)
	}
	if _, isList := want.([]any); !isList && (spec.Op == "in" || spec.Op == "not_in") {
		return checkOutcome{}, fmt.Errorf("arg_predicate op %q requires an array 'value'", spec.Op)
	}

	calls := toolCallsNamed(trace, spec.Tool)
	rule := fmt.Sprintf("%s %s %s", spec.Arg, spec.Op, displayJSON(spec.Value))
	var violations, missing []string
	for _, c := range calls {
		raw, ok := resolveArg(c.step, spec.Arg)
		if !ok {
			missing = append(missing, fmt.Sprint(c.index))
			continue
		}
		var got any
		if err := json.Unmarshal(raw, &got); err != nil {
			return checkOutcome{}, fmt.Errorf("step %d argument %q is not valid JSON: %v", c.index, spec.Arg, err)
		}
		if !argSatisfies(got, spec.Op, want) {
			violations = append(violations, fmt.Sprintf("step %d: %s=%s", c.index, spec.Arg, displayJSON(raw)))
		}
	}
	if len(violations) > 0 {
		return checkOutcome{explanation: fmt.Sprintf("%s violates %s at %s", spec.Tool, rule, strings.Join(violations, "; "))}, nil
	}
	if len(missing) > 0 {
		return checkOutcome{missing: fmt.Sprintf("%s call(s) at step(s) %s have no argument %q", spec.Tool, strings.Join(missing, ", "), spec.Arg)}, nil
	}
	if len(calls) == 0 {
		return checkOutcome{passed: true, explanation: fmt.Sprintf("%s not called; %s holds vacuously", spec.Tool, rule)}, nil
	}
	return checkOutcome{passed: true, explanation: fmt.Sprintf("%s satisfies %s on all %d call(s)", spec.Tool, rule, len(calls))}, nil
}

func isNumericOp(op string) bool {
	return op == "lt" || op == "lte" || op == "gt" || op == "gte"
}

// argSatisfies applies op to a decoded argument. Numeric operators fail on a
// non-numeric argument; equality is JSON value equality.
func argSatisfies(got any, op string, want any) bool {
	switch op {
	case "eq":
		return reflect.DeepEqual(got, want)
	case "ne":
		return !reflect.DeepEqual(got, want)
	case "in", "not_in":
		found := false
		for _, item := range want.([]any) {
			if reflect.DeepEqual(got, item) {
				found = true
				break
			}
		}
		return found == (op == "in")
	}
	g, ok := got.(float64)
	if !ok {
		return false
	}
	w := want.(float64)
	switch op {
	case "lt":
		return g < w
	case "lte":
		return g <= w
	case "gt":
		return g > w
	default:
		return g >= w
	}
}
