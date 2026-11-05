package assertion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// contractCall is a test step: a tool call unless typ is set.
type contractCall struct {
	name string
	args string
	err  bool
	typ  string
}

func contractTrace(calls ...contractCall) *types.Trace {
	steps := make([]types.Step, len(calls))
	for i, c := range calls {
		typ := c.typ
		if typ == "" {
			typ = types.StepTypeToolCall
		}
		args := c.args
		if args == "" {
			args = `{}`
		}
		steps[i] = types.Step{Type: typ, Name: c.name, Args: json.RawMessage(args)}
		if c.err {
			steps[i].Error = &types.StepError{Type: "timeout"}
		}
	}
	return &types.Trace{
		SchemaVersion: types.TraceSchemaVersion2,
		TraceID:       "trc_contract",
		Output:        json.RawMessage(`{"message":"ok"}`),
		Steps:         steps,
	}
}

func tools(names ...string) []contractCall {
	calls := make([]contractCall, len(names))
	for i, n := range names {
		calls[i] = contractCall{name: n}
	}
	return calls
}

func evalTraceSpec(t *testing.T, trace *types.Trace, spec string) *types.AssertionResult {
	t.Helper()
	return (&TraceEvaluator{}).Evaluate(trace, &types.Assertion{
		AssertionID: "assert_contract",
		Type:        types.TypeTrace,
		Spec:        json.RawMessage(spec),
	})
}

type contractCase struct {
	name        string
	trace       *types.Trace
	spec        string
	wantStatus  string
	wantExplain string // substring of the explanation; empty skips the check
}

func runContractCases(t *testing.T, cases []contractCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evalTraceSpec(t, tc.trace, tc.spec)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; explanation: %s", got.Status, tc.wantStatus, got.Explanation)
			}
			if tc.wantExplain != "" && !strings.Contains(got.Explanation, tc.wantExplain) {
				t.Fatalf("explanation %q does not contain %q", got.Explanation, tc.wantExplain)
			}
			if tc.wantStatus == types.StatusNotEvaluated && got.Reason != types.ReasonMissingEvidence {
				t.Fatalf("reason = %q, want %q", got.Reason, types.ReasonMissingEvidence)
			}
		})
	}
}

