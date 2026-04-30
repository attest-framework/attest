package cache_test

import (
	"database/sql"
	"testing"

	"github.com/attest-ai/attest/engine/internal/cache"
	_ "modernc.org/sqlite"
)

func newBaselineStore(t *testing.T) *cache.BaselineStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := cache.NewBaselineStore(db)
	if err != nil {
		t.Fatalf("NewBaselineStore: %v", err)
	}
	return store
}

func TestBaselineStore_PinAndGet(t *testing.T) {
	store := newBaselineStore(t)

	entries := []cache.BaselineEntry{
		{AssertionID: "a1", Type: "constraint", Status: "pass", Score: 1.0, Cost: 0, DurationMS: 5},
		{AssertionID: "a2", Type: "llm_judge", Status: "soft_fail", Score: 0.6, Cost: 0.001, DurationMS: 120},
		{AssertionID: "a3", Type: "schema", Status: "hard_fail", Score: 0.0, DurationMS: 1},
	}
	if err := store.Pin("v1.0.0", entries); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	got, err := store.Get("v1.0.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Get returned %d entries, want 3", len(got))
	}
	// Ordered by assertion_id.
	wantIDs := []string{"a1", "a2", "a3"}
	for i, want := range wantIDs {
		if got[i].AssertionID != want {
			t.Errorf("Get[%d].AssertionID = %q, want %q", i, got[i].AssertionID, want)
		}
	}
	if got[0].Score != 1.0 || got[1].Score != 0.6 || got[2].Score != 0.0 {
		t.Errorf("scores not preserved: got %+v", got)
	}
	if got[1].Cost != 0.001 {
		t.Errorf("cost not preserved: got %f", got[1].Cost)
	}
	if got[1].DurationMS != 120 {
		t.Errorf("duration not preserved: got %d", got[1].DurationMS)
	}
	if got[0].PinnedAt.IsZero() {
		t.Error("PinnedAt should be set after Pin")
	}
}

func TestBaselineStore_PinReplacesPreviousSnapshot(t *testing.T) {
	store := newBaselineStore(t)

	if err := store.Pin("v1", []cache.BaselineEntry{
		{AssertionID: "a1", Type: "schema", Status: "pass", Score: 1.0},
		{AssertionID: "a2", Type: "schema", Status: "pass", Score: 1.0},
	}); err != nil {
		t.Fatalf("first pin: %v", err)
	}

	// Re-pin with a smaller and different snapshot — must replace, not merge.
	if err := store.Pin("v1", []cache.BaselineEntry{
		{AssertionID: "a3", Type: "constraint", Status: "soft_fail", Score: 0.5},
	}); err != nil {
		t.Fatalf("second pin: %v", err)
	}

	got, err := store.Get("v1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after re-pin, got %d entries, want 1", len(got))
	}
	if got[0].AssertionID != "a3" {
		t.Errorf("after re-pin, expected a3, got %s", got[0].AssertionID)
	}
}

func TestBaselineStore_PinRejectsEmptyTagOrEntries(t *testing.T) {
	store := newBaselineStore(t)

	if err := store.Pin("", []cache.BaselineEntry{{AssertionID: "a1", Status: "pass"}}); err == nil {
		t.Error("Pin with empty tag must error")
	}
	if err := store.Pin("v1", nil); err == nil {
		t.Error("Pin with no entries must error")
	}
}

func TestBaselineStore_GetUnknownTagReturnsEmpty(t *testing.T) {
	store := newBaselineStore(t)
	got, err := store.Get("never-pinned")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get for unknown tag = %d entries, want 0", len(got))
	}
}

func TestBaselineStore_List(t *testing.T) {
	store := newBaselineStore(t)

	if err := store.Pin("v1.0.0", []cache.BaselineEntry{
		{AssertionID: "a1", Type: "schema", Status: "pass", Score: 1.0, Cost: 0.01, DurationMS: 10},
		{AssertionID: "a2", Type: "schema", Status: "soft_fail", Score: 0.5, Cost: 0.02, DurationMS: 20},
	}); err != nil {
		t.Fatalf("pin v1.0.0: %v", err)
	}
	if err := store.Pin("v1.1.0", []cache.BaselineEntry{
		{AssertionID: "a1", Type: "schema", Status: "pass", Score: 1.0, Cost: 0.05, DurationMS: 50},
	}); err != nil {
		t.Fatalf("pin v1.1.0: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("List returned %d summaries, want 2", len(summaries))
	}
	// Ordered by pinned_at DESC — v1.1.0 was pinned second.
	if summaries[0].Tag != "v1.1.0" {
		t.Errorf("List[0].Tag = %q, want v1.1.0", summaries[0].Tag)
	}
	if summaries[1].Tag != "v1.0.0" {
		t.Errorf("List[1].Tag = %q, want v1.0.0", summaries[1].Tag)
	}
	if summaries[1].AssertionCount != 2 || summaries[1].Passed != 1 || summaries[1].SoftFailed != 1 {
		t.Errorf("List[1] counts wrong: %+v", summaries[1])
	}
	if summaries[1].TotalCost < 0.029 || summaries[1].TotalCost > 0.031 {
		t.Errorf("List[1].TotalCost = %f, want ~0.03", summaries[1].TotalCost)
	}
	if summaries[1].TotalDurationMS != 30 {
		t.Errorf("List[1].TotalDurationMS = %d, want 30", summaries[1].TotalDurationMS)
	}
}

func TestBaselineStore_Delete(t *testing.T) {
	store := newBaselineStore(t)

	if err := store.Pin("v1", []cache.BaselineEntry{
		{AssertionID: "a1", Status: "pass", Score: 1.0},
		{AssertionID: "a2", Status: "pass", Score: 1.0},
	}); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	n, err := store.Delete("v1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n != 2 {
		t.Errorf("Delete removed %d rows, want 2", n)
	}

	got, err := store.Get("v1")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get after Delete = %d entries, want 0", len(got))
	}

	// Deleting an unknown tag is not an error; rows-affected is zero.
	n, err = store.Delete("never-pinned")
	if err != nil {
		t.Fatalf("Delete unknown: %v", err)
	}
	if n != 0 {
		t.Errorf("Delete unknown rows = %d, want 0", n)
	}

	if _, err := store.Delete(""); err == nil {
		t.Error("Delete with empty tag must error")
	}
}
