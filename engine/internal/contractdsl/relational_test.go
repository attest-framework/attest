package contractdsl_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/contractdsl"
)

func TestParse_SumRelated_BuildsTheSpec(t *testing.T) {
	call, err := contractdsl.Parse(`sum("refunds", "amount_cents", at_most=related("orders", "amount_cents", on="order_id"))`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"at_most":{"related":{"field":"amount_cents","on":"order_id","resource":"orders"}},"check":"sum","field":"amount_cents","resource":"refunds"}`
	got, _ := json.Marshal(call.Spec())
	if string(got) != want {
		t.Fatalf("spec %s, want %s", got, want)
	}
	if call.Type() != "effect" || !call.Method.Effect {
		t.Fatalf("type %s effect %v", call.Type(), call.Method.Effect)
	}
}

func TestParse_SumNumericBoundWithFilter(t *testing.T) {
	call, err := contractdsl.Parse(`sum("refunds", "amount_cents", at_most=15000.5, status="paid")`)
	if err != nil {
		t.Fatal(err)
	}
	spec := call.Spec()
	if spec["at_most"] != 15000.5 || !reflect.DeepEqual(spec["where"], map[string]any{"status": "paid"}) {
		t.Fatalf("spec %v", spec)
	}
}

func TestParse_MatchesRelated_BuildsTheSpec(t *testing.T) {
	call, err := contractdsl.Parse(`matches("notifications", "customer_id", related("orders", "customer_id", on="order_id"))`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"check":"matches","field":"customer_id","related":{"field":"customer_id","on":"order_id","resource":"orders"},"resource":"notifications"}`
	got, _ := json.Marshal(call.Spec())
	if string(got) != want {
		t.Fatalf("spec %s, want %s", got, want)
	}
}

func TestParse_RelatedKeywordForm(t *testing.T) {
	if _, err := contractdsl.Parse(`matches("n", "c", related="x")`); err == nil {
		t.Fatal("a string is not a related reference")
	}
	if _, err := contractdsl.Parse(`matches("n", "c", related=related("o", "c", on="k"))`); err != nil {
		t.Fatalf("keyword related: %v", err)
	}
}

func TestParse_RelatedRejectsEverythingButTheClosedForm(t *testing.T) {
	for name, stmt := range map[string]string{
		"identifier name":     `sum("r", "f", at_most=related(x, "f", on="k"))`,
		"missing on":          `sum("r", "f", at_most=related("o", "f"))`,
		"positional on":       `sum("r", "f", at_most=related("o", "f", "k"))`,
		"duplicate on":        `sum("r", "f", at_most=related("o", "f", on="k", on="j"))`,
		"unknown keyword":     `sum("r", "f", at_most=related("o", "f", via="k"))`,
		"empty name":          `sum("r", "f", at_most=related("o", "f", on=""))`,
		"nested related":      `sum("r", "f", at_most=related("o", "f", on=related("o", "f", on="k")))`,
		"inside a list":       `sum("r", "f", at_most=[related("o", "f", on="k")])`,
		"attribute access":    `sum("r", "f", at_most=related("o", "f", on="k").x)`,
		"another call":        `matches("r", "f", sum("o", "f", at_most=1))`,
		"wrong parameter":     `count("r", equals=related("o", "f", on="k"))`,
		"matches without ref": `matches("r", "f")`,
		"sum without bound":   `sum("r", "f")`,
		"related not a call":  `matches("r", "f", related)`,
		"number bound string": `sum("r", "f", at_most="12")`,
		"effect filter":       `count("refunds", at_most=1, order_id=related("o", "f", on="k"))`,
		"filter on sum":       `sum("r", "f", at_most=1, k=related("o", "f", on="k"))`,
		"value parameter":     `tool_arg_satisfies("t", "a", "ne", related("o", "f", on="k"))`,
		"value keyword":       `tool_arg_satisfies("t", "a", "ne", value=related("o", "f", on="k"))`,
		"where parameter":     `count("refunds", at_most=1, where=related("o", "f", on="k"))`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := contractdsl.Parse(stmt); err == nil {
				t.Fatalf("Parse(%s) succeeded", stmt)
			} else if _, ok := err.(*contractdsl.SyntaxError); !ok {
				t.Fatalf("error %T, want *SyntaxError", err)
			}
		})
	}
}

func TestStatement_RelationalRoundTripsThroughSpec(t *testing.T) {
	for _, stmt := range append([]string{}, relationalExamples...) {
		call, err := contractdsl.Parse(stmt)
		if err != nil {
			t.Fatal(err)
		}
		again, err := contractdsl.FromSpec(call.Type(), call.Spec())
		if err != nil {
			t.Fatalf("FromSpec(%s): %v", stmt, err)
		}
		a, _ := call.Statement()
		b, _ := again.Statement()
		if a != b || a != stmt {
			t.Fatalf("statement %q, from spec %q, want %q", a, b, stmt)
		}
	}
}

func TestFromSpec_RelationalRejectsMalformedValues(t *testing.T) {
	for _, spec := range []string{
		`{"check":"sum","resource":"r","field":"f","at_most":{"related":{"resource":"o"}}}`,
		`{"check":"sum","resource":"r","field":"f","at_most":{"other":1}}`,
		`{"check":"matches","resource":"r","field":"f","related":"o"}`,
		`{"check":"matches","resource":"r","field":"f","related":{"resource":"o","field":"f","on":"k","extra":"x"}}`,
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(spec), &m); err != nil {
			t.Fatal(err)
		}
		if _, err := contractdsl.FromSpec("effect", m); err == nil {
			t.Errorf("FromSpec accepted %s", spec)
		}
	}
}

func TestSignature_ListsRelationalMethods(t *testing.T) {
	for _, name := range []string{"sum", "matches"} {
		m, ok := contractdsl.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the catalog", name)
		}
		if sig := m.Signature(); !strings.HasPrefix(sig, name+"(resource, field") {
			t.Fatalf("%s signature %q", name, sig)
		}
	}
}
