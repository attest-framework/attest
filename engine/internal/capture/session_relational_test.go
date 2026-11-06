package capture

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/contractdsl"
	"github.com/attest-framework/attest/engine/internal/world"
	"github.com/attest-framework/attest/engine/pkg/types"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func buildWithWorld(t *testing.T, snap *world.Snapshot, expectFail bool) (*Fixture, BuildReport) {
	t.Helper()
	up := newFakeUpstream(t)
	s, err := Start(Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})
	if err != nil {
		t.Fatal(err)
	}
	scriptedRun(t, s.Env(), up, 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	spans, exchanges, _, err := s.Recorded()
	if err != nil {
		t.Fatal(err)
	}
	f, report, err := BuildFixtureReport(BuildInput{
		Name: "scripted", Source: Source{Kind: SourceCommand, ReplayCapable: true, Command: []string{"agent"}},
		Spans: spans, Exchanges: exchanges, ExpectFail: expectFail, Redactor: redactorFor(plantedKey), World: snap,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, report
}

func relationalIDs(f *Fixture) []string {
	var ids []string
	for _, c := range f.Contracts {
		if c.Kind == KindRelational {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func overRefundedWorld() *world.Snapshot {
	return &world.Snapshot{
		WorldVersion: 1, DefinitionSHA256: zeroSHA, FaultsFired: []world.Fired{},
		State: map[string][]map[string]any{
			"orders":  {{"order_id": "O-2077", "amount_cents": 12600}},
			"refunds": {{"refund_id": "R-1", "order_id": "O-2077", "amount_cents": 13800}},
		},
	}
}

func TestBuildFixture_WorldWithRelation_StoresAndVerifiesRelationalDrafts(t *testing.T) {
	f, report := buildWithWorld(t, overRefundedWorld(), true)

	ids := relationalIDs(f)
	if len(ids) != 1 || ids[0] != "relational-sum-refunds-amount_cents-per-order_id" || report.RelationalCapped {
		t.Fatalf("relational contracts %v capped %v", ids, report.RelationalCapped)
	}
	if !f.Verification.Verified || len(f.Verification.Results) != len(f.Contracts) {
		t.Fatalf("verified %v, %d results for %d contracts", f.Verification.Verified, len(f.Verification.Results), len(f.Contracts))
	}
	last := f.Verification.Results[len(f.Verification.Results)-1]
	if last.Contract != ids[0] || last.Status != types.StatusHardFail || last.Explanation != "effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600" {
		t.Fatalf("relational result %+v", last)
	}
}

func TestBuildFixture_NoWorld_DraftsNoRelationalContracts(t *testing.T) {
	f, report := buildWithWorld(t, nil, false)

	if ids := relationalIDs(f); len(ids) != 0 || report.RelationalCapped {
		t.Fatalf("relational contracts %v capped %v", ids, report.RelationalCapped)
	}
}

func TestBuildFixture_WorldRelatingMoreThanTheCap_ReportsCappedDrafts(t *testing.T) {
	order, refund := map[string]any{"order_id": "O-1"}, map[string]any{"order_id": "O-1"}
	for i := 0; i < contractdsl.MaxStatements+10; i++ {
		order[fmt.Sprintf("f%04d", i)] = 10
		refund[fmt.Sprintf("f%04d", i)] = 1
	}
	snap := &world.Snapshot{WorldVersion: 1, DefinitionSHA256: zeroSHA, FaultsFired: []world.Fired{},
		State: map[string][]map[string]any{"orders": {order}, "refunds": {refund}}}

	f, report := buildWithWorld(t, snap, false)

	if n := len(relationalIDs(f)); n != contractdsl.MaxStatements || !report.RelationalCapped {
		t.Fatalf("%d relational contracts, capped %v; want %d, true", n, report.RelationalCapped, contractdsl.MaxStatements)
	}
}

func TestVerifyIn_EffectContractWithoutWorld_Errors(t *testing.T) {
	c := Contract{ID: "relational-x", Kind: KindRelational, Type: types.TypeEffect, Spec: []byte(`{"check":"count","resource":"refunds"}`)}

	if _, err := VerifyIn(&types.Trace{}, nil, []Contract{c}, false, TraceCheckResult{OK: true}); err == nil {
		t.Fatal("effect contract verified with no world")
	}
}

func TestFixtureV1Schema_CaptureWithRelationalDrafts_Valid(t *testing.T) {
	s := compileFixtureV1Schema(t)
	f, _ := buildWithWorld(t, overRefundedWorld(), true)
	data, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(doc); err != nil {
		t.Fatalf("capture with relational drafts fails the schema: %v", err)
	}
}

func TestBuildFixture_WorldTooLargeForTheDrafts_TrimsDraftsAndStillWritesTheFixture(t *testing.T) {
	order, refund := map[string]any{"order_id": "O-1", "blob": strings.Repeat("x", 1<<20)}, map[string]any{"order_id": "O-1"}
	for i := 0; i < 40; i++ {
		order[fmt.Sprintf("f%04d", i)] = 10
		refund[fmt.Sprintf("f%04d", i)] = 1
	}
	snap := &world.Snapshot{WorldVersion: 1, DefinitionSHA256: zeroSHA, FaultsFired: []world.Fired{},
		State: map[string][]map[string]any{"orders": {order}, "refunds": {refund}}}

	f, report := buildWithWorld(t, snap, false)

	n := len(relationalIDs(f))
	if n == 0 || n >= 40 || !report.RelationalTrimmed || report.RelationalKept != n {
		t.Fatalf("%d relational contracts, trimmed %v, kept %d; want 0 < n < 40, trimmed, kept == n", n, report.RelationalTrimmed, report.RelationalKept)
	}
	if work := len(f.World) * n; work > RelationalWorkLimit {
		t.Fatalf("world %d bytes x %d contracts = %d, over the limit %d", len(f.World), n, work, RelationalWorkLimit)
	}
}

func TestBuildFixture_WorldBeyondTheLimitAlone_DropsEveryRelationalDraft(t *testing.T) {
	order, refund := map[string]any{"order_id": "O-1", "blob": strings.Repeat("x", RelationalWorkLimit+1)}, map[string]any{"order_id": "O-1", "amount": 1}
	order["amount"] = 10
	snap := &world.Snapshot{WorldVersion: 1, DefinitionSHA256: zeroSHA, FaultsFired: []world.Fired{},
		State: map[string][]map[string]any{"orders": {order}, "refunds": {refund}}}

	f, report := buildWithWorld(t, snap, false)

	if n := len(relationalIDs(f)); n != 0 || !report.RelationalTrimmed || report.RelationalKept != 0 {
		t.Fatalf("%d relational contracts, trimmed %v, kept %d; want 0, trimmed, 0", n, report.RelationalTrimmed, report.RelationalKept)
	}
}