func TestToolCardinality(t *testing.T) {
	refund := func(order string) contractCall {
		return contractCall{name: "issue_refund", args: `{"order_id":"` + order + `","amount":5}`}
	}
	runContractCases(t, []contractCase{
		{"per key passes with distinct keys", contractTrace(refund("O-1"), refund("O-2")),
			`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`,
			types.StatusPass, "for each of 2 order_id value(s)"},
		{"per key fails on repeated key", contractTrace(refund("O-1"), refund("O-2"), refund("O-1")),
			`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`,
			types.StatusHardFail, "issue_refund called 2× for order_id=O-1 (max 1)"},
		{"per key reports every violating key", contractTrace(refund("O-1"), refund("O-2"), refund("O-1"), refund("O-2")),
			`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`,
			types.StatusHardFail, "for order_id=O-1 (max 1); issue_refund called 2× for order_id=O-2 (max 1)"},
		{"non-string key renders as JSON", contractTrace(
			contractCall{name: "pay", args: `{"id":7}`}, contractCall{name: "pay", args: `{"id":7}`}),
			`{"check":"tool_cardinality","tool":"pay","per":"id","max":1}`,
			types.StatusHardFail, "pay called 2× for id=7 (max 1)"},
		{"nested key path", contractTrace(
			contractCall{name: "pay", args: `{"order":{"id":"A"}}`}, contractCall{name: "pay", args: `{"order":{"id":"A"}}`}),
			`{"check":"tool_cardinality","tool":"pay","per":"order.id","max":1}`,
			types.StatusHardFail, "for order.id=A"},
		{"missing key is not evaluated", contractTrace(refund("O-1"), contractCall{name: "issue_refund", args: `{"amount":5}`}),
			`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`,
			types.StatusNotEvaluated, `have no argument "order_id"`},
		{"violation wins over missing key", contractTrace(refund("O-1"), refund("O-1"), contractCall{name: "issue_refund"}),
			`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`,
			types.StatusHardFail, "called 2× for order_id=O-1"},
		{"without key counts all calls", contractTrace(tools("search", "search", "search")...),
			`{"check":"tool_cardinality","tool":"search","max":2}`,
			types.StatusHardFail, "search called 3× (max 2)"},
		{"min fails when never called", contractTrace(tools("search")...),
			`{"check":"tool_cardinality","tool":"lookup","min":1}`,
			types.StatusHardFail, "lookup called 0× (min 1)"},
		{"min and max pass in range", contractTrace(tools("search", "search")...),
			`{"check":"tool_cardinality","tool":"search","min":1,"max":2}`,
			types.StatusPass, "(min 1, max 2)"},
		{"ignores non-tool steps", contractTrace(contractCall{name: "search", typ: types.StepTypeLLMCall}, contractCall{name: "search"}),
			`{"check":"tool_cardinality","tool":"search","max":1}`,
			types.StatusPass, ""},
		{"soft failure", contractTrace(tools("search", "search")...),
			`{"check":"tool_cardinality","tool":"search","max":1,"soft":true}`,
			types.StatusSoftFail, ""},
		{"requires a bound", contractTrace(), `{"check":"tool_cardinality","tool":"x"}`, types.StatusHardFail, "requires 'min' or 'max'"},
		{"requires tool", contractTrace(), `{"check":"tool_cardinality","max":1}`, types.StatusHardFail, "requires 'tool'"},
		{"rejects negative bound", contractTrace(), `{"check":"tool_cardinality","tool":"x","max":-1}`, types.StatusHardFail, "must be >= 0"},
		{"rejects min above max", contractTrace(), `{"check":"tool_cardinality","tool":"x","min":3,"max":1}`, types.StatusHardFail, "exceeds 'max'"},
	})
}

func TestLoopDetectionKeepsAllStepSemantics(t *testing.T) {
	trace := contractTrace(contractCall{name: "fetch", typ: types.StepTypeLLMCall}, contractCall{name: "fetch"})
	runContractCases(t, []contractCase{
		{"counts every step type", trace, `{"check":"loop_detection","tool":"fetch","max_repetitions":1}`,
			types.StatusHardFail, "fetch called 2× (max 1)"},
		{"within limit", trace, `{"check":"loop_detection","tool":"fetch","max_repetitions":2}`, types.StatusPass, ""},
	})
}

func TestTrajectory(t *testing.T) {
	abc := contractTrace(
		contractCall{name: "plan", typ: types.StepTypeLLMCall},
		contractCall{name: "a"}, contractCall{name: "b"}, contractCall{name: "c"},
	)
	spec := func(mode, list string) string {
		return `{"check":"trajectory","mode":"` + mode + `","tools":` + list + `}`
	}
	runContractCases(t, []contractCase{
		{"strict passes ignoring model calls", abc, spec("strict", `["a","b","c"]`), types.StatusPass, ""},
		{"strict fails on order", abc, spec("strict", `["a","c","b"]`), types.StatusHardFail, "differ from expected"},
		{"strict fails on extra call", abc, spec("strict", `["a","b"]`), types.StatusHardFail, ""},
		{"unordered passes on permutation", abc, spec("unordered", `["c","a","b"]`), types.StatusPass, ""},
		{"unordered counts multiplicity", abc, spec("unordered", `["a","a","b","c"]`), types.StatusHardFail, "not a permutation"},
		{"subset passes when calls within expected", abc, spec("subset", `["a","b","c","d"]`), types.StatusPass, ""},
		{"subset fails on unexpected call", abc, spec("subset", `["a","b"]`), types.StatusHardFail, "not in the expected trajectory: c (1 vs 0)"},
		{"superset passes when expected all called", abc, spec("superset", `["a","c"]`), types.StatusPass, ""},
		{"superset fails on missing call", abc, spec("superset", `["a","d"]`), types.StatusHardFail, "missing: d (1 vs 0)"},
		{"superset counts multiplicity", abc, spec("superset", `["a","a"]`), types.StatusHardFail, "missing: a (2 vs 1)"},
		{"rejects unknown mode", abc, spec("fuzzy", `["a"]`), types.StatusHardFail, "'mode' must be"},
		{"rejects missing mode", abc, `{"check":"trajectory","tools":["a"]}`, types.StatusHardFail, "'mode' must be"},
		{"requires tools", abc, spec("strict", `[]`), types.StatusHardFail, "requires 'tools'"},
		{"rejects order outside partial_order", abc, `{"check":"trajectory","mode":"strict","tools":["a"],"order":[["a","b"]]}`,
			types.StatusHardFail, "only to mode partial_order"},
	})
}

