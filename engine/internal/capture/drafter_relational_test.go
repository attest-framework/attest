package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/attest-framework/attest/engine/internal/contractdsl"
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
	cs, _, err := DraftRelational(worldOf(refundDeskState))
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
	cs, _, _ := DraftRelational(snapshot)
	results, err := VerifyRelational(context.Background(), snapshot, cs)
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
			cs, _, err := DraftRelational(worldOf(state))
			if err != nil || len(cs) != 0 {
				t.Fatalf("contracts %v err %v", cs, err)
			}
		})
	}
}

func TestDraftRelational_PluralisesYToIes(t *testing.T) {
	cs, _, err := DraftRelational(worldOf(`{"categories":[{"category_id":"K1","limit":5}],"items":[{"category_id":"K1","limit":3}]}`))
	if err != nil || len(cs) != 1 || cs[0].ID != "relational-sum-items-limit-per-category_id" {
		t.Fatalf("contracts %+v err %v", cs, err)
	}
}

func manySharedFieldsWorld(fields int) json.RawMessage {
	var orders, refunds []string
	for i := 0; i < fields; i++ {
		orders = append(orders, fmt.Sprintf(`"f%04d":10`, i))
		refunds = append(refunds, fmt.Sprintf(`"f%04d":1`, i))
	}
	return worldOf(`{"orders":[{"order_id":"O-1",` + strings.Join(orders, ",") + `}],"refunds":[{"order_id":"O-1",` + strings.Join(refunds, ",") + `}]}`)
}

func TestDraftRelational_ManySharedFieldsAreCappedAndReported(t *testing.T) {
	start := time.Now()
	cs, capped, err := DraftRelational(manySharedFieldsWorld(2000))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != contractdsl.MaxStatements || !capped {
		t.Fatalf("got %d contracts, capped=%v; want %d, true", len(cs), capped, contractdsl.MaxStatements)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("drafting took %s", elapsed)
	}
}

func TestDraftRelational_BelowTheCapIsNotReportedCapped(t *testing.T) {
	cs, capped, err := DraftRelational(worldOf(refundDeskState))
	if err != nil || capped || len(cs) == 0 {
		t.Fatalf("contracts %d capped %v err %v", len(cs), capped, err)
	}
}

func TestVerifyRelational_CancelledContextStopsBetweenContracts(t *testing.T) {
	snapshot := manySharedFieldsWorld(10)
	cs, _, err := DraftRelational(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyRelational(ctx, snapshot, cs); err != context.Canceled {
		t.Fatalf("err %v, want context.Canceled", err)
	}
}

func TestDraftRelational_IdAndDescriptionSanitizeWorldNames(t *testing.T) {
	long := strings.Repeat("x", 200)
	field := `amount\u001b[31m\u202e` + long
	snapshot := worldOf(`{"orders":[{"order_id":"O-1","` + field + `":10}],"refunds":[{"order_id":"O-1","` + field + `":1}]}`)
	cs, _, err := DraftRelational(snapshot)
	if err != nil || len(cs) != 1 {
		t.Fatalf("contracts %d err %v", len(cs), err)
	}
	for _, text := range []string{cs[0].ID, cs[0].Description} {
		for _, r := range text {
			if r < 0x20 || r == 0x7f || r == 0x202e {
				t.Fatalf("control or format character %U in %q", r, text)
			}
		}
		if len(text) > 400 {
			t.Fatalf("unbounded text of %d bytes", len(text))
		}
	}
	if !strings.Contains(string(cs[0].Spec), long[:50]) {
		t.Fatalf("spec lost the raw field name: %s", cs[0].Spec)
	}
}

func draftedIDs(t *testing.T, state string) []string {
	t.Helper()
	cs, _, err := DraftRelational(worldOf(state))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}

func TestDraftRelational_NamesThatCollideAfterSanitizing_GetDistinctIDs(t *testing.T) {
	// "amount" and "amo\u0001unt" both display as "amount".
	ids := draftedIDs(t, `{
  "orders":[{"order_id":"O-1","amount":10,"amo\u0001unt":10}],
  "refunds":[{"refund_id":"R-1","order_id":"O-1","amount":1,"amo\u0001unt":1}]
}`)

	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate contract id %q in %q", id, ids)
		}
		seen[id] = true
	}
	if len(ids) != 2 {
		t.Fatalf("%d contracts drafted, want 2: %q", len(ids), ids)
	}
}

func TestDraftRelational_HyphenAmbiguousNames_GetDistinctIDs(t *testing.T) {
	// ("a-b", "c") and ("a", "b-c") both render relational-sum-a-b-c-per-k_id.
	ids := draftedIDs(t, `{
  "ks":[{"k_id":"K1","c":5,"b-c":5}],
  "a-b":[{"k_id":"K1","c":1}],
  "a":[{"k_id":"K1","b-c":1}]
}`)

	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("ids %q, want two distinct", ids)
	}
}

func TestDraftRelational_WhitespaceInNames_IDsHaveNone(t *testing.T) {
	ids := draftedIDs(t, "{"+
		`"orders":[{"order_id":"O-1","amount cents":10,"tab\tfield":10,"nb space":10}],`+
		`"refunds":[{"refund_id":"R-1","order_id":"O-1","amount cents":1,"tab\tfield":1,"nb space":1}]}`)

	if len(ids) != 3 {
		t.Fatalf("%d contracts, want 3: %q", len(ids), ids)
	}
	for _, id := range ids {
		if strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }) >= 0 {
			t.Errorf("id %q contains whitespace", id)
		}
	}
}

func TestDraftRelational_CleanNames_KeepTheirPlainID(t *testing.T) {
	ids := draftedIDs(t, refundDeskState)

	for _, id := range ids {
		if strings.Contains(id, "~") {
			t.Errorf("clean names got a hash suffix: %q", id)
		}
	}
}
