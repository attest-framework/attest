package contractdsl_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/contractdsl"
)

const renderedGolden = contractsDir + "rendered.json"

// renderedExamples has at least one statement per catalog method. The lines the
// catalog renders for them are the golden file the Python and TypeScript SDK
// tests apply to a real expect chain.
var renderedExamples = []string{
	`tool_cardinality("issue_refund", per="order_id", at_most=1)`,
	`tool_cardinality("lookup_order", at_least=1, at_most=1, soft=True)`,
	`required_tools(["lookup_order", "issue_refund"])`,
	`forbidden_tools(["delete_order"])`,
	`trajectory(["lookup_order", "issue_refund"], mode="superset")`,
	`trajectory_partial_order([["lookup_order", "issue_refund"]], tools=["lookup_order"])`,
	`tool_called_before("lookup_order", "issue_refund")`,
	`no_tool_cycles(2, min_repeats=3)`,
	`max_tool_failure_streak(1)`,
	`tool_arg_satisfies("issue_refund", "amount_cents", "lte", 5000)`,
	`tool_arg_satisfies("issue_refund", "order_id", "in", ["O-1042"], soft=True)`,
	`output_matches_schema({"type": "object", "required": ["refund_id"]})`,
	`cost_under(0.05)`,
	`count("refunds", equals=1, order_id="O-1042")`,
	`count("refunds", at_least=1, at_most=2)`,
	`unique("refunds", by=["order_id", "amount_cents"])`,
	`idempotent("refunds", key="idempotency_key", where={"key": "k1", "by": "x"})`,
	"forbidden_tools([\"a\u200bb\", \"c\u00add\", \"e\u202ef\", \"g\U000e0041h\", \"i\u2028j\"])",
}

type renderedCase struct {
	Statement   string         `json:"statement"`
	Type        string         `json:"type"`
	Spec        map[string]any `json:"spec"`
	ChainPrefix string         `json:"chain_prefix"`
	Python      string         `json:"python"`
	TypeScript  string         `json:"typescript"`
}

func renderExamples(t *testing.T) []renderedCase {
	t.Helper()
	var out []renderedCase
	covered := map[string]bool{}
	for _, stmt := range renderedExamples {
		call, err := contractdsl.Parse(stmt)
		if err != nil {
			t.Fatalf("Parse(%q): %v", stmt, err)
		}
		covered[call.Method.Name] = true
		py, err := call.PythonLine()
		if err != nil {
			t.Fatal(err)
		}
		ts, err := call.TypeScriptLine()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, renderedCase{Statement: stmt, Type: call.Type(), Spec: call.Spec(), ChainPrefix: call.ChainPrefix(), Python: py, TypeScript: ts})
	}
	for _, m := range contractdsl.Methods() {
		if !covered[m.Name] {
			t.Errorf("no rendered example for catalog method %s", m.Name)
		}
	}
	return out
}

func TestRenderedLines_MatchGoldenFile(t *testing.T) {
	doc := map[string]any{
		"description": "Chain lines rendered by the contract catalog for each method (Go: internal/contractdsl). The Python and TypeScript SDK tests apply each line to a real expect chain with FIXTURE bound to an object holding a world snapshot. Regenerate with ATTEST_UPDATE_GOLDEN=1 go test ./internal/contractdsl.",
		"cases":       renderExamples(t),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ATTEST_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(renderedGolden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(renderedGolden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("%s is stale: regenerate with ATTEST_UPDATE_GOLDEN=1 go test ./internal/contractdsl", renderedGolden)
	}
}

func TestRenderedLines_EffectChecksCarryWorldPrefix(t *testing.T) {
	for _, c := range renderExamples(t) {
		call, _ := contractdsl.Parse(c.Statement)
		isEffect := call.Method.Effect
		if isEffect != (c.ChainPrefix == ".world(FIXTURE.world)") {
			t.Errorf("%s: prefix %q", c.Statement, c.ChainPrefix)
		}
		if isEffect && (!strings.HasPrefix(c.Python, ".world(FIXTURE.world).") || !strings.HasPrefix(c.TypeScript, ".world(FIXTURE.world).")) {
			t.Errorf("%s: lines %q / %q lack the world prefix", c.Statement, c.Python, c.TypeScript)
		}
		if !isEffect && strings.Contains(c.Python+c.TypeScript, ".world(") {
			t.Errorf("%s: trace check carries a world prefix", c.Statement)
		}
	}
}

func TestRenderedLines_FormatCharactersAreEscaped(t *testing.T) {
	for _, c := range renderExamples(t) {
		for _, r := range c.Python + c.TypeScript {
			if r == 0x200b || r == 0xad || r == 0x202e || r == 0x2028 || (r >= 0xe0000 && r <= 0xe007f) {
				t.Errorf("%s: raw format character %U in a rendered line", c.Statement, r)
			}
		}
	}
	last := renderExamples(t)[len(renderedExamples)-1]
	for _, want := range []string{`\u200b`, `\u00ad`, `\u202e`, `\u2028`} {
		if !strings.Contains(last.Python, want) || !strings.Contains(last.TypeScript, want) {
			t.Errorf("missing %s in\n%s\n%s", want, last.Python, last.TypeScript)
		}
	}
	if !strings.Contains(last.Python, `\U000e0041`) || !strings.Contains(last.TypeScript, `\u{e0041}`) {
		t.Errorf("tag-block character not escaped:\n%s\n%s", last.Python, last.TypeScript)
	}
}

func TestStatement_FormatCharacter_HasNoStatementForm(t *testing.T) {
	call, err := contractdsl.Parse("forbidden_tools([\"a\u200bb\"])")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call.Statement(); err == nil {
		t.Error("a zero-width character rendered into a statement")
	}
}
