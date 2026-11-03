package contractdsl_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/attest-ai/attest/engine/internal/contractdsl"
)

var rejectedSeeds = []string{
	`__import__("os")`, `os.system("x")`, `tool_cardinality(x)`, `tool_cardinality("a", per=open("f"))`,
	`1+1`, `a.b.c()`, `f"x"`, `lambda: 1`, `forbidden_tools(["a"]); forbidden_tools(["b"])`,
	`forbidden_tools(["a"]) # x`, `tool_cardinality("a", at_most=1, at_most=2)`, `forbidden_tools(["\x41"])`,
	`forbidden_tools(*x)`, `forbidden_tools(**x)`, `forbidden_tools(x := 1)`, `cost_under(0x10)`,
	`tool_arg_satisfies("t", "a", "eq", [[[[[[[[[[]]]]]]]]]])`, "forbidden_tools([\"a\nb\"])", ``,
}

// FuzzParse checks that no input panics the parser or the renderers, and that
// whatever parses renders back to a statement with the same spec.
func FuzzParse(f *testing.F) {
	for _, file := range []string{"dsl.json", "effect-dsl.json"} {
		data, err := readContracts(file)
		if err != nil {
			f.Fatal(err)
		}
		var doc struct {
			Cases []dslCase `json:"cases"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			f.Fatal(err)
		}
		for _, c := range doc.Cases {
			f.Add(caseStatement(c))
		}
	}
	for _, s := range renderedExamples {
		f.Add(s)
	}
	for _, s := range rejectedSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, stmt string) {
		call, err := contractdsl.Parse(stmt)
		if err != nil {
			if _, ok := err.(*contractdsl.SyntaxError); !ok {
				t.Fatalf("error %T, want *SyntaxError", err)
			}
			return
		}
		spec := call.Spec()
		want, err := json.Marshal(spec)
		if err != nil {
			t.Fatalf("spec does not marshal: %v", err)
		}
		call.Python()
		call.TypeScript()
		call.PythonLine()
		call.TypeScriptLine()
		text, err := call.Statement()
		if err != nil {
			return // a string with no statement form
		}
		again, err := contractdsl.Parse(text)
		if err != nil {
			t.Fatalf("rendered statement %q does not parse: %v", text, err)
		}
		got, _ := json.Marshal(again.Spec())
		var a, b any
		json.Unmarshal(got, &a)
		json.Unmarshal(want, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("round trip changed the spec:\n%s\n%s", want, got)
		}
	})
}