func TestTrajectoryPartialOrder(t *testing.T) {
	trace := contractTrace(tools("auth", "lookup", "verify", "refund")...)
	spec := func(rest string) string { return `{"check":"trajectory","mode":"partial_order",` + rest + `}` }
	runContractCases(t, []contractCase{
		{"edges hold with extra calls", trace, spec(`"order":[["auth","refund"],["verify","refund"]]`), types.StatusPass, ""},
		{"edge broken", trace, spec(`"order":[["refund","lookup"]]`), types.StatusHardFail, "lookup called at step 1 before refund at step 3"},
		{"edge endpoint absent", trace, spec(`"order":[["escalate","refund"]]`), types.StatusHardFail, "tools not called: escalate"},
		{"required tools absent", trace, spec(`"tools":["notify"],"order":[["auth","refund"]]`), types.StatusHardFail, "tools not called: notify"},
		{"requires order", trace, spec(`"tools":["auth"]`), types.StatusHardFail, "requires 'order'"},
		{"rejects malformed edge", trace, spec(`"order":[["auth"]]`), types.StatusHardFail, "[before, after] pairs"},
		{"rejects self edge", trace, spec(`"order":[["auth","auth"]]`), types.StatusHardFail, "distinct tools"},
	})
}

func TestToolOrder(t *testing.T) {
	spec := `{"check":"tool_order","before":"verify_identity","after":"issue_refund"}`
	runContractCases(t, []contractCase{
		{"passes when before comes first", contractTrace(tools("verify_identity", "issue_refund", "issue_refund")...), spec, types.StatusPass, ""},
		{"fails when after comes first", contractTrace(tools("issue_refund", "verify_identity")...), spec,
			types.StatusHardFail, "issue_refund called at step 0 before verify_identity at step 1"},
		{"fails when before never called", contractTrace(tools("lookup", "issue_refund")...), spec,
			types.StatusHardFail, "issue_refund called at step 1 without a prior verify_identity"},
		{"vacuous when after never called", contractTrace(tools("lookup")...), spec, types.StatusPass, "issue_refund not called"},
		{"model step with the name does not count", contractTrace(
			contractCall{name: "verify_identity", typ: types.StepTypeLLMCall}, contractCall{name: "issue_refund"}), spec,
			types.StatusHardFail, "without a prior verify_identity"},
		{"requires both tools", contractTrace(), `{"check":"tool_order","before":"a"}`, types.StatusHardFail, "requires 'before' and 'after'"},
		{"rejects same tool", contractTrace(), `{"check":"tool_order","before":"a","after":"a"}`, types.StatusHardFail, "must differ"},
	})
}

