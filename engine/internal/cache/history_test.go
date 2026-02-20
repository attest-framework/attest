package cache_test

import (
	"database/sql"
	"math"
	"testing"

	"github.com/attest-ai/attest/engine/internal/cache"
	_ "modernc.org/sqlite"
)

func newTestHistoryStore(t *testing.T) *cache.HistoryStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	return store
}

func TestHistoryStore_RecordAndQueryWindow(t *testing.T) {
	store := newTestHistoryStore(t)

	scores := []float64{0.9, 0.8, 0.7, 0.6, 0.5}
	for _, s := range scores {
		if err := store.Record("trace-1", "assert-1", "constraint", s, "pass"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := store.QueryWindow("assert-1", 5)
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(got) != len(scores) {
		t.Fatalf("QueryWindow returned %d scores, want %d", len(got), len(scores))
	}
	// Most-recent first: inserted in order 0.9→0.5, so returned 0.5→0.9.
	if got[0] != 0.5 {
		t.Errorf("first (most recent) score = %f, want 0.5", got[0])
	}
	if got[len(got)-1] != 0.9 {
		t.Errorf("last (oldest) score = %f, want 0.9", got[len(got)-1])
	}
}

func TestHistoryStore_QueryWindowRespectsLimit(t *testing.T) {
	store := newTestHistoryStore(t)

	for i := 0; i < 10; i++ {
		if err := store.Record("trace-1", "assert-limit", "constraint", float64(i)*0.1, "pass"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := store.QueryWindow("assert-limit", 3)
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("QueryWindow with windowSize=3 returned %d scores, want 3", len(got))
	}
}

func TestHistoryStore_Stats(t *testing.T) {
	store := newTestHistoryStore(t)

	// Insert known values: 0.6, 0.8, 1.0 → mean=0.8, population stddev=sqrt((0.04+0+0.04)/3)
	values := []float64{0.6, 0.8, 1.0}
	for _, v := range values {
		if err := store.Record("trace-1", "assert-stats", "constraint", v, "pass"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	mean, stddev, count, err := store.Stats("assert-stats")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if math.Abs(mean-0.8) > 1e-9 {
		t.Errorf("mean = %f, want 0.8", mean)
	}
	// population stddev = sqrt((0.04+0+0.04)/3) ≈ 0.16329932
	wantStddev := math.Sqrt(0.08 / 3.0)
	if math.Abs(stddev-wantStddev) > 1e-9 {
		t.Errorf("stddev = %f, want %f", stddev, wantStddev)
	}
}

func TestHistoryStore_EmptyHistoryReturnsZeroValues(t *testing.T) {
	store := newTestHistoryStore(t)

	scores, err := store.QueryWindow("nonexistent", 10)
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(scores) != 0 {
		t.Errorf("QueryWindow for unknown ID returned %d scores, want 0", len(scores))
	}

	mean, stddev, count, err := store.Stats("nonexistent")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 0 || mean != 0 || stddev != 0 {
		t.Errorf("Stats for unknown ID = (%f, %f, %d), want (0, 0, 0)", mean, stddev, count)
	}
}

func TestHistoryStore_MultipleAssertionIDsIsolated(t *testing.T) {
	store := newTestHistoryStore(t)

	if err := store.Record("trace-1", "assert-A", "constraint", 0.9, "pass"); err != nil {
		t.Fatalf("Record A: %v", err)
	}
	if err := store.Record("trace-1", "assert-B", "constraint", 0.3, "hard_fail"); err != nil {
		t.Fatalf("Record B: %v", err)
	}

	aScores, err := store.QueryWindow("assert-A", 10)
	if err != nil {
		t.Fatalf("QueryWindow A: %v", err)
	}
	bScores, err := store.QueryWindow("assert-B", 10)
	if err != nil {
		t.Fatalf("QueryWindow B: %v", err)
	}

	if len(aScores) != 1 || aScores[0] != 0.9 {
		t.Errorf("assert-A scores = %v, want [0.9]", aScores)
	}
	if len(bScores) != 1 || bScores[0] != 0.3 {
		t.Errorf("assert-B scores = %v, want [0.3]", bScores)
	}
}
