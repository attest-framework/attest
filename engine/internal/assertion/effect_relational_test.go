package assertion

import (
	"math/big"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func relationalSnapshot(orders, refunds, notifications string) string {
	return `{"world_version":1,"definition_sha256":"` + zeroHash + `","state":{"orders":` + orders +
		`,"refunds":` + refunds + `,"notifications":` + notifications + `},"faults_fired":[],"faults_fired_count":0}`
}

const (
	relOrders   = `[{"order_id":"O-2077","customer_id":"C-0311","amount_cents":12600},{"order_id":"O-3000","customer_id":"C-0001","amount_cents":500}]`
	sumRelated  = `"check":"sum","resource":"refunds","field":"amount_cents","at_most":{"related":{"resource":"orders","field":"amount_cents","on":"order_id"}}`
	matchesSpec = `"check":"matches","resource":"notifications","field":"customer_id","related":{"resource":"orders","field":"customer_id","on":"order_id"}`
)

func sumSpec(refunds, extra string) string {
	return `{` + sumRelated + extra + `,"world":` + relationalSnapshot(relOrders, refunds, `[]`) + `}`
}

func TestEffectSumRelatedFailsOverTheOrderAmount(t *testing.T) {
	r := evalEffect(sumSpec(`[{"order_id":"O-2077","amount_cents":1200},{"order_id":"O-2077","amount_cents":12600}]`, ""))
	want := "effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestEffectSumRelatedPassesWithinTheOrderAmount(t *testing.T) {
	r := evalEffect(sumSpec(`[{"order_id":"O-2077","amount_cents":1200},{"order_id":"O-2077","amount_cents":11400}]`, ""))
	if r.Status != types.StatusPass {
		t.Fatalf("status %s: %s", r.Status, r.Explanation)
	}
}

func TestEffectSumRelatedMissingRelatedRecordNamesTheKey(t *testing.T) {
	r := evalEffect(sumSpec(`[{"order_id":"O-9999","amount_cents":100}]`, ""))
	want := "effect violated: refunds has order_id O-9999 but orders has no record with that order_id"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestEffectSumRelatedReportsFirstKeyInSortedOrder(t *testing.T) {
	refunds := `[{"order_id":"O-3000","amount_cents":900},{"order_id":"O-2077","amount_cents":99999}]`
	want := "effect violated: refunds amount_cents sum 99999 for O-2077 exceeds orders.amount_cents 12600; 1 more violations"
	for i := 0; i < 20; i++ {
		if r := evalEffect(sumSpec(refunds, "")); r.Explanation != want {
			t.Fatalf("run %d explanation %q, want %q", i, r.Explanation, want)
		}
	}
}

func TestEffectSumRelatedWhereFiltersRecords(t *testing.T) {
	refunds := `[{"order_id":"O-2077","amount_cents":13000,"status":"voided"},{"order_id":"O-2077","amount_cents":100,"status":"paid"}]`
	r := evalEffect(sumSpec(refunds, `,"where":{"status":"paid"}`))
	if r.Status != types.StatusPass {
		t.Fatalf("status %s: %s", r.Status, r.Explanation)
	}
}

func TestEffectSumRelatedRecordWithoutKeyIsAViolation(t *testing.T) {
	r := evalEffect(sumSpec(`[{"amount_cents":100}]`, ""))
	want := "effect violated: refunds record 1 has no order_id to relate to orders"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestEffectSumNumericBound(t *testing.T) {
	refunds := `[{"order_id":"O-2077","amount_cents":1200,"kind":"a"},{"order_id":"O-3000","amount_cents":300.5,"kind":"b"}]`
	snap := relationalSnapshot(relOrders, refunds, `[]`)
	for name, tc := range map[string]struct {
		spec, want string
		status     string
	}{
		"total fails": {`"at_most":1500`, "effect violated: refunds amount_cents sum 1500.5 exceeds 1500", types.StatusHardFail},
		"total fits":  {`"at_most":1500.5`, "", types.StatusPass},
		"where fits":  {`"at_most":1200,"where":{"kind":"a"}`, "", types.StatusPass},
		"where fails": {`"at_most":1000,"where":{"kind":"a"}`, "effect violated: refunds amount_cents sum 1200 for a exceeds 1000", types.StatusHardFail},
	} {
		t.Run(name, func(t *testing.T) {
			r := evalEffect(`{"check":"sum","resource":"refunds","field":"amount_cents",` + tc.spec + `,"world":` + snap + `}`)
			if r.Status != tc.status || (tc.want != "" && r.Explanation != tc.want) {
				t.Fatalf("status %s explanation %q, want %s %q", r.Status, r.Explanation, tc.status, tc.want)
			}
		})
	}
}

func TestEffectSumNonNumericValueIsAHardFail(t *testing.T) {
	for name, refunds := range map[string]string{
		"string":  `[{"order_id":"O-2077","amount_cents":"12"}]`,
		"null":    `[{"order_id":"O-2077","amount_cents":null}]`,
		"missing": `[{"order_id":"O-2077"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			r := evalEffect(sumSpec(refunds, ""))
			if r.Status != types.StatusHardFail || !strings.HasPrefix(r.Explanation, "effect evidence: refunds amount_cents is not a number in record 1") {
				t.Fatalf("status %s explanation %q", r.Status, r.Explanation)
			}
		})
	}
}

func TestEffectSumNonNumericRelatedBoundIsAHardFail(t *testing.T) {
	orders := `[{"order_id":"O-2077","amount_cents":"lots"}]`
	spec := `{` + sumRelated + `,"world":` + relationalSnapshot(orders, `[{"order_id":"O-2077","amount_cents":1}]`, `[]`) + `}`
	r := evalEffect(spec)
	if r.Status != types.StatusHardFail || !strings.HasPrefix(r.Explanation, "effect evidence: orders amount_cents is not a number for order_id O-2077") {
		t.Fatalf("status %s explanation %q", r.Status, r.Explanation)
	}
}

func TestEffectSumAmbiguousRelatedKeyIsAHardFail(t *testing.T) {
	orders := `[{"order_id":"O-1","amount_cents":5},{"order_id":"O-1","amount_cents":6}]`
	spec := `{` + sumRelated + `,"world":` + relationalSnapshot(orders, `[{"order_id":"O-1","amount_cents":1}]`, `[]`) + `}`
	r := evalEffect(spec)
	if r.Status != types.StatusHardFail || !strings.Contains(r.Explanation, "orders has more than one record with order_id O-1") {
		t.Fatalf("status %s explanation %q", r.Status, r.Explanation)
	}
}

func TestEffectMatchesFailsForAMisdirectedNotification(t *testing.T) {
	notes := `[{"notification_id":"N-1","order_id":"O-2077","customer_id":"C-0520"}]`
	r := evalEffect(`{` + matchesSpec + `,"world":` + relationalSnapshot(relOrders, `[]`, notes) + `}`)
	want := "effect violated: notifications customer_id C-0520 for O-2077 does not match orders.customer_id C-0311"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestEffectMatchesPassesAndFiltersWithWhere(t *testing.T) {
	notes := `[{"order_id":"O-2077","customer_id":"C-0311","channel":"email"},{"order_id":"O-3000","customer_id":"C-9","channel":"sms"}]`
	snap := relationalSnapshot(relOrders, `[]`, notes)
	if r := evalEffect(`{` + matchesSpec + `,"where":{"channel":"email"},"world":` + snap + `}`); r.Status != types.StatusPass {
		t.Fatalf("filtered status %s: %s", r.Status, r.Explanation)
	}
	if r := evalEffect(`{` + matchesSpec + `,"world":` + snap + `}`); r.Status != types.StatusHardFail {
		t.Fatalf("unfiltered status %s: %s", r.Status, r.Explanation)
	}
}

func TestEffectMatchesMissingRelatedRecordNamesTheKey(t *testing.T) {
	notes := `[{"order_id":"O-404","customer_id":"C-1"}]`
	r := evalEffect(`{` + matchesSpec + `,"world":` + relationalSnapshot(relOrders, `[]`, notes) + `}`)
	want := "effect violated: notifications has order_id O-404 but orders has no record with that order_id"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestEffectRelationalRejectsMalformedSpecs(t *testing.T) {
	snap := relationalSnapshot(relOrders, `[]`, `[]`)
	for name, spec := range map[string]string{
		"sum no field":        `{"check":"sum","resource":"refunds","at_most":1,"world":` + snap + `}`,
		"sum no bound":        `{"check":"sum","resource":"refunds","field":"amount_cents","world":` + snap + `}`,
		"sum equals":          `{"check":"sum","resource":"refunds","field":"amount_cents","at_most":1,"equals":1,"world":` + snap + `}`,
		"sum bad bound":       `{"check":"sum","resource":"refunds","field":"amount_cents","at_most":"x","world":` + snap + `}`,
		"sum incomplete ref":  `{"check":"sum","resource":"refunds","field":"amount_cents","at_most":{"related":{"resource":"orders"}},"world":` + snap + `}`,
		"sum unknown related": `{"check":"sum","resource":"refunds","field":"amount_cents","at_most":{"related":{"resource":"nope","field":"a","on":"b"}},"world":` + snap + `}`,
		"matches no related":  `{"check":"matches","resource":"notifications","field":"customer_id","world":` + snap + `}`,
		"matches no field":    `{"check":"matches","resource":"notifications","related":{"resource":"orders","field":"a","on":"b"},"world":` + snap + `}`,
		"count float at_most": `{"check":"count","resource":"refunds","at_most":1.5,"world":` + snap + `}`,
		"count related bound": `{"check":"count","resource":"refunds","at_most":{"related":{"resource":"orders","field":"a","on":"b"}},"world":` + snap + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			if r := evalEffect(spec); r.Status != types.StatusHardFail {
				t.Fatalf("status %s: %s", r.Status, r.Explanation)
			}
		})
	}
}

func TestEffectSumRelatedMalformedBoundsReportTheSortedFirstKeyEveryRun(t *testing.T) {
	orders := `[{"order_id":"B","amount_cents":"x"},{"order_id":"A","amount_cents":"y"},{"order_id":"C","amount_cents":null},{"order_id":"D","amount_cents":true}]`
	refunds := `[{"order_id":"D","amount_cents":1},{"order_id":"C","amount_cents":1},{"order_id":"B","amount_cents":1},{"order_id":"A","amount_cents":1}]`
	spec := `{` + sumRelated + `,"world":` + relationalSnapshot(orders, refunds, `[]`) + `}`
	want := evalEffect(spec).Explanation
	if !strings.Contains(want, "for order_id A ") {
		t.Fatalf("explanation %q does not name the sorted-first key A", want)
	}
	for i := 0; i < 100; i++ {
		if got := evalEffect(spec).Explanation; got != want {
			t.Fatalf("run %d explanation %q, want %q", i, got, want)
		}
	}
}

func TestEffectSumDisplaysDistinctValuesLosslessly(t *testing.T) {
	orders := `[{"order_id":"O-1","amount_cents":0}]`
	refunds := `[{"order_id":"O-1","amount_cents":1e-13}]`
	spec := `{` + sumRelated + `,"world":` + relationalSnapshot(orders, refunds, `[]`) + `}`
	r := evalEffect(spec)
	want := "effect violated: refunds amount_cents sum 0.0000000000001 for O-1 exceeds orders.amount_cents 0"
	if r.Status != types.StatusHardFail || r.Explanation != want {
		t.Fatalf("status %s explanation %q, want %q", r.Status, r.Explanation, want)
	}
}

func TestFormatRatIsLossless(t *testing.T) {
	cases := map[string]string{
		"13800": "13800", "0": "0", "-5": "-5", "12.50": "12.5", "0.1": "0.1",
		"1e-13": "0.0000000000001", "0.000000000000123456789": "0.000000000000123456789",
		"1.25": "1.25", "1/3": "1/3", "-2/7": "-2/7",
	}
	for in, want := range cases {
		r, ok := new(big.Rat).SetString(in)
		if !ok {
			t.Fatalf("bad case %q", in)
		}
		if got := formatRat(r); got != want {
			t.Errorf("formatRat(%s) = %q, want %q", in, got, want)
		}
	}
}
