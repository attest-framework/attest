package contractdsl_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/contractdsl"
)

const contractsDir = "../../../protocol-tests/contracts/"

type dslCase struct {
	Name   string `json:"name"`
	Python struct {
		Method string         `json:"method"`
		Args   []any          `json:"args"`
		Kwargs map[string]any `json:"kwargs"`
	} `json:"python"`
	TypeScript struct {
		Method string `json:"method"`
		Args   []any  `json:"args"`
	} `json:"typescript"`
	Spec map[string]any `json:"spec"`
}

func loadCases(t *testing.T, file string) []dslCase {
	t.Helper()
	data, err := os.ReadFile(contractsDir + file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []dslCase `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatalf("%s has no cases", file)
	}
	return doc.Cases
}

// plain converts json.Number to int64 or float64, the types the parser yields.
func plain(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return f
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plain(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = plain(e)
		}
		return out
	}
	return v
}

// norm round-trips v through JSON so int64 and float64 compare by value.
func norm(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// pyLiteral is a test-local Python literal writer, independent of the package.
func pyLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case string:
		b, _ := json.Marshal(x)
		return string(b)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyLiteral(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyLiteral(k) + ": " + pyLiteral(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	panic(fmt.Sprintf("literal %T", v))
}

func caseStatement(c dslCase) string {
	var args []string
	for _, a := range c.Python.Args {
		args = append(args, pyLiteral(a))
	}
	keys := make([]string, 0, len(c.Python.Kwargs))
	for k := range c.Python.Kwargs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k+"="+pyLiteral(c.Python.Kwargs[k]))
	}
	return c.Python.Method + "(" + strings.Join(args, ", ") + ")"
}

func TestParse_DSLCases_RoundTripToSpecAndBothLanguages(t *testing.T) {
	var cases []dslCase
	cases = append(cases, loadCases(t, "dsl.json")...)
	cases = append(cases, loadCases(t, "effect-dsl.json")...)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			stmt := caseStatement(c)

			call, err := contractdsl.Parse(stmt)
			if err != nil {
				t.Fatalf("Parse(%s): %v", stmt, err)
			}
			if got, want := norm(t, call.Spec()), norm(t, c.Spec); !reflect.DeepEqual(got, want) {
				t.Errorf("spec of %s\n got %v\nwant %v", stmt, got, want)
			}

			py := call.Python()
			if py.Method != c.Python.Method {
				t.Errorf("python method %q, want %q", py.Method, c.Python.Method)
			}
			if got, want := norm(t, py.Args), norm(t, plain(c.Python.Args)); !reflect.DeepEqual(got, want) {
				t.Errorf("python args %v, want %v", got, want)
			}
			if got, want := norm(t, py.Kwargs), norm(t, plain(orEmpty(c.Python.Kwargs))); !reflect.DeepEqual(got, want) {
				t.Errorf("python kwargs %v, want %v", got, want)
			}

			ts := call.TypeScript()
			if ts.Method != c.TypeScript.Method {
				t.Errorf("typescript method %q, want %q", ts.Method, c.TypeScript.Method)
			}
			if got, want := norm(t, ts.Args), norm(t, plain(c.TypeScript.Args)); !reflect.DeepEqual(got, want) {
				t.Errorf("typescript args %v, want %v", got, want)
			}

			// The rendered statement parses back to the same spec.
			text, err := call.Statement()
			if err != nil {
				t.Fatal(err)
			}
			again, err := contractdsl.Parse(text)
			if err != nil {
				t.Fatalf("Parse(rendered %s): %v", text, err)
			}
			if got, want := norm(t, again.Spec()), norm(t, c.Spec); !reflect.DeepEqual(got, want) {
				t.Errorf("re-parsed spec %v, want %v", got, want)
			}

			// And the spec maps back to a call with the same spec.
			back, err := contractdsl.FromSpec(call.Type(), norm(t, c.Spec).(map[string]any))
			if err != nil {
				t.Fatalf("FromSpec: %v", err)
			}
			if got, want := norm(t, back.Spec()), norm(t, c.Spec); !reflect.DeepEqual(got, want) {
				t.Errorf("FromSpec spec %v, want %v", got, want)
			}
		})
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func TestParse_TypeScriptMethodName_ResolvesToSameCall(t *testing.T) {
	call, err := contractdsl.Parse(`toolCardinality("issue_refund", per="order_id", atMost=1)`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"check": "tool_cardinality", "tool": "issue_refund", "per": "order_id", "max": float64(1), "soft": false}
	if got := norm(t, call.Spec()); !reflect.DeepEqual(got, any(want)) {
		t.Errorf("spec %v, want %v", got, want)
	}
}

func TestParse_Literals_AcceptedForms(t *testing.T) {
	cases := []struct{ name, stmt string }{
		{"single quotes", `forbidden_tools(['a', 'b'])`},
		{"escapes", `forbidden_tools(["a\tb\n\\ \" \'"])`},
		{"lowercase literals", `tool_arg_satisfies("t", "a", "eq", null)`},
		{"python literals", `tool_arg_satisfies("t", "a", "eq", None, soft=True)`},
		{"float exponent", `cost_under(1e-3)`},
		{"negative", `tool_arg_satisfies("t", "a", "gt", -3)`},
		{"nested value", `tool_arg_satisfies("t", "a", "in", [[1, 2], {"k": [true]}])`},
		{"trailing comma and newlines", "forbidden_tools(\n  [\"a\"],\n  soft=False,\n)"},
		{"unknown filter on effect", `count("refunds", equals=1, order_id="O-1")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := contractdsl.Parse(c.stmt); err != nil {
				t.Errorf("Parse(%q): %v", c.stmt, err)
			}
		})
	}
}

