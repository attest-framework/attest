package assertion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

func snapshotJSON(refunds string) string {
	return `{"world_version":1,"definition_sha256":"` + zeroHash + `","state":{"refunds":` + refunds + `,"orders":[{"id":"O-1042"}]},"faults_fired":[]}`
}

func evalEffect(spec string) *types.AssertionResult {
	return (&EffectEvaluator{}).Evaluate(&types.Trace{TraceID: "trc"}, &types.Assertion{
		AssertionID: "a1", Type: types.TypeEffect, Spec: json.RawMessage(spec),
	})
}

const twoRefunds = `[{"refund_id":"R-0001","order_id":"O-1042","amount_cents":4999},{"refund_id":"R-0002","order_id":"O-1042","amount_cents":4999}]`

func TestEffectCountViolationUsesTheIncidentMessage(t *testing.T) {
	r := evalEffect(`{"check":"count","world":` + snapshotJSON(twoRefunds) + `,"resource":"refunds","where":{"order_id":"O-1042"},"equals":1}`)
	if r.Status != types.StatusHardFail {
		t.Fatalf("status %s: %s", r.Status, r.Explanation)
	}
	if want := "effect violated: 2 refunds for O-1042 (expected 1)"; r.Explanation != want {
		t.Fatalf("explanation %q, want %q", r.Explanation, want)
	}
}

func TestEffectCountPassesWhenTheEffectHolds(t *testing.T) {
	one := `[{"refund_id":"R-0001","order_id":"O-1042","amount_cents":4999}]`
	r := evalEffect(`{"check":"count","world":` + snapshotJSON(one) + `,"resource":"refunds","where":{"order_id":"O-1042"},"equals":1}`)
	if r.Status != types.StatusPass {
		t.Fatalf("status %s: %s", r.Status, r.Explanation)
	}
}

func TestEffectCountBoundsAndFilters(t *testing.T) {
	snap := snapshotJSON(twoRefunds)
	for name, tc := range map[string]struct{ spec, want string }{
		"at most":    {`"where":{"order_id":"O-1042"},"at_most":1`, "effect violated: 2 refunds for O-1042 (expected at most 1)"},
		"at least":   {`"where":{"order_id":"O-1042"},"at_least":3`, "effect violated: 2 refunds for O-1042 (expected at least 3)"},
		"no filter":  {`"equals":1`, "effect violated: 2 refunds (expected 1)"},
		"two fields": {`"where":{"order_id":"O-1042","amount_cents":4999},"equals":1`, "effect violated: 2 refunds for amount_cents=4999, order_id=O-1042 (expected 1)"},
	} {
		t.Run(name, func(t *testing.T) {
			r := evalEffect(`{"check":"count","world":` + snap + `,"resource":"refunds",` + tc.spec + `}`)
			if r.Explanation != tc.want {
				t.Fatalf("explanation %q, want %q", r.Explanation, tc.want)
			}
		})
	}
	num := evalEffect(`{"check":"count","world":` + snap + `,"resource":"refunds","where":{"amount_cents":4999.0},"equals":2}`)
	if num.Status != types.StatusPass {
		t.Fatalf("4999.0 should match 4999: %s", num.Explanation)
	}
	one := snapshotJSON(`[{"refund_id":"R-1","order_id":"O-1"}]`)
	r := evalEffect(`{"check":"count","world":` + one + `,"resource":"refunds","where":{"order_id":"O-1"},"equals":0}`)
	if want := "effect violated: 1 refund for O-1 (expected 0)"; r.Explanation != want {
		t.Fatalf("explanation %q, want %q", r.Explanation, want)
	}
}

func TestEffectUnique(t *testing.T) {
	r := evalEffect(`{"check":"unique","world":` + snapshotJSON(twoRefunds) + `,"resource":"refunds","fields":["order_id"]}`)
	if want := "effect violated: 2 refunds share order_id=O-1042 (expected unique by order_id)"; r.Explanation != want {
		t.Fatalf("explanation %q, want %q", r.Explanation, want)
	}
	r = evalEffect(`{"check":"unique","world":` + snapshotJSON(twoRefunds) + `,"resource":"refunds","fields":["refund_id"]}`)
	if r.Status != types.StatusPass {
		t.Fatalf("distinct ids: %s", r.Explanation)
	}
}