func TestCycle(t *testing.T) {
	abab := contractTrace(tools("A", "B", "A", "B")...)
	runContractCases(t, []contractCase{
		{"A B A B fails period 2", abab, `{"check":"cycle","period":2}`,
			types.StatusHardFail, "tool cycle of period 2 repeated 2× at steps 0-3: A → B (min_repeats 2)"},
		{"A B A B passes period 1", abab, `{"check":"cycle","period":1}`, types.StatusPass, ""},
		{"A B A B passes min_repeats 3", abab, `{"check":"cycle","period":2,"min_repeats":3}`, types.StatusPass, ""},
		{"period 1 repeat", contractTrace(tools("x", "A", "A")...), `{"check":"cycle","period":1}`,
			types.StatusHardFail, "period 1 repeated 2× at steps 1-2: A"},
		{"period 3", contractTrace(tools("A", "B", "C", "A", "B", "C", "A")...), `{"check":"cycle","period":3}`,
			types.StatusHardFail, "A → B → C"},
		{"period 4", contractTrace(tools("A", "B", "C", "D", "A", "B", "C", "D")...), `{"check":"cycle","period":4}`,
			types.StatusHardFail, "period 4 repeated 2×"},
		{"counts full run length", contractTrace(tools("A", "B", "A", "B", "A", "B")...), `{"check":"cycle","period":2}`,
			types.StatusHardFail, "repeated 3× at steps 0-5"},
		{"period 2 ignores a period 1 run", contractTrace(tools("A", "A", "A", "A")...), `{"check":"cycle","period":2}`, types.StatusPass, ""},
		{"model calls between tool calls are skipped", contractTrace(
			contractCall{name: "A"}, contractCall{name: "think", typ: types.StepTypeLLMCall},
			contractCall{name: "B"}, contractCall{name: "A"}, contractCall{name: "B"}), `{"check":"cycle","period":2}`,
			types.StatusHardFail, "at steps 0-4"},
		{"no cycle", contractTrace(tools("A", "B", "C", "D")...), `{"check":"cycle","period":2}`, types.StatusPass, ""},
		{"rejects period 0", abab, `{"check":"cycle"}`, types.StatusHardFail, "between 1 and 4"},
		{"rejects period 5", abab, `{"check":"cycle","period":5}`, types.StatusHardFail, "between 1 and 4"},
		{"rejects min_repeats 1", abab, `{"check":"cycle","period":2,"min_repeats":1}`, types.StatusHardFail, "'min_repeats' must be >= 2"},
	})
}

func TestFailureStreak(t *testing.T) {
	trace := contractTrace(
		contractCall{name: "a", err: true}, contractCall{name: "b"},
		contractCall{name: "c", err: true}, contractCall{name: "think", typ: types.StepTypeLLMCall},
		contractCall{name: "d", err: true}, contractCall{name: "e", err: true},
	)
	v1 := contractTrace(contractCall{name: "a"})
	v1.SchemaVersion = 1
	runContractCases(t, []contractCase{
		{"streak above max fails", trace, `{"check":"failure_streak","max":2}`,
			types.StatusHardFail, "3 consecutive tool failures at steps 2, 4, 5 (max 2): c, d, e"},
		{"streak at max passes", trace, `{"check":"failure_streak","max":3}`, types.StatusPass, "longest tool failure streak 3 (max 3)"},
		{"max 0 forbids any failure", trace, `{"check":"failure_streak","max":0}`, types.StatusHardFail, "3 consecutive tool failures at steps 2, 4, 5 (max 0)"},
		{"no failures pass", contractTrace(tools("a", "b")...), `{"check":"failure_streak","max":0}`, types.StatusPass, "streak 0"},
		{"v1 trace is not evaluated", v1, `{"check":"failure_streak","max":1}`, types.StatusNotEvaluated, "schema_version 2"},
		{"requires max", trace, `{"check":"failure_streak"}`, types.StatusHardFail, "requires 'max' >= 0"},
		{"rejects negative max", trace, `{"check":"failure_streak","max":-1}`, types.StatusHardFail, "requires 'max' >= 0"},
	})
}

