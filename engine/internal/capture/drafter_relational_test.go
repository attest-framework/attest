package capture

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func worldOf(state string) json.RawMessage {
	return json.RawMessage(`{"world_version":1,"definition_sha256":"` + zeroSHA + `","state":` + state + `,"faults_fired":[],"faults_fired_count":0}`)
}

const zeroSHA = "0000000000000000000000000000000000000000000000000000000000000000"

const refundDeskState = `{
  "orders":[{"order_id":"O-2077","customer_id":"C-0311","amount_cents":12600}],
  "refunds":[{"refund_id":"R-1","order_id":"O-2077","amount_cents":1200},{"refund_id":"R-2","order_id":"O-2077","amount_cents":12600}],
  "notifications":[{"notification_id":"N-1","order_id":"O-2077","customer_id":"C-0311"}]
}`

func TestDraftRelational_FindsSumAndMatchesFromIdKeys(t *testing.T) {
	cs, err := DraftRelational(worldOf(refundDeskState))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
		if c.Kind != KindRelational || c.Type != types.TypeEffect {
			t.Errorf("%s: kind %s type %s", c.ID, c.Kind, c.Type)
		}
	}
	want := []string{
		"relational-matches-notifications-customer_id-per-order_id",
		"relational-sum-refunds-amount_cents-per-order_id",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	if got, want := string(cs[1].Spec), `{"at_most":{"related":{"field":"amount_cents","on":"order_id","resource":"orders"}},"check":"sum","field":"amount_cents","resource":"refunds"}`; got != want {
		t.Errorf("sum spec %s, want %s", got, want)
	}
}

func TestVerifyRelational_IncidentWorldFailsTheDraftedSum(t *testing.T) {
	snapshot := worldOf(refundDeskState)
	cs, _ := DraftRelational(snapshot)
	results, err := VerifyRelational(snapshot, cs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range results {
		got[r.Contract] = r.Status
	}
	if got["relational-sum-refunds-amount_cents-per-order_id"] != types.StatusHardFail ||
		got["relational-matches-notifications-customer_id-per-order_id"] != types.StatusPass {
		t.Fatalf("statuses %v", got)
	}
}

func TestDraftRelational_NoRelationDraftsNothing(t *testing.T) {
	for name, state := range map[string]string{
		"related resource missing": `{"refunds":[{"order_id":"O-1","amount_cents":1}]}`,
		"related key not unique":   `{"orders":[{"order_id":"O-1","amount_cents":1},{"order_id":"O-1","amount_cents":2}],"refunds":[{"order_id":"O-1","amount_cents":1}]}`,
		"related key absent":       `{"orders":[{"id":"O-1","amount_cents":1}],"refunds":[{"order_id":"O-1","amount_cents":1}]}`,
		"no shared field":          `{"orders":[{"order_id":"O-1","total":1}],"refunds":[{"order_id":"O-1","amount_cents":1}]}`,
		"field optional somewhere": `{"orders":[{"order_id":"O-1","amount_cents":1}],"refunds":[{"order_id":"O-1","amount_cents":1},{"order_id":"O-1"}]}`,
		"mixed types":              `{"orders":[{"order_id":"O-1","amount_cents":"1"}],"refunds":[{"order_id":"O-1","amount_cents":1}]}`,
		"empty resources":          `{"orders":[],"refunds":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			cs, err := DraftRelational(worldOf(state))
			if err != nil || len(cs) != 0 {
				t.Fatalf("contracts %v err %v", cs, err)
			}
		})
	}
}

func TestDraftRelational_PluralisesYToIes(t *testing.T) {
	cs, err := DraftRelational(worldOf(`{"categories":[{"category_id":"K1","limit":5}],"items":[{"category_id":"K1","limit":3}]}`))
	if err != nil || len(cs) != 1 || cs[0].ID != "relational-sum-items-limit-per-category_id" {
		t.Fatalf("contracts %+v err %v", cs, err)
	}
}