func TestEffectIdempotent(t *testing.T) {
	keyed := `[{"refund_id":"R-1","idempotency_key":"k1"},{"refund_id":"R-2","idempotency_key":"k2"}]`
	if r := evalEffect(`{"check":"idempotent","world":` + snapshotJSON(keyed) + `,"resource":"refunds","field":"idempotency_key"}`); r.Status != types.StatusPass {
		t.Fatalf("keyed: %s", r.Explanation)
	}
	r := evalEffect(`{"check":"idempotent","world":` + snapshotJSON(twoRefunds) + `,"resource":"refunds","field":"idempotency_key"}`)
	if r.Status != types.StatusHardFail || !strings.HasPrefix(r.Explanation, "effect violated: 2 of 2 refunds created without an idempotency key") {
		t.Fatalf("unkeyed: %s %s", r.Status, r.Explanation)
	}
	dup := `[{"refund_id":"R-1","idempotency_key":"k"},{"refund_id":"R-2","idempotency_key":"k"}]`
	r = evalEffect(`{"check":"idempotent","world":` + snapshotJSON(dup) + `,"resource":"refunds","field":"idempotency_key"}`)
	if !strings.Contains(r.Explanation, "share idempotency key k") {
		t.Fatalf("shared key: %s", r.Explanation)
	}
}

func TestEffectNullWorldIsNotEvaluatedForMissingEvidence(t *testing.T) {
	r := evalEffect(`{"check":"count","world":null,"resource":"refunds","equals":1}`)
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonMissingEvidence {
		t.Fatalf("got %s/%s", r.Status, r.Reason)
	}
}

func TestEffectRejectsMalformedSpecs(t *testing.T) {
	snap := snapshotJSON(twoRefunds)
	for name, spec := range map[string]string{
		"not json":         `{`,
		"no check":         `{"world":` + snap + `,"resource":"refunds"}`,
		"no world":         `{"check":"count","resource":"refunds","equals":1}`,
		"bad snapshot":     `{"check":"count","world":{"world_version":1},"resource":"refunds","equals":1}`,
		"unknown check":    `{"check":"vibes","world":` + snap + `,"resource":"refunds"}`,
		"unknown resource": `{"check":"count","world":` + snap + `,"resource":"payouts","equals":1}`,
		"count no bound":   `{"check":"count","world":` + snap + `,"resource":"refunds"}`,
		"equals and max":   `{"check":"count","world":` + snap + `,"resource":"refunds","equals":1,"at_most":2}`,
		"unique no field":  `{"check":"unique","world":` + snap + `,"resource":"refunds"}`,
		"idempotent bare":  `{"check":"idempotent","world":` + snap + `,"resource":"refunds"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if r := evalEffect(spec); r.Status != types.StatusHardFail {
				t.Fatalf("status %s: %s", r.Status, r.Explanation)
			}
		})
	}
}

func TestEffectIsLayerThreeAndRegistered(t *testing.T) {
	if got := types.LayerForType(types.TypeEffect); got != 3 {
		t.Fatalf("layer %d, want 3", got)
	}
	if !NewRegistry().HasEvaluator(types.TypeEffect) {
		t.Fatal("effect evaluator not registered")
	}
	if layerOrder[types.TypeEffect] != 3 {
		t.Fatal("effect not ordered with layer 3")
	}
}

func TestEffectUniqueDistinguishesTypesAndMissingFields(t *testing.T) {
	recs := `[{"id":"a","order_id":"1"},{"id":"b","order_id":1},{"id":"c"},{"id":"d","order_id":null}]`
	r := evalEffect(`{"check":"unique","world":` + snapshotJSON(recs) + `,"resource":"refunds","fields":["order_id"]}`)
	if r.Status != types.StatusPass {
		t.Fatalf("false duplicate: %s", r.Explanation)
	}
}