func TestArgPredicate(t *testing.T) {
	trace := contractTrace(
		contractCall{name: "refund", args: `{"amount":50,"currency":"USD","meta":{"tier":"gold"}}`},
		contractCall{name: "refund", args: `{"amount":500,"currency":"EUR","meta":{"tier":"basic"}}`},
	)
	spec := func(arg, op, value string) string {
		return `{"check":"arg_predicate","tool":"refund","arg":"` + arg + `","op":"` + op + `","value":` + value + `}`
	}
	runContractCases(t, []contractCase{
		{"lte passes", trace, spec("amount", "lte", `500`), types.StatusPass, "on all 2 call(s)"},
		{"lt fails", trace, spec("amount", "lt", `500`), types.StatusHardFail, "refund violates amount lt 500 at step 1: amount=500"},
		{"gt fails", trace, spec("amount", "gt", `50`), types.StatusHardFail, "step 0: amount=50"},
		{"gte passes", trace, spec("amount", "gte", `50`), types.StatusPass, ""},
		{"eq string fails", trace, spec("currency", "eq", `"USD"`), types.StatusHardFail, "step 1: currency=EUR"},
		{"ne passes", trace, spec("currency", "ne", `"GBP"`), types.StatusPass, ""},
		{"in passes", trace, spec("currency", "in", `["USD","EUR"]`), types.StatusPass, ""},
		{"not_in fails", trace, spec("currency", "not_in", `["EUR"]`), types.StatusHardFail, "step 1"},
		{"nested path", trace, spec("meta.tier", "eq", `"gold"`), types.StatusHardFail, "step 1: meta.tier=basic"},
		{"numeric op on string argument fails", trace, spec("currency", "lt", `1`), types.StatusHardFail, "currency=USD"},
		{"missing argument is not evaluated", trace, spec("note", "eq", `"x"`), types.StatusNotEvaluated, `have no argument "note"`},
		{"tool never called passes", contractTrace(tools("lookup")...), spec("amount", "lte", `1`), types.StatusPass, "vacuously"},
		{"rejects unknown op", trace, spec("amount", "approx", `1`), types.StatusHardFail, "'op' must be one of"},
		{"rejects non-numeric value for lt", trace, spec("amount", "lt", `"1"`), types.StatusHardFail, "requires a numeric 'value'"},
		{"rejects non-array value for in", trace, spec("amount", "in", `1`), types.StatusHardFail, "requires an array 'value'"},
		{"requires fields", trace, `{"check":"arg_predicate","tool":"refund"}`, types.StatusHardFail, "requires 'tool', 'arg', 'op' and 'value'"},
	})
}

func loadContractTrace(t *testing.T, path string) *types.Trace {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol-tests", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var trace types.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return &trace
}

// TestINC1042Contracts runs the refund-desk incident contract over the frozen
// Python and TypeScript traces (the second issue_refund retries the first)
// and over the corrected fixture, which re-checks the order instead.
func TestINC1042Contracts(t *testing.T) {
	const cardinalitySpec = `{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`
	for _, path := range []string{"trace-v2/otlp/inc-1042-refund.trace.json", "contracts/traces/inc-1042-ts.trace.json"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			got := evalTraceSpec(t, loadContractTrace(t, path), cardinalitySpec)
			if got.Status != types.StatusHardFail {
				t.Fatalf("status = %q, want hard_fail; explanation: %s", got.Status, got.Explanation)
			}
			if want := "issue_refund called 2× for order_id=O-1042 (max 1)"; got.Explanation != want {
				t.Fatalf("explanation = %q, want %q", got.Explanation, want)
			}
		})
	}
	t.Run("corrected", func(t *testing.T) {
		trace := loadContractTrace(t, "contracts/traces/inc-1042-corrected.trace.json")
		for _, spec := range []string{
			cardinalitySpec,
			`{"check":"tool_order","before":"lookup_order","after":"issue_refund"}`,
			`{"check":"failure_streak","max":1}`,
		} {
			if got := evalTraceSpec(t, trace, spec); got.Status != types.StatusPass {
				t.Fatalf("%s: status = %q, want pass; explanation: %s", spec, got.Status, got.Explanation)
			}
		}
	})
}