func TestParse_ArbitraryCode_Rejected(t *testing.T) {
	deep := strings.Repeat("[", contractdsl.MaxDepth+1) + strings.Repeat("]", contractdsl.MaxDepth+1)
	cases := []struct{ name, stmt string }{
		{"import call", `__import__("os")`},
		{"attribute call", `os.system("x")`},
		{"identifier value", `tool_cardinality(x)`},
		{"call in argument", `tool_cardinality("a", per=open("f"))`},
		{"arithmetic", `1+1`},
		{"arithmetic in argument", `cost_under(1+1)`},
		{"attribute chain", `a.b.c()`},
		{"f-string", `f"x"`},
		{"f-string argument", `forbidden_tools([f"x"])`},
		{"lambda", `lambda: 1`},
		{"lambda argument", `tool_cardinality("a", per=lambda x: x)`},
		{"import statement", `import os`},
		{"two statements", `forbidden_tools(["a"]); forbidden_tools(["b"])`},
		{"trailing code", `forbidden_tools(["a"]) and 1`},
		{"comment", `forbidden_tools(["a"]) # x`},
		{"comment inside", "forbidden_tools([\"a\"], # x\n soft=True)"},
		{"unknown method", `eval("1")`},
		{"unknown keyword", `tool_cardinality("a", at_most=1, bogus=2)`},
		{"duplicate keyword", `tool_cardinality("a", at_most=1, at_most=2)`},
		{"positional and keyword", `tool_cardinality("a", tool="b", at_most=1)`},
		{"positional after keyword", `tool_cardinality(at_most=1, "a")`},
		{"too many positional", `tool_cardinality("a", "b")`},
		{"missing argument", `tool_cardinality(at_most=1)`},
		{"missing bound", `tool_cardinality("a")`},
		{"wrong type", `tool_cardinality("a", at_most="1")`},
		{"bool as int", `tool_cardinality("a", at_most=True)`},
		{"bad enum", `trajectory(["a"], mode="loose")`},
		{"effect bound conflict", `count("r", equals=1, at_most=2)`},
		{"duplicate filter", `count("r", equals=1, where={"a": 1}, a=1)`},
		{"duplicate map key", `tool_arg_satisfies("t", "a", "eq", {"k": 1, "k": 2})`},
		{"non-string map key", `tool_arg_satisfies("t", "a", "eq", {1: 1})`},
		{"set literal", `forbidden_tools({"a"})`},
		{"tuple", `forbidden_tools(("a",))`},
		{"comprehension", `forbidden_tools([x for x in "a"])`},
		{"bad escape", `forbidden_tools(["\x41"])`},
		{"unterminated string", `forbidden_tools(["a])`},
		{"raw newline in string", "forbidden_tools([\"a\nb\"])"},
		{"unclosed call", `forbidden_tools(["a"]`},
		{"empty", ``},
		{"whitespace", `   `},
		{"no call", `forbidden_tools`},
		{"hex number", `cost_under(0x10)`},
		{"integer overflow", `no_tool_cycles(99999999999999999999)`},
		{"deep nesting", `tool_arg_satisfies("t", "a", "eq", ` + deep + `)`},
		{"oversized", `forbidden_tools(["` + strings.Repeat("a", contractdsl.MaxStatementBytes) + `"])`},
		{"non-ascii identifier", `forbidden_tools(é)`},
		{"nul byte", "forbidden_tools([\"a\x00\"])"},
		{"splat", `forbidden_tools(*x)`},
		{"double splat", `forbidden_tools(**x)`},
		{"walrus", `forbidden_tools(x := 1)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			call, err := contractdsl.Parse(c.stmt)
			if err == nil {
				t.Fatalf("Parse(%.60q) = %+v, want an error", c.stmt, call)
			}
			var syn *contractdsl.SyntaxError
			if e, ok := err.(*contractdsl.SyntaxError); ok {
				syn = e
				if syn.Pos < 1 {
					t.Errorf("position %d, want >= 1", syn.Pos)
				}
			} else {
				t.Errorf("error %T, want *SyntaxError", err)
			}
		})
	}
}

func TestParse_Error_ReportsPositionOfOffendingToken(t *testing.T) {
	_, err := contractdsl.Parse(`tool_cardinality("a", per=open("f"))`)
	syn, ok := err.(*contractdsl.SyntaxError)
	if !ok {
		t.Fatalf("error %T", err)
	}
	if syn.Pos != 31 {
		t.Errorf("position %d, want 31 (the opening parenthesis of open)", syn.Pos)
	}
	if !strings.Contains(syn.Msg, "calls are not allowed") {
		t.Errorf("message %q", syn.Msg)
	}
}

func TestMaxDepth_AtLimit_Accepted(t *testing.T) {
	// The value is nested MaxDepth levels deep.
	v := strings.Repeat("[", contractdsl.MaxDepth) + strings.Repeat("]", contractdsl.MaxDepth)
	if _, err := contractdsl.Parse(`tool_arg_satisfies("t", "a", "eq", ` + v + `)`); err != nil {
		t.Errorf("depth %d: %v", contractdsl.MaxDepth, err)
	}
}

func TestFromSpec_DraftedContracts_RenderParseableStatements(t *testing.T) {
	specs := []struct {
		typ  string
		spec string
	}{
		{"trace", `{"check":"trajectory","mode":"superset","tools":["lookup_order","issue_refund"],"soft":false}`},
		{"trace", `{"check":"tool_order","before":"lookup_order","after":"issue_refund","soft":false}`},
		{"trace", `{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1,"soft":false}`},
		{"schema", `{"target":"output.structured","schema":{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}}`},
		{"constraint", `{"field":"metadata.cost_usd","operator":"lte","value":0.0525,"soft":false}`},
	}
	for _, s := range specs {
		var spec map[string]any
		if err := json.Unmarshal([]byte(s.spec), &spec); err != nil {
			t.Fatal(err)
		}
		call, err := contractdsl.FromSpec(s.typ, spec)
		if err != nil {
			t.Fatalf("FromSpec(%s): %v", s.spec, err)
		}
		stmt, err := call.Statement()
		if err != nil {
			t.Fatal(err)
		}
		again, err := contractdsl.Parse(stmt)
		if err != nil {
			t.Fatalf("Parse(%s): %v", stmt, err)
		}
		if got, want := norm(t, again.Spec()), norm(t, spec); !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n got %v\nwant %v", stmt, got, want)
		}
		if line, err := call.PythonLine(); err != nil || !strings.HasPrefix(line, ".") {
			t.Errorf("python line %q, %v", line, err)
		}
		if line, err := call.TypeScriptLine(); err != nil || !strings.HasPrefix(line, ".") {
			t.Errorf("typescript line %q, %v", line, err)
		}
	}
}

func TestFromSpec_UnknownKey_Rejected(t *testing.T) {
	_, err := contractdsl.FromSpec("trace", map[string]any{"check": "forbidden_tools", "tools": []any{"a"}, "extra": 1})
	if err == nil {
		t.Fatal("want an error for a spec key the statement form cannot carry")
	}
}

func TestStatement_UnrepresentableString_Errors(t *testing.T) {
	call, err := contractdsl.FromSpec("trace", map[string]any{"check": "forbidden_tools", "tools": []any{"a\x01b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call.Statement(); err == nil {
		t.Error("want an error for a control character with no escape")
	}
}

func TestTypeScriptLine_FormatsOptionsObject(t *testing.T) {
	call, err := contractdsl.Parse(`tool_cardinality("issue_refund", per="order_id", at_most=1)`)
	if err != nil {
		t.Fatal(err)
	}
	line, err := call.TypeScriptLine()
	if err != nil {
		t.Fatal(err)
	}
	want := `.toolCardinality("issue_refund", { per: "order_id", atMost: 1 })`
	if line != want {
		t.Errorf("got %s, want %s", line, want)
	}
}

func TestMethods_AllHaveBothNames(t *testing.T) {
	for _, m := range contractdsl.Methods() {
		if m.Name == "" || m.TS == "" {
			t.Errorf("method %+v lacks a name", m)
		}
		if got, ok := contractdsl.Lookup(m.TS); !ok || got != m {
			t.Errorf("Lookup(%q) failed", m.TS)
		}
	}
}