func TestContractArgumentValues(t *testing.T) {
	call := func(name, args string) contractCall { return contractCall{name: name, args: args} }
	cardinality := `{"check":"tool_cardinality","tool":"r","per":"id","max":1}`
	runContractCases(t, []contractCase{
		{"string and number keys stay apart", contractTrace(call("r", `{"id":"7"}`), call("r", `{"id":7}`)),
			cardinality, types.StatusPass, "for each of 2 id value(s)"},
		{"equal numbers merge", contractTrace(call("r", `{"id":7}`), call("r", `{"id":7.0}`)),
			cardinality, types.StatusHardFail, "r called 2× for id=7 (max 1)"},
		{"large integers stay apart", contractTrace(call("r", `{"id":9007199254740993}`), call("r", `{"id":9007199254740992}`)),
			cardinality, types.StatusPass, ""},
		{"object keys group regardless of key order", contractTrace(call("r", `{"id":{"a":1,"b":2}}`), call("r", `{"id":{"b":2,"a":1}}`)),
			cardinality, types.StatusHardFail, `for id={"a":1,"b":2}`},
		{"null key is missing evidence", contractTrace(call("r", `{"id":null}`), call("r", `{"id":null}`)),
			cardinality, types.StatusNotEvaluated, `have no argument "id"`},
		{"per with min fails when never called", contractTrace(tools("x")...),
			`{"check":"tool_cardinality","tool":"r","per":"id","min":1}`, types.StatusHardFail, "r called 0× (min 1)"},
		{"per with max passes when never called", contractTrace(tools("x")...), cardinality, types.StatusPass, ""},
		{"null argument is missing evidence", contractTrace(call("pay", `{"amount":null}`)),
			`{"check":"arg_predicate","tool":"pay","arg":"amount","op":"lt","value":10}`, types.StatusNotEvaluated, `no argument "amount"`},
		{"eq is exact on large integers", contractTrace(call("pay", `{"n":9007199254740993}`)),
			`{"check":"arg_predicate","tool":"pay","arg":"n","op":"eq","value":9007199254740992}`, types.StatusHardFail, "n=9007199254740993"},
		{"eq treats 1 and 1.0 as equal", contractTrace(call("pay", `{"n":1.0}`)),
			`{"check":"arg_predicate","tool":"pay","arg":"n","op":"eq","value":1}`, types.StatusPass, ""},
		{"lt is exact on decimals", contractTrace(call("pay", `{"n":0.30000000000000001}`)),
			`{"check":"arg_predicate","tool":"pay","arg":"n","op":"lt","value":0.3}`, types.StatusHardFail, ""},
		{"in compares strings and numbers apart", contractTrace(call("pay", `{"n":"1"}`)),
			`{"check":"arg_predicate","tool":"pay","arg":"n","op":"in","value":[1]}`, types.StatusHardFail, "n=1"},
	})
}

func TestTrajectoryPartialOrderRejectsContradictoryEdges(t *testing.T) {
	trace := contractTrace(tools("a", "b", "c")...)
	runContractCases(t, []contractCase{
		{"two-edge cycle", trace, `{"check":"trajectory","mode":"partial_order","order":[["a","b"],["b","a"]]}`,
			types.StatusHardFail, "contradictory: a → b → a"},
		{"three-edge cycle", trace, `{"check":"trajectory","mode":"partial_order","order":[["a","b"],["b","c"],["c","a"]]}`,
			types.StatusHardFail, "contradictory: a → b → c → a"},
		{"diamond is a partial order", trace, `{"check":"trajectory","mode":"partial_order","order":[["a","b"],["a","c"],["b","c"]]}`,
			types.StatusPass, ""},
	})
}
